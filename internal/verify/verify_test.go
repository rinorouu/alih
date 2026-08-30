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
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"alih/internal/connector/clickup"
)

func TestVerifyHealthyArchiveIsProvenWithoutModifyingIt(t *testing.T) {
	t.Parallel()

	archivePath := buildFixtureArchive(t, http.StatusOK)
	before := treeDigest(t, archivePath)
	report := verifyFixture(t, archivePath)

	if report.Result != ResultVerifiedWithLimitations {
		t.Fatalf("result = %s, want %s\nchecks: %#v", report.Result, ResultVerifiedWithLimitations, report.Checks)
	}
	if report.Failed() {
		t.Fatal("a verified archive was reported as failed")
	}
	// Only the two claims that archived evidence genuinely cannot establish
	// may be anything other than a pass.
	unproven := map[string]bool{
		"source_consistency_scope": true, "limitation_preservation": true,
		"access_scope_completeness": true,
	}
	for _, check := range report.Checks {
		if unproven[check.Name] {
			if check.Status != CheckUnproven {
				t.Errorf("check %s = %s, want %s", check.Name, check.Status, CheckUnproven)
			}
			continue
		}
		if check.Status != CheckPass {
			t.Errorf("check %s = %s (%s) %v", check.Name, check.Status, check.Summary, check.Findings)
		}
	}
	for _, entity := range report.Reconciliation {
		if entity.Status != CheckPass || entity.Expected != entity.Archived {
			t.Errorf("reconciliation %#v", entity)
		}
	}
	if after := treeDigest(t, archivePath); !reflect.DeepEqual(before, after) {
		t.Fatal("verification modified the archive it was verifying")
	}
	if !containsSubstring(report.Limitations, "Point-in-time consistency is not claimed") {
		t.Errorf("verification dropped the non-atomic source limitation: %#v", report.Limitations)
	}
	if !containsSubstring(report.Limitations, "Task relationships") {
		t.Errorf("verification dropped the PARTIAL source capability limitation: %#v", report.Limitations)
	}
}

func TestVerifyRejectsUnverifiedArchiveClaimsWithoutEvidence(t *testing.T) {
	t.Parallel()

	// An archive whose supported attachment could not be downloaded is
	// INCOMPLETE, and completing the M4 export does not make it verified.
	archivePath := buildFixtureArchive(t, http.StatusServiceUnavailable)
	report := verifyFixture(t, archivePath)

	if report.Result != ResultIncomplete {
		t.Fatalf("result = %s, want %s", report.Result, ResultIncomplete)
	}
	if !report.Failed() {
		t.Fatal("an incomplete archive was reported as passing")
	}
	if check := checkStatus(t, report, "attachment_integrity"); check.Status != CheckIncomplete {
		t.Fatalf("attachment_integrity = %s %v", check.Status, check.Findings)
	}
	if check := checkStatus(t, report, "discrepancy_reconciliation"); check.Status != CheckPass {
		t.Fatalf("the manifest should still disclose the unresolved attachment: %#v", check)
	}
	for _, entity := range report.Reconciliation {
		if entity.Entity == "attachments" && (entity.Unresolved != 1 || entity.Status != CheckIncomplete) {
			t.Fatalf("attachment reconciliation = %#v", entity)
		}
	}
}

