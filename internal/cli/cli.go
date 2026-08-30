// Copyright 2025 rinorouu
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package cli implements Alih's command-line entry point.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/credentials"
	"alih/internal/report"
	"alih/internal/snapshot"
	"alih/internal/verify"
)

const helpText = `Alih is a local-first SaaS data portability tool.

Usage:
  alih --help
  alih auth
  alih scan [--workspace-id ID]
  alih extract --output PATH [--workspace-id ID]
  alih export --snapshot PATH [--output PATH]
  alih verify --archive PATH
  alih report --archive PATH [--format text|html|json]

Commands:
  auth         Authenticate with ClickUp and list accessible Workspaces
  scan         Inventory one ClickUp Workspace without modifying it
  extract      Save an M3 raw ClickUp API snapshot without creating an archive
  export       Build an unverified M4 portable archive from an M3 snapshot
  verify       Independently verify an existing M4 portable archive
  report       Produce a human-readable recovery report for an archive

M1 manual verification:
  Set ALIH_CLICKUP_TOKEN in the process environment, then run "alih auth".
  A successfully verified token is saved locally for later "alih auth" runs.

Flags:
  -h, --help   Show this help message
`

const authHelpText = `Configure and verify local ClickUp authentication.

Usage:
  alih auth

For initial setup, provide ALIH_CLICKUP_TOKEN in the process environment.
After successful verification, Alih saves it in the user configuration directory.
Later runs load the saved credential when ALIH_CLICKUP_TOKEN is not set.
The token is never accepted as a command-line argument.
`

const scanHelpText = `Inventory one ClickUp Workspace through the official read-only API.

Usage:
  alih scan [--workspace-id ID]

If exactly one Workspace is accessible, it is selected automatically.
If multiple Workspaces are accessible, --workspace-id is required.
The token is loaded from ALIH_CLICKUP_TOKEN or the verified local credential.
Scan does not create an archive or make a portability-completeness claim.
`

const extractHelpText = `Extract raw ClickUp API evidence for one Workspace.

Usage:
  alih extract --output PATH [--workspace-id ID]

PATH must not already exist. A complete traversal creates PATH. A failure or
gracefully interrupted traversal is preserved as PATH.failed and marked FAILED.
An uncatchable termination can leave a private .partial-* directory whose
run.json remains IN_PROGRESS. The token is loaded from ALIH_CLICKUP_TOKEN or
the verified local credential and is never stored in the snapshot.

M3 output contains raw successful JSON responses, a request/failure ledger,
and a source-ID inventory with a logical digest. It is not an M4 portable
archive: it contains no normalized model, SQLite database, manifest, or
downloaded attachment binaries.
`

const exportHelpText = `Build an M4 portable archive from a completed M3 raw snapshot.

Usage:
  alih export --snapshot PATH [--output PATH]

--snapshot must point to a completed M3 extraction. --output defaults to
"alih-export" and must not already exist. The archive contains alih.db,
manifest.json, schema.json, an immutable copy of the M3 raw evidence, and
retrieved attachment binaries.

An archive with unresolved supported attachments is marked INCOMPLETE and the
command exits non-zero. Successful construction is CREATED_UNVERIFIED; M5
verification is not implemented or implied by this command.
`

const verifyHelpText = `Independently verify an existing M4 portable archive.

Usage:
  alih verify --archive PATH [--json]

Verification reads the archive only. It never contacts ClickUp, never modifies
the source, and never writes to the archive under test, so its result describes
internal archive integrity rather than current source state.

It checks archive structure, manifest agreement, recorded file checksums,
SQLite integrity, the archived raw M3 evidence and its checksums, expected
versus archived entity counts, portable identifier derivation, referential
integrity, relationship and hierarchy evidence, attachment existence, size and
checksum, custom-field evidence, and disclosed discrepancies.

Results:
  VERIFIED                    everything in supported scope was proven
  VERIFIED_WITH_LIMITATIONS   proven, with source limitations that remain
  INCOMPLETE                  supported data Alih expected is not archived
  FAILED                      the archive could not be proven intact

INCOMPLETE and FAILED exit non-zero.
`

