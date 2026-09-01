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

package state

import (
	"errors"
	"syscall"
	"time"
)

// Windows does not let a file be replaced while another handle is open on it
// without FILE_SHARE_DELETE, which Go does not request. A reader and a writer
// that meet therefore fail transiently: the writer's rename is denied, and the
// reader's open reports a sharing violation. Neither means the state is bad.
//
// POSIX has no equivalent contention, so this retry exists only here. It is
// bounded: state must never block a backup, so after the budget is spent the
// error is returned unchanged and the caller reports it as it always would.
const (
	sharingAttempts = 20
	sharingBackoff  = 5 * time.Millisecond
)

// retryWhileShared runs an operation, retrying only the transient sharing
// failures above. Any other error is returned immediately, so a genuine
// permission problem is never mistaken for contention and waited on.
func retryWhileShared(operation func() error) error {
	var err error
	for attempt := 0; attempt < sharingAttempts; attempt++ {
		if err = operation(); err == nil || !isSharingViolation(err) {
			return err
		}
		time.Sleep(sharingBackoff)
	}
	return err
}

func isSharingViolation(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	// ERROR_ACCESS_DENIED is what a denied replace reports; ERROR_SHARING_VIOLATION
	// and ERROR_LOCK_VIOLATION are what a colliding open reports.
	return errno == syscall.ERROR_ACCESS_DENIED || errno == syscall.Errno(32) || errno == syscall.Errno(33)
}
