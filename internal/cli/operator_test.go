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
	"strings"
	"testing"

	"alih/internal/event"
	"alih/internal/state"
	"alih/internal/verify"
)

// These tests answer the Stage 10 question: can somebody operating an Alih
// installation on another person's behalf do every part of the job with the
// same public commands, stable JSON, and local files a self-managed user has?
//
// Each test is written as the operator's task, not as a unit of code, because
// the thing under review is the operator's workflow rather than any one
// function.

// operatorInstallation is one customer environment: its own configuration
// directory, its own destination, its own state, and its own recorded history.
type operatorInstallation struct {
	harness    *backupHarness
	stateRoot  string
	configRoot string
}

func newOperatorInstallation(t *testing.T, workspaceName string) *operatorInstallation {
	t.Helper()
	h := newBackupHarness(t, verify.ResultVerified)
	h.authenticator.result.Workspaces[0].Name = workspaceName
	h.scanner.result.Workspace.Name = workspaceName
	h.extractor.result.Workspace.Name = workspaceName
	h.exporter.workspace.Name = workspaceName

	install := &operatorInstallation{
		harness:    h,
		stateRoot:  filepath.Join(t.TempDir(), "state"),
		configRoot: filepath.Join(t.TempDir(), "config"),
	}
	h.app.options.StateRoot = install.stateRoot
	h.app.options.NotificationRoot = install.configRoot
	h.app.options.ScheduleRoot = install.configRoot
	return install
}

// statusJSON runs the documented machine-readable status and decodes it.
func (i *operatorInstallation) statusJSON(t *testing.T) (map[string]any, int) {
	t.Helper()
	app, stdout, stderr := statusApp(t, i.stateRoot, Options{
		BackupRoot: i.harness.root, NotificationRoot: i.configRoot, ScheduleRoot: i.configRoot,
	})
	code := app.Run([]string{"status", "--json"})
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("status --json is not one JSON document (exit %d): %v\nstdout=%s\nstderr=%s",
			code, err, stdout.String(), stderr.String())
	}
	return document, code
}

