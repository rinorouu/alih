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
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/connector/clickup"
	"alih/internal/event"
	coreexporter "alih/internal/exporter"
	"alih/internal/oplock"
	"alih/internal/report"
	corereporter "alih/internal/reporter"
	"alih/internal/state"
	coreverifier "alih/internal/verifier"
	"alih/internal/verify"
)

var alphaTestTime = time.Date(2026, 8, 30, 12, 30, 0, 0, time.UTC)

type backupRecorder struct {
	calls []string
}

func (r *backupRecorder) call(name string) { r.calls = append(r.calls, name) }

type backupAuthenticator struct {
	recorder *backupRecorder
	result   connector.Authentication
	err      error
}

func (s *backupAuthenticator) Name() string { return "clickup" }
func (s *backupAuthenticator) Authenticate(_ context.Context, _ string) (connector.Authentication, error) {
	s.recorder.call("auth")
	return s.result, s.err
}

type backupScanner struct {
	recorder  *backupRecorder
	result    connector.ScanResult
	err       error
	workspace connector.Workspace
}

func (s *backupScanner) Name() string { return "clickup" }
func (s *backupScanner) Scan(_ context.Context, _ string, workspace connector.Workspace) (connector.ScanResult, error) {
	s.recorder.call("scan")
	s.workspace = workspace
	return s.result, s.err
}

type backupExtractor struct {
	recorder  *backupRecorder
	result    connector.ExtractionResult
	err       error
	workspace connector.Workspace
}

func (s *backupExtractor) Name() string { return "clickup" }
func (s *backupExtractor) Extract(_ context.Context, _ string, workspace connector.Workspace, sink connector.RawEvidenceSink) (connector.ExtractionResult, error) {
	s.recorder.call("extract")
	s.workspace = workspace
	if s.err != nil {
		return connector.ExtractionResult{}, s.err
	}
	if err := sink.RecordResponse(connector.RawResponse{
		Operation: "list Workspaces", Method: "GET", Path: "/team",
		Attempt: 1, StatusCode: 200, Body: []byte(`{"teams":[]}`),
	}); err != nil {
		return connector.ExtractionResult{}, err
	}
	return s.result, nil
}

type backupExporter struct {
	recorder  *backupRecorder
	status    string
	err       error
	path      string
	connector string
	workspace connector.Workspace
}

func (s *backupExporter) Export(_ context.Context, _, outputPath, _ string) (archive.Summary, error) {
	s.recorder.call("export")
	s.path = outputPath
	if s.err != nil {
		return archive.Summary{}, s.err
	}
	if err := os.Mkdir(outputPath, 0o700); err != nil {
		return archive.Summary{}, err
	}
	// A real export seals a manifest carrying the assessment of the run that
	// produced the archive; the stub writes the smallest truthful equivalent so
	// state recording and reconciliation are exercised here.
	assessment, err := connector.HealthyAssessment("clickup", connector.HealthBasisBackup, alphaTestTime,
		connector.AuthenticationAuthenticated, nil)
	if err != nil {
		return archive.Summary{}, err
	}
	encodedAssessment, err := json.Marshal(assessment)
	if err != nil {
		return archive.Summary{}, err
	}
	sealed := alphaTestTime.Format(time.RFC3339)
	manifest := fmt.Sprintf(
		`{"schema_version":%d,"alih_version":"0.0.1","status":%q,"connector":%q,`+
			`"source":{"id":%q,"name":%q},"source_snapshot_completed_at":%q,"archive_completed_at":%q,`+
			`"operational_assessment":%s,`+
			`"input_snapshot":{"logical_inventory_digest":"sha256:%s","status":"COMPLETE","atomic":false}}`,
		archive.ArchiveSchemaVersion, s.status, s.connector, s.workspace.ID, s.workspace.Name,
		sealed, sealed, encodedAssessment, strings.Repeat("ab", 32))
	if err := os.WriteFile(filepath.Join(outputPath, "manifest.json"), []byte(manifest), 0o600); err != nil {
		return archive.Summary{}, err
	}
	return archive.Summary{Path: outputPath, Status: s.status, OperationalAssessment: &assessment}, nil
}

