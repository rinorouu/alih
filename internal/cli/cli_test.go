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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/credentials"
	"alih/internal/report"
	"alih/internal/verify"
)

type stubAuthenticator struct {
	result     connector.Authentication
	err        error
	credential string
}

type stubScanner struct {
	result     connector.ScanResult
	err        error
	credential string
	workspace  connector.Workspace
	called     bool
}

type stubExtractor struct {
	result     connector.ExtractionResult
	err        error
	credential string
	workspace  connector.Workspace
	called     bool
}

type stubArchiveExporter struct {
	result       archive.Summary
	err          error
	snapshotPath string
	outputPath   string
	credential   string
}

func (stub *stubArchiveExporter) Export(_ context.Context, snapshotPath, outputPath, credential string) (archive.Summary, error) {
	stub.snapshotPath, stub.outputPath, stub.credential = snapshotPath, outputPath, credential
	return stub.result, stub.err
}

type stubArchiveVerifier struct {
	report verify.Report
	err    error
	path   string
}

func (stub *stubArchiveVerifier) Verify(path string) (verify.Report, error) {
	stub.path = path
	return stub.report, stub.err
}

func (s *stubExtractor) Name() string { return "clickup" }

func (s *stubExtractor) Extract(_ context.Context, credential string, workspace connector.Workspace, sink connector.RawEvidenceSink) (connector.ExtractionResult, error) {
	s.called = true
	s.credential = credential
	s.workspace = workspace
	if err := sink.RecordResponse(connector.RawResponse{
		Operation: "list Spaces", Method: "GET", Path: "/team/100/space",
		Attempt: 1, StatusCode: 200, Body: []byte(`{"spaces":[]}`),
	}); err != nil {
		return connector.ExtractionResult{}, err
	}
	if s.err != nil {
		_ = sink.RecordFailure(connector.RequestFailure{
			Operation: "list Tasks", Method: "GET", Path: "/list/200/task",
			Attempt: 3, StatusCode: 503, Error: "bounded retries exhausted",
		})
		return connector.ExtractionResult{}, s.err
	}
	return s.result, nil
}

func (s *stubScanner) Name() string { return "clickup" }

func (s *stubScanner) Scan(_ context.Context, credential string, workspace connector.Workspace) (connector.ScanResult, error) {
	s.called = true
	s.credential = credential
	s.workspace = workspace
	return s.result, s.err
}

func (s *stubAuthenticator) Name() string { return "clickup" }

func (s *stubAuthenticator) Authenticate(_ context.Context, credential string) (connector.Authentication, error) {
	s.credential = credential
	return s.result, s.err
}

type stubCredentialStore struct {
	loaded      string
	loadErr     error
	saved       string
	saveErr     error
	location    string
	locationErr error
}

func (s *stubCredentialStore) Load() (string, error) { return s.loaded, s.loadErr }
func (s *stubCredentialStore) Save(token string) error {
	s.saved = token
	return s.saveErr
}
func (s *stubCredentialStore) Location() (string, error) { return s.location, s.locationErr }

func TestHelp(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})

	code := app.Run([]string{"--help"})

	if code != 0 {
		t.Fatalf("Run(--help) returned %d, want 0", code)
	}
	for _, expected := range []string{
		"ALIH creates and verifies local, portable SaaS backups.",
		"ClickUp is currently supported through its official read-only API.",
		"Usage:\n  alih <command> [options]\n  alih --help\n  alih --version",
		"version      Print the ALIH version",
		"auth         Authenticate with ClickUp and list accessible Workspaces",
		"scan         Inventory one ClickUp Workspace without modifying it",
		"Get started:",
		"Set ALIH_CLICKUP_TOKEN in your environment",
		"Run \"alih auth\"",
		"Optionally run \"alih scan\"",
		"Run \"alih backup\"",
		"alih <command> --help",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("help output does not contain %q: %q", expected, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(--help) wrote to stderr: %q", stderr.String())
	}
}

func TestNoArgumentsPrintsFirstRunHelp(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})

	if code := app.Run(nil); code != 0 {
		t.Fatalf("Run(nil) returned %d, want 0", code)
	}
	for _, expected := range []string{"Get started:", "alih auth", "alih scan", "alih backup"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("first-run help does not contain %q: %q", expected, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(nil) wrote to stderr: %q", stderr.String())
	}
}