// TestOperatorRunbookOnACleanMachine walks the whole responsibility list on an
// installation that has never been used, in the order an operator would.
func TestOperatorRunbookOnACleanMachine(t *testing.T) {
	t.Parallel()
	install := newOperatorInstallation(t, "Customer Workspace")
	h := install.harness

	// 1. Monitoring, before anything exists. Nothing recorded is its own answer
	//    and its own exit code, not an error and not a false healthy.
	document, code := install.statusJSON(t)
	if code != statusExitUnknown {
		t.Fatalf("a clean machine reported exit %d, want %d", code, statusExitUnknown)
	}
	if document["schema_version"] == nil {
		t.Fatal("status --json carries no schema version for automation to pin")
	}

	// 2. Configuration check, before any delivery is configured. Silence is the
	//    documented default and must be reported as such rather than as a fault.
	notifyApp, notifyOut, _ := statusApp(t, install.stateRoot, Options{NotificationRoot: install.configRoot})
	if code := notifyApp.Run([]string{"notify", "--json"}); code != 0 {
		t.Fatalf("notify --json on a clean machine exited %d: %s", code, notifyOut.String())
	}
	if !json.Valid(notifyOut.Bytes()) {
		t.Fatalf("notify --json is not JSON: %s", notifyOut.String())
	}

	// 3. Scheduling check, before any schedule is defined.
	scheduleApp, scheduleOut, _ := statusApp(t, install.stateRoot, Options{ScheduleRoot: install.configRoot})
	if code := scheduleApp.Run([]string{"schedule", "check", "--json"}); code != 0 {
		t.Fatalf("schedule check --json exited %d: %s", code, scheduleOut.String())
	}
	if !json.Valid(scheduleOut.Bytes()) {
		t.Fatalf("schedule check --json is not JSON: %s", scheduleOut.String())
	}

	// 4. The backup itself, through the ordinary public command.
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup exited %d: %s", code, h.stderr.String())
	}

	// 5. Monitoring after a successful run. The operator learns the outcome,
	//    the archive, and the verification without reading prose or SQLite.
	document, code = install.statusJSON(t)
	if code != statusExitHealthy {
		t.Fatalf("status after a verified backup exited %d, want %d", code, statusExitHealthy)
	}
	scopes, ok := document["scopes"].([]any)
	if !ok || len(scopes) != 1 {
		t.Fatalf("status --json does not report exactly one scope: %v", document["scopes"])
	}
	scope, ok := scopes[0].(map[string]any)
	if !ok {
		t.Fatalf("scope is not an object: %#v", scopes[0])
	}
	for _, field := range []string{
		"connector", "workspace_id", "destination", "status",
		"last_attempt", "last_success", "last_verification",
	} {
		if scope[field] == nil {
			t.Errorf("status --json omits %q, which the operator needs", field)
		}
	}

	// 6. The archive and report the customer is handed. The operator learns
	//    both paths from status rather than from knowing Alih's layout.
	success, ok := scope["last_success"].(map[string]any)
	if !ok {
		t.Fatalf("last_success is not an object: %#v", scope["last_success"])
	}
	archivePath, _ := success["archive_path"].(string)
	reportPath, _ := success["report_path"].(string)
	if archivePath == "" || reportPath == "" {
		t.Fatalf("status --json does not name the archive and report: %#v", success)
	}
	for _, path := range []string{archivePath, reportPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("status names %s, which does not exist: %v", path, err)
		}
	}

	// 7. Verification of that archive, reached only through what status said.
	verifyApp, _, verifyErr := statusApp(t, install.stateRoot, Options{Verifier: h.verifier})
	if code := verifyApp.Run([]string{"verify", "--archive", archivePath}); code != 0 {
		t.Fatalf("the archive status points at does not verify: exit %d\n%s", code, verifyErr.String())
	}

	// 8. Recorded history, correlated to the run by operation id.
	events, damaged, err := event.Read(install.stateRoot)
	if err != nil {
		t.Fatalf("read recorded history: %v", err)
	}
	if damaged != 0 || len(events) == 0 {
		t.Fatalf("history has %d events and %d damaged lines", len(events), damaged)
	}
	record := loadOnlyRecord(t, install.stateRoot)
	var correlated bool
	for _, recorded := range events {
		if record.LastAttempt != nil && recorded.OperationID == record.LastAttempt.OperationID {
			correlated = true
		}
	}
	if !correlated {
		t.Error("no recorded event shares the operation id status reports")
	}
}