type backupVerifier struct {
	recorder *backupRecorder
	result   string
	err      error
	path     string
}

func (s *backupVerifier) Verify(path string) (verify.Report, error) {
	s.recorder.call("verify")
	s.path = path
	return verify.Report{ArchivePath: path, Result: s.result}, s.err
}

type backupReporter struct {
	recorder *backupRecorder
	result   string
	err      error
	path     string
	// before is a fault-injection seam. It runs after reporting has been asked
	// for and before the bundle is published, which is the only window in which
	// a test can make the final publication itself fail.
	before func()
}

func (s *backupReporter) Report(path string) (report.Document, error) {
	s.recorder.call("report")
	s.path = path
	if s.before != nil {
		s.before()
	}
	return report.Document{
		SchemaVersion: report.SchemaVersion,
		Kind:          report.Kind,
		AlihVersion:   "0.0.1",
		GeneratedAt:   alphaTestTime,
		Identity: report.Identity{
			ArchivePath: path, Connector: "clickup", WorkspaceID: "100",
			WorkspaceName: "Example Workspace", ManifestReadable: true,
		},
		Verification: report.Verification{Result: s.result, Headline: "Verification passed."},
		Conclusion:   report.Conclusion{Result: s.result, Verdict: "Archive integrity is proven."},
	}, s.err
}

type backupHarness struct {
	app           *App
	stdout        *bytes.Buffer
	stderr        *bytes.Buffer
	recorder      *backupRecorder
	authenticator *backupAuthenticator
	scanner       *backupScanner
	extractor     *backupExtractor
	exporter      *backupExporter
	verifier      *backupVerifier
	reporter      *backupReporter
	root          string
	token         string
}

func newBackupHarness(t *testing.T, result string) *backupHarness {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Alih")
	recorder := &backupRecorder{}
	workspace := connector.Workspace{ID: "100", Name: "Example Workspace"}
	authenticator := &backupAuthenticator{recorder: recorder, result: connector.Authentication{
		Identity: connector.Identity{ID: "u1", Name: "Tester"}, Workspaces: []connector.Workspace{workspace},
	}}
	scanner := &backupScanner{recorder: recorder, result: connector.ScanResult{Workspace: workspace}}
	extractor := &backupExtractor{recorder: recorder, result: connector.ExtractionResult{ScanResult: connector.ScanResult{Workspace: workspace}}}
	exporter := &backupExporter{
		recorder: recorder, status: archive.StatusCreatedUnverified,
		connector: "clickup", workspace: workspace,
	}
	verifier := &backupVerifier{recorder: recorder, result: result}
	reporter := &backupReporter{recorder: recorder, result: result}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	token := "pk_alpha_secret"
	app := New(stdout, stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator: authenticator, Scanner: scanner, Extractor: extractor,
		Exporter: exporter, Verifier: verifier, Reporter: reporter,
		CredentialStore: &stubCredentialStore{loaded: token}, BackupRoot: root,
		Now: func() time.Time { return alphaTestTime },
	})
	return &backupHarness{
		app: app, stdout: stdout, stderr: stderr, recorder: recorder,
		authenticator: authenticator, scanner: scanner,
		extractor: extractor, exporter: exporter, verifier: verifier, reporter: reporter,
		root: root, token: token,
	}
}

func (h *backupHarness) finalRoot() string {
	return filepath.Join(h.root, "Example-Workspace", "2026-08-30T123000Z")
}

