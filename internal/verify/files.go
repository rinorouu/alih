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

package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"alih/internal/archive"
)

// archiveFile is one regular file actually present inside the archive.
type archiveFile struct {
	Path  string
	Bytes int64
}

// requiredEntries are the archive members an M4 archive must always contain.
var requiredFiles = []string{"alih.db", "manifest.json", "schema.json"}
var requiredDirectories = []string{"raw", "attachments"}

// checkStructure walks the archive and establishes the real file inventory.
// Symlinks and non-regular files are rejected: an archive that can reference
// data outside itself is not a portable archive.
func (v *verification) checkStructure() map[string]archiveFile {
	files := make(map[string]archiveFile)
	var findings []string
	walkErr := filepath.WalkDir(v.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == v.root {
			return nil
		}
		relative, err := filepath.Rel(v.root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			findings = append(findings, fmt.Sprintf("%s is a symlink", relative))
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			findings = append(findings, fmt.Sprintf("%s is not a regular file", relative))
			return nil
		}
		files[relative] = archiveFile{Path: relative, Bytes: info.Size()}
		return nil
	})
	if walkErr != nil {
		v.fail("archive_structure", "the archive directory could not be traversed", []string{walkErr.Error()})
		return files
	}
	for _, name := range requiredFiles {
		if _, present := files[name]; !present {
			findings = append(findings, fmt.Sprintf("required file %s is missing", name))
		}
	}
	for _, name := range requiredDirectories {
		info, err := os.Lstat(filepath.Join(v.root, name))
		if err != nil || !info.IsDir() {
			findings = append(findings, fmt.Sprintf("required directory %s/ is missing", name))
		}
	}
	v.decide("archive_structure",
		"required archive members are present and every archive entry is a regular file",
		"the archive is missing required members or contains entries that are not regular files",
		findings)
	return files
}

// checkManifest parses manifest.json and establishes what the archive claims
// to be. Reconciling the recorded file list with the files actually present is
// reported separately so that a file-level discrepancy does not hide the
// deeper evidence checks.
func (v *verification) checkManifest(files map[string]archiveFile) (archive.Manifest, bool) {
	var manifest archive.Manifest
	content, err := os.ReadFile(filepath.Join(v.root, "manifest.json"))
	if err != nil {
		v.fail("manifest_integrity", "manifest.json could not be read", []string{err.Error()})
		return manifest, false
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		v.fail("manifest_integrity", "manifest.json is not valid JSON", []string{err.Error()})
		return manifest, false
	}

	var findings []string
	if manifest.SchemaVersion != archive.ArchiveSchemaVersion {
		findings = append(findings, fmt.Sprintf("unsupported manifest schema_version %d; this Alih build reads version %d", manifest.SchemaVersion, archive.ArchiveSchemaVersion))
	}
	if strings.TrimSpace(manifest.Connector) == "" {
		findings = append(findings, "manifest does not identify the source connector")
	}
	if strings.TrimSpace(manifest.Source.ID) == "" {
		findings = append(findings, "manifest does not identify the source workspace")
	}
	switch manifest.Status {
	case archive.StatusCreatedUnverified, archive.StatusIncomplete:
	case archive.StatusFailed:
		findings = append(findings, "manifest records archive status FAILED; the archive was never completed")
	default:
		findings = append(findings, fmt.Sprintf("manifest records unknown archive status %q", manifest.Status))
	}
	if manifest.Verification.Status == "" {
		findings = append(findings, "manifest does not record a verification status")
	}

	ok := v.decide("manifest_integrity",
		fmt.Sprintf("manifest.json describes a %s archive of workspace %q written by Alih %s", manifest.Status, manifest.Source.ID, manifest.AlihVersion),
		"manifest.json does not describe a usable archive",
		findings)
	v.checkManifestFileInventory(manifest, files)
	return manifest, ok
}

// checkManifestFileInventory proves that the manifest records exactly the
// files present in the archive. A file that exists but is not recorded, or a
// recorded file that no longer exists, is a manifest integrity failure.
func (v *verification) checkManifestFileInventory(manifest archive.Manifest, files map[string]archiveFile) {
	var findings []string
	recorded := make(map[string]archive.FileRecord, len(manifest.Files))
	for _, file := range manifest.Files {
		if file.Path == "manifest.json" {
			findings = append(findings, "manifest records itself in its own file list")
			continue
		}
		if _, duplicate := recorded[file.Path]; duplicate {
			findings = append(findings, fmt.Sprintf("manifest records %s more than once", file.Path))
			continue
		}
		if file.Path == "" || filepath.IsAbs(filepath.FromSlash(file.Path)) || strings.Contains(file.Path, "..") {
			findings = append(findings, fmt.Sprintf("manifest records unsafe archive path %q", file.Path))
			continue
		}
		recorded[file.Path] = file
		if _, present := files[file.Path]; !present {
			findings = append(findings, fmt.Sprintf("%s is recorded in the manifest but missing from the archive", file.Path))
		}
	}
	for path := range files {
		if path == "manifest.json" {
			continue
		}
		if _, present := recorded[path]; !present {
			findings = append(findings, fmt.Sprintf("%s exists in the archive but is not recorded in the manifest", path))
		}
	}
	v.decide("manifest_file_inventory",
		fmt.Sprintf("manifest.json records exactly the %d files present in the archive", len(recorded)),
		"manifest.json does not record exactly the files present in the archive",
		findings)
}

// checkRecordedFileChecksums re-reads every recorded file and recomputes its
// size and SHA-256 digest.
func (v *verification) checkRecordedFileChecksums(manifest archive.Manifest, files map[string]archiveFile) {
	var findings []string
	verified := 0
	for _, record := range manifest.Files {
		if record.Path == "manifest.json" {
			continue
		}
		if _, present := files[record.Path]; !present {
			continue
		}
		size, checksum, err := fileDigest(filepath.Join(v.root, filepath.FromSlash(record.Path)))
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s could not be read: %v", record.Path, err))
			continue
		}
		if size != record.Bytes {
			findings = append(findings, fmt.Sprintf("%s is %d bytes but the manifest records %d", record.Path, size, record.Bytes))
			continue
		}
		if checksum != record.Checksum {
			findings = append(findings, fmt.Sprintf("%s checksum %s does not match the manifest checksum %s", record.Path, checksum, record.Checksum))
			continue
		}
		verified++
	}
	v.decide("file_checksums",
		fmt.Sprintf("%d archived files match their recorded size and SHA-256 checksum", verified),
		"at least one archived file no longer matches its recorded checksum",
		findings)
}

func fileDigest(path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return 0, "", err
	}
	return size, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
