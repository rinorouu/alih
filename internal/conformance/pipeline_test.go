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

package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/organize"
	"alih/internal/report"
	"alih/internal/snapshot"
	"alih/internal/verify"
)

func attachmentClient(status int) *http.Client {
	return &http.Client{Transport: fakeRoundTrip(func(request *http.Request) (*http.Response, error) {
		body := []byte(fakeAttachment)
		if status != http.StatusOK {
			body = nil
		}
		return &http.Response{
			StatusCode: status, Header: make(http.Header),
			Body: io.NopCloser(bytes.NewReader(body)), Request: request,
		}, nil
	})}
}

// buildFakeArchive walks the paper adapter through authenticate, scan, extract
// (M3), normalize, and archive (M4) using the real Core implementations.
func buildFakeArchive(t *testing.T, attachmentStatus int) (string, *fakeConnector) {
	t.Helper()
	fake := newFakeConnector()
	root := t.TempDir()

	authentication, err := fake.Authenticate(context.Background(), "example-credential")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	workspace := authentication.Workspaces[0]

	snapshotPath := filepath.Join(root, "m3")
	session, err := snapshot.Begin(snapshotPath, fake.Name(), workspace, authentication.Identity)
	if err != nil {
		t.Fatalf("begin snapshot: %v", err)
	}
	extraction, err := fake.Extract(context.Background(), "example-credential", workspace, session)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := session.Complete(extraction); err != nil {
		t.Fatalf("complete snapshot: %v", err)
	}
	evidence, err := snapshot.LoadComplete(snapshotPath)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	portable, err := fake.NormalizeSnapshot(evidence)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	target := filepath.Join(root, "archive")
	if _, err := archive.Build(context.Background(), evidence, portable, target, archive.Options{
		HTTPClient:           attachmentClient(attachmentStatus),
		Sleep:                func(context.Context, time.Duration) error { return nil },
		ConnectorDisplayName: fake.DisplayName(),
	}); err != nil {
		t.Fatalf("build archive: %v", err)
	}
	return target, fake
}