func TestBackupHappyPathUsesExistingPipelineAndPublishesVerifiedBundle(t *testing.T) {
	t.Parallel()
	for _, result := range []string{verify.ResultVerified, verify.ResultVerifiedWithLimitations} {
		result := result
		t.Run(result, func(t *testing.T) {
			t.Parallel()
			h := newBackupHarness(t, result)
			if code := h.app.Run([]string{"backup"}); code != 0 {
				t.Fatalf("code=%d stderr=%q", code, h.stderr.String())
			}
			if want := []string{"auth", "scan", "extract", "export", "verify", "report"}; !reflect.DeepEqual(h.recorder.calls, want) {
				t.Fatalf("pipeline calls=%v want=%v", h.recorder.calls, want)
			}
			archivePath := filepath.Join(h.finalRoot(), backupArchiveDirectory)
			reportPath := filepath.Join(h.finalRoot(), backupReportFilename)
			if info, err := os.Stat(archivePath); err != nil || !info.IsDir() {
				t.Fatalf("archive was not published at %s: %v", archivePath, err)
			}
			content, err := os.ReadFile(reportPath)
			if err != nil {
				t.Fatalf("recovery report missing: %v", err)
			}
			if !strings.Contains(string(content), "<!doctype html>") || !strings.Contains(string(content), archivePath) {
				t.Fatal("Recovery Report is not HTML derived for the final archive path")
			}
			for _, expected := range []string{
				"ALIH — CLICKUP BACKUP", "Scanning workspace...", "Extracting source data...",
				"Building portable archive...", "Verifying archive...", "Generating recovery report...",
				"ALIH — BACKUP COMPLETE", "Status: " + result, archivePath, reportPath,
				"Your ClickUp data was not modified.",
			} {
				if !strings.Contains(h.stdout.String(), expected) {
					t.Errorf("stdout missing %q:\n%s", expected, h.stdout.String())
				}
			}
			if strings.Contains(h.stdout.String()+h.stderr.String(), "ALI H") {
				t.Fatal("legacy split branding appeared in user-facing output")
			}
			if strings.Contains(h.stdout.String()+h.stderr.String(), h.token) {
				t.Fatal("credential appeared in user-facing output")
			}
		})
	}
}

func TestBackupRefusesOverlapBeforeEnteringThePipeline(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, verify.ResultVerified)
	h.app.options.StateRoot = filepath.Join(t.TempDir(), "state")
	scope := backupScope("clickup", "100", h.root)
	held, err := oplock.Acquire(filepath.Join(h.root, ".alih-locks"), scope,
		"20260901T090000Z-holder", "dev", alphaTestTime)
	if err != nil {
		t.Fatalf("hold lock: %v", err)
	}
	defer held.Release()
	if code := h.app.Run([]string{"backup", "--schedule-id", "daily-main"}); code != 1 {
		t.Fatalf("overlapping backup code=%d stdout=%s stderr=%s", code, h.stdout.String(), h.stderr.String())
	}
	if h.scanner.workspace.ID != "" || h.extractor.workspace.ID != "" || h.exporter.path != "" {
		t.Fatalf("overlap entered pipeline: scan=%#v extract=%#v export=%q",
			h.scanner.workspace, h.extractor.workspace, h.exporter.path)
	}
	if !strings.Contains(h.stderr.String(), "operation scope is already locked") ||
		!strings.Contains(h.stderr.String(), "Backup was not completed") {
		t.Fatalf("overlap was not explained: %s", h.stderr.String())
	}
	record, err := stateStoreAt(t, h.app.options.StateRoot).Load(scope)
	if err != nil {
		t.Fatalf("load overlap state: %v", err)
	}
	if record.LastAttempt == nil || record.LastAttempt.Outcome != state.OutcomeSkipped ||
		record.LastAttempt.ScheduleID != "daily-main" || record.LastAttempt.SkipReason != "OPERATION_OVERLAP" {
		t.Fatalf("overlap state = %#v", record.LastAttempt)
	}
	history, _, err := event.Read(h.app.options.StateRoot)
	if err != nil {
		t.Fatalf("read overlap events: %v", err)
	}
	foundSkip := false
	for _, entry := range history {
		if entry.Type == event.TypeOperationSkipped && entry.Metadata["schedule_id"] == "daily-main" {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Fatalf("scheduled skip event missing: %#v", history)
	}
}

