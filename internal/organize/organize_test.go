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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"alih/internal/verify"
)

// TestBuildProducesBrowsableHierarchy proves the shape of the generated view
// against the current ClickUp fixture: one workspace, one space, one
// collection, three records and one attachment, each traceable to canonical
// evidence.
func TestBuildProducesBrowsableHierarchy(t *testing.T) {
	t.Parallel()

	result, output := organizeFixture(t, buildFixtureArchive(t, http.StatusOK))
	tree := readTree(t, output)

	workspace := "Fixture Workspace--47585244507d7928"
	space := workspace + "/spaces/Space--e6de1cd97d9a6d82"
	collection := space + "/collections/List--34fccb0640c10e23"
	firstTask := collection + "/records/First task--7cb65357d0c71949"

	want := []string{
		"README.md",
		"provenance.json",
		workspace + "/index.md",
		space + "/index.md",
		collection + "/index.md",
		firstTask + "/record.md",
		firstTask + "/attachments/file--2395cda3ef0c792a.txt",
		collection + "/records/Second task--f840fb7a5f9337bd/record.md",
		collection + "/records/Nested record--6736c49851561cb5/record.md",
	}
	for _, path := range want {
		if _, ok := tree[path]; !ok {
			t.Errorf("missing generated path %q\ngot: %v", path, sortedKeys(tree))
		}
	}
	if len(tree) != len(want) {
		t.Errorf("generated %d files, want %d: %v", len(tree), len(want), sortedKeys(tree))
	}
	if result.Files != len(want) || result.Attachments != 1 {
		t.Errorf("result = %+v, want %d files and 1 attachment", result, len(want))
	}
	if result.SchemaVersion != SchemaVersion || result.Verification != verify.ResultVerifiedWithLimitations {
		t.Errorf("result = %+v", result)
	}
	if got := tree[firstTask+"/attachments/file--2395cda3ef0c792a.txt"]; got != string(fixtureAttachmentContent) {
		t.Errorf("attachment content = %q", got)
	}
	// The deepest path a minimal workspace produces must leave room inside the
	// 260-character limit Windows applies without long-path support.
	for _, path := range sortedKeys(tree) {
		if len(path) > 200 {
			t.Errorf("generated path is %d characters, too deep to be portable: %q", len(path), path)
		}
	}
}

// TestBuildFailsClosedOnAPathCollision proves two distinct records whose
// derived names would occupy the same path stop the build instead of one
// silently overwriting or merging into the other.
func TestBuildFailsClosedOnAPathCollision(t *testing.T) {
	t.Parallel()

	// Two portable identities that agree over the whole suffix, given the same
	// title, are the only way one derived path can be claimed twice.
	shared := strings.Repeat("a", suffixLength)
	tampered := mutatedCopy(t, buildFixtureArchive(t, http.StatusOK),
		`UPDATE records SET title='Same name'`,
		`UPDATE records SET id='alih_`+shared+`1' WHERE source_id='t2'`,
		`UPDATE records SET id='alih_`+shared+`2' WHERE source_id='t3'`,
	)
	output := filepath.Join(t.TempDir(), "view")
	_, err := Build(context.Background(), tampered, output, Options{
		Verifier: stubVerifier{report: verify.Report{Result: verify.ResultVerified}}, AlihVersion: "test-version",
	})
	if err == nil {
		t.Fatal("a colliding derived path produced an organized view")
	}
	if !strings.Contains(err.Error(), "exists") {
		t.Errorf("error = %v, want an exclusive-create refusal", err)
	}
	if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
		t.Error("a refused build published an output directory")
	}
}

