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
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"alih/internal/connector"
)

// The cancellation tests elsewhere in this package cancel a context directly.
// That proves the pipeline honours cancellation, but not that a real signal
// ever reaches that context. These tests send a real signal to a real process.

const (
	signalHelperEnv        = "ALIH_SIGNAL_HELPER_ROOT"
	signalHelperReadyEnv   = "ALIH_SIGNAL_HELPER_READY"
	signalHelperSucceedEnv = "ALIH_SIGNAL_HELPER_SUCCEED"
)

// blockingScanner stops the pipeline mid-run and tells the parent process it
// has arrived, so the signal lands while real work is in flight rather than
// before the run starts or after it finished.
type blockingScanner struct {
	recorder  *backupRecorder
	result    connector.ScanResult
	readyPath string
}

func (s *blockingScanner) Name() string { return "clickup" }

func (s *blockingScanner) Scan(ctx context.Context, _ string, _ connector.Workspace) (connector.ScanResult, error) {
	s.recorder.call("scan")
	if err := os.WriteFile(s.readyPath, []byte("ready"), 0o600); err != nil {
		return connector.ScanResult{}, err
	}
	select {
	case <-ctx.Done():
		return connector.ScanResult{}, ctx.Err()
	case <-time.After(30 * time.Second):
		return s.result, errors.New("the signal never arrived")
	}
}

// TestBackupHelperProcess is not a test. It is the child process the signal
// tests drive, and it exits immediately unless the parent asked for it.
func TestBackupHelperProcess(t *testing.T) {
	root := os.Getenv(signalHelperEnv)
	if root == "" {
		t.Skip("not the signal helper child process")
	}
	h := newBackupHarness(t, "VERIFIED")
	h.app.options.BackupRoot = root
	h.app.options.StateRoot = filepath.Join(root, "state")
	if os.Getenv(signalHelperSucceedEnv) == "" {
		h.app.options.Scanner = &blockingScanner{
			recorder: h.recorder, result: h.scanner.result,
			readyPath: os.Getenv(signalHelperReadyEnv),
		}
	}
	code := h.app.Run([]string{"backup"})
	fmt.Fprint(os.Stdout, h.stdout.String())
	fmt.Fprint(os.Stderr, h.stderr.String())
	os.Exit(code)
}

func startSignalHelper(t *testing.T, root string, extraEnv ...string) (*exec.Cmd, string) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestBackupHelperProcess$", "-test.v=false")
	command.Env = append(os.Environ(),
		signalHelperEnv+"="+root,
		signalHelperReadyEnv+"="+ready,
		// TestMain sandboxes the configuration directory; the child process
		// runs its own TestMain and does the same for itself.

	)
	command.Env = append(command.Env, extraEnv...)
	if err := command.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	return command, ready
}

func waitForReady(t *testing.T, command *exec.Cmd, ready string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = command.Process.Kill()
	t.Fatal("the helper process never reached the source stage")
}

// publishedBundles counts the directories a completed backup would have left,
// ignoring the working and failed evidence a failure is allowed to keep.
func publishedBundles(t *testing.T, root string) []string {
	t.Helper()
	var published []string
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	for _, workspace := range entries {
		if !workspace.IsDir() || !isWorkspaceDirectory(workspace.Name()) {
			continue
		}
		runs, err := os.ReadDir(filepath.Join(root, workspace.Name()))
		if err != nil {
			continue
		}
		for _, run := range runs {
			name := run.Name()
			if strings.HasSuffix(name, ".failed") || strings.Contains(name, ".partial-") || strings.HasPrefix(name, ".") {
				continue
			}
			published = append(published, filepath.Join(workspace.Name(), name))
		}
	}
	return published
}

// isWorkspaceDirectory separates the per-Workspace backup directories from the
// bookkeeping the destination also holds: the operation-lock directory and, in
// these tests, the redirected state root.
func isWorkspaceDirectory(name string) bool {
	return !strings.HasPrefix(name, ".") && name != "state" && name != "config"
}

// TestACatchableSignalStopsTheRunWithoutPublishingIt sends a real SIGTERM to a
// real process in the middle of a backup and proves the interruption is
// reported as a failure rather than silently completing or silently vanishing.
func TestACatchableSignalStopsTheRunWithoutPublishingIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal semantics; Windows termination is covered separately")
	}
	for _, signalCase := range []struct {
		name   string
		signal os.Signal
	}{
		{"SIGTERM", os.Signal(syscall.SIGTERM)},
		{"SIGINT", os.Interrupt},
	} {
		t.Run(signalCase.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "Alih")
			command, ready := startSignalHelper(t, root)
			waitForReady(t, command, ready)

			if err := command.Process.Signal(signalCase.signal); err != nil {
				t.Fatalf("signal the helper process: %v", err)
			}
			err := command.Wait()
			if err == nil {
				t.Fatal("an interrupted backup exited successfully")
			}
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) {
				t.Fatalf("helper process ended unexpectedly: %v", err)
			}
			if exitError.ExitCode() == 0 {
				t.Fatal("an interrupted backup reported success")
			}
			if published := publishedBundles(t, root); len(published) != 0 {
				t.Fatalf("an interrupted backup published %v", published)
			}
		})
	}
}

// TestAnUninterruptedHelperStillCompletes proves the harness above fails for
// the right reason. Without it, a helper that could never succeed would make
// the signal tests pass no matter what the signal did.
func TestAnUninterruptedHelperStillCompletes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("paired with the POSIX signal test")
	}
	root := filepath.Join(t.TempDir(), "Alih")
	command, _ := startSignalHelper(t, root, signalHelperSucceedEnv+"=1")

	if err := command.Wait(); err != nil {
		t.Fatalf("an uninterrupted backup failed: %v", err)
	}
	if published := publishedBundles(t, root); len(published) != 1 {
		t.Fatalf("an uninterrupted backup published %v, want exactly one bundle", published)
	}
}

// TestAnUncatchableTerminationLeavesNoPublishedBackup documents the recovery
// behaviour Alih cannot handle gracefully. SIGKILL runs no deferred cleanup, so
// the guarantee is narrower and stated as such: the working directory may
// survive, but nothing that survives may look like a completed backup.
func TestAnUncatchableTerminationLeavesNoPublishedBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal semantics")
	}
	root := filepath.Join(t.TempDir(), "Alih")
	command, ready := startSignalHelper(t, root)
	waitForReady(t, command, ready)

	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill the helper process: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("a killed backup exited successfully")
	}
	if published := publishedBundles(t, root); len(published) != 0 {
		t.Fatalf("a killed backup published %v", published)
	}
	// Whatever the kill left behind must be recognisable as incomplete work.
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, workspace := range entries {
		if !isWorkspaceDirectory(workspace.Name()) {
			continue
		}
		runs, err := os.ReadDir(filepath.Join(root, workspace.Name()))
		if err != nil {
			continue
		}
		for _, run := range runs {
			if !strings.Contains(run.Name(), ".partial-") && !strings.HasSuffix(run.Name(), ".failed") {
				t.Errorf("a killed backup left %q, which does not read as incomplete work", run.Name())
			}
		}
	}
}
