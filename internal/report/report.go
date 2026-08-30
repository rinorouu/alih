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

// Package report turns archived M4 evidence and an M5 verification result into
// a recovery report a human can act on.
//
// The report is a restatement of evidence, not a new claim. Every recovery
// statement it makes is tied to the M5 checks that support it, and any claim
// those checks did not establish is rendered as an explicit non-claim. The
// report never contacts the source SaaS, never repairs an archive and never
// presents a manifest figure as fact when verification did not corroborate it.
package report

import (
	"fmt"
	"strings"
	"time"

	"alih/internal/archive"
	"alih/internal/verify"
)

// SchemaVersion identifies the machine-readable recovery report format.
const SchemaVersion = 1

// Kind names the machine-readable document so a consumer cannot confuse a
// recovery report with a manifest or a verification report.
const Kind = "alih_recovery_report"

// Identity answers where the archive came from and when it was made.
type Identity struct {
	ArchivePath   string `json:"archive_path"`
	Connector     string `json:"connector"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	// ExtractedByID and ExtractedByName name the account whose access this
	// archive represents. An archive is one identity's view of the source.
	ExtractedByID   string `json:"extracted_by_id,omitempty"`
	ExtractedByName string `json:"extracted_by_name,omitempty"`
	// SourceSnapshotCompletedAt is when the source was read; ArchiveCompletedAt
	// is when this artifact was finished. They are separate questions and the
	// report answers them separately rather than presenting one as the other.
	SourceSnapshotCompletedAt *time.Time `json:"source_snapshot_completed_at"`
	ArchiveCompletedAt        *time.Time `json:"archive_completed_at"`
	CompletionLag             string     `json:"completion_lag,omitempty"`
	RecordedStatus            string     `json:"recorded_archive_status"`
	CreatedByAlihVersion      string     `json:"created_by_alih_version"`
	ArchiveSchemaVersion      int        `json:"archive_schema_version"`
	SourceSnapshotDigest      string     `json:"source_snapshot_logical_digest"`
	SourceSnapshotAtomic      bool       `json:"source_snapshot_atomic"`
	RecordedFiles             int        `json:"recorded_files"`
	ManifestReadable          bool       `json:"manifest_readable"`
	ManifestError             string     `json:"manifest_error,omitempty"`
}

// Verification restates the M5 result without softening it, and without
// widening it: Established and NotEstablished name the individual checks, so
// the report can scope every statement to the evidence that actually supports
// or contradicts it.
type Verification struct {
	Result         string         `json:"result"`
	Headline       string         `json:"headline"`
	Counts         map[string]int `json:"check_counts"`
	Checks         []verify.Check `json:"checks"`
	Established    []string       `json:"established_checks"`
	NotEstablished []string       `json:"not_established_checks"`
	FiguresTrust   string         `json:"recorded_figure_trust"`
}

// Statement is one recovery claim and the verification checks behind it. A
// statement that its supporting checks did not establish is kept in the report
// as an explicit non-claim rather than dropped.
type Statement struct {
	Claim   string   `json:"claim"`
	Proven  bool     `json:"proven"`
	BasedOn []string `json:"based_on"`
	Reason  string   `json:"reason,omitempty"`
}

// EntityCoverage is one expected-versus-archived line, taken from the verifier
// rather than from the manifest.
type EntityCoverage struct {
	Entity     string `json:"entity"`
	Expected   int    `json:"expected"`
	Archived   int    `json:"archived"`
	Unresolved int    `json:"unresolved"`
	Status     string `json:"status"`
}

// UnresolvedAttachment is one supported attachment that was never archived.
type UnresolvedAttachment struct {
	SourceID     string `json:"source_id"`
	Filename     string `json:"filename,omitempty"`
	ExpectedSize *int64 `json:"expected_size,omitempty"`
	Reason       string `json:"reason"`
}

// Attachments separates binaries that were preserved from those that were not.
type Attachments struct {
	Expected        int                    `json:"expected"`
	Retrieved       int                    `json:"retrieved"`
	UnresolvedCount int                    `json:"unresolved"`
	IntegrityCheck  string                 `json:"integrity_check_status"`
	IntegrityNote   string                 `json:"integrity_note"`
	Unresolved      []UnresolvedAttachment `json:"unresolved_items"`
}

// Capability restates a source capability and what it means for recovery. The
// state is never rewritten: UNKNOWN stays UNKNOWN and PARTIAL stays PARTIAL.
//
// State describes the source: what ClickUp's API exposes and what Alih has
// implemented for it. ArchiveEvidence describes this particular archive. The
// two are deliberately separate, because a capability can remain SUPPORTED at
// the source while the integrity of this archive's copy of it has failed.
type Capability struct {
	Name            string `json:"name"`
	State           string `json:"state"`
	Note            string `json:"note"`
	RecoveryMeaning string `json:"recovery_meaning"`
	ArchiveEvidence string `json:"archive_evidence"`
}

// Item is one discrepancy or unresolved item with its evidence origin.
type Item struct {
	Kind     string `json:"kind"`
	SourceID string `json:"source_id,omitempty"`
	Message  string `json:"message"`
	Origin   string `json:"origin"`
}

// Conclusion is the conservative recovery verdict.
type Conclusion struct {
	Result     string   `json:"result"`
	Verdict    string   `json:"verdict"`
	Statements []string `json:"statements"`
}

// Document is the complete machine-readable recovery report.
type Document struct {
	SchemaVersion int              `json:"schema_version"`
	Kind          string           `json:"kind"`
	AlihVersion   string           `json:"alih_version"`
	GeneratedAt   time.Time        `json:"generated_at"`
	Identity      Identity         `json:"archive_identity"`
	Verification  Verification     `json:"verification"`
	Recovery      []Statement      `json:"recovery_summary"`
	Coverage      []EntityCoverage `json:"entity_coverage"`
	Attachments   Attachments      `json:"attachments"`
	Capabilities  []Capability     `json:"capability_coverage"`
	Limitations   []string         `json:"limitations"`
	NotProven     []string         `json:"unproven_claims"`
	Discrepancies []Item           `json:"discrepancies"`
	MustNotClaim  []string         `json:"must_not_be_claimed"`
	Conclusion    Conclusion       `json:"recovery_conclusion"`
}

// Failed reports whether the archive this report describes did not pass
// verification. A report is still produced for such an archive; it simply must
// not read as a recovery-ready result.
func (document Document) Failed() bool {
	return document.Conclusion.Result != verify.ResultVerified &&
		document.Conclusion.Result != verify.ResultVerifiedWithLimitations
}

// Inputs bundles the evidence a recovery report is allowed to use: what the
// archive records about itself, and what M5 verification could prove about it.
type Inputs struct {
	ArchivePath       string
	Manifest          archive.Manifest
	ManifestAvailable bool
	ManifestError     string
	Verification      verify.Report
	GeneratedAt       time.Time
	AlihVersion       string
}

// Build assembles the recovery report. It performs no I/O and makes no source
// access, so it can only restate the evidence it was given.
func Build(inputs Inputs) Document {
	statuses := checkStatuses(inputs.Verification)
	document := Document{
		SchemaVersion: SchemaVersion,
		Kind:          Kind,
		AlihVersion:   inputs.AlihVersion,
		GeneratedAt:   inputs.GeneratedAt.UTC(),
		Identity:      buildIdentity(inputs),
		Verification:  buildVerification(inputs.Verification),
		Recovery:      buildRecovery(inputs, statuses),
		Coverage:      buildCoverage(inputs.Verification),
		Attachments:   buildAttachments(inputs, statuses),
		Capabilities:  buildCapabilities(inputs, statuses),
		Limitations:   append([]string(nil), inputs.Verification.Limitations...),
		NotProven:     append([]string(nil), inputs.Verification.NotProven...),
		Discrepancies: buildDiscrepancies(inputs),
	}
	document.MustNotClaim = buildMustNotClaim(inputs, document, statuses)
	document.Conclusion = buildConclusion(inputs, document)
	return document
}

func checkStatuses(verification verify.Report) map[string]string {
	statuses := make(map[string]string, len(verification.Checks))
	for _, check := range verification.Checks {
		statuses[check.Name] = check.Status
	}
	return statuses
}

func buildIdentity(inputs Inputs) Identity {
	identity := Identity{
		ArchivePath:      inputs.ArchivePath,
		ManifestReadable: inputs.ManifestAvailable,
		ManifestError:    inputs.ManifestError,
		Connector:        inputs.Verification.Connector,
		WorkspaceID:      inputs.Verification.Source.ID,
		WorkspaceName:    inputs.Verification.Source.Name,
	}
	if !inputs.ManifestAvailable {
		// Without a readable manifest nothing about the archive's identity is
		// established, and that must be visible rather than blank.
		identity.RecordedStatus = "UNKNOWN"
		return identity
	}
	manifest := inputs.Manifest
	if identity.Connector == "" {
		identity.Connector = manifest.Connector
	}
	if identity.WorkspaceID == "" {
		identity.WorkspaceID = manifest.Source.ID
		identity.WorkspaceName = manifest.Source.Name
	}
	if !manifest.SourceSnapshotCompletedAt.IsZero() {
		completed := manifest.SourceSnapshotCompletedAt.UTC()
		identity.SourceSnapshotCompletedAt = &completed
	}
	if manifest.ArchiveCompletedAt != nil && !manifest.ArchiveCompletedAt.IsZero() {
		completed := manifest.ArchiveCompletedAt.UTC()
		identity.ArchiveCompletedAt = &completed
	}
	identity.CompletionLag = completionLag(identity.SourceSnapshotCompletedAt, identity.ArchiveCompletedAt)
	if manifest.ExtractedBy != nil {
		identity.ExtractedByID = manifest.ExtractedBy.ID
		identity.ExtractedByName = manifest.ExtractedBy.Name
	}
	identity.RecordedStatus = manifest.Status
	identity.CreatedByAlihVersion = manifest.AlihVersion
	identity.ArchiveSchemaVersion = manifest.SchemaVersion
	identity.SourceSnapshotDigest = manifest.InputSnapshot.LogicalDigest
	identity.SourceSnapshotAtomic = manifest.InputSnapshot.Atomic
	identity.RecordedFiles = len(manifest.Files)
	return identity
}

// completionLag states the interval between reading the source and finishing
// the archive. It is the honest answer to "how stale is this archive relative
// to the moment it describes", and it is only stated when both instants exist.
func completionLag(snapshotCompleted, archiveCompleted *time.Time) string {
	if snapshotCompleted == nil || archiveCompleted == nil {
		return ""
	}
	lag := archiveCompleted.Sub(*snapshotCompleted)
	if lag < 0 {
		return fmt.Sprintf("the archive records completing %s BEFORE the source extraction it was built from finished; the archive does not explain this", (-lag).Round(time.Second))
	}
	return fmt.Sprintf("%s after the source extraction finished", lag.Round(time.Second))
}

func buildVerification(verification verify.Report) Verification {
	counts := map[string]int{}
	for _, check := range verification.Checks {
		counts[check.Status]++
	}
	outcome := summarizeChecks(verification)
	summary := Verification{
		Result:         verification.Result,
		Counts:         counts,
		Checks:         append([]verify.Check(nil), verification.Checks...),
		Established:    outcome.Established,
		NotEstablished: outcome.NotEstablished,
	}
	switch verification.Result {
	case verify.ResultVerified:
		summary.Headline = "Verification passed: everything Alih expected within its supported scope is archived and provable from this archive."
		summary.FiguresTrust = "Figures recorded in the archive were corroborated by verification."
	case verify.ResultVerifiedWithLimitations:
		summary.Headline = "Verification passed within the supported scope, and source limitations remain that this archive cannot resolve."
		summary.FiguresTrust = "Figures recorded in the archive were corroborated by verification."
	case verify.ResultIncomplete:
		summary.Headline = "Verification did not pass: Alih expected supported data that this archive does not contain."
		summary.FiguresTrust = fmt.Sprintf(
			"%d of %d checks passed and what they establish is still valid evidence; the archive is nonetheless missing supported data Alih expected, so it is not a complete copy. Figures that depend on %s are not corroborated.",
			len(outcome.Established), outcome.Total, nameList(outcome.NotEstablished))
	default:
		summary.Headline = "Verification failed: Alih cannot prove this archive is intact."
		// One failing check does not invalidate the checks that passed. The
		// distrust is scoped to what actually failed, so a reader can still
		// use the proven evidence without treating the archive as sound.
		summary.FiguresTrust = fmt.Sprintf(
			"%d of %d checks passed and what they establish is still valid evidence, listed as PROVEN in the recovery summary. Only figures and structure that depend on %s are uncorroborated and must not be read as fact.",
			len(outcome.Established), outcome.Total, nameList(outcome.NotEstablished))
	}
	return summary
}

// checkOutcome separates the checks that established something from the checks
// that did not, so that no report sentence has to speak for all of them at once.
type checkOutcome struct {
	Total          int
	Established    []string
	NotEstablished []string
}

func summarizeChecks(verification verify.Report) checkOutcome {
	outcome := checkOutcome{Total: len(verification.Checks)}
	for _, check := range verification.Checks {
		switch check.Status {
		case verify.CheckPass:
			outcome.Established = append(outcome.Established, check.Name)
		case verify.CheckUnproven:
			// An unproven claim is a limitation, not a contradiction; it is
			// reported in its own section rather than as a failure here.
		default:
			outcome.NotEstablished = append(outcome.NotEstablished, check.Name)
		}
	}
	return outcome
}

// nameList renders check names for prose without letting a long list bury the
// sentence that contains it.
func nameList(names []string) string {
	if len(names) == 0 {
		return "no check"
	}
	const maximum = 6
	if len(names) <= maximum {
		return joinWithAnd(names)
	}
	return strings.Join(names[:maximum], ", ") + fmt.Sprintf(" and %d further checks", len(names)-maximum)
}

func buildCoverage(verification verify.Report) []EntityCoverage {
	coverage := make([]EntityCoverage, 0, len(verification.Reconciliation))
	for _, entity := range verification.Reconciliation {
		coverage = append(coverage, EntityCoverage{
			Entity: entity.Entity, Expected: entity.Expected, Archived: entity.Archived,
			Unresolved: entity.Unresolved, Status: entity.Status,
		})
	}
	return coverage
}
