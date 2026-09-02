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

//go:build windows

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWindowsDetectorUsesConsoleCapability(t *testing.T) {
	t.Parallel()

	const handle syscall.Handle = 42
	called := false
	if !consoleModeAvailable(handle, func(got syscall.Handle, mode *uint32) error {
		called = true
		if got != handle {
			t.Errorf("queried handle = %d, want %d", got, handle)
		}
		*mode = 1
		return nil
	}) {
		t.Error("a successful console-mode query was classified as non-interactive")
	}
	if !called {
		t.Error("the Windows console API was not queried")
	}
	if consoleModeAvailable(handle, func(syscall.Handle, *uint32) error {
		return errors.New("not a console handle")
	}) {
		t.Error("a failed console-mode query was classified as interactive")
	}
}

// TestTheWindowsConsoleIsRecognisedAsATerminal is the regression for the
// defect a user reported after installing v0.2.6: "alih setup" in PowerShell
// answered "no terminal is attached" and refused to prompt.
//
// CONIN$ is the console input handle, so this is the same question the real
// session asks. GitHub Actions normally redirects the test process handles;
// when no console is attached, the test allocates one before making the query.
// That makes the positive Windows assertion run instead of silently skipping
// on the exact CI job meant to protect it.
func TestTheWindowsConsoleIsRecognisedAsATerminal(t *testing.T) {
	console := openWindowsConsole(t)

	if !isTerminal(console) {
		t.Error("the Windows console was not recognised as a terminal; this is the v0.2.6 PowerShell defect")
	}
}

func openWindowsConsole(t *testing.T) *os.File {
	t.Helper()

	console, err := os.Open("CONIN$")
	if err == nil {
		t.Cleanup(func() { _ = console.Close() })
		return console
	}

	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	allocated, _, callErr := kernel32.NewProc("AllocConsole").Call()
	if allocated == 0 {
		t.Fatalf("attach a Windows console for the terminal regression test: %v (opening CONIN$ first failed: %v)", callErr, err)
	}
	t.Cleanup(func() {
		kernel32.NewProc("FreeConsole").Call()
	})

	console, err = os.Open("CONIN$")
	if err != nil {
		t.Fatalf("open CONIN$ after AllocConsole succeeded: %v", err)
	}
	t.Cleanup(func() { _ = console.Close() })
	return console
}

func TestWindowsRedirectedInputIsNotATerminal(t *testing.T) {
	t.Parallel()

	regular := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(regular, []byte("1\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(regular)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	defer writeEnd.Close()

	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()

	for _, testCase := range []struct {
		name string
		file *os.File
	}{
		{"redirected file", file},
		{"pipe", readEnd},
		{"NUL", null},
	} {
		if isTerminal(testCase.file) {
			t.Errorf("%s was treated as a terminal; unattended Windows runs must not prompt", testCase.name)
		}
	}
}

// TestFileIdentityCannotDistinguishTheNullDeviceOnWindows pins the standard
// library behaviour that caused the v0.2.6 defect, so the approach cannot be
// reintroduced by someone who assumes Windows behaves like Unix.
//
// A console, a pipe and NUL all produce a FileInfo whose volume and file index
// are zero, and os.SameFile compares exactly those. Every character device on
// Windows therefore looks like the null device. If this ever fails, the
// standard library has changed -- which still would not make file identity the
// right way to detect a console.
func TestFileIdentityCannotDistinguishTheNullDeviceOnWindows(t *testing.T) {
	t.Parallel()

	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	nullInfo, err := null.Stat()
	if err != nil {
		t.Fatal(err)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	defer writeEnd.Close()
	pipeInfo, err := readEnd.Stat()
	if err != nil {
		t.Fatal(err)
	}

	if !os.SameFile(pipeInfo, nullInfo) {
		t.Skip("os.SameFile now distinguishes a pipe from NUL; it is still not how Alih detects a console")
	}
}
