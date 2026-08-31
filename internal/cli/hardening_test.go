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
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/credentials"
	"alih/internal/report"
	"alih/internal/verify"
)

func runCLI(t *testing.T, options Options, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	app := New(&stdout, &stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), options)
	code := app.Run(args)
	return code, stdout.String(), stderr.String()
}

// TestExitCodeMatrix pins the contract every caller and script depends on:
// 0 means the command made the claim it was asked to make, 1 means Alih could
// not, and 2 means the invocation itself was wrong. A failure must never be
// reported as 0.
func TestExitCodeMatrix(t *testing.T) {
	t.Parallel()

	verified := report.Document{Conclusion: report.Conclusion{Result: verify.ResultVerified}}
	incomplete := report.Document{Conclusion: report.Conclusion{Result: verify.ResultIncomplete}}
	workspace := connector.Workspace{ID: "w1", Name: "W"}
	authenticated := connector.Authentication{
		Identity: connector.Identity{ID: "u1", Name: "User"}, Workspaces: []connector.Workspace{workspace},
	}

	cases := []struct {
		name    string
		args    []string
		options Options
		want    int
	}{
		// Usage errors: exit 2.
		{"no command prints help", []string{}, Options{}, 0},
		{"unknown command", []string{"restore"}, Options{}, 2},
		{"unknown flag", []string{"scan", "--nope"}, Options{}, 2},
		{"scan rejects positional arguments", []string{"scan", "extra"}, Options{}, 2},
		{"extract without output", []string{"extract"}, Options{}, 2},
		{"extract rejects positional arguments", []string{"extract", "--output", "x", "extra"}, Options{}, 2},
		{"export without snapshot", []string{"export"}, Options{}, 2},
		{"export with empty output", []string{"export", "--snapshot", "s", "--output", " "}, Options{}, 2},
		{"verify without archive", []string{"verify"}, Options{Verifier: &stubArchiveVerifier{}}, 2},
		{"verify with two paths", []string{"verify", "--archive", "a", "b"}, Options{Verifier: &stubArchiveVerifier{}}, 2},
		{"report without archive", []string{"report"}, Options{Reporter: &stubArchiveReporter{}}, 2},
		{"report with an unknown format", []string{"report", "--archive", "a", "--format", "pdf"}, Options{Reporter: &stubArchiveReporter{}}, 2},
		{"auth rejects arguments", []string{"auth", "token"}, Options{}, 2},

		// Help is a successful invocation of every command.
		{"help", []string{"--help"}, Options{}, 0},
		{"auth help", []string{"auth", "--help"}, Options{}, 0},
		{"scan help", []string{"scan", "--help"}, Options{}, 0},
		{"extract help", []string{"extract", "--help"}, Options{}, 0},
		{"export help", []string{"export", "--help"}, Options{}, 0},
		{"verify help", []string{"verify", "--help"}, Options{}, 0},
		{"report help", []string{"report", "--help"}, Options{}, 0},

		// Missing dependencies are an operational failure, not a usage error.
		{"scan without dependencies", []string{"scan"}, Options{}, 1},
		{"export without dependencies", []string{"export", "--snapshot", "s"}, Options{}, 1},
		{"verify without a verifier", []string{"verify", "--archive", "a"}, Options{}, 1},
		{"report without a reporter", []string{"report", "--archive", "a"}, Options{}, 1},

		// Credential problems: exit 1, never a silent success.
		{"auth with no stored credential", []string{"auth"}, Options{
			Authenticator: &stubAuthenticator{}, CredentialStore: &stubCredentialStore{loadErr: credentials.ErrNotConfigured},
		}, 1},
		{"auth with a malformed environment token", []string{"auth"}, Options{
			Authenticator: &stubAuthenticator{}, CredentialStore: &stubCredentialStore{},
			EnvironmentToken: "bad\ntoken", EnvironmentTokenSet: true,
		}, 1},
		{"auth with an unreadable credential store", []string{"auth"}, Options{
			Authenticator: &stubAuthenticator{}, CredentialStore: &stubCredentialStore{loadErr: errors.New("permissions too broad")},
		}, 1},
		{"scan when the source rejects the token", []string{"scan"}, Options{
			Authenticator: &stubAuthenticator{err: errors.New("rejected")},
			Scanner:       &stubScanner{}, CredentialStore: &stubCredentialStore{loaded: "t"},
		}, 1},

		// Source failures: exit 1.
		{"scan when the traversal fails", []string{"scan"}, Options{
			Authenticator:   &stubAuthenticator{result: authenticated},
			Scanner:         &stubScanner{err: errors.New("pagination did not terminate")},
			CredentialStore: &stubCredentialStore{loaded: "t"},
		}, 1},
		{"scan when several workspaces need a choice", []string{"scan"}, Options{
			Authenticator: &stubAuthenticator{result: connector.Authentication{
				Workspaces: []connector.Workspace{{ID: "a"}, {ID: "b"}},
			}},
			Scanner: &stubScanner{}, CredentialStore: &stubCredentialStore{loaded: "t"},
		}, 1},
		{"scan when no workspace is accessible", []string{"scan"}, Options{
			Authenticator:   &stubAuthenticator{result: connector.Authentication{}},
			Scanner:         &stubScanner{},
			CredentialStore: &stubCredentialStore{loaded: "t"},
		}, 1},
		{"scan with an inaccessible workspace id", []string{"scan", "--workspace-id", "zz"}, Options{
			Authenticator:   &stubAuthenticator{result: authenticated},
			Scanner:         &stubScanner{},
			CredentialStore: &stubCredentialStore{loaded: "t"},
		}, 1},

		// Archive outcomes.
		{"export failure", []string{"export", "--snapshot", "s"}, Options{
			Exporter: &stubArchiveExporter{err: errors.New("normalize failed")}, CredentialStore: &stubCredentialStore{loaded: "t"},
		}, 1},
		{"export incomplete", []string{"export", "--snapshot", "s"}, Options{
			Exporter:        &stubArchiveExporter{result: archive.Summary{Path: "p", Status: archive.StatusIncomplete}},
			CredentialStore: &stubCredentialStore{loaded: "t"},
		}, 1},
		{"export unexpected status", []string{"export", "--snapshot", "s"}, Options{
			Exporter:        &stubArchiveExporter{result: archive.Summary{Path: "p", Status: "SOMETHING_ELSE"}},
			CredentialStore: &stubCredentialStore{loaded: "t"},
		}, 1},
		{"export success", []string{"export", "--snapshot", "s"}, Options{
			Exporter:        &stubArchiveExporter{result: archive.Summary{Path: "p", Status: archive.StatusCreatedUnverified}},
			CredentialStore: &stubCredentialStore{loaded: "t"},
		}, 0},

		// Verification outcomes.
		{"verify verified", []string{"verify", "--archive", "a"}, Options{
			Verifier: &stubArchiveVerifier{report: verify.Report{Result: verify.ResultVerified}},
		}, 0},
		{"verify with limitations", []string{"verify", "--archive", "a"}, Options{
			Verifier: &stubArchiveVerifier{report: verify.Report{Result: verify.ResultVerifiedWithLimitations}},
		}, 0},
		{"verify incomplete", []string{"verify", "--archive", "a"}, Options{
			Verifier: &stubArchiveVerifier{report: verify.Report{Result: verify.ResultIncomplete}},
		}, 1},
		{"verify failed", []string{"verify", "--archive", "a"}, Options{
			Verifier: &stubArchiveVerifier{report: verify.Report{Result: verify.ResultFailed}},
		}, 1},
		{"verify cannot start", []string{"verify", "--archive", "a"}, Options{
			Verifier: &stubArchiveVerifier{err: errors.New("not a directory")},
		}, 1},

		// Reporting outcomes.
		{"report on a verified archive", []string{"report", "--archive", "a"}, Options{
			Reporter: &stubArchiveReporter{document: verified},
		}, 0},
		{"report on an incomplete archive", []string{"report", "--archive", "a"}, Options{
			Reporter: &stubArchiveReporter{document: incomplete},
		}, 1},
		{"report cannot be produced", []string{"report", "--archive", "a"}, Options{
			Reporter: &stubArchiveReporter{err: errors.New("archive missing")},
		}, 1},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			code, stdout, stderr := runCLI(t, testCase.options, testCase.args...)
			if code != testCase.want {
				t.Fatalf("exit code = %d, want %d\nstdout: %s\nstderr: %s", code, testCase.want, stdout, stderr)
			}
			if code != 0 && stderr == "" {
				t.Error("a non-zero exit produced no explanation on stderr")
			}
		})
	}
}

