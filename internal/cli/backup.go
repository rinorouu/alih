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
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/oplock"
	"alih/internal/snapshot"
	"alih/internal/state"
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
	destination := flags.String("destination", "", "absolute local backup root (default: ~/Alih)")
	scheduleID := flags.String("schedule-id", "", "schedule identity supplied by Alih's native scheduler")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(a.stderr, "alih backup: positional arguments are not accepted; use --workspace-id ID and --destination PATH")
		return 2
	}
	if strings.TrimSpace(*scheduleID) != "" && !validLocalID(strings.TrimSpace(*scheduleID)) {
		fmt.Fprintln(a.stderr, "alih backup: --schedule-id may contain only letters, digits, hyphen, and underscore")
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
		return a.backupFailure(nil, "authentication", state.StageAuthenticate, err, "", token)
	}
	authentication, err := a.options.Authenticator.Authenticate(ctx, token)
	if err != nil {
		return a.backupFailure(nil, "authentication", state.StageAuthenticate, err, "", token)
	}
	workspace, err := selectWorkspace(authentication.Workspaces, strings.TrimSpace(*workspaceID))
	if err != nil {
		return a.backupFailure(nil, "workspace selection", state.StageAuthenticate, err, "", token)
	}
	if shouldSave {
		// The credential has already been verified against the source, so being
		// unable to cache it locally says nothing about whether this backup can
		// succeed. A read-only or ephemeral installation must not lose its
		// backup over a convenience it never asked for.
		if err := a.saveVerifiedCredential(token); err != nil {
			fmt.Fprintf(a.stderr, "alih backup: the verified credential could not be saved: %v\n", err)
			fmt.Fprintln(a.stderr, "alih backup: continuing; this run uses the credential already supplied")
		}
	}

	root, err := a.backupRootFor(*destination)
	if err != nil {
		// Without a resolved destination there is no scope to record against.
		return a.backupFailure(nil, "setup", state.StagePrepare, err, "", token)
	}
	startedAt := time.Now().UTC()
	if a.options.Now != nil {
		startedAt = a.options.Now().UTC()
	}
	workspaceComponent := safeWorkspaceComponent(safeText(workspace.Name, token), safeText(workspace.ID, token))
	finalRoot := filepath.Join(root, workspaceComponent, startedAt.Format("2006-01-02T150405Z"))

	recorder := a.beginOperation(operationStart{
		ctx:           ctx,
		scheduleID:    strings.TrimSpace(*scheduleID),
		operation:     state.OperationBackup,
		scope:         backupScope(a.options.Extractor.Name(), safeText(workspace.ID, token), root),
		startedAt:     startedAt,
		workspaceName: safeText(workspace.Name, token),
		identity: connector.Identity{
			ID:   safeText(authentication.Identity.ID, token),
			Name: safeText(authentication.Identity.Name, token),
		},
	})
	lockRoot := filepath.Join(root, ".alih-locks")
	if strings.TrimSpace(a.options.LockRoot) != "" {
		lockRoot, err = filepath.Abs(a.options.LockRoot)
		if err != nil {
			return a.backupFailure(recorder, "operation lock", state.StagePrepare,
				fmt.Errorf("resolve operation lock storage: %w", err), "", token)
		}
	}
	operationID := recorder.attempt.OperationID
	if operationID == "" {
		operationID, err = state.NewOperationID(startedAt, a.options.Entropy)
		if err != nil {
			return a.backupFailure(recorder, "operation lock", state.StagePrepare, err, "", token)
		}
	}
	operationLock, err := oplock.Acquire(lockRoot, recorder.scope, operationID, a.recordedVersion(), startedAt)
	if err != nil {
		if errors.Is(err, oplock.ErrHeld) {
			return a.backupOverlap(recorder, err, token)
		}
		return a.backupFailure(recorder, "operation lock", state.StagePrepare, err, "", token)
	}
	defer func() {
		if err := operationLock.Release(); err != nil {
			fmt.Fprintf(a.stderr, "alih: operation lock could not be released cleanly: %s\n", safeError(err, ""))
		}
	}()
	if _, err := os.Lstat(finalRoot); err == nil {
		return a.backupFailure(recorder, "setup", state.StagePrepare, fmt.Errorf("backup directory already exists: %s", finalRoot), "", token)
	} else if !errors.Is(err, os.ErrNotExist) {
		return a.backupFailure(recorder, "setup", state.StagePrepare, fmt.Errorf("inspect backup directory: %w", err), "", token)
	}
	if err := os.MkdirAll(filepath.Dir(finalRoot), 0o700); err != nil {
		return a.backupFailure(recorder, "setup", state.StagePrepare, fmt.Errorf("create backup parent directory: %w", err), "", token)
	}
	workingRoot, err := os.MkdirTemp(filepath.Dir(finalRoot), "."+filepath.Base(finalRoot)+".partial-")
	if err != nil {
		return a.backupFailure(recorder, "setup", state.StagePrepare, fmt.Errorf("create private working directory: %w", err), "", token)
	}
	if err := os.Chmod(workingRoot, 0o700); err != nil {
		return a.failBackupWorkingState(recorder, "setup", state.StagePrepare, err, workingRoot, finalRoot, token)
	}

	fmt.Fprintln(a.stdout, "ALIH — CLICKUP BACKUP")
	if recorder.attempt.ScheduleID != "" {
		fmt.Fprintf(a.stdout, "\nSchedule: %s\n", recorder.attempt.ScheduleID)
	}
	fmt.Fprintf(a.stdout, "\nWorkspace: %s (ID: %s)\n\n", safeText(workspace.Name, token), safeText(workspace.ID, token))

	if err := ctx.Err(); err != nil {
		return a.failBackupWorkingState(recorder, "scan", state.StageScan, err, workingRoot, finalRoot, token)
	}
	fmt.Fprintln(a.stdout, "Scanning workspace...")
	scanResult, err := a.options.Scanner.Scan(ctx, token, workspace)
	if err != nil {
		return a.failBackupWorkingState(recorder, "scan", state.StageScan, err, workingRoot, finalRoot, token)
	}
	if scanResult.Workspace.ID != workspace.ID {
		return a.failBackupWorkingState(recorder, "scan", state.StageScan, errors.New("connector returned a different Workspace"), workingRoot, finalRoot, token)
	}

	fmt.Fprintln(a.stdout, "Extracting source data...")
	snapshotPath := filepath.Join(workingRoot, "snapshot")
	session, err := snapshot.BeginWithOptions(snapshotPath, a.options.Extractor.Name(), workspace,
		authentication.Identity, snapshot.Options{AlihVersion: a.recordedVersion()}, token)
	if err != nil {
		return a.failBackupWorkingState(recorder, "extract", state.StageExtract, err, workingRoot, finalRoot, token)
	}
	extraction, err := a.options.Extractor.Extract(ctx, token, workspace, session)
	if err != nil {
		_, preserveErr := session.Fail(err)
		if preserveErr != nil {
			err = fmt.Errorf("%w; preserve failed extraction evidence: %v", err, preserveErr)
		}
		return a.failBackupWorkingState(recorder, "extract", state.StageExtract, err, workingRoot, finalRoot, token)
	}
	if extraction.Workspace.ID != workspace.ID {
		err = errors.New("connector returned a different Workspace")
		_, _ = session.Fail(err)
		return a.failBackupWorkingState(recorder, "extract", state.StageExtract, err, workingRoot, finalRoot, token)
	}
	if _, err := session.Complete(extraction); err != nil {
		_, _ = session.Fail(err)
		return a.failBackupWorkingState(recorder, "extract", state.StageExtract, err, workingRoot, finalRoot, token)
	}

	fmt.Fprintln(a.stdout, "Building portable archive...")
	archivePath := filepath.Join(workingRoot, backupArchiveDirectory)
	summary, err := a.options.Exporter.Export(ctx, snapshotPath, archivePath, token)
	if err != nil {
		return a.failBackupWorkingState(recorder, "export", state.StageExport, err, workingRoot, finalRoot, token)
	}
	if summary.Status != archive.StatusCreatedUnverified {
		return a.failBackupWorkingState(recorder, "export", state.StageExport, fmt.Errorf("archive result is %s", displayValue(summary.Status)), workingRoot, finalRoot, token)
	}

	fmt.Fprintln(a.stdout, "Verifying archive...")
	verification, err := a.options.Verifier.Verify(archivePath)
	if err != nil {
		return a.failBackupWorkingState(recorder, "verification", state.StageVerify, err, workingRoot, finalRoot, token)
	}
	if !successfulVerification(verification.Result) {
		return a.failBackupWorkingState(recorder, "verification", state.StageVerify, fmt.Errorf("archive result is %s", displayValue(verification.Result)), workingRoot, finalRoot, token)
	}

	fmt.Fprintln(a.stdout, "Generating recovery report...")
	document, err := a.options.Reporter.Report(archivePath)
	if err != nil {
		return a.failBackupWorkingState(recorder, "report", state.StageReport, err, workingRoot, finalRoot, token)
	}
	if !successfulVerification(document.Conclusion.Result) {
		return a.failBackupWorkingState(recorder, "report", state.StageReport, fmt.Errorf("recovery report result is %s", displayValue(document.Conclusion.Result)), workingRoot, finalRoot, token)
	}
	if document.Conclusion.Result != verification.Result {
		return a.failBackupWorkingState(recorder, "report", state.StageReport, fmt.Errorf("verification result changed from %s to %s", verification.Result, document.Conclusion.Result), workingRoot, finalRoot, token)
	}
	// The archive will move together with the report when the private bundle is
	// finalized. Record the user-visible final path, not the staging path.
	document.Identity.ArchivePath = filepath.Join(finalRoot, backupArchiveDirectory)
	rendered, err := renderReport(document, "html")
	if err != nil {
		return a.failBackupWorkingState(recorder, "report", state.StageReport, err, workingRoot, finalRoot, token)
	}
	if err := os.WriteFile(filepath.Join(workingRoot, backupReportFilename), rendered, 0o600); err != nil {
		return a.failBackupWorkingState(recorder, "report", state.StageReport, fmt.Errorf("write recovery report: %w", err), workingRoot, finalRoot, token)
	}
	if err := ctx.Err(); err != nil {
		return a.failBackupWorkingState(recorder, "report", state.StageReport, err, workingRoot, finalRoot, token)
	}
	// Publication must never consume something that is already there. POSIX
	// refuses to rename a directory onto an existing file, but Windows renames
	// with MOVEFILE_REPLACE_EXISTING and would delete a user's file to make
	// room for the bundle. Checking first makes the refusal explicit and
	// identical on every platform.
	if _, err := os.Lstat(finalRoot); err == nil {
		return a.failBackupWorkingState(recorder, "finalization", state.StageFinalize,
			fmt.Errorf("publish completed backup: %s already exists", finalRoot), workingRoot, finalRoot, token)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return a.failBackupWorkingState(recorder, "finalization", state.StageFinalize,
			fmt.Errorf("publish completed backup: %w", err), workingRoot, finalRoot, token)
	}
	if err := os.Rename(workingRoot, finalRoot); err != nil {
		return a.failBackupWorkingState(recorder, "finalization", state.StageFinalize, fmt.Errorf("publish completed backup: %w", err), workingRoot, finalRoot, token)
	}

	finalArchive := filepath.Join(finalRoot, backupArchiveDirectory)
	finalReport := filepath.Join(finalRoot, backupReportFilename)
	completed := operationResult{
		archivePath: finalArchive, reportPath: finalReport,
		assessment:              summary.OperationalAssessment,
		capabilitySchemaVersion: verification.CapabilitySchemaVersion,
		capabilities:            verification.Capabilities,
	}
	// The archive's own manifest correctly keeps recording verification as
	// NOT_RUN, so the verification this run performed is preserved outside it,
	// anchored to the published archive by the checksum of its sealed manifest.
	if identity, err := archiveIdentity(finalArchive); err != nil {
		a.warnState(err)
	} else {
		record := a.verificationRecord(verification, identity)
		completed.verification = &record
	}
	recorder.succeed(state.StageFinalize, completed)
	fmt.Fprintln(a.stdout, "\nALIH — BACKUP COMPLETE")
	fmt.Fprintf(a.stdout, "\nWorkspace: %s\n", safeText(workspace.Name, token))
	fmt.Fprintf(a.stdout, "Status: %s\n", verification.Result)
	fmt.Fprintf(a.stdout, "\nArchive:\n%s\n", finalArchive)
	fmt.Fprintf(a.stdout, "\nRecovery report:\n%s\n", finalReport)
	fmt.Fprintln(a.stdout, "\nYour ClickUp data was not modified.")
	return 0
}

