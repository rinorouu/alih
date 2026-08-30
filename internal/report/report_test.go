// Copyright 2026 rinorouu
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

package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/verify"
)

var (
	generatedAt = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	// The live audit case: the source was read at 01:54 and the archive was
	// only finished 37m32s later.
	snapshotCompletedAt = time.Date(2026, 8, 30, 1, 54, 42, 0, time.UTC)
	archiveCompletedAt  = time.Date(2026, 8, 30, 2, 32, 14, 0, time.UTC)
)

func stringPointer(value string) *string { return &value }
func int64Pointer(value int64) *int64    { return &value }

// healthyInputs models an archive that passed verification and whose source
// still has capabilities Alih cannot establish.
func healthyInputs() Inputs {
	return Inputs{
		ArchivePath: "/archives/example",
		Manifest: archive.Manifest{
			SchemaVersion: archive.ArchiveSchemaVersion, AlihVersion: "0.0.1", Status: archive.StatusCreatedUnverified,
			SourceSnapshotCompletedAt: snapshotCompletedAt, ArchiveCompletedAt: &archiveCompletedAt,
			Connector:     "clickup",
			Source:        connector.Workspace{ID: "w1", Name: "Example Workspace"},
			InputSnapshot: archive.InputSnapshot{LogicalDigest: "sha256:abc", Status: "COMPLETE", Atomic: false},
			Files:         []archive.FileRecord{{Path: "alih.db"}, {Path: "schema.json"}},
			Limitations:   []string{"Custom Field values are observed values only."},
		},
		ManifestAvailable: true,
		Verification:      healthyVerification(),
		GeneratedAt:       generatedAt,
		AlihVersion:       "0.0.1",
	}
}

func healthyVerification() verify.Report {
	checks := []verify.Check{}
	for _, name := range []string{
		"archive_structure", "manifest_integrity", "manifest_file_inventory", "file_checksums",
		"sqlite_integrity", "raw_evidence_integrity", "schema_consistency", "archive_metadata_consistency",
		"portable_identifier_derivation", "referential_integrity", "attachment_integrity",
		"custom_field_evidence", "discrepancy_reconciliation", "source_object_reconciliation",
		"hierarchy_reconstruction", "raw_evidence_references", "count_reconciliation",
	} {
		checks = append(checks, verify.Check{Name: name, Status: verify.CheckPass, Summary: name + " passed"})
	}
	checks = append(checks,
		verify.Check{Name: "source_consistency_scope", Status: verify.CheckUnproven, Summary: "Point-in-time consistency is not claimed."},
		verify.Check{Name: "limitation_preservation", Status: verify.CheckUnproven, Summary: "2 source capabilities are not fully supported."},
	)
	return verify.Report{
		ArchivePath: "/archives/example", Result: verify.ResultVerifiedWithLimitations,
		ArchiveStatus: archive.StatusCreatedUnverified, Connector: "clickup",
		Source: connector.Workspace{ID: "w1", Name: "Example Workspace"},
		Checks: checks,
		Reconciliation: []verify.Reconciliation{
			{Entity: "tasks", Expected: 6, Archived: 6, Status: verify.CheckPass},
			{Entity: "attachments", Expected: 1, Archived: 1, Status: verify.CheckPass},
		},
		Capabilities: []connector.Capability{
			{Name: "Tasks/subtasks", State: connector.CapabilitySupported, Note: "home-List tasks"},
			{Name: "Docs", State: connector.CapabilityPartial, Note: "outside M2 inventory"},
			{Name: "Whiteboards", State: connector.CapabilityUnknown, Note: "not established by M2"},
			{Name: "Legacy exports", State: connector.CapabilityUnavailable, Note: "API does not expose it"},
		},
		Limitations: []string{"Point-in-time consistency is not claimed."},
		NotProven:   []string{"2 source capabilities are not fully supported."},
	}
}

