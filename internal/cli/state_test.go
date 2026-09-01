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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/connector/clickup"
	coreexporter "alih/internal/exporter"
	corereporter "alih/internal/reporter"
	"alih/internal/state"
	coreverifier "alih/internal/verifier"
)

// TestMain keeps the whole package away from the real user configuration
// directory. Commands resolve credential and state locations from it, and a
// test must never write into the account running the test suite.
func TestMain(m *testing.M) {
	sandbox, err := os.MkdirTemp("", "alih-cli-config-")
	if err != nil {
		panic(err)
	}
	for _, variable := range []string{"XDG_CONFIG_HOME", "HOME", "AppData", "USERPROFILE"} {
		if err := os.Setenv(variable, sandbox); err != nil {
			panic(err)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(sandbox)
	os.Exit(code)
}

func stateStoreAt(t *testing.T, root string) *state.Store {
	t.Helper()
	store, err := state.NewStore(root)
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	return store
}

func loadOnlyRecord(t *testing.T, root string) state.Record {
	t.Helper()
	records, unreadable, err := stateStoreAt(t, root).List()
	if err != nil {
		t.Fatalf("list state: %v", err)
	}
	if len(unreadable) != 0 {
		t.Fatalf("state contains unreadable files: %v", unreadable)
	}
	if len(records) != 1 {
		t.Fatalf("recorded scopes = %d, want exactly 1", len(records))
	}
	return records[0]
}

func TestBackupRecordsTheRunItCompleted(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot

	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, h.stderr.String())
	}

	record := loadOnlyRecord(t, stateRoot)
	if record.Scope.Connector != "clickup" || record.Scope.WorkspaceID != "100" || record.Scope.Destination != h.root {
		t.Fatalf("scope = %#v", record.Scope)
	}
	if record.WorkspaceName != "Example Workspace" {
		t.Fatalf("workspace name = %q", record.WorkspaceName)
	}
	if record.Account == nil || record.Account.ID != "u1" {
		t.Fatalf("account = %#v", record.Account)
	}
	if record.LastAttempt == nil || record.LastAttempt.Outcome != state.OutcomeSucceeded ||
		record.LastAttempt.Stage != state.StageFinalize || record.LastAttempt.Operation != state.OperationBackup {
		t.Fatalf("last attempt = %#v", record.LastAttempt)
	}
	if record.LastSuccess == nil {
		t.Fatal("a completed backup did not become the last known success")
	}
	wantArchive := filepath.Join(h.finalRoot(), backupArchiveDirectory)
	wantReport := filepath.Join(h.finalRoot(), backupReportFilename)
	if record.LastSuccess.ArchivePath != wantArchive || record.LastSuccess.ReportPath != wantReport {
		t.Fatalf("recorded paths = %q and %q", record.LastSuccess.ArchivePath, record.LastSuccess.ReportPath)
	}
	if record.LastSuccess.EndedAt == nil || record.LastSuccess.EndedAt.Before(record.LastSuccess.StartedAt) {
		t.Fatalf("recorded times = %v to %v", record.LastSuccess.StartedAt, record.LastSuccess.EndedAt)
	}
	if record.LastAttempt.OperationID == "" || !strings.Contains(record.LastAttempt.OperationID, "-") {
		t.Fatalf("operation id = %q", record.LastAttempt.OperationID)
	}
	if record.Revision < 2 {
		t.Fatalf("revision = %d, want the start and the completion to be separate writes", record.Revision)
	}
	if strings.Contains(h.stdout.String()+h.stderr.String(), "operational state could not be recorded") {
		t.Fatal("a successful run warned about state")
	}
}

func TestBackupRecordsAnInterruptedRunAsStartedNotSucceeded(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot

	// The scanner observes state from inside the run, before any source work
	// has produced a result. Nothing may claim success at that point.
	var observed state.Record
	var observeErr error
	h.scanner.err = errors.New("stop after observing")
	h.app.options.Scanner = &observingScanner{inner: h.scanner, observe: func() {
		observed, observeErr = stateStoreAt(t, stateRoot).Load(
			backupScope("clickup", "100", h.root))
	}}

	if code := h.app.Run([]string{"backup"}); code != 1 {
		t.Fatalf("code=%d stderr=%q", code, h.stderr.String())
	}
	if observeErr != nil {
		t.Fatalf("state was not recorded before the source was read: %v", observeErr)
	}
	if observed.LastAttempt == nil || observed.LastAttempt.Outcome != state.OutcomeStarted ||
		observed.LastAttempt.Stage != state.StagePrepare {
		t.Fatalf("in-flight attempt = %#v", observed.LastAttempt)
	}
	if observed.LastSuccess != nil {
		t.Fatal("an in-flight run recorded a success")
	}
}