func TestVerifyDetectsDeliberateCorruption(t *testing.T) {
	t.Parallel()

	healthy := buildFixtureArchive(t, http.StatusOK)
	attachmentPath := attachmentRelativePath(t, healthy)

	cases := []struct {
		name        string
		corrupt     func(*testing.T, string)
		failedCheck string
		wantResult  string
	}{
		{
			name: "attachment binary deleted",
			corrupt: func(t *testing.T, path string) {
				if err := os.Remove(filepath.Join(path, filepath.FromSlash(attachmentPath))); err != nil {
					t.Fatal(err)
				}
			},
			failedCheck: "attachment_integrity",
		},
		{
			name: "attachment binary altered and re-recorded in the manifest",
			corrupt: func(t *testing.T, path string) {
				writeFile(t, filepath.Join(path, filepath.FromSlash(attachmentPath)), []byte("tampered attachment\n"))
				refreshManifestChecksum(t, path, attachmentPath)
			},
			failedCheck: "attachment_integrity",
		},
		{
			name: "attachment binary altered",
			corrupt: func(t *testing.T, path string) {
				writeFile(t, filepath.Join(path, filepath.FromSlash(attachmentPath)), []byte("tampered attachment\n"))
			},
			failedCheck: "file_checksums",
		},
		{
			name: "manifest-recorded archive file changed",
			corrupt: func(t *testing.T, path string) {
				writeFile(t, filepath.Join(path, "raw", "raw", "000001.json"), []byte(`{"spaces":[{"id":"s1","name":"Renamed"}]}`))
			},
			failedCheck: "file_checksums",
		},
		{
			name: "unrecorded file added to the archive",
			corrupt: func(t *testing.T, path string) {
				writeFile(t, filepath.Join(path, "attachments", "smuggled.bin"), []byte("not archived evidence"))
			},
			failedCheck: "manifest_file_inventory",
		},
		{
			name: "sqlite pages corrupted",
			corrupt: func(t *testing.T, path string) {
				corruptSQLitePages(t, filepath.Join(path, "alih.db"))
				refreshManifestChecksum(t, path, "alih.db")
			},
			failedCheck: "sqlite_integrity",
		},
		{
			name: "relationship endpoint broken",
			corrupt: func(t *testing.T, path string) {
				mutateDatabase(t, path, `UPDATE relationships SET to_record_id='alih_absent'`)
			},
			failedCheck: "referential_integrity",
		},
		{
			name: "record silently reparented",
			corrupt: func(t *testing.T, path string) {
				mutateDatabase(t, path, `UPDATE records SET parent_record_id=(SELECT id FROM records WHERE source_id='t2') WHERE source_id='t3'`)
			},
			failedCheck: "hierarchy_reconstruction",
		},
		{
			name: "archived record removed",
			corrupt: func(t *testing.T, path string) {
				mutateDatabase(t, path,
					`DELETE FROM record_identities WHERE record_id=(SELECT id FROM records WHERE source_id='t2')`,
					`DELETE FROM records WHERE source_id='t2'`)
			},
			failedCheck: "source_object_reconciliation",
		},
		{
			name: "archived comment removed",
			corrupt: func(t *testing.T, path string) {
				mutateDatabase(t, path, `DELETE FROM comments WHERE source_id='c2'`)
			},
			failedCheck: "count_reconciliation",
		},
		{
			name: "custom field value no longer exists in its definition",
			corrupt: func(t *testing.T, path string) {
				mutateDatabase(t, path, `UPDATE record_field_values SET observed_value_json='"o-removed"'`)
			},
			failedCheck: "custom_field_evidence",
		},
		{
			name: "custom field value points at an unarchived definition",
			corrupt: func(t *testing.T, path string) {
				mutateDatabase(t, path, `UPDATE record_field_values SET field_id='alih_absent'`)
			},
			failedCheck: "referential_integrity",
		},
		{
			name: "portable identifier no longer derives from its source id",
			corrupt: func(t *testing.T, path string) {
				mutateDatabase(t, path, `UPDATE field_definitions SET source_id='f-renamed'`)
			},
			failedCheck: "portable_identifier_derivation",
		},
		{
			name: "manifest hides an archive file",
			corrupt: func(t *testing.T, path string) {
				manifest := readManifest(t, path)
				kept := manifest.Files[:0]
				for _, file := range manifest.Files {
					if file.Path != "schema.json" {
						kept = append(kept, file)
					}
				}
				manifest.Files = kept
				writeManifest(t, path, manifest)
			},
			failedCheck: "manifest_file_inventory",
		},
		{
			name: "manifest overstates what was archived",
			corrupt: func(t *testing.T, path string) {
				manifest := readManifest(t, path)
				count := manifest.Inventory["tasks"]
				count.Expected = 99
				manifest.Inventory["tasks"] = count
				writeManifest(t, path, manifest)
			},
			failedCheck: "count_reconciliation",
		},
		{
			name: "raw evidence altered",
			corrupt: func(t *testing.T, path string) {
				writeFile(t, filepath.Join(path, "raw", "inventory.json"), []byte(`{"schema_version":1,"kind":"raw_source_inventory"}`))
				refreshManifestChecksum(t, path, "raw/inventory.json")
			},
			failedCheck: "raw_evidence_integrity",
		},
		{
			name: "schema no longer describes the database",
			corrupt: func(t *testing.T, path string) {
				content, err := os.ReadFile(filepath.Join(path, "schema.json"))
				if err != nil {
					t.Fatal(err)
				}
				altered := strings.Replace(string(content), `"name": "download_status"`, `"name": "download_state"`, 1)
				if altered == string(content) {
					t.Fatal("schema fixture no longer contains the expected column")
				}
				writeFile(t, filepath.Join(path, "schema.json"), []byte(altered))
				refreshManifestChecksum(t, path, "schema.json")
			},
			failedCheck: "schema_consistency",
		},
		{
			name: "database and manifest disagree about the archive",
			corrupt: func(t *testing.T, path string) {
				mutateDatabase(t, path, `UPDATE archive_metadata SET value='sha256:forged' WHERE key='source_snapshot_logical_digest'`)
			},
			failedCheck: "archive_metadata_consistency",
		},
		{
			name: "manifest hides an unresolved attachment",
			corrupt: func(t *testing.T, path string) {
				mutateDatabase(t, path, `UPDATE attachments SET download_status='UNRESOLVED', local_path=NULL, archived_size=NULL, checksum=NULL, error='hidden'`)
				if err := os.Remove(filepath.Join(path, filepath.FromSlash(attachmentPath))); err != nil {
					t.Fatal(err)
				}
				manifest := readManifest(t, path)
				kept := manifest.Files[:0]
				for _, file := range manifest.Files {
					if file.Path != attachmentPath {
						kept = append(kept, file)
					}
				}
				manifest.Files = kept
				writeManifest(t, path, manifest)
			},
			failedCheck: "discrepancy_reconciliation",
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			corrupted := corruptCopy(t, healthy, testCase.corrupt)
			report := verifyFixture(t, corrupted)
			want := testCase.wantResult
			if want == "" {
				want = ResultFailed
			}
			if report.Result != want {
				t.Fatalf("result = %s, want %s\nchecks: %#v", report.Result, want, report.Checks)
			}
			if !report.Failed() {
				t.Fatal("a corrupted archive was not rejected")
			}
			check := checkStatus(t, report, testCase.failedCheck)
			if check.Status != CheckFail {
				t.Fatalf("check %s = %s (%s) %v; the corruption was not attributed to it", check.Name, check.Status, check.Summary, check.Findings)
			}
			if len(check.Findings) == 0 {
				t.Fatalf("check %s failed without stating why", check.Name)
			}
		})
	}
}