func renderBoth(t *testing.T, document Document) string {
	t.Helper()
	var text, html bytes.Buffer
	if err := RenderText(&text, document); err != nil {
		t.Fatalf("RenderText() error = %v", err)
	}
	if err := RenderHTML(&html, document); err != nil {
		t.Fatalf("RenderHTML() error = %v", err)
	}
	if !strings.Contains(html.String(), "<!doctype html>") {
		t.Fatal("HTML report is not a complete document")
	}
	for _, external := range []string{"http://", "https://", "src=", "<script"} {
		if strings.Contains(html.String(), external) {
			t.Fatalf("HTML report is not self-contained: contains %q", external)
		}
	}
	return text.String() + "\n" + html.String()
}

func TestReportFromVerifiedWithLimitationsArchiveStatesScopeWithoutOverclaiming(t *testing.T) {
	t.Parallel()

	document := Build(healthyInputs())
	rendered := renderBoth(t, document)

	if document.Conclusion.Result != verify.ResultVerifiedWithLimitations {
		t.Fatalf("conclusion result = %s", document.Conclusion.Result)
	}
	if document.Failed() {
		t.Fatal("a verified archive was reported as failed")
	}
	if !strings.Contains(document.Conclusion.Verdict, "proven within Alih's supported scope") {
		t.Fatalf("verdict = %q", document.Conclusion.Verdict)
	}
	// A passing archive must never carry failure language.
	for _, forbidden := range []string{
		"verification did not pass",
		"Do not rely on it for recovery",
		"complete copy",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("a verified archive's report contains failure language %q", forbidden)
		}
	}
	for _, statement := range document.Recovery {
		if !statement.Proven {
			t.Errorf("healthy archive left recovery claim unproven: %q (%s)", statement.Claim, statement.Reason)
		}
	}
	for _, expected := range []string{
		"1. ARCHIVE IDENTITY", "2. VERIFICATION STATUS", "3. RECOVERY SUMMARY",
		"4. ENTITY COVERAGE", "5. ATTACHMENTS", "6. CAPABILITY COVERAGE",
		"7. LIMITATIONS AND UNPROVEN CLAIMS", "8. DISCREPANCIES AND UNRESOLVED ITEMS",
		"9. RECOVERY CONCLUSION",
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("report is missing required section %q", expected)
		}
	}
}

func TestReportPreservesCapabilityStatesExactly(t *testing.T) {
	t.Parallel()

	document := Build(healthyInputs())
	rendered := renderBoth(t, document)

	states := map[string]string{}
	for _, capability := range document.Capabilities {
		states[capability.Name] = capability.State
	}
	want := map[string]string{
		"Tasks/subtasks": "SUPPORTED", "Docs": "PARTIAL",
		"Whiteboards": "UNKNOWN", "Legacy exports": "UNAVAILABLE",
	}
	for name, state := range want {
		if states[name] != state {
			t.Errorf("capability %q reported as %q, want %q", name, states[name], state)
		}
	}
	// UNKNOWN must not be read as empty, and PARTIAL must not become supported.
	for _, capability := range document.Capabilities {
		switch capability.State {
		case "UNKNOWN":
			if !strings.Contains(capability.RecoveryMeaning, "Cannot be assessed") ||
				!strings.Contains(capability.RecoveryMeaning, "not evidence that it is absent") {
				t.Errorf("UNKNOWN capability %q was not stated as unassessable: %q", capability.Name, capability.RecoveryMeaning)
			}
		case "PARTIAL":
			if !strings.Contains(capability.RecoveryMeaning, "Only partially represented") {
				t.Errorf("PARTIAL capability %q was softened: %q", capability.Name, capability.RecoveryMeaning)
			}
		}
	}
	for _, name := range []string{"Docs", "Whiteboards", "Legacy exports"} {
		if !strings.Contains(rendered, "Do not claim \""+name+"\" is recoverable") &&
			!strings.Contains(rendered, "Do not claim &#34;"+name+"&#34; is recoverable") {
			t.Errorf("non-SUPPORTED capability %q is not listed as unclaimable", name)
		}
	}
}