type observingScanner struct {
	inner   *backupScanner
	observe func()
}

func (s *observingScanner) Name() string { return s.inner.Name() }
func (s *observingScanner) Scan(ctx context.Context, credential string, workspace connector.Workspace) (connector.ScanResult, error) {
	s.observe()
	return s.inner.Scan(ctx, credential, workspace)
}

func TestBackupFailureRecordsTheStageAndReasonWithoutProviderText(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	h.exporter.err = &clickup.Error{
		Kind: clickup.ErrorAPI, Operation: "list Tasks", StatusCode: http.StatusNotFound,
		Cause: errors.New("provider said something unrepeatable"),
	}

	if code := h.app.Run([]string{"backup"}); code != 1 {
		t.Fatalf("code=%d stderr=%q", code, h.stderr.String())
	}

	record := loadOnlyRecord(t, stateRoot)
	attempt := record.LastAttempt
	if attempt == nil || attempt.Outcome != state.OutcomeFailed || attempt.Stage != state.StageExport {
		t.Fatalf("failed attempt = %#v", attempt)
	}
	if attempt.Error == nil || attempt.Error.Reason != connector.HealthReasonCapabilityRemoved {
		t.Fatalf("recorded reason = %#v", attempt.Error)
	}
	if strings.Contains(attempt.Error.Message, "unrepeatable") {
		t.Fatalf("state repeated provider text: %q", attempt.Error.Message)
	}
	if record.LastSuccess != nil {
		t.Fatal("a failed run was recorded as a successful backup")
	}
	if attempt.FailedPath != h.finalRoot()+".failed" {
		t.Fatalf("failed working state = %q", attempt.FailedPath)
	}
	if record.Assessment == nil || record.Assessment.Health.Reason != connector.HealthReasonCapabilityRemoved {
		t.Fatalf("recorded assessment = %#v", record.Assessment)
	}
}

func TestUntypedBackupFailureStaysAnExplicitlyUnknownFailure(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	h.verifier.err = errors.New("disk fell over while reading the archive")

	if code := h.app.Run([]string{"backup"}); code != 1 {
		t.Fatalf("code=%d stderr=%q", code, h.stderr.String())
	}
	attempt := loadOnlyRecord(t, stateRoot).LastAttempt
	if attempt == nil || attempt.Stage != state.StageVerify || attempt.Error == nil {
		t.Fatalf("failed attempt = %#v", attempt)
	}
	if attempt.Error.Reason != connector.HealthReasonUnknownFailure {
		t.Fatalf("reason = %q, want an explicitly unknown failure", attempt.Error.Reason)
	}
	if strings.Contains(attempt.Error.Message, "disk fell over") {
		t.Fatalf("state borrowed the raw error text: %q", attempt.Error.Message)
	}
}

func TestBackupKeepsItsResultWhenStateCannotBeRecorded(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	h.app.options.StateRoot = blocked

	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("a sealed and verified backup was downgraded by a state failure: code=%d stderr=%q",
			code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "operational state could not be recorded") {
		t.Fatalf("the state failure was not reported: %q", h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "the result above is unaffected") {
		t.Fatalf("the warning did not explain what it means: %q", h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "ALIH — BACKUP COMPLETE") {
		t.Fatal("the completed backup was no longer reported to the user")
	}
	if info, err := os.Stat(filepath.Join(h.finalRoot(), backupArchiveDirectory)); err != nil || !info.IsDir() {
		t.Fatalf("the archive was not published: %v", err)
	}
}

func TestRecordedStateNeverContainsTheCredential(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot

	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, h.stderr.String())
	}
	entries, err := os.ReadDir(stateRoot)
	if err != nil {
		t.Fatalf("read state directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no state was written")
	}
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(stateRoot, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		if bytes.Contains(content, []byte(h.token)) {
			t.Fatalf("state file %s contains the credential", entry.Name())
		}
	}
}

