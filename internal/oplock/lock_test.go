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

package oplock

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"alih/internal/state"
)

var lockTestTime = time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)

func lockTestScope(root string) state.Scope {
	return state.Scope{Connector: "clickup", WorkspaceID: "100", Destination: filepath.Join(root, "Alih")}
}

func TestLockScopeIsStablePrivateAndIndependent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scope := lockTestScope(root)
	first, err := Acquire(root, scope, "20260901T090000Z-00000001", "dev", lockTestTime)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer first.Release()
	if filepath.Base(first.Path()) != scope.Key()+".lock" {
		t.Fatalf("lock path = %q", first.Path())
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(first.Path())
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("lock permissions = %v (%v)", info, err)
		}
	}
	if _, err := Acquire(root, scope, "20260901T090000Z-00000002", "dev", lockTestTime); !errors.Is(err, ErrHeld) {
		t.Fatalf("overlap error = %v, want ErrHeld", err)
	}
	other := scope
	other.WorkspaceID = "200"
	second, err := Acquire(root, other, "20260901T090000Z-00000003", "dev", lockTestTime)
	if err != nil {
		t.Fatalf("independent scope was blocked: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("release independent scope: %v", err)
	}
}

func TestReleasedMetadataFileNeverActsAsAStaleLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scope := lockTestScope(root)
	first, err := Acquire(root, scope, "20260901T090000Z-00000001", "dev", lockTestTime)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	path := first.Path()
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("inspection metadata disappeared: %v", err)
	}
	second, err := Acquire(root, scope, "20260901T090100Z-00000002", "dev", lockTestTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("released metadata blocked a later run: %v", err)
	}
	_ = second.Release()
}

func TestTwoProcessesCannotEnterOneScopeAndKillReleasesIt(t *testing.T) {
	if os.Getenv("ALIH_OPERATION_LOCK_HELPER") == "1" {
		runLockHelper()
		return
	}
	t.Parallel()
	root := t.TempDir()
	destination := filepath.Join(root, "Alih")
	command := exec.Command(os.Args[0], "-test.run=TestTwoProcessesCannotEnterOneScopeAndKillReleasesIt")
	command.Env = append(os.Environ(),
		"ALIH_OPERATION_LOCK_HELPER=1", "ALIH_OPERATION_LOCK_ROOT="+root,
		"ALIH_OPERATION_LOCK_DESTINATION="+destination)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("helper stdin: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("helper stdout: %v", err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "READY" {
		_ = command.Process.Kill()
		t.Fatalf("helper readiness = %q (%v)", line, err)
	}
	scope := state.Scope{Connector: "clickup", WorkspaceID: "100", Destination: destination}
	_, err = Acquire(root, scope, "20260901T090000Z-parent", "dev", lockTestTime)
	if !errors.Is(err, ErrHeld) {
		_ = command.Process.Kill()
		t.Fatalf("second process overlap error = %v", err)
	}
	// Simulate an uncatchable process termination. The OS handle, not PID age or
	// metadata deletion, decides when the scope is available again.
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = stdin.Close()
	_ = command.Wait()
	lock, err := Acquire(root, scope, "20260901T090100Z-parent", "dev", lockTestTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("killed process left a stale lock: %v", err)
	}
	_ = lock.Release()
}

func runLockHelper() {
	root := os.Getenv("ALIH_OPERATION_LOCK_ROOT")
	destination := os.Getenv("ALIH_OPERATION_LOCK_DESTINATION")
	lock, err := Acquire(root, state.Scope{
		Connector: "clickup", WorkspaceID: "100", Destination: destination,
	}, "20260901T090000Z-helper", "dev", lockTestTime)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "READY")
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = lock.Release()
	os.Exit(0)
}
