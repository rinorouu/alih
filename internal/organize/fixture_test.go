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

package organize

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/connector/clickup"
	"alih/internal/snapshot"
	"alih/internal/sqliteutil"
	"alih/internal/verify"
)

var fixtureAttachmentContent = []byte("portable attachment\n")

type fixtureRoundTrip func(*http.Request) (*http.Response, error)

func (function fixtureRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

// fixtureVerifier is the same independent verifier the CLI wires in, kept local
// so the organize package does not import an application service.
type fixtureVerifier struct{ calls int }

func (v *fixtureVerifier) Verify(path string) (verify.Report, error) {
	v.calls++
	return verify.Archive(path, verify.Options{FieldSemantics: clickup.FieldSemantics{}})
}

// stubVerifier returns a fixed answer so refusal paths can be exercised without
// having to corrupt an archive in a specific way.
type stubVerifier struct {
	report verify.Report
	err    error
}

func (v stubVerifier) Verify(string) (verify.Report, error) { return v.report, v.err }

type fixtureResponse struct{ operation, path, body string }

// fixtureResponses mirrors the minimal realistic ClickUp traversal the verifier
// fixture uses: one Space, one List, two Tasks, one nested Task, one comment
// with a threaded reply, one enumerated Custom Field with an observed value,
// one attachment and one dependency relationship.
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
				Containers: 1, Collections: 1, Records: 3, NestedRecords: 1,
				Comments: 2, Attachments: 1, CustomFields: 1, Relationships: 1,
				ContainerKinds: map[string]int{"space": 1},
				RecordKinds:    map[string]int{"task": 2, "subtask": 1},
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

// buildFixtureArchive produces a real sealed archive inside a temporary root
// without contacting any network service.
func buildFixtureArchive(t *testing.T, attachmentStatus int) string {
	t.Helper()
	root := t.TempDir()
	snapshotPath := filepath.Join(root, "m3")
	session, err := snapshot.Begin(snapshotPath, "clickup",
		connector.Workspace{ID: "w1", Name: "Fixture Workspace"},
		connector.Identity{ID: "u1", Name: "Fixture User"})
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
		body := fixtureAttachmentContent
		if attachmentStatus != http.StatusOK {
			body = nil
		}
		return &http.Response{
			StatusCode: attachmentStatus, Header: make(http.Header),
			Body: io.NopCloser(bytes.NewReader(body)), Request: request,
		}, nil
	})}
	target := filepath.Join(root, "archive")
	if _, err := archive.Build(context.Background(), evidence, portable, target, archive.Options{
		HTTPClient: client,
		Sleep:      func(context.Context, time.Duration) error { return nil },
	}); err != nil {
		t.Fatalf("build fixture archive: %v", err)
	}
	return target
}

// organizeFixture builds a view from archivePath into a fresh output path.
func organizeFixture(t *testing.T, archivePath string) (Result, string) {
	t.Helper()
	output := filepath.Join(t.TempDir(), "view")
	result, err := Build(context.Background(), archivePath, output, Options{
		Verifier: &fixtureVerifier{}, AlihVersion: "test-version",
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	return result, output
}

// readTree returns every regular file below root keyed by its slash-separated
// relative path, so two views can be compared byte for byte.
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
		tree[filepath.ToSlash(relative)] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func sortedKeys(tree map[string]string) []string {
	keys := make([]string, 0, len(tree))
	for key := range tree {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// treeDigest fingerprints an archive so a test can prove organization left the
// canonical evidence byte for byte unchanged.
func treeDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	digests := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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

func copyTree(t *testing.T, source, target string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
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

// mutateDatabase edits alih.db in a copied archive and re-records the file in
// the manifest, so the file-checksum check still passes and only the semantic
// checks stand between the change and a verified result.
func mutateDatabase(t *testing.T, archivePath string, statements ...string) {
	t.Helper()
	path := filepath.Join(archivePath, "alih.db")
	database, err := sql.Open("sqlite3", sqliteutil.FileURI(path))
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

func refreshManifestChecksum(t *testing.T, archivePath, relative string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(archivePath, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	manifestPath := filepath.Join(archivePath, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest archive.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
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
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// mutatedCopy copies the fixture archive and edits the copy, leaving the
// original archive untouched for comparison.
func mutatedCopy(t *testing.T, source string, statements ...string) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "mutated-archive")
	copyTree(t, source, target)
	mutateDatabase(t, target, statements...)
	return target
}

// sqlText quotes a value for a test-only SQL statement.
func sqlText(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// recordDirectories returns the record directories of a generated view.
func recordDirectories(t *testing.T, tree map[string]string) []string {
	t.Helper()
	var directories []string
	for _, path := range sortedKeys(tree) {
		if strings.HasSuffix(path, "/record.md") {
			directories = append(directories, strings.TrimSuffix(path, "/record.md"))
		}
	}
	return directories
}