const reportHelpText = `Produce a recovery report for an existing M4 portable archive.

Usage:
  alih report --archive PATH [--format text|html|json] [--output PATH]

The report restates archived evidence and the result of M5 verification, which
this command runs for you. It contacts nothing, re-reads no source data, and
neither modifies nor repairs the archive.

Formats:
  text   human-readable report on stdout (default)
  html   self-contained report.html; without --output it is written next to
         the archive as PATH.report.html
  json   machine-readable recovery report document on stdout

Use --output - to write any format to stdout.

The report is produced for a FAILED or INCOMPLETE archive too, and says so
plainly. Those results exit non-zero.

Note: PRD section 7 sketches report.html inside the archive directory. Alih
writes it outside instead, because an M4 archive is sealed by manifest
checksums and adding a file to it would make "alih verify" fail.
`

type credentialStore interface {
	Load() (string, error)
	Save(string) error
	Location() (string, error)
}

// Options contains the M1 authentication dependencies and the optional token
// supplied by the process environment for initial setup.
type Options struct {
	Authenticator       connector.Authenticator
	Scanner             connector.Scanner
	Extractor           connector.Extractor
	Exporter            archiveExporter
	Verifier            archiveVerifier
	Reporter            archiveReporter
	CredentialStore     credentialStore
	EnvironmentToken    string
	EnvironmentTokenSet bool
}

type archiveExporter interface {
	Export(context.Context, string, string, string) (archive.Summary, error)
}

type archiveVerifier interface {
	Verify(string) (verify.Report, error)
}

type archiveReporter interface {
	Report(string) (report.Document, error)
}

// App contains the dependencies for the Alih command-line application.
type App struct {
	stdout  io.Writer
	stderr  io.Writer
	logger  *slog.Logger
	options Options
}

// New creates an Alih command-line application.
func New(stdout, stderr io.Writer, logger *slog.Logger, options Options) *App {
	return &App{
		stdout:  stdout,
		stderr:  stderr,
		logger:  logger,
		options: options,
	}
}

// Run executes the CLI with args and returns a process exit code.
func (a *App) Run(args []string) int {
	if len(args) > 0 && args[0] == "auth" {
		return a.runAuth(args[1:])
	}
	if len(args) > 0 && args[0] == "scan" {
		return a.runScan(args[1:])
	}
	if len(args) > 0 && args[0] == "extract" {
		return a.runExtract(args[1:])
	}
	if len(args) > 0 && args[0] == "export" {
		return a.runExport(args[1:])
	}
	if len(args) > 0 && args[0] == "verify" {
		return a.runVerify(args[1:])
	}
	if len(args) > 0 && args[0] == "report" {
		return a.runReport(args[1:])
	}

	flags := flag.NewFlagSet("alih", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() {
		fmt.Fprint(a.stdout, helpText)
	}

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "alih: unknown command %q\n", flags.Arg(0))
		return 2
	}

	// Keep the logger as an explicit application dependency from the start.
	// Commands added in later milestones can use it without relying on a global.
	_ = a.logger

	flags.Usage()
	return 0
}

func (a *App) runReport(args []string) int {
	flags := flag.NewFlagSet("alih report", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() { fmt.Fprint(a.stdout, reportHelpText) }
	archivePath := flags.String("archive", "", "existing M4 archive directory to report on")
	format := flags.String("format", "text", "report format: text, html or json")
	outputPath := flags.String("output", "", "write the report to this path, or - for stdout")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	target, code := singleArchivePath(a.stderr, "report", *archivePath, flags.Args())
	if code != 0 {
		return code
	}
	switch *format {
	case "text", "html", "json":
	default:
		fmt.Fprintf(a.stderr, "alih report: unknown --format %q; use text, html or json\n", displayValue(*format))
		return 2
	}
	if a.options.Reporter == nil {
		fmt.Fprintln(a.stderr, "alih report: reporting dependencies are unavailable")
		return 1
	}
	document, err := a.options.Reporter.Report(target)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih report: %v\n", err)
		fmt.Fprintln(a.stderr, "alih report: no recovery report was produced; the archive was not assessed")
		return 1
	}

	destination := strings.TrimSpace(*outputPath)
	if destination == "" && *format == "html" {
		// An HTML report is a file, not terminal output. It is written beside
		// the archive because writing into a sealed archive would invalidate
		// the manifest checksums that "alih verify" depends on.
		destination = strings.TrimRight(document.Identity.ArchivePath, string(os.PathSeparator)) + ".report.html"
	}
	if destination == "" {
		destination = "-"
	}
	if destination != "-" {
		if inside, err := pathInsideArchive(document.Identity.ArchivePath, destination); err != nil {
			fmt.Fprintf(a.stderr, "alih report: resolve --output: %v\n", err)
			return 1
		} else if inside {
			fmt.Fprintln(a.stderr, "alih report: --output must not be inside the archive; adding a file to a sealed archive would make \"alih verify\" fail")
			return 2
		}
	}

	rendered, err := renderReport(document, *format)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih report: render %s report: %v\n", *format, err)
		return 1
	}
	if destination == "-" {
		if _, err := a.stdout.Write(rendered); err != nil {
			fmt.Fprintf(a.stderr, "alih report: write report: %v\n", err)
			return 1
		}
	} else {
		if err := os.WriteFile(destination, rendered, 0o600); err != nil {
			fmt.Fprintf(a.stderr, "alih report: write report: %v\n", err)
			return 1
		}
		fmt.Fprintf(a.stdout, "Recovery report written: %s\n", destination)
		fmt.Fprintf(a.stdout, "Result: %s\n", document.Conclusion.Result)
		fmt.Fprintf(a.stdout, "%s\n", displayValue(document.Conclusion.Verdict))
	}
	if document.Failed() {
		fmt.Fprintf(a.stderr, "alih report: archive result is %s; this archive is not a proven recovery source\n", document.Conclusion.Result)
		return 1
	}
	return 0
}