func TestBackupDestinationFlagIsAbsoluteAndOverridesTheDefault(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, verify.ResultVerified)
	if code := h.app.Run([]string{"backup", "--destination", "relative"}); code != 1 {
		t.Fatalf("relative destination code=%d stderr=%s", code, h.stderr.String())
	}

	h = newBackupHarness(t, verify.ResultVerified)
	destination := filepath.Join(t.TempDir(), "scheduled-backups")
	if code := h.app.Run([]string{"backup", "--destination", destination}); code != 0 {
		t.Fatalf("absolute destination code=%d stderr=%s", code, h.stderr.String())
	}
	archivePath := filepath.Join(destination, "Example-Workspace", "2026-08-30T123000Z", backupArchiveDirectory)
	if _, err := os.Stat(filepath.Join(archivePath, state.ManifestFilename)); err != nil {
		t.Fatalf("archive was not published under --destination: %v", err)
	}
}

func TestScheduledBackupUsesTheSamePipelineAndCorrelatesStateAndEvents(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, verify.ResultVerified)
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup", "--schedule-id", "daily-main"}); code != 0 {
		t.Fatalf("scheduled backup code=%d stderr=%s", code, h.stderr.String())
	}
	scope := backupScope("clickup", "100", h.root)
	record, err := stateStoreAt(t, stateRoot).Load(scope)
	if err != nil {
		t.Fatalf("load scheduled state: %v", err)
	}
	if record.LastSuccess == nil || record.LastSuccess.ScheduleID != "daily-main" ||
		record.LastSuccess.Outcome != state.OutcomeSucceeded {
		t.Fatalf("scheduled success = %#v", record.LastSuccess)
	}
	history, _, err := event.Read(stateRoot)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	started, completed := false, false
	for _, entry := range history {
		if entry.Metadata["schedule_id"] != "daily-main" {
			continue
		}
		started = started || entry.Type == event.TypeOperationStarted
		completed = completed || entry.Type == event.TypeOperationCompleted
	}
	if !started || !completed {
		t.Fatalf("scheduled event correlation missing: %#v", history)
	}
	if !reflect.DeepEqual(h.recorder.calls, []string{"auth", "scan", "extract", "export", "verify", "report"}) {
		t.Fatalf("scheduled run used another pipeline: %v", h.recorder.calls)
	}
}

func TestFailedBackupReleasesItsOperationLock(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, verify.ResultVerified)
	h.app.options.StateRoot = filepath.Join(t.TempDir(), "state")
	h.exporter.err = errors.New("injected export failure")
	if code := h.app.Run([]string{"backup", "--schedule-id", "daily-main"}); code != 1 {
		t.Fatalf("failed backup code=%d", code)
	}
	scope := backupScope("clickup", "100", h.root)
	lock, err := oplock.Acquire(filepath.Join(h.root, ".alih-locks"), scope,
		"20260901T100000Z-next", "dev", alphaTestTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("failed backup left its operation lock held: %v", err)
	}
	_ = lock.Release()
}

func TestBackupRunsTheExistingM3ThroughM6CoreImplementations(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "Alih")
	recorder := &backupRecorder{}
	workspace := connector.Workspace{ID: "100", Name: "Core Reuse"}
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
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	app := New(stdout, stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator: authenticator,
		Scanner:       scanner,
		Extractor:     extractor,
		Exporter:      coreexporter.New(nil, clickup.Normalizer{}),
		Verifier:      independentVerifier,
		Reporter:      corereporter.New(independentVerifier),
		CredentialStore: &stubCredentialStore{
			loaded: "pk_core_reuse_secret",
		},
		BackupRoot: root,
		Now:        func() time.Time { return alphaTestTime },
	})
	if code := app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	archivePath := filepath.Join(root, "Core-Reuse", "2026-08-30T123000Z", backupArchiveDirectory)
	verification, err := independentVerifier.Verify(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !successfulVerification(verification.Result) {
		t.Fatalf("published core archive re-verifies as %s", verification.Result)
	}
	for _, path := range []string{"alih.db", "manifest.json", "schema.json", "raw", "attachments"} {
		if _, err := os.Stat(filepath.Join(archivePath, path)); err != nil {
			t.Errorf("existing M4 output %s missing: %v", path, err)
		}
	}
	if !reflect.DeepEqual(recorder.calls, []string{"auth", "scan", "extract"}) {
		t.Fatalf("source calls=%v; backup must use only existing read-only source stages", recorder.calls)
	}
}

