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

package report

import (
	"fmt"
	"sort"
	"strings"

	"alih/internal/connector"
	"alih/internal/verify"
)

// recoveryClaims are the things a user may want to do with an archive. Each
// one names the verification checks that must have passed before Alih is
// willing to state it. A claim whose checks did not pass is reported as a
// non-claim, never silently omitted.
var recoveryClaims = []struct {
	claim   string
	basedOn []string
}{
	{
		"The portable database alih.db opens and passes SQLite's own integrity check, so archived rows can be queried locally with ordinary SQLite tools and without ClickUp.",
		[]string{"sqlite_integrity"},
	},
	{
		"schema.json describes exactly the tables and columns present in alih.db, so the archive explains its own structure without reference to ClickUp.",
		[]string{"schema_consistency"},
	},
	{
		"Every archived row still carries its original ClickUp identifiers and a path into raw/, so any archived object can be traced back to the exact API response it came from.",
		[]string{"portable_identifier_derivation", "raw_evidence_references"},
	},
	{
		"The raw ClickUp API responses stored in raw/ still match the checksums recorded when they were captured, so the underlying evidence is intact and re-readable.",
		[]string{"raw_evidence_integrity"},
	},
	{
		"Hierarchy, ownership and relationship pointers in the archive reproduce the structure recorded during extraction, so containers, collections, records, nested records and threaded comments can be reassembled offline.",
		[]string{"hierarchy_reconstruction", "referential_integrity"},
	},
	{
		"Every attachment binary the archive claims to hold is present and matches its recorded SHA-256 checksum.",
		[]string{"attachment_integrity"},
	},
	{
		"The number of entities in the archive matches the number the extraction observed at the source, so nothing ALIH saw was silently dropped on the way into the archive.",
		[]string{"count_reconciliation", "source_object_reconciliation"},
	},
	{
		"Every archived file matches the size and checksum the manifest recorded for it, and the manifest records exactly the files present, so the archive has not been altered since it was written.",
		[]string{"file_checksums", "manifest_file_inventory"},
	},
	{
		"Observed Custom Field values reference archived field definitions and do not contradict them.",
		[]string{"custom_field_evidence"},
	},
}

func buildRecovery(inputs Inputs, statuses map[string]string) []Statement {
	statements := make([]Statement, 0, len(recoveryClaims)+1)
	if content := archiveContentStatement(inputs, statuses); content.Claim != "" {
		statements = append(statements, content)
	}
	for _, claim := range recoveryClaims {
		statement := Statement{Claim: claim.claim, Proven: true, BasedOn: claim.basedOn}
		var blocking []string
		for _, name := range claim.basedOn {
			status, present := statuses[name]
			if !present {
				blocking = append(blocking, name+" was not run")
				continue
			}
			if status != verify.CheckPass {
				blocking = append(blocking, name+" is "+status)
			}
		}
		if len(blocking) > 0 {
			statement.Proven = false
			statement.Reason = "not established because " + strings.Join(blocking, " and ")
		}
		statements = append(statements, statement)
	}
	return statements
}

// archiveContentStatement describes, in counted terms, what a reader will find
// in the archive. It is only stated when the counts themselves were proven.
func archiveContentStatement(inputs Inputs, statuses map[string]string) Statement {
	if len(inputs.Verification.Reconciliation) == 0 {
		return Statement{}
	}
	parts := make([]string, 0, len(inputs.Verification.Reconciliation))
	for _, entity := range inputs.Verification.Reconciliation {
		if entity.Archived == 0 && entity.Expected == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d %s", entity.Archived, strings.ReplaceAll(entity.Entity, "_", " ")))
	}
	if len(parts) == 0 {
		return Statement{}
	}
	statement := Statement{
		Claim:   "The archive holds " + joinWithAnd(parts) + ", readable from alih.db without ClickUp.",
		Proven:  true,
		BasedOn: []string{"sqlite_integrity", "count_reconciliation"},
	}
	for _, name := range statement.BasedOn {
		if statuses[name] != verify.CheckPass {
			statement.Proven = false
			statement.Reason = "these counts come from the archive but " + name + " is " + statusOrMissing(statuses, name) + ", so they are not corroborated"
			break
		}
	}
	return statement
}

// capabilityMeaning explains a source capability state in recovery terms. The
// wording for a state is fixed so that a report can never quietly promote
// UNKNOWN or PARTIAL into something stronger. It describes the source only:
// whether this particular archive's copy is intact is capabilityEvidence's job.
func capabilityMeaning(state connector.CapabilityState) string {
	switch state {
	case connector.CapabilitySupported:
		return "Within ALIH's supported scope, so data for it was collected into the archive."
	case connector.CapabilityPartial:
		return "Only partially represented. Do not treat this capability as fully recoverable from the archive."
	case connector.CapabilityUnsupported:
		return "Not archived. ALIH can reach this source concept but has not implemented portability for it."
	case connector.CapabilityUnavailable:
		return "Not archived. The official API does not expose enough information to recover it."
	case connector.CapabilityUnknown:
		return "Cannot be assessed. ALIH could not establish whether this data exists or is recoverable; its absence from the archive is not evidence that it is absent from the source."
	case connector.CapabilityFailed:
		return "Collection failed. Nothing about this capability is archived or proven."
	default:
		return "Unrecognised capability state; treat it as not recoverable."
	}
}

