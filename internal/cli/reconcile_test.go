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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"alih/internal/state"
)

// backedUpDestination runs one real backup through the harness and then throws
// its state away, leaving exactly the situation reconciliation exists for: a
// destination full of evidence that Alih has no record of.
func backedUpDestination(t *testing.T, result string) (*backupHarness, string) {
	t.Helper()
	h := newBackupHarness(t, result)
	h.app.options.StateRoot = filepath.Join(t.TempDir(), "discarded-state")
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%q", code, h.stderr.String())
	}
	if err := os.RemoveAll(h.app.options.StateRoot); err != nil {
		t.Fatalf("discard state: %v", err)
	}
	return h, filepath.Join(t.TempDir(), "state")
}

func TestReconcileRebuildsStateForABackupAlihForgot(t *testing.T) {
	t.Parallel()
	h, stateRoot := backedUpDestination(t, "VERIFIED")
	app, stdout, stderr := statusApp(t, stateRoot, Options{Verifier: h.verifier, BackupRoot: h.root})

	if code := app.Run([]string{"status", "--reconcile"}); code != statusExitHealthy {
		t.Fatalf("code=%d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	archivePath := filepath.Join(h.finalRoot(), backupArchiveDirectory)
	if !strings.Contains(stdout.String(), "recorded this archive as the last successful backup") {
		t.Fatalf("reconciliation did not report what it recorded:\n%s", stdout.String())
	}

	record := loadOnlyRecord(t, stateRoot)
	// The scope came from the archive's own manifest, not from its directory.
	if record.Scope.Connector != "clickup" || record.Scope.WorkspaceID != "100" || record.Scope.Destination != h.root {
		t.Fatalf("scope = %#v", record.Scope)
	}
	if record.WorkspaceName != "Example Workspace" {
		t.Fatalf("workspace name = %q", record.WorkspaceName)
	}
	if record.LastSuccess == nil || record.LastSuccess.ArchivePath != archivePath {
		t.Fatalf("last success = %#v", record.LastSuccess)
	}
	if record.LastSuccess.ReportPath != filepath.Join(h.finalRoot(), backupReportFilename) {
		t.Fatalf("recovery report = %q", record.LastSuccess.ReportPath)
	}
	if record.LastVerification == nil || record.LastVerification.Result != "VERIFIED" {
		t.Fatalf("verification = %#v", record.LastVerification)
	}
	if condition := state.InspectArchive(record.LastVerification.Archive); condition != state.ArchivePresent {
		t.Fatalf("archive condition = %s", condition)
	}
}

func TestReconcilingTwiceNeverInventsASecondRun(t *testing.T) {
	t.Parallel()
	h, stateRoot := backedUpDestination(t, "VERIFIED")
	app, _, _ := statusApp(t, stateRoot, Options{Verifier: h.verifier, BackupRoot: h.root})

	if code := app.Run([]string{"status", "--reconcile"}); code != statusExitHealthy {
		t.Fatalf("first reconcile code=%d", code)
	}
	first := loadOnlyRecord(t, stateRoot)
	if code := app.Run([]string{"status", "--reconcile"}); code != statusExitHealthy {
		t.Fatalf("second reconcile code=%d", code)
	}
	second := loadOnlyRecord(t, stateRoot)

	if second.LastSuccess.OperationID != first.LastSuccess.OperationID {
		t.Fatalf("operation id changed from %q to %q", first.LastSuccess.OperationID, second.LastSuccess.OperationID)
	}
	if !second.LastSuccess.EndedAt.Equal(*first.LastSuccess.EndedAt) {
		t.Fatal("reconciling again moved the recorded completion time")
	}
}

func TestReconcileNeverCountsFailedOrAbandonedWorkAsABackup(t *testing.T) {
	t.Parallel()
	h, stateRoot := backedUpDestination(t, "VERIFIED")
	failed := h.finalRoot() + ".failed"
	if err := os.MkdirAll(filepath.Join(failed, backupArchiveDirectory), 0o700); err != nil {
		t.Fatalf("create failed run: %v", err)
	}
	// A preserved failure can contain a sealed archive; it is still not a backup.
	content, err := os.ReadFile(filepath.Join(h.finalRoot(), backupArchiveDirectory, state.ManifestFilename))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(failed, backupArchiveDirectory, state.ManifestFilename), content, 0o600); err != nil {
		t.Fatalf("write failed manifest: %v", err)
	}
	abandoned := filepath.Join(h.root, "Example-Workspace", ".2026-08-30T123000Z.partial-1234")
	if err := os.MkdirAll(filepath.Join(abandoned, backupArchiveDirectory), 0o700); err != nil {
		t.Fatalf("create abandoned run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(abandoned, backupArchiveDirectory, state.ManifestFilename), content, 0o600); err != nil {
		t.Fatalf("write abandoned manifest: %v", err)
	}

	app, stdout, _ := statusApp(t, stateRoot, Options{Verifier: h.verifier, BackupRoot: h.root})
	if code := app.Run([]string{"status", "--reconcile"}); code != statusExitHealthy {
		t.Fatalf("code=%d:\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Preserved failed run; it is not a backup.") ||
		!strings.Contains(stdout.String(), "Abandoned working directory; it is not a backup.") {
		t.Fatalf("reconciliation did not report the working states:\n%s", stdout.String())
	}
	record := loadOnlyRecord(t, stateRoot)
	if record.LastSuccess == nil || record.LastSuccess.ArchivePath != filepath.Join(h.finalRoot(), backupArchiveDirectory) {
		t.Fatalf("last success = %#v", record.LastSuccess)
	}
	if strings.Contains(record.LastSuccess.ArchivePath, ".failed") ||
		strings.Contains(record.LastSuccess.ArchivePath, ".partial-") {
		t.Fatal("failed or abandoned work was recorded as a successful backup")
	}
}

func TestReconcileRecordsAFailingArchiveWithoutCallingItASuccess(t *testing.T) {
	t.Parallel()
	h, stateRoot := backedUpDestination(t, "VERIFIED")
	h.verifier.result = "INCOMPLETE"
	app, stdout, _ := statusApp(t, stateRoot, Options{Verifier: h.verifier, BackupRoot: h.root})

	if code := app.Run([]string{"status", "--reconcile"}); code != statusExitAttention {
		t.Fatalf("code=%d, want %d:\n%s", code, statusExitAttention, stdout.String())
	}
	record := loadOnlyRecord(t, stateRoot)
	if record.LastVerification == nil || record.LastVerification.Result != "INCOMPLETE" {
		t.Fatalf("verification = %#v", record.LastVerification)
	}
	if record.LastSuccess != nil {
		t.Fatal("an archive that does not verify was recorded as a successful backup")
	}
	if !strings.Contains(stdout.String(), "the last verification result was INCOMPLETE") {
		t.Fatalf("status did not explain the result:\n%s", stdout.String())
	}
}

func TestReconcileLeavesUnprovableArchivesAlone(t *testing.T) {
	t.Parallel()

	t.Run("verification cannot run", func(t *testing.T) {
		t.Parallel()
		h, stateRoot := backedUpDestination(t, "VERIFIED")
		h.verifier.err = errors.New("verification could not start")
		app, stdout, _ := statusApp(t, stateRoot, Options{Verifier: h.verifier, BackupRoot: h.root})
		if code := app.Run([]string{"status", "--reconcile"}); code != statusExitUnknown {
			t.Fatalf("code=%d, want %d:\n%s", code, statusExitUnknown, stdout.String())
		}
		if !strings.Contains(stdout.String(), "it could not be verified now, so nothing about it was recorded") {
			t.Fatalf("reconciliation did not explain the skip:\n%s", stdout.String())
		}
		records, _, err := stateStoreAt(t, stateRoot).List()
		if err != nil {
			t.Fatalf("list state: %v", err)
		}
		if len(records) != 0 {
			t.Fatalf("an unprovable archive produced state: %#v", records)
		}
	})

	t.Run("manifest cannot be read", func(t *testing.T) {
		t.Parallel()
		h, stateRoot := backedUpDestination(t, "VERIFIED")
		manifest := filepath.Join(h.finalRoot(), backupArchiveDirectory, state.ManifestFilename)
		if err := os.WriteFile(manifest, []byte("{ not a manifest"), 0o600); err != nil {
			t.Fatalf("corrupt manifest: %v", err)
		}
		app, stdout, _ := statusApp(t, stateRoot, Options{Verifier: h.verifier, BackupRoot: h.root})
		if code := app.Run([]string{"status", "--reconcile"}); code != statusExitUnknown {
			t.Fatalf("code=%d, want %d:\n%s", code, statusExitUnknown, stdout.String())
		}
		if !strings.Contains(stdout.String(), "its sealed manifest could not be read") {
			t.Fatalf("reconciliation did not report the unreadable manifest:\n%s", stdout.String())
		}
		records, _, err := stateStoreAt(t, stateRoot).List()
		if err != nil {
			t.Fatalf("list state: %v", err)
		}
		if len(records) != 0 {
			t.Fatalf("an unreadable archive produced state: %#v", records)
		}
	})
}

func TestReconcileNeverReplacesANewerRecordedSuccess(t *testing.T) {
	t.Parallel()
	h, stateRoot := backedUpDestination(t, "VERIFIED")
	scope := backupScope("clickup", "100", h.root)
	newer := alphaTestTime.Add(48 * time.Hour)
	newerArchive := filepath.Join(h.root, "Example-Workspace", "2026-09-01T123000Z", backupArchiveDirectory)
	if _, err := stateStoreAt(t, stateRoot).Update(scope, func(record *state.Record) error {
		ended := newer
		record.WorkspaceName = "Example Workspace"
		record.UpdatedAt = newer
		record.LastSuccess = &state.Attempt{
			OperationID: "20260901T123000Z-0000feed", Operation: state.OperationBackup,
			Stage: state.StageFinalize, Outcome: state.OutcomeSucceeded,
			StartedAt: newer.Add(-time.Minute), EndedAt: &ended, ArchivePath: newerArchive,
		}
		record.LastAttempt = record.LastSuccess
		return nil
	}); err != nil {
		t.Fatalf("seed newer success: %v", err)
	}

	app, _, _ := statusApp(t, stateRoot, Options{Verifier: h.verifier, BackupRoot: h.root})
	app.Run([]string{"status", "--reconcile"})

	record := loadOnlyRecord(t, stateRoot)
	if record.LastSuccess == nil || record.LastSuccess.ArchivePath != newerArchive {
		t.Fatalf("an older archive replaced the newer recorded success: %#v", record.LastSuccess)
	}
	// The older archive still contributed what it can prove: a verification.
	if record.LastVerification == nil ||
		record.LastVerification.Archive.Path != filepath.Join(h.finalRoot(), backupArchiveDirectory) {
		t.Fatalf("verification = %#v", record.LastVerification)
	}
}

func TestReconcileNeverFollowsASymbolicLinkOutOfTheDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic link creation is not universally available on Windows")
	}
	t.Parallel()
	h, stateRoot := backedUpDestination(t, "VERIFIED")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(outside, backupArchiveDirectory), 0o700); err != nil {
		t.Fatalf("create outside archive: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(h.finalRoot(), backupArchiveDirectory, state.ManifestFilename))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, backupArchiveDirectory, state.ManifestFilename), content, 0o600); err != nil {
		t.Fatalf("write outside manifest: %v", err)
	}
	link := filepath.Join(h.root, "linked-elsewhere")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	app, stdout, _ := statusApp(t, stateRoot, Options{Verifier: h.verifier, BackupRoot: h.root})
	app.Run([]string{"status", "--reconcile"})

	if strings.Contains(stdout.String(), outside) {
		t.Fatalf("reconciliation followed a symbolic link out of the destination:\n%s", stdout.String())
	}
	record := loadOnlyRecord(t, stateRoot)
	if strings.HasPrefix(record.LastSuccess.ArchivePath, outside) {
		t.Fatal("an archive outside the destination was recorded through a link")
	}
}

func TestReconcileReportsAnEmptyDestinationWithoutInventingState(t *testing.T) {
	t.Parallel()
	stateRoot := filepath.Join(t.TempDir(), "state")
	empty := filepath.Join(t.TempDir(), "Alih")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatalf("create destination: %v", err)
	}
	verifier := &backupVerifier{recorder: &backupRecorder{}, result: "VERIFIED"}
	app, stdout, _ := statusApp(t, stateRoot, Options{Verifier: verifier, BackupRoot: empty})

	if code := app.Run([]string{"status", "--reconcile"}); code != statusExitUnknown {
		t.Fatalf("code=%d, want %d:\n%s", code, statusExitUnknown, stdout.String())
	}
	if !strings.Contains(stdout.String(), "No archive, failed run, or working directory was found there.") {
		t.Fatalf("reconciliation did not report an empty destination:\n%s", stdout.String())
	}
	records, _, err := stateStoreAt(t, stateRoot).List()
	if err != nil {
		t.Fatalf("list state: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("an empty destination produced state: %#v", records)
	}
}

func TestStatusDoesNotReadTheDestinationUnlessAsked(t *testing.T) {
	t.Parallel()
	h, stateRoot := backedUpDestination(t, "VERIFIED")
	app, stdout, _ := statusApp(t, stateRoot, Options{Verifier: h.verifier, BackupRoot: h.root})
	before := countCalls(h.recorder.calls, "verify")

	if code := app.Run([]string{"status"}); code != statusExitUnknown {
		t.Fatalf("code=%d, want %d", code, statusExitUnknown)
	}
	if strings.Contains(stdout.String(), "Reconciled destination") {
		t.Fatalf("status walked the destination without being asked:\n%s", stdout.String())
	}
	// The only verification that has ever run is the one the backup performed.
	if after := countCalls(h.recorder.calls, "verify"); after != before {
		t.Fatalf("status verified %d archives without being asked", after-before)
	}
}

func countCalls(calls []string, name string) int {
	count := 0
	for _, call := range calls {
		if call == name {
			count++
		}
	}
	return count
}

func TestReconcileNeverInventsHealthAnArchiveDoesNotRecord(t *testing.T) {
	t.Parallel()
	h, stateRoot := backedUpDestination(t, "VERIFIED")
	manifestPath := filepath.Join(h.finalRoot(), backupArchiveDirectory, state.ManifestFilename)
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	// An archive written before Alih recorded operational assessments.
	delete(manifest, "operational_assessment")
	stripped, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, stripped, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	app, stdout, _ := statusApp(t, stateRoot, Options{Verifier: h.verifier, BackupRoot: h.root})
	if code := app.Run([]string{"status", "--reconcile"}); code != statusExitUnknown {
		t.Fatalf("code=%d, want %d:\n%s", code, statusExitUnknown, stdout.String())
	}
	record := loadOnlyRecord(t, stateRoot)
	if record.LastSuccess == nil || record.LastVerification == nil {
		t.Fatalf("what the archive does prove was not recorded: %#v", record)
	}
	if record.Assessment != nil {
		t.Fatalf("health was invented for an archive that records none: %#v", record.Assessment)
	}
	if !strings.Contains(stdout.String(), "Connector health: not recorded") {
		t.Fatalf("status did not say the health is unknown:\n%s", stdout.String())
	}
}
