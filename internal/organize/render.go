// Copyright 2025 rinorouu
// Licensed under the Apache License, Version 2.0.

package organize

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

func (builder *viewBuilder) rootReadme(workspace namedEntity) string {
	var output strings.Builder
	output.WriteString("# Alih Organized View\n\n")
	output.WriteString("This is a disposable, read-only browsing view derived from a canonical Alih archive. It is not the archive and must not be used as a restore source. Regenerate it instead of editing or merging it.\n\n")
	writeMeta(&output, "Workspace", pointerValue(workspace.Name, "unnamed"))
	writeMeta(&output, "Workspace portable ID", workspace.ID)
	writeMeta(&output, "Source ID", workspace.Source.ID)
	writeMeta(&output, "Verification", builder.report.Result)
	writeMeta(&output, "Manifest checksum", builder.manifestChecksum)
	output.WriteString("\n## Limitations\n\n")
	for _, limitation := range builder.report.Limitations {
		output.WriteString("- ")
		output.WriteString(markdownInline(limitation))
		output.WriteByte('\n')
	}
	output.WriteString("\n`provenance.json` maps every generated content file to its portable and original source identity. It intentionally excludes itself to avoid self-reference.\n")
	return output.String()
}

func (builder *viewBuilder) entityIndex(label string, entity namedEntity, parent *namedEntity) string {
	var output strings.Builder
	output.WriteString("# ")
	output.WriteString(markdownInline(pointerValue(entity.Name, label)))
	output.WriteString("\n\n")
	writeMeta(&output, "Kind", label)
	writeMeta(&output, "Portable ID", entity.ID)
	writeMeta(&output, "Source type", entity.Source.Type)
	writeMeta(&output, "Source ID", entity.Source.ID)
	writeMeta(&output, "Raw evidence path", entity.Source.RawPath)
	if parent != nil {
		writeMeta(&output, "Parent portable ID", parent.ID)
		writeMeta(&output, "Parent source ID", parent.Source.ID)
	}
	return output.String()
}

func (builder *viewBuilder) recordMarkdown(item record) (string, error) {
	var output strings.Builder
	output.WriteString("# ")
	output.WriteString(markdownInline(pointerValue(item.Title, item.Kind)))
	output.WriteString("\n\n")
	writeMeta(&output, "Kind", item.Kind)
	writeMeta(&output, "Portable ID", item.ID)
	writeMeta(&output, "Source type", item.Source.Type)
	writeMeta(&output, "Source ID", item.Source.ID)
	writeMeta(&output, "Raw evidence path", item.Source.RawPath)
	writeOptional(&output, "Parent record portable ID", item.ParentRecordID)
	writeOptional(&output, "Status", item.Status)
	writeOptional(&output, "Status type", item.StatusType)
	writeOptional(&output, "Priority", item.Priority)
	if item.Archived != nil {
		writeMeta(&output, "Archived", fmt.Sprintf("%t", *item.Archived))
	}
	for _, field := range []struct {
		label string
		value *string
	}{{"Created at source", item.Created}, {"Updated at source", item.Updated}, {"Closed at source", item.Closed}, {"Done at source", item.Done}, {"Start at source", item.Start}, {"Due at source", item.Due}} {
		writeOptional(&output, field.label, field.value)
	}
	if item.EstimateMS != nil {
		writeMeta(&output, "Time estimate ms", fmt.Sprintf("%d", *item.EstimateMS))
	}
	if item.SpentMS != nil {
		writeMeta(&output, "Time spent ms", fmt.Sprintf("%d", *item.SpentMS))
	}
	if item.Points != nil {
		writeMeta(&output, "Points", fmt.Sprintf("%g", *item.Points))
	}
	writeText(&output, "Description", item.Description)
	writeText(&output, "Text content", item.TextContent)
	if err := builder.renderTags(&output, item.ID); err != nil {
		return "", err
	}
	if err := builder.renderIdentities(&output, item.ID); err != nil {
		return "", err
	}
	if err := builder.renderFields(&output, item.ID); err != nil {
		return "", err
	}
	if err := builder.renderComments(&output, item.ID); err != nil {
		return "", err
	}
	if err := builder.renderRelationships(&output, item.ID); err != nil {
		return "", err
	}
	if err := builder.renderAttachmentIndex(&output, item.ID); err != nil {
		return "", err
	}
	return output.String(), nil
}