func TestVersionCommandAndFlagDefaultToDevelopmentIdentity(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var stdout, stderr bytes.Buffer
		app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
		if code := app.Run(args); code != 0 {
			t.Fatalf("Run(%v) returned %d, stderr=%q", args, code, stderr.String())
		}
		if got := stdout.String(); got != "alih dev\n" {
			t.Errorf("Run(%v) output=%q, want %q", args, got, "alih dev\n")
		}
		if stderr.Len() != 0 {
			t.Errorf("Run(%v) stderr=%q", args, stderr.String())
		}
	}
}

func TestVersionCommandAndFlagPrintInjectedReleaseIdentity(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var stdout, stderr bytes.Buffer
		app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Version: "0.1.0-alpha.1"})
		if code := app.Run(args); code != 0 {
			t.Fatalf("Run(%v) returned %d, stderr=%q", args, code, stderr.String())
		}
		if got := stdout.String(); got != "alih 0.1.0-alpha.1\n" {
			t.Errorf("Run(%v) output=%q", args, got)
		}
	}
}

func TestVersionRejectsArguments(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	if code := app.Run([]string{"version", "unexpected"}); code != 2 {
		t.Fatalf("code=%d, want 2", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "arguments are not accepted") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestExportHelpDefinesM4Boundary(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	if code := app.Run([]string{"export", "--help"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	for _, expected := range []string{
		"alih export --snapshot PATH [--output PATH]", "completed M3", "alih.db", "manifest.json",
		"INCOMPLETE", "CREATED_UNVERIFIED", "verification is not implemented",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("help missing %q: %q", expected, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestExportPrintsCreatedUnverifiedArchive(t *testing.T) {
	t.Parallel()
	const token = "saved-export-token"
	exporter := &stubArchiveExporter{result: archive.Summary{
		Path: "/tmp/alih-export", Status: archive.StatusCreatedUnverified,
		Inventory: map[string]archive.EntityCount{"tasks": {Expected: 2, Archived: 2}, "attachments": {Expected: 1, Archived: 1}},
		Observed:  map[string]int{"workspaces": 1, "identities": 2},
	}}
	var stdout, stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Exporter: exporter, CredentialStore: &stubCredentialStore{loaded: token},
	})
	if code := app.Run([]string{"export", "--snapshot", "/tmp/m3", "--output", "/tmp/alih-export"}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if exporter.snapshotPath != "/tmp/m3" || exporter.outputPath != "/tmp/alih-export" || exporter.credential != token {
		t.Fatal("export arguments not forwarded")
	}
	for _, expected := range []string{"PORTABLE ARCHIVE", "Status: CREATED_UNVERIFIED", "Verification: NOT_RUN", "tasks", "expected=2 archived=2", "No M5 verification was performed"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("stdout missing %q: %q", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String()+stderr.String(), token) {
		t.Fatal("token exposed")
	}
}

func TestExportIncompleteIsNotCleanSuccess(t *testing.T) {
	t.Parallel()
	exporter := &stubArchiveExporter{result: archive.Summary{
		Path: "/tmp/alih-export", Status: archive.StatusIncomplete,
		Inventory: map[string]archive.EntityCount{"attachments": {Expected: 1, Archived: 0, Unresolved: 1}},
		Observed:  map[string]int{},
	}}
	var stdout, stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Exporter: exporter, CredentialStore: &stubCredentialStore{loaded: "token"},
	})
	if code := app.Run([]string{"export", "--snapshot", "/tmp/m3"}); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stdout.String(), "Status: INCOMPLETE") || !strings.Contains(stderr.String(), "expected supported attachments were unresolved") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestAuthHelpExplainsEnvironmentAndSavedCredentialPaths(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})

	code := app.Run([]string{"auth", "--help"})

	if code != 0 {
		t.Fatalf("Run(auth --help) returned %d, want 0", code)
	}
	for _, expected := range []string{
		"Usage:\n  alih auth",
		"provide ALIH_CLICKUP_TOKEN in the process environment",
		"Later runs load the saved credential",
		"never accepted as a command-line argument",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("auth help does not contain %q: %q", expected, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(auth --help) wrote to stderr: %q", stderr.String())
	}
}

func TestUnknownCommandFails(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})

	code := app.Run([]string{"restore"})

	if code != 2 {
		t.Fatalf("Run(restore) returned %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unknown command "restore"`) {
		t.Fatalf("Run(restore) stderr = %q", stderr.String())
	}
}

func TestScanHelpExplainsWorkspaceSelectionAndCredential(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})

	if code := app.Run([]string{"scan", "--help"}); code != 0 {
		t.Fatalf("Run(scan --help) returned %d, want 0", code)
	}
	for _, expected := range []string{
		"alih scan [--workspace-id ID]",
		"If exactly one Workspace is accessible",
		"ALIH_CLICKUP_TOKEN",
		"does not create an archive",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("scan help does not contain %q: %q", expected, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(scan --help) wrote to stderr: %q", stderr.String())
	}
}

func TestExtractHelpDefinesM3BoundaryAndFailurePath(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})

	if code := app.Run([]string{"extract", "--help"}); code != 0 {
		t.Fatalf("Run(extract --help) returned %d, want 0", code)
	}
	for _, expected := range []string{
		"alih extract --output PATH [--workspace-id ID]",
		"PATH.failed", "never stored in the snapshot", "source-ID inventory",
		"no normalized model, SQLite database, manifest", "attachment binaries",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("extract help does not contain %q: %q", expected, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(extract --help) wrote to stderr: %q", stderr.String())
	}
}

func TestExtractCreatesCompleteRawSnapshotUsingSavedCredential(t *testing.T) {
	t.Parallel()

	const token = "saved-private-token"
	workspace := connector.Workspace{ID: "100", Name: "Primary"}
	target := filepath.Join(t.TempDir(), "m3-raw")
	extractor := &stubExtractor{result: connector.ExtractionResult{
		ScanResult:    connector.ScanResult{Workspace: workspace},
		SourceObjects: []connector.SourceObject{{Type: "workspace", ID: workspace.ID}},
	}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator: &stubAuthenticator{result: connector.Authentication{Workspaces: []connector.Workspace{workspace}}},
		Extractor:     extractor, CredentialStore: &stubCredentialStore{loaded: token},
	})

	if code := app.Run([]string{"extract", "--output", target}); code != 0 {
		t.Fatalf("Run(extract) returned %d, stderr = %q", code, stderr.String())
	}
	if extractor.credential != token || extractor.workspace != workspace {
		t.Fatal("extract did not receive the selected Workspace and saved credential")
	}
	for _, path := range []string{"run.json", "requests.json", "inventory.json", filepath.Join("raw", "000001.json")} {
		if _, err := os.Stat(filepath.Join(target, path)); err != nil {
			t.Errorf("snapshot file %s: %v", path, err)
		}
	}
	for _, expected := range []string{
		"CLICKUP RAW EXTRACTION", "Workspace: Primary (ID: 100)", "Logical inventory digest: sha256:",
		"Raw extraction complete.", "does not provide an atomic snapshot", "No portable model", "No source data modified.",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("extract output does not contain %q: %q", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String()+stderr.String(), token) {
		t.Fatal("extract output exposed the credential")
	}
}

func TestExtractFailureCreatesFailedEvidenceWithoutCredential(t *testing.T) {
	t.Parallel()

	const token = "failed-private-token"
	workspace := connector.Workspace{ID: "100", Name: "Primary"}
	target := filepath.Join(t.TempDir(), "m3-raw")
	extractor := &stubExtractor{err: errors.New("API failed for " + token)}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator: &stubAuthenticator{result: connector.Authentication{Workspaces: []connector.Workspace{workspace}}},
		Extractor:     extractor, CredentialStore: &stubCredentialStore{loaded: token},
	})

	if code := app.Run([]string{"extract", "--output", target}); code != 1 {
		t.Fatalf("Run(extract) returned %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("failed extract printed success output: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), token) || !strings.Contains(stderr.String(), "extraction FAILED") ||
		!strings.Contains(stderr.String(), target+".failed") {
		t.Fatalf("failed extract stderr = %q", stderr.String())
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("complete target exists after failed extraction: %v", err)
	}
	err := filepath.WalkDir(target+".failed", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(content), token) {
			t.Errorf("credential found in failed evidence file %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestScanUsesSavedCredentialAndPrintsCompletedInventory(t *testing.T) {
	t.Parallel()

	const token = "saved-secret"
	workspace := connector.Workspace{ID: "100", Name: "Primary"}
	authenticator := &stubAuthenticator{result: connector.Authentication{
		Identity:   connector.Identity{ID: "1", Name: "User"},
		Workspaces: []connector.Workspace{workspace},
	}}
	scanner := &stubScanner{result: connector.ScanResult{
		Workspace: workspace,
		Inventory: connector.Inventory{
			Spaces: 1, Folders: 2, Lists: 3, Tasks: 4, Subtasks: 5,
			Comments: 6, Attachments: 7, CustomFields: 8, Relationships: 9,
		},
		Capabilities: []connector.Capability{
			{Name: "Tasks/subtasks", State: connector.CapabilitySupported, Note: "complete traversal"},
			{Name: "Docs", State: connector.CapabilityPartial, Note: "outside M2 inventory"},
		},
	}}
	store := &stubCredentialStore{loaded: token, location: "/secure/credentials.json"}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator:   authenticator,
		Scanner:         scanner,
		CredentialStore: store,
	})

	if code := app.Run([]string{"scan"}); code != 0 {
		t.Fatalf("Run(scan) returned %d, stderr = %q", code, stderr.String())
	}
	if authenticator.credential != token || scanner.credential != token {
		t.Fatal("saved credential was not used for authentication and scan")
	}
	if scanner.workspace != workspace {
		t.Fatalf("scanned Workspace = %#v, want %#v", scanner.workspace, workspace)
	}
	for _, expected := range []string{
		"ALIH — CLICKUP SCAN",
		"Workspace: Primary (ID: 100)",
		"Spaces                 1",
		"Subtasks               5",
		"Task comments          6",
		"Task relationships     9",
		"Tasks/subtasks         SUPPORTED",
		"Docs                   PARTIAL",
		"All supported M2 traversals and pagination completed",
		"does not provide an atomic snapshot",
		"No archive or portability-completeness claim was made.",
		"No source data modified.",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("scan stdout does not contain %q: %q", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
		t.Fatal("scan output exposed the credential")
	}
}

