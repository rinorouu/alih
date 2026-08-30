package snapshot

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"alih/internal/connector"
)

func testExtractionResult(workspace connector.Workspace, reversed bool) connector.ExtractionResult {
	objects := []connector.SourceObject{
		{Type: "workspace", ID: workspace.ID},
		{Type: "space", ID: "space-1", ParentType: "workspace", ParentID: workspace.ID},
		{Type: "list", ID: "list-1", ParentType: "space", ParentID: "space-1"},
		{Type: "task", ID: "task-1", ParentType: "list", ParentID: "list-1"},
	}
	if reversed {
		for left, right := 0, len(objects)-1; left < right; left, right = left+1, right-1 {
			objects[left], objects[right] = objects[right], objects[left]
		}
	}
	return connector.ExtractionResult{
		ScanResult:    connector.ScanResult{Workspace: workspace, Inventory: connector.Inventory{Spaces: 1, Lists: 1, Tasks: 1}},
		SourceObjects: objects,
	}
}

func TestCompletePreservesExactRawResponseAndStableLogicalDigest(t *testing.T) {
	t.Parallel()

	workspace := connector.Workspace{ID: "workspace-1", Name: "Test"}
	var digests []string
	for run := 0; run < 2; run++ {
		target := filepath.Join(t.TempDir(), "raw-run")
		session, err := Begin(target, "clickup", workspace, "private-token")
		if err != nil {
			t.Fatal(err)
		}
		body := []byte("{\n  \"spaces\": [{\"id\": \"space-1\"}]\n}\n")
		if err := session.RecordResponse(connector.RawResponse{
			Operation: "list Spaces", Method: "GET", Path: "/team/workspace-1/space",
			Query: url.Values{"archived": {"false"}}, Attempt: 1, StatusCode: 200, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
		summary, err := session.Complete(testExtractionResult(workspace, run == 1))
		if err != nil {
			t.Fatal(err)
		}
		digests = append(digests, summary.LogicalDigest)
		stored, err := os.ReadFile(filepath.Join(target, "raw", "000001.json"))
		if err != nil {
			t.Fatal(err)
		}
		if string(stored) != string(body) {
			t.Fatalf("raw body changed:\n got %q\nwant %q", stored, body)
		}
		if _, err := os.Stat(filepath.Join(target, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("M3 unexpectedly created an M4 manifest: %v", err)
		}
		assertPermissions(t, target, 0o700)
		assertPermissions(t, filepath.Join(target, "raw", "000001.json"), 0o600)
		var runFile runRecord
		decodeFile(t, filepath.Join(target, "run.json"), &runFile)
		if runFile.Status != "COMPLETE" || runFile.Consistency.Atomic {
			t.Fatalf("run record = %#v", runFile)
		}
	}
	if digests[0] != digests[1] {
		t.Fatalf("equivalent logical inventories produced different digests: %q != %q", digests[0], digests[1])
	}
}

func TestBeginMarksUnfinishedStagingAsInProgress(t *testing.T) {
	t.Parallel()

	session, err := Begin(filepath.Join(t.TempDir(), "raw-run"), "clickup", connector.Workspace{ID: "w1", Name: "Test"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	var runFile runRecord
	decodeFile(t, filepath.Join(session.stagingPath, "run.json"), &runFile)
	if runFile.Status != "IN_PROGRESS" || runFile.Consistency.Atomic {
		t.Fatalf("initial run record = %#v", runFile)
	}
}

func TestLoadCompleteRejectsCorruptRawEvidence(t *testing.T) {
	t.Parallel()

	workspace := connector.Workspace{ID: "workspace-1", Name: "Test"}
	target := filepath.Join(t.TempDir(), "raw-run")
	session, err := Begin(target, "clickup", workspace, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.RecordResponse(connector.RawResponse{
		Operation: "list Spaces", Method: "GET", Path: "/team/workspace-1/space",
		Attempt: 1, StatusCode: 200, Body: []byte(`{"spaces":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Complete(testExtractionResult(workspace, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadComplete(target); err != nil {
		t.Fatalf("LoadComplete() before corruption: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "raw", "000001.json"), []byte(`{"spaces":[{"id":"changed"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadComplete(target); err == nil || !strings.Contains(err.Error(), "raw evidence") {
		t.Fatalf("LoadComplete() corruption error = %v", err)
	}
}

func TestCredentialInRawResponseIsOmittedAndFailedEvidenceIsPreserved(t *testing.T) {
	t.Parallel()

	const token = "private-token-that-must-not-be-written"
	target := filepath.Join(t.TempDir(), "raw-run")
	session, err := Begin(target, "clickup", connector.Workspace{ID: "w1", Name: "Test"}, token)
	if err != nil {
		t.Fatal(err)
	}
	err = session.RecordResponse(connector.RawResponse{
		Operation: "test", Method: "GET", Path: "/test", Attempt: 1, StatusCode: 200,
		Body: []byte(`{"echo":"` + token + `"}`),
	})
	if err == nil {
		t.Fatal("RecordResponse() accepted a body containing the credential")
	}
	failedPath, err := session.Fail(errors.New("provider echoed " + token))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("complete target exists after failure: %v", err)
	}
	assertTreeExcludes(t, failedPath, token)
	var runFile runRecord
	decodeFile(t, filepath.Join(failedPath, "run.json"), &runFile)
	if runFile.Status != "FAILED" || !strings.Contains(runFile.Failure, "[REDACTED]") {
		t.Fatalf("failed run record = %#v", runFile)
	}
	var requests []requestRecord
	decodeFile(t, filepath.Join(failedPath, "requests.json"), &requests)
	if len(requests) != 1 || requests[0].Outcome != "OMITTED_SECRET" || requests[0].RawPath != "" {
		t.Fatalf("request ledger = %#v", requests)
	}
}

func TestFailedTraversalAccountsForRetriesAndKeepsPartialEvidence(t *testing.T) {
	t.Parallel()

	target := filepath.Join(t.TempDir(), "raw-run")
	session, err := Begin(target, "clickup", connector.Workspace{ID: "w1", Name: "Test"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.RecordResponse(connector.RawResponse{
		Operation: "page 0", Method: "GET", Path: "/list/l1/task", Query: url.Values{"page": {"0"}},
		Attempt: 1, StatusCode: 200, Body: []byte(`{"tasks":[{"id":"t1"}]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.RecordFailure(connector.RequestFailure{
		Operation: "page 1", Method: "GET", Path: "/list/l1/task", Query: url.Values{"page": {"1"}},
		Attempt: 1, StatusCode: 503, Retrying: true, Error: "ClickUp API failed",
	}); err != nil {
		t.Fatal(err)
	}
	failedPath, err := session.Fail(errors.New("pagination failed after bounded attempts"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(failedPath, "raw", "000001.json")); err != nil {
		t.Fatalf("partial raw evidence was not preserved: %v", err)
	}
	var runFile runRecord
	decodeFile(t, filepath.Join(failedPath, "run.json"), &runFile)
	if runFile.Status != "FAILED" || runFile.Requests.SuccessfulResponses != 1 ||
		runFile.Requests.FailedAttempts != 1 || runFile.Requests.RetriedAttempts != 1 {
		t.Fatalf("failure accounting = %#v", runFile)
	}
}

func decodeFile(t *testing.T, path string, destination any) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, destination); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func assertPermissions(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s permissions = %v, want %v", path, got, want)
	}
}

func assertTreeExcludes(t *testing.T, root, forbidden string) {
	t.Helper()
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
		if strings.Contains(string(content), forbidden) {
			t.Errorf("credential found in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
