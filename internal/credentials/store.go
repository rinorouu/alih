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
	"strings"
)

const (
	credentialsDirectory = "alih"
	credentialsFilename  = "credentials.json"
	maxCredentialFile    = 64 * 1024
)

// ErrNotConfigured means no saved ClickUp credential exists.
var ErrNotConfigured = errors.New("ClickUp credential is not configured")

type credentialFile struct {
	Version       int    `json:"version"`
	Provider      string `json:"provider"`
	PersonalToken string `json:"personal_token"`
}

// FileStore persists a ClickUp personal token in the user's configuration
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

// Load returns the saved personal token after enforcing restrictive file
// permissions and validating the credential file shape.
func (s *FileStore) Load() (string, error) {
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
	if info.Mode().Perm()&0o077 != 0 {
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
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var saved credentialFile
	if err := decoder.Decode(&saved); err != nil {
		return "", fmt.Errorf("decode credential file: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return "", fmt.Errorf("decode credential file: %w", err)
	}
	if saved.Version != 1 || saved.Provider != "clickup" {
		return "", errors.New("credential file has an unsupported version or provider")
	}
	if err := ValidateToken(saved.PersonalToken); err != nil {
		return "", fmt.Errorf("credential file: %w", err)
	}
	return saved.PersonalToken, nil
}

// Save atomically persists token with user-only filesystem permissions.
func (s *FileStore) Save(token string) error {
	if err := ValidateToken(token); err != nil {
		return err
	}

	path, err := s.Location()
	if err != nil {
		return err
	}
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
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(credentialFile{
		Version:       1,
		Provider:      "clickup",
		PersonalToken: token,
	}); err != nil {
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

// ValidateToken rejects empty tokens and control characters that could alter
// an HTTP header. It deliberately does not assume an undocumented token shape.
func ValidateToken(token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("ClickUp personal token is empty")
	}
	if token != strings.TrimSpace(token) {
		return errors.New("ClickUp personal token has leading or trailing whitespace")
	}
	if strings.ContainsAny(token, "\r\n") {
		return errors.New("ClickUp personal token contains a line break")
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
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("credential directory permissions %04o are too broad; require 0700", info.Mode().Perm())
	}
	return nil
}