func TestScanRequiresWorkspaceIDWhenMultipleAreAccessible(t *testing.T) {
	t.Parallel()

	authenticator := &stubAuthenticator{result: connector.Authentication{Workspaces: []connector.Workspace{
		{ID: "100", Name: "One"},
		{ID: "200", Name: "Two"},
	}}}
	scanner := &stubScanner{}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator:   authenticator,
		Scanner:         scanner,
		CredentialStore: &stubCredentialStore{loaded: "token"},
	})

	if code := app.Run([]string{"scan"}); code != 1 {
		t.Fatalf("Run(scan) returned %d, want 1", code)
	}
	if scanner.called {
		t.Fatal("scanner ran without an explicit selection among multiple Workspaces")
	}
	for _, expected := range []string{"multiple Workspaces", "--workspace-id ID", "One (ID: 100)", "Two (ID: 200)"} {
		if !strings.Contains(stderr.String(), expected) {
			t.Errorf("selection error does not contain %q: %q", expected, stderr.String())
		}
	}
}

func TestScanUsesRequestedAccessibleWorkspace(t *testing.T) {
	t.Parallel()

	selected := connector.Workspace{ID: "200", Name: "Two"}
	authenticator := &stubAuthenticator{result: connector.Authentication{Workspaces: []connector.Workspace{
		{ID: "100", Name: "One"}, selected,
	}}}
	scanner := &stubScanner{result: connector.ScanResult{Workspace: selected}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator:   authenticator,
		Scanner:         scanner,
		CredentialStore: &stubCredentialStore{loaded: "token"},
	})

	if code := app.Run([]string{"scan", "--workspace-id", "200"}); code != 0 {
		t.Fatalf("Run(scan --workspace-id) returned %d, stderr = %q", code, stderr.String())
	}
	if scanner.workspace != selected {
		t.Fatalf("scanner Workspace = %#v, want %#v", scanner.workspace, selected)
	}
}