func renderReport(document report.Document, format string) ([]byte, error) {
	var buffer bytes.Buffer
	switch format {
	case "html":
		if err := report.RenderHTML(&buffer, document); err != nil {
			return nil, err
		}
	case "json":
		encoded, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return nil, err
		}
		buffer.Write(encoded)
		buffer.WriteByte('\n')
	default:
		if err := report.RenderText(&buffer, document); err != nil {
			return nil, err
		}
	}
	return buffer.Bytes(), nil
}

func pathInsideArchive(archivePath, candidate string) (bool, error) {
	root, err := filepath.Abs(archivePath)
	if err != nil {
		return false, err
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil {
		return false, err
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

// singleArchivePath accepts an archive either as --archive PATH or as one
// positional path, and refuses anything ambiguous.
func singleArchivePath(stderr io.Writer, command, flagValue string, positional []string) (string, int) {
	if len(positional) > 1 {
		fmt.Fprintf(stderr, "alih %s: only one archive path is accepted; use --archive PATH\n", command)
		return "", 2
	}
	target := strings.TrimSpace(flagValue)
	if len(positional) == 1 {
		if target != "" {
			fmt.Fprintf(stderr, "alih %s: pass the archive either as --archive PATH or as a single positional path, not both\n", command)
			return "", 2
		}
		target = strings.TrimSpace(positional[0])
	}
	if target == "" {
		fmt.Fprintf(stderr, "alih %s: --archive PATH is required\n", command)
		return "", 2
	}
	return target, 0
}

func (a *App) runVerify(args []string) int {
	flags := flag.NewFlagSet("alih verify", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() { fmt.Fprint(a.stdout, verifyHelpText) }
	archivePath := flags.String("archive", "", "existing M4 archive directory to verify")
	asJSON := flags.Bool("json", false, "print the machine-readable verification report")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	target, code := singleArchivePath(a.stderr, "verify", *archivePath, flags.Args())
	if code != 0 {
		return code
	}
	if a.options.Verifier == nil {
		fmt.Fprintln(a.stderr, "alih verify: verification dependencies are unavailable")
		return 1
	}
	report, err := a.options.Verifier.Verify(target)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih verify: %v\n", err)
		fmt.Fprintln(a.stderr, "alih verify: verification could not be started; the archive was not proven")
		return 1
	}
	if *asJSON {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(a.stderr, "alih verify: encode report: %v\n", err)
			return 1
		}
		fmt.Fprintf(a.stdout, "%s\n", encoded)
	} else {
		printVerification(a.stdout, report)
	}
	if report.Failed() {
		fmt.Fprintf(a.stderr, "alih verify: archive result is %s; Alih cannot present this archive as verified\n", report.Result)
		return 1
	}
	return 0
}

func printVerification(output io.Writer, report verify.Report) {
	fmt.Fprintln(output, "ALI H — VERIFICATION")
	fmt.Fprintf(output, "\nArchive: %s\n", report.ArchivePath)
	fmt.Fprintf(output, "Connector: %s\n", displayValue(report.Connector))
	fmt.Fprintf(output, "Source workspace: %s (ID: %s)\n", displayValue(report.Source.Name), displayValue(report.Source.ID))
	fmt.Fprintf(output, "Recorded archive status: %s\n", displayValue(report.ArchiveStatus))

	if len(report.Reconciliation) > 0 {
		fmt.Fprintln(output, "\nExpected vs archived")
		for _, entity := range report.Reconciliation {
			fmt.Fprintf(output, "%-16s %6d / %-6d unresolved=%-4d %s\n",
				entity.Entity, entity.Archived, entity.Expected, entity.Unresolved, entity.Status)
		}
	}

	fmt.Fprintln(output, "\nChecks")
	for _, check := range report.Checks {
		fmt.Fprintf(output, "%-32s %-14s %s\n", check.Name, check.Status, displayValue(check.Summary))
		if check.Status == verify.CheckPass {
			continue
		}
		for _, finding := range check.Findings {
			fmt.Fprintf(output, "    - %s\n", displayValue(finding))
		}
	}

	if len(report.Capabilities) > 0 {
		fmt.Fprintln(output, "\nSource capability (preserved unchanged)")
		for _, capability := range report.Capabilities {
			fmt.Fprintf(output, "%-22s %-12s %s\n", displayValue(capability.Name), capability.State, displayValue(capability.Note))
		}
	}

	if len(report.Limitations) > 0 {
		fmt.Fprintln(output, "\nLimitations and unproven claims")
		for _, limitation := range report.Limitations {
			fmt.Fprintf(output, "- %s\n", displayValue(limitation))
		}
	}

	fmt.Fprintf(output, "\nRESULT\n\n%s\n\n", report.Result)
	switch report.Result {
	case verify.ResultVerified:
		fmt.Fprintln(output, "Everything Alih expected within supported scope is archived and provable from this archive.")
	case verify.ResultVerifiedWithLimitations:
		fmt.Fprintln(output, "Supported archived data is provable from this archive; the limitations above remain and were not resolved by verification.")
	case verify.ResultIncomplete:
		fmt.Fprintln(output, "Alih expected supported data that this archive does not contain. This is not a verified archive.")
	default:
		fmt.Fprintln(output, "Alih cannot prove this archive is complete or intact.")
	}
	fmt.Fprintln(output, "Verification read the archive only. No source data modified. No archive data modified.")
}

func (a *App) runExport(args []string) int {
	flags := flag.NewFlagSet("alih export", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() { fmt.Fprint(a.stdout, exportHelpText) }
	snapshotPath := flags.String("snapshot", "", "completed M3 raw snapshot directory")
	outputPath := flags.String("output", "alih-export", "new M4 archive directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(a.stderr, "alih export: positional arguments are not accepted; use --snapshot PATH and --output PATH")
		return 2
	}
	if strings.TrimSpace(*snapshotPath) == "" {
		fmt.Fprintln(a.stderr, "alih export: --snapshot PATH is required")
		return 2
	}
	if strings.TrimSpace(*outputPath) == "" {
		fmt.Fprintln(a.stderr, "alih export: --output PATH cannot be empty")
		return 2
	}
	if a.options.Exporter == nil || a.options.CredentialStore == nil {
		fmt.Fprintln(a.stderr, "alih export: archive dependencies are unavailable")
		return 1
	}
	token, _, err := a.authenticationToken()
	if err != nil {
		fmt.Fprintf(a.stderr, "alih export: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	summary, err := a.options.Exporter.Export(ctx, *snapshotPath, *outputPath, token)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih export: %s\n", safeError(err, token))
		fmt.Fprintln(a.stderr, "alih export: archive creation FAILED; no clean M4 archive was produced")
		if summary.Path != "" {
			fmt.Fprintf(a.stderr, "Failed archive evidence: %s\n", summary.Path)
		}
		return 1
	}
	printArchiveSummary(a.stdout, summary)
	if summary.Status == archive.StatusIncomplete {
		fmt.Fprintln(a.stderr, "alih export: archive is INCOMPLETE because one or more expected supported attachments were unresolved")
		return 1
	}
	if summary.Status != archive.StatusCreatedUnverified {
		fmt.Fprintf(a.stderr, "alih export: unexpected archive status %s\n", summary.Status)
		return 1
	}
	return 0
}

func printArchiveSummary(output io.Writer, summary archive.Summary) {
	fmt.Fprintln(output, "ALI H — PORTABLE ARCHIVE")
	fmt.Fprintf(output, "\nArchive: %s\n", summary.Path)
	fmt.Fprintf(output, "Status: %s\n", summary.Status)
	fmt.Fprintln(output, "Verification: NOT_RUN")
	fmt.Fprintln(output, "\nArchived inventory")
	for _, name := range []string{"spaces", "folders", "lists", "tasks", "subtasks", "comments", "attachments", "custom_fields", "relationships"} {
		count := summary.Inventory[name]
		fmt.Fprintf(output, "%-16s expected=%d archived=%d unresolved=%d\n", name, count.Expected, count.Archived, count.Unresolved)
	}
	fmt.Fprintln(output, "\nObserved portable entities")
	for _, name := range []string{"workspaces", "identities", "record_identity_roles", "record_tags", "record_field_values"} {
		fmt.Fprintf(output, "%-24s %d\n", name, summary.Observed[name])
	}
	fmt.Fprintln(output, "\nFiles: alih.db, manifest.json, schema.json, raw/, attachments/")
	fmt.Fprintln(output, "No M5 verification was performed. No source data modified.")
}

func (a *App) runExtract(args []string) int {
	flags := flag.NewFlagSet("alih extract", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() { fmt.Fprint(a.stdout, extractHelpText) }
	workspaceID := flags.String("workspace-id", "", "ClickUp Workspace ID to extract")
	outputPath := flags.String("output", "", "new directory for M3 raw evidence")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(a.stderr, "alih extract: positional arguments are not accepted; use --output PATH and --workspace-id ID")
		return 2
	}
	if strings.TrimSpace(*outputPath) == "" {
		fmt.Fprintln(a.stderr, "alih extract: --output PATH is required")
		return 2
	}
	if a.options.Authenticator == nil || a.options.Extractor == nil || a.options.CredentialStore == nil {
		fmt.Fprintln(a.stderr, "alih extract: extraction dependencies are unavailable")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	token, shouldSave, err := a.authenticationToken()
	if err != nil {
		fmt.Fprintf(a.stderr, "alih extract: %v\n", err)
		return 1
	}
	authentication, err := a.options.Authenticator.Authenticate(ctx, token)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih extract: %s\n", safeError(err, token))
		return 1
	}
	workspace, err := selectWorkspace(authentication.Workspaces, strings.TrimSpace(*workspaceID))
	if err != nil {
		fmt.Fprintf(a.stderr, "alih extract: %v\n", err)
		return 1
	}
	if shouldSave {
		if err := a.options.CredentialStore.Save(token); err != nil {
			fmt.Fprintf(a.stderr, "alih extract: verified credential could not be saved: %v\n", err)
			return 1
		}
	}

	session, err := snapshot.Begin(*outputPath, a.options.Extractor.Name(), workspace, authentication.Identity, token)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih extract: create raw snapshot: %v\n", err)
		return 1
	}
	result, err := a.options.Extractor.Extract(ctx, token, workspace, session)
	if err != nil {
		return a.failExtraction(session, errors.New(safeError(err, token)), fmt.Sprintf("alih extract: %s", safeError(err, token)))
	}
	if result.Workspace.ID != workspace.ID {
		reason := errors.New("connector returned a different Workspace")
		return a.failExtraction(session, reason, "alih extract: connector returned a different Workspace")
	}
	summary, err := session.Complete(result)
	if err != nil {
		return a.failExtraction(session, err, fmt.Sprintf("alih extract: finalize raw snapshot: %v", err))
	}

	fmt.Fprintln(a.stdout, "ALI H — CLICKUP RAW EXTRACTION")
	fmt.Fprintf(a.stdout, "\nWorkspace: %s (ID: %s)\n", displayValue(workspace.Name), displayValue(workspace.ID))
	fmt.Fprintf(a.stdout, "Snapshot: %s\n", summary.Path)
	fmt.Fprintf(a.stdout, "Logical inventory digest: %s\n", summary.LogicalDigest)
	fmt.Fprintf(a.stdout, "API attempts: %d\n", summary.Requests)
	fmt.Fprintf(a.stdout, "Raw successful responses: %d\n", summary.RawResponses)
	fmt.Fprintf(a.stdout, "Retried attempts: %d\n", summary.RetriedAttempts)
	fmt.Fprintln(a.stdout, "\nRaw extraction complete.")
	fmt.Fprintln(a.stdout, "Limitation: ClickUp does not provide an atomic snapshot; run.json records this extraction as non-atomic.")
	fmt.Fprintln(a.stdout, "No portable model, SQLite database, manifest, or attachment binary was created.")
	fmt.Fprintln(a.stdout, "No source data modified.")
	return 0
}

func (a *App) failExtraction(session *snapshot.Session, reason error, headline string) int {
	failedPath, preserveErr := session.Fail(reason)
	fmt.Fprintln(a.stderr, headline)
	fmt.Fprintln(a.stderr, "alih extract: extraction FAILED; no complete raw snapshot was produced")
	if preserveErr != nil {
		fmt.Fprintf(a.stderr, "alih extract: preserving failure accounting also failed: %v (staging: %s)\n", preserveErr, failedPath)
	} else {
		fmt.Fprintf(a.stderr, "Failed evidence: %s\n", failedPath)
	}
	return 1
}

// safeError renders an error for the operator without trusting its producer to
// have removed the credential. Connectors sanitise their own errors; this is
// the boundary that makes a lapse there harmless.
func safeError(err error, secret string) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	return displayValue(message)
}

func (a *App) runScan(args []string) int {
	flags := flag.NewFlagSet("alih scan", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() { fmt.Fprint(a.stdout, scanHelpText) }
	workspaceID := flags.String("workspace-id", "", "ClickUp Workspace ID to scan")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(a.stderr, "alih scan: positional arguments are not accepted; use --workspace-id ID")
		return 2
	}
	if a.options.Authenticator == nil || a.options.Scanner == nil || a.options.CredentialStore == nil {
		fmt.Fprintln(a.stderr, "alih scan: scan dependencies are unavailable")
		return 1
	}

	token, shouldSave, err := a.authenticationToken()
	if err != nil {
		fmt.Fprintf(a.stderr, "alih scan: %v\n", err)
		return 1
	}
	authentication, err := a.options.Authenticator.Authenticate(context.Background(), token)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih scan: %s\n", safeError(err, token))
		return 1
	}
	workspace, err := selectWorkspace(authentication.Workspaces, strings.TrimSpace(*workspaceID))
	if err != nil {
		fmt.Fprintf(a.stderr, "alih scan: %s\n", safeError(err, token))
		return 1
	}

	result, err := a.options.Scanner.Scan(context.Background(), token, workspace)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih scan: %s\n", safeError(err, token))
		fmt.Fprintln(a.stderr, "alih scan: inventory FAILED; Alih cannot prove this source inventory is complete")
		return 1
	}
	if result.Workspace.ID != workspace.ID {
		fmt.Fprintln(a.stderr, "alih scan: connector returned a different Workspace; inventory FAILED")
		return 1
	}
	if shouldSave {
		if err := a.options.CredentialStore.Save(token); err != nil {
			fmt.Fprintf(a.stderr, "alih scan: scan completed but verified credential could not be saved: %v\n", err)
			return 1
		}
	}

	printScan(a.stdout, result)
	return 0
}