func TestBackupFailureBeforeADestinationIsKnownRecordsNothing(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	h.app.options.Authenticator = &backupAuthenticator{
		recorder: h.recorder, err: errors.New("credential rejected"),
	}

	if code := h.app.Run([]string{"backup"}); code != 1 {
		t.Fatalf("code=%d", code)
	}
	records, unreadable, err := stateStoreAt(t, stateRoot).List()
	if err != nil {
		t.Fatalf("list state: %v", err)
	}
	if len(records) != 0 || len(unreadable) != 0 {
		t.Fatalf("a run that never reached a destination wrote state: %#v %v", records, unreadable)
	}
}

func TestExtractRecordsAnAttemptButNeverASuccessfulBackup(t *testing.T) {
	t.Parallel()
	const token = "saved-private-token"
	workspace := connector.Workspace{ID: "100", Name: "Primary"}
	target := filepath.Join(t.TempDir(), "m3-raw")
	stateRoot := filepath.Join(t.TempDir(), "state")
	var stdout, stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator: &stubAuthenticator{result: connector.Authentication{Workspaces: []connector.Workspace{workspace}}},
		Extractor: &stubExtractor{result: connector.ExtractionResult{
			ScanResult:    connector.ScanResult{Workspace: workspace},
			SourceObjects: []connector.SourceObject{{Type: "workspace", ID: workspace.ID}},
		}},
		CredentialStore: &stubCredentialStore{loaded: token},
		StateRoot:       stateRoot,
	})

	if code := app.Run([]string{"extract", "--output", target}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}

	record := loadOnlyRecord(t, stateRoot)
	if record.Scope.Destination != target {
		t.Fatalf("destination = %q, want the path the operator chose", record.Scope.Destination)
	}
	if record.LastAttempt == nil || record.LastAttempt.Operation != state.OperationExtract ||
		record.LastAttempt.Outcome != state.OutcomeSucceeded || record.LastAttempt.Stage != state.StageExtract {
		t.Fatalf("last attempt = %#v", record.LastAttempt)
	}
	if record.LastSuccess != nil {
		t.Fatal("a raw extraction was recorded as a successful backup")
	}
	if record.LastAttempt.ArchivePath != "" {
		t.Fatalf("a raw extraction recorded an archive path: %q", record.LastAttempt.ArchivePath)
	}
}

func TestBackupPreservesTheVerificationTheSealedArchiveCannotHold(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "Alih")
	stateRoot := filepath.Join(t.TempDir(), "state")
	recorder := &backupRecorder{}
	workspace := connector.Workspace{ID: "100", Name: "Sealed Claim"}
	authenticator := &backupAuthenticator{recorder: recorder, result: connector.Authentication{
		Identity: connector.Identity{ID: "u1", Name: "Tester"}, Workspaces: []connector.Workspace{workspace},
	}}
	scanner := &backupScanner{recorder: recorder, result: connector.ScanResult{
		Workspace: workspace,
		Capabilities: []connector.Capability{{
			Name: "Tasks/subtasks", State: connector.CapabilitySupported, Note: "complete traversal",
		}},
	}}
	extractor := &backupExtractor{recorder: recorder, result: connector.ExtractionResult{
		ScanResult:    scanner.result,
		SourceObjects: []connector.SourceObject{{Type: "workspace", ID: workspace.ID}},
	}}
	independentVerifier := coreverifier.New(clickup.FieldSemantics{})
	var stdout, stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator: authenticator, Scanner: scanner, Extractor: extractor,
		Exporter: coreexporter.New(nil, clickup.Normalizer{}), Verifier: independentVerifier,
		Reporter:        corereporter.New(independentVerifier),
		CredentialStore: &stubCredentialStore{loaded: "pk_sealed_claim_secret"},
		BackupRoot:      root, StateRoot: stateRoot,
		Now: func() time.Time { return alphaTestTime },
	})

	if code := app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	archivePath := filepath.Join(root, "Sealed-Claim", "2026-08-30T123000Z", backupArchiveDirectory)

	// The archive itself must keep saying that it was never verified.
	content, err := os.ReadFile(filepath.Join(archivePath, state.ManifestFilename))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest archive.Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Verification.Status != "NOT_RUN" {
		t.Fatalf("sealed manifest verification status = %q, want NOT_RUN", manifest.Verification.Status)
	}

	record := loadOnlyRecord(t, stateRoot)
	if record.LastVerification == nil {
		t.Fatal("the verification this run performed was not preserved outside the archive")
	}
	if !successfulVerification(record.LastVerification.Result) {
		t.Fatalf("recorded result = %q", record.LastVerification.Result)
	}
	if record.LastVerification.Archive.Path != archivePath {
		t.Fatalf("recorded archive = %q, want the published path %q", record.LastVerification.Archive.Path, archivePath)
	}
	if record.LastVerification.Archive.LogicalDigest != manifest.InputSnapshot.LogicalDigest {
		t.Fatalf("recorded logical digest = %q", record.LastVerification.Archive.LogicalDigest)
	}
	if condition := state.InspectArchive(record.LastVerification.Archive); condition != state.ArchivePresent {
		t.Fatalf("archive condition = %s, want PRESENT", condition)
	}
	if !record.LastVerification.VerifiedAt.Equal(alphaTestTime) {
		t.Fatalf("verified at = %s, want the injected clock", record.LastVerification.VerifiedAt)
	}
}

