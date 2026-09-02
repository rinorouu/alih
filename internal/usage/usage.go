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

// Package usage records how an installation is operated: by the person who
// installed it, or by Alih Assistance on their behalf.
//
// # What a mode is, and what it is not
//
// A mode answers exactly one question: *who is responsible for operating this
// installation?* It does not answer what this binary may do. Every capability
// Alih has — connectors, backup, verify, report, organize, scheduling,
// notifications — is available in every mode, and there is no code path
// anywhere that consults a mode before doing work.
//
// That is a product commitment, not an implementation detail: Alih is free and
// open source, and Assistance is an optional service where somebody else
// operates the same binary. Nobody pays to unlock a feature. The rule is kept
// honest structurally rather than by discipline — internal/hardening asserts
// that no package outside the setup and status surfaces may even import this
// one, so a future `if mode == assistance { enable(...) }` cannot compile
// without a test failing first.
//
// # What is deliberately absent
//
// There is no subscription state here, and there must never be. No ACTIVE, no
// TRIAL, no EXPIRED. Whether somebody has an Assistance subscription is a fact
// only a future Assistance system could know, and a local boolean that a user
// or a corrupted file can change is not evidence of anything. This package
// records a locally chosen operating preference and nothing more.
package usage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Mode is how an installation is operated.
type Mode string

const (
	// SelfManaged means the person who installed Alih configures and runs it.
	// This is the default, and the meaning of no recorded choice at all.
	SelfManaged Mode = "self-managed"
	// Assistance means the person intends Alih Assistance to operate this
	// installation for them. It records intent only: it is not a subscription,
	// and it grants nothing.
	Assistance Mode = "assistance"
)

// SchemaVersion is the recorded usage document this build writes.
const SchemaVersion = 1

const (
	configDirectory = "alih"
	configFilename  = "usage.json"
	maxUsageFile    = 16 * 1024
)

// ErrNotChosen means no mode has been recorded. It is not an error condition:
// an installation that never ran setup is self-managed, which is why every
// command works without setup ever having been run.
var ErrNotChosen = errors.New("no usage mode has been chosen")

// Valid reports whether mode is one this build understands.
func (m Mode) Valid() bool { return m == SelfManaged || m == Assistance }

// Display is how a mode is named to a person.
func (m Mode) Display() string {
	switch m {
	case SelfManaged:
		return "Self-managed"
	case Assistance:
		return "Alih Assistance"
	default:
		return string(m)
	}
}

// document is the on-disk shape. It carries no secret, no account, no
// subscription state, and nothing that could identify an installation.
type document struct {
	SchemaVersion int    `json:"schema_version"`
	Mode          Mode   `json:"mode"`
	ChosenAt      string `json:"chosen_at"`
}

// Store persists the chosen mode beside Alih's other local configuration.
type Store struct{ path string }

// NewStore creates a store at path. An empty path resolves to the user
// configuration directory, beside the credential store and operational state.
func NewStore(path string) *Store { return &Store{path: path} }

// Location returns the usage file path without creating it.
func (s *Store) Location() (string, error) {
	if s.path != "" {
		return s.path, nil
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user configuration directory: %w", err)
	}
	return filepath.Join(directory, configDirectory, configFilename), nil
}

// Load returns the recorded mode.
//
// A missing file returns ErrNotChosen, which callers treat as self-managed. A
// file this build cannot read is an error rather than a silent default: Alih
// never rewrites local state it does not understand, and pretending a damaged
// file said "self-managed" would hide the damage.
func (s *Store) Load() (Mode, error) {
	path, err := s.Location()
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotChosen
	}
	if err != nil {
		return "", fmt.Errorf("inspect usage file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}

	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open usage file: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxUsageFile+1))
	if err != nil {
		return "", fmt.Errorf("read usage file: %w", err)
	}
	if len(content) > maxUsageFile {
		return "", fmt.Errorf("usage file exceeds %d bytes", maxUsageFile)
	}

	var probe struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(content, &probe); err != nil {
		return "", fmt.Errorf("decode usage file: %w", err)
	}
	if probe.SchemaVersion > SchemaVersion {
		return "", fmt.Errorf("usage file schema version %d is newer than this build understands", probe.SchemaVersion)
	}

	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var saved document
	if err := decoder.Decode(&saved); err != nil {
		return "", fmt.Errorf("decode usage file: %w", err)
	}
	if !saved.Mode.Valid() {
		return "", fmt.Errorf("usage file records unknown mode %q", saved.Mode)
	}
	return saved.Mode, nil
}

// Save atomically records the chosen mode with user-only permissions.
func (s *Store) Save(mode Mode, now time.Time) error {
	if !mode.Valid() {
		return fmt.Errorf("refusing to record unknown usage mode %q", mode)
	}
	path, err := s.Location()
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("secure configuration directory: %w", err)
		}
	}

	encoded, err := json.Marshal(document{
		SchemaVersion: SchemaVersion, Mode: mode, ChosenAt: now.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("encode usage file: %w", err)
	}

	temporary, err := os.CreateTemp(directory, ".usage-*")
	if err != nil {
		return fmt.Errorf("create temporary usage file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary usage file: %w", err)
	}
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write usage file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync usage file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close usage file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit usage file: %w", err)
	}
	committed = true
	return nil
}

// ParseMode reads a mode written by a person or a script.
func ParseMode(value string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(value))) {
	case SelfManaged:
		return SelfManaged, nil
	case Assistance:
		return Assistance, nil
	default:
		return "", fmt.Errorf("unknown usage mode %q; use %q or %q", value, SelfManaged, Assistance)
	}
}
