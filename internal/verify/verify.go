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

// Package verify independently verifies an M4 portable archive.
//
// Verification reads the archive only. It never contacts the source SaaS, and
// it never writes to the archive under test, so an internal integrity claim is
// made purely from archived evidence: manifest, recorded checksums, SQLite
// integrity, the immutable raw M3 evidence copied into the archive, and the
// portable model itself.
//
// Verification never upgrades a source limitation. A PARTIAL, UNAVAILABLE or
// UNKNOWN capability stays exactly as the source connector recorded it, and a
// clean internal result is never presented as proof of source completeness or
// of point-in-time consistency.
package verify

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"alih/internal/archive"
	"alih/internal/connector"
)

// Archive result states. INCOMPLETE and FAILED are both non-clean outcomes and
// must never be reported as a verified archive.
const (
	ResultVerified                = "VERIFIED"
	ResultVerifiedWithLimitations = "VERIFIED_WITH_LIMITATIONS"
	ResultIncomplete              = "INCOMPLETE"
	ResultFailed                  = "FAILED"
)

// Individual check outcomes. UNPROVEN means the archive is internally
// consistent but the claim itself cannot be established from archived
// evidence; it is a limitation, never a pass. NOT_EVALUATED means an earlier
// failure removed the evidence this check needs and therefore fails closed.
const (
	CheckPass         = "PASS"
	CheckFail         = "FAIL"
	CheckIncomplete   = "INCOMPLETE"
	CheckUnproven     = "UNPROVEN"
	CheckNotEvaluated = "NOT_EVALUATED"
)

// Field value verdicts returned by a connector's field semantics.
const (
	FieldValueValid    = "VALID"
	FieldValueInvalid  = "INVALID"
	FieldValueUnproven = "UNPROVEN"
)

const maxFindings = 20

// FieldSemantics lets a connector prove observed custom-field values against
// their archived source definitions without putting provider assumptions into
// the core verifier. A connector must return UNPROVEN rather than guessing.
type FieldSemantics interface {
	Connector() string
	ValidateFieldValue(fieldType string, definitionJSON, observedJSON []byte) (verdict string, reason string)
}

// Options carries optional connector-specific evidence interpreters.
type Options struct {
	FieldSemantics FieldSemantics
}

// Check is one named verification claim and its supporting findings.
type Check struct {
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Summary  string   `json:"summary"`
	Findings []string `json:"findings,omitempty"`
}

// Reconciliation is one expected-versus-archived entity comparison.
type Reconciliation struct {
	Entity     string `json:"entity"`
	Expected   int    `json:"expected"`
	Archived   int    `json:"archived"`
	Unresolved int    `json:"unresolved"`
	Status     string `json:"status"`
}

// Report is the complete machine-readable verification result.
type Report struct {
	ArchivePath             string                           `json:"archive_path"`
	Result                  string                           `json:"result"`
	ArchiveStatus           string                           `json:"archive_status"`
	Connector               string                           `json:"connector"`
	Source                  connector.Workspace              `json:"source"`
	Checks                  []Check                          `json:"checks"`
	Reconciliation          []Reconciliation                 `json:"reconciliation"`
	CapabilitySchemaVersion int                              `json:"capability_schema_version,omitempty"`
	Capabilities            []connector.Capability           `json:"capabilities"`
	OperationalAssessment   *connector.OperationalAssessment `json:"operational_assessment,omitempty"`
	Limitations             []string                         `json:"limitations"`
	NotProven               []string                         `json:"not_proven"`
}

// Failed reports whether the archive did not pass verification.
func (report Report) Failed() bool {
	return report.Result != ResultVerified && report.Result != ResultVerifiedWithLimitations
}

// verification accumulates checks while walking the archive.
type verification struct {
	root        string
	options     Options
	checks      []Check
	counts      []Reconciliation
	notProven   []string
	foreignKeys []string
}

func (v *verification) record(name, status, summary string, findings []string) {
	sort.Strings(findings)
	if len(findings) > maxFindings {
		remaining := len(findings) - maxFindings
		findings = append(findings[:maxFindings:maxFindings], fmt.Sprintf("... and %d further findings", remaining))
	}
	v.checks = append(v.checks, Check{Name: name, Status: status, Summary: summary, Findings: findings})
}

func (v *verification) pass(name, summary string) { v.record(name, CheckPass, summary, nil) }

func (v *verification) fail(name, summary string, findings []string) {
	v.record(name, CheckFail, summary, findings)
}

func (v *verification) unproven(name, summary string, findings []string) {
	v.record(name, CheckUnproven, summary, findings)
	v.notProven = append(v.notProven, summary)
}

func (v *verification) notEvaluated(name, summary string) {
	v.record(name, CheckNotEvaluated, summary, nil)
}

// decide reports findings and passes only when findings is empty.
func (v *verification) decide(name, passSummary, failSummary string, findings []string) bool {
	if len(findings) == 0 {
		v.pass(name, passSummary)
		return true
	}
	v.fail(name, failSummary, findings)
	return false
}

