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

// Package oplock prevents two processes from entering the same Alih operation
// scope. The lock is owned by an operating-system file handle rather than by
// the continued existence of a lock file, so process exit and uncatchable
// termination release it without guessing whether a PID is stale.
package oplock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"alih/internal/state"
)

const (
	SchemaVersion = 1
	lockDirectory = "locks"
	maxOwnerBytes = 4096
)

// ErrHeld means another process owns the scope. Owner metadata is best-effort
// context only; the operating-system handle is the authority.
var ErrHeld = errors.New("operation scope is already locked")

// Owner is safe lock-inspection metadata. It contains no command line,
// environment, credential, or provider-controlled display name.
type Owner struct {
	SchemaVersion int         `json:"schema_version"`
	Scope         state.Scope `json:"scope"`
	OperationID   string      `json:"operation_id"`
	PID           int         `json:"pid"`
	AcquiredAt    time.Time   `json:"acquired_at"`
	AlihVersion   string      `json:"alih_version,omitempty"`
}

// HeldError reports the previous owner when it could be read safely.
type HeldError struct{ Owner *Owner }

func (e *HeldError) Error() string {
	if e == nil || e.Owner == nil {
		return ErrHeld.Error()
	}
	return fmt.Sprintf("%s by operation %s (pid %d)", ErrHeld, e.Owner.OperationID, e.Owner.PID)
}
func (e *HeldError) Unwrap() error { return ErrHeld }

// Lock is one held operating-system lock. Release is idempotent.
type Lock struct {
	path string
	file *os.File
}

// Path returns the stable lock file used for this scope.
func Path(root string, scope state.Scope) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("operation lock root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve operation lock root: %w", err)
	}
	normalized := scope.Normalize()
	if normalized.Connector == "" || normalized.WorkspaceID == "" || normalized.Destination == "" ||
		!filepath.IsAbs(normalized.Destination) {
		return "", errors.New("operation lock scope is incomplete")
	}
	return filepath.Join(absolute, lockDirectory, normalized.Key()+".lock"), nil
}

// Acquire takes the scope lock without waiting. Waiting or queuing would hide
// overlap from schedulers; the caller receives ErrHeld immediately instead.
func Acquire(root string, scope state.Scope, operationID, alihVersion string, now time.Time) (*Lock, error) {
	path, err := Path(root, scope)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(operationID) == "" {
		return nil, errors.New("operation lock requires an operation id")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create operation lock directory: %w", err)
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect operation lock directory: %w", err)
	}
	if !directoryInfo.IsDir() {
		return nil, errors.New("operation lock directory is not a real directory")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, fmt.Errorf("secure operation lock directory: %w", err)
		}
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("operation lock path is not a regular file")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("inspect operation lock path: %w", err)
	}
	file, held, err := openAndLock(path)
	if held {
		owner := readOwner(path)
		return nil, &HeldError{Owner: owner}
	}
	if err != nil {
		return nil, fmt.Errorf("acquire operation lock: %w", err)
	}
	lock := &Lock{path: path, file: file}
	owner := Owner{
		SchemaVersion: SchemaVersion, Scope: scope.Normalize(), OperationID: operationID,
		PID: os.Getpid(), AcquiredAt: now.UTC(), AlihVersion: strings.TrimSpace(alihVersion),
	}
	content, err := json.Marshal(owner)
	if err != nil {
		_ = lock.Release()
		return nil, fmt.Errorf("encode operation lock owner: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		_ = lock.Release()
		return nil, fmt.Errorf("reset operation lock owner: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = lock.Release()
		return nil, fmt.Errorf("position operation lock owner: %w", err)
	}
	if _, err := file.Write(append(content, '\n')); err != nil {
		_ = lock.Release()
		return nil, fmt.Errorf("write operation lock owner: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = lock.Release()
		return nil, fmt.Errorf("sync operation lock owner: %w", err)
	}
	if runtime.GOOS != "windows" {
		_ = file.Chmod(0o600)
	}
	return lock, nil
}

// Path returns the held lock's local file path.
func (lock *Lock) Path() string {
	if lock == nil {
		return ""
	}
	return lock.path
}

// Release drops the operating-system handle. The metadata file is retained for
// inspection, but an unlocked file never blocks a later operation.
func (lock *Lock) Release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	file := lock.file
	lock.file = nil
	return unlockAndClose(file)
}

func readOwner(path string) *Owner {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxOwnerBytes+1))
	if err != nil || len(content) > maxOwnerBytes {
		return nil
	}
	var owner Owner
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&owner); err != nil || owner.SchemaVersion != SchemaVersion ||
		owner.OperationID == "" || owner.PID <= 0 || owner.AcquiredAt.IsZero() {
		return nil
	}
	return &owner
}

// IsMissing is exposed only for callers rendering inspection without parsing
// platform-specific errors.
func IsMissing(err error) bool { return errors.Is(err, fs.ErrNotExist) }