func TestReportDisclosesNonAtomicSource(t *testing.T) {
	t.Parallel()

	document := Build(healthyInputs())
	rendered := renderBoth(t, document)

	if document.Identity.SourceSnapshotAtomic {
		t.Fatal("a non-atomic snapshot was reported as atomic")
	}
	if !strings.Contains(rendered, "NON-ATOMIC") {
		t.Error("the non-atomic source snapshot is not visible in the report")
	}
	if !containsAny(document.MustNotClaim, "point-in-time consistent snapshot") {
		t.Errorf("point-in-time consistency was not excluded: %#v", document.MustNotClaim)
	}
	if !containsAny(document.Conclusion.Statements, "non-atomic") {
		t.Errorf("the conclusion does not disclose the non-atomic source: %#v", document.Conclusion.Statements)
	}

	atomic := healthyInputs()
	atomic.Manifest.InputSnapshot.Atomic = true
	if containsAny(Build(atomic).MustNotClaim, "point-in-time consistent snapshot") {
		t.Error("an atomic snapshot still carried the non-atomic exclusion")
	}
}

func TestReportNeverClaimsExecutableComputedFieldSemantics(t *testing.T) {
	t.Parallel()

	document := Build(healthyInputs())
	if !containsAny(document.MustNotClaim, "formulas, rollups and other computed fields are not reconstructable") {
		t.Fatalf("computed-field semantics were not excluded: %#v", document.MustNotClaim)
	}
	rendered := renderBoth(t, document)
	if strings.Contains(rendered, "formula semantics are reconstructable") {
		t.Fatal("the report claimed computed-field semantics")
	}
}

func TestReportFromIncompleteArchiveNamesTheMissingData(t *testing.T) {
	t.Parallel()

	inputs := healthyInputs()
	inputs.Manifest.Status = archive.StatusIncomplete
	inputs.Manifest.Attachments = []archive.AttachmentRecord{{
		ID: "alih_a1", SourceID: "a1", RecordID: "alih_t1",
		Filename: stringPointer("design.png"), ExpectedSize: int64Pointer(4096),
		Status: "UNRESOLVED", Error: stringPointer("attachment endpoint returned HTTP 503"),
	}}
	inputs.Manifest.Discrepancies = []archive.Discrepancy{{
		Kind: "ATTACHMENT_UNRESOLVED", SourceID: "a1", Message: "attachment endpoint returned HTTP 503",
	}}
	inputs.Verification.Result = verify.ResultIncomplete
	inputs.Verification.ArchiveStatus = archive.StatusIncomplete
	inputs.Verification.Reconciliation = []verify.Reconciliation{
		{Entity: "tasks", Expected: 6, Archived: 6, Status: verify.CheckPass},
		{Entity: "attachments", Expected: 1, Archived: 0, Unresolved: 1, Status: verify.CheckIncomplete},
	}
	for index := range inputs.Verification.Checks {
		if inputs.Verification.Checks[index].Name == "attachment_integrity" {
			inputs.Verification.Checks[index].Status = verify.CheckIncomplete
			inputs.Verification.Checks[index].Findings = []string{`attachment source "a1" was expected but not archived: attachment endpoint returned HTTP 503`}
		}
	}

	document := Build(inputs)
	rendered := renderBoth(t, document)

	if !document.Failed() {
		t.Fatal("an INCOMPLETE archive was reported as a passing result")
	}
	if document.Conclusion.Result != verify.ResultIncomplete {
		t.Fatalf("conclusion result = %s", document.Conclusion.Result)
	}
	if !strings.Contains(document.Conclusion.Verdict, "NOT a complete recovery source") {
		t.Fatalf("verdict does not state incompleteness: %q", document.Conclusion.Verdict)
	}
	if document.Attachments.UnresolvedCount != 1 || len(document.Attachments.Unresolved) != 1 {
		t.Fatalf("attachments = %#v", document.Attachments)
	}
	unresolved := document.Attachments.Unresolved[0]
	if unresolved.SourceID != "a1" || unresolved.Filename != "design.png" ||
		!strings.Contains(unresolved.Reason, "HTTP 503") {
		t.Fatalf("unresolved attachment loses its evidence: %#v", unresolved)
	}
	// The original source identifier must remain traceable in the output.
	if !strings.Contains(rendered, "a1") || !strings.Contains(rendered, "design.png") {
		t.Error("the unresolved attachment is not traceable in the rendered report")
	}
	if !containsAny(document.MustNotClaim, "unresolved attachments listed above were preserved") {
		t.Errorf("unresolved attachments were not excluded from recovery claims: %#v", document.MustNotClaim)
	}
	for _, statement := range document.Recovery {
		if strings.Contains(statement.Claim, "attachment binary") && statement.Proven {
			t.Error("the report claimed attachment integrity for an incomplete archive")
		}
	}
	if len(document.Discrepancies) == 0 {
		t.Fatal("the unresolved attachment is missing from the discrepancy section")
	}
}

