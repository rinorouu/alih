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

package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStoreSavesAndLoadsWithRestrictivePermissions(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "config", "alih")
	path := filepath.Join(directory, "credentials.json")
	store := NewFileStore(path)

	if err := store.Save("pk_test_secret"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory permissions = %04o, want 0700", got)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("file permissions = %04o, want 0600", got)
	}

	token, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if token != "pk_test_secret" {
		t.Fatal("Load() did not return the saved token")
	}
}

func TestFileStoreReportsNotConfigured(t *testing.T) {
	t.Parallel()

	store := NewFileStore(filepath.Join(t.TempDir(), "missing", "credentials.json"))
	_, err := store.Load()
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Load() error = %v, want ErrNotConfigured", err)
	}
}

func TestFileStoreRejectsBroadFilePermissions(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "alih")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "credentials.json")
	content := `{"version":1,"provider":"clickup","personal_token":"secret"}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := NewFileStore(path).Load()
	if err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("Load() error = %v, want permissions error", err)
	}
}

func TestFileStoreRejectsBroadDirectoryPermissions(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "alih")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "credentials.json")
	content := `{"version":1,"provider":"clickup","personal_token":"secret"}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewFileStore(path).Load()
	if err == nil || !strings.Contains(err.Error(), "directory permissions") {
		t.Fatalf("Load() error = %v, want directory permissions error", err)
	}
}

func TestFileStoreRejectsSymlink(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("symlink permissions differ on Windows")
	}

	directory := filepath.Join(t.TempDir(), "alih")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"provider":"clickup","personal_token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "credentials.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	_, err := NewFileStore(path).Load()
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Load() error = %v, want regular-file error", err)
	}
}

func TestFileStoreRejectsUnexpectedOrMalformedContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "wrong provider", content: `{"version":1,"provider":"other","personal_token":"secret"}`},
		{name: "unknown field", content: `{"version":1,"provider":"clickup","personal_token":"secret","extra":true}`},
		{name: "multiple values", content: `{"version":1,"provider":"clickup","personal_token":"secret"}{}`},
		{name: "empty token", content: `{"version":1,"provider":"clickup","personal_token":""}`},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(t.TempDir(), "alih")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "credentials.json")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFileStore(path).Load(); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func TestValidateToken(t *testing.T) {
	t.Parallel()

	for _, invalid := range []string{"", "  ", " token", "token ", "token\nheader", "token\rheader"} {
		if err := ValidateToken(invalid); err == nil {
			t.Errorf("ValidateToken(%q) error = nil", invalid)
		}
	}
	if err := ValidateToken("pk_valid"); err != nil {
		t.Fatalf("ValidateToken(valid) error = %v", err)
	}
}