// TestOperatorDiagnosesAFailureFromMachineReadableEvidenceAlone proves the
// operator never has to parse prose or open the archive database to find out
// what went wrong and at which stage.
func TestOperatorDiagnosesAFailureFromMachineReadableEvidenceAlone(t *testing.T) {
	t.Parallel()
	install := newOperatorInstallation(t, "Failing Workspace")
	install.harness.exporter.err = errors.New("the source stopped answering")

	if code := install.harness.app.Run([]string{"backup"}); code == 0 {
		t.Fatal("a failing backup exited 0")
	}

	document, code := install.statusJSON(t)
	if code != statusExitAttention {
		t.Fatalf("status after a failure exited %d, want %d", code, statusExitAttention)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	// The stage that failed must be machine-readable, not inferred from text.
	if !strings.Contains(string(encoded), state.StageExport) {
		t.Errorf("status --json does not name the stage that failed:\n%s", encoded)
	}

	events, _, err := event.Read(install.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	var failure *event.Event
	for index := range events {
		if events[index].Type == event.TypeOperationFailed {
			failure = &events[index]
		}
	}
	if failure == nil {
		t.Fatal("a failed run recorded no operation.failed event")
	}
	if failure.Stage != state.StageExport || failure.Message == "" {
		t.Errorf("the recorded failure is not diagnosable: %#v", failure)
	}
	// And nothing in that evidence repeats the provider's own words.
	if strings.Contains(failure.Message, "stopped answering") {
		t.Errorf("recorded history repeated provider text: %q", failure.Message)
	}
}

// TestTwoCustomerInstallationsCannotAffectEachOther proves an operator running
// several environments keeps them separate: separate state, separate history,
// separate destinations, separate identities.
func TestTwoCustomerInstallationsCannotAffectEachOther(t *testing.T) {
	t.Parallel()
	first := newOperatorInstallation(t, "Customer One")
	second := newOperatorInstallation(t, "Customer Two")
	second.harness.authenticator.result.Workspaces[0].ID = "200"
	second.harness.scanner.result.Workspace.ID = "200"
	second.harness.extractor.result.Workspace.ID = "200"
	second.harness.exporter.workspace.ID = "200"

	if code := first.harness.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("first backup exited %d: %s", code, first.harness.stderr.String())
	}
	// The second installation fails, and must not disturb the first.
	second.harness.exporter.err = errors.New("second customer is unreachable")
	if code := second.harness.app.Run([]string{"backup"}); code == 0 {
		t.Fatal("the failing second backup exited 0")
	}

	if first.stateRoot == second.stateRoot || first.harness.root == second.harness.root {
		t.Fatal("the two installations share a root")
	}
	firstRecord := loadOnlyRecord(t, first.stateRoot)
	secondRecord := loadOnlyRecord(t, second.stateRoot)
	if firstRecord.LastSuccess == nil {
		t.Error("the healthy installation lost its recorded success")
	}
	if secondRecord.LastSuccess != nil {
		t.Error("the failing installation recorded a success")
	}
	if firstRecord.Scope.Key() == secondRecord.Scope.Key() {
		t.Fatal("two customer installations share one state identity")
	}

	// Each still reports its own condition.
	if _, code := first.statusJSON(t); code != statusExitHealthy {
		t.Errorf("the healthy installation reported exit %d", code)
	}
	if _, code := second.statusJSON(t); code != statusExitAttention {
		t.Errorf("the failing installation reported exit %d", code)
	}
}

// TestReadOnlyCommandsMakeNoSourceRequest proves the commands an operator uses
// for routine monitoring are genuinely offline, so watching an installation
// cannot spend a customer's rate limit or leak that they are being watched.
func TestReadOnlyCommandsMakeNoSourceRequest(t *testing.T) {
	t.Parallel()
	install := newOperatorInstallation(t, "Observed Workspace")
	if code := install.harness.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup exited %d", code)
	}
	before := len(install.harness.recorder.calls)

	// A connector that fails on any use at all: if a read-only command touched
	// the source, the command would fail rather than quietly succeed.
	refusing := &backupAuthenticator{
		recorder: install.harness.recorder,
		err:      errors.New("a read-only command must not contact the source"),
	}
	for _, arguments := range [][]string{
		{"status"},
		{"status", "--json"},
		{"notify", "--json"},
		{"schedule", "check", "--json"},
	} {
		app, _, stderr := statusApp(t, install.stateRoot, Options{
			Authenticator: refusing, BackupRoot: install.harness.root,
			NotificationRoot: install.configRoot, ScheduleRoot: install.configRoot,
		})
		if code := app.Run(arguments); code == 2 {
			t.Fatalf("%v was rejected as a usage error: %s", arguments, stderr.String())
		}
	}
	if len(install.harness.recorder.calls) != before {
		t.Fatalf("a read-only command contacted the source: %v", install.harness.recorder.calls)
	}
}

// TestCustomerKeepsItsCredentialWhenPersistenceIsOff proves an installation
// whose credential is injected per run does not end up with a copy of that
// credential on disk.
func TestCustomerKeepsItsCredentialWhenPersistenceIsOff(t *testing.T) {
	t.Parallel()
	install := newOperatorInstallation(t, "Injected Credential")
	store := &stubCredentialStore{}
	install.harness.app.options.CredentialStore = store
	install.harness.app.options.EnvironmentToken = install.harness.token
	install.harness.app.options.EnvironmentTokenSet = true
	install.harness.app.options.SaveEnvironmentCredential = false

	if code := install.harness.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup exited %d: %s", code, install.harness.stderr.String())
	}
	if store.saved != "" {
		t.Fatalf("a credential was persisted although persistence is off: %q", store.saved)
	}
	// The backup still happened, and its state carries no credential either.
	record := loadOnlyRecord(t, install.stateRoot)
	if record.LastSuccess == nil {
		t.Fatal("refusing to persist the credential also lost the backup")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), install.harness.token) {
		t.Fatal("the credential reached the operational state")
	}
}