func TestReportFromFailedArchiveNeverReadsAsSafe(t *testing.T) {
	t.Parallel()

	inputs := healthyInputs()
	inputs.Verification.Result = verify.ResultFailed
	for index := range inputs.Verification.Checks {
		switch inputs.Verification.Checks[index].Name {
		case "sqlite_integrity":
			inputs.Verification.Checks[index].Status = verify.CheckFail
			inputs.Verification.Checks[index].Summary = "alih.db integrity could not be established"
			inputs.Verification.Checks[index].Findings = []string{"database disk image is malformed"}
		case "count_reconciliation", "source_object_reconciliation", "referential_integrity",
			"hierarchy_reconstruction", "attachment_integrity", "custom_field_evidence":
			inputs.Verification.Checks[index].Status = verify.CheckNotEvaluated
			inputs.Verification.Checks[index].Summary = "not evaluated because alih.db could not be read"
		}
	}

	document := Build(inputs)
	rendered := renderBoth(t, document)

	if !document.Failed() {
		t.Fatal("a FAILED archive was reported as a passing result")
	}
	if !strings.Contains(document.Conclusion.Verdict, "cannot prove this archive is intact") {
		t.Fatalf("verdict = %q", document.Conclusion.Verdict)
	}
	if !containsAny(document.Conclusion.Statements, "Do not use this archive as a recovery source") {
		t.Errorf("the conclusion does not warn against recovery use: %#v", document.Conclusion.Statements)
	}
	if !strings.Contains(document.Verification.FiguresTrust, "sqlite_integrity") {
		t.Errorf("distrust was not scoped to the checks that failed: %q", document.Verification.FiguresTrust)
	}
	if strings.Contains(document.Verification.FiguresTrust, "must not be read as fact.") &&
		!strings.Contains(document.Verification.FiguresTrust, "Only figures and structure that depend on") {
		t.Errorf("distrust was stated without scope: %q", document.Verification.FiguresTrust)
	}
	// Every recovery claim that depends on a failed or unevaluated check must
	// be reported as not proven.
	for _, statement := range document.Recovery {
		for _, basis := range statement.BasedOn {
			if basis == "sqlite_integrity" && statement.Proven {
				t.Errorf("claim survived a failed check: %q", statement.Claim)
			}
		}
	}
	if !strings.Contains(rendered, "database disk image is malformed") {
		t.Error("the corruption finding was not carried into the report")
	}
	if !containsAny(document.MustNotClaim, "verification did not pass") {
		t.Errorf("a failed archive did not exclude recovery guarantees: %#v", document.MustNotClaim)
	}
	if strings.Contains(document.Conclusion.Verdict, "proven within") {
		t.Fatal("a failed archive was described as proven")
	}
}

func TestReportNeverClaimsMoreThanTheVerifier(t *testing.T) {
	t.Parallel()

	// Every check status is walked so that no combination lets a recovery
	// claim outlive the check it depends on.
	for _, status := range []string{verify.CheckFail, verify.CheckIncomplete, verify.CheckUnproven, verify.CheckNotEvaluated} {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			for _, target := range []string{
				"sqlite_integrity", "schema_consistency", "raw_evidence_integrity",
				"referential_integrity", "hierarchy_reconstruction", "attachment_integrity",
				"count_reconciliation", "file_checksums", "custom_field_evidence",
				"portable_identifier_derivation", "raw_evidence_references",
				"manifest_file_inventory", "source_object_reconciliation",
			} {
				inputs := healthyInputs()
				for index := range inputs.Verification.Checks {
					if inputs.Verification.Checks[index].Name == target {
						inputs.Verification.Checks[index].Status = status
					}
				}
				document := Build(inputs)
				for _, statement := range document.Recovery {
					for _, basis := range statement.BasedOn {
						if basis == target && statement.Proven {
							t.Fatalf("claim %q stayed proven while %s was %s", statement.Claim, target, status)
						}
					}
				}
			}
		})
	}
}