func TestBackupVerificationFailureNeverProducesSuccess(t *testing.T) {
	t.Parallel()
	for _, result := range []string{verify.ResultFailed, verify.ResultIncomplete, "UNKNOWN"} {
		result := result
		t.Run(result, func(t *testing.T) {
			t.Parallel()
			h := newBackupHarness(t, result)
			if code := h.app.Run([]string{"backup"}); code == 0 {
				t.Fatal("failed verification returned exit zero")
			}
			if strings.Contains(h.stdout.String()+h.stderr.String(), "BACKUP COMPLETE") {
				t.Fatal("failed verification was presented as complete")
			}
			if !strings.Contains(h.stderr.String(), "verification stage FAILED") || !strings.Contains(h.stderr.String(), result) {
				t.Fatalf("verification failure not explained: %q", h.stderr.String())
			}
			if containsString(h.recorder.calls, "report") {
				t.Fatal("report ran after verification rejected the archive")
			}
			if _, err := os.Stat(h.finalRoot()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed work was published as the completed backup path")
			}
		})
	}
}

func TestBackupStageFailureIsNonZeroAndNamed(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, verify.ResultVerified)
	h.scanner.err = errors.New("inventory traversal stopped")
	if code := h.app.Run([]string{"backup"}); code == 0 {
		t.Fatal("scan failure returned exit zero")
	}
	if !strings.Contains(h.stderr.String(), "scan stage FAILED") || strings.Contains(h.stdout.String()+h.stderr.String(), "BACKUP COMPLETE") {
		t.Fatalf("failure output=%q stdout=%q", h.stderr.String(), h.stdout.String())
	}
	if want := []string{"auth", "scan"}; !reflect.DeepEqual(h.recorder.calls, want) {
		t.Fatalf("pipeline continued after failure: %v", h.recorder.calls)
	}
}

func TestBackupDoesNotOverwriteAnExistingBundle(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, verify.ResultVerified)
	if err := os.MkdirAll(h.finalRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(h.finalRoot(), "keep.txt")
	if err := os.WriteFile(marker, []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.app.Run([]string{"backup"}); code == 0 {
		t.Fatal("existing backup path returned exit zero")
	}
	content, err := os.ReadFile(marker)
	if err != nil || string(content) != "user data" {
		t.Fatalf("existing backup was changed: content=%q err=%v", content, err)
	}
	if containsString(h.recorder.calls, "scan") {
		t.Fatal("source scan started despite an existing destination")
	}
}

func TestBackupWorkspaceSelectionIsUnambiguous(t *testing.T) {
	t.Parallel()
	t.Run("single workspace is deterministic", func(t *testing.T) {
		h := newBackupHarness(t, verify.ResultVerified)
		if code := h.app.Run([]string{"backup"}); code != 0 {
			t.Fatalf("code=%d stderr=%q", code, h.stderr.String())
		}
		if h.scanner.workspace.ID != "100" || h.extractor.workspace.ID != "100" {
			t.Fatal("the only accessible Workspace was not used consistently")
		}
	})
	t.Run("multiple workspaces require an id", func(t *testing.T) {
		h := newBackupHarness(t, verify.ResultVerified)
		auth := h.app.options.Authenticator.(*backupAuthenticator)
		auth.result.Workspaces = append(auth.result.Workspaces, connector.Workspace{ID: "200", Name: "Other"})
		if code := h.app.Run([]string{"backup"}); code == 0 {
			t.Fatal("multiple Workspaces were selected arbitrarily")
		}
		if !strings.Contains(h.stderr.String(), "--workspace-id ID") || containsString(h.recorder.calls, "scan") {
			t.Fatalf("selection failure=%q calls=%v", h.stderr.String(), h.recorder.calls)
		}
	})
	t.Run("requested accessible workspace is used", func(t *testing.T) {
		h := newBackupHarness(t, verify.ResultVerified)
		auth := h.app.options.Authenticator.(*backupAuthenticator)
		second := connector.Workspace{ID: "200", Name: "Other"}
		auth.result.Workspaces = append(auth.result.Workspaces, second)
		h.scanner.result.Workspace = second
		h.extractor.result.Workspace = second
		if code := h.app.Run([]string{"backup", "--workspace-id", "200"}); code != 0 {
			t.Fatalf("code=%d stderr=%q", code, h.stderr.String())
		}
		if h.scanner.workspace.ID != "200" || h.extractor.workspace.ID != "200" {
			t.Fatal("--workspace-id was not reused consistently")
		}
	})
}

func TestBackupRedactsCredentialFromErrors(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, verify.ResultVerified)
	auth := h.app.options.Authenticator.(*backupAuthenticator)
	auth.result.Workspaces[0].Name = h.token
	h.scanner.err = errors.New("provider rejected " + h.token)
	if code := h.app.Run([]string{"backup"}); code == 0 {
		t.Fatal("credential-bearing source error returned exit zero")
	}
	combined := h.stdout.String() + h.stderr.String()
	if strings.Contains(combined, h.token) || !strings.Contains(combined, "[REDACTED]") {
		t.Fatalf("credential was not safely redacted: %q", combined)
	}
}