func TestARecordedVerificationStopsMatchingAChangedOrMissingArchive(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, h.stderr.String())
	}
	record := loadOnlyRecord(t, stateRoot)
	if record.LastVerification == nil {
		t.Fatal("no verification was recorded")
	}
	identity := record.LastVerification.Archive

	manifest := filepath.Join(identity.Path, state.ManifestFilename)
	original, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := os.WriteFile(manifest, append(original, ' '), 0o600); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}
	if condition := state.InspectArchive(identity); condition != state.ArchiveChanged {
		t.Fatalf("condition = %s, want CHANGED", condition)
	}

	if err := os.RemoveAll(identity.Path); err != nil {
		t.Fatalf("remove archive: %v", err)
	}
	if condition := state.InspectArchive(identity); condition != state.ArchiveMissing {
		t.Fatalf("condition = %s, want MISSING", condition)
	}
	// The record itself is untouched: Alih reports the weakened claim, it does
	// not quietly delete the history that produced it.
	if again := loadOnlyRecord(t, stateRoot); again.LastVerification == nil || again.LastSuccess == nil {
		t.Fatal("inspecting an archive changed the stored record")
	}
}

func TestManualVerifyUpdatesTheRecordForAnArchiveAlihKnows(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%q", code, h.stderr.String())
	}
	before := loadOnlyRecord(t, stateRoot)

	archivePath := filepath.Join(h.finalRoot(), backupArchiveDirectory)
	h.verifier.result = "INCOMPLETE"
	if code := h.app.Run([]string{"verify", "--archive", archivePath}); code != 1 {
		t.Fatalf("verify code=%d stderr=%q", code, h.stderr.String())
	}

	after := loadOnlyRecord(t, stateRoot)
	if after.LastVerification == nil || after.LastVerification.Result != "INCOMPLETE" {
		t.Fatalf("recorded verification = %#v", after.LastVerification)
	}
	if after.Revision <= before.Revision {
		t.Fatalf("revision = %d, want it to advance past %d", after.Revision, before.Revision)
	}
	// The earlier successful backup is history and stays recorded; only the
	// claim about the archive's current state changed.
	if after.LastSuccess == nil || after.LastSuccess.ArchivePath != archivePath {
		t.Fatalf("last success = %#v", after.LastSuccess)
	}
}