func TestScanFailurePrintsNoPartialInventory(t *testing.T) {
	t.Parallel()

	const token = "private-token"
	workspace := connector.Workspace{ID: "100", Name: "Primary"}
	scanner := &stubScanner{err: errors.New("ClickUp API failed while fetching task page 2")}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator:   &stubAuthenticator{result: connector.Authentication{Workspaces: []connector.Workspace{workspace}}},
		Scanner:         scanner,
		CredentialStore: &stubCredentialStore{loaded: token},
	})

	if code := app.Run([]string{"scan"}); code != 1 {
		t.Fatalf("Run(scan) returned %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("failed scan printed partial output: %q", stdout.String())
	}
	for _, expected := range []string{"task page 2", "inventory FAILED", "cannot prove this source inventory is complete"} {
		if !strings.Contains(stderr.String(), expected) {
			t.Errorf("failure output does not contain %q: %q", expected, stderr.String())
		}
	}
	if strings.Contains(stderr.String(), token) {
		t.Fatal("failed scan exposed the credential")
	}
}

func TestAuthVerifiesEnvironmentTokenSavesAndListsWorkspaces(t *testing.T) {
	t.Parallel()

	const token = "pk_secret_value"
	authenticator := &stubAuthenticator{result: connector.Authentication{
		Identity: connector.Identity{ID: "42", Name: "Local Developer"},
		Workspaces: []connector.Workspace{
			{ID: "100", Name: "Primary"},
			{ID: "200", Name: ""},
		},
	}}
	store := &stubCredentialStore{location: "/local/config/alih/credentials.json"}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator:       authenticator,
		CredentialStore:     store,
		EnvironmentToken:    token,
		EnvironmentTokenSet: true,
	})

	code := app.Run([]string{"auth"})

	if code != 0 {
		t.Fatalf("Run(auth) returned %d, stderr = %q", code, stderr.String())
	}
	if authenticator.credential != token || store.saved != token {
		t.Fatal("environment credential was not used and saved")
	}
	credentialProtection := "plaintext, protected by permissions 0600"
	if runtime.GOOS == "windows" {
		credentialProtection = "plaintext, stored under your Windows user profile"
	}
	for _, expected := range []string{
		"Authenticated with ClickUp as Local Developer (ID: 42)",
		"Accessible Workspaces (2):",
		"- Primary (ID: 100)",
		"- <unnamed> (ID: 200)",
		credentialProtection,
		"No source data modified.",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("stdout does not contain %q: %q", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
		t.Fatal("auth output exposed the token")
	}
}

