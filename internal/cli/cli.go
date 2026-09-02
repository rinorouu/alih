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
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"alih/internal/archive"
	"alih/internal/config"
	"alih/internal/connector"
	"alih/internal/credentials"
	"alih/internal/event"
	"alih/internal/notify"
	"alih/internal/organize"
	"alih/internal/report"
	"alih/internal/schedule"
	"alih/internal/snapshot"
	"alih/internal/state"
	"alih/internal/verify"
)

const helpText = `ALIH creates and verifies local, portable SaaS backups.
Each supported SaaS is reached through its own official read-only API.

Usage:
  alih [--connector NAME] <command> [options]
  alih --help
  alih --version

Commands:
  version      Print the ALIH version
  auth         Authenticate with the selected source and list its Workspaces
  backup       Create and verify a portable backup of one Workspace
  scan         Inventory one Workspace without modifying it
  extract      Save an M3 raw API snapshot without creating an archive
  export       Build an unverified M4 portable archive from an M3 snapshot
  verify       Independently verify an existing M4 portable archive
  report       Produce a human-readable recovery report for an archive
  status       Report what ALIH has recorded about its own local operations
  notify       Check notification configuration or explicitly replay an event
  schedule     Preview and manage native recurring backup schedules
  organize     Build a safe browsing view from a verified archive

Get started:
  1. Set the token for the connector you are using, named after it:
     ALIH_CLICKUP_TOKEN for ClickUp, ALIH_NOTION_TOKEN for Notion.
  2. Run "alih auth" to verify and save the credential locally.
  3. Optionally run "alih scan" to inspect the accessible Workspace.
  4. Run "alih backup" to create and verify a backup.

Connectors:
  --connector NAME   Choose the source. Defaults to clickup, so existing
                     commands keep working unchanged. Credentials, backups
                     and status are recorded separately per connector.

Run "alih <command> --help" for command-specific options.

Flags:
  -h, --help   Show this help message
  --version    Print the ALIH version
`

const statusHelpText = `Report what ALIH has recorded about its own operations.

Usage:
  alih status [--json] [--refresh] [--reconcile]

Status reads local records only and makes no source request. It reports the
last attempt, the last successful backup, the recorded verification and whether
the archive it refers to is still unchanged, plus the connector health and
authentication observed at the time, each with its own observation age. An
observation older than 24 hours is marked stale: it is what was true then, not
a statement about now. --refresh makes exactly one authentication request and
updates only the scopes that request covers.

Status also summarises the local event history for each scope: how many
transitions were recorded, how many failed, and the most recent one. That
history is context only. It never decides the status, so damaged or missing
history leaves the reported status and exit code unchanged.

--reconcile reads the backup destination itself, independently verifies every
archive it finds there, and records what that verification proves. It is how a
backup that ALIH has no record of becomes visible again. Which source an
archive belongs to is read from the archive's own sealed manifest, never from a
directory name; a preserved .failed run or an abandoned working directory is
reported but never counted as a backup; and an archive that cannot be verified
now is reported and left alone.

--json prints one stable document on standard output; diagnostics stay on
standard error.

Exit codes:
  0  every recorded scope is healthy
  1  at least one scope needs attention
  2  usage error
  3  nothing is recorded, or nothing recorded can be called healthy
  4  local state exists but cannot be read; ALIH never rewrites it
`

const versionHelpText = `Print the ALIH version.

Usage:
  alih version
`

const backupHelpText = `ALIH creates a verified portable ClickUp backup.

Usage:
  alih backup [--workspace-id ID] [--destination PATH]

If exactly one Workspace is accessible, it is selected automatically.
If multiple Workspaces are accessible, --workspace-id is required.
--destination selects an absolute local backup root. The default remains
~/Alih. Alih never writes a credential into that path or into scheduled command
arguments.

The default backup directory is ~/Alih/<workspace>/<UTC-start-time>/ and is
never overwritten. It contains the sealed M4 archive in archive/ and the
English Recovery Report in recovery-report.html. Keeping the report beside the
sealed archive preserves independent M5 verification of the archive contents.

Only VERIFIED and VERIFIED_WITH_LIMITATIONS are successful backup results.
INCOMPLETE, FAILED, a failed stage, or an interruption exits non-zero and is
never presented as a completed backup. ALIH uses ClickUp's official read-only
API and does not modify source data.
`

