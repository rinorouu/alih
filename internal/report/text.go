package report

import (
	"fmt"
	"io"
	"strings"

	"alih/internal/verify"
)

// RenderText writes the human-readable recovery report as plain text. The
// section order is fixed so a reader always finds the failure states before
// the recovery conclusion.
func RenderText(output io.Writer, document Document) error {
	writer := &textWriter{output: output}

	writer.line("ALI H — RECOVERY REPORT")
	writer.line("")
	writer.line("Generated: %s (from archived evidence only; the source was not contacted)", document.GeneratedAt.Format("2006-01-02 15:04:05 UTC"))

	writer.section("1. ARCHIVE IDENTITY")
	identity := document.Identity
	writer.field("Archive", identity.ArchivePath)
	writer.field("Connector", orUnknown(identity.Connector))
	writer.field("Source workspace", fmt.Sprintf("%s (ID: %s)", orUnknown(identity.WorkspaceName), orUnknown(identity.WorkspaceID)))
	if identity.CreatedAt != nil {
		writer.field("Archive created", identity.CreatedAt.Format("2006-01-02 15:04:05 UTC"))
	} else {
		writer.field("Archive created", "unknown: the archive does not record a usable creation time")
	}
	writer.field("Created by", orUnknown(identity.CreatedByAlihVersion))
	writer.field("Recorded archive status", orUnknown(identity.RecordedStatus))
	writer.field("Source snapshot digest", orUnknown(identity.SourceSnapshotDigest))
	if identity.SourceSnapshotAtomic {
		writer.field("Source snapshot", "atomic")
	} else {
		writer.field("Source snapshot", "NON-ATOMIC — records may reflect different moments of the extraction")
	}
	writer.field("Files recorded in manifest", fmt.Sprintf("%d", identity.RecordedFiles))
	if !identity.ManifestReadable {
		writer.line("")
		writer.line("  manifest.json could not be read: %s", orUnknown(identity.ManifestError))
		writer.line("  The archive therefore does not state what it is.")
	}

	writer.section("2. VERIFICATION STATUS")
	writer.field("Result", document.Verification.Result)
	writer.line("")
	writer.wrap("  ", document.Verification.Headline)
	writer.wrap("  ", document.Verification.FiguresTrust)
	writer.line("")
	for _, status := range []string{verify.CheckPass, verify.CheckFail, verify.CheckIncomplete, verify.CheckUnproven, verify.CheckNotEvaluated} {
		if count := document.Verification.Counts[status]; count > 0 {
			writer.line("  %-14s %d", status, count)
		}
	}
	writer.line("")
	for _, check := range document.Verification.Checks {
		writer.line("  %-32s %s", check.Name, check.Status)
		if check.Status == verify.CheckPass {
			continue
		}
		writer.wrap("      ", check.Summary)
		for _, finding := range check.Findings {
			writer.wrap("      - ", finding)
		}
	}

	writer.section("3. RECOVERY SUMMARY")
	writer.line("  What this archive supports, and what it does not, according to verification.")
	writer.line("")
	for _, statement := range document.Recovery {
		marker := "PROVEN    "
		if !statement.Proven {
			marker = "NOT PROVEN"
		}
		writer.wrap("  ["+marker+"] ", statement.Claim)
		if !statement.Proven {
			writer.wrap("      ", statement.Reason)
		}
	}

	writer.section("4. ENTITY COVERAGE")
	if len(document.Coverage) == 0 {
		writer.line("  No entity coverage could be established.")
	} else {
		writer.line("  %-16s %10s %10s %12s  %s", "entity", "expected", "archived", "unresolved", "status")
		for _, entity := range document.Coverage {
			writer.line("  %-16s %10d %10d %12d  %s", entity.Entity, entity.Expected, entity.Archived, entity.Unresolved, entity.Status)
		}
	}

	writer.section("5. ATTACHMENTS")
	attachments := document.Attachments
	writer.field("Expected", fmt.Sprintf("%d", attachments.Expected))
	writer.field("Preserved", fmt.Sprintf("%d", attachments.Retrieved))
	writer.field("Not preserved", fmt.Sprintf("%d", attachments.UnresolvedCount))
	writer.field("Integrity check", attachments.IntegrityCheck)
	writer.line("")
	writer.wrap("  ", attachments.IntegrityNote)
	if len(attachments.Unresolved) > 0 {
		writer.line("")
		writer.line("  Attachments that were expected but not archived:")
		for _, item := range attachments.Unresolved {
			name := item.Filename
			if name == "" {
				name = "<no filename recorded>"
			}
			writer.line("  - source id %s (%s)", item.SourceID, name)
			writer.wrap("      reason: ", item.Reason)
		}
	}

	writer.section("6. CAPABILITY COVERAGE")
	if len(document.Capabilities) == 0 {
		writer.line("  The archive declares no source capabilities, so its scope is not established.")
	}
	for _, capability := range document.Capabilities {
		writer.line("  %-22s %s", capability.Name, capability.State)
		if capability.Note != "" {
			writer.wrap("      source note: ", capability.Note)
		}
		writer.wrap("      source scope: ", capability.RecoveryMeaning)
		writer.wrap("      this archive: ", capability.ArchiveEvidence)
	}

	writer.section("7. LIMITATIONS AND UNPROVEN CLAIMS")
	if len(document.Limitations) == 0 {
		writer.line("  None recorded.")
	}
	for _, limitation := range document.Limitations {
		writer.wrap("  - ", limitation)
	}

	writer.section("8. DISCREPANCIES AND UNRESOLVED ITEMS")
	if len(document.Discrepancies) == 0 {
		writer.line("  None. Neither the archive nor verification recorded an unresolved item.")
	}
	for _, item := range document.Discrepancies {
		if item.SourceID != "" {
			writer.line("  - [%s] source id %s (%s)", item.Kind, item.SourceID, item.Origin)
		} else {
			writer.line("  - [%s] (%s)", item.Kind, item.Origin)
		}
		writer.wrap("      ", item.Message)
	}

	writer.section("9. RECOVERY CONCLUSION")
	writer.line("  %s", document.Conclusion.Result)
	writer.line("")
	writer.wrap("  ", document.Conclusion.Verdict)
	writer.line("")
	for _, statement := range document.Conclusion.Statements {
		writer.wrap("  - ", statement)
	}
	writer.line("")
	writer.line("  What must NOT be claimed from this archive:")
	for _, claim := range document.MustNotClaim {
		writer.wrap("  - ", claim)
	}
	writer.line("")
	writer.line("  No source data modified. No archive data modified.")
	return writer.err
}

