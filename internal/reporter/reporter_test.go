package reporter

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/verify"
)

type stubVerifier struct {
	report verify.Report
	err    error
	path   string
}

func (stub *stubVerifier) Verify(path string) (verify.Report, error) {
	stub.path = path
	return stub.report, stub.err
}

var completedAt = time.Date(2026, 8, 30, 1, 30, 0, 0, time.UTC)

func writeManifest(t *testing.T, directory string, manifest archive.Manifest) {
	t.Helper()
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newService(verifier *stubVerifier) *Service {
	service := New(verifier)
	service.now = func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }
	return service
}

func TestReportCombinesArchiveEvidenceWithVerification(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeManifest(t, directory, archive.Manifest{
		SchemaVersion: archive.ArchiveSchemaVersion, AlihVersion: "0.0.1", Status: archive.StatusCreatedUnverified,
		SourceSnapshotCompletedAt: time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC),
		ArchiveCompletedAt:        &completedAt, Connector: "clickup",
		Source:        connector.Workspace{ID: "w1", Name: "Example"},
		InputSnapshot: archive.InputSnapshot{LogicalDigest: "sha256:abc", Atomic: false},
		Files:         []archive.FileRecord{{Path: "alih.db"}},
	})
	verifier := &stubVerifier{report: verify.Report{
		ArchivePath: directory, Result: verify.ResultVerifiedWithLimitations,
		Connector: "clickup", Source: connector.Workspace{ID: "w1", Name: "Example"},
		Checks: []verify.Check{{Name: "sqlite_integrity", Status: verify.CheckPass}},
	}}

	document, err := newService(verifier).Report(directory)
	if err != nil {
		t.Fatal(err)
	}
	if verifier.path != directory {
		t.Fatalf("verifier received %q", verifier.path)
	}
	if !document.Identity.ManifestReadable {
		t.Fatal("a readable manifest was reported as unreadable")
	}
	if document.Identity.SourceSnapshotCompletedAt == nil || document.Identity.ArchiveCompletedAt == nil || document.Identity.RecordedFiles != 1 {
		t.Fatalf("identity = %#v", document.Identity)
	}
	if document.Conclusion.Result != verify.ResultVerifiedWithLimitations {
		t.Fatalf("conclusion result = %s", document.Conclusion.Result)
	}
	if document.AlihVersion == "" || document.GeneratedAt.IsZero() {
		t.Fatal("the report does not identify who produced it and when")
	}
}

func TestReportStillProducedWhenTheArchiveCannotDescribeItself(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier := &stubVerifier{report: verify.Report{ArchivePath: directory, Result: verify.ResultFailed}}

	document, err := newService(verifier).Report(directory)
	if err != nil {
		t.Fatal(err)
	}
	if document.Identity.ManifestReadable || document.Identity.ManifestError == "" {
		t.Fatalf("identity = %#v", document.Identity)
	}
	if !document.Failed() {
		t.Fatal("an archive that cannot describe itself was reported as passing")
	}
}

func TestReportFailsWhenVerificationCannotRun(t *testing.T) {
	t.Parallel()

	verifier := &stubVerifier{err: errors.New("archive path must be a real directory")}
	if _, err := newService(verifier).Report("/nonexistent"); err == nil {
		t.Fatal("Report() produced a document without a verification result")
	}
}

func TestReportDoesNotWriteToTheArchive(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeManifest(t, directory, archive.Manifest{SchemaVersion: archive.ArchiveSchemaVersion, Status: archive.StatusCreatedUnverified})
	before, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	verifier := &stubVerifier{report: verify.Report{ArchivePath: directory, Result: verify.ResultVerified}}
	if _, err := newService(verifier).Report(directory); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("reporting changed the archive contents: %d entries before, %d after", len(before), len(after))
	}
}