func TestBackupInterruptionCannotPublishACompletedBackup(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, verify.ResultVerified)
	h.extractor.err = context.Canceled
	if code := h.app.Run([]string{"backup"}); code == 0 {
		t.Fatal("interruption returned exit zero")
	}
	if strings.Contains(h.stdout.String()+h.stderr.String(), "BACKUP COMPLETE") {
		t.Fatal("interrupted work was presented as complete")
	}
	if _, err := os.Stat(h.finalRoot()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted work was published at the completed path")
	}
	if want := []string{"auth", "scan", "extract"}; !reflect.DeepEqual(h.recorder.calls, want) {
		t.Fatalf("pipeline continued after interruption: %v", h.recorder.calls)
	}
}

func TestBackupRejectsIncompleteExportBeforeVerification(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, verify.ResultVerified)
	h.exporter.status = archive.StatusIncomplete
	if code := h.app.Run([]string{"backup"}); code == 0 {
		t.Fatal("INCOMPLETE export returned exit zero")
	}
	if containsString(h.recorder.calls, "verify") || strings.Contains(h.stdout.String()+h.stderr.String(), "BACKUP COMPLETE") {
		t.Fatalf("pipeline=%v stdout=%q stderr=%q", h.recorder.calls, h.stdout.String(), h.stderr.String())
	}
}

func TestBackupHelpUsesEnglishALIHBrandingAndStatesReadOnlyContract(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, verify.ResultVerified)
	if code := h.app.Run([]string{"backup", "--help"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	for _, expected := range []string{"creates a verified portable ClickUp backup", "ALIH", "VERIFIED_WITH_LIMITATIONS", "does not modify source data"} {
		if !strings.Contains(h.stdout.String(), expected) {
			t.Errorf("help missing %q: %q", expected, h.stdout.String())
		}
	}
	if strings.Contains(h.stdout.String(), "ALI H") {
		t.Fatal("help uses split branding")
	}
}

func TestSafeWorkspaceComponentCannotEscapeItsDirectory(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"Example Workspace":    "Example-Workspace",
		"../../Customer\\Data": "Customer-Data",
		"  ":                   "workspace-100",
	} {
		if got := safeWorkspaceComponent(input, "100"); got != want {
			t.Errorf("safeWorkspaceComponent(%q)=%q want=%q", input, got, want)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