func TestAuthLoadsSavedTokenWithoutSavingAgain(t *testing.T) {
	t.Parallel()

	store := &stubCredentialStore{loaded: "saved-token", location: "/secure/credentials.json"}
	authenticator := &stubAuthenticator{result: connector.Authentication{
		Identity: connector.Identity{ID: "1", Name: "User"},
	}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator:   authenticator,
		CredentialStore: store,
	})

	if code := app.Run([]string{"auth"}); code != 0 {
		t.Fatalf("Run(auth) returned %d, stderr = %q", code, stderr.String())
	}
	if authenticator.credential != "saved-token" {
		t.Fatalf("Authenticate credential = %q", authenticator.credential)
	}
	if store.saved != "" {
		t.Fatal("saved credential was unnecessarily rewritten")
	}
	if !strings.Contains(stdout.String(), "Accessible Workspaces: none returned by ClickUp.") {
		t.Fatalf("empty Workspace state was not explicit: %q", stdout.String())
	}
}

func TestAuthFailureDoesNotSaveCredentialOrPrintPartialSuccess(t *testing.T) {
	t.Parallel()

	const token = "rejected-secret"
	authenticator := &stubAuthenticator{err: errors.New("ClickUp rejected the personal token")}
	store := &stubCredentialStore{location: "/secure/credentials.json"}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator:       authenticator,
		CredentialStore:     store,
		EnvironmentToken:    token,
		EnvironmentTokenSet: true,
	})

	if code := app.Run([]string{"auth"}); code != 1 {
		t.Fatalf("Run(auth) returned %d, want 1", code)
	}
	if store.saved != "" {
		t.Fatal("rejected credential was saved")
	}
	if stdout.Len() != 0 {
		t.Fatalf("failed auth printed partial success: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "alih auth: ClickUp rejected the personal token") {
		t.Fatalf("authentication failure is not clear: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), token) {
		t.Fatal("failed auth exposed the token")
	}
}

func TestAuthReportsMissingCredential(t *testing.T) {
	t.Parallel()

	store := &stubCredentialStore{loadErr: credentials.ErrNotConfigured}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator:   &stubAuthenticator{},
		CredentialStore: store,
	})

	if code := app.Run([]string{"auth"}); code != 1 {
		t.Fatalf("Run(auth) returned %d, want 1", code)
	}
	for _, expected := range []string{"Alih is not authenticated", "Set ALIH_CLICKUP_TOKEN", `run "alih auth"`} {
		if !strings.Contains(stderr.String(), expected) {
			t.Fatalf("missing credential error does not contain %q: %q", expected, stderr.String())
		}
	}
	if strings.Contains(stderr.String(), "ErrNotConfigured") {
		t.Fatalf("missing credential error is not explicit: %q", stderr.String())
	}
}

