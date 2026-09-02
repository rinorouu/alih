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
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"alih/internal/usage"
)

func setupCLI(t *testing.T, stdin string, interactive bool) (*App, *strings.Builder, *strings.Builder, string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "usage.json")
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	app := New(stdout, stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator:       &stubAuthenticator{},
		UsageStatePath:      path,
		Stdin:               strings.NewReader(stdin),
		Interactive:         &interactive,
		AvailableConnectors: []string{"clickup", "notion"},
	})
	return app, stdout, stderr, path
}

func recordedMode(t *testing.T, path string) usage.Mode {
	t.Helper()
	mode, err := usage.NewStore(path).Load()
	if err != nil {
		t.Fatalf("load recorded mode: %v", err)
	}
	return mode
}

// TestSetupOnAFreshInstallRecordsNothingUntilAsked proves setup is an
// onboarding surface, not a gate: an installation that never ran it is
// self-managed, and --show says so without writing anything.
func TestSetupOnAFreshInstallRecordsNothingUntilAsked(t *testing.T) {
	t.Parallel()
	app, stdout, _, path := setupCLI(t, "", false)

	if code := app.Run([]string{"setup", "--show"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout.String(), "Self-managed") {
		t.Errorf("a fresh install is not reported as self-managed: %s", stdout)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("--show wrote usage state; reading the mode must not create it")
	}
}

func TestSetupRecordsEachModeWithoutPrompting(t *testing.T) {
	t.Parallel()
	for _, mode := range []usage.Mode{usage.SelfManaged, usage.Assistance} {
		mode := mode
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			app, _, stderr, path := setupCLI(t, "", false)
			if code := app.Run([]string{"setup", "--mode", string(mode)}); code != 0 {
				t.Fatalf("code = %d stderr=%s", code, stderr)
			}
			if got := recordedMode(t, path); got != mode {
				t.Errorf("recorded %q, want %q", got, mode)
			}
		})
	}
}