func TestManualVerifyNeverInventsStateForAnUnknownArchive(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot

	// An archive-shaped directory that no recorded run ever produced.
	stray := filepath.Join(t.TempDir(), "archive")
	if err := os.MkdirAll(stray, 0o700); err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stray, state.ManifestFilename), []byte(`{"status":"CREATED_UNVERIFIED"}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	if code := h.app.Run([]string{"verify", "--archive", stray}); code != 0 {
		t.Fatalf("verify code=%d stderr=%q", code, h.stderr.String())
	}
	records, unreadable, err := stateStoreAt(t, stateRoot).List()
	if err != nil {
		t.Fatalf("list state: %v", err)
	}
	if len(records) != 0 || len(unreadable) != 0 {
		t.Fatalf("verifying an unknown archive invented state: %#v %v", records, unreadable)
	}
}

// TestOneReleaseVersionReachesEveryArtifactARunWrites proves the provenance
// contract end to end: the version the application was given is the version
// recorded in the raw snapshot, the sealed archive, its database, the recovery
// report, and local state — with no compiled-in placeholder anywhere.
func TestOneReleaseVersionReachesEveryArtifactARunWrites(t *testing.T) {
	t.Parallel()
	const release = "9.9.9-provenance"
	root := filepath.Join(t.TempDir(), "Alih")
	stateRoot := filepath.Join(t.TempDir(), "state")
	recorder := &backupRecorder{}
	workspace := connector.Workspace{ID: "100", Name: "Provenance"}
	scanner := &backupScanner{recorder: recorder, result: connector.ScanResult{
		Workspace: workspace,
		Capabilities: []connector.Capability{{
			Name: "Tasks/subtasks", State: connector.CapabilitySupported, Note: "complete traversal",
		}},
	}}
	independentVerifier := coreverifier.New(clickup.FieldSemantics{})
	var stdout, stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator: &backupAuthenticator{recorder: recorder, result: connector.Authentication{
			Identity: connector.Identity{ID: "u1", Name: "Tester"}, Workspaces: []connector.Workspace{workspace},
		}},
		Scanner: scanner,
		Extractor: &backupExtractor{recorder: recorder, result: connector.ExtractionResult{
			ScanResult:    scanner.result,
			SourceObjects: []connector.SourceObject{{Type: "workspace", ID: workspace.ID}},
		}},
		Exporter:        coreexporter.NewWithVersion(nil, release, clickup.Normalizer{}),
		Verifier:        independentVerifier,
		Reporter:        corereporter.NewWithVersion(independentVerifier, release),
		CredentialStore: &stubCredentialStore{loaded: "pk_provenance_secret"},
		BackupRoot:      root, StateRoot: stateRoot, Version: release,
		Now: func() time.Time { return alphaTestTime },
	})

	if code := app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	bundle := filepath.Join(root, "Provenance", "2026-08-30T123000Z")
	archivePath := filepath.Join(bundle, backupArchiveDirectory)

	content, err := os.ReadFile(filepath.Join(archivePath, state.ManifestFilename))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest archive.Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.AlihVersion != release {
		t.Fatalf("manifest version = %q, want %q", manifest.AlihVersion, release)
	}

	report, err := os.ReadFile(filepath.Join(bundle, backupReportFilename))
	if err != nil {
		t.Fatalf("read recovery report: %v", err)
	}
	if !strings.Contains(string(report), release) {
		t.Fatal("the recovery report does not record the release that produced it")
	}

	run, err := os.ReadFile(filepath.Join(bundle, "snapshot", "run.json"))
	if err != nil {
		t.Fatalf("read raw extraction run: %v", err)
	}
	var recorded struct {
		AlihVersion string `json:"alih_version"`
	}
	if err := json.Unmarshal(run, &recorded); err != nil {
		t.Fatalf("decode run.json: %v", err)
	}
	if recorded.AlihVersion != release {
		t.Fatalf("raw snapshot version = %q, want %q", recorded.AlihVersion, release)
	}

	record := loadOnlyRecord(t, stateRoot)
	if record.AlihVersion != release || record.LastSuccess == nil || record.LastSuccess.AlihVersion != release {
		t.Fatalf("state version = %q / %#v", record.AlihVersion, record.LastSuccess)
	}

	// No artifact may carry the placeholder that used to be compiled in.
	if err := filepath.WalkDir(bundle, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(body, []byte("0.0.1")) {
			t.Errorf("%s still records the hard-coded 0.0.1 version", path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk bundle: %v", err)
	}
}

// TestAnExistingArchiveIsNeverRewrittenToCarryANewVersion protects archives
// created by an older Alih: reading them must leave every byte untouched.
func TestAnExistingArchiveIsNeverRewrittenToCarryANewVersion(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%q", code, h.stderr.String())
	}
	archivePath := filepath.Join(h.finalRoot(), backupArchiveDirectory)

	before := map[string][]byte{}
	if err := filepath.WalkDir(archivePath, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, readErr := os.ReadFile(path)
		before[path] = body
		return readErr
	}); err != nil {
		t.Fatalf("snapshot archive: %v", err)
	}

	// A newer Alih reads the archive; nothing it does may change it.
	newer, _, _ := statusApp(t, stateRoot, Options{
		Verifier: h.verifier, Reporter: h.reporter, BackupRoot: h.root, Version: "10.0.0",
	})
	if code := newer.Run([]string{"verify", "--archive", archivePath}); code != 0 {
		t.Fatalf("verify code=%d", code)
	}
	if code := newer.Run([]string{"status", "--reconcile"}); code != statusExitHealthy {
		t.Fatalf("status code=%d", code)
	}

	for path, original := range before {
		current, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !bytes.Equal(current, original) {
			t.Fatalf("%s was rewritten by a newer Alih", path)
		}
	}
}