func TestReportSurvivesAnArchiveThatDoesNotDescribeItself(t *testing.T) {
	t.Parallel()

	inputs := Inputs{
		ArchivePath: "/archives/broken", ManifestAvailable: false,
		ManifestError: "manifest.json is not valid JSON",
		Verification: verify.Report{
			ArchivePath: "/archives/broken", Result: verify.ResultFailed,
			Checks: []verify.Check{{Name: "manifest_integrity", Status: verify.CheckFail,
				Summary: "manifest.json is not valid JSON", Findings: []string{"unexpected end of JSON input"}}},
		},
		GeneratedAt: generatedAt, AlihVersion: "0.0.1",
	}
	document := Build(inputs)
	rendered := renderBoth(t, document)

	if !document.Failed() {
		t.Fatal("an unreadable archive was reported as passing")
	}
	if document.Identity.RecordedStatus != "UNKNOWN" {
		t.Fatalf("recorded status = %q, want UNKNOWN", document.Identity.RecordedStatus)
	}
	if !containsAny(document.MustNotClaim, "its manifest could not be read") {
		t.Errorf("the unreadable manifest was not disclosed: %#v", document.MustNotClaim)
	}
	if !strings.Contains(rendered, "manifest.json could not be read") {
		t.Error("the rendered report does not surface the unreadable manifest")
	}
	if len(document.Capabilities) != 0 {
		t.Fatal("capabilities were invented for an archive that declares none")
	}
	if !strings.Contains(rendered, "scope is not established") {
		t.Error("an archive with no declared capabilities did not say so")
	}
}

func TestReportDocumentRoundTripsAsMachineReadableJSON(t *testing.T) {
	t.Parallel()

	document := Build(healthyInputs())
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var decoded Document
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Kind != Kind || decoded.SchemaVersion != SchemaVersion {
		t.Fatalf("document identity = %q v%d", decoded.Kind, decoded.SchemaVersion)
	}
	if decoded.Conclusion.Result != document.Conclusion.Result || len(decoded.Recovery) != len(document.Recovery) {
		t.Fatal("the machine-readable report lost content in a round trip")
	}
}

func TestReportEscapesArchivedSourceContentInHTML(t *testing.T) {
	t.Parallel()

	inputs := healthyInputs()
	inputs.Verification.Source.Name = `Demeter <script>alert(1)</script>`
	inputs.Manifest.Source.Name = inputs.Verification.Source.Name
	var html bytes.Buffer
	if err := RenderHTML(&html, Build(inputs)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html.String(), "<script>alert(1)</script>") {
		t.Fatal("archived source content was rendered as HTML markup")
	}
	if !strings.Contains(html.String(), "Demeter") {
		t.Fatal("the workspace name was lost instead of escaped")
	}
}

