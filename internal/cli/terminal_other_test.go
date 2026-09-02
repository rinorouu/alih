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

//go:build linux || darwin

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestATerminalIsRecognisedAsOne is the assertion v0.2.6 did not have.
//
// Every setup test but one overrode terminal detection, and the one that did
// not asserted a negative -- that the null device is not a terminal. A bug
// that classifies *everything* as non-interactive satisfies that assertion on
// every platform, which is exactly how the released Windows defect passed CI.
// The positive case has to be asserted for its own sake.
func TestATerminalIsRecognisedAsOne(t *testing.T) {
	t.Parallel()

	// The master side of a pseudo-terminal is a character device that is not
	// the null device: a real terminal, obtained without a helper library.
	terminal, err := os.Open("/dev/ptmx")
	if err != nil {
		t.Skipf("no pseudo-terminal available on this machine: %v", err)
	}
	defer terminal.Close()

	if !isTerminal(terminal) {
		t.Error("a pseudo-terminal was not recognised as a terminal; setup would refuse to prompt a real user")
	}
}

func TestRedirectedInputIsNotATerminal(t *testing.T) {
	t.Parallel()

	regular := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(regular, []byte("1\n"), 0o600); err != nil {
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

	zero, err := os.Open("/dev/zero")
	if err != nil {
		t.Skipf("no /dev/zero available on this machine: %v", err)
	}
	defer zero.Close()

	for _, testCase := range []struct {
		name string
		file *os.File
	}{
		{"redirected file", file},
		{"pipe", readEnd},
		{"null device", null},
		{"non-terminal character device", zero},
	} {
		if isTerminal(testCase.file) {
			t.Errorf("%s was treated as a terminal; setup must not prompt a stream nobody can answer", testCase.name)
		}
	}
}