// Archive verifies the M4 archive rooted at path. It returns an error only when
// the path cannot be treated as an archive directory at all; every other
// problem is reported as a failed verification result.
func Archive(path string, options Options) (Report, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Report{}, fmt.Errorf("resolve archive path: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return Report{}, fmt.Errorf("inspect archive: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Report{}, errors.New("archive path must be a real directory")
	}

	v := &verification{root: absolute, options: options}
	report := Report{ArchivePath: absolute}

	files := v.checkStructure()

	manifest, manifestOK := v.checkManifest(files)
	if manifestOK {
		report.ArchiveStatus = manifest.Status
		report.Connector = manifest.Connector
		report.Source = manifest.Source
		report.CapabilitySchemaVersion = manifest.CapabilitySchemaVersion
		report.Capabilities = connector.CanonicalCapabilities(manifest.CapabilitySchemaVersion, manifest.Capabilities)
		report.OperationalAssessment = manifest.OperationalAssessment
		v.checkRecordedFileChecksums(manifest, files)
	} else {
		v.notEvaluated("file_checksums", "recorded file checksums were not evaluated because manifest.json could not be read")
	}

	database, databaseOK := v.checkSQLite()
	evidence, evidenceOK := v.checkRawEvidence(manifest, manifestOK)

	if databaseOK {
		v.checkSchemaConsistency(database)
	} else {
		v.notEvaluated("schema_consistency", "portable schema consistency was not evaluated because alih.db could not be read")
	}
	if databaseOK && manifestOK {
		v.checkArchiveMetadata(database, manifest)
		v.checkPortableIdentifiers(database)
		v.checkReferentialIntegrity(database)
		v.checkAttachments(database, manifest, files)
		v.checkCustomFields(database, manifest)
		v.checkDiscrepancies(database, manifest)
	} else {
		for _, name := range []string{"archive_metadata_consistency", "portable_identifier_derivation", "referential_integrity", "attachment_integrity", "custom_field_evidence", "discrepancy_reconciliation"} {
			v.notEvaluated(name, "not evaluated because alih.db or manifest.json could not be read")
		}
	}
	if databaseOK && evidenceOK {
		v.checkSourceObjectReconciliation(database, evidence)
		v.checkParentLinkage(database, evidence)
		v.checkRawPaths(database, files)
	} else {
		for _, name := range []string{"source_object_reconciliation", "hierarchy_reconstruction", "raw_evidence_references"} {
			v.notEvaluated(name, "not evaluated because alih.db or the archived raw M3 evidence could not be read")
		}
	}
	if databaseOK && manifestOK && evidenceOK {
		v.checkCounts(database, manifest, evidence)
	} else {
		v.notEvaluated("count_reconciliation", "expected-versus-archived counts were not evaluated because required archive evidence could not be read")
	}
	if manifestOK {
		v.checkSourceConsistencyScope(database, manifest, databaseOK)
		v.checkAccessScopeCompleteness(manifest)
		v.checkLimitationPreservation(manifest)
	} else {
		v.notEvaluated("source_consistency_scope", "source consistency scope was not evaluated because manifest.json could not be read")
		v.notEvaluated("access_scope_completeness", "access scope was not evaluated because manifest.json could not be read")
		v.notEvaluated("limitation_preservation", "source limitations were not evaluated because manifest.json could not be read")
	}

	report.Checks = v.checks
	report.Reconciliation = v.counts
	report.NotProven = v.notProven
	report.Limitations = v.limitations(manifest, manifestOK)
	report.Result = v.result()
	return report, nil
}

func (v *verification) limitations(manifest archive.Manifest, manifestOK bool) []string {
	limitations := []string{
		"Verification establishes internal archive integrity from archived evidence only; it does not re-read the source SaaS and therefore cannot prove that the source still matches this archive.",
	}
	if manifestOK {
		limitations = append(limitations, manifest.Limitations...)
		for _, capability := range manifest.Capabilities {
			if capability.State != connector.CapabilitySupported {
				limitations = append(limitations, fmt.Sprintf("Source capability %q remains %s and was not verified as portable data: %s", capability.Name, capability.State, capability.Note))
			}
			if manifest.CapabilitySchemaVersion == connector.CapabilitySchemaVersion && capability.Availability != connector.CapabilityAvailabilityAvailable {
				limitations = append(limitations, fmt.Sprintf("Source capability %q was %s for this archive operation.", capability.Name, capability.Availability))
			}
		}
	}
	limitations = append(limitations, v.notProven...)
	sort.Strings(limitations)
	return dedupe(limitations)
}

func (v *verification) result() string {
	failed, incomplete, limited := false, false, false
	for _, check := range v.checks {
		switch check.Status {
		case CheckFail, CheckNotEvaluated:
			failed = true
		case CheckIncomplete:
			incomplete = true
		case CheckUnproven:
			limited = true
		}
	}
	switch {
	case failed:
		return ResultFailed
	case incomplete:
		return ResultIncomplete
	case limited || len(v.notProven) > 0:
		return ResultVerifiedWithLimitations
	default:
		return ResultVerified
	}
}

func dedupe(values []string) []string {
	result := make([]string, 0, len(values))
	var previous string
	for index, value := range values {
		if index > 0 && value == previous {
			continue
		}
		result = append(result, value)
		previous = value
	}
	return result
}