// capabilityEvidence states what this archive proves about a capability, which
// is a different question from the capability's state at the source. A
// capability stays SUPPORTED even when the archive's copy of its data failed
// verification: the source did expose it, and this archive did not keep it
// intact. Collapsing the two would either excuse a corrupt archive or wrongly
// demote a source capability.
func capabilityEvidence(state connector.CapabilityState, result string) string {
	if state != connector.CapabilitySupported {
		return "Not archived, so this archive makes no integrity claim about it."
	}
	switch result {
	case verify.ResultVerified, verify.ResultVerifiedWithLimitations:
		return "This archive's copy passed verification."
	case verify.ResultIncomplete:
		return "The capability state above describes the source, not this archive. This archive is incomplete; see the entity coverage and attachment sections for what is actually present."
	default:
		return "The capability state above describes the source, not this archive. Verification of this archive failed; see the verification status above for which checks did and did not hold."
	}
}

func buildCapabilities(inputs Inputs, statuses map[string]string) []Capability {
	source := inputs.Verification.Capabilities
	if len(source) == 0 && inputs.ManifestAvailable {
		source = inputs.Manifest.Capabilities
	}
	capabilities := make([]Capability, 0, len(source))
	for _, capability := range source {
		capabilities = append(capabilities, Capability{
			Name: capability.Name, State: string(capability.State), Note: capability.Note,
			RecoveryMeaning: capabilityMeaning(capability.State),
			ArchiveEvidence: capabilityEvidence(capability.State, inputs.Verification.Result),
		})
	}
	return capabilities
}

func buildAttachments(inputs Inputs, statuses map[string]string) Attachments {
	summary := Attachments{IntegrityCheck: statusOrMissing(statuses, "attachment_integrity")}
	for _, entity := range inputs.Verification.Reconciliation {
		if entity.Entity == "attachments" {
			summary.Expected = entity.Expected
			summary.Retrieved = entity.Archived
			summary.UnresolvedCount = entity.Unresolved
		}
	}
	if inputs.ManifestAvailable {
		for _, attachment := range inputs.Manifest.Attachments {
			if attachment.Status == "RETRIEVED" {
				continue
			}
			item := UnresolvedAttachment{
				SourceID: attachment.SourceID, ExpectedSize: attachment.ExpectedSize,
				Reason: "no reason recorded",
			}
			if attachment.Filename != nil {
				item.Filename = *attachment.Filename
			}
			if attachment.Error != nil {
				item.Reason = *attachment.Error
			}
			summary.Unresolved = append(summary.Unresolved, item)
		}
		sort.Slice(summary.Unresolved, func(i, j int) bool {
			return summary.Unresolved[i].SourceID < summary.Unresolved[j].SourceID
		})
	}
	switch summary.IntegrityCheck {
	case verify.CheckPass:
		summary.IntegrityNote = fmt.Sprintf("All %d archived attachment binaries exist and match their recorded SHA-256 checksums.", summary.Retrieved)
	case verify.CheckIncomplete:
		summary.IntegrityNote = fmt.Sprintf("The %d archived binaries match their recorded checksums, but %d supported attachments were never archived and are listed below.", summary.Retrieved, summary.UnresolvedCount)
	case verify.CheckFail:
		summary.IntegrityNote = "Attachment evidence failed verification. Archived binaries must not be assumed to be the files they claim to be."
	default:
		summary.IntegrityNote = "Attachment integrity was not established."
	}
	return summary
}

func buildDiscrepancies(inputs Inputs) []Item {
	items := make([]Item, 0)
	if inputs.ManifestAvailable {
		for _, discrepancy := range inputs.Manifest.Discrepancies {
			items = append(items, Item{
				Kind: discrepancy.Kind, SourceID: discrepancy.SourceID,
				Message: discrepancy.Message, Origin: "archive manifest",
			})
		}
	}
	for _, check := range inputs.Verification.Checks {
		if check.Status == verify.CheckPass || check.Status == verify.CheckUnproven {
			continue
		}
		if len(check.Findings) == 0 {
			items = append(items, Item{Kind: strings.ToUpper(check.Name), Message: check.Summary, Origin: "verification"})
			continue
		}
		for _, finding := range check.Findings {
			items = append(items, Item{Kind: strings.ToUpper(check.Name), Message: finding, Origin: "verification"})
		}
	}
	return items
}

