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
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"alih/internal/connector"
	"alih/internal/model"
	"alih/internal/snapshot"
)

var hardeningBuildTime = time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC)

func noSleep(context.Context, time.Duration) error { return nil }

func fixedClock() func() time.Time { return func() time.Time { return hardeningBuildTime } }

// multiAttachmentFixture builds evidence and a portable model with several
// attachments so that a partial failure can be distinguished from a total one.
func multiAttachmentFixture(t *testing.T, urls []string, sizes []int64) (snapshot.Evidence, model.Archive) {
	t.Helper()
	evidence, portable := archiveFixture(t, "https://unused.example/file", 1)
	evidence.Inventory.Attachments = len(urls)
	workspaceID := portable.Workspace.ID
	recordID := portable.Records[0].ID

	attachments := make([]model.Attachment, 0, len(urls))
	for index, downloadURL := range urls {
		name := "file" + string(rune('a'+index)) + ".txt"
		mediaType := "text/plain"
		sourceID := "a" + string(rune('a'+index))
		size := sizes[index]
		attachments = append(attachments, model.Attachment{
			ID: model.PortableID("clickup", "attachment", sourceID), WorkspaceID: workspaceID,
			RecordID: recordID, Filename: &name, MediaType: &mediaType, ExpectedSize: &size,
			DownloadURL: downloadURL, DownloadStatus: "PENDING",
			Source: model.SourceRef{Provider: "clickup", Type: "attachment", ID: sourceID, RawPath: "raw/evidence.json"},
		})
	}
	sort.Slice(attachments, func(i, j int) bool { return attachments[i].ID < attachments[j].ID })
	portable.Attachments = attachments
	return evidence, portable
}

// routedAttachmentClient answers each attachment URL differently so one build
// can exercise success and several distinct failures at once.
func routedAttachmentClient(routes map[string]func(*http.Request) (*http.Response, error)) *http.Client {
	return &http.Client{Transport: archiveRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if route, found := routes[request.URL.String()]; found {
			return route(request)
		}
		return &http.Response{
			StatusCode: http.StatusNotFound, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader("")), Request: request,
		}, nil
	})}
}

func okResponse(body []byte) func(*http.Request) (*http.Response, error) {
	return func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(bytes.NewReader(body)), Request: request,
		}, nil
	}
}