// TestAForeignConnectorTraversesTheEvidencePipeline is the core reuse
// question for M3 and M4: can a provider Core has never heard of produce a
// sealed archive using nothing but the published interfaces?
func TestAForeignConnectorTraversesTheEvidencePipeline(t *testing.T) {
	t.Parallel()

	archivePath, _ := buildFakeArchive(t, http.StatusOK)

	for _, name := range []string{"alih.db", "manifest.json", "schema.json"} {
		if _, err := os.Stat(filepath.Join(archivePath, name)); err != nil {
			t.Errorf("the archive is missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(archivePath, "raw", "run.json")); err != nil {
		t.Errorf("raw evidence was not sealed into the archive: %v", err)
	}
}

// TestAForeignConnectorVerifiesThroughCore proves the M5 verifier reaches a
// clean result for a connector it has no knowledge of, and that connector
// field semantics are supplied by the adapter rather than known to Core.
func TestAForeignConnectorVerifiesThroughCore(t *testing.T) {
	t.Parallel()

	archivePath, _ := buildFakeArchive(t, http.StatusOK)

	report, err := verify.Archive(archivePath, verify.Options{FieldSemantics: fakeFieldSemantics{}})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.Failed() {
		t.Fatalf("result = %s\nchecks: %#v", report.Result, report.Checks)
	}
	if report.Connector != fakeConnectorName {
		t.Errorf("verification reports connector %q", report.Connector)
	}
	// The adapter's own semantics were applied, so the enumerated selection was
	// proven rather than left unproven.
	var fieldCheck *verify.Check
	for index := range report.Checks {
		if strings.Contains(report.Checks[index].Name, "field") {
			fieldCheck = &report.Checks[index]
		}
	}
	if fieldCheck == nil {
		t.Fatal("no field-semantics check ran for the foreign connector")
	}
	if fieldCheck.Status != verify.CheckPass {
		t.Errorf("field semantics check = %s %v", fieldCheck.Status, fieldCheck.Findings)
	}
}

// TestAForeignConnectorFailsClosedOnAMissingRequiredCapability proves the
// fail-closed gate is Core's, not ClickUp's: a foreign connector that cannot
// retrieve attachment content cannot produce a clean archive either.
func TestAForeignConnectorFailsClosedOnAMissingRequiredCapability(t *testing.T) {
	t.Parallel()

	archivePath, _ := buildFakeArchive(t, http.StatusServiceUnavailable)

	result, err := verify.Archive(archivePath, verify.Options{FieldSemantics: fakeFieldSemantics{}})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if result.Result != verify.ResultIncomplete {
		t.Fatalf("result = %s, want %s", result.Result, verify.ResultIncomplete)
	}
	if !result.Failed() {
		t.Fatal("a foreign connector's incomplete archive was reported as passing")
	}
}

// TestAForeignConnectorProducesARecoveryReport proves M6 renders a connector
// it does not know, and does not attribute the archive to ClickUp.
func TestAForeignConnectorProducesARecoveryReport(t *testing.T) {
	t.Parallel()

	archivePath, _ := buildFakeArchive(t, http.StatusOK)
	verification, err := verify.Archive(archivePath, verify.Options{FieldSemantics: fakeFieldSemantics{}})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	manifest := readArchiveManifest(t, archivePath)
	document := report.Build(report.Inputs{
		ArchivePath: archivePath, Manifest: manifest, ManifestAvailable: true,
		Verification: verification, GeneratedAt: fakeClock(), AlihVersion: "test-version",
	})
	if document.Failed() {
		t.Fatalf("report conclusion = %s", document.Conclusion.Result)
	}
	if document.Identity.Connector != fakeConnectorName {
		t.Errorf("the report attributes the archive to %q", document.Identity.Connector)
	}
	var rendered bytes.Buffer
	if err := report.RenderText(&rendered, document); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.String(), fakeConnectorName) {
		t.Errorf("the report does not name the connector that produced the archive:\n%s", rendered.String())
	}
}

// TestARecoveryReportNamesTheConnectorThatProducedTheArchive proves the
// report's prose follows the archive rather than whichever connector this build
// happens to ship. This was blocker B2: the recovery statements named ClickUp
// as a literal, so an archive from anywhere else was described with the wrong
// provider's name.
func TestARecoveryReportNamesTheConnectorThatProducedTheArchive(t *testing.T) {
	t.Parallel()

	archivePath, fake := buildFakeArchive(t, http.StatusOK)
	verification, err := verify.Archive(archivePath, verify.Options{FieldSemantics: fakeFieldSemantics{}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := readArchiveManifest(t, archivePath)
	if manifest.ConnectorDisplayName != fake.DisplayName() {
		t.Fatalf("the archive sealed display name %q, want %q", manifest.ConnectorDisplayName, fake.DisplayName())
	}
	document := report.Build(report.Inputs{
		ArchivePath: archivePath, Manifest: manifest, ManifestAvailable: true,
		Verification: verification, GeneratedAt: fakeClock(), AlihVersion: "test-version",
	})
	var rendered bytes.Buffer
	if err := report.RenderText(&rendered, document); err != nil {
		t.Fatal(err)
	}
	text := rendered.String()

	if strings.Contains(text, "ClickUp") {
		t.Errorf("the report for a foreign connector still names ClickUp:\n%s", text)
	}
	if !strings.Contains(text, fake.DisplayName()) {
		t.Errorf("the report does not name %q anywhere:\n%s", fake.DisplayName(), text)
	}
	// No placeholder may survive into what a person reads.
	if strings.Contains(text, "{connector}") {
		t.Errorf("an unresolved connector placeholder reached the report:\n%s", text)
	}
	// The recovery statements specifically must carry the right name.
	var named bool
	for _, statement := range document.Recovery {
		if strings.Contains(statement.Claim, "ClickUp") {
			t.Errorf("recovery statement names ClickUp: %q", statement.Claim)
		}
		if strings.Contains(statement.Claim, fake.DisplayName()) {
			named = true
		}
	}
	if !named {
		t.Error("no recovery statement names the connector that produced the archive")
	}
}

// TestAForeignConnectorCanBeOrganized proves the newest capability in the
// foundation is provider-neutral too.
func TestAForeignConnectorCanBeOrganized(t *testing.T) {
	t.Parallel()

	archivePath, _ := buildFakeArchive(t, http.StatusOK)
	output := filepath.Join(t.TempDir(), "view")

	result, err := organize.Build(context.Background(), archivePath, output, organize.Options{
		Verifier: foreignVerifier{}, AlihVersion: "test-version",
	})
	if err != nil {
		t.Fatalf("organize: %v", err)
	}
	if result.Files == 0 || result.Attachments != 1 {
		t.Fatalf("result = %+v", result)
	}
	// The view is browsable and traceable, which is the reuse question. The
	// vocabulary it uses is blocker B1, pinned separately below.
	if _, err := os.Stat(filepath.Join(output, "provenance.json")); err != nil {
		t.Errorf("no provenance index was written for a foreign connector: %v", err)
	}
}

// TestAConnectorKeepsItsOwnVocabularyEndToEnd proves what blocker B1 used to
// prevent: a connector describes its objects in its own words, and those words
// survive the M3 index, the portable model, the sealed manifest, verification,
// and the organized view.
//
// Previously two Core rules contradicted each other — verification required a
// container's kind to equal its source type, while the archive writer and the
// reconciler counted only ClickUp's words — so any other connector had to
// rename its concepts before Core would accept them.
func TestAConnectorKeepsItsOwnVocabularyEndToEnd(t *testing.T) {
	t.Parallel()

	archivePath, _ := buildFakeArchive(t, http.StatusOK)

	// The sealed manifest records the connector's vocabulary beside the neutral
	// totals, and nothing in it mentions another provider's words.
	manifest := readArchiveManifest(t, archivePath)
	for _, key := range []string{"container:section", "record:item", "record:nested_item"} {
		if _, present := manifest.Inventory[key]; !present {
			t.Errorf("the manifest does not record %q: %v", key, inventoryKeys(manifest.Inventory))
		}
	}
	for _, foreign := range []string{"spaces", "folders", "lists", "tasks", "subtasks"} {
		if _, present := manifest.Inventory[foreign]; present {
			t.Errorf("the manifest still records another provider's entity %q", foreign)
		}
	}
	for _, neutral := range []string{"containers", "collections", "records", "nested_records"} {
		if _, present := manifest.Inventory[neutral]; !present {
			t.Errorf("the manifest does not record the neutral total %q", neutral)
		}
	}

	// Verification accepts it, which is what the contradiction used to prevent.
	verification, err := verify.Archive(archivePath, verify.Options{FieldSemantics: fakeFieldSemantics{}})
	if err != nil {
		t.Fatal(err)
	}
	if verification.Failed() {
		t.Fatalf("an archive in the connector's own vocabulary did not verify: %s\n%#v",
			verification.Result, verification.Checks)
	}

	// And the browsable view uses those words too.
	output := filepath.Join(t.TempDir(), "view")
	if _, err := organize.Build(context.Background(), archivePath, output, organize.Options{
		Verifier: foreignVerifier{}, AlihVersion: "test-version",
	}); err != nil {
		t.Fatal(err)
	}
	var directories []string
	if err := filepath.WalkDir(output, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, entry.Name())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(directories, " ")
	if !strings.Contains(joined, "sections") {
		t.Errorf("the organized view lost the connector's container vocabulary: %v", directories)
	}
	if strings.Contains(joined, "spaces") {
		t.Errorf("the organized view used another provider's vocabulary: %v", directories)
	}
}

func inventoryKeys(inventory map[string]archive.EntityCount) []string {
	keys := make([]string, 0, len(inventory))
	for key := range inventory {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// readArchiveManifest reads the sealed manifest the foreign connector produced.
func readArchiveManifest(t *testing.T, archivePath string) archive.Manifest {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(archivePath, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest archive.Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// foreignVerifier verifies with the foreign connector's own field semantics.
type foreignVerifier struct{}

func (foreignVerifier) Verify(path string) (verify.Report, error) {
	return verify.Archive(path, verify.Options{FieldSemantics: fakeFieldSemantics{}})
}

// TestCapabilityAndHealthAreProviderNeutral proves the two contracts Stage 1
// and Stage 2 introduced accept a connector Core has never seen.
func TestCapabilityAndHealthAreProviderNeutral(t *testing.T) {
	t.Parallel()

	fake := newFakeConnector()
	contract := fake.CapabilityContract()
	if err := connector.ValidateCapabilityContract(contract); err != nil {
		t.Fatalf("a foreign capability contract was rejected: %v", err)
	}
	authentication, err := fake.Authenticate(context.Background(), "example-credential")
	if err != nil {
		t.Fatal(err)
	}
	assessment := authentication.Assessment
	if err := connector.ValidateOperationalAssessment(assessment); err != nil {
		t.Fatalf("a foreign operational assessment was rejected: %v", err)
	}
	var human, machine bytes.Buffer
	if err := connector.WriteOperationalAssessmentText(&human, assessment); err != nil {
		t.Fatal(err)
	}
	if err := connector.WriteOperationalAssessmentJSON(&machine, assessment); err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{human.String(), machine.String()} {
		if strings.Contains(rendered, "ClickUp") {
			t.Errorf("a health renderer names ClickUp for a foreign connector:\n%s", rendered)
		}
		if !strings.Contains(rendered, fakeConnectorName) {
			t.Errorf("a health renderer does not name the foreign connector:\n%s", rendered)
		}
	}
}

// TestAnUnauthenticatedForeignConnectorIsRefused proves the failure paths are
// Core's too, not something each adapter reimplements.
func TestAnUnauthenticatedForeignConnectorIsRefused(t *testing.T) {
	t.Parallel()

	fake := newFakeConnector()
	if _, err := fake.Authenticate(context.Background(), "   "); err == nil {
		t.Fatal("an empty credential authenticated")
	}
}