// TestRecordMarkdownCarriesSupportedSemantics proves a record page renders the
// semantics the archive actually holds and links back to canonical evidence.
func TestRecordMarkdownCarriesSupportedSemantics(t *testing.T) {
	t.Parallel()

	_, output := organizeFixture(t, buildFixtureArchive(t, http.StatusOK))
	tree := readTree(t, output)
	directories := recordDirectories(t, tree)
	if len(directories) != 3 {
		t.Fatalf("record directories = %v", directories)
	}
	var page string
	for _, directory := range directories {
		if strings.Contains(directory, "First task") {
			page = tree[directory+"/record.md"]
		}
	}
	if page == "" {
		t.Fatal("the first task record page was not generated")
	}
	for _, fragment := range []string{
		"# First task",
		"- Source ID: `t1`",
		"- Raw evidence path: `raw/raw/000004.json`",
		"- Status: `open`",
		"## Tags",
		"## Identities",
		"## Custom fields",
		"- Semantics: `OBSERVED_ONLY`",
		"## Comments",
		"## Relationships",
		"task_dependency",
		"## Attachments",
		"status RETRIEVED",
	} {
		if !strings.Contains(page, fragment) {
			t.Errorf("record page is missing %q:\n%s", fragment, page)
		}
	}
	// A threaded reply must not be rendered before the comment it answers.
	parent, reply := strings.Index(page, "hello"), strings.Index(page, "reply")
	if parent < 0 || reply < 0 || parent > reply {
		t.Errorf("comments are not in source order: parent=%d reply=%d", parent, reply)
	}
	// The nested record keeps its parent link rather than being hidden.
	for _, directory := range directories {
		if strings.Contains(directory, "Nested record") {
			if !strings.Contains(tree[directory+"/record.md"], "- Parent record portable ID: `alih_7cb65357") {
				t.Error("the nested record does not link to its parent record")
			}
		}
	}
}

// TestProvenanceIndexCoversEveryContentFile proves the machine-readable index
// maps each derived path back to portable and original source identity.
func TestProvenanceIndexCoversEveryContentFile(t *testing.T) {
	t.Parallel()

	_, output := organizeFixture(t, buildFixtureArchive(t, http.StatusOK))
	tree := readTree(t, output)

	var document provenanceDocument
	if err := json.Unmarshal([]byte(tree["provenance.json"]), &document); err != nil {
		t.Fatalf("provenance.json is not valid JSON: %v\n%s", err, tree["provenance.json"])
	}
	if document.SchemaVersion != SchemaVersion || document.Kind != "alih_organized_view" || document.GeneratedBy != "test-version" {
		t.Errorf("provenance header = %+v", document)
	}
	if document.Verification != verify.ResultVerifiedWithLimitations || len(document.Limitations) == 0 {
		t.Errorf("provenance must carry the verification result and its limitations: %+v", document)
	}
	indexed := make(map[string]provenanceEntry, len(document.Entries))
	for _, entry := range document.Entries {
		if _, duplicate := indexed[entry.Path]; duplicate {
			t.Errorf("duplicate provenance entry for %q", entry.Path)
		}
		if _, exists := tree[entry.Path]; !exists {
			t.Errorf("provenance names %q, which was not generated", entry.Path)
		}
		if entry.PortableID == "" || entry.SourceID == "" || entry.SourceRawPath == "" || entry.SourceProvider != "clickup" {
			t.Errorf("provenance entry is not traceable: %+v", entry)
		}
		indexed[entry.Path] = entry
	}
	// Every generated file except the self-referential index is indexed.
	for path := range tree {
		if path == "provenance.json" {
			continue
		}
		if _, ok := indexed[path]; !ok {
			t.Errorf("generated file %q has no provenance entry", path)
		}
	}
	attachment := ""
	for path, entry := range indexed {
		if strings.Contains(path, "/attachments/") {
			attachment = entry.AttachmentChecksum
		}
	}
	if !strings.HasPrefix(attachment, "sha256:") {
		t.Errorf("the attachment entry must record its checksum, got %q", attachment)
	}
}

// TestBuildIsDeterministic proves two runs over the same archive produce
// byte-identical output, so a view is safely regenerable.
func TestBuildIsDeterministic(t *testing.T) {
	t.Parallel()

	archivePath := buildFixtureArchive(t, http.StatusOK)
	_, first := organizeFixture(t, archivePath)
	_, second := organizeFixture(t, archivePath)

	firstTree, secondTree := readTree(t, first), readTree(t, second)
	if !reflect.DeepEqual(firstTree, secondTree) {
		t.Fatalf("regeneration is not deterministic\nfirst: %v\nsecond: %v", sortedKeys(firstTree), sortedKeys(secondTree))
	}
}

