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

//go:build !windows

package state

import "os"

// retryWhileShared runs an operation once. POSIX replaces a file by rename
// regardless of who holds it open, so a reader and a writer never contend and
// there is nothing transient to wait for.
func retryWhileShared(operation func() error) error { return operation() }

// openForRead opens a state file for reading. POSIX places no restriction on
// replacing a file that is open, so the standard call is enough.
func openForRead(path string) (*os.File, error) { return os.Open(path) }

// retryWhileReplaced runs an open once. A POSIX rename replaces the name
// atomically, so there is no instant in which the file appears to be absent.
func retryWhileReplaced(operation func() error) error { return operation() }