func TestAuthRejectsArgumentsWithoutEchoingThem(t *testing.T) {
	t.Parallel()

	const accidentalToken = "pk_accidental_secret"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})

	if code := app.Run([]string{"auth", accidentalToken}); code != 2 {
		t.Fatalf("Run(auth token) returned %d, want 2", code)
	}
	if strings.Contains(stderr.String(), accidentalToken) {
		t.Fatal("rejected command-line argument was echoed")
	}
}

func TestVerifyReportsAProvenArchiveWithoutHidingItsLimitations(t *testing.T) {
	t.Parallel()

	verifier := &stubArchiveVerifier{report: verify.Report{
		ArchivePath: "/archives/example", Result: verify.ResultVerifiedWithLimitations,
		ArchiveStatus: "CREATED_UNVERIFIED", Connector: "clickup",
		Source: connector.Workspace{ID: "w1", Name: "Example"},
		Checks: []verify.Check{
			{Name: "file_checksums", Status: verify.CheckPass, Summary: "checksums match"},
			{Name: "limitation_preservation", Status: verify.CheckUnproven, Summary: "capabilities remain limited", Findings: []string{"Docs remains PARTIAL"}},
		},
		Reconciliation: []verify.Reconciliation{{Entity: "tasks", Expected: 6, Archived: 6, Status: verify.CheckPass}},
		Capabilities:   []connector.Capability{{Name: "Docs", State: connector.CapabilityPartial, Note: "outside inventory"}},
		Limitations:    []string{"Point-in-time consistency is not claimed."},
	}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Verifier: verifier})

	if code := app.Run([]string{"verify", "--archive", "/archives/example"}); code != 0 {
		t.Fatalf("Run(verify) returned %d, stderr = %q", code, stderr.String())
	}
	if verifier.path != "/archives/example" {
		t.Fatalf("verifier received path %q", verifier.path)
	}
	for _, expected := range []string{
		"ALIH — VERIFICATION",
		"VERIFIED_WITH_LIMITATIONS",
		"limitation_preservation",
		"Docs remains PARTIAL",
		"Point-in-time consistency is not claimed.",
		"Docs                   PARTIAL",
		"No source data modified. No archive data modified.",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("verification output does not contain %q:\n%s", expected, stdout.String())
		}
	}
}