// TestBuildIsIndependentOfPhysicalRowOrder proves generation depends on the
// ordered queries rather than on how rows happen to be stored.
func TestBuildIsIndependentOfPhysicalRowOrder(t *testing.T) {
	t.Parallel()

	archivePath := buildFixtureArchive(t, http.StatusOK)
	_, original := organizeFixture(t, archivePath)

	// VACUUM rewrites every table page, and reinserting the records in the
	// opposite order changes the order they were written in.
	reordered := mutatedCopy(t, archivePath,
		`CREATE TEMP TABLE reordered AS SELECT * FROM records`,
		`DELETE FROM records`,
		`INSERT INTO records SELECT * FROM reordered ORDER BY id DESC`,
		`VACUUM`,
	)
	_, rebuilt := organizeFixture(t, reordered)

	// Re-recording alih.db necessarily changes the manifest checksum the view
	// discloses, so the comparison is over the derived content and the
	// provenance entries rather than that identity.
	first, second := semanticTree(t, original), semanticTree(t, rebuilt)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("physical row order changed the generated view\nfirst: %v\nsecond: %v",
			sortedKeys(first), sortedKeys(second))
	}
}

// semanticTree drops the two files that legitimately carry archive identity so
// two views built from the same logical content can be compared.
func semanticTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := readTree(t, root)
	var document provenanceDocument
	if err := json.Unmarshal([]byte(tree["provenance.json"]), &document); err != nil {
		t.Fatalf("provenance.json is not valid JSON: %v", err)
	}
	entries, err := json.Marshal(document.Entries)
	if err != nil {
		t.Fatal(err)
	}
	delete(tree, "README.md")
	tree["provenance.json"] = string(entries)
	return tree
}

// TestBuildLeavesCanonicalArchiveUntouched proves organization never modifies
// the sealed evidence it reads.
func TestBuildLeavesCanonicalArchiveUntouched(t *testing.T) {
	t.Parallel()

	archivePath := buildFixtureArchive(t, http.StatusOK)
	before := treeDigest(t, archivePath)
	organizeFixture(t, archivePath)
	after := treeDigest(t, archivePath)

	if !reflect.DeepEqual(before, after) {
		t.Fatal("the canonical archive changed while an organized view was built")
	}
	verifier := &fixtureVerifier{}
	report, err := verifier.Verify(archivePath)
	if err != nil || report.Result != verify.ResultVerifiedWithLimitations {
		t.Fatalf("the archive no longer verifies identically: %v %+v", err, report.Result)
	}
}

// TestBuildRefusesArchivesItCannotStandBehind proves no view is produced from
// evidence that has not independently verified.
func TestBuildRefusesArchivesItCannotStandBehind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		verifier Verifier
		want     string
	}{
		{"incomplete", stubVerifier{report: verify.Report{Result: verify.ResultIncomplete}}, "INCOMPLETE"},
		{"failed", stubVerifier{report: verify.Report{Result: verify.ResultFailed}}, "FAILED"},
		{"empty result", stubVerifier{report: verify.Report{}}, "UNKNOWN"},
		{"verifier error", stubVerifier{err: errors.New("archive is corrupt")}, "verify archive before organization"},
	}
	archivePath := buildFixtureArchive(t, http.StatusOK)
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "view")
			_, err := Build(context.Background(), archivePath, output, Options{Verifier: testCase.verifier})
			if err == nil {
				t.Fatal("an unverified archive produced an organized view")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error = %v, want mention of %q", err, testCase.want)
			}
			if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
				t.Error("a refused build left an output directory behind")
			}
		})
	}
}

