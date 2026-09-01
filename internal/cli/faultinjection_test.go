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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"alih/internal/event"
	"alih/internal/state"
	"alih/internal/verify"
)

// The pre-flight test elsewhere proves an existing bundle is refused before any
// source work starts. These tests attack the other end: the moment after every
// stage has succeeded, when the only thing left is to publish. A failure there
// is the most dangerous one in the pipeline, because everything upstream of it
// genuinely worked.

// TestAFailureToPublishIsAFailureNotAQuietSuccess makes the final rename fail
// after verification and reporting have already passed.
func TestAFailureToPublishIsAFailureNotAQuietSuccess(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, verify.ResultVerified)
	final := h.finalRoot()

	// Occupying the destination with a regular file makes the rename fail at
	// the last step without changing anything the pipeline did before it.
	h.reporter.before = func() {
		if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
			t.Error(err)
			return
		}
		if err := os.WriteFile(final, []byte("in the way"), 0o600); err != nil {
			t.Error(err)
		}
	}

	code := h.app.Run([]string{"backup"})
	if code == 0 {
		t.Fatalf("a backup that could not be published exited 0\nstdout=%s", h.stdout.String())
	}
	if strings.Contains(h.stdout.String(), "Backup complete") {
		t.Errorf("a backup that was never published claimed completion:\n%s", h.stdout.String())
	}
	// The destination is untouched: Alih did not overwrite what was there.
	content, err := os.ReadFile(final)
	if err != nil || string(content) != "in the way" {
		t.Fatalf("the occupied destination was changed: content=%q err=%v", content, err)
	}
	// The work is preserved as explicitly failed evidence, not silently deleted.
	if _, err := os.Lstat(final + ".failed"); err != nil {
		t.Errorf("a failed publication kept no evidence: %v", err)
	}
}

// TestAFailureToPublishIsRecordedAsAFailedAttempt proves the operational record
// and the event history agree with the exit code.
func TestAFailureToPublishIsRecordedAsAFailedAttempt(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, verify.ResultVerified)
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	final := h.finalRoot()
	h.reporter.before = func() {
		if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
			t.Error(err)
			return
		}
		if err := os.WriteFile(final, []byte("in the way"), 0o600); err != nil {
			t.Error(err)
		}
	}

	if code := h.app.Run([]string{"backup"}); code == 0 {
		t.Fatal("a backup that could not be published exited 0")
	}

	record := loadOnlyRecord(t, stateRoot)
	if record.LastSuccess != nil {
		t.Errorf("a backup that was never published was recorded as a success: %#v", record.LastSuccess)
	}
	if record.LastAttempt == nil || record.LastAttempt.Outcome != state.OutcomeFailed {
		t.Fatalf("last attempt = %#v, want a recorded failure", record.LastAttempt)
	}
	if record.LastAttempt.Stage != state.StageFinalize {
		t.Errorf("failure stage = %q, want %q", record.LastAttempt.Stage, state.StageFinalize)
	}

	events, damaged, err := event.Read(stateRoot)
	if err != nil {
		t.Fatalf("read recorded history: %v", err)
	}
	if damaged != 0 {
		t.Errorf("recorded history has %d damaged lines", damaged)
	}
	for _, recorded := range events {
		if recorded.Type == event.TypeOperationCompleted {
			t.Fatalf("a backup that was never published emitted %s", event.TypeOperationCompleted)
		}
	}
	if len(events) == 0 {
		t.Fatal("a failed publication recorded no history at all")
	}
	if last := events[len(events)-1]; last.Type != event.TypeOperationFailed {
		t.Errorf("history ends with %s, want %s", last.Type, event.TypeOperationFailed)
	}
}

// TestAnUnwritableDestinationFailsBeforeTouchingTheSource proves Alih does not
// spend a user's rate limit discovering that it cannot store the result.
func TestAnUnwritableDestinationFailsBeforeTouchingTheSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permission semantics")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores directory permissions")
	}
	t.Parallel()

	h := newBackupHarness(t, verify.ResultVerified)
	if err := os.MkdirAll(h.root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(h.root, 0o700) })

	if code := h.app.Run([]string{"backup"}); code == 0 {
		t.Fatal("an unwritable destination exited 0")
	}
	if containsString(h.recorder.calls, "scan") || containsString(h.recorder.calls, "extract") {
		t.Fatalf("the source was read despite an unwritable destination: %v", h.recorder.calls)
	}
}

// TestStateThatCannotBeWrittenNeverInventsAnOutcome pairs with the existing
// proof that a backup keeps its result when state cannot be recorded. The
// question here is what happens afterwards: status must say nothing was
// recorded rather than present a healthy-looking guess.
//
// The fault is injected at the parent directory. Alih deliberately owns and
// re-tightens its own state directory, so making that directory unwritable
// would simply be corrected on the next write; making it uncreatable cannot be.
func TestStateThatCannotBeWrittenNeverInventsAnOutcome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permission semantics")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores directory permissions")
	}
	t.Parallel()

	h := newBackupHarness(t, verify.ResultVerified)
	parent := filepath.Join(t.TempDir(), "sealed")
	if err := os.MkdirAll(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	stateRoot := filepath.Join(parent, "state")
	h.app.options.StateRoot = stateRoot

	// The archive is real and verified, so the run itself still succeeds. An
	// inability to take a note about it does not unprove the archive.
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("code=%d; an unwritable note must not invalidate a proven archive\nstderr=%s", code, h.stderr.String())
	}
	if !strings.Contains(strings.ToLower(h.stderr.String()), "state") {
		t.Errorf("the operator was not told the run could not be recorded:\n%s", h.stderr.String())
	}
	if _, err := os.Lstat(stateRoot); !os.IsNotExist(err) {
		t.Fatalf("the state directory was created after all: %v", err)
	}

	app, stdout, _ := statusApp(t, stateRoot, Options{})
	if code := app.Run([]string{"status"}); code == statusExitHealthy {
		t.Fatalf("status called an unrecorded run healthy:\n%s", stdout.String())
	}
}

// TestAStateDirectoryAlihDoesNotOwnIsRefusedOnRead documents the other half of
// that ownership rule: a write tightens the directory Alih owns, but a read
// never trusts a record sitting in a directory anyone could have written to.
func TestAStateDirectoryAlihDoesNotOwnIsRefusedOnRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permission semantics")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores directory permissions")
	}
	t.Parallel()

	h := newBackupHarness(t, verify.ResultVerified)
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, h.stderr.String())
	}
	if err := os.Chmod(stateRoot, 0o777); err != nil {
		t.Fatal(err)
	}

	app, stdout, stderr := statusApp(t, stateRoot, Options{})
	code := app.Run([]string{"status"})
	if code == statusExitHealthy {
		t.Fatalf("a world-writable state directory was reported as healthy:\n%s", stdout.String())
	}
	if code != statusExitUnreadable {
		t.Fatalf("code = %d, want the unreadable-state exit code %d", code, statusExitUnreadable)
	}
	for _, expected := range []string{"UNREADABLE", "Unreadable state file", "did not modify or replace it"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("status does not report %q:\n%s", expected, stdout.String())
		}
	}
	if stderr.Len() != 0 && !strings.Contains(stderr.String(), "state") {
		t.Errorf("unexpected diagnostics: %s", stderr.String())
	}
}