// TestAnUnwritableCredentialStoreNeverCostsABackup proves an ephemeral or
// read-only installation still completes its work. Caching a credential is a
// convenience, and losing a customer's backup over it would be indefensible.
func TestAnUnwritableCredentialStoreNeverCostsABackup(t *testing.T) {
	t.Parallel()
	install := newOperatorInstallation(t, "Ephemeral Installation")
	install.harness.app.options.CredentialStore = &stubCredentialStore{
		saveErr: errors.New("read-only file system"),
	}
	install.harness.app.options.EnvironmentToken = install.harness.token
	install.harness.app.options.EnvironmentTokenSet = true
	install.harness.app.options.SaveEnvironmentCredential = true

	code := install.harness.app.Run([]string{"backup"})
	if code != 0 {
		t.Fatalf("a backup failed only because it could not cache a credential: exit %d\n%s",
			code, install.harness.stderr.String())
	}
	if !strings.Contains(install.harness.stderr.String(), "could not be saved") {
		t.Errorf("the operator was not told the credential was not cached:\n%s", install.harness.stderr.String())
	}
	if loadOnlyRecord(t, install.stateRoot).LastSuccess == nil {
		t.Fatal("the backup did not complete")
	}
}

// TestOffboardingLeavesTheCustomerWithUsableArchives proves that removing every
// trace of Alih's own operation does not touch what the customer actually owns,
// and that what remains is still independently verifiable.
func TestOffboardingLeavesTheCustomerWithUsableArchives(t *testing.T) {
	t.Parallel()
	install := newOperatorInstallation(t, "Departing Customer")
	if code := install.harness.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup exited %d: %s", code, install.harness.stderr.String())
	}
	// The operator learns the paths from the record, not from Alih's layout.
	recorded := loadOnlyRecord(t, install.stateRoot)
	if recorded.LastSuccess == nil {
		t.Fatal("the backup recorded no success to offboard from")
	}
	archivePath := recorded.LastSuccess.ArchivePath
	reportPath := recorded.LastSuccess.ReportPath

	// Offboarding: the credential is revoked, and Alih's own local records are
	// removed. Nothing here touches the destination.
	if err := os.RemoveAll(install.stateRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(install.configRoot); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{archivePath, reportPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("offboarding removed customer data at %s: %v", path, err)
		}
	}

	// The archive still verifies on its own, with no state and no credential.
	app, stdout, stderr := statusApp(t, filepath.Join(t.TempDir(), "empty-state"), Options{
		Verifier: install.harness.verifier,
	})
	if code := app.Run([]string{"verify", "--archive", archivePath}); code != 0 {
		t.Fatalf("an offboarded archive no longer verifies: exit %d\n%s\n%s", code, stdout.String(), stderr.String())
	}

	// And a fresh installation pointed at that destination can recover what is
	// there, without any prior knowledge.
	recovered := filepath.Join(t.TempDir(), "recovered-state")
	recoverApp, recoverOut, recoverErr := statusApp(t, recovered, Options{
		Verifier: install.harness.verifier, BackupRoot: install.harness.root,
	})
	if code := recoverApp.Run([]string{"status", "--reconcile"}); code == 2 {
		t.Fatalf("reconciliation was rejected as a usage error: %s", recoverErr.String())
	}
	if !strings.Contains(recoverOut.String(), "Departing Customer") {
		t.Errorf("a fresh installation could not recover what the destination holds:\n%s", recoverOut.String())
	}
}