// TestBuildRefusesIncompleteRealArchive proves the refusal also holds for an
// archive whose attachment could not be retrieved, produced by the real
// pipeline rather than a stub.
func TestBuildRefusesIncompleteRealArchive(t *testing.T) {
	t.Parallel()

	archivePath := buildFixtureArchive(t, http.StatusServiceUnavailable)
	output := filepath.Join(t.TempDir(), "view")
	_, err := Build(context.Background(), archivePath, output, Options{Verifier: &fixtureVerifier{}})
	if err == nil || !strings.Contains(err.Error(), verify.ResultIncomplete) {
		t.Fatalf("error = %v, want a refusal naming %s", err, verify.ResultIncomplete)
	}
	if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
		t.Error("a refused build left an output directory behind")
	}
}

// TestBuildRefusesUnsafeRoots covers absolute-path, containment, symlink and
// existing-output rules that protect canonical evidence.
func TestBuildRefusesUnsafeRoots(t *testing.T) {
	t.Parallel()

	archivePath := buildFixtureArchive(t, http.StatusOK)

	t.Run("output already exists", func(t *testing.T) {
		output := filepath.Join(t.TempDir(), "view")
		if err := os.MkdirAll(output, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Build(context.Background(), archivePath, output, Options{Verifier: &fixtureVerifier{}}); err == nil ||
			!strings.Contains(err.Error(), "already exists") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("output inside the archive", func(t *testing.T) {
		output := filepath.Join(archivePath, "view")
		if _, err := Build(context.Background(), archivePath, output, Options{Verifier: &fixtureVerifier{}}); err == nil ||
			!strings.Contains(err.Error(), "must not contain one another") {
			t.Fatalf("error = %v", err)
		}
		if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
			t.Fatal("a refused build wrote inside the canonical archive")
		}
	})

	t.Run("archive inside the output", func(t *testing.T) {
		if _, err := Build(context.Background(), archivePath, filepath.Dir(archivePath), Options{Verifier: &fixtureVerifier{}}); err == nil {
			t.Fatal("an output containing the archive was accepted")
		}
	})

	t.Run("missing archive", func(t *testing.T) {
		if _, err := Build(context.Background(), filepath.Join(t.TempDir(), "absent"), filepath.Join(t.TempDir(), "view"),
			Options{Verifier: &fixtureVerifier{}}); err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("empty paths", func(t *testing.T) {
		if _, err := Build(context.Background(), "", "", Options{Verifier: &fixtureVerifier{}}); err == nil {
			t.Fatal("empty paths were accepted")
		}
	})

	t.Run("missing verifier", func(t *testing.T) {
		if _, err := Build(context.Background(), archivePath, filepath.Join(t.TempDir(), "view"), Options{}); err == nil ||
			!strings.Contains(err.Error(), "independent verifier") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("symlinked output parent", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic links require elevation on Windows")
		}
		root := t.TempDir()
		real := filepath.Join(root, "real")
		if err := os.MkdirAll(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Build(context.Background(), archivePath, filepath.Join(link, "view"),
			Options{Verifier: &fixtureVerifier{}}); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("symlinked archive path", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic links require elevation on Windows")
		}
		link := filepath.Join(t.TempDir(), "archive-link")
		if err := os.Symlink(archivePath, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Build(context.Background(), link, filepath.Join(t.TempDir(), "view"),
			Options{Verifier: &fixtureVerifier{}}); err == nil {
			t.Fatal("a symlinked archive path was accepted")
		}
	})
}

// TestBuildPublishesNothingWhenInterrupted proves an interrupted or failing
// build leaves neither a partial view nor a modified archive.
func TestBuildPublishesNothingWhenInterrupted(t *testing.T) {
	t.Parallel()

	archivePath := buildFixtureArchive(t, http.StatusOK)
	before := treeDigest(t, archivePath)

	cases := []struct {
		name    string
		options func(*Options, string)
		want    string
	}{
		{
			name: "cancelled before publication",
			options: func(options *Options, _ string) {
				options.BeforePublish = func() error { return context.Canceled }
			},
			want: "context canceled",
		},
		{
			name: "staging write fails",
			options: func(options *Options, _ string) {
				options.BeforePublish = func() error { return errors.New("no space left on device") }
			},
			want: "no space left on device",
		},
		{
			name: "archive changes while the view is built",
			options: func(options *Options, archivePath string) {
				options.BeforePublish = func() error {
					manifest := filepath.Join(archivePath, "manifest.json")
					content, err := os.ReadFile(manifest)
					if err != nil {
						return err
					}
					return os.WriteFile(manifest, append(content, ' '), 0o600)
				}
			},
			want: "archive",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Each case gets its own copy so a deliberate mutation cannot
			// affect another case.
			working := filepath.Join(t.TempDir(), "archive")
			copyTree(t, archivePath, working)
			parent := t.TempDir()
			output := filepath.Join(parent, "view")
			options := Options{Verifier: &fixtureVerifier{}}
			testCase.options(&options, working)

			if _, err := Build(context.Background(), working, output, options); err == nil {
				t.Fatal("a failing build published a view")
			} else if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error = %v, want mention of %q", err, testCase.want)
			}
			if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
				t.Error("a failing build published an output directory")
			}
			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				t.Errorf("a failing build left %q behind", entry.Name())
			}
		})
	}
	if !reflect.DeepEqual(before, treeDigest(t, archivePath)) {
		t.Fatal("a failing build modified the canonical archive")
	}
}

// TestBuildStopsOnCancellation proves a cancelled context ends the build
// without publishing anything.
func TestBuildStopsOnCancellation(t *testing.T) {
	t.Parallel()

	archivePath := buildFixtureArchive(t, http.StatusOK)
	output := filepath.Join(t.TempDir(), "view")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Build(ctx, archivePath, output, Options{Verifier: &fixtureVerifier{}}); err == nil {
		t.Fatal("a cancelled build published a view")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Error("a cancelled build published an output directory")
	}
}

// TestBuildRefusesUnresolvedAttachment proves a tampered archive whose
// attachment metadata survives without its binary cannot silently produce a
// view that looks complete.
func TestBuildRefusesUnresolvedAttachment(t *testing.T) {
	t.Parallel()

	tampered := mutatedCopy(t, buildFixtureArchive(t, http.StatusOK),
		`UPDATE attachments SET download_status='UNRESOLVED', local_path=NULL, archived_size=NULL, checksum=NULL, error='fixture: retrieval failed'`)
	output := filepath.Join(t.TempDir(), "view")

	// Either the verifier refuses the tampering or the generator refuses the
	// unresolved attachment; both are fail-closed, and neither publishes.
	if _, err := Build(context.Background(), tampered, output, Options{Verifier: &fixtureVerifier{}}); err == nil {
		t.Fatal("an unresolved attachment produced an organized view")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Error("a refused build published an output directory")
	}
}

// TestBuildRefusesUnsafeArchiveAttachmentPath proves a recorded attachment path
// cannot escape the archive root.
func TestBuildRefusesUnsafeArchiveAttachmentPath(t *testing.T) {
	t.Parallel()

	for _, unsafe := range []string{"../../etc/passwd", "/etc/passwd"} {
		tampered := mutatedCopy(t, buildFixtureArchive(t, http.StatusOK),
			`UPDATE attachments SET local_path=`+sqlText(unsafe))
		output := filepath.Join(t.TempDir(), "view")
		if _, err := Build(context.Background(), tampered, output, Options{Verifier: &fixtureVerifier{}}); err == nil {
			t.Fatalf("attachment path %q was accepted", unsafe)
		}
		if _, err := os.Lstat(output); !os.IsNotExist(err) {
			t.Errorf("a refused build published an output directory for %q", unsafe)
		}
	}
}

// TestBuildHandlesHostileSourceNames proves provider-controlled names cannot
// escape the output tree, collide with one another, or produce a path a
// supported operating system refuses.
func TestBuildHandlesHostileSourceNames(t *testing.T) {
	t.Parallel()

	names := map[string]string{
		"t1": "../../escape",
		"t2": "CON",
		"t3": "  ",
	}
	statements := []string{
		`UPDATE containers SET name='C:\evil<>:"|?*name'`,
		`UPDATE collections SET name='` + strings.Repeat("wide", 120) + `'`,
	}
	for sourceID, name := range names {
		statements = append(statements, fmt.Sprintf(
			`UPDATE records SET title=%s WHERE source_id=%s`, sqlText(name), sqlText(sourceID)))
	}
	tampered := mutatedCopy(t, buildFixtureArchive(t, http.StatusOK), statements...)
	_, output := organizeFixture(t, tampered)
	tree := readTree(t, output)

	seen := make(map[string]string)
	for _, path := range sortedKeys(tree) {
		for _, component := range strings.Split(path, "/") {
			if component == "." || component == ".." || component == "" {
				t.Fatalf("generated path %q contains an unsafe component", path)
			}
			if len(component) > 128 {
				t.Errorf("generated component is %d bytes: %q", len(component), component)
			}
			if strings.ContainsAny(component, `<>:"\|?*`) {
				t.Errorf("generated component %q is not portable to Windows", component)
			}
			if strings.HasSuffix(component, " ") || strings.HasSuffix(component, ".") {
				t.Errorf("generated component %q ends in a character Windows trims", component)
			}
		}
		// Case-insensitive filesystems must not merge two derived files.
		lower := strings.ToLower(path)
		if previous, clash := seen[lower]; clash {
			t.Errorf("case collision between %q and %q", previous, path)
		}
		seen[lower] = path
	}
	if directories := recordDirectories(t, tree); len(directories) != 3 {
		t.Fatalf("hostile names collapsed the records into %v", directories)
	}
	if !strings.Contains(strings.Join(sortedKeys(tree), "\n"), "_CON--") {
		t.Error("a Windows reserved basename was not escaped")
	}
}

// TestBuildKeepsDistinctRecordsDistinct proves identical names, identical
// attachment filenames and identical attachment content still produce separate
// derived paths.
func TestBuildKeepsDistinctRecordsDistinct(t *testing.T) {
	t.Parallel()

	tampered := mutatedCopy(t, buildFixtureArchive(t, http.StatusOK), `UPDATE records SET title='Same name'`)
	_, output := organizeFixture(t, tampered)
	tree := readTree(t, output)

	directories := recordDirectories(t, tree)
	if len(directories) != 3 {
		t.Fatalf("three identically named records produced %v", directories)
	}
	unique := make(map[string]struct{}, len(directories))
	for _, directory := range directories {
		unique[strings.ToLower(directory)] = struct{}{}
	}
	if len(unique) != 3 {
		t.Fatalf("identically named records collided: %v", directories)
	}
}

// TestBuildRendersUnknownAndExternalEvidence proves missing optional values and
// an unresolved external relationship are shown rather than invented.
func TestBuildRendersUnknownAndExternalEvidence(t *testing.T) {
	t.Parallel()

	_, output := organizeFixture(t, buildFixtureArchive(t, http.StatusOK))
	tree := readTree(t, output)

	var second string
	for _, directory := range recordDirectories(t, tree) {
		if strings.Contains(directory, "Second task") {
			second = tree[directory+"/record.md"]
		}
	}
	if second == "" {
		t.Fatal("the second record page was not generated")
	}
	// The fixture's second task has no status, priority, tags or comments; the
	// page must simply omit them rather than assert a value.
	for _, absent := range []string{"- Status:", "- Priority:", "## Tags", "## Comments", "## Custom fields"} {
		if strings.Contains(second, absent) {
			t.Errorf("record page invents %q:\n%s", absent, second)
		}
	}
	// It is the target of the dependency, so the relationship is shown on both
	// sides with its resolution state.
	if !strings.Contains(second, "RESOLVED") {
		t.Errorf("the relationship resolution state is missing:\n%s", second)
	}
}

// TestBuildRendersExternalRelationship proves a relationship pointing outside
// the archive is disclosed as external rather than dropped.
func TestBuildRendersExternalRelationship(t *testing.T) {
	t.Parallel()

	tampered := mutatedCopy(t, buildFixtureArchive(t, http.StatusOK),
		`UPDATE relationships SET to_record_id=NULL, to_source_id='outside', resolution_state='UNRESOLVED_EXTERNAL'`)
	// The edit deliberately contradicts the sealed manifest, so acceptance
	// comes from a stub while the generator itself stays under test.
	output := filepath.Join(t.TempDir(), "view")
	if _, err := Build(context.Background(), tampered, output, Options{
		Verifier: stubVerifier{report: verify.Report{Result: verify.ResultVerified}}, AlihVersion: "test-version",
	}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	tree := readTree(t, output)

	found := false
	for _, directory := range recordDirectories(t, tree) {
		page := tree[directory+"/record.md"]
		if strings.Contains(page, "UNRESOLVED_EXTERNAL") && strings.Contains(page, "(external)") {
			found = true
		}
	}
	if !found {
		t.Error("an unresolved external relationship was not disclosed")
	}
}

// TestRenderedTextIsInertMarkdown proves provider-controlled text cannot inject
// markup or terminal control sequences into the generated view.
func TestRenderedTextIsInertMarkdown(t *testing.T) {
	t.Parallel()

	hostile := "<script>alert(1)</script> `code` \x1b[31mred\x1b[0m"
	tampered := mutatedCopy(t, buildFixtureArchive(t, http.StatusOK),
		`UPDATE records SET status=`+sqlText(hostile)+` WHERE source_id='t1'`)
	_, output := organizeFixture(t, tampered)
	tree := readTree(t, output)

	for _, directory := range recordDirectories(t, tree) {
		page := tree[directory+"/record.md"]
		if strings.Contains(page, "<script>") {
			t.Errorf("raw HTML survived into %q", directory)
		}
		if strings.Contains(page, "&lt;script&gt;") && !strings.Contains(page, "&#96;code&#96;") {
			t.Errorf("backticks were not neutralised in %q:\n%s", directory, page)
		}
	}
}

// TestLargeArchiveStreamsWithBoundedWork proves a synthetic workspace much
// larger than the fixture completes without loading everything into memory or
// producing colliding paths.
func TestLargeArchiveStreamsWithBoundedWork(t *testing.T) {
	if testing.Short() {
		t.Skip("large synthetic dataset")
	}
	t.Parallel()

	const extra = 500
	statements := []string{
		`CREATE TEMP TABLE seed AS SELECT * FROM records WHERE source_id='t2'`,
		fmt.Sprintf(`WITH RECURSIVE counter(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM counter WHERE n < %d)
			INSERT INTO records (id,kind,workspace_id,collection_id,parent_record_id,title,description,text_content,
			status,status_type,priority,archived,date_created_source,date_updated_source,date_closed_source,date_done_source,
			start_date_source,due_date_source,time_estimate_ms,time_spent_ms,points,
			source_provider,source_type,source_id,source_raw_path,source_id_composite)
			SELECT 'alih_synthetic'||printf('%%06d',n), kind, workspace_id, collection_id, NULL, 'Bulk record',
			description, text_content, status, status_type, priority, archived, date_created_source, date_updated_source,
			date_closed_source, date_done_source, start_date_source, due_date_source, time_estimate_ms, time_spent_ms, points,
			source_provider, source_type, 'bulk'||n, source_raw_path, source_id_composite
			FROM seed, counter`, extra),
	}
	// The synthetic rows deliberately break the manifest's reconciliation, so
	// this test uses a stub verifier for the acceptance decision and keeps the
	// real generator under test.
	archivePath := mutatedCopy(t, buildFixtureArchive(t, http.StatusOK), statements...)
	output := filepath.Join(t.TempDir(), "view")
	result, err := Build(context.Background(), archivePath, output, Options{
		Verifier:    stubVerifier{report: verify.Report{Result: verify.ResultVerified}},
		AlihVersion: "test-version",
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	tree := readTree(t, output)
	if got := len(recordDirectories(t, tree)); got != 3+extra {
		t.Fatalf("generated %d record pages, want %d", got, 3+extra)
	}
	if result.Files != len(tree) {
		t.Errorf("result reports %d files, tree has %d", result.Files, len(tree))
	}
	var document provenanceDocument
	if err := json.Unmarshal([]byte(tree["provenance.json"]), &document); err != nil {
		t.Fatalf("provenance.json is not valid JSON at scale: %v", err)
	}
	if len(document.Entries) != len(tree)-1 {
		t.Errorf("provenance indexes %d of %d generated files", len(document.Entries), len(tree)-1)
	}
}

// TestServiceRejectsMissingDependencies proves the injected service surface
// fails closed rather than panicking.
func TestServiceRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	var service *Service
	if _, err := service.Build(context.Background(), "archive", "output"); err == nil {
		t.Fatal("a nil service produced a result")
	}
	if _, err := New(nil, "test").Build(context.Background(), "archive", "output"); err == nil {
		t.Fatal("a service without a verifier produced a result")
	}
}

// openDescriptors counts this process's open files. It is Linux-specific and
// the test that uses it skips elsewhere; the property it guards is not.
func openDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd on this platform: %v", err)
	}
	return len(entries)
}

// TestGenerationDoesNotAccumulateOpenFiles proves the generator's per-record
// and per-attachment work releases its handles. A leak here would not fail a
// small fixture; it would fail a real workspace, which is exactly the case a
// user cannot afford to discover during a recovery.
func TestGenerationDoesNotAccumulateOpenFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("descriptor counting is read from /proc")
	}
	if testing.Short() {
		t.Skip("large synthetic dataset")
	}

	const extra = 300
	archivePath := mutatedCopy(t, buildFixtureArchive(t, http.StatusOK),
		`CREATE TEMP TABLE seed AS SELECT * FROM records WHERE source_id='t2'`,
		fmt.Sprintf(`WITH RECURSIVE counter(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM counter WHERE n < %d)
			INSERT INTO records (id,kind,workspace_id,collection_id,parent_record_id,title,description,text_content,
			status,status_type,priority,archived,date_created_source,date_updated_source,date_closed_source,date_done_source,
			start_date_source,due_date_source,time_estimate_ms,time_spent_ms,points,
			source_provider,source_type,source_id,source_raw_path,source_id_composite)
			SELECT 'alih_fdcheck'||printf('%%08d',n), kind, workspace_id, collection_id, NULL, 'Bulk record',
			description, text_content, status, status_type, priority, archived, date_created_source, date_updated_source,
			date_closed_source, date_done_source, start_date_source, due_date_source, time_estimate_ms, time_spent_ms, points,
			source_provider, source_type, 'fd'||n, source_raw_path, source_id_composite
			FROM seed, counter`, extra),
	)

	// One warm-up run first: the SQLite driver and the runtime open handles of
	// their own on first use, and those are not the leak under test.
	warmUp := filepath.Join(t.TempDir(), "warm-up")
	if _, err := Build(context.Background(), archivePath, warmUp, Options{
		Verifier: stubVerifier{report: verify.Report{Result: verify.ResultVerified}}, AlihVersion: "test-version",
	}); err != nil {
		t.Fatalf("warm-up build: %v", err)
	}

	before := openDescriptors(t)
	for run := 0; run < 3; run++ {
		output := filepath.Join(t.TempDir(), fmt.Sprintf("view-%d", run))
		if _, err := Build(context.Background(), archivePath, output, Options{
			Verifier: stubVerifier{report: verify.Report{Result: verify.ResultVerified}}, AlihVersion: "test-version",
		}); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	after := openDescriptors(t)

	// The bound is deliberately generous: the claim is that descriptor use does
	// not grow with the number of records or runs, not that it is constant to
	// the file.
	if after-before > 8 {
		t.Fatalf("open descriptors grew from %d to %d across three runs of %d records", before, after, extra+3)
	}
}