func TestAuthenticatedCommandsExplainHowToConfigureMissingAuthentication(t *testing.T) {
	t.Parallel()

	missing := &stubCredentialStore{loadErr: credentials.ErrNotConfigured}
	cases := []struct {
		name    string
		args    []string
		options Options
	}{
		{"auth", []string{"auth"}, Options{Authenticator: &stubAuthenticator{}, CredentialStore: missing}},
		{"scan", []string{"scan"}, Options{Authenticator: &stubAuthenticator{}, Scanner: &stubScanner{}, CredentialStore: missing}},
		{"extract", []string{"extract", "--output", "snapshot"}, Options{Authenticator: &stubAuthenticator{}, Extractor: &stubExtractor{}, CredentialStore: missing}},
		{"export", []string{"export", "--snapshot", "snapshot"}, Options{Exporter: &stubArchiveExporter{}, CredentialStore: missing}},
		{"backup", []string{"backup"}, Options{
			Authenticator: &stubAuthenticator{}, Scanner: &stubScanner{}, Extractor: &stubExtractor{},
			Exporter: &stubArchiveExporter{}, Verifier: &stubArchiveVerifier{}, Reporter: &stubArchiveReporter{},
			CredentialStore: missing,
		}},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			code, stdout, stderr := runCLI(t, testCase.options, testCase.args...)
			if code != 1 {
				t.Fatalf("Run(%v) returned %d, stdout=%q stderr=%q", testCase.args, code, stdout, stderr)
			}
			for _, expected := range []string{"Alih is not authenticated", "Set ALIH_CLICKUP_TOKEN", `run "alih auth"`} {
				if !strings.Contains(stderr, expected) {
					t.Errorf("stderr does not contain %q: %q", expected, stderr)
				}
			}
		})
	}
}

