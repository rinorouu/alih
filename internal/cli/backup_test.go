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
	"errors"
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
	coreexporter "alih/internal/exporter"
	"alih/internal/report"
	corereporter "alih/internal/reporter"
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
	recorder *backupRecorder
	status   string
	err      error
	path     string
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
	return archive.Summary{Path: outputPath, Status: s.status}, nil
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
}

func (s *backupReporter) Report(path string) (report.Document, error) {
	s.recorder.call("report")
	s.path = path
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
	app       *App
	stdout    *bytes.Buffer
	stderr    *bytes.Buffer
	recorder  *backupRecorder
	scanner   *backupScanner
	extractor *backupExtractor
	exporter  *backupExporter
	verifier  *backupVerifier
	reporter  *backupReporter
	root      string
	token     string
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
	exporter := &backupExporter{recorder: recorder, status: archive.StatusCreatedUnverified}
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
		app: app, stdout: stdout, stderr: stderr, recorder: recorder, scanner: scanner,
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
	independentVerifier := coreverifier.New()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	app := New(stdout, stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator: authenticator,
		Scanner:       scanner,
		Extractor:     extractor,
		Exporter:      coreexporter.New(nil),
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