const authHelpText = `Configure and verify local ClickUp authentication.

Usage:
  alih auth

For initial setup, provide ALIH_CLICKUP_TOKEN in the process environment.
After successful verification, ALIH saves it in the user configuration directory.
Later runs load the saved credential when ALIH_CLICKUP_TOKEN is not set.
The token is never accepted as a command-line argument.
`

const scanHelpText = `Inventory one ClickUp Workspace through the official read-only API.

Usage:
  alih scan [--workspace-id ID] [--json]

If exactly one Workspace is accessible, it is selected automatically.
If multiple Workspaces are accessible, --workspace-id is required.
The token is loaded from ALIH_CLICKUP_TOKEN or the verified local credential.
Scan does not create an archive or make a portability-completeness claim.
--json prints a stable machine-readable scan and operational assessment.
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
  INCOMPLETE                  supported data ALIH expected is not archived
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

Note: PRD section 7 sketches report.html inside the archive directory. ALIH
writes it outside instead, because an M4 archive is sealed by manifest
checksums and adding a file to it would make "alih verify" fail.
`

// credentialStore is Core's view of credential persistence. Every call names
// the connector it concerns, so Core never holds "the" credential and a second
// connector cannot overwrite the first.
type credentialStore interface {
	Load(connectorName string) (string, error)
	Save(connectorName, secret string) error
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
	// SaveEnvironmentCredential reports whether a credential taken from the
	// environment may also be written to the local credential store. The
	// application entry point sets it from ALIH_SAVE_CREDENTIAL; the zero value
	// is deliberately false so an embedded caller persists nothing it did not
	// ask to persist.
	SaveEnvironmentCredential bool
	// Version is the release identity supplied by the application entry point.
	// An empty value is rendered as dev for local development builds.
	Version string
	// BackupRoot overrides ~/Alih for tests or an explicitly embedded caller.
	// The command-level --destination flag takes precedence when present.
	BackupRoot string
	// LockRoot overrides where cross-process operation locks live. By default
	// they live in a private hidden directory under the backup destination, so
	// all Alih installations targeting that destination share the same lock.
	LockRoot string
	// Now supplies the backup-run start instant used only in the directory name.
	// Archive provenance continues to come from the M3/M4 implementations.
	Now func() time.Time
	// StateRoot overrides the local operational state directory. An empty value
	// resolves to the user configuration directory, beside the credential store.
	StateRoot string
	// Entropy supplies the random part of an operation ID. An empty value uses
	// the system source; tests inject a deterministic reader.
	Entropy io.Reader
	// EventSink overrides where operational events are recorded. An empty value
	// writes the bounded local log beside the operational state.
	EventSink event.Sink
	// NotificationRoot overrides the directory containing notifications.json.
	// It is separate from StateRoot because production configuration lives one
	// level above the state files; tests inject both explicitly.
	NotificationRoot string
	// Notifier overrides the one webhook transport. An empty value uses the
	// bounded HTTPS implementation; tests inject an in-memory notifier.
	Notifier notify.Notifier
	// ScheduleRoot overrides the directory containing schedules.json.
	ScheduleRoot string
	// ScheduleRunner injects native scheduler process execution for tests.
	ScheduleRunner schedule.Runner
	Organizer      organizedViewBuilder
	// These values make native plan generation deterministic in tests. Empty
	// values resolve from the current process.
	SchedulePlatform string
	ExecutablePath   string
	UserHome         string
	UserID           string
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

type organizedViewBuilder interface {
	Build(context.Context, string, string) (organize.Result, error)
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
	if len(args) > 0 && args[0] == "version" {
		return a.runVersion(args[1:])
	}
	if len(args) > 0 && args[0] == "auth" {
		return a.runAuth(args[1:])
	}
	if len(args) > 0 && args[0] == "backup" {
		return a.runBackup(args[1:])
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
	if len(args) > 0 && args[0] == "status" {
		return a.runStatus(args[1:])
	}
	if len(args) > 0 && args[0] == "notify" {
		return a.runNotify(args[1:])
	}
	if len(args) > 0 && args[0] == "schedule" {
		return a.runSchedule(args[1:])
	}
	if len(args) > 0 && args[0] == "organize" {
		return a.runOrganize(args[1:])
	}

	flags := flag.NewFlagSet("alih", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() {
		fmt.Fprint(a.stdout, helpText)
	}
	showVersion := flags.Bool("version", false, "print the ALIH version")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		if flags.NArg() > 0 {
			fmt.Fprintln(a.stderr, "alih: --version cannot be combined with commands or arguments")
			return 2
		}
		a.printVersion()
		return 0
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

func (a *App) runVersion(args []string) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(a.stdout, versionHelpText)
		return 0
	}
	if len(args) > 0 {
		fmt.Fprintln(a.stderr, "alih version: arguments are not accepted")
		return 2
	}
	a.printVersion()
	return 0
}

func (a *App) printVersion() {
	version := strings.TrimSpace(a.options.Version)
	if version == "" {
		version = "dev"
	}
	fmt.Fprintf(a.stdout, "alih %s\n", displayValue(version))
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
	// A verification result belongs to the archive, not to the run that asked
	// for it: it is recorded whether it passed or failed, and only for an
	// archive Alih already knows about.
	a.recordVerification(target, report)
	if report.Failed() {
		fmt.Fprintf(a.stderr, "alih verify: archive result is %s; ALIH cannot present this archive as verified\n", report.Result)
		return 1
	}
	return 0
}

func printVerification(output io.Writer, report verify.Report) {
	fmt.Fprintln(output, "ALIH — VERIFICATION")
	fmt.Fprintf(output, "\nArchive: %s\n", report.ArchivePath)
	fmt.Fprintf(output, "Connector: %s\n", displayValue(report.Connector))
	fmt.Fprintf(output, "Source workspace: %s (ID: %s)\n", displayValue(report.Source.Name), displayValue(report.Source.ID))
	fmt.Fprintf(output, "Recorded archive status: %s\n", displayValue(report.ArchiveStatus))
	if report.OperationalAssessment != nil && connector.ValidateOperationalAssessment(*report.OperationalAssessment) == nil {
		fmt.Fprintln(output, "\nRecorded operational assessment")
		_ = connector.WriteOperationalAssessmentText(output, *report.OperationalAssessment)
	}

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
			if report.CapabilitySchemaVersion == connector.CapabilitySchemaVersion {
				fmt.Fprintf(output, "%-22s %-12s required=%-8s availability=%-11s id=%s  %s\n",
					displayValue(capability.Name), capability.State, capability.Requirement, capability.Availability,
					capability.ID, displayValue(capability.Note))
			} else {
				fmt.Fprintf(output, "%-22s %-12s %s\n", displayValue(capability.Name), capability.State, displayValue(capability.Note))
			}
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
		fmt.Fprintln(output, "Everything ALIH expected within supported scope is archived and provable from this archive.")
	case verify.ResultVerifiedWithLimitations:
		fmt.Fprintln(output, "Supported archived data is provable from this archive; the limitations above remain and were not resolved by verification.")
	case verify.ResultIncomplete:
		fmt.Fprintln(output, "ALIH expected supported data that this archive does not contain. This is not a verified archive.")
	default:
		fmt.Fprintln(output, "ALIH cannot prove this archive is complete or intact.")
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
	fmt.Fprintln(output, "ALIH — PORTABLE ARCHIVE")
	fmt.Fprintf(output, "\nArchive: %s\n", summary.Path)
	fmt.Fprintf(output, "Status: %s\n", summary.Status)
	if summary.OperationalAssessment != nil && connector.ValidateOperationalAssessment(*summary.OperationalAssessment) == nil {
		fmt.Fprintln(output, "\nOperational assessment")
		_ = connector.WriteOperationalAssessmentText(output, *summary.OperationalAssessment)
	}
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
	workspace, err := selectWorkspace(authentication.Workspaces, strings.TrimSpace(*workspaceID), a.connectorDisplayName())
	if err != nil {
		fmt.Fprintf(a.stderr, "alih extract: %v\n", err)
		return 1
	}
	if shouldSave {
		if err := a.saveVerifiedCredential(token); err != nil {
			fmt.Fprintf(a.stderr, "alih extract: the verified credential could not be saved: %v\n", err)
			fmt.Fprintln(a.stderr, "alih extract: continuing; the credential in the environment is still valid for this run")
		}
	}

	// The destination of an extraction is the path the operator chose, so state
	// is scoped to that path rather than to the backup root they did not use.
	destination, err := absolutePath(*outputPath)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih extract: %v\n", err)
		return 1
	}
	recorder := a.beginOperation(operationStart{
		ctx:           ctx,
		operation:     state.OperationExtract,
		scope:         backupScope(a.options.Extractor.Name(), safeText(workspace.ID, token), destination),
		startedAt:     a.observedAt(),
		workspaceName: safeText(workspace.Name, token),
		identity: connector.Identity{
			ID:   safeText(authentication.Identity.ID, token),
			Name: safeText(authentication.Identity.Name, token),
		},
	})

	session, err := snapshot.BeginWithOptions(*outputPath, a.options.Extractor.Name(), workspace,
		authentication.Identity, snapshot.Options{AlihVersion: a.recordedVersion()}, token)
	if err != nil {
		recorder.fail(state.StagePrepare, err, "")
		fmt.Fprintf(a.stderr, "alih extract: create raw snapshot: %v\n", err)
		return 1
	}
	result, err := a.options.Extractor.Extract(ctx, token, workspace, session)
	if err != nil {
		return a.failExtraction(recorder, session, err, fmt.Sprintf("alih extract: %s", safeError(err, token)))
	}
	if result.Workspace.ID != workspace.ID {
		reason := errors.New("connector returned a different Workspace")
		return a.failExtraction(recorder, session, reason, "alih extract: connector returned a different Workspace")
	}
	summary, err := session.Complete(result)
	if err != nil {
		return a.failExtraction(recorder, session, err, fmt.Sprintf("alih extract: finalize raw snapshot: %v", err))
	}
	// A raw snapshot is not an archive, so this run can never become the last
	// known successful backup; it only records that the attempt succeeded.
	assessment := result.Assessment
	recorder.succeed(state.StageExtract, operationResult{
		assessment:              &assessment,
		capabilitySchemaVersion: result.CapabilitySchemaVersion,
		capabilities:            result.Capabilities,
	})

	fmt.Fprintln(a.stdout, "ALIH — CLICKUP RAW EXTRACTION")
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

func (a *App) failExtraction(recorder *operationState, session *snapshot.Session, reason error, headline string) int {
	failedPath, preserveErr := session.Fail(reason)
	recorder.fail(state.StageExtract, reason, failedPath)
	fmt.Fprintln(a.stderr, headline)
	a.writeErrorAssessment(a.stderr, reason)
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
	asJSON := flags.Bool("json", false, "print the machine-readable scan result")
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
		if *asJSON {
			a.writeScanFailureJSON(err, token, nil)
			return 1
		}
		fmt.Fprintf(a.stderr, "alih scan: %s\n", safeError(err, token))
		a.writeErrorAssessment(a.stderr, err)
		return 1
	}
	workspace, err := selectWorkspace(authentication.Workspaces, strings.TrimSpace(*workspaceID), a.connectorDisplayName())
	if err != nil {
		if *asJSON {
			a.writeScanFailureJSON(err, token, validAssessmentPointer(authentication.Assessment))
			return 1
		}
		fmt.Fprintf(a.stderr, "alih scan: %s\n", safeError(err, token))
		return 1
	}

	result, err := a.options.Scanner.Scan(context.Background(), token, workspace)
	if err != nil {
		if *asJSON {
			a.writeScanFailureJSON(err, token, nil)
			return 1
		}
		fmt.Fprintf(a.stderr, "alih scan: %s\n", safeError(err, token))
		a.writeErrorAssessment(a.stderr, err)
		fmt.Fprintln(a.stderr, "alih scan: inventory FAILED; ALIH cannot prove this source inventory is complete")
		return 1
	}
	if result.Workspace.ID != workspace.ID {
		fmt.Fprintln(a.stderr, "alih scan: connector returned a different Workspace; inventory FAILED")
		return 1
	}
	if shouldSave {
		if err := a.saveVerifiedCredential(token); err != nil {
			fmt.Fprintf(a.stderr, "alih scan: the scan completed but the verified credential could not be saved: %v\n", err)
		}
	}

	if *asJSON {
		if err := writeScanJSON(a.stdout, result); err != nil {
			fmt.Fprintf(a.stderr, "alih scan: encode result: %v\n", err)
			return 1
		}
	} else {
		printScan(a.stdout, result)
	}
	return 0
}

type scanDocument struct {
	SchemaVersion           int                              `json:"schema_version"`
	Kind                    string                           `json:"kind"`
	Status                  string                           `json:"status"`
	Connector               string                           `json:"connector"`
	Workspace               *connector.Workspace             `json:"workspace,omitempty"`
	Inventory               *connector.Inventory             `json:"inventory,omitempty"`
	CapabilitySchemaVersion int                              `json:"capability_schema_version,omitempty"`
	Capabilities            []connector.Capability           `json:"capabilities,omitempty"`
	OperationalAssessment   *connector.OperationalAssessment `json:"operational_assessment,omitempty"`
	Error                   string                           `json:"error,omitempty"`
}

func writeScanJSON(output io.Writer, result connector.ScanResult) error {
	assessment := validAssessmentPointer(result.Assessment)
	if assessment == nil {
		return errors.New("connector returned no valid operational assessment")
	}
	document := scanDocument{
		SchemaVersion: 1, Kind: "connector_scan", Status: "COMPLETE", Connector: assessment.Health.Connector,
		Workspace: &result.Workspace, Inventory: &result.Inventory,
		CapabilitySchemaVersion: result.CapabilitySchemaVersion,
		Capabilities:            connector.CanonicalCapabilities(result.CapabilitySchemaVersion, result.Capabilities),
		OperationalAssessment:   assessment,
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(true)
	return encoder.Encode(document)
}

// connectorName is the connector this build was wired with. It is empty only
// when no connector is available, which no command that reports one reaches.
func (a *App) connectorName() string {
	if a.options.Scanner != nil {
		return a.options.Scanner.Name()
	}
	if a.options.Authenticator != nil {
		return a.options.Authenticator.Name()
	}
	if a.options.Extractor != nil {
		return a.options.Extractor.Name()
	}
	// "alih export" wires no source adapter because it reads a snapshot rather
	// than a provider, yet it still has to name the credential variable when
	// authentication is missing. The exporter answers from the adapter it was
	// given, and answers nothing when it holds more than one.
	if namer, ok := a.options.Exporter.(interface{ Connector() string }); ok && namer != nil {
		return namer.Connector()
	}
	return ""
}

func (a *App) writeScanFailureJSON(err error, secret string, fallback *connector.OperationalAssessment) {
	assessment, ok := connector.AssessmentFromError(err, a.observedAt())
	if !ok && fallback != nil {
		assessment, ok = *fallback, true
	}
	// The connector comes from the wiring rather than a literal, so a failure
	// document cannot attribute a run to a connector that did not produce it.
	document := scanDocument{
		SchemaVersion: 1, Kind: "connector_scan", Status: "FAILED",
		Connector: a.connectorName(), Error: safeError(err, secret),
	}
	if ok {
		document.OperationalAssessment = &assessment
		// Typed assessments deliberately contain only bounded Alih-owned text;
		// do not duplicate provider-controlled API messages into JSON.
		document.Error = assessment.Health.Message
	}
	encoder := json.NewEncoder(a.stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(true)
	if encodeErr := encoder.Encode(document); encodeErr != nil {
		fmt.Fprintf(a.stderr, "alih scan: encode failure: %v\n", encodeErr)
	}
}

func validAssessmentPointer(assessment connector.OperationalAssessment) *connector.OperationalAssessment {
	if err := connector.ValidateOperationalAssessment(assessment); err != nil {
		return nil
	}
	connector.CanonicalizeOperationalAssessment(&assessment)
	return &assessment
}

func (a *App) observedAt() time.Time {
	if a.options.Now != nil {
		return a.options.Now().UTC()
	}
	return time.Now().UTC()
}

func (a *App) writeErrorAssessment(output io.Writer, err error) {
	if assessment, ok := connector.AssessmentFromError(err, a.observedAt()); ok {
		_ = connector.WriteOperationalAssessmentText(output, assessment)
	}
}

func selectWorkspace(workspaces []connector.Workspace, requestedID, connectorDisplayName string) (connector.Workspace, error) {
	if requestedID != "" {
		for _, workspace := range workspaces {
			if workspace.ID == requestedID {
				return workspace, nil
			}
		}
		return connector.Workspace{}, fmt.Errorf("Workspace ID %q is not accessible to the authenticated user", displayValue(requestedID))
	}
	if len(workspaces) == 0 {
		return connector.Workspace{}, fmt.Errorf("%s returned no accessible Workspaces", connectorDisplayName)
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
	fmt.Fprintln(output, "ALIH — CLICKUP SCAN")
	fmt.Fprintf(output, "\nWorkspace: %s (ID: %s)\n", displayValue(result.Workspace.Name), displayValue(result.Workspace.ID))
	fmt.Fprintln(output, "Scope: data accessible to the authenticated user through ClickUp's official API.")
	if connector.ValidateOperationalAssessment(result.Assessment) == nil {
		fmt.Fprintln(output, "\nOperational assessment")
		_ = connector.WriteOperationalAssessmentText(output, result.Assessment)
	}
	// The hierarchy is printed in the connector's own words, which arrive with
	// the inventory rather than being known here.
	fmt.Fprintln(output, "\nHierarchy")
	for _, kind := range sortedKinds(inventory.ContainerKinds) {
		fmt.Fprintf(output, "%-22s %d\n", displayValue(titleCase(kind)), inventory.ContainerKinds[kind])
	}
	fmt.Fprintf(output, "%-22s %d\n", "Collections", inventory.Collections)
	fmt.Fprintln(output, "\nContent")
	for _, kind := range sortedKinds(inventory.RecordKinds) {
		fmt.Fprintf(output, "%-22s %d\n", displayValue(titleCase(kind)), inventory.RecordKinds[kind])
	}
	fmt.Fprintf(output, "%-22s %d\n", "Records", inventory.Records)
	fmt.Fprintf(output, "%-22s %d\n", "Comments", inventory.Comments)
	fmt.Fprintf(output, "%-22s %d\n", "Attachments", inventory.Attachments)
	fmt.Fprintf(output, "%-22s %d\n", "Custom fields", inventory.CustomFields)
	fmt.Fprintf(output, "%-22s %d\n", "Relationships", inventory.Relationships)
	fmt.Fprintln(output, "\nCapability")
	for _, capability := range result.Capabilities {
		if result.CapabilitySchemaVersion == connector.CapabilitySchemaVersion {
			fmt.Fprintf(output, "%-22s %-10s required=%-8s availability=%-11s id=%s  %s\n",
				displayValue(capability.Name), capability.State, capability.Requirement, capability.Availability,
				capability.ID, displayValue(capability.Note))
		} else {
			fmt.Fprintf(output, "%-22s %-10s %s\n", displayValue(capability.Name), capability.State, displayValue(capability.Note))
		}
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

	token, _, err := a.authenticationToken()
	if err != nil {
		fmt.Fprintf(a.stderr, "alih auth: %v\n", err)
		return 1
	}

	result, err := a.options.Authenticator.Authenticate(context.Background(), token)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih auth: %s\n", safeError(err, token))
		return 1
	}

	// "alih auth" exists in order to save. Running it is itself the request, so
	// it saves an environment credential regardless of the persistence setting
	// that governs the incidental saving other commands do, and a failure to
	// save is a failure of the command rather than a warning.
	if a.options.EnvironmentTokenSet {
		if err := a.saveVerifiedCredential(token); err != nil {
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
	protection := "plaintext, protected by permissions 0600"
	if runtime.GOOS == "windows" {
		protection = "plaintext, stored under your Windows user profile"
	}
	fmt.Fprintf(a.stdout, "\nCredential storage: %q (%s)\n", location, protection)
	fmt.Fprintln(a.stdout, "No source data modified.")
	return 0
}

// saveVerifiedCredential writes a credential that has just been verified. It is
// one seam so every caller agrees on what saving means; whether a failure to
// save is fatal is each command's own decision, because only "alih auth" exists
// in order to save.
func (a *App) saveVerifiedCredential(token string) error {
	if a.options.CredentialStore == nil {
		return errors.New("no credential store is available")
	}
	return a.options.CredentialStore.Save(a.connectorName(), token)
}

func (a *App) authenticationToken() (token string, shouldSave bool, err error) {
	if a.options.EnvironmentTokenSet {
		if err := credentials.ValidateToken(a.options.EnvironmentToken); err != nil {
			return "", false, err
		}
		return a.options.EnvironmentToken, a.options.SaveEnvironmentCredential, nil
	}

	token, err = a.options.CredentialStore.Load(a.connectorName())
	if errors.Is(err, credentials.ErrNotConfigured) {
		return "", false, fmt.Errorf("Alih is not authenticated. Set %s in your environment, then run %q",
			config.CredentialEnvironmentVariable(a.connectorName()), "alih auth")
	}
	if err != nil {
		return "", false, fmt.Errorf("load credential: %w", err)
	}
	return token, false, nil
}

// sortedKinds orders a connector's own vocabulary so that scan output is
// stable without Core knowing any of the names.
func sortedKinds(counts map[string]int) []string {
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// titleCase presents a connector's kind name as a column heading. It is
// presentation only; the stored vocabulary is never altered.
func titleCase(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return value
	}
	upper := []rune(strings.ToUpper(string(runes[0])))
	if len(upper) > 0 {
		runes[0] = upper[0]
	}
	return string(runes) + "s"
}

func displayValue(value string) string {
	return strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, value)
}

// connectorDisplayName is how the wired connector names itself to a person.
// It follows connectorName's resolution order so the two never disagree, and
// falls back to the identifier when an adapter offers no display name, so Core
// never has to supply a provider name of its own.
func (a *App) connectorDisplayName() string {
	for _, candidate := range []any{a.options.Scanner, a.options.Authenticator} {
		namer, ok := candidate.(interface{ DisplayName() string })
		if !ok || namer == nil {
			continue
		}
		if name := strings.TrimSpace(namer.DisplayName()); name != "" {
			return name
		}
	}
	return a.connectorName()
}