// TestPartialAttachmentFailureIsAccountedPerAttachment covers PRD section 21's
// attachment failure category: a build where some binaries arrive and others do
// not must archive the good ones, name every bad one, and stay INCOMPLETE.
func TestPartialAttachmentFailureIsAccountedPerAttachment(t *testing.T) {
	t.Parallel()

	good := []byte("archived bytes\n")
	short := []byte("too short")
	client := routedAttachmentClient(map[string]func(*http.Request) (*http.Response, error){
		"https://files.example/ok": okResponse(good),
		"https://files.example/gone": func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		},
		"https://files.example/short":   okResponse(short),
		"https://files.example/network": func(*http.Request) (*http.Response, error) { return nil, errors.New("connection reset") },
	})
	evidence, portable := multiAttachmentFixture(t,
		[]string{"https://files.example/ok", "https://files.example/gone", "https://files.example/short", "https://files.example/network"},
		[]int64{int64(len(good)), 10, int64(len(good)), 10})

	target := filepath.Join(t.TempDir(), "archive")
	summary, err := Build(context.Background(), evidence, portable, target, Options{
		HTTPClient: client, Sleep: noSleep, Now: fixedClock(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != StatusIncomplete {
		t.Fatalf("status = %s, want %s", summary.Status, StatusIncomplete)
	}
	if summary.Inventory["attachments"].Archived != 1 || summary.Inventory["attachments"].Unresolved != 3 {
		t.Fatalf("attachment inventory = %#v", summary.Inventory["attachments"])
	}

	var manifest Manifest
	readJSON(t, filepath.Join(target, "manifest.json"), &manifest)
	retrieved, unresolved := 0, 0
	for _, attachment := range manifest.Attachments {
		switch attachment.Status {
		case "RETRIEVED":
			retrieved++
			if attachment.Checksum == nil || attachment.LocalPath == nil || attachment.Error != nil {
				t.Errorf("retrieved attachment is missing its evidence: %#v", attachment)
			}
		case "UNRESOLVED":
			unresolved++
			if attachment.Error == nil || *attachment.Error == "" {
				t.Errorf("unresolved attachment %q carries no reason", attachment.SourceID)
			}
			if attachment.LocalPath != nil || attachment.Checksum != nil {
				t.Errorf("unresolved attachment claims binary evidence: %#v", attachment)
			}
		default:
			t.Errorf("unknown attachment status %q", attachment.Status)
		}
	}
	if retrieved != 1 || unresolved != 3 {
		t.Fatalf("retrieved=%d unresolved=%d, want 1 and 3", retrieved, unresolved)
	}
	// Every unresolved attachment must also be disclosed as a discrepancy.
	if len(manifest.Discrepancies) != 3 {
		t.Fatalf("discrepancies = %d, want 3", len(manifest.Discrepancies))
	}
	// Only the one good binary may exist on disk.
	entries, err := os.ReadDir(filepath.Join(target, "attachments"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("attachments directory holds %d files, want 1", len(entries))
	}
}

func TestAttachmentSizeMismatchIsRefusedRatherThanArchived(t *testing.T) {
	t.Parallel()

	body := []byte("nine byte")
	client := routedAttachmentClient(map[string]func(*http.Request) (*http.Response, error){
		"https://files.example/short": okResponse(body),
	})
	// The source promised more bytes than arrived.
	evidence, portable := multiAttachmentFixture(t, []string{"https://files.example/short"}, []int64{4096})
	target := filepath.Join(t.TempDir(), "archive")
	summary, err := Build(context.Background(), evidence, portable, target, Options{
		HTTPClient: client, Sleep: noSleep, Now: fixedClock(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != StatusIncomplete {
		t.Fatalf("a truncated attachment produced status %s", summary.Status)
	}
	var manifest Manifest
	readJSON(t, filepath.Join(target, "manifest.json"), &manifest)
	if manifest.Attachments[0].Status != "UNRESOLVED" ||
		!strings.Contains(*manifest.Attachments[0].Error, "size mismatch") {
		t.Fatalf("attachment = %#v", manifest.Attachments[0])
	}
	entries, _ := os.ReadDir(filepath.Join(target, "attachments"))
	if len(entries) != 0 {
		t.Fatalf("a size-mismatched download was left in the archive: %#v", entries)
	}
}

func TestAttachmentURLsThatCannotBeTrustedAreNeverRequested(t *testing.T) {
	t.Parallel()

	requested := 0
	client := &http.Client{Transport: archiveRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requested++
		return okResponse([]byte("x"))(request)
	})}
	for _, downloadURL := range []string{"http://files.example/insecure", "ftp://files.example/x", "", "://broken"} {
		downloadURL := downloadURL
		t.Run(downloadURL, func(t *testing.T) {
			evidence, portable := multiAttachmentFixture(t, []string{downloadURL}, []int64{1})
			target := filepath.Join(t.TempDir(), "archive")
			summary, err := Build(context.Background(), evidence, portable, target, Options{
				HTTPClient: client, Sleep: noSleep, Now: fixedClock(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if summary.Status != StatusIncomplete {
				t.Fatalf("status = %s, want INCOMPLETE", summary.Status)
			}
		})
	}
	if requested != 0 {
		t.Fatalf("%d requests were made for URLs that are not usable HTTPS", requested)
	}
}

func TestAttachmentRedirectDropsTheCredential(t *testing.T) {
	t.Parallel()

	const credential = "clickup-personal-token"
	var sawAuthorizationOffSite bool
	client := &http.Client{Transport: archiveRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "api.clickup.com" {
			response := &http.Response{
				StatusCode: http.StatusFound, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader("")), Request: request,
			}
			response.Header.Set("Location", "https://cdn.elsewhere.example/blob")
			return response, nil
		}
		if request.Header.Get("Authorization") != "" {
			sawAuthorizationOffSite = true
		}
		return okResponse([]byte("redirected bytes\n"))(request)
	})}
	body := []byte("redirected bytes\n")
	evidence, portable := multiAttachmentFixture(t, []string{"https://api.clickup.com/attachment/a"}, []int64{int64(len(body))})

	target := filepath.Join(t.TempDir(), "archive")
	summary, err := Build(context.Background(), evidence, portable, target, Options{
		HTTPClient: client, Credential: credential, Sleep: noSleep, Now: fixedClock(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if sawAuthorizationOffSite {
		t.Fatal("the credential was forwarded to a host that is not the source API")
	}
	if summary.Status != StatusCreatedUnverified {
		t.Fatalf("status = %s", summary.Status)
	}
}

func TestAttachmentContainingTheCredentialIsRefused(t *testing.T) {
	t.Parallel()

	const credential = "clickup-personal-token"
	body := []byte("leading bytes " + credential + " trailing bytes")
	client := routedAttachmentClient(map[string]func(*http.Request) (*http.Response, error){
		"https://api.clickup.com/attachment/a": okResponse(body),
	})
	evidence, portable := multiAttachmentFixture(t, []string{"https://api.clickup.com/attachment/a"}, []int64{int64(len(body))})

	target := filepath.Join(t.TempDir(), "archive")
	summary, err := Build(context.Background(), evidence, portable, target, Options{
		HTTPClient: client, Credential: credential, Sleep: noSleep, Now: fixedClock(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != StatusIncomplete {
		t.Fatalf("status = %s, want INCOMPLETE", summary.Status)
	}
	// The refusal reason itself must not leak the credential.
	var manifest Manifest
	readJSON(t, filepath.Join(target, "manifest.json"), &manifest)
	content, err := os.ReadFile(filepath.Join(target, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte(credential)) {
		t.Fatal("the manifest recorded the credential while refusing it")
	}
	entries, _ := os.ReadDir(filepath.Join(target, "attachments"))
	if len(entries) != 0 {
		t.Fatal("a body containing the credential was archived")
	}
}

func TestBuildRefusesTargetThatAlreadyExists(t *testing.T) {
	t.Parallel()

	evidence, portable := archiveFixture(t, "https://unused.example/file", 1)
	evidence.Inventory.Attachments = 0
	portable.Attachments = nil
	target := filepath.Join(t.TempDir(), "archive")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), evidence, portable, target, Options{Now: fixedClock()}); err == nil {
		t.Fatal("Build() overwrote an existing archive path")
	}
}

// TestBuildFailsClosedWhenTheFilesystemRefusesWrites covers the filesystem
// failure path: no clean archive may appear when the archive cannot be written.
func TestBuildFailsClosedWhenTheFilesystemRefusesWrites(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("chmod does not deny writes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not deny writes")
	}
	evidence, portable := archiveFixture(t, "https://unused.example/file", 1)
	evidence.Inventory.Attachments = 0
	portable.Attachments = nil

	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0o700)

	target := filepath.Join(parent, "archive")
	summary, err := Build(context.Background(), evidence, portable, target, Options{Now: fixedClock()})
	if err == nil {
		t.Fatal("Build() reported success on an unwritable filesystem")
	}
	if summary.Status == StatusCreatedUnverified {
		t.Fatalf("a failed build reported %s", summary.Status)
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("a clean archive path exists after a failed build")
	}
}

func TestBuildRefusesRawEvidenceContainingSymlinks(t *testing.T) {
	t.Parallel()

	evidence, portable := archiveFixture(t, "https://unused.example/file", 1)
	evidence.Inventory.Attachments = 0
	portable.Attachments = nil
	if err := os.Symlink("/etc/hostname", filepath.Join(evidence.RootPath, "link.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	target := filepath.Join(t.TempDir(), "archive")
	if _, err := Build(context.Background(), evidence, portable, target, Options{Now: fixedClock()}); err == nil {
		t.Fatal("Build() copied a raw snapshot containing a symlink")
	}
}

// TestArchiveIsDeterministicRegardlessOfAttachmentOrdering proves the archive
// content depends on the evidence, not on the order the writer happened to see
// it in.
func TestArchiveIsDeterministicRegardlessOfAttachmentOrdering(t *testing.T) {
	t.Parallel()

	body := []byte("shared bytes\n")
	client := routedAttachmentClient(map[string]func(*http.Request) (*http.Response, error){
		"https://files.example/one": okResponse(body),
		"https://files.example/two": okResponse(body),
	})
	digests := make([]string, 0, 2)
	manifests := make([]string, 0, 2)
	for run := 0; run < 2; run++ {
		evidence, portable := multiAttachmentFixture(t,
			[]string{"https://files.example/one", "https://files.example/two"},
			[]int64{int64(len(body)), int64(len(body))})
		if run == 1 {
			// Present the same attachments to the writer in the opposite order.
			portable.Attachments[0], portable.Attachments[1] = portable.Attachments[1], portable.Attachments[0]
		}
		target := filepath.Join(t.TempDir(), "archive")
		if _, err := Build(context.Background(), evidence, portable, target, Options{
			HTTPClient: client, Sleep: noSleep, Now: fixedClock(),
		}); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filepath.Join(target, "alih.db"))
		if err != nil {
			t.Fatal(err)
		}
		digests = append(digests, string(content))
		var manifest Manifest
		readJSON(t, filepath.Join(target, "manifest.json"), &manifest)
		if len(manifest.Attachments) != 2 || manifest.Attachments[0].ID > manifest.Attachments[1].ID {
			t.Fatalf("manifest attachments are not in a stable order: %#v", manifest.Attachments)
		}
		content, err = os.ReadFile(filepath.Join(target, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		manifests = append(manifests, string(content))
	}
	if digests[0] != digests[1] {
		t.Fatal("attachment ordering changed the archived database")
	}
	if manifests[0] != manifests[1] {
		t.Fatal("attachment ordering changed the manifest, so the writer depends on its caller for determinism")
	}
}

func TestBuildRejectsPortableModelThatContradictsTheSnapshot(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		damage func(*snapshot.Evidence, *model.Archive)
	}{
		{"record count disagrees", func(e *snapshot.Evidence, p *model.Archive) { e.Inventory.Tasks = 99 }},
		{"collection count disagrees", func(e *snapshot.Evidence, p *model.Archive) { e.Inventory.Lists = 5 }},
		{"connector disagrees", func(e *snapshot.Evidence, p *model.Archive) { p.Connector = "other" }},
		{"workspace disagrees", func(e *snapshot.Evidence, p *model.Archive) { p.Workspace.Source.ID = "other" }},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			evidence, portable := archiveFixture(t, "https://unused.example/file", 1)
			evidence.Inventory.Attachments = 0
			portable.Attachments = nil
			testCase.damage(&evidence, &portable)
			target := filepath.Join(t.TempDir(), "archive")
			if _, err := Build(context.Background(), evidence, portable, target, Options{Now: fixedClock()}); err == nil {
				t.Fatal("Build() accepted a portable model that contradicts its own source evidence")
			}
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a clean archive was produced from contradictory evidence")
			}
		})
	}
}

var _ = connector.Workspace{}
