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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileStoreSavesAndLoadsWithRestrictivePermissions(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "config", "alih")
	path := filepath.Join(directory, "credentials.json")
	store := NewFileStore(path)

	if err := store.Save("clickup", "pk_test_secret"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if runtime.GOOS != "windows" {
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
	}

	token, err := store.Load("clickup")
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
	_, err := store.Load("clickup")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Load() error = %v, want ErrNotConfigured", err)
	}
}

func TestFileStoreRejectsBroadFilePermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows reports synthesized Unix permission bits")
	}

	directory := filepath.Join(t.TempDir(), "alih")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "credentials.json")
	content := `{"version":1,"provider":"clickup","personal_token":"secret"}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := NewFileStore(path).Load("clickup")
	if err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("Load() error = %v, want permissions error", err)
	}
}

func TestFileStoreRejectsBroadDirectoryPermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows reports synthesized Unix permission bits")
	}

	directory := filepath.Join(t.TempDir(), "alih")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "credentials.json")
	content := `{"version":1,"provider":"clickup","personal_token":"secret"}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewFileStore(path).Load("clickup")
	if err == nil || !strings.Contains(err.Error(), "directory permissions") {
		t.Fatalf("Load() error = %v, want directory permissions error", err)
	}
}

func TestFileStoreRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
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

	_, err := NewFileStore(path).Load("clickup")
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
			if _, err := NewFileStore(path).Load("clickup"); err == nil {
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

// TestALegacyCredentialFileStillLoadsAsItsProvider proves the file an Alih
// released before multi-connector support wrote is still read, and is read as
// the connector it named rather than as "the" credential.
func TestALegacyCredentialFileStillLoadsAsItsProvider(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "credentials.json")
	writeFixture(t, path, `{"version":1,"provider":"clickup","personal_token":"pk_released_secret"}`)

	token, err := NewFileStore(path).Load("clickup")
	if err != nil {
		t.Fatalf("load a released credential file: %v", err)
	}
	if token != "pk_released_secret" {
		t.Fatalf("token = %q, want the exact released secret", token)
	}

	// The same file holds nothing for any other connector, and says so with
	// the not-configured answer rather than by handing over ClickUp's secret.
	if _, err := NewFileStore(path).Load("other"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("loading another connector returned %v, want ErrNotConfigured", err)
	}
}

// TestSavingOneConnectorKeepsTheFileReadableByOlderAlih proves the migration is
// tied to a real cause. While ClickUp is the only credential stored the file
// keeps the shape v0.2.5 can read; a second connector is what moves it.
func TestSavingOneConnectorKeepsTheFileReadableByOlderAlih(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "credentials.json")
	store := NewFileStore(path)
	if err := store.Save("clickup", "pk_one"); err != nil {
		t.Fatalf("save: %v", err)
	}

	var legacy struct {
		Version       int    `json:"version"`
		Provider      string `json:"provider"`
		PersonalToken string `json:"personal_token"`
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &legacy); err != nil {
		t.Fatalf("a single ClickUp credential must still be the released shape: %v", err)
	}
	if legacy.Version != 1 || legacy.Provider != "clickup" || legacy.PersonalToken != "pk_one" {
		t.Fatalf("file = %s, want the version 1 shape", content)
	}
}

// TestTwoConnectorsCoexistWithoutOverwritingEachOther is the blocker this work
// exists to close: before it, saving a second connector replaced the first.
func TestTwoConnectorsCoexistWithoutOverwritingEachOther(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "credentials.json")
	store := NewFileStore(path)
	if err := store.Save("clickup", "pk_first"); err != nil {
		t.Fatalf("save first: %v", err)
	}
	if err := store.Save("example", "pk_second"); err != nil {
		t.Fatalf("save second: %v", err)
	}

	first, err := store.Load("clickup")
	if err != nil || first != "pk_first" {
		t.Fatalf("first connector credential = %q, %v; the second overwrote it", first, err)
	}
	second, err := store.Load("example")
	if err != nil || second != "pk_second" {
		t.Fatalf("second connector credential = %q, %v", second, err)
	}

	// Replacing one leaves the other alone.
	if err := store.Save("clickup", "pk_rotated"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated, err := store.Load("clickup"); err != nil || rotated != "pk_rotated" {
		t.Fatalf("rotated credential = %q, %v", rotated, err)
	}
	if kept, err := store.Load("example"); err != nil || kept != "pk_second" {
		t.Fatalf("rotating one connector disturbed another: %q, %v", kept, err)
	}
}

// TestAnUnsupportedCredentialSchemaIsRefusedNotGuessed proves a file from a
// build this one does not understand is never partially read. Overwriting it
// would destroy a credential, so refusing is the only safe answer.
func TestAnUnsupportedCredentialSchemaIsRefusedNotGuessed(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "credentials.json")
	writeFixture(t, path, `{"version":99,"credentials":[{"connector":"clickup","secret":"pk_future"}]}`)

	if _, err := NewFileStore(path).Load("clickup"); err == nil ||
		!strings.Contains(err.Error(), "not supported by this build") {
		t.Fatalf("error = %v, want an explicit refusal naming the schema", err)
	}
	// Saving must not silently replace what could not be read.
	if err := NewFileStore(path).Save("clickup", "pk_new"); err == nil {
		t.Fatal("saving over an unreadable credential file was allowed")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "pk_future") {
		t.Fatal("the unreadable credential file was overwritten")
	}
}

// TestCredentialStoreNamesNoProvider proves Core's credential vocabulary is
// connector-neutral: no exported error or validation message names ClickUp.
func TestCredentialStoreNamesNoProvider(t *testing.T) {
	t.Parallel()

	messages := []string{ErrNotConfigured.Error()}
	for _, invalid := range []string{"", " pk_x", "pk\nx"} {
		if err := ValidateToken(invalid); err != nil {
			messages = append(messages, err.Error())
		}
	}
	if err := ValidateConnector("Bad Name"); err != nil {
		messages = append(messages, err.Error())
	}
	for _, message := range messages {
		if strings.Contains(strings.ToLower(message), "clickup") {
			t.Errorf("credential vocabulary names a provider: %q", message)
		}
	}
}

// TestConnectorNameIsValidatedBeforeItSelectsACredential proves a connector
// identifier cannot carry path separators or anything else that would let it
// address something other than an entry in the file.
func TestConnectorNameIsValidatedBeforeItSelectsACredential(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "credentials.json")
	for _, name := range []string{"", "../escape", "a/b", "UPPER", "sp ace", strings.Repeat("x", 65)} {
		if _, err := NewFileStore(path).Load(name); err == nil {
			t.Errorf("Load(%q) was accepted", name)
		}
		if err := NewFileStore(path).Save(name, "pk_x"); err == nil {
			t.Errorf("Save(%q) was accepted", name)
		}
	}
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	// The store refuses a world-readable credential directory, which is the
	// point; MkdirAll leaves the umask in place, so tighten it deliberately.
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