func (a *App) backupOverlap(recorder *operationState, err error, token string) int {
	reason := "OPERATION_OVERLAP"
	recorder.skip(state.StagePrepare, reason)
	fmt.Fprintf(a.stderr, "alih backup: skipped: %s\n", safeError(err, token))
	fmt.Fprintln(a.stderr, "Another backup for the same connector, Workspace, and destination is active. Nothing was queued.")
	fmt.Fprintln(a.stderr, "Backup was not completed. Your ClickUp data was not modified.")
	return 1
}

func validLocalID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9', character == '-', character == '_':
		default:
			return false
		}
	}
	return true
}

func successfulVerification(result string) bool {
	return result == verify.ResultVerified || result == verify.ResultVerifiedWithLimitations
}

func (a *App) backupRoot() (string, error) {
	return a.backupRootFor("")
}

func (a *App) backupRootFor(override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		if !filepath.IsAbs(strings.TrimSpace(override)) {
			return "", errors.New("backup destination must be an absolute path")
		}
		return filepath.Clean(strings.TrimSpace(override)), nil
	}
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

func (a *App) backupFailure(recorder *operationState, stage, stateStage string, err error, workingRoot, token string) int {
	// State is recorded before the failure is printed so that an operator who
	// reads status after an interrupted run sees the same stage they were told.
	recorder.fail(stateStage, err, workingRoot)
	fmt.Fprintf(a.stderr, "alih backup: %s stage FAILED: %s\n", stage, safeError(err, token))
	a.writeErrorAssessment(a.stderr, err)
	if workingRoot != "" {
		fmt.Fprintf(a.stderr, "Failed working state: %s\n", workingRoot)
	}
	fmt.Fprintln(a.stderr, "Backup was not completed. Your ClickUp data was not modified.")
	return 1
}

func (a *App) failBackupWorkingState(recorder *operationState, stage, stateStage string, err error, workingRoot, finalRoot, token string) int {
	failedPath := finalRoot + ".failed"
	if _, statErr := os.Lstat(failedPath); errors.Is(statErr, os.ErrNotExist) {
		if renameErr := os.Rename(workingRoot, failedPath); renameErr == nil {
			workingRoot = failedPath
		}
	}
	return a.backupFailure(recorder, stage, stateStage, err, workingRoot, token)
}
