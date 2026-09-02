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

package usage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "usage.json")
	return NewStore(path), path
}

// TestNoRecordedChoiceMeansSelfManaged is the compatibility guarantee: an
// installation upgrading from a build without setup must keep working.
func TestNoRecordedChoiceMeansSelfManaged(t *testing.T) {
	t.Parallel()
	store, _ := testStore(t)

	if _, err := store.Load(); !errors.Is(err, ErrNotChosen) {
		t.Fatalf("error = %v, want ErrNotChosen", err)
	}
}

func TestEachModeRoundTrips(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{SelfManaged, Assistance} {
		mode := mode
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			store, path := testStore(t)
			if err := store.Save(mode, time.Unix(0, 0)); err != nil {
				t.Fatalf("save: %v", err)
			}
			loaded, err := store.Load()
			if err != nil || loaded != mode {
				t.Fatalf("loaded %q, %v", loaded, err)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o600 {
					t.Errorf("usage file permissions %04o, want 0600", info.Mode().Perm())
				}
			}
		})
	}
}

func TestSavingReplacesRatherThanAccumulates(t *testing.T) {
	t.Parallel()
	store, _ := testStore(t)
	for _, mode := range []Mode{SelfManaged, Assistance, SelfManaged} {
		if err := store.Save(mode, time.Unix(0, 0)); err != nil {
			t.Fatalf("save %q: %v", mode, err)
		}
		if loaded, err := store.Load(); err != nil || loaded != mode {
			t.Fatalf("after saving %q, loaded %q, %v", mode, loaded, err)
		}
	}
}

// TestUnreadableStateIsRefusedNotGuessed matches how every other Alih state
// file behaves: damaged local state is reported, never silently defaulted.
func TestUnreadableStateIsRefusedNotGuessed(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct{ name, content string }{
		{"not json", "not json at all"},
		{"unknown mode", `{"schema_version":1,"mode":"premium","chosen_at":""}`},
		{"unknown field", `{"schema_version":1,"mode":"self-managed","subscription":"active"}`},
		{"newer schema", `{"schema_version":99,"mode":"self-managed","chosen_at":""}`},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			store, path := testStore(t)
			if err := os.WriteFile(path, []byte(testCase.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(); err == nil || errors.Is(err, ErrNotChosen) {
				t.Fatalf("error = %v; damaged state must be reported, not defaulted", err)
			}
			content, err := os.ReadFile(path)
			if err != nil || string(content) != testCase.content {
				t.Error("reading damaged state rewrote it")
			}
		})
	}
}

func TestAnUnknownModeIsNeverRecorded(t *testing.T) {
	t.Parallel()
	store, path := testStore(t)
	if err := store.Save(Mode("premium"), time.Unix(0, 0)); err == nil {
		t.Fatal("an unknown mode was recorded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a rejected save created a file")
	}
}

func TestParseModeAcceptsOnlyTheTwoModes(t *testing.T) {
	t.Parallel()
	for _, valid := range []string{"self-managed", "SELF-MANAGED", " assistance "} {
		if _, err := ParseMode(valid); err != nil {
			t.Errorf("ParseMode(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "pro", "premium", "free", "enterprise", "trial"} {
		if _, err := ParseMode(invalid); err == nil {
			t.Errorf("ParseMode(%q) was accepted", invalid)
		}
	}
}

// TestTheRecordCarriesNothingSensitive proves the file is safe to exist: no
// secret, no account, no identifier, and nothing about money.
func TestTheRecordCarriesNothingSensitive(t *testing.T) {
	t.Parallel()
	store, path := testStore(t)
	if err := store.Save(Assistance, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	document := strings.ToLower(string(content))
	for _, forbidden := range []string{"token", "secret", "email", "account", "customer", "subscription", "payment", "licence", "license"} {
		if strings.Contains(document, forbidden) {
			t.Errorf("the usage record contains %q: %s", forbidden, content)
		}
	}
	for _, expected := range []string{"schema_version", "mode", "assistance"} {
		if !strings.Contains(document, expected) {
			t.Errorf("the usage record is missing %q: %s", expected, content)
		}
	}
}