func containsAny(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

// failedByAttachmentCorruption models the audited case: an attachment binary no
// longer matches its recorded checksum, which fails the archive overall while
// leaving the database, raw evidence, hierarchy and counts provably intact.
func failedByAttachmentCorruption() Inputs {
	inputs := healthyInputs()
	inputs.Verification.Result = verify.ResultFailed
	for index := range inputs.Verification.Checks {
		switch inputs.Verification.Checks[index].Name {
		case "attachment_integrity":
			inputs.Verification.Checks[index].Status = verify.CheckFail
			inputs.Verification.Checks[index].Summary = "archived attachment binaries do not match the evidence recorded for them"
			inputs.Verification.Checks[index].Findings = []string{"attachment binary attachments/a1.png checksum does not match the recorded checksum"}
		case "file_checksums":
			inputs.Verification.Checks[index].Status = verify.CheckFail
			inputs.Verification.Checks[index].Summary = "at least one archived file no longer matches its recorded checksum"
			inputs.Verification.Checks[index].Findings = []string{"attachments/a1.png checksum does not match the manifest checksum"}
		}
	}
	return inputs
}

// stillProvenChecks are the claims that attachment corruption does not touch.
var stillProvenChecks = []string{
	"sqlite_integrity", "raw_evidence_integrity", "hierarchy_reconstruction",
	"count_reconciliation", "source_object_reconciliation", "schema_consistency",
	"referential_integrity", "portable_identifier_derivation", "custom_field_evidence",
}

func TestFailedReportKeepsEvidenceFromChecksThatPassed(t *testing.T) {
	t.Parallel()

	document := Build(failedByAttachmentCorruption())
	rendered := renderBoth(t, document)

	if !document.Failed() || document.Conclusion.Result != verify.ResultFailed {
		t.Fatalf("attachment corruption did not fail the archive: %s", document.Conclusion.Result)
	}
	// Every check untouched by the corruption must still be reported as
	// established, both as a check and as the recovery claim it supports.
	established := map[string]bool{}
	for _, name := range document.Verification.Established {
		established[name] = true
	}
	for _, name := range stillProvenChecks {
		if !established[name] {
			t.Errorf("check %s passed but the report did not record it as established", name)
		}
	}
	for _, statement := range document.Recovery {
		touchesAttachments := false
		for _, basis := range statement.BasedOn {
			if basis == "attachment_integrity" || basis == "file_checksums" {
				touchesAttachments = true
			}
		}
		if touchesAttachments {
			if statement.Proven {
				t.Errorf("a claim covering the corrupted evidence stayed proven: %q", statement.Claim)
			}
			continue
		}
		if !statement.Proven {
			t.Errorf("attachment corruption erased an unrelated recovery claim: %q (%s)", statement.Claim, statement.Reason)
		}
	}

	// The failure must not be generalised into a verdict on all evidence.
	for _, forbidden := range []string{
		"Treat every figure this archive states about itself as unproven",
		"Figures recorded in the archive are NOT corroborated by verification and must not be read as fact",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("one failing check was generalised into a blanket distrust: %q", forbidden)
		}
	}
	if !strings.Contains(document.Verification.FiguresTrust, "attachment_integrity") ||
		!strings.Contains(document.Verification.FiguresTrust, "file_checksums") {
		t.Errorf("distrust does not name the checks it applies to: %q", document.Verification.FiguresTrust)
	}
	for _, name := range stillProvenChecks {
		if strings.Contains(document.Verification.FiguresTrust, name) {
			t.Errorf("a check that passed was named as uncorroborated: %s", name)
		}
	}

	// The archive as a whole must still be unusable as a recovery source.
	if !containsAny(document.Conclusion.Statements, "does not make the archive as a whole safe to recover from") {
		t.Errorf("the conclusion no longer warns against recovering from the archive: %#v", document.Conclusion.Statements)
	}
	if !containsAny(document.Conclusion.Statements, "Do not use this archive as a recovery source") {
		t.Errorf("the conclusion lost its recovery warning: %#v", document.Conclusion.Statements)
	}
	if !containsAny(document.MustNotClaim, "as a whole is a proven recovery source") {
		t.Errorf("the archive-level non-claim was lost: %#v", document.MustNotClaim)
	}
	if !containsAny(document.Conclusion.Statements, "checks did pass") {
		t.Errorf("the conclusion does not acknowledge the checks that passed: %#v", document.Conclusion.Statements)
	}
}