// buildMustNotClaim states, from evidence, what this archive does not support
// claiming. It is deliberately the section that grows when evidence is weak.
func buildMustNotClaim(inputs Inputs, document Document, statuses map[string]string) []string {
	claims := make([]string, 0, 8)
	// The verification result is the authority here: document.Conclusion is
	// not populated until after this list is built.
	if inputs.Verification.Failed() {
		claims = append(claims, "Do not claim this archive as a whole is a proven recovery source: verification did not pass. The checks that did pass remain valid evidence for exactly what they cover, but they do not cover the failures listed above.")
	}
	if inputs.ManifestAvailable && !inputs.Manifest.InputSnapshot.Atomic {
		claims = append(claims, "Do not claim this archive is a point-in-time consistent snapshot of the Workspace. The source snapshot it was built from is recorded as non-atomic, so archived records may reflect different moments of the extraction.")
	}
	if !inputs.ManifestAvailable {
		claims = append(claims, "Do not claim anything about what this archive contains: its manifest could not be read, so the archive does not state what it is.")
	}
	claims = append(claims, "Do not claim executable source semantics for Custom Fields. Values are archived as observed data only; formulas, rollups and other computed fields are not reconstructable from this archive.")
	claims = append(claims, "Do not claim this archive reproduces ClickUp. It preserves only the portable representation described in schema.json; anything outside that representation, including permissions, automations, views and source-side rendering, is not archived.")
	for _, capability := range document.Capabilities {
		if connector.CapabilityState(capability.State) == connector.CapabilitySupported {
			continue
		}
		claims = append(claims, fmt.Sprintf("Do not claim %q is recoverable from this archive: it remains %s. %s", capability.Name, capability.State, capability.Note))
	}
	if document.Attachments.UnresolvedCount > 0 {
		claims = append(claims, fmt.Sprintf("Do not claim the %d unresolved attachments listed above were preserved. Their bytes are not in this archive.", document.Attachments.UnresolvedCount))
	}
	for _, discrepancy := range document.Discrepancies {
		if strings.HasPrefix(discrepancy.Kind, "RELATIONSHIP_") && discrepancy.Kind != "RELATIONSHIP_RESOLVED" {
			claims = append(claims, "Do not claim every relationship endpoint is archived. Some relationships point at records outside the archived record set and retain only the original source identifier.")
			break
		}
	}
	if statuses["source_object_reconciliation"] != verify.CheckPass {
		claims = append(claims, "Do not claim this archive contains everything the extraction observed: the source-object reconciliation did not pass.")
	}
	claims = append(claims, "Do not read this report as a statement about the source today. It describes archived evidence only; ALIH did not re-read ClickUp to produce it.")
	return dedupe(claims)
}

func buildConclusion(inputs Inputs, document Document) Conclusion {
	conclusion := Conclusion{Result: inputs.Verification.Result}
	switch inputs.Verification.Result {
	case verify.ResultVerified:
		conclusion.Verdict = "Archive integrity is proven within ALIH's supported scope."
		conclusion.Statements = []string{
			"Everything ALIH expected within its supported scope is archived, and every archived byte matches the evidence recorded for it.",
			"The archived data can be read and queried locally without ClickUp.",
		}
	case verify.ResultVerifiedWithLimitations:
		conclusion.Verdict = "Archive integrity is proven within ALIH's supported scope; the source limitations below still apply and this archive does not resolve them."
		conclusion.Statements = []string{
			"Everything ALIH expected within its supported scope is archived, and every archived byte matches the evidence recorded for it.",
			"The archived data can be read and queried locally without ClickUp.",
			"Source capabilities that are not SUPPORTED were never archived and remain outside anything this archive can prove; their state has not been changed by verification or by this report.",
		}
	case verify.ResultIncomplete:
		conclusion.Verdict = "This archive is NOT a complete recovery source. ALIH expected supported data that the archive does not contain."
		conclusion.Statements = []string{
			"Do not treat this archive as a complete copy of the supported scope.",
			"The unresolved items listed above were expected by ALIH and are missing from the archive; they must be recovered from the source or accepted as lost.",
			"Parts of the archive that did pass verification remain readable, but completeness is not established.",
		}
	default:
		outcome := summarizeChecks(inputs.Verification)
		conclusion.Verdict = "ALIH cannot prove this archive is intact. Do not rely on it for recovery."
		conclusion.Statements = []string{
			fmt.Sprintf("Verification failed on %s. Anything that depends on those checks is not established.", nameList(outcome.NotEstablished)),
			fmt.Sprintf("%d of %d checks did pass. What they prove is marked PROVEN in the recovery summary above and remains valid evidence for exactly that claim.", len(outcome.Established), outcome.Total),
			"A passing check does not make the archive as a whole safe to recover from: treat the proven parts as evidence, never as a completeness or integrity guarantee for the archive.",
			"Do not use this archive as a recovery source until the failures above are explained.",
			"ALIH has not modified or repaired this archive, and producing this report did not change it.",
		}
	}
	if inputs.ManifestAvailable && !inputs.Manifest.InputSnapshot.Atomic {
		conclusion.Statements = append(conclusion.Statements,
			"The source snapshot was non-atomic, so no point-in-time consistency claim is made about the Workspace at any single moment.")
	}
	if document.Attachments.UnresolvedCount > 0 {
		conclusion.Statements = append(conclusion.Statements,
			fmt.Sprintf("%d of %d supported attachments were never archived.", document.Attachments.UnresolvedCount, document.Attachments.Expected))
	}
	return conclusion
}

func statusOrMissing(statuses map[string]string, name string) string {
	if status, present := statuses[name]; present {
		return status
	}
	return "NOT_RUN"
}

func joinWithAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func dedupe(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
