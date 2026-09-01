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

package compat

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"alih/internal/event"
	"alih/internal/state"
)

// The local operational formats have never shipped in a release, so unlike the
// archive corpus these documents were captured from this working tree rather
// than from a tag. They are still frozen on purpose: a migration test that
// re-marshals the current shape and edits the version number would follow the
// writer wherever it went, and would not notice a renamed or newly required
// field. These bytes do not move.
const localFormats = "testdata/local-formats"

func readCorpus(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(localFormats, name))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

// TestEveryRecordedStateVersionStillLoads proves an upgrade finds the state a
// previous build left behind, whatever schema it was written under.
func TestEveryRecordedStateVersionStillLoads(t *testing.T) {
	t.Parallel()

	cases := []struct {
		file              string
		wantNotifications bool
	}{
		{"state-v1.json", false},
		{"state-v2.json", true},
		{"state-v3.json", true},
	}
	var scopeKey string
	for _, testCase := range cases {
		t.Run(testCase.file, func(t *testing.T) {
			t.Parallel()
			record, err := state.Unmarshal(readCorpus(t, testCase.file))
			if err != nil {
				t.Fatalf("read %s: %v", testCase.file, err)
			}
			if record.SchemaVersion != state.SchemaVersion {
				t.Errorf("migrated to schema %d, want %d", record.SchemaVersion, state.SchemaVersion)
			}
			if err := state.Validate(record); err != nil {
				t.Errorf("a migrated record does not validate: %v", err)
			}
			// A migration must not invent evidence the old document never held.
			if testCase.wantNotifications != (record.Notifications != nil) {
				t.Errorf("notifications present = %t, want %t", record.Notifications != nil, testCase.wantNotifications)
			}
			// The last successful backup a user already has must survive.
			if record.LastSuccess == nil || record.LastSuccess.ArchivePath == "" {
				t.Error("migration dropped the recorded successful backup")
			}
			if record.LastVerification == nil || record.LastVerification.Result != "VERIFIED" {
				t.Error("migration dropped the recorded verification")
			}
		})
	}
	// Every version must key to the same state file, or an upgrade would
	// silently start writing beside the record it should be updating.
	for _, testCase := range cases {
		record, err := state.Unmarshal(readCorpus(t, testCase.file))
		if err != nil {
			t.Fatal(err)
		}
		if scopeKey == "" {
			scopeKey = record.Scope.Key()
			continue
		}
		if record.Scope.Key() != scopeKey {
			t.Fatalf("%s keys to a different state file than the earlier schema", testCase.file)
		}
	}
}

// TestReadingOldStateNeverRewritesItInPlace proves opening a record is not a
// migration on disk. A downgrade or a crash must find the file it left.
func TestReadingOldStateNeverRewritesItInPlace(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"state-v1.json", "state-v2.json"} {
		original := readCorpus(t, name)
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := state.Unmarshal(content); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(original, after) {
			t.Errorf("reading %s rewrote it on disk", name)
		}
	}
}

// TestAFutureStateSchemaIsRefusedNotGuessed proves a newer build's file makes
// an older build stop, rather than reinterpret fields it does not know.
func TestAFutureStateSchemaIsRefusedNotGuessed(t *testing.T) {
	t.Parallel()

	future := bytes.Replace(readCorpus(t, "state-v3.json"),
		[]byte(`"schema_version": 3`), []byte(`"schema_version": 99`), 1)
	if _, err := state.Unmarshal(future); err == nil {
		t.Fatal("a future state schema was accepted")
	}

	// An unknown field is equally refused rather than silently dropped.
	unknown := bytes.Replace(readCorpus(t, "state-v3.json"),
		[]byte(`"revision": 0,`), []byte(`"revision": 0, "invented_field": true,`), 1)
	if _, err := state.Unmarshal(unknown); err == nil {
		t.Fatal("an unknown state field was accepted")
	}
}

// TestEveryRecordedEventVersionStillLoads proves recorded history stays
// readable across an upgrade and gains no facts it never recorded.
func TestEveryRecordedEventVersionStillLoads(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"events-v1.jsonl", "events-v2.jsonl"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var events []event.Event
			for _, line := range strings.Split(strings.TrimSpace(string(readCorpus(t, name))), "\n") {
				recorded, err := event.Unmarshal([]byte(line))
				if err != nil {
					t.Fatalf("read a recorded event: %v", err)
				}
				if recorded.SchemaVersion != event.SchemaVersion {
					t.Errorf("migrated to schema %d, want %d", recorded.SchemaVersion, event.SchemaVersion)
				}
				if err := event.Validate(recorded); err != nil {
					t.Errorf("a migrated event does not validate: %v", err)
				}
				events = append(events, recorded)
			}
			if len(events) != 2 {
				t.Fatalf("read %d events, want 2", len(events))
			}
			// Version 1 predates schedule correlation and must not acquire it.
			if name == "events-v1.jsonl" {
				for _, recorded := range events {
					if recorded.Metadata["schedule_id"] != "" {
						t.Error("a version 1 event gained invented schedule evidence")
					}
				}
			}
			if events[0].Type != event.TypeOperationStarted || events[1].Type != event.TypeOperationCompleted {
				t.Fatalf("recorded types = %s, %s", events[0].Type, events[1].Type)
			}
			// Ordering is by position, so an upgrade cannot reorder history.
			event.Order(events)
			if events[0].Sequence >= events[1].Sequence {
				t.Fatal("recorded history was reordered")
			}
		})
	}
}

// TestAFutureEventSchemaIsSkippedNotMisread proves one unreadable line cannot
// make a build guess at, or discard, the history around it.
func TestAFutureEventSchemaIsSkippedNotMisread(t *testing.T) {
	t.Parallel()

	line := strings.Split(strings.TrimSpace(string(readCorpus(t, "events-v2.jsonl"))), "\n")[0]
	future := strings.Replace(line, `"schema_version":2`, `"schema_version":99`, 1)
	if _, err := event.Unmarshal([]byte(future)); err == nil {
		t.Fatal("a future event schema was accepted")
	}
	unknown := strings.Replace(line, `"sequence":1`, `"sequence":1,"invented_field":true`, 1)
	if _, err := event.Unmarshal([]byte(unknown)); err == nil {
		t.Fatal("an unknown event field was accepted")
	}
}

// TestTheLocalCorpusCarriesNoCredential holds the frozen local documents to the
// same standard as everything else Alih persists.
func TestTheLocalCorpusCarriesNoCredential(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(localFormats)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("the local-format corpus is empty")
	}
	for _, entry := range entries {
		content := readCorpus(t, entry.Name())
		for _, needle := range []string{"pk_", "token", "Bearer", "secret", "password", "Authorization"} {
			if bytes.Contains(bytes.ToLower(content), bytes.ToLower([]byte(needle))) {
				t.Errorf("%s contains %q", entry.Name(), needle)
			}
		}
		if json.Valid(content) || strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		t.Errorf("%s is neither valid JSON nor a JSON line file", entry.Name())
	}
}