func TestVerifyRejectsAnArchiveThatIsNotOne(t *testing.T) {
	t.Parallel()

	empty := t.TempDir()
	report, err := Archive(empty, Options{FieldSemantics: clickup.FieldSemantics{}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != ResultFailed {
		t.Fatalf("result = %s, want %s", report.Result, ResultFailed)
	}
	if _, err := Archive(filepath.Join(empty, "absent"), Options{}); err == nil {
		t.Fatal("Archive() accepted a path that does not exist")
	}
}

func TestVerifyWithoutFieldSemanticsDoesNotClaimValueValidity(t *testing.T) {
	t.Parallel()

	archivePath := buildFixtureArchive(t, http.StatusOK)
	report, err := Archive(archivePath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	check := checkStatus(t, report, "custom_field_evidence")
	if check.Status != CheckUnproven {
		t.Fatalf("custom_field_evidence = %s, want %s", check.Status, CheckUnproven)
	}
	if report.Result != ResultVerifiedWithLimitations {
		t.Fatalf("result = %s", report.Result)
	}
}

func attachmentRelativePath(t *testing.T, archivePath string) string {
	t.Helper()
	manifest := readManifest(t, archivePath)
	for _, attachment := range manifest.Attachments {
		if attachment.Status == "RETRIEVED" && attachment.LocalPath != nil {
			return *attachment.LocalPath
		}
	}
	t.Fatal("fixture archive contains no retrieved attachment")
	return ""
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

// corruptSQLitePages damages database pages while leaving the header intact so
// that the file still opens and PRAGMA integrity_check is what detects it.
func corruptSQLitePages(t *testing.T, path string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) < 8192 {
		t.Fatalf("fixture database is only %d bytes", len(content))
	}
	for offset := 4096; offset < 4096+512 && offset < len(content); offset++ {
		content[offset] ^= 0xff
	}
	writeFile(t, path, content)
}

func containsSubstring(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func TestVerifyDoesNotClaimExecutableSemanticsForComputedFields(t *testing.T) {
	t.Parallel()

	archivePath := corruptCopy(t, buildFixtureArchive(t, http.StatusOK), func(t *testing.T, path string) {
		// Re-archive the fixture field as a ClickUp formula field, which M4
		// records as observed-only evidence with no executable semantics.
		mutateDatabase(t, path,
			`UPDATE field_definitions SET field_type='formula', semantics_state='OBSERVED_ONLY_NO_EXECUTION',
			 definition_json=replace(definition_json,'"type":"drop_down"','"type":"formula"')`)
	})
	report := verifyFixture(t, archivePath)

	if report.Result != ResultVerifiedWithLimitations {
		t.Fatalf("result = %s, want %s", report.Result, ResultVerifiedWithLimitations)
	}
	check := checkStatus(t, report, "custom_field_evidence")
	if check.Status != CheckUnproven {
		t.Fatalf("custom_field_evidence = %s (%s) %v", check.Status, check.Summary, check.Findings)
	}
	if !containsSubstring(check.Findings, "does not claim executable source semantics") {
		t.Fatalf("computed-field limitation was not stated: %#v", check.Findings)
	}
}

func TestVerifyRejectsAnArchiveThatPointsOutsideItself(t *testing.T) {
	t.Parallel()

	healthy := buildFixtureArchive(t, http.StatusOK)
	outside := filepath.Join(t.TempDir(), "outside.bin")
	writeFile(t, outside, attachmentContent)
	attachmentPath := attachmentRelativePath(t, healthy)
	corrupted := corruptCopy(t, healthy, func(t *testing.T, path string) {
		binary := filepath.Join(path, filepath.FromSlash(attachmentPath))
		if err := os.Remove(binary); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, binary); err != nil {
			t.Skipf("symlinks are unavailable in this environment: %v", err)
		}
	})
	report := verifyFixture(t, corrupted)

	if report.Result != ResultFailed {
		t.Fatalf("result = %s, want %s", report.Result, ResultFailed)
	}
	if check := checkStatus(t, report, "archive_structure"); check.Status != CheckFail {
		t.Fatalf("archive_structure = %s %v", check.Status, check.Findings)
	}
}

func TestVerifyNeverEstablishesAccessScopeCompleteness(t *testing.T) {
	t.Parallel()

	// PRD section 22 requires that an insufficient and undetected credential
	// scope cannot produce a clean VERIFIED result. No archive-internal
	// evidence can detect it, so the claim must be permanently unproven.
	report := verifyFixture(t, buildFixtureArchive(t, http.StatusOK))

	check := checkStatus(t, report, "access_scope_completeness")
	if check.Status != CheckUnproven {
		t.Fatalf("access_scope_completeness = %s, want %s", check.Status, CheckUnproven)
	}
	if report.Result == ResultVerified {
		t.Fatal("an archive reached a clean VERIFIED without establishing its access scope")
	}
	if !containsSubstring(check.Findings, "Fixture User") || !containsSubstring(check.Findings, "u1") {
		t.Errorf("the archive's access scope was not attributed to the extracting account: %#v", check.Findings)
	}
	if !containsSubstring(report.NotProven, "could see the entire Workspace") {
		t.Errorf("the access scope limitation is missing from the unproven claims: %#v", report.NotProven)
	}
}
