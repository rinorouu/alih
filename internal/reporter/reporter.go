// Package reporter coordinates M6 recovery reporting. It reads what the
// archive records about itself, runs M5 verification, and hands both to the
// report builder so a report can never claim more than the verifier proved.
package reporter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"alih/internal/archive"
	"alih/internal/report"
	"alih/internal/verify"
)

// alihVersion identifies the Alih build that produced the report.
const alihVersion = "0.0.1"

type archiveVerifier interface {
	Verify(path string) (verify.Report, error)
}

// Service builds a recovery report for one archive.
type Service struct {
	verifier archiveVerifier
	now      func() time.Time
}

// New creates the M6 reporting service on top of the M5 verifier.
func New(verifier archiveVerifier) *Service {
	return &Service{verifier: verifier, now: func() time.Time { return time.Now().UTC() }}
}

// Name identifies this application service.
func (service *Service) Name() string { return "m6-recovery-report" }

// Report verifies the archive at path and renders the result as a recovery
// report document. It performs no network access, does not re-read the source,
// and does not write to the archive.
func (service *Service) Report(path string) (report.Document, error) {
	verification, err := service.verifier.Verify(path)
	if err != nil {
		return report.Document{}, err
	}
	manifest, manifestErr := readManifest(path)
	inputs := report.Inputs{
		ArchivePath:       verification.ArchivePath,
		Manifest:          manifest,
		ManifestAvailable: manifestErr == nil,
		Verification:      verification,
		GeneratedAt:       service.now(),
		AlihVersion:       alihVersion,
	}
	if inputs.ArchivePath == "" {
		inputs.ArchivePath = path
	}
	if manifestErr != nil {
		inputs.ManifestError = manifestErr.Error()
	}
	return report.Build(inputs), nil
}

// readManifest loads the archive's own statement about itself. A report is
// still produced when it cannot be read; the absence is reported rather than
// filled in.
func readManifest(archivePath string) (archive.Manifest, error) {
	path := filepath.Join(archivePath, "manifest.json")
	info, err := os.Lstat(path)
	if err != nil {
		return archive.Manifest{}, err
	}
	if !info.Mode().IsRegular() {
		return archive.Manifest{}, fmt.Errorf("manifest.json is not a regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return archive.Manifest{}, err
	}
	var manifest archive.Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return archive.Manifest{}, fmt.Errorf("manifest.json is not valid JSON: %w", err)
	}
	return manifest, nil
}