type textWriter struct {
	output io.Writer
	err    error
}

func (writer *textWriter) line(format string, arguments ...any) {
	if writer.err != nil {
		return
	}
	_, writer.err = fmt.Fprintf(writer.output, sanitize(format)+"\n", sanitizeArguments(arguments)...)
}

func (writer *textWriter) section(title string) {
	writer.line("")
	writer.line("%s", title)
	writer.line("%s", strings.Repeat("-", len(title)))
}

func (writer *textWriter) field(name, value string) {
	writer.line("  %-28s %s", name, value)
}

// wrap prints long evidence text at a readable width without truncating it,
// because a finding that is cut short is a finding that can be missed.
func (writer *textWriter) wrap(prefix, text string) {
	const width = 92
	continuation := strings.Repeat(" ", len(prefix))
	words := strings.Fields(text)
	if len(words) == 0 {
		return
	}
	current := prefix
	empty := true
	for _, word := range words {
		if !empty && len(current)+1+len(word) > width {
			writer.line("%s", current)
			current = continuation
			empty = true
		}
		if empty {
			current += word
			empty = false
			continue
		}
		current += " " + word
	}
	if !empty {
		writer.line("%s", current)
	}
}

func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

// sanitize keeps archived source content from injecting control characters
// into a terminal while leaving the content itself intact.
func sanitize(value string) string {
	return strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' {
			return character
		}
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, value)
}

func sanitizeArguments(arguments []any) []any {
	sanitized := make([]any, len(arguments))
	for index, argument := range arguments {
		if text, isText := argument.(string); isText {
			sanitized[index] = sanitize(text)
			continue
		}
		sanitized[index] = argument
	}
	return sanitized
}