func (builder *viewBuilder) renderTags(output *strings.Builder, recordID string) error {
	rows, err := builder.database.QueryContext(builder.ctx, `SELECT name,position FROM record_tags WHERE record_id=? ORDER BY position,name`, recordID)
	if err != nil {
		return err
	}
	defer rows.Close()
	first := true
	for rows.Next() {
		var name string
		var position int
		if err := rows.Scan(&name, &position); err != nil {
			return err
		}
		if first {
			output.WriteString("\n## Tags\n\n")
			first = false
		}
		fmt.Fprintf(output, "- %d: %s\n", position, markdownInline(name))
	}
	return rows.Err()
}

func (builder *viewBuilder) renderIdentities(output *strings.Builder, recordID string) error {
	rows, err := builder.database.QueryContext(builder.ctx, `SELECT ri.role,ri.position,i.id,i.username,i.email,i.source_id,i.source_raw_path
		FROM record_identities ri JOIN identities i ON i.id=ri.identity_id WHERE ri.record_id=? ORDER BY ri.role,ri.position,i.id`, recordID)
	if err != nil {
		return err
	}
	defer rows.Close()
	first := true
	for rows.Next() {
		var role, id, sourceID, rawPath string
		var position int
		var username, email *string
		if err := rows.Scan(&role, &position, &id, &username, &email, &sourceID, &rawPath); err != nil {
			return err
		}
		if first {
			output.WriteString("\n## Identities\n\n")
			first = false
		}
		fmt.Fprintf(output, "- %s %d: %s; portable `%s`; source `%s`; raw `%s`", markdownInline(role), position, markdownInline(pointerValue(username, "unnamed")), markdownInline(id), markdownInline(sourceID), markdownInline(rawPath))
		if email != nil {
			fmt.Fprintf(output, "; email %s", markdownInline(*email))
		}
		output.WriteByte('\n')
	}
	return rows.Err()
}

func (builder *viewBuilder) renderFields(output *strings.Builder, recordID string) error {
	rows, err := builder.database.QueryContext(builder.ctx, `SELECT f.id,f.name,f.field_type,v.semantics_state,v.observed_value_json,f.source_id,f.source_raw_path
		FROM record_field_values v JOIN field_definitions f ON f.id=v.field_id WHERE v.record_id=? ORDER BY f.id`, recordID)
	if err != nil {
		return err
	}
	defer rows.Close()
	first := true
	for rows.Next() {
		var id, semantics, observed, sourceID, rawPath string
		var name, fieldType *string
		if err := rows.Scan(&id, &name, &fieldType, &semantics, &observed, &sourceID, &rawPath); err != nil {
			return err
		}
		if first {
			output.WriteString("\n## Custom fields\n")
			first = false
		}
		output.WriteString("\n### ")
		output.WriteString(markdownInline(pointerValue(name, "unnamed field")))
		output.WriteString("\n\n")
		writeMeta(output, "Portable field ID", id)
		writeOptional(output, "Field type", fieldType)
		writeMeta(output, "Semantics", semantics)
		writeMeta(output, "Source ID", sourceID)
		writeMeta(output, "Raw evidence path", rawPath)
		writeCode(output, observed)
	}
	return rows.Err()
}

func (builder *viewBuilder) renderComments(output *strings.Builder, recordID string) error {
	rows, err := builder.database.QueryContext(builder.ctx, `SELECT c.id,c.parent_comment_id,c.body_text,c.body_json,c.date_created_source,c.source_id,c.source_raw_path,i.username,i.source_id
		FROM comments c LEFT JOIN identities i ON i.id=c.author_identity_id WHERE c.record_id=? ORDER BY c.date_created_source IS NULL, c.date_created_source, c.id`, recordID)
	if err != nil {
		return err
	}
	defer rows.Close()
	first := true
	for rows.Next() {
		var id, sourceID, rawPath string
		var parent, body, bodyJSON, created, author, authorSourceID *string
		if err := rows.Scan(&id, &parent, &body, &bodyJSON, &created, &sourceID, &rawPath, &author, &authorSourceID); err != nil {
			return err
		}
		if first {
			output.WriteString("\n## Comments\n")
			first = false
		}
		output.WriteString("\n### Comment ")
		output.WriteString(markdownInline(id))
		output.WriteString("\n\n")
		writeMeta(output, "Source ID", sourceID)
		writeMeta(output, "Raw evidence path", rawPath)
		writeOptional(output, "Parent comment portable ID", parent)
		writeOptional(output, "Created at source", created)
		if author != nil {
			writeMeta(output, "Author", *author)
		}
		if authorSourceID != nil {
			writeMeta(output, "Author source ID", *authorSourceID)
		}
		switch {
		case body != nil:
			writeCode(output, *body)
		case bodyJSON != nil:
			writeCode(output, canonicalJSON(*bodyJSON))
		default:
			output.WriteString("_This comment archived no readable body._\n")
		}
	}
	return rows.Err()
}

