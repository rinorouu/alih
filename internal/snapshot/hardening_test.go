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

package snapshot

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"alih/internal/connector"
)

// completeSnapshot writes a small but genuinely valid M3 snapshot that the
// corruption cases below can then damage one field at a time.
func completeSnapshot(t *testing.T) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "m3")
	workspace := connector.Workspace{ID: "w1", Name: "Hardening"}
	session, err := Begin(target, "clickup", workspace, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if err := session.RecordResponse(connector.RawResponse{
			Operation: "list Spaces", Method: "GET", Path: "/team/w1/space", Query: url.Values{},
			Attempt: 1, StatusCode: 200, Body: []byte(`{"spaces":[]}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := session.Complete(connector.ExtractionResult{
		ScanResult:    connector.ScanResult{Workspace: workspace},
		SourceObjects: []connector.SourceObject{{Type: "workspace", ID: "w1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadComplete(target); err != nil {
		t.Fatalf("fixture snapshot is not loadable to begin with: %v", err)
	}
	return target
}

func rewriteJSON(t *testing.T, path string, mutate func(map[string]any)) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	mutate(document)
	updated, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLoadCompleteRejectsDamagedRawEvidence walks the ways archived M3 evidence
// can stop supporting the inventory built from it. Every one must fail closed:
// an unreadable snapshot can never become an input to an archive.
func TestLoadCompleteRejectsDamagedRawEvidence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		corrupt func(*testing.T, string)
		want    string
	}{
		{"run record still in progress", func(t *testing.T, root string) {
			rewriteJSON(t, filepath.Join(root, "run.json"), func(d map[string]any) { d["status"] = "IN_PROGRESS" })
		}, "not COMPLETE"},
		{"run record marked failed", func(t *testing.T, root string) {
			rewriteJSON(t, filepath.Join(root, "run.json"), func(d map[string]any) { d["status"] = "FAILED" })
		}, "not COMPLETE"},
		{"unsupported run schema", func(t *testing.T, root string) {
			rewriteJSON(t, filepath.Join(root, "run.json"), func(d map[string]any) { d["schema_version"] = 99 })
		}, "unsupported raw snapshot run schema"},
		{"source identity removed", func(t *testing.T, root string) {
			rewriteJSON(t, filepath.Join(root, "run.json"), func(d map[string]any) {
				d["source"] = map[string]any{"workspace_id": "", "workspace_name": ""}
			})
		}, "missing source identity"},
		{"inventory disagrees with the run record", func(t *testing.T, root string) {
			rewriteJSON(t, filepath.Join(root, "inventory.json"), func(d map[string]any) { d["workspace_id"] = "other" })
		}, "inconsistent"},
		{"counts altered without the digest", func(t *testing.T, root string) {
			rewriteJSON(t, filepath.Join(root, "inventory.json"), func(d map[string]any) {
				d["counts"].(map[string]any)["tasks"] = float64(99)
			})
		}, "digest mismatch"},
		{"source objects altered without the digest", func(t *testing.T, root string) {
			rewriteJSON(t, filepath.Join(root, "inventory.json"), func(d map[string]any) {
				d["source_objects"] = []any{map[string]any{"type": "workspace", "id": "forged"}}
			})
		}, "digest mismatch"},
		{"request ledger sequence broken", func(t *testing.T, root string) {
			content, err := os.ReadFile(filepath.Join(root, "requests.json"))
			if err != nil {
				t.Fatal(err)
			}
			var ledger []map[string]any
			if err := json.Unmarshal(content, &ledger); err != nil {
				t.Fatal(err)
			}
			ledger[1]["sequence"] = float64(7)
			updated, _ := json.MarshalIndent(ledger, "", "  ")
			_ = os.WriteFile(filepath.Join(root, "requests.json"), updated, 0o600)
		}, "not contiguous"},
		{"ledger no longer matches the run summary", func(t *testing.T, root string) {
			rewriteJSON(t, filepath.Join(root, "run.json"), func(d map[string]any) {
				d["requests"].(map[string]any)["successful_responses"] = float64(99)
			})
		}, "does not match request ledger"},
		{"raw body altered", func(t *testing.T, root string) {
			_ = os.WriteFile(filepath.Join(root, "raw", "000001.json"), []byte(`{"spaces":[{"id":"x"}]}`), 0o600)
		}, "raw evidence"},
		{"raw body truncated", func(t *testing.T, root string) {
			_ = os.WriteFile(filepath.Join(root, "raw", "000001.json"), []byte(`{"spa`), 0o600)
		}, "raw evidence"},
		{"raw body missing", func(t *testing.T, root string) {
			_ = os.Remove(filepath.Join(root, "raw", "000001.json"))
		}, "raw evidence"},
		{"raw path escapes the snapshot", func(t *testing.T, root string) {
			content, _ := os.ReadFile(filepath.Join(root, "requests.json"))
			var ledger []map[string]any
			_ = json.Unmarshal(content, &ledger)
			ledger[0]["raw_path"] = "../../../etc/passwd"
			updated, _ := json.MarshalIndent(ledger, "", "  ")
			_ = os.WriteFile(filepath.Join(root, "requests.json"), updated, 0o600)
		}, "escapes snapshot root"},
		{"raw evidence replaced by a symlink", func(t *testing.T, root string) {
			path := filepath.Join(root, "raw", "000001.json")
			_ = os.Remove(path)
			if err := os.Symlink("/etc/hostname", path); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		}, "raw evidence"},
		{"unresolved request outcome in a complete snapshot", func(t *testing.T, root string) {
			content, _ := os.ReadFile(filepath.Join(root, "requests.json"))
			var ledger []map[string]any
			_ = json.Unmarshal(content, &ledger)
			ledger[0]["outcome"] = "OMITTED_SECRET"
			updated, _ := json.MarshalIndent(ledger, "", "  ")
			_ = os.WriteFile(filepath.Join(root, "requests.json"), updated, 0o600)
		}, "unresolved request outcome"},
		{"control file replaced by a directory", func(t *testing.T, root string) {
			_ = os.Remove(filepath.Join(root, "inventory.json"))
			_ = os.Mkdir(filepath.Join(root, "inventory.json"), 0o700)
		}, "not a regular file"},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := completeSnapshot(t)
			testCase.corrupt(t, root)
			_, err := LoadComplete(root)
			if err == nil {
				t.Fatal("LoadComplete() accepted damaged raw evidence")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func TestLoadCompleteRejectsNonDirectoryAndSymlinkRoots(t *testing.T) {
	t.Parallel()

	root := completeSnapshot(t)
	if _, err := LoadComplete(filepath.Join(root, "run.json")); err == nil {
		t.Fatal("LoadComplete() accepted a file as a snapshot root")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := LoadComplete(link); err == nil {
		t.Fatal("LoadComplete() followed a symlinked snapshot root")
	}
}

// TestInterruptedSessionIsNeverLoadableAsComplete covers the PRD section 22
// invariant "unknown extraction termination": a staging directory abandoned
// mid-run must not read as a finished snapshot.
func TestInterruptedSessionIsNeverLoadableAsComplete(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	target := filepath.Join(parent, "m3")
	session, err := Begin(target, "clickup", connector.Workspace{ID: "w1", Name: "Interrupted"}, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.RecordResponse(connector.RawResponse{
		Operation: "list Spaces", Method: "GET", Path: "/team/w1/space", Query: url.Values{},
		Attempt: 1, StatusCode: 200, Body: []byte(`{"spaces":[]}`),
	}); err != nil {
		t.Fatal(err)
	}

	// Abandon the session exactly as an uncatchable termination would.
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	staging := ""
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".m3.partial-") {
			staging = filepath.Join(parent, entry.Name())
		}
	}
	if staging == "" {
		t.Fatal("no private staging directory was created")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an unfinished extraction was already visible at the target path")
	}
	var run map[string]any
	content, err := os.ReadFile(filepath.Join(staging, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &run); err != nil {
		t.Fatal(err)
	}
	if run["status"] != "IN_PROGRESS" {
		t.Fatalf("abandoned run status = %v, want IN_PROGRESS", run["status"])
	}
	if _, err := LoadComplete(staging); err == nil {
		t.Fatal("an abandoned staging directory loaded as a complete snapshot")
	}
}

func TestSessionRefusesToOverwriteExistingEvidence(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	target := filepath.Join(parent, "m3")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Begin(target, "clickup", connector.Workspace{ID: "w1"}, testIdentity); err == nil {
		t.Fatal("Begin() overwrote an existing path")
	}

	// The same protection must hold for the failure path.
	other := filepath.Join(parent, "m3b")
	session, err := Begin(other, "clickup", connector.Workspace{ID: "w1"}, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(other+".failed", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Fail(errors.New("interrupted")); err == nil {
		t.Fatal("Fail() overwrote existing failed evidence")
	}
}

func TestSessionRejectsUseAfterClose(t *testing.T) {
	t.Parallel()

	target := filepath.Join(t.TempDir(), "m3")
	workspace := connector.Workspace{ID: "w1"}
	session, err := Begin(target, "clickup", workspace, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Complete(connector.ExtractionResult{
		ScanResult:    connector.ScanResult{Workspace: workspace},
		SourceObjects: []connector.SourceObject{{Type: "workspace", ID: "w1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.RecordResponse(connector.RawResponse{Query: url.Values{}}); err == nil {
		t.Error("a closed session accepted a new response")
	}
	if err := session.RecordFailure(connector.RequestFailure{Query: url.Values{}}); err == nil {
		t.Error("a closed session accepted a new failure")
	}
	if _, err := session.Complete(connector.ExtractionResult{}); err == nil {
		t.Error("a closed session was completed twice")
	}
	if _, err := session.Fail(errors.New("late")); err == nil {
		t.Error("a closed session was failed after completion")
	}
}

// TestRecordResponseReportsWriteFailure covers the filesystem failure path:
// evidence that cannot be persisted must surface as an error and be accounted
// for in the ledger, never dropped.
func TestRecordResponseReportsWriteFailure(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not deny writes on Windows")
	}

	parent := t.TempDir()
	target := filepath.Join(parent, "m3")
	session, err := Begin(target, "clickup", connector.Workspace{ID: "w1"}, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	// Make the raw evidence directory unwritable so the next write fails.
	rawDirectory := filepath.Join(session.stagingPath, "raw")
	if err := os.Chmod(rawDirectory, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(rawDirectory, 0o700)
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not deny writes")
	}

	err = session.RecordResponse(connector.RawResponse{
		Operation: "list Spaces", Method: "GET", Path: "/team/w1/space", Query: url.Values{},
		Attempt: 1, StatusCode: 200, Body: []byte(`{"spaces":[]}`),
	})
	if err == nil {
		t.Fatal("RecordResponse() reported success when the write failed")
	}
	if len(session.records) != 1 || session.records[0].Outcome != "WRITE_FAILED" {
		t.Fatalf("ledger = %#v, want the failure accounted for", session.records)
	}
	if session.rawResponses != 0 {
		t.Fatal("a failed write was counted as persisted raw evidence")
	}
}

func TestSourceObjectIndexRejectsUnusableGraphs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		objects []connector.SourceObject
		want    string
	}{
		{"empty type", []connector.SourceObject{{Type: "", ID: "a"}}, "empty source type or id"},
		{"empty id", []connector.SourceObject{{Type: "task", ID: ""}}, "empty source type or id"},
		{"duplicate object", []connector.SourceObject{{Type: "task", ID: "a"}, {Type: "task", ID: "a"}}, "duplicate source object"},
		{"half a parent reference", []connector.SourceObject{{Type: "task", ID: "a", ParentType: "list"}}, "incomplete parent reference"},
		{"missing parent", []connector.SourceObject{{Type: "task", ID: "a", ParentType: "list", ParentID: "gone"}}, "references missing parent"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := validateSourceObjects(testCase.objects)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}
