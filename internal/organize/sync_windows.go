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

package organize

// syncDirectory is a no-op on Windows. Windows does not permit opening a
// directory as a file handle for flushing: the attempt fails with "Access is
// denied", which made publishing an organized view impossible rather than
// durable. Directory metadata is not separately flushable here, and the
// ordering the POSIX implementation buys with this call is provided by the
// filesystem itself, so the honest thing is to do nothing rather than to
// report an error the caller cannot act on.
//
// The file contents themselves are still flushed before publication; only the
// directory-entry sync is skipped.
func syncDirectory(string) error { return nil }