func TestSupportedCapabilityIsNotAnIntegrityClaimAboutTheArchive(t *testing.T) {
	t.Parallel()

	failed := Build(failedByAttachmentCorruption())
	var attachments *Capability
	for index := range failed.Capabilities {
		if failed.Capabilities[index].Name == "Tasks/subtasks" {
			attachments = &failed.Capabilities[index]
		}
	}
	if attachments == nil {
		t.Fatal("fixture lost its SUPPORTED capability")
	}
	// The source capability state must survive an archive-level failure.
	if attachments.State != "SUPPORTED" {
		t.Fatalf("a failed archive demoted a source capability to %q", attachments.State)
	}
	// It must not be read as a statement that this archive is intact.
	if strings.Contains(attachments.RecoveryMeaning, "covered by this verification") {
		t.Errorf("SUPPORTED still implies this archive passed verification: %q", attachments.RecoveryMeaning)
	}
	if !strings.Contains(attachments.ArchiveEvidence, "describes the source, not this archive") {
		t.Errorf("SUPPORTED was not separated from archive integrity: %q", attachments.ArchiveEvidence)
	}
	if !strings.Contains(attachments.ArchiveEvidence, "failed") {
		t.Errorf("the archive-level failure is not stated beside the capability: %q", attachments.ArchiveEvidence)
	}

	healthy := Build(healthyInputs())
	for _, capability := range healthy.Capabilities {
		if capability.State == "SUPPORTED" && !strings.Contains(capability.ArchiveEvidence, "passed verification") {
			t.Errorf("a verified archive does not confirm its own copy: %q", capability.ArchiveEvidence)
		}
		if capability.State != "SUPPORTED" && !strings.Contains(capability.ArchiveEvidence, "no integrity claim") {
			t.Errorf("a non-SUPPORTED capability made an integrity claim: %q", capability.ArchiveEvidence)
		}
	}
	rendered := renderBoth(t, failed)
	if !strings.Contains(rendered, "describes the source, not this archive") {
		t.Error("the source/archive distinction is not visible in the rendered report")
	}
}

func TestReportSeparatesSourceReadTimeFromArchiveCompletionTime(t *testing.T) {
	t.Parallel()

	document := Build(healthyInputs())
	rendered := renderBoth(t, document)

	if document.Identity.SourceSnapshotCompletedAt == nil || !document.Identity.SourceSnapshotCompletedAt.Equal(snapshotCompletedAt) {
		t.Fatalf("source read instant = %v, want %s", document.Identity.SourceSnapshotCompletedAt, snapshotCompletedAt)
	}
	if document.Identity.ArchiveCompletedAt == nil || !document.Identity.ArchiveCompletedAt.Equal(archiveCompletedAt) {
		t.Fatalf("archive completion instant = %v, want %s", document.Identity.ArchiveCompletedAt, archiveCompletedAt)
	}
	// The defect this replaces: the source read time rendered as "Archive created".
	if strings.Contains(rendered, "Archive created") {
		t.Error("the report still labels a timestamp as the archive creation time")
	}
	for _, expected := range []string{
		"Source read completed", "2026-08-30 01:54:42 UTC",
		"Archive completed", "2026-08-30 02:32:14 UTC",
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("report does not state %q", expected)
		}
	}
	// The gap between the two events is stated rather than left for the reader
	// to assume it is zero.
	if document.Identity.CompletionLag != "37m32s after the source extraction finished" {
		t.Errorf("completion lag = %q", document.Identity.CompletionLag)
	}
	if !strings.Contains(rendered, "37m32s after the source extraction finished") {
		t.Error("the interval between reading the source and finishing the archive is not visible")
	}
}

func TestReportStatesWhenAnArchiveRecordsNoCompletionTime(t *testing.T) {
	t.Parallel()

	// A FAILED archive was never completed, so M4 records no completion instant.
	inputs := failedByAttachmentCorruption()
	inputs.Manifest.ArchiveCompletedAt = nil
	document := Build(inputs)
	rendered := renderBoth(t, document)

	if document.Identity.ArchiveCompletedAt != nil {
		t.Fatalf("a missing completion time was invented: %s", document.Identity.ArchiveCompletedAt)
	}
	if document.Identity.CompletionLag != "" {
		t.Errorf("a lag was computed without a completion time: %q", document.Identity.CompletionLag)
	}
	if !strings.Contains(rendered, "this archive states no completion time") {
		t.Error("the absent completion time is not disclosed")
	}
	// The source read time is still known and must not be dropped with it.
	if document.Identity.SourceSnapshotCompletedAt == nil {
		t.Error("the source read instant was discarded along with the missing completion time")
	}
}

func TestReportFlagsACompletionTimeBeforeItsOwnSourceEvidence(t *testing.T) {
	t.Parallel()

	inputs := healthyInputs()
	impossible := snapshotCompletedAt.Add(-time.Hour)
	inputs.Manifest.ArchiveCompletedAt = &impossible
	document := Build(inputs)

	if !strings.Contains(document.Identity.CompletionLag, "BEFORE the source extraction") {
		t.Fatalf("an archive completing before its own evidence was not flagged: %q", document.Identity.CompletionLag)
	}
}
