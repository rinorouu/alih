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

// Package credentials stores source credentials locally without exposing them
// to logs, command-line arguments, archives, or normal command output.
package credentials

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	credentialsDirectory = "alih"
	credentialsFilename  = "credentials.json"
	maxCredentialFile    = 64 * 1024
	maxConnectorName     = 64
	// legacyProvider is the one connector the version 1 file could hold.
	legacyProvider = "clickup"
)

// ErrNotConfigured means no saved credential exists for the connector asked
// about. It does not mean the file is missing: a file holding another
// connector's credential answers this way too.
var ErrNotConfigured = errors.New("credential is not configured")

// SchemaVersion is the credential file this build writes when it holds more
// than one connector. Version 1 held exactly one credential with the provider
// written as a field, which is still read and still written while ClickUp is
// the only connector configured, so an installation that never adds a second
// connector stays readable by an older Alih.
const SchemaVersion = 2

// legacyFile is the single-credential shape released through v0.2.5.
type legacyFile struct {
	Version       int    `json:"version"`
	Provider      string `json:"provider"`
	PersonalToken string `json:"personal_token"`
}

// credentialEntry is one connector's secret.
type credentialEntry struct {
	Connector string `json:"connector"`
	Secret    string `json:"secret"`
}

// credentialFile is the multi-connector shape. Entries are kept sorted by
// connector so the file is byte-stable for a given set of credentials.
type credentialFile struct {
	Version     int               `json:"version"`
	Credentials []credentialEntry `json:"credentials"`
}

// FileStore persists one secret per connector in the user's configuration
// directory. An explicit path is accepted only to keep tests isolated.
type FileStore struct {
	path string
}

// NewFileStore creates a store at path. If path is empty, the platform user
// configuration directory is resolved when the store is first used.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

// Location returns the credential file path without creating it.
func (s *FileStore) Location() (string, error) {
	if s.path != "" {
		return s.path, nil
	}

	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user configuration directory: %w", err)
	}
	return filepath.Join(directory, credentialsDirectory, credentialsFilename), nil
}

// Load returns the saved secret for one connector after enforcing restrictive
// file permissions and validating the credential file shape. A file written by
// an earlier Alih holds one credential with its provider named inside, and is
// read as exactly that connector's credential.
func (s *FileStore) Load(connectorName string) (string, error) {
	if err := ValidateConnector(connectorName); err != nil {
		return "", err
	}
	path, err := s.Location()
	if err != nil {
		return "", err
	}
	if err := validateSecureDirectory(filepath.Dir(path)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNotConfigured
		}
		return "", err
	}

	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotConfigured
	}
	if err != nil {
		return "", fmt.Errorf("inspect credential file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("credential path is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("credential file permissions %04o are too broad; require 0600", info.Mode().Perm())
	}

	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open credential file: %w", err)
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxCredentialFile+1))
	if err != nil {
		return "", fmt.Errorf("read credential file: %w", err)
	}
	if len(content) > maxCredentialFile {
		return "", fmt.Errorf("credential file exceeds %d bytes", maxCredentialFile)
	}
	entries, err := decodeCredentials(content)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Connector != connectorName {
			continue
		}
		if err := ValidateToken(entry.Secret); err != nil {
			return "", fmt.Errorf("credential file: %w", err)
		}
		return entry.Secret, nil
	}
	return "", ErrNotConfigured
}

// decodeCredentials reads either credential file shape. Version 1 is a single
// credential naming its provider; version 2 is a list. Neither is guessed at:
// an unknown version is refused rather than read as the shape that happens to
// parse, so a file from a newer Alih is never half-understood.
func decodeCredentials(content []byte) ([]credentialEntry, error) {
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(content, &probe); err != nil {
		return nil, fmt.Errorf("decode credential file: %w", err)
	}
	switch probe.Version {
	case 1:
		var saved legacyFile
		if err := strictDecode(content, &saved); err != nil {
			return nil, err
		}
		if strings.TrimSpace(saved.Provider) == "" {
			return nil, errors.New("credential file names no provider")
		}
		return []credentialEntry{{Connector: saved.Provider, Secret: saved.PersonalToken}}, nil
	case SchemaVersion:
		var saved credentialFile
		if err := strictDecode(content, &saved); err != nil {
			return nil, err
		}
		seen := make(map[string]struct{}, len(saved.Credentials))
		for _, entry := range saved.Credentials {
			if err := ValidateConnector(entry.Connector); err != nil {
				return nil, fmt.Errorf("credential file: %w", err)
			}
			if _, duplicate := seen[entry.Connector]; duplicate {
				return nil, fmt.Errorf("credential file records %q more than once", entry.Connector)
			}
			seen[entry.Connector] = struct{}{}
		}
		return saved.Credentials, nil
	default:
		return nil, fmt.Errorf("credential file schema version %d is not supported by this build", probe.Version)
	}
}

