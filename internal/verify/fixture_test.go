// Copyright 2026 rinorouu
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

package verify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/connector/clickup"
	"alih/internal/snapshot"
)

var attachmentContent = []byte("portable attachment\n")

type fixtureRoundTrip func(*http.Request) (*http.Response, error)

func (function fixtureRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type fixtureResponse struct {
	operation string
	path      string
	body      string
}

// fixtureResponses is a minimal but realistic ClickUp traversal: one Space,
// one List, two Tasks, one nested Task, one comment with a threaded reply, one
// enumerated Custom Field with an observed value, one attachment and one
// dependency relationship.
func fixtureResponses() []fixtureResponse {
	return []fixtureResponse{
		{"list Spaces", "/team/w1/space", `{"spaces":[{"id":"s1","name":"Space"}]}`},
		{"list folderless Lists", "/space/s1/list", `{"lists":[{"id":"l1","name":"List"}]}`},
		{"list Custom Fields", "/list/l1/field", `{"fields":[{"id":"f1","name":"Workstream","type":"drop_down","type_config":{"options":[{"id":"o1","name":"Engineering","orderindex":0},{"id":"o2","name":"Sales","orderindex":1}]}}]}`},
		{"get Task inventory details", "/task/t1", `{
			"id":"t1","name":"First task",
			"status":{"status":"open","type":"open"},
			"priority":{"priority":"high"},
			"creator":{"id":7,"username":"Tester","email":"tester@example.com"},
			"assignees":[{"id":7,"username":"Tester","email":"tester@example.com"}],
			"tags":[{"name":"alpha"}],
			"custom_fields":[{"id":"f1","name":"Workstream","type":"drop_down","value":"o1"}],
			"attachments":[{"id":"a1","title":"file.txt","mimetype":"text/plain","size":20,"url":"https://files.example/a1","url_w_query":"https://files.example/a1?signature=private"}],
			"dependencies":[{"task_id":"t1","depends_on":"t2"}]
		}`},
		{"get Task inventory details", "/task/t2", `{"id":"t2","name":"Second task","creator":{"id":7,"username":"Tester","email":"tester@example.com"}}`},
		{"get Task inventory details", "/task/t3", `{"id":"t3","name":"Nested record","creator":{"id":7,"username":"Tester","email":"tester@example.com"}}`},
		{"list Task comments", "/task/t1/comment", `{"comments":[{"id":"c1","date":"1700000000000","user":{"id":7,"username":"Tester","email":"tester@example.com"},"comment_text":"hello","comment":[{"text":"hello"}]}]}`},
		{"list threaded comment replies", "/comment/c1/reply", `{"comments":[{"id":"c2","date":"1700000001000","user":{"id":7,"username":"Tester","email":"tester@example.com"},"comment_text":"reply"}]}`},
	}
}

func fixtureExtraction() connector.ExtractionResult {
	return connector.ExtractionResult{
		ScanResult: connector.ScanResult{
			Workspace: connector.Workspace{ID: "w1", Name: "Fixture Workspace"},
			Inventory: connector.Inventory{
				Spaces: 1, Folders: 0, Lists: 1, Tasks: 2, Subtasks: 1,
				Comments: 2, Attachments: 1, CustomFields: 1, Relationships: 1,
			},
			Capabilities: []connector.Capability{
				{Name: "Tasks/subtasks", State: connector.CapabilitySupported, Note: "fixture"},
				{Name: "Task comments", State: connector.CapabilitySupported, Note: "fixture"},
				{Name: "Task attachments", State: connector.CapabilitySupported, Note: "fixture"},
				{Name: "Custom fields", State: connector.CapabilitySupported, Note: "fixture"},
				{Name: "Task relationships", State: connector.CapabilityPartial, Note: "dependencies only"},
			},
		},
		SourceObjects: []connector.SourceObject{
			{Type: "workspace", ID: "w1"},
			{Type: "space", ID: "s1", ParentType: "workspace", ParentID: "w1"},
			{Type: "list", ID: "l1", ParentType: "space", ParentID: "s1"},
			{Type: "task", ID: "t1", ParentType: "list", ParentID: "l1"},
			{Type: "task", ID: "t2", ParentType: "list", ParentID: "l1"},
			{Type: "subtask", ID: "t3", ParentType: "task", ParentID: "t1"},
			{Type: "comment", ID: "c1", ParentType: "task", ParentID: "t1"},
			{Type: "comment", ID: "c2", ParentType: "comment", ParentID: "c1"},
			{Type: "custom_field", ID: "f1"},
			{Type: "attachment", ID: "a1", ParentType: "task", ParentID: "t1"},
			{Type: "relationship.task_dependency", ID: "t1->t2", ParentType: "task", ParentID: "t1", Composite: true},
		},
	}
}

// buildFixtureArchive produces a complete M4 archive from a complete M3
// snapshot so that verification is exercised against a real archive rather
// than a hand-written approximation of one.
func buildFixtureArchive(t *testing.T, attachmentStatus int) string {
	t.Helper()
	root := t.TempDir()
	snapshotPath := filepath.Join(root, "m3")
	session, err := snapshot.Begin(snapshotPath, "clickup", connector.Workspace{ID: "w1", Name: "Fixture Workspace"}, connector.Identity{ID: "u1", Name: "Fixture User"})
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range fixtureResponses() {
		if err := session.RecordResponse(connector.RawResponse{
			Operation: response.operation, Method: http.MethodGet, Path: response.path,
			Query: url.Values{}, Attempt: 1, StatusCode: http.StatusOK, Body: []byte(response.body),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := session.Complete(fixtureExtraction()); err != nil {
		t.Fatal(err)
	}
	evidence, err := snapshot.LoadComplete(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	portable, err := clickup.NormalizeSnapshot(evidence)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: fixtureRoundTrip(func(request *http.Request) (*http.Response, error) {
		body := attachmentContent
		if attachmentStatus != http.StatusOK {
			body = nil
		}
		return &http.Response{
			StatusCode: attachmentStatus, Header: make(http.Header),
			Body: io.NopCloser(bytes.NewReader(body)), Request: request,
		}, nil
	})}
	target := filepath.Join(root, "archive")
	summary, err := archive.Build(context.Background(), evidence, portable, target, archive.Options{
		HTTPClient: client,
		Sleep:      func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("build fixture archive: %v", err)
	}
	if attachmentStatus == http.StatusOK && summary.Status != archive.StatusCreatedUnverified {
		t.Fatalf("fixture archive status = %s", summary.Status)
	}
	return target
}

// corruptCopy copies the fixture archive and applies a deliberate corruption
// to the copy, leaving the original archive untouched.
func corruptCopy(t *testing.T, source string, corrupt func(t *testing.T, archivePath string)) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "corrupt-archive")
	copyTree(t, source, target)
	corrupt(t, target)
	return target
}

func copyTree(t *testing.T, source, target string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, content, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// treeDigest fingerprints every file in the archive so a test can prove that
// verification did not write to the archive it verified.
func treeDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	digests := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		digests[filepath.ToSlash(relative)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return digests
}

func readManifest(t *testing.T, archivePath string) archive.Manifest {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(archivePath, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest archive.Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writeManifest(t *testing.T, archivePath string, manifest archive.Manifest) {
	t.Helper()
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archivePath, "manifest.json"), append(content, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// refreshManifestChecksum re-records a file the test has deliberately changed,
// which models an attacker who also updates the manifest. It lets a test prove
// that a deeper check, not only the recorded checksum, catches the tampering.
func refreshManifestChecksum(t *testing.T, archivePath, relative string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(archivePath, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	manifest := readManifest(t, archivePath)
	found := false
	for index := range manifest.Files {
		if manifest.Files[index].Path == relative {
			manifest.Files[index].Bytes = int64(len(content))
			manifest.Files[index].Checksum = "sha256:" + hex.EncodeToString(sum[:])
			found = true
		}
	}
	if !found {
		t.Fatalf("manifest does not record %s", relative)
	}
	writeManifest(t, archivePath, manifest)
}

// mutateDatabase edits alih.db in place, then re-records it in the manifest so
// that the file checksum check still passes and the semantic checks are the
// only thing standing between the tampering and a verified result.
func mutateDatabase(t *testing.T, archivePath string, statements ...string) {
	t.Helper()
	path := filepath.Join(archivePath, "alih.db")
	database, err := sql.Open("sqlite3", (&url.URL{Scheme: "file", Path: path}).String())
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			_ = database.Close()
			t.Fatalf("mutate archive database: %v", err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	refreshManifestChecksum(t, archivePath, "alih.db")
}

func verifyFixture(t *testing.T, archivePath string) Report {
	t.Helper()
	report, err := Archive(archivePath, Options{FieldSemantics: clickup.FieldSemantics{}})
	if err != nil {
		t.Fatalf("Archive() error = %v", err)
	}
	return report
}

func checkStatus(t *testing.T, report Report, name string) Check {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("report contains no check named %q", name)
	return Check{}
}