func selectWorkspace(workspaces []connector.Workspace, requestedID string) (connector.Workspace, error) {
	if requestedID != "" {
		for _, workspace := range workspaces {
			if workspace.ID == requestedID {
				return workspace, nil
			}
		}
		return connector.Workspace{}, fmt.Errorf("Workspace ID %q is not accessible to the authenticated user", displayValue(requestedID))
	}
	if len(workspaces) == 0 {
		return connector.Workspace{}, errors.New("ClickUp returned no accessible Workspaces")
	}
	if len(workspaces) > 1 {
		var choices strings.Builder
		for _, workspace := range workspaces {
			fmt.Fprintf(&choices, "\n- %s (ID: %s)", displayValue(workspace.Name), displayValue(workspace.ID))
		}
		return connector.Workspace{}, fmt.Errorf("multiple Workspaces are accessible; rerun with --workspace-id ID:%s", choices.String())
	}
	return workspaces[0], nil
}

func printScan(output io.Writer, result connector.ScanResult) {
	inventory := result.Inventory
	fmt.Fprintln(output, "ALI H — CLICKUP SCAN")
	fmt.Fprintf(output, "\nWorkspace: %s (ID: %s)\n", displayValue(result.Workspace.Name), displayValue(result.Workspace.ID))
	fmt.Fprintln(output, "Scope: data accessible to the authenticated user through ClickUp's official API.")
	fmt.Fprintln(output, "\nHierarchy")
	fmt.Fprintf(output, "Spaces                 %d\n", inventory.Spaces)
	fmt.Fprintf(output, "Folders                %d\n", inventory.Folders)
	fmt.Fprintf(output, "Lists                  %d\n", inventory.Lists)
	fmt.Fprintln(output, "\nContent")
	fmt.Fprintf(output, "Tasks                  %d\n", inventory.Tasks)
	fmt.Fprintf(output, "Subtasks               %d\n", inventory.Subtasks)
	fmt.Fprintf(output, "Task comments          %d\n", inventory.Comments)
	fmt.Fprintf(output, "Task attachments       %d\n", inventory.Attachments)
	fmt.Fprintf(output, "Custom fields          %d\n", inventory.CustomFields)
	fmt.Fprintf(output, "Task relationships     %d\n", inventory.Relationships)
	fmt.Fprintln(output, "\nCapability")
	for _, capability := range result.Capabilities {
		fmt.Fprintf(output, "%-22s %-10s %s\n", displayValue(capability.Name), capability.State, displayValue(capability.Note))
	}
	fmt.Fprintln(output, "\nScan complete.")
	fmt.Fprintln(output, "All supported M2 traversals and pagination completed without unresolved failures.")
	fmt.Fprintln(output, "Limitation: ClickUp does not provide an atomic snapshot; concurrent source changes can affect counts.")
	fmt.Fprintln(output, "No archive or portability-completeness claim was made.")
	fmt.Fprintln(output, "No source data modified.")
}

