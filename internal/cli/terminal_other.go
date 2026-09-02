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
	"syscall"
	"unsafe"
)

// isTerminal reports whether file is a terminal.
//
// A character-device check is insufficient: /dev/null, /dev/zero and similar
// redirected devices are character devices too. Asking the kernel for the
// terminal attributes succeeds for a terminal or PTY and fails for regular
// files, pipes and non-terminal character devices. terminalReadIOCTL is selected
// per supported Unix platform because Linux and Darwin use different requests.
func isTerminal(file *os.File) bool {
	var state syscall.Termios
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		file.Fd(),
		uintptr(terminalReadIOCTL),
		uintptr(unsafe.Pointer(&state)),
	)
	return errno == 0
}