func TestVerifyFailsClosedOnACorruptArchive(t *testing.T) {
	t.Parallel()

	verifier := &stubArchiveVerifier{report: verify.Report{
		ArchivePath: "/archives/broken", Result: verify.ResultFailed,
		Checks: []verify.Check{{
			Name: "attachment_integrity", Status: verify.CheckFail,
			Summary:  "archived attachment binaries do not match the evidence recorded for them",
			Findings: []string{"attachment binary attachments/a.png is missing from the archive"},
		}},
	}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Verifier: verifier})

	if code := app.Run([]string{"verify", "--archive", "/archives/broken"}); code != 1 {
		t.Fatalf("Run(verify) returned %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), "ALIH cannot prove this archive is complete or intact.") {
		t.Fatalf("failure was not stated plainly: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "archive result is FAILED") {
		t.Fatalf("stderr does not report the failure: %q", stderr.String())
	}
}

func TestVerifyIncompleteArchiveExitsNonZero(t *testing.T) {
	t.Parallel()

	verifier := &stubArchiveVerifier{report: verify.Report{Result: verify.ResultIncomplete}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Verifier: verifier})

	if code := app.Run([]string{"verify", "--archive", "/archives/incomplete"}); code != 1 {
		t.Fatalf("Run(verify) returned %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), "This is not a verified archive.") {
		t.Fatalf("INCOMPLETE was not distinguished from VERIFIED: %q", stdout.String())
	}
}

func TestVerifyRequiresAnArchivePath(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Verifier: &stubArchiveVerifier{}})

	if code := app.Run([]string{"verify"}); code != 2 {
		t.Fatalf("Run(verify) returned %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--archive PATH is required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if code := app.Run([]string{"verify", "--help"}); code != 0 {
		t.Fatalf("Run(verify --help) returned %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "never writes to the archive under test") {
		t.Fatalf("verify help does not state its read-only guarantee: %q", stdout.String())
	}
}

func TestVerifyAcceptsAPositionalArchivePath(t *testing.T) {
	t.Parallel()

	verifier := &stubArchiveVerifier{report: verify.Report{Result: verify.ResultVerified}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Verifier: verifier})

	if code := app.Run([]string{"verify", "./alih-example"}); code != 0 {
		t.Fatalf("Run(verify path) returned %d, stderr = %q", code, stderr.String())
	}
	if verifier.path != "./alih-example" {
		t.Fatalf("verifier received path %q", verifier.path)
	}
}

type stubArchiveReporter struct {
	document report.Document
	err      error
	path     string
}

func (stub *stubArchiveReporter) Report(path string) (report.Document, error) {
	stub.path = path
	return stub.document, stub.err
}

func verifiedReportDocument() report.Document {
	return report.Document{
		SchemaVersion: report.SchemaVersion, Kind: report.Kind, AlihVersion: "0.0.1",
		GeneratedAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
		Identity: report.Identity{
			ArchivePath: "/archives/example", Connector: "clickup",
			WorkspaceID: "w1", WorkspaceName: "Example", ManifestReadable: true,
		},
		Verification: report.Verification{Result: verify.ResultVerifiedWithLimitations, Headline: "Verification passed within the supported scope."},
		Recovery:     []report.Statement{{Claim: "alih.db can be queried locally.", Proven: true, BasedOn: []string{"sqlite_integrity"}}},
		Capabilities: []report.Capability{{Name: "Docs", State: "PARTIAL", Note: "outside inventory", RecoveryMeaning: "Only partially represented."}},
		Conclusion: report.Conclusion{
			Result:  verify.ResultVerifiedWithLimitations,
			Verdict: "Archive integrity is proven within Alih's supported scope.",
		},
	}
}

func TestReportPrintsAllNineSectionsAsText(t *testing.T) {
	t.Parallel()

	reporter := &stubArchiveReporter{document: verifiedReportDocument()}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Reporter: reporter})

	if code := app.Run([]string{"report", "--archive", "/archives/example"}); code != 0 {
		t.Fatalf("Run(report) returned %d, stderr = %q", code, stderr.String())
	}
	if reporter.path != "/archives/example" {
		t.Fatalf("reporter received %q", reporter.path)
	}
	for _, section := range []string{
		"ALIH — RECOVERY REPORT", "1. ARCHIVE IDENTITY", "2. VERIFICATION STATUS",
		"3. RECOVERY SUMMARY", "4. ENTITY COVERAGE", "5. ATTACHMENTS",
		"6. CAPABILITY COVERAGE", "7. LIMITATIONS AND UNPROVEN CLAIMS",
		"8. DISCREPANCIES AND UNRESOLVED ITEMS", "9. RECOVERY CONCLUSION",
		"No source data modified. No archive data modified.",
	} {
		if !strings.Contains(stdout.String(), section) {
			t.Errorf("report output is missing %q", section)
		}
	}
}