// TestAssistanceNeverClaimsASubscription is the product boundary in prose form:
// choosing Assistance records intent and must not imply anything was activated.
func TestAssistanceNeverClaimsASubscription(t *testing.T) {
	t.Parallel()
	app, stdout, _, _ := setupCLI(t, "", false)
	if code := app.Run([]string{"setup", "--mode", "assistance"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	output := stdout.String()
	for _, forbidden := range []string{"Subscription: Active", "subscription is active", "Trial", "Expired", "Upgrade", "Pro", "Premium"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("assistance output claims %q: %s", forbidden, output)
		}
	}
	for _, required := range []string{"not connected", "not available yet", "free and open source", "does not"} {
		if !strings.Contains(output, required) {
			t.Errorf("assistance output does not say %q: %s", required, output)
		}
	}
}

func TestSetupMovesBetweenModesInBothDirections(t *testing.T) {
	t.Parallel()
	app, _, _, path := setupCLI(t, "", false)

	for _, step := range []usage.Mode{usage.SelfManaged, usage.Assistance, usage.SelfManaged} {
		if code := app.Run([]string{"setup", "--mode", string(step)}); code != 0 {
			t.Fatalf("moving to %q returned %d", step, code)
		}
		if got := recordedMode(t, path); got != step {
			t.Fatalf("recorded %q, want %q", got, step)
		}
	}
}

// TestReturningToSelfManagedDestroysNothing covers the invariant that leaving
// Assistance is a change of responsibility, never a teardown.
func TestReturningToSelfManagedDestroysNothing(t *testing.T) {
	t.Parallel()
	app, stdout, _, path := setupCLI(t, "", false)
	directory := filepath.Dir(path)

	credentials := filepath.Join(directory, "credentials.json")
	if err := os.WriteFile(credentials, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(credentials)
	if err != nil {
		t.Fatal(err)
	}

	if code := app.Run([]string{"setup", "--mode", "assistance"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	stdout.Reset()
	if code := app.Run([]string{"setup", "--mode", "self-managed"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	after, err := os.ReadFile(credentials)
	if err != nil {
		t.Fatalf("returning to self-managed removed a neighbouring file: %v", err)
	}
	if string(before) != string(after) {
		t.Error("returning to self-managed modified a neighbouring file")
	}
	if !strings.Contains(stdout.String(), "Nothing was removed") {
		t.Errorf("the user is not told their data survived: %s", stdout)
	}
}

func TestInteractiveSetupHandlesEveryAnswer(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name, input string
		wantCode    int
		wantMode    usage.Mode
		wantWritten bool
	}{
		{"chooses self-managed", "1\n", 0, usage.SelfManaged, true},
		{"chooses assistance", "2\n", 0, usage.Assistance, true},
		{"empty answer keeps things as they are", "\n", 0, "", false},
		{"EOF cancels without writing", "", 0, "", false},
		{"invalid then valid", "9\nx\n1\n", 0, usage.SelfManaged, true},
		{"three invalid answers give up", "9\n8\n7\n", 2, "", false},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			app, _, _, path := setupCLI(t, testCase.input, true)
			if code := app.Run([]string{"setup"}); code != testCase.wantCode {
				t.Fatalf("code = %d, want %d", code, testCase.wantCode)
			}
			_, err := os.Stat(path)
			written := err == nil
			if written != testCase.wantWritten {
				t.Fatalf("state written = %v, want %v", written, testCase.wantWritten)
			}
			if testCase.wantWritten {
				if got := recordedMode(t, path); got != testCase.wantMode {
					t.Errorf("recorded %q, want %q", got, testCase.wantMode)
				}
			}
		})
	}
}

// TestSetupWillNotPromptWithoutATerminal keeps Alih usable unattended.
func TestSetupWillNotPromptWithoutATerminal(t *testing.T) {
	t.Parallel()
	app, _, stderr, path := setupCLI(t, "1\n", false)

	if code := app.Run([]string{"setup"}); code != 2 {
		t.Fatalf("code = %d, want a usage error", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a non-interactive run recorded a mode nobody chose")
	}
	if !strings.Contains(stderr.String(), "--mode") {
		t.Errorf("the error does not point at the unattended flag: %s", stderr)
	}
}

func TestSetupRefusesUnreadableStateRatherThanRewritingIt(t *testing.T) {
	t.Parallel()
	app, _, stderr, path := setupCLI(t, "", false)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := app.Run([]string{"setup", "--show"}); code != 4 {
		t.Fatalf("code = %d, want 4", code)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "not json" {
		t.Errorf("unreadable state was rewritten: %q, %v", content, err)
	}
	if !strings.Contains(stderr.String(), "does not rewrite") {
		t.Errorf("stderr does not explain the refusal: %s", stderr)
	}
}

func TestSetupRejectsAnUnknownMode(t *testing.T) {
	t.Parallel()
	app, _, stderr, path := setupCLI(t, "", false)
	if code := app.Run([]string{"setup", "--mode", "premium"}); code != 2 {
		t.Fatalf("code = %d, want a usage error", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("an unknown mode was recorded")
	}
	if !strings.Contains(stderr.String(), "unknown usage mode") {
		t.Errorf("stderr = %s", stderr)
	}
}

func TestSetupNeverPrintsACredential(t *testing.T) {
	t.Parallel()
	const secret = "pk_setup_must_never_print_this"
	directory := t.TempDir()
	_ = os.Chmod(directory, 0o700)
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	interactive := false
	app := New(stdout, stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator:       &stubAuthenticator{},
		CredentialStore:     &stubCredentialStore{loaded: secret},
		UsageStatePath:      filepath.Join(directory, "usage.json"),
		Interactive:         &interactive,
		AvailableConnectors: []string{"clickup", "notion"},
	})
	for _, mode := range []string{"self-managed", "assistance"} {
		if code := app.Run([]string{"setup", "--mode", mode}); code != 0 {
			t.Fatalf("code = %d", code)
		}
	}
	if strings.Contains(stdout.String()+stderr.String(), secret) {
		t.Error("setup printed a credential")
	}
}

func TestSetupAppearsInHelp(t *testing.T) {
	t.Parallel()
	app, stdout, _, _ := setupCLI(t, "", false)
	if code := app.Run([]string{"--help"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout.String(), "setup") {
		t.Errorf("help does not mention setup: %s", stdout)
	}
}

// TestTheNullDeviceIsNotATerminal covers the unattended shape an installer or
// a cron job actually uses. The null device is a character device, so naive
// terminal detection accepts it, prompts into the void, and then exits 0
// having recorded nothing -- reporting success for work that did not happen.
func TestTheNullDeviceIsNotATerminal(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "usage.json")
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()

	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	app := New(stdout, stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Authenticator:       &stubAuthenticator{},
		UsageStatePath:      path,
		Stdin:               null,
		AvailableConnectors: []string{"clickup", "notion"},
	})

	if code := app.Run([]string{"setup"}); code != 2 {
		t.Fatalf("code = %d, want a usage error; %s", code, stderr)
	}
	if !strings.Contains(stderr.String(), "--mode") {
		t.Errorf("the error does not point at the unattended flag: %s", stderr)
	}
	if strings.Contains(stdout.String(), "Select [1-2]") {
		t.Errorf("setup prompted a reader that can never answer: %s", stdout)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a non-interactive run recorded a mode nobody chose")
	}
}
