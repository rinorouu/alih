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
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"alih/internal/schedule"
)

type cliScheduleRunner struct{ commands []schedule.Command }

func (runner *cliScheduleRunner) Run(_ context.Context, command schedule.Command) error {
	runner.commands = append(runner.commands, command)
	return nil
}

func writeScheduleConfig(t *testing.T, root, destination string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	encodedDestination, _ := json.Marshal(destination)
	content := `{
  "schema_version": 1,
  "schedules": [{
    "id": "daily-main",
    "enabled": true,
    "operation": "backup",
    "connector": "clickup",
    "workspace_id": "100",
    "destination": ` + string(encodedDestination) + `,
    "cadence": {"frequency":"daily","at":"02:30","timezone":"local","missed_run_policy":"run_once"}
  }]
}`
	if err := os.WriteFile(filepath.Join(root, "schedules.json"), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// scheduleTestToken is a credential the schedule harness holds so that
// "the generated plan carries no credential" is a claim about a real secret
// rather than about the word "token" appearing in the output.
const scheduleTestToken = "pk_schedule_must_never_leak"

func scheduleCLI(t *testing.T) (*App, *bytes.Buffer, *bytes.Buffer, *cliScheduleRunner) {
	t.Helper()
	configRoot := filepath.Join(t.TempDir(), "config")
	home := filepath.Join(t.TempDir(), "home")
	destination := filepath.Join(t.TempDir(), "Alih Backups")
	writeScheduleConfig(t, configRoot, destination)
	runner := &cliScheduleRunner{}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	app := New(stdout, stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ScheduleRoot: configRoot, ScheduleRunner: runner, SchedulePlatform: runtime.GOOS,
		ExecutablePath: filepath.Join(home, "bin", "alih"), UserHome: home, UserID: "1000",
		// A schedule may only be generated for a connector this build can run,
		// so the harness wires the one the fixture configuration names.
		Authenticator:   &backupAuthenticator{recorder: &backupRecorder{}},
		CredentialStore: &stubCredentialStore{loaded: scheduleTestToken},
	})
	return app, stdout, stderr, runner
}

func TestScheduleCheckAndPreviewAreReadOnly(t *testing.T) {
	t.Parallel()
	app, stdout, stderr, runner := scheduleCLI(t)
	if code := app.Run([]string{"schedule", "check", "--json"}); code != 0 {
		t.Fatalf("check code=%d stderr=%s", code, stderr.String())
	}
	if len(runner.commands) != 0 || !strings.Contains(stdout.String(), `"configured": true`) {
		t.Fatalf("check invoked commands=%#v output=%s", runner.commands, stdout.String())
	}
	stdout.Reset()
	if code := app.Run([]string{"schedule", "preview", "daily-main", "--json"}); code != 0 {
		t.Fatalf("preview code=%d stderr=%s", code, stderr.String())
	}
	// The credential itself must never appear. Searching for the word "token"
	// is not a usable proxy for that: a Windows Task Scheduler definition
	// legitimately contains <LogonType>InteractiveToken</LogonType>, which made
	// this check fail on Windows for a plan that leaked nothing.
	if len(runner.commands) != 0 || !strings.Contains(stdout.String(), `"schedule_id": "daily-main"`) ||
		!strings.Contains(stdout.String(), `--workspace-id`) || strings.Contains(stdout.String(), scheduleTestToken) {
		t.Fatalf("preview invoked commands=%#v or was unsafe: %s", runner.commands, stdout.String())
	}
}

func TestScheduleInstallInspectAndRemoveRequireExplicitActions(t *testing.T) {
	t.Parallel()
	app, stdout, stderr, runner := scheduleCLI(t)
	if code := app.Run([]string{"schedule", "install", "daily-main"}); code != 0 {
		t.Fatalf("install code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if len(runner.commands) == 0 || !strings.Contains(stdout.String(), "installed for the current user") {
		t.Fatalf("install commands=%#v output=%s", runner.commands, stdout.String())
	}
	stdout.Reset()
	runner.commands = nil
	if code := app.Run([]string{"schedule", "inspect", "daily-main", "--json"}); code != 0 {
		t.Fatalf("inspect code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if len(runner.commands) == 0 || !strings.Contains(stdout.String(), `"installed": true`) ||
		!strings.Contains(stdout.String(), `"registered": true`) {
		t.Fatalf("inspect commands=%#v output=%s", runner.commands, stdout.String())
	}
	stdout.Reset()
	runner.commands = nil
	if code := app.Run([]string{"schedule", "remove", "daily-main"}); code != 0 {
		t.Fatalf("remove code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if len(runner.commands) == 0 || !strings.Contains(stdout.String(), "removed for the current user") {
		t.Fatalf("remove commands=%#v output=%s", runner.commands, stdout.String())
	}
}

func TestScheduleWithoutConfigDoesNotInvokeNativeScheduler(t *testing.T) {
	t.Parallel()
	runner := &cliScheduleRunner{}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	app := New(stdout, stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		ScheduleRoot: filepath.Join(t.TempDir(), "missing"), ScheduleRunner: runner,
	})
	if code := app.Run([]string{"schedule"}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if len(runner.commands) != 0 || !strings.Contains(stdout.String(), "no recurring execution") {
		t.Fatalf("commands=%#v output=%s", runner.commands, stdout.String())
	}
}
