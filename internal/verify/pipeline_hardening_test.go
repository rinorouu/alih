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
	"strings"
	"testing"

	"alih/internal/connector/clickup"
)

// TestNoArchiveDamageEverProducesAVerifiedResult is the umbrella fail-closed
// assertion for PRD section 22. Whatever is done to an archive, the result must
// never be VERIFIED or VERIFIED_WITH_LIMITATIONS unless the archive really is
// intact.
func TestNoArchiveDamageEverProducesAVerifiedResult(t *testing.T) {
	t.Parallel()

	healthy := buildFixtureArchive(t, http.StatusOK)
	attachmentPath := attachmentRelativePath(t, healthy)

	damages := []struct {
		name    string
		corrupt func(*testing.T, string)
	}{
		{"alih.db truncated to nothing", func(t *testing.T, path string) {
			writeFile(t, filepath.Join(path, "alih.db"), nil)
			refreshManifestChecksum(t, path, "alih.db")
		}},
		{"alih.db replaced by text", func(t *testing.T, path string) {
			writeFile(t, filepath.Join(path, "alih.db"), []byte("not a database"))
			refreshManifestChecksum(t, path, "alih.db")
		}},
		{"alih.db header damaged", func(t *testing.T, path string) {
			content, err := os.ReadFile(filepath.Join(path, "alih.db"))
			if err != nil {
				t.Fatal(err)
			}
			copy(content[:16], []byte("XXXXXXXXXXXXXXXX"))
			writeFile(t, filepath.Join(path, "alih.db"), content)
			refreshManifestChecksum(t, path, "alih.db")
		}},
		{"alih.db removed", func(t *testing.T, path string) {
			if err := os.Remove(filepath.Join(path, "alih.db")); err != nil {
				t.Fatal(err)
			}
		}},
		{"schema.json removed", func(t *testing.T, path string) {
			if err := os.Remove(filepath.Join(path, "schema.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"manifest.json removed", func(t *testing.T, path string) {
			if err := os.Remove(filepath.Join(path, "manifest.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"manifest.json emptied", func(t *testing.T, path string) {
			writeFile(t, filepath.Join(path, "manifest.json"), nil)
		}},
		{"raw evidence directory removed", func(t *testing.T, path string) {
			if err := os.RemoveAll(filepath.Join(path, "raw")); err != nil {
				t.Fatal(err)
			}
		}},
		{"attachments directory removed", func(t *testing.T, path string) {
			if err := os.RemoveAll(filepath.Join(path, "attachments")); err != nil {
				t.Fatal(err)
			}
		}},
		{"raw run record downgraded to in progress", func(t *testing.T, path string) {
			runPath := filepath.Join(path, "raw", "run.json")
			content, err := os.ReadFile(runPath)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, runPath, []byte(strings.Replace(string(content), `"COMPLETE"`, `"IN_PROGRESS"`, 1)))
			refreshManifestChecksum(t, path, "raw/run.json")
		}},
		{"manifest claims a status it cannot support", func(t *testing.T, path string) {
			manifest := readManifest(t, path)
			manifest.Status = "FAILED"
			writeManifest(t, path, manifest)
		}},
		{"manifest schema version from another format", func(t *testing.T, path string) {
			manifest := readManifest(t, path)
			manifest.SchemaVersion = 99
			writeManifest(t, path, manifest)
		}},
		{"archive metadata claims another workspace", func(t *testing.T, path string) {
			mutateDatabase(t, path, `UPDATE archive_metadata SET value='someone-else' WHERE key='source_workspace_id'`)
		}},
		{"portable rows point at another workspace", func(t *testing.T, path string) {
			mutateDatabase(t, path, `UPDATE records SET workspace_id='alih_elsewhere'`)
		}},
		{"a comment is re-attached to another record", func(t *testing.T, path string) {
			mutateDatabase(t, path,
				`UPDATE comments SET record_id=(SELECT id FROM records WHERE source_id='t2') WHERE parent_comment_id IS NULL`)
		}},
		{"an identity role points at nothing", func(t *testing.T, path string) {
			mutateDatabase(t, path, `UPDATE record_identities SET identity_id='alih_absent'`)
		}},
		{"a field definition contradicts its own source definition", func(t *testing.T, path string) {
			mutateDatabase(t, path, `UPDATE field_definitions SET field_type='labels'`)
		}},
		{"an observed value claims stronger semantics", func(t *testing.T, path string) {
			mutateDatabase(t, path, `UPDATE record_field_values SET semantics_state='EXECUTABLE'`)
		}},
		{"a relationship loses its original endpoint ids", func(t *testing.T, path string) {
			mutateDatabase(t, path, `UPDATE relationships SET from_source_id=''`)
		}},
		{"an attachment is re-pointed at another record", func(t *testing.T, path string) {
			mutateDatabase(t, path, `UPDATE attachments SET record_id=(SELECT id FROM records WHERE source_id='t2')`)
		}},
		{"an extra portable row appears from nowhere", func(t *testing.T, path string) {
			mutateDatabase(t, path, `INSERT INTO record_tags VALUES('alih_absent','ghost',0)`)
		}},
		{"an attachment binary is swapped for another", func(t *testing.T, path string) {
			writeFile(t, filepath.Join(path, filepath.FromSlash(attachmentPath)), []byte("different bytes entirely"))
			refreshManifestChecksum(t, path, attachmentPath)
		}},
		{"the archive gains an unrecorded file", func(t *testing.T, path string) {
			writeFile(t, filepath.Join(path, "raw", "extra.json"), []byte(`{}`))
		}},
	}

	for _, damage := range damages {
		damage := damage
		t.Run(damage.name, func(t *testing.T) {
			t.Parallel()
			corrupted := corruptCopy(t, healthy, damage.corrupt)
			report := verifyFixture(t, corrupted)
			if !report.Failed() {
				t.Fatalf("damage %q still produced %s", damage.name, report.Result)
			}
			// A rejected archive must always name a reason.
			named := false
			for _, check := range report.Checks {
				if check.Status == CheckFail || check.Status == CheckNotEvaluated || check.Status == CheckIncomplete {
					named = true
				}
			}
			if !named {
				t.Fatalf("archive was rejected without any check reporting a problem: %#v", report.Checks)
			}
		})
	}
}

// TestVerificationIsRepeatableAndReadOnly proves that verification is a pure
// observation: two runs agree, and neither touches the archive.
func TestVerificationIsRepeatableAndReadOnly(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		archivePath := buildFixtureArchive(t, status)
		before := treeDigest(t, archivePath)
		first := verifyFixture(t, archivePath)
		second := verifyFixture(t, archivePath)
		after := treeDigest(t, archivePath)

		if first.Result != second.Result {
			t.Fatalf("verification is not repeatable: %s then %s", first.Result, second.Result)
		}
		if len(first.Checks) != len(second.Checks) {
			t.Fatalf("check count changed between runs: %d then %d", len(first.Checks), len(second.Checks))
		}
		for index := range first.Checks {
			if first.Checks[index].Name != second.Checks[index].Name || first.Checks[index].Status != second.Checks[index].Status {
				t.Fatalf("check %d differs between runs: %#v vs %#v", index, first.Checks[index], second.Checks[index])
			}
		}
		if len(before) != len(after) {
			t.Fatalf("verification changed the archive file set: %d then %d files", len(before), len(after))
		}
		for path, digest := range before {
			if after[path] != digest {
				t.Fatalf("verification modified %s", path)
			}
		}
	}
}

// TestFieldSemanticsFromAnotherConnectorIsNotApplied guards the connector
// boundary: evidence must not be interpreted by rules written for a different
// source.
func TestFieldSemanticsFromAnotherConnectorIsNotApplied(t *testing.T) {
	t.Parallel()

	archivePath := buildFixtureArchive(t, http.StatusOK)
	report, err := Archive(archivePath, Options{FieldSemantics: foreignSemantics{}})
	if err != nil {
		t.Fatal(err)
	}
	check := checkStatus(t, report, "custom_field_evidence")
	if check.Status != CheckUnproven {
		t.Fatalf("custom_field_evidence = %s, want %s when no matching interpreter exists", check.Status, CheckUnproven)
	}
	if !containsSubstring(check.Findings, "no field-value semantics are available for connector") {
		t.Fatalf("the missing interpreter was not disclosed: %#v", check.Findings)
	}
}

type foreignSemantics struct{}

func (foreignSemantics) Connector() string { return "not-clickup" }
func (foreignSemantics) ValidateFieldValue(string, []byte, []byte) (string, string) {
	return FieldValueValid, "this interpreter must never be consulted"
}

var _ FieldSemantics = clickup.FieldSemantics{}