func (a *App) runAuth(args []string) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(a.stdout, authHelpText)
		return 0
	}
	if len(args) > 0 {
		fmt.Fprintln(a.stderr, "alih auth: command-line arguments are not accepted; use ALIH_CLICKUP_TOKEN for initial setup")
		return 2
	}
	if a.options.Authenticator == nil || a.options.CredentialStore == nil {
		fmt.Fprintln(a.stderr, "alih auth: authentication dependencies are unavailable")
		return 1
	}

	token, shouldSave, err := a.authenticationToken()
	if err != nil {
		fmt.Fprintf(a.stderr, "alih auth: %v\n", err)
		return 1
	}

	result, err := a.options.Authenticator.Authenticate(context.Background(), token)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih auth: %s\n", safeError(err, token))
		return 1
	}

	if shouldSave {
		if err := a.options.CredentialStore.Save(token); err != nil {
			fmt.Fprintf(a.stderr, "alih auth: credential verified but could not be saved: %v\n", err)
			return 1
		}
	}
	location, err := a.options.CredentialStore.Location()
	if err != nil {
		fmt.Fprintf(a.stderr, "alih auth: resolve credential location: %v\n", err)
		return 1
	}

	fmt.Fprintf(a.stdout, "Authenticated with ClickUp as %s (ID: %s)\n\n", displayValue(result.Identity.Name), displayValue(result.Identity.ID))
	if len(result.Workspaces) == 0 {
		fmt.Fprintln(a.stdout, "Accessible Workspaces: none returned by ClickUp.")
	} else {
		fmt.Fprintf(a.stdout, "Accessible Workspaces (%d):\n", len(result.Workspaces))
		for _, workspace := range result.Workspaces {
			name := displayValue(workspace.Name)
			if strings.TrimSpace(name) == "" {
				name = "<unnamed>"
			}
			fmt.Fprintf(a.stdout, "- %s (ID: %s)\n", name, displayValue(workspace.ID))
		}
	}
	fmt.Fprintf(a.stdout, "\nCredential storage: %q (plaintext, protected by permissions 0600)\n", location)
	fmt.Fprintln(a.stdout, "No source data modified.")
	return 0
}

func (a *App) authenticationToken() (token string, shouldSave bool, err error) {
	if a.options.EnvironmentTokenSet {
		if err := credentials.ValidateToken(a.options.EnvironmentToken); err != nil {
			return "", false, err
		}
		return a.options.EnvironmentToken, true, nil
	}

	token, err = a.options.CredentialStore.Load()
	if errors.Is(err, credentials.ErrNotConfigured) {
		return "", false, errors.New("no ClickUp credential configured; set ALIH_CLICKUP_TOKEN for the initial auth run")
	}
	if err != nil {
		return "", false, fmt.Errorf("load credential: %w", err)
	}
	return token, false, nil
}

func displayValue(value string) string {
	return strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, value)
}