// TestFailingCommandsNeverPrintASuccessClaim guards against a failure that
// still reads like a success on stdout.
func TestFailingCommandsNeverPrintASuccessClaim(t *testing.T) {
	t.Parallel()

	forbidden := []string{"VERIFIED\n", "Scan complete", "Raw extraction complete", "No silent omissions"}
	cases := []struct {
		name    string
		args    []string
		options Options
	}{
		{"scan failure", []string{"scan"}, Options{
			Authenticator:   &stubAuthenticator{result: connector.Authentication{Workspaces: []connector.Workspace{{ID: "w1"}}}},
			Scanner:         &stubScanner{err: errors.New("traversal failed")},
			CredentialStore: &stubCredentialStore{loaded: "t"},
		}},
		{"export failure", []string{"export", "--snapshot", "s"}, Options{
			Exporter: &stubArchiveExporter{err: errors.New("failed")}, CredentialStore: &stubCredentialStore{loaded: "t"},
		}},
		{"verify failure", []string{"verify", "--archive", "a"}, Options{
			Verifier: &stubArchiveVerifier{report: verify.Report{Result: verify.ResultFailed}},
		}},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			code, stdout, _ := runCLI(t, testCase.options, testCase.args...)
			if code == 0 {
				t.Fatal("a failing command exited 0")
			}
			for _, phrase := range forbidden {
				if strings.Contains(stdout, phrase) {
					t.Errorf("failed command printed the success phrase %q", phrase)
				}
			}
		})
	}
}

// TestCredentialFailuresNeverEchoTheToken covers the credential failure path:
// no error message may carry the secret it was handling.
func TestCredentialFailuresNeverEchoTheToken(t *testing.T) {
	t.Parallel()

	const token = "pk_secret_value_that_must_not_leak"
	cases := []struct {
		name    string
		args    []string
		options Options
	}{
		{"auth rejected by the source", []string{"auth"}, Options{
			Authenticator:   &stubAuthenticator{err: errors.New("ClickUp rejected " + token)},
			CredentialStore: &stubCredentialStore{}, EnvironmentToken: token, EnvironmentTokenSet: true,
		}},
		{"scan rejected by the source", []string{"scan"}, Options{
			Authenticator:   &stubAuthenticator{err: errors.New("rejected " + token)},
			Scanner:         &stubScanner{},
			CredentialStore: &stubCredentialStore{}, EnvironmentToken: token, EnvironmentTokenSet: true,
		}},
		{"export failure mentioning the token", []string{"export", "--snapshot", "s"}, Options{
			Exporter:        &stubArchiveExporter{err: errors.New("download failed for " + token)},
			CredentialStore: &stubCredentialStore{}, EnvironmentToken: token, EnvironmentTokenSet: true,
		}},
		{"credential could not be saved", []string{"auth"}, Options{
			Authenticator:    &stubAuthenticator{},
			CredentialStore:  &stubCredentialStore{saveErr: errors.New("disk full")},
			EnvironmentToken: token, EnvironmentTokenSet: true,
		}},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			code, stdout, stderr := runCLI(t, testCase.options, testCase.args...)
			if code == 0 {
				t.Fatal("a credential failure exited 0")
			}
			if strings.Contains(stdout, token) || strings.Contains(stderr, token) {
				t.Fatal("the credential appeared in command output")
			}
		})
	}
}

func TestExportDoesNotSaveAnUnverifiedEnvironmentToken(t *testing.T) {
	t.Parallel()

	store := &stubCredentialStore{}
	options := Options{
		Exporter:        &stubArchiveExporter{result: archive.Summary{Path: "p", Status: archive.StatusCreatedUnverified}},
		CredentialStore: store, EnvironmentToken: "pk_never_verified", EnvironmentTokenSet: true,
	}
	if code, _, stderr := runCLI(t, options, "export", "--snapshot", "s"); code != 0 {
		t.Fatalf("export exit=%d stderr=%s", code, stderr)
	}
	// export never authenticates, so it has no evidence the token is valid and
	// must not persist it as if it were verified.
	if store.saved != "" {
		t.Fatalf("export saved an unverified credential: %q", store.saved)
	}
}
