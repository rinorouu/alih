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
	"os"
	"syscall"
)

// isTerminal reports whether file is a Windows console, by asking the console
// itself rather than by inspecting file metadata.
//
// GetConsoleMode succeeds only for a real console handle. A redirected file, a
// pipe, and NUL all fail it, which is exactly the distinction setup needs.
//
// Metadata cannot answer this question on Windows, and v0.2.6 shipped a bug
// proving it. A console and NUL are both FILE_TYPE_CHAR, and os.statHandle
// returns a fileStat carrying only a name and a file type for that type -- its
// path is empty and its volume and index fields stay zero. os.SameFile compares
// exactly those three zero fields, and skips loading them when the path is
// empty, so os.SameFile(console, NUL) is true for every character device on
// Windows. Alih used that comparison to exclude the null device, and therefore
// classified every native PowerShell session as having no terminal.
func isTerminal(file *os.File) bool {
	return consoleModeAvailable(syscall.Handle(file.Fd()), syscall.GetConsoleMode)
}

func consoleModeAvailable(handle syscall.Handle, getMode func(syscall.Handle, *uint32) error) bool {
	var mode uint32
	return getMode(handle, &mode) == nil
}
