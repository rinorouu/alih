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

package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"alih/internal/connector"
	"alih/internal/model"
	"alih/internal/snapshot"
)

type archiveRoundTripFunc func(*http.Request) (*http.Response, error)

func (function archiveRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func attachmentHTTPClient(status int, body []byte, inspect func(*http.Request)) *http.Client {
	return &http.Client{Transport: archiveRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if inspect != nil {
			inspect(request)
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    request,
		}, nil
	})}
}

func archiveFixture(t *testing.T, downloadURL string, expectedSize int64) (snapshot.Evidence, model.Archive) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "evidence.json"), []byte(`{"source":"unchanged"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceSource := connector.Workspace{ID: "w1", Name: "Workspace"}
	evidence := snapshot.Evidence{
		RootPath: root, Connector: "clickup", Workspace: workspaceSource,
		FinishedAt: time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC), LogicalDigest: "sha256:logical",
		Inventory:    connector.Inventory{Spaces: 1, Lists: 1, Tasks: 1, Attachments: 1},
		Capabilities: []connector.Capability{{Name: "Task attachments", State: connector.CapabilitySupported, Note: "fixture"}},
	}
	workspaceID := model.PortableID("clickup", "workspace", "w1")
	spaceID := model.PortableID("clickup", "space", "s1")
	listID := model.PortableID("clickup", "list", "l1")
	taskID := model.PortableID("clickup", "task", "t1")
	attachmentID := model.PortableID("clickup", "attachment", "a1")
	name, title, filename, mediaType, sourceURL := "Workspace", "Task", "file.txt", "text/plain", "https://attachments.example/file.txt"
	portable := model.Archive{
		Connector:   "clickup",
		Workspace:   model.Workspace{ID: workspaceID, Name: &name, Source: model.SourceRef{Provider: "clickup", Type: "workspace", ID: "w1", RawPath: "raw/inventory.json"}},
		Containers:  []model.Container{{ID: spaceID, Kind: "space", WorkspaceID: workspaceID, Source: model.SourceRef{Provider: "clickup", Type: "space", ID: "s1", RawPath: "raw/evidence.json"}}},
		Collections: []model.Collection{{ID: listID, WorkspaceID: workspaceID, ContainerID: spaceID, Source: model.SourceRef{Provider: "clickup", Type: "list", ID: "l1", RawPath: "raw/evidence.json"}}},
		Records:     []model.Record{{ID: taskID, Kind: "task", WorkspaceID: workspaceID, CollectionID: listID, Title: &title, Source: model.SourceRef{Provider: "clickup", Type: "task", ID: "t1", RawPath: "raw/evidence.json"}}},
		Attachments: []model.Attachment{{
			ID: attachmentID, WorkspaceID: workspaceID, RecordID: taskID, Filename: &filename, MediaType: &mediaType,
			ExpectedSize: &expectedSize, SourceURL: &sourceURL, DownloadURL: downloadURL, DownloadStatus: "PENDING",
			Source: model.SourceRef{Provider: "clickup", Type: "attachment", ID: "a1", RawPath: "raw/evidence.json"},
		}},
		Capabilities: evidence.Capabilities,
		Limitations:  []string{"fixture limitation"},
	}
	return evidence, portable
}

func TestBuildCreatesDeterministicSQLiteManifestSchemaRawAndAttachment(t *testing.T) {
	t.Parallel()

	content := []byte("portable attachment\n")
	client := attachmentHTTPClient(http.StatusOK, content, func(request *http.Request) {
		if request.Header.Get("Authorization") != "" {
			t.Error("credential was sent to a non-API attachment host")
		}
	})
	evidence, portable := archiveFixture(t, "https://attachments.example/download?signature=private", int64(len(content)))

	buildTimes := []time.Time{
		time.Date(2026, 8, 30, 2, 32, 15, 0, time.UTC),
		time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC),
	}
	var outputs []string
	for run := 0; run < 2; run++ {
		buildTime := buildTimes[run]
		target := filepath.Join(t.TempDir(), "alih-export")
		summary, err := Build(context.Background(), evidence, portable, target, Options{
			HTTPClient: client, Credential: "clickup-token",
			Now: func() time.Time { return buildTime },
		})
		if err != nil {
			t.Fatal(err)
		}
		if summary.Status != StatusCreatedUnverified {
			t.Fatalf("status = %s", summary.Status)
		}
		for _, path := range []string{"alih.db", "manifest.json", "schema.json", filepath.Join("raw", "evidence.json"), filepath.Join("attachments", model.PortableID("clickup", "attachment", "a1")+".txt")} {
			if _, err := os.Stat(filepath.Join(target, path)); err != nil {
				t.Errorf("missing %s: %v", path, err)
			}
		}
		if info, err := os.Stat(filepath.Join(target, "alih.db")); err != nil {
			t.Fatal(err)
		} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("alih.db permissions = %o, want 600", info.Mode().Perm())
		}
		stored, err := os.ReadFile(filepath.Join(target, "raw", "evidence.json"))
		if err != nil {
			t.Fatal(err)
		}
		original, _ := os.ReadFile(filepath.Join(evidence.RootPath, "evidence.json"))
		if !bytes.Equal(stored, original) {
			t.Fatal("raw evidence was modified")
		}
		var manifest Manifest
		readJSON(t, filepath.Join(target, "manifest.json"), &manifest)
		if manifest.Status != StatusCreatedUnverified || manifest.Verification.Status != "NOT_RUN" || manifest.Inventory["attachments"].Archived != 1 || len(manifest.Discrepancies) != 0 {
			t.Fatalf("manifest = %#v", manifest)
		}
		if manifest.SchemaVersion != ArchiveSchemaVersion {
			t.Fatalf("manifest schema_version = %d, want %d", manifest.SchemaVersion, ArchiveSchemaVersion)
		}
		// The two instants describe different events and must not be conflated.
		if !manifest.SourceSnapshotCompletedAt.Equal(evidence.FinishedAt) {
			t.Errorf("source_snapshot_completed_at = %s, want the M3 finish time %s", manifest.SourceSnapshotCompletedAt, evidence.FinishedAt)
		}
		if manifest.ArchiveCompletedAt == nil || !manifest.ArchiveCompletedAt.Equal(buildTime) {
			t.Fatalf("archive_completed_at = %v, want the observed build instant %s", manifest.ArchiveCompletedAt, buildTime)
		}
		if manifest.ArchiveCompletedAt.Equal(manifest.SourceSnapshotCompletedAt) {
			t.Error("the archive completion time was taken from the source snapshot instead of being observed")
		}
		// It must be an observed instant, never a filesystem timestamp.
		info, err := os.Stat(filepath.Join(target, "alih.db"))
		if err != nil {
			t.Fatal(err)
		}
		if manifest.ArchiveCompletedAt.Equal(info.ModTime().UTC()) {
			t.Error("the archive completion time was derived from filesystem mtime")
		}
		database, err := sql.Open("sqlite3", filepath.Join(target, "alih.db"))
		if err != nil {
			t.Fatal(err)
		}
		var sourceID, status, checksum string
		if err := database.QueryRow(`SELECT source_id,download_status,checksum FROM attachments`).Scan(&sourceID, &status, &checksum); err != nil {
			t.Fatal(err)
		}
		_ = database.Close()
		if sourceID != "a1" || status != "RETRIEVED" || checksum == "" {
			t.Fatalf("attachment row = %q %q %q", sourceID, status, checksum)
		}
		outputs = append(outputs, target)
	}
	for _, name := range []string{"alih.db", "schema.json"} {
		first, _ := os.ReadFile(filepath.Join(outputs[0], name))
		second, _ := os.ReadFile(filepath.Join(outputs[1], name))
		if !bytes.Equal(first, second) {
			t.Errorf("%s differs for identical source evidence", name)
		}
	}
	// manifest.json must still be reproducible apart from the single field that
	// records when the archive was built. Neutralising only that field proves it
	// is the sole source of variation, including the file checksums it feeds.
	firstManifest, _ := os.ReadFile(filepath.Join(outputs[0], "manifest.json"))
	secondManifest, _ := os.ReadFile(filepath.Join(outputs[1], "manifest.json"))
	if bytes.Equal(firstManifest, secondManifest) {
		t.Fatal("two builds recorded the same archive completion time")
	}
	if !bytes.Equal(withoutCompletionTime(t, firstManifest), withoutCompletionTime(t, secondManifest)) {
		t.Error("manifest.json differs for identical source evidence beyond the archive completion time")
	}
}

// withoutCompletionTime removes the one field that legitimately varies between
// builds so the rest of the manifest can be compared byte for byte.
func withoutCompletionTime(t *testing.T, content []byte) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "archive_completed_at")
	normalized, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func TestBuildMarksAttachmentFailureIncompleteWithoutSilentOmission(t *testing.T) {
	t.Parallel()

	client := attachmentHTTPClient(http.StatusServiceUnavailable, nil, nil)
	evidence, portable := archiveFixture(t, "https://attachments.example/unavailable", 10)
	target := filepath.Join(t.TempDir(), "alih-export")
	summary, err := Build(context.Background(), evidence, portable, target, Options{
		HTTPClient: client, Credential: "token",
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != StatusIncomplete || summary.Inventory["attachments"].Unresolved != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	var manifest Manifest
	readJSON(t, filepath.Join(target, "manifest.json"), &manifest)
	if manifest.Status != StatusIncomplete || len(manifest.Discrepancies) != 1 || manifest.Attachments[0].Status != "UNRESOLVED" || manifest.Attachments[0].Error == nil {
		t.Fatalf("manifest = %#v", manifest)
	}
	entries, err := os.ReadDir(filepath.Join(target, "attachments"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed attachment left files: %#v", entries)
	}
	database, err := sql.Open("sqlite3", filepath.Join(target, "alih.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var status string
	var localPath any
	if err := database.QueryRow(`SELECT download_status,local_path FROM attachments`).Scan(&status, &localPath); err != nil {
		t.Fatal(err)
	}
	if status != "UNRESOLVED" || localPath != nil {
		t.Fatalf("database attachment status=%q local_path=%v", status, localPath)
	}
}

func TestBuildFailsClosedOnBrokenRelationship(t *testing.T) {
	t.Parallel()

	evidence, portable := archiveFixture(t, "https://invalid.example/file", 1)
	evidence.Inventory.Attachments = 0
	portable.Attachments = nil
	evidence.Inventory.Relationships = 1
	missing := model.PortableID("clickup", "task", "missing")
	portable.Relationships = []model.Relationship{{
		ID: model.PortableID("clickup", "relationship.task_link", "t1<->missing"), Kind: "task_link",
		FromRecordID: &portable.Records[0].ID, ToRecordID: &missing, FromSourceID: "t1", ToSourceID: "missing",
		ResolutionState: "RESOLVED", SourceMetadataJSON: json.RawMessage(`{}`),
		Source: model.SourceRef{Provider: "clickup", Type: "relationship.task_link", ID: "t1<->missing", RawPath: "raw/evidence.json", IDComposite: true},
	}}
	target := filepath.Join(t.TempDir(), "alih-export")
	summary, err := Build(context.Background(), evidence, portable, target, Options{Credential: "token"})
	if err == nil {
		t.Fatal("Build() accepted a broken resolved relationship")
	}
	if summary.Status != StatusFailed || summary.Path != target+".failed" {
		t.Fatalf("summary = %#v", summary)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean target exists: %v", err)
	}
	var manifest Manifest
	readJSON(t, filepath.Join(summary.Path, "manifest.json"), &manifest)
	if manifest.Status != StatusFailed || manifest.Verification.Status != "NOT_RUN" {
		t.Fatalf("failed manifest = %#v", manifest)
	}
	if manifest.ArchiveCompletedAt != nil {
		t.Errorf("an archive that was never completed recorded a completion time: %s", manifest.ArchiveCompletedAt)
	}
	if manifest.SourceSnapshotCompletedAt.IsZero() {
		t.Error("a failed archive lost the source snapshot instant it was built from")
	}
}

func TestBuildRefusesCredentialInRawSnapshot(t *testing.T) {
	t.Parallel()

	const token = "credential-must-not-enter-archive"
	evidence, portable := archiveFixture(t, "https://invalid.example/file", 1)
	evidence.Inventory.Attachments = 0
	portable.Attachments = nil
	if err := os.WriteFile(filepath.Join(evidence.RootPath, "credential.txt"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "alih-export")
	if _, err := Build(context.Background(), evidence, portable, target, Options{Credential: token}); err == nil {
		t.Fatal("Build() accepted raw evidence containing the credential")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target exists after credential refusal: %v", err)
	}
}

func TestBuildRefusesOutputInsideRawSnapshot(t *testing.T) {
	t.Parallel()

	evidence, portable := archiveFixture(t, "https://invalid.example/file", 1)
	evidence.Inventory.Attachments = 0
	portable.Attachments = nil
	target := filepath.Join(evidence.RootPath, "alih-export")
	if _, err := Build(context.Background(), evidence, portable, target, Options{}); err == nil {
		t.Fatal("Build() accepted an output path inside the raw snapshot")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target exists after path refusal: %v", err)
	}
}

func readJSON(t *testing.T, path string, destination any) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, destination); err != nil {
		t.Fatal(err)
	}
}
