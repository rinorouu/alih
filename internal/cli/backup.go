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

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"alih/internal/archive"
	"alih/internal/snapshot"
	"alih/internal/verify"
)

const (
	backupArchiveDirectory = "archive"
	backupReportFilename   = "recovery-report.html"
)

func (a *App) runBackup(args []string) int {
	flags := flag.NewFlagSet("alih backup", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() { fmt.Fprint(a.stdout, backupHelpText) }
	workspaceID := flags.String("workspace-id", "", "ClickUp Workspace ID to back up")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(a.stderr, "alih backup: positional arguments are not accepted; use --workspace-id ID")
		return 2
	}
	if a.options.Authenticator == nil || a.options.Scanner == nil || a.options.Extractor == nil ||
		a.options.Exporter == nil || a.options.Verifier == nil || a.options.Reporter == nil || a.options.CredentialStore == nil {
		fmt.Fprintln(a.stderr, "alih backup: backup dependencies are unavailable")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	token, shouldSave, err := a.authenticationToken()
	if err != nil {
		return a.backupFailure("authentication", err, "", token)
	}
	authentication, err := a.options.Authenticator.Authenticate(ctx, token)
	if err != nil {
		return a.backupFailure("authentication", err, "", token)
	}
	workspace, err := selectWorkspace(authentication.Workspaces, strings.TrimSpace(*workspaceID))
	if err != nil {
		return a.backupFailure("workspace selection", err, "", token)
	}
	if shouldSave {
		if err := a.options.CredentialStore.Save(token); err != nil {
			return a.backupFailure("authentication", fmt.Errorf("verified credential could not be saved: %w", err), "", token)
		}
	}

	root, err := a.backupRoot()
	if err != nil {
		return a.backupFailure("setup", err, "", token)
	}
	startedAt := time.Now().UTC()
	if a.options.Now != nil {
		startedAt = a.options.Now().UTC()
	}
	workspaceComponent := safeWorkspaceComponent(safeText(workspace.Name, token), safeText(workspace.ID, token))
	finalRoot := filepath.Join(root, workspaceComponent, startedAt.Format("2006-01-02T150405Z"))
	if _, err := os.Lstat(finalRoot); err == nil {
		return a.backupFailure("setup", fmt.Errorf("backup directory already exists: %s", finalRoot), "", token)
	} else if !errors.Is(err, os.ErrNotExist) {
		return a.backupFailure("setup", fmt.Errorf("inspect backup directory: %w", err), "", token)
	}
	if err := os.MkdirAll(filepath.Dir(finalRoot), 0o700); err != nil {
		return a.backupFailure("setup", fmt.Errorf("create backup parent directory: %w", err), "", token)
	}
	workingRoot, err := os.MkdirTemp(filepath.Dir(finalRoot), "."+filepath.Base(finalRoot)+".partial-")
	if err != nil {
		return a.backupFailure("setup", fmt.Errorf("create private working directory: %w", err), "", token)
	}
	if err := os.Chmod(workingRoot, 0o700); err != nil {
		return a.failBackupWorkingState("setup", err, workingRoot, finalRoot, token)
	}

	fmt.Fprintln(a.stdout, "ALIH — CLICKUP BACKUP")
	fmt.Fprintf(a.stdout, "\nWorkspace: %s (ID: %s)\n\n", safeText(workspace.Name, token), safeText(workspace.ID, token))

	if err := ctx.Err(); err != nil {
		return a.failBackupWorkingState("scan", err, workingRoot, finalRoot, token)
	}
	fmt.Fprintln(a.stdout, "Scanning workspace...")
	scanResult, err := a.options.Scanner.Scan(ctx, token, workspace)
	if err != nil {
		return a.failBackupWorkingState("scan", err, workingRoot, finalRoot, token)
	}
	if scanResult.Workspace.ID != workspace.ID {
		return a.failBackupWorkingState("scan", errors.New("connector returned a different Workspace"), workingRoot, finalRoot, token)
	}

	fmt.Fprintln(a.stdout, "Extracting source data...")
	snapshotPath := filepath.Join(workingRoot, "snapshot")
	session, err := snapshot.Begin(snapshotPath, a.options.Extractor.Name(), workspace, authentication.Identity, token)
	if err != nil {
		return a.failBackupWorkingState("extract", err, workingRoot, finalRoot, token)
	}
	extraction, err := a.options.Extractor.Extract(ctx, token, workspace, session)
	if err != nil {
		_, preserveErr := session.Fail(errors.New(safeError(err, token)))
		if preserveErr != nil {
			err = fmt.Errorf("%v; preserve failed extraction evidence: %w", err, preserveErr)
		}
		return a.failBackupWorkingState("extract", err, workingRoot, finalRoot, token)
	}
	if extraction.Workspace.ID != workspace.ID {
		err = errors.New("connector returned a different Workspace")
		_, _ = session.Fail(err)
		return a.failBackupWorkingState("extract", err, workingRoot, finalRoot, token)
	}
	if _, err := session.Complete(extraction); err != nil {
		_, _ = session.Fail(err)
		return a.failBackupWorkingState("extract", err, workingRoot, finalRoot, token)
	}

	fmt.Fprintln(a.stdout, "Building portable archive...")
	archivePath := filepath.Join(workingRoot, backupArchiveDirectory)
	summary, err := a.options.Exporter.Export(ctx, snapshotPath, archivePath, token)
	if err != nil {
		return a.failBackupWorkingState("export", err, workingRoot, finalRoot, token)
	}
	if summary.Status != archive.StatusCreatedUnverified {
		return a.failBackupWorkingState("export", fmt.Errorf("archive result is %s", displayValue(summary.Status)), workingRoot, finalRoot, token)
	}

	fmt.Fprintln(a.stdout, "Verifying archive...")
	verification, err := a.options.Verifier.Verify(archivePath)
	if err != nil {
		return a.failBackupWorkingState("verification", err, workingRoot, finalRoot, token)
	}
	if !successfulVerification(verification.Result) {
		return a.failBackupWorkingState("verification", fmt.Errorf("archive result is %s", displayValue(verification.Result)), workingRoot, finalRoot, token)
	}

	fmt.Fprintln(a.stdout, "Generating recovery report...")
	document, err := a.options.Reporter.Report(archivePath)
	if err != nil {
		return a.failBackupWorkingState("report", err, workingRoot, finalRoot, token)
	}
	if !successfulVerification(document.Conclusion.Result) {
		return a.failBackupWorkingState("report", fmt.Errorf("recovery report result is %s", displayValue(document.Conclusion.Result)), workingRoot, finalRoot, token)
	}
	if document.Conclusion.Result != verification.Result {
		return a.failBackupWorkingState("report", fmt.Errorf("verification result changed from %s to %s", verification.Result, document.Conclusion.Result), workingRoot, finalRoot, token)
	}
	// The archive will move together with the report when the private bundle is
	// finalized. Record the user-visible final path, not the staging path.
	document.Identity.ArchivePath = filepath.Join(finalRoot, backupArchiveDirectory)
	rendered, err := renderReport(document, "html")
	if err != nil {
		return a.failBackupWorkingState("report", err, workingRoot, finalRoot, token)
	}
	if err := os.WriteFile(filepath.Join(workingRoot, backupReportFilename), rendered, 0o600); err != nil {
		return a.failBackupWorkingState("report", fmt.Errorf("write recovery report: %w", err), workingRoot, finalRoot, token)
	}
	if err := ctx.Err(); err != nil {
		return a.failBackupWorkingState("report", err, workingRoot, finalRoot, token)
	}
	if err := os.Rename(workingRoot, finalRoot); err != nil {
		return a.failBackupWorkingState("finalization", fmt.Errorf("publish completed backup: %w", err), workingRoot, finalRoot, token)
	}

	finalArchive := filepath.Join(finalRoot, backupArchiveDirectory)
	finalReport := filepath.Join(finalRoot, backupReportFilename)
	fmt.Fprintln(a.stdout, "\nALIH — BACKUP COMPLETE")
	fmt.Fprintf(a.stdout, "\nWorkspace: %s\n", safeText(workspace.Name, token))
	fmt.Fprintf(a.stdout, "Status: %s\n", verification.Result)
	fmt.Fprintf(a.stdout, "\nArchive:\n%s\n", finalArchive)
	fmt.Fprintf(a.stdout, "\nRecovery report:\n%s\n", finalReport)
	fmt.Fprintln(a.stdout, "\nYour ClickUp data was not modified.")
	return 0
}

func successfulVerification(result string) bool {
	return result == verify.ResultVerified || result == verify.ResultVerifiedWithLimitations
}

func (a *App) backupRoot() (string, error) {
	if strings.TrimSpace(a.options.BackupRoot) != "" {
		return filepath.Abs(a.options.BackupRoot)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Alih"), nil
}

func safeWorkspaceComponent(name, id string) string {
	component := filesystemComponent(name)
	if component == "" {
		component = filesystemComponent("workspace-" + id)
	}
	if component == "" {
		return "workspace"
	}
	return component
}

func filesystemComponent(value string) string {
	var output strings.Builder
	separator := false
	count := 0
	for _, character := range strings.TrimSpace(value) {
		if count >= 80 {
			break
		}
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_' {
			output.WriteRune(character)
			separator = false
			count++
			continue
		}
		if output.Len() > 0 && !separator {
			output.WriteByte('-')
			separator = true
			count++
		}
	}
	return strings.Trim(output.String(), "-_. ")
}

func safeText(value, token string) string {
	if token != "" {
		value = strings.ReplaceAll(value, token, "[REDACTED]")
	}
	return displayValue(value)
}

func (a *App) backupFailure(stage string, err error, workingRoot, token string) int {
	fmt.Fprintf(a.stderr, "alih backup: %s stage FAILED: %s\n", stage, safeError(err, token))
	if workingRoot != "" {
		fmt.Fprintf(a.stderr, "Failed working state: %s\n", workingRoot)
	}
	fmt.Fprintln(a.stderr, "Backup was not completed. Your ClickUp data was not modified.")
	return 1
}

func (a *App) failBackupWorkingState(stage string, err error, workingRoot, finalRoot, token string) int {
	failedPath := finalRoot + ".failed"
	if _, statErr := os.Lstat(failedPath); errors.Is(statErr, os.ErrNotExist) {
		if renameErr := os.Rename(workingRoot, failedPath); renameErr == nil {
			workingRoot = failedPath
		}
	}
	return a.backupFailure(stage, err, workingRoot, token)
}