func strictDecode(content []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode credential file: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode credential file: %w", err)
	}
	return nil
}

// Save atomically persists one connector's secret with user-only filesystem
// permissions, leaving every other connector's credential untouched.
//
// The file is written in the version 1 shape while ClickUp is the only
// credential stored, so an installation that never adds a second connector can
// still be read by an Alih released before this change. The moment a second
// connector is saved the file becomes version 2, which older builds refuse
// explicitly rather than misread. That migration is one-way and happens only
// when a second connector genuinely exists.
func (s *FileStore) Save(connectorName, token string) error {
	if err := ValidateConnector(connectorName); err != nil {
		return err
	}
	if err := ValidateToken(token); err != nil {
		return err
	}

	path, err := s.Location()
	if err != nil {
		return err
	}
	entries, err := s.existingEntries(path)
	if err != nil {
		return err
	}
	document := mergeCredential(entries, connectorName, token)

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect credential directory: %w", err)
	}
	if !directoryInfo.IsDir() {
		return errors.New("credential directory path is not a directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure credential directory: %w", err)
	}

	temporary, err := os.CreateTemp(directory, ".credentials-*")
	if err != nil {
		return fmt.Errorf("create temporary credential file: %w", err)
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
		return fmt.Errorf("secure temporary credential file: %w", err)
	}
	if err := json.NewEncoder(temporary).Encode(document); err != nil {
		return fmt.Errorf("write credential file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync credential file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close credential file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit credential file: %w", err)
	}
	committed = true
	return nil
}

// ValidateToken rejects empty secrets and control characters that could alter
// an HTTP header. It deliberately does not assume any provider's token shape.
func ValidateToken(token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("credential is empty")
	}
	if token != strings.TrimSpace(token) {
		return errors.New("credential has leading or trailing whitespace")
	}
	if strings.ContainsAny(token, "\r\n") {
		return errors.New("credential contains a line break")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateSecureDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("credential directory path is not a directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("credential directory permissions %04o are too broad; require 0700", info.Mode().Perm())
	}
	return nil
}

// existingEntries reads whatever credentials are already stored so that saving
// one connector never discards another. A missing file is not an error; an
// unreadable one is, because overwriting a file this build cannot parse would
// destroy a credential it does not understand.
func (s *FileStore) existingEntries(path string) ([]credentialEntry, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read credential file: %w", err)
	}
	if len(content) > maxCredentialFile {
		return nil, fmt.Errorf("credential file exceeds %d bytes", maxCredentialFile)
	}
	entries, err := decodeCredentials(content)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// mergeCredential replaces one connector's secret and returns the document to
// write. The version is chosen by what the result actually holds rather than by
// what this build prefers, which is what keeps a single ClickUp installation
// readable by an older Alih.
func mergeCredential(entries []credentialEntry, connectorName, token string) any {
	merged := make([]credentialEntry, 0, len(entries)+1)
	replaced := false
	for _, entry := range entries {
		if entry.Connector == connectorName {
			merged = append(merged, credentialEntry{Connector: connectorName, Secret: token})
			replaced = true
			continue
		}
		merged = append(merged, entry)
	}
	if !replaced {
		merged = append(merged, credentialEntry{Connector: connectorName, Secret: token})
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Connector < merged[j].Connector })

	if len(merged) == 1 && merged[0].Connector == legacyProvider {
		return legacyFile{Version: 1, Provider: legacyProvider, PersonalToken: merged[0].Secret}
	}
	return credentialFile{Version: SchemaVersion, Credentials: merged}
}

// ValidateConnector rejects a connector identifier that could not have come
// from a wired adapter. The name selects a credential and is written into a
// file, so it is held to the same shape Alih uses for connector identity
// everywhere else rather than accepted as free text.
func ValidateConnector(name string) error {
	if name == "" {
		return errors.New("connector name is empty")
	}
	if len(name) > maxConnectorName {
		return fmt.Errorf("connector name exceeds %d bytes", maxConnectorName)
	}
	for _, character := range name {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= '0' && character <= '9':
		case character == '-' || character == '_':
		default:
			return fmt.Errorf("connector name %q may use only lowercase letters, digits, - and _", name)
		}
	}
	return nil
}
