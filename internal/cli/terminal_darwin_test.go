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

//go:build darwin

package cli

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

// openTestTerminal returns the slave side of a Darwin pseudo-terminal. The
// slave is the tty endpoint used as a command's stdin; unlike Linux, Darwin
// does not accept TIOCGETA on the /dev/ptmx master used to create it.
func openTestTerminal(t *testing.T) *os.File {
	t.Helper()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open Darwin pseudo-terminal master: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })

	var slaveName [128]byte
	if err := testTerminalIOCTL(master, syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&slaveName[0]))); err != nil {
		t.Fatalf("get Darwin pseudo-terminal slave name: %v", err)
	}
	nameEnd := bytes.IndexByte(slaveName[:], 0)
	if nameEnd < 0 {
		t.Fatal("Darwin pseudo-terminal slave name is not NUL-terminated")
	}

	if err := testTerminalIOCTL(master, syscall.TIOCPTYGRANT, 0); err != nil {
		t.Fatalf("grant Darwin pseudo-terminal slave: %v", err)
	}
	if err := testTerminalIOCTL(master, syscall.TIOCPTYUNLK, 0); err != nil {
		t.Fatalf("unlock Darwin pseudo-terminal slave: %v", err)
	}

	slave, err := os.OpenFile(string(slaveName[:nameEnd]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open Darwin pseudo-terminal slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return slave
}

func testTerminalIOCTL(file *os.File, request uintptr, argument uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), request, argument)
	if errno != 0 {
		return errno
	}
	return nil
}