func (builder *viewBuilder) renderRelationships(output *strings.Builder, recordID string) error {
	rows, err := builder.database.QueryContext(builder.ctx, `SELECT id,kind,from_record_id,to_record_id,from_source_id,to_source_id,resolution_state,source_id,source_raw_path
		FROM relationships WHERE from_record_id=? OR to_record_id=? ORDER BY id`, recordID, recordID)
	if err != nil {
		return err
	}
	defer rows.Close()
	first := true
	for rows.Next() {
		var id, kind, fromSource, toSource, resolution, sourceID, rawPath string
		var fromID, toID *string
		if err := rows.Scan(&id, &kind, &fromID, &toID, &fromSource, &toSource, &resolution, &sourceID, &rawPath); err != nil {
			return err
		}
		if first {
			output.WriteString("\n## Relationships\n\n")
			first = false
		}
		fmt.Fprintf(output, "- %s `%s`: from source `%s` (%s) to source `%s` (%s); %s; raw `%s`\n",
			markdownInline(kind), markdownInline(id), markdownInline(fromSource), markdownInline(pointerValue(fromID, "external")),
			markdownInline(toSource), markdownInline(pointerValue(toID, "external")), markdownInline(resolution), markdownInline(rawPath))
	}
	return rows.Err()
}

func (builder *viewBuilder) renderAttachmentIndex(output *strings.Builder, recordID string) error {
	rows, err := builder.database.QueryContext(builder.ctx, `SELECT id,filename,media_type,download_status,checksum,source_id,source_raw_path FROM attachments WHERE record_id=? ORDER BY id`, recordID)
	if err != nil {
		return err
	}
	defer rows.Close()
	first := true
	for rows.Next() {
		var id, status, sourceID, rawPath string
		var filename, mediaType, checksum *string
		if err := rows.Scan(&id, &filename, &mediaType, &status, &checksum, &sourceID, &rawPath); err != nil {
			return err
		}
		if first {
			output.WriteString("\n## Attachments\n\n")
			first = false
		}
		fmt.Fprintf(output, "- %s; portable `%s`; source `%s`; status %s", markdownInline(pointerValue(filename, "unnamed")), markdownInline(id), markdownInline(sourceID), markdownInline(status))
		if mediaType != nil {
			fmt.Fprintf(output, "; media type %s", markdownInline(*mediaType))
		}
		if checksum != nil {
			fmt.Fprintf(output, "; checksum `%s`", markdownInline(*checksum))
		}
		fmt.Fprintf(output, "; raw `%s`\n", markdownInline(rawPath))
	}
	return rows.Err()
}

func writeMeta(output *strings.Builder, label, value string) {
	output.WriteString("- ")
	output.WriteString(markdownInline(label))
	output.WriteString(": `")
	output.WriteString(markdownInline(value))
	output.WriteString("`\n")
}

func writeOptional(output *strings.Builder, label string, value *string) {
	if value != nil {
		writeMeta(output, label, *value)
	}
}

func writeText(output *strings.Builder, label string, value *string) {
	if value == nil {
		return
	}
	output.WriteString("\n## ")
	output.WriteString(markdownInline(label))
	output.WriteString("\n\n")
	writeCode(output, *value)
}

func writeCode(output *strings.Builder, value string) {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	for _, line := range strings.Split(value, "\n") {
		output.WriteString("    ")
		output.WriteString(line)
		output.WriteByte('\n')
	}
}

func markdownInline(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, "`", "&#96;")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func canonicalJSON(value string) string {
	var parsed any
	if json.Unmarshal([]byte(value), &parsed) != nil {
		return value
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if encoder.Encode(parsed) != nil {
		return value
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

func entryFor(path string, entity namedEntity) provenanceEntry {
	return provenanceEntry{Path: filepathSlash(path), PortableID: entity.ID, SourceProvider: entity.Source.Provider, SourceType: entity.Source.Type, SourceID: entity.Source.ID, SourceRawPath: entity.Source.RawPath}
}

func filepathSlash(path string) string { return strings.ReplaceAll(path, "\\", "/") }

var _ = sql.ErrNoRows