func TestReportWritesSelfContainedHTMLBesideTheArchive(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	archivePath := filepath.Join(directory, "alih-example")
	if err := os.Mkdir(archivePath, 0o700); err != nil {
		t.Fatal(err)
	}
	document := verifiedReportDocument()
	document.Identity.ArchivePath = archivePath
	reporter := &stubArchiveReporter{document: document}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Reporter: reporter})

	if code := app.Run([]string{"report", "--archive", archivePath, "--format", "html"}); code != 0 {
		t.Fatalf("Run(report --format html) returned %d, stderr = %q", code, stderr.String())
	}
	written := archivePath + ".report.html"
	content, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("report.html was not written beside the archive: %v", err)
	}
	if !strings.Contains(string(content), "<!doctype html>") || !strings.Contains(string(content), "Recovery Report") {
		t.Fatal("the written file is not an HTML recovery report")
	}
	// The archive itself must be untouched: adding a file would break the
	// manifest checksums that "alih verify" relies on.
	entries, err := os.ReadDir(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("reporting wrote into the archive: %#v", entries)
	}
	if !strings.Contains(stdout.String(), "Recovery report written:") {
		t.Fatalf("stdout did not name the written report: %q", stdout.String())
	}
}

func TestReportRefusesToWriteInsideTheArchive(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	archivePath := filepath.Join(directory, "alih-example")
	if err := os.Mkdir(archivePath, 0o700); err != nil {
		t.Fatal(err)
	}
	document := verifiedReportDocument()
	document.Identity.ArchivePath = archivePath
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Reporter: &stubArchiveReporter{document: document},
	})

	code := app.Run([]string{"report", "--archive", archivePath, "--format", "html", "--output", filepath.Join(archivePath, "report.html")})
	if code != 2 {
		t.Fatalf("Run(report --output inside archive) returned %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "must not be inside the archive") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(archivePath, "report.html")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a report was written into the archive anyway")
	}
}

func TestReportEmitsMachineReadableDocument(t *testing.T) {
	t.Parallel()

	reporter := &stubArchiveReporter{document: verifiedReportDocument()}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Reporter: reporter})

	if code := app.Run([]string{"report", "--archive", "/archives/example", "--format", "json"}); code != 0 {
		t.Fatalf("Run(report --format json) returned %d, stderr = %q", code, stderr.String())
	}
	var decoded report.Document
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not a machine-readable report: %v", err)
	}
	if decoded.Kind != report.Kind || decoded.Conclusion.Result != verify.ResultVerifiedWithLimitations {
		t.Fatalf("decoded document = %#v", decoded.Conclusion)
	}
}

func TestReportForACorruptArchiveExitsNonZeroAndStatesTheFailure(t *testing.T) {
	t.Parallel()

	document := verifiedReportDocument()
	document.Verification = report.Verification{Result: verify.ResultFailed, Headline: "Verification failed: Alih cannot prove this archive is intact."}
	document.Conclusion = report.Conclusion{
		Result:     verify.ResultFailed,
		Verdict:    "Alih cannot prove this archive is intact. Do not rely on it for recovery.",
		Statements: []string{"Do not use this archive as a recovery source until the failures above are explained."},
	}
	reporter := &stubArchiveReporter{document: document}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Reporter: reporter})

	if code := app.Run([]string{"report", "--archive", "/archives/broken"}); code != 1 {
		t.Fatalf("Run(report) on a corrupt archive returned %d, want 1", code)
	}
	// The report is still produced, and it says plainly what it is.
	if !strings.Contains(stdout.String(), "Do not rely on it for recovery") {
		t.Fatalf("the failure was not stated in the report: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "archive result is FAILED") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestReportRejectsUnknownFormatAndMissingArchive(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Reporter: &stubArchiveReporter{document: verifiedReportDocument()},
	})

	if code := app.Run([]string{"report"}); code != 2 {
		t.Fatalf("Run(report) with no archive returned %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--archive PATH is required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if code := app.Run([]string{"report", "--archive", "/a", "--format", "pdf"}); code != 2 {
		t.Fatalf("Run(report --format pdf) returned %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unknown --format "pdf"`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if code := app.Run([]string{"report", "--help"}); code != 0 {
		t.Fatalf("Run(report --help) returned %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "neither modifies nor repairs the archive") {
		t.Fatalf("report help does not state its guarantees: %q", stdout.String())
	}
}
