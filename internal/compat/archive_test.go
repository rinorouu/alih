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

package compat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/connector/clickup"
	"alih/internal/organize"
	"alih/internal/report"
	"alih/internal/reporter"
	"alih/internal/snapshot"
	"alih/internal/verify"
)

const (
	releasedArchive           = "testdata/released-0.2.4-archive"
	releasedIncompleteArchive = "testdata/released-0.2.4-archive-incomplete"
	// releasedVersion is the hard-coded provenance value v0.2.4 wrote before
	// the release identity was injected. A current build must never correct it.
	releasedVersion = "0.0.1"
)

// workingCopy copies a frozen corpus archive into a temporary directory. The
// corpus itself is never handed to code under test, so a defect cannot damage
// the fixture that would detect it.
func workingCopy(t *testing.T, source string) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), filepath.Base(source))
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

	// attachments/ is a required archive member, and in the incomplete corpus it
	// is legitimately empty: that archive failed before it retrieved anything.
	// Git cannot store an empty directory, so a clone of this repository does
	// not have it, and without it the archive fails on structure before
	// verification can reach the INCOMPLETE result the fixture exists to pin.
	// Recreating it restores the archive as it was sealed rather than changing
	// it: an empty directory carries no evidence, and a placeholder file inside
	// it would be recorded as an unlisted file and correctly rejected by
	// manifest_file_inventory.
	if err := os.MkdirAll(filepath.Join(target, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	return target
}

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

func verifyReleased(t *testing.T, path string) verify.Report {
	t.Helper()
	report, err := verify.Archive(path, verify.Options{FieldSemantics: clickup.FieldSemantics{}})
	if err != nil {
		t.Fatalf("verify released archive: %v", err)
	}
	return report
}

func readManifest(t *testing.T, path string) archive.Manifest {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest archive.Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// TestTheCorpusIsTheReleasedShapeNotACurrentOne guards the corpus itself. If a
// later change regenerates these files with current writers, the compatibility
// tests below would silently start proving nothing.
func TestTheCorpusIsTheReleasedShapeNotACurrentOne(t *testing.T) {
	t.Parallel()

	manifest := readManifest(t, releasedArchive)
	if manifest.SchemaVersion != 2 {
		t.Fatalf("corpus manifest schema = %d, want the released 2", manifest.SchemaVersion)
	}
	if manifest.CapabilitySchemaVersion != 0 {
		t.Fatalf("corpus carries capability schema %d; it is no longer the pre-contract release", manifest.CapabilitySchemaVersion)
	}
	if manifest.AlihVersion != releasedVersion {
		t.Fatalf("corpus records version %q, want the released hard-coded %q", manifest.AlihVersion, releasedVersion)
	}
	for _, capability := range manifest.Capabilities {
		if capability.ID != "" || capability.Requirement != "" || capability.Implementation != "" || capability.Availability != "" {
			t.Fatalf("corpus capability already carries contract fields: %#v", capability)
		}
		if capability.Name == "" || capability.State == "" {
			t.Fatalf("corpus capability lost its released shape: %#v", capability)
		}
	}
}

// TestAReleasedArchiveStillVerifies proves the current verifier reads an
// archive produced by an earlier release and reaches the same clean result.
func TestAReleasedArchiveStillVerifies(t *testing.T) {
	t.Parallel()

	path := workingCopy(t, releasedArchive)
	before := treeDigest(t, path)

	report := verifyReleased(t, path)
	if report.Result != verify.ResultVerifiedWithLimitations {
		t.Fatalf("result = %s, want %s\nchecks: %#v", report.Result, verify.ResultVerifiedWithLimitations, report.Checks)
	}
	if report.Failed() {
		t.Fatal("a released archive was reported as failing")
	}
	if !reflect.DeepEqual(before, treeDigest(t, path)) {
		t.Fatal("verification modified a released archive")
	}
}

// TestVerifyingAReleasedArchiveIsRepeatable proves verification is read-only
// and stable, so an old archive does not decay by being checked.
func TestVerifyingAReleasedArchiveIsRepeatable(t *testing.T) {
	t.Parallel()

	path := workingCopy(t, releasedArchive)
	first := verifyReleased(t, path)
	digest := treeDigest(t, path)

	for attempt := 0; attempt < 3; attempt++ {
		repeat := verifyReleased(t, path)
		if repeat.Result != first.Result {
			t.Fatalf("attempt %d result = %s, first = %s", attempt, repeat.Result, first.Result)
		}
		if !reflect.DeepEqual(repeat.Limitations, first.Limitations) {
			t.Fatalf("attempt %d disclosed different limitations", attempt)
		}
		if !reflect.DeepEqual(digest, treeDigest(t, path)) {
			t.Fatalf("attempt %d modified the archive", attempt)
		}
	}
}

// TestAReleasedArchiveKeepsItsOwnProvenance proves a newer build never
// backfills its own release identity or an invented capability contract into an
// archive somebody else already sealed.
func TestAReleasedArchiveKeepsItsOwnProvenance(t *testing.T) {
	t.Parallel()

	path := workingCopy(t, releasedArchive)
	report := verifyReleased(t, path)

	if report.CapabilitySchemaVersion != 0 {
		t.Errorf("verification invented capability schema %d for a pre-contract archive", report.CapabilitySchemaVersion)
	}
	for _, capability := range report.Capabilities {
		if capability.ID != "" || capability.Requirement != "" {
			t.Errorf("a released capability gained an invented identity: %#v", capability)
		}
		if capability.Availability != "" {
			t.Errorf("a released capability gained an invented availability: %#v", capability)
		}
	}
	if manifest := readManifest(t, path); manifest.AlihVersion != releasedVersion {
		t.Errorf("the archive's recorded version became %q", manifest.AlihVersion)
	}
}

// TestAReleasedRawSnapshotStillLoads proves the M3 evidence sealed inside a
// released archive is still readable by the current snapshot reader.
func TestAReleasedRawSnapshotStillLoads(t *testing.T) {
	t.Parallel()

	path := filepath.Join(workingCopy(t, releasedArchive), "raw")
	before := treeDigest(t, path)

	evidence, err := snapshot.LoadComplete(path)
	if err != nil {
		t.Fatalf("load released raw evidence: %v", err)
	}
	if evidence.CapabilitySchemaVersion != 0 {
		t.Errorf("released evidence gained capability schema %d", evidence.CapabilitySchemaVersion)
	}
	if evidence.CapabilityDigest != "" {
		t.Errorf("released evidence gained a capability digest %q", evidence.CapabilityDigest)
	}
	if len(evidence.Capabilities) == 0 {
		t.Fatal("released capability evidence was dropped rather than preserved")
	}
	for _, capability := range evidence.Capabilities {
		if capability.State == connector.CapabilitySupported && capability.ID != "" {
			t.Errorf("a released capability was reinterpreted as a contract record: %#v", capability)
		}
	}
	if !reflect.DeepEqual(before, treeDigest(t, path)) {
		t.Fatal("loading released evidence modified it")
	}
}

// TestAReleasedArchiveStillProducesARecoveryReport proves the M6 reporter reads
// an older archive and neither writes into it nor overstates it.
func TestAReleasedArchiveStillProducesARecoveryReport(t *testing.T) {
	t.Parallel()

	path := workingCopy(t, releasedArchive)
	before := treeDigest(t, path)

	document, err := reporter.NewWithVersion(&fixtureVerifier{}, "test-version").Report(path)
	if err != nil {
		t.Fatalf("report a released archive: %v", err)
	}
	if document.Conclusion.Result != verify.ResultVerifiedWithLimitations {
		t.Fatalf("report result = %s", document.Conclusion.Result)
	}
	if document.Failed() {
		t.Error("a released archive's report reads as a failure")
	}
	if len(document.Limitations) == 0 {
		t.Error("the report dropped the limitations the archive discloses")
	}
	// The report is new work, so it carries the running build; the archive it
	// describes keeps the release identity that actually sealed it.
	if document.AlihVersion != "test-version" {
		t.Errorf("report version = %q, want the running build", document.AlihVersion)
	}
	if manifest := readManifest(t, path); manifest.AlihVersion != releasedVersion {
		t.Errorf("reporting rewrote the archive's recorded version to %q", manifest.AlihVersion)
	}
	if !reflect.DeepEqual(before, treeDigest(t, path)) {
		t.Fatal("reporting modified a released archive")
	}
}

// TestAReleasedArchiveCanBeOrganized proves the newest capability in the
// foundation accepts the oldest artifact it is meant to read.
func TestAReleasedArchiveCanBeOrganized(t *testing.T) {
	t.Parallel()

	path := workingCopy(t, releasedArchive)
	before := treeDigest(t, path)
	output := filepath.Join(t.TempDir(), "view")

	result, err := organize.Build(context.Background(), path, output, organize.Options{
		Verifier: &fixtureVerifier{}, AlihVersion: "test-version",
	})
	if err != nil {
		t.Fatalf("organize a released archive: %v", err)
	}
	if result.Verification != verify.ResultVerifiedWithLimitations || result.Files == 0 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(output, "provenance.json")); err != nil {
		t.Fatalf("no provenance index was written: %v", err)
	}
	if !reflect.DeepEqual(before, treeDigest(t, path)) {
		t.Fatal("organization modified a released archive")
	}
}

// TestAReleasedIncompleteArchiveIsStillRefused proves an old archive that never
// deserved to pass does not start passing under a newer build.
func TestAReleasedIncompleteArchiveIsStillRefused(t *testing.T) {
	t.Parallel()

	path := workingCopy(t, releasedIncompleteArchive)
	report := verifyReleased(t, path)

	if report.Result != verify.ResultIncomplete {
		t.Fatalf("result = %s, want %s", report.Result, verify.ResultIncomplete)
	}
	if !report.Failed() {
		t.Fatal("a released incomplete archive was reported as passing")
	}
	output := filepath.Join(t.TempDir(), "view")
	if _, err := organize.Build(context.Background(), path, output, organize.Options{
		Verifier: &fixtureVerifier{},
	}); err == nil {
		t.Fatal("a released incomplete archive produced an organized view")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Error("a refused organization published an output directory")
	}
}

// TestReleasedArchivesCarryNoAlihCredential scans the frozen corpus so a
// corpus that ever carried Alih's own token cannot be committed unnoticed.
//
// Raw evidence is deliberately excluded from the provider-URL half of this
// check. `raw/` is a verbatim copy of what the provider returned, and ClickUp
// returns a short-lived signed attachment URL inside those bytes. Rewriting
// them would break both the "exact response bytes" guarantee and the
// checksums that make the evidence provable. What must hold is that such a URL
// never escapes raw evidence into the portable database, the manifest, or any
// operational surface, and that Alih's own credential appears nowhere at all.
func TestReleasedArchivesCarryNoAlihCredential(t *testing.T) {
	t.Parallel()

	alihCredential := []string{"pk_", "Authorization", "Bearer "}
	providerSigned := []string{"signature=", "url_w_query"}

	for _, root := range []string{releasedArchive, releasedIncompleteArchive} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, needle := range alihCredential {
				if strings.Contains(string(content), needle) {
					t.Errorf("corpus file %s contains Alih credential material %q", path, needle)
				}
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if strings.HasPrefix(filepath.ToSlash(relative), "raw/") {
				return nil
			}
			for _, needle := range providerSigned {
				if strings.Contains(string(content), needle) {
					t.Errorf("a provider-signed URL escaped raw evidence into %s (%q)", path, needle)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestTheProvableSurfacesNeverCarryASignedURL states the boundary above as a
// claim about the surfaces a user or an operator actually reads, rather than
// about file layout.
func TestTheProvableSurfacesNeverCarryASignedURL(t *testing.T) {
	t.Parallel()

	path := workingCopy(t, releasedArchive)
	report := verifyReleased(t, path)
	encodedReport, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	document, err := reporter.NewWithVersion(&fixtureVerifier{}, "test-version").Report(path)
	if err != nil {
		t.Fatal(err)
	}
	encodedDocument, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	database, err := os.ReadFile(filepath.Join(path, "alih.db"))
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "view")
	if _, err := organize.Build(context.Background(), path, output, organize.Options{
		Verifier: &fixtureVerifier{}, AlihVersion: "test-version",
	}); err != nil {
		t.Fatal(err)
	}

	surfaces := map[string][]byte{
		"verification report": encodedReport,
		"recovery report":     encodedDocument,
		"portable database":   database,
	}
	err = filepath.WalkDir(output, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		surfaces["organized view "+entry.Name()] = content
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range surfaces {
		for _, needle := range []string{"signature=", "url_w_query", "pk_", "Bearer "} {
			if strings.Contains(string(content), needle) {
				t.Errorf("%s contains %q", name, needle)
			}
		}
	}
}

type fixtureVerifier struct{}

func (fixtureVerifier) Verify(path string) (verify.Report, error) {
	return verify.Archive(path, verify.Options{FieldSemantics: clickup.FieldSemantics{}})
}

// clickUpNamer is the display name the real composition root supplies. A
// schema 2 archive predates the manifest field, so the running build is the
// only thing that can still name its provider properly.
type clickUpNamer struct{}

func (clickUpNamer) Connector() string   { return "clickup" }
func (clickUpNamer) DisplayName() string { return "ClickUp" }

// TestAReleasedArchiveIsStillNamedProperlyInItsReport proves the connector
// display name introduced with manifest schema 3 did not cost older archives
// their correct prose. A schema 2 archive carries no display name, so the
// build's own wiring supplies it, and the report reads exactly as it did.
func TestAReleasedArchiveIsStillNamedProperlyInItsReport(t *testing.T) {
	t.Parallel()

	path := workingCopy(t, releasedArchive)
	if manifest := readManifest(t, path); manifest.ConnectorDisplayName != "" {
		t.Fatalf("the released corpus already carries a display name %q", manifest.ConnectorDisplayName)
	}

	document, err := reporter.NewWithVersion(&fixtureVerifier{}, "test-version", clickUpNamer{}).Report(path)
	if err != nil {
		t.Fatalf("report a released archive: %v", err)
	}
	var rendered bytes.Buffer
	if err := report.RenderText(&rendered, document); err != nil {
		t.Fatal(err)
	}
	text := rendered.String()
	if !strings.Contains(text, "ClickUp") {
		t.Errorf("a released ClickUp archive lost its provider's name:\n%s", text)
	}
	if strings.Contains(text, "{connector}") {
		t.Errorf("an unresolved placeholder reached a released archive's report:\n%s", text)
	}
}

// TestWithoutAnyNamerAReleasedArchiveFallsBackToItsIdentifier proves the
// fallback chain ends somewhere honest rather than with a placeholder or with
// another provider's name.
func TestWithoutAnyNamerAReleasedArchiveFallsBackToItsIdentifier(t *testing.T) {
	t.Parallel()

	path := workingCopy(t, releasedArchive)
	document, err := reporter.NewWithVersion(&fixtureVerifier{}, "test-version").Report(path)
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	if err := report.RenderText(&rendered, document); err != nil {
		t.Fatal(err)
	}
	text := rendered.String()
	if strings.Contains(text, "{connector}") {
		t.Errorf("an unresolved placeholder reached the report:\n%s", text)
	}
	if !strings.Contains(text, "clickup") {
		t.Errorf("the report does not fall back to the connector identifier:\n%s", text)
	}
}

// TestANewArchiveRecordsSchemaThreeAndItsProvidersName proves the two things
// schema 3 exists for are actually written.
func TestANewArchiveRecordsSchemaThreeAndItsProvidersName(t *testing.T) {
	t.Parallel()

	if archive.ArchiveSchemaVersion != 3 {
		t.Fatalf("archive schema version = %d, want 3", archive.ArchiveSchemaVersion)
	}
	if archive.MinReadableSchemaVersion != 2 {
		t.Fatalf("oldest readable manifest = %d; dropping a released format must be deliberate",
			archive.MinReadableSchemaVersion)
	}
}

// TestAnUnreadableManifestSchemaIsRefusedNotGuessed pins the behaviour that the
// readable-version range exists for. Manifest schema 3 is the first archive
// format Alih has shipped alongside an older one it still reads, so the two
// ends of that range are now compatibility promises: a version below the range
// was never released and a version above it was written by a build that knew
// something this one does not.
//
// Both must be refused explicitly. What must not happen is the archive being
// interpreted under whichever schema this build happens to implement, because
// that would let a newer archive be read with older meaning and reported as
// proven. The state and event readers are held to the same rule by
// internal/hardening.TestEverySchemaVersionIsPositiveAndRefusesTheFuture; the
// archive manifest is the one contract that test deliberately does not cover,
// because refusing it is verification's job rather than a decoder's.
func TestAnUnreadableManifestSchemaIsRefusedNotGuessed(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name   string
		schema int
	}{
		{"older than any released format", archive.MinReadableSchemaVersion - 1},
		{"newer than this build understands", archive.ArchiveSchemaVersion + 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			path := workingCopy(t, releasedArchive)
			manifestPath := filepath.Join(path, "manifest.json")
			content, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(content, &document); err != nil {
				t.Fatal(err)
			}
			document["schema_version"] = testCase.schema
			rewritten, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, rewritten, 0o600); err != nil {
				t.Fatal(err)
			}
			before := treeDigest(t, path)

			// Refusal must be a verification result rather than a panic or a
			// transport error, so the caller still gets a report to act on.
			report := verifyReleased(t, path)
			if report.Result != verify.ResultFailed {
				t.Fatalf("schema %d verified as %s; an unreadable format must fail closed",
					testCase.schema, report.Result)
			}

			// The reason must name the schema and the supported range. Without
			// it the operator cannot tell "this archive is damaged" from
			// "this Alih is the wrong version for this archive".
			var explained bool
			for _, check := range report.Checks {
				if check.Name != "manifest_integrity" {
					continue
				}
				for _, finding := range check.Findings {
					if strings.Contains(finding, "unsupported manifest schema_version") &&
						strings.Contains(finding, strconv.Itoa(testCase.schema)) &&
						strings.Contains(finding, strconv.Itoa(archive.MinReadableSchemaVersion)) &&
						strings.Contains(finding, strconv.Itoa(archive.ArchiveSchemaVersion)) {
						explained = true
					}
				}
			}
			if !explained {
				t.Errorf("schema %d was refused without naming the readable range: %#v",
					testCase.schema, report.Checks)
			}

			// Nothing downstream may claim to have proven anything about an
			// archive whose format was never understood.
			for _, check := range report.Checks {
				switch check.Name {
				case "count_reconciliation", "archive_metadata_consistency", "limitation_preservation":
					if check.Status == verify.CheckPass {
						t.Errorf("%s passed on an archive whose manifest schema was refused", check.Name)
					}
				}
			}

			// Refusing an archive must never be a reason to rewrite it.
			if after := treeDigest(t, path); !reflect.DeepEqual(before, after) {
				t.Error("refusing an unreadable manifest schema modified the archive")
			}
		})
	}
}
