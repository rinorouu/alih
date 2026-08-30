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

package archive

type columnSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Nullable    bool   `json:"nullable"`
	Description string `json:"description"`
}

type tableSpec struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Columns     []columnSpec `json:"columns"`
}

type schemaDocument struct {
	SchemaVersion int         `json:"schema_version"`
	Format        string      `json:"format"`
	Database      string      `json:"database"`
	Tables        []tableSpec `json:"tables"`
	Semantics     []string    `json:"semantics"`
}

var portableTableSpecs = []tableSpec{
	{Name: "archive_metadata", Description: "Archive-level values used by future independent verification.", Columns: []columnSpec{
		{Name: "key", Type: "TEXT", Description: "Stable metadata key."},
		{Name: "value", Type: "TEXT", Description: "Metadata value."},
	}},
	{Name: "workspaces", Description: "Portable workspace roots.", Columns: entityColumns(
		columnSpec{Name: "name", Type: "TEXT", Nullable: true, Description: "Observed workspace name."},
	)},
	{Name: "containers", Description: "Source-agnostic hierarchy containers such as spaces and folders.", Columns: entityColumns(
		columnSpec{Name: "kind", Type: "TEXT", Description: "Portable container kind."},
		columnSpec{Name: "workspace_id", Type: "TEXT", Description: "Owning portable workspace ID."},
		columnSpec{Name: "parent_id", Type: "TEXT", Nullable: true, Description: "Parent portable container ID."},
		columnSpec{Name: "name", Type: "TEXT", Nullable: true, Description: "Observed container name."},
	)},
	{Name: "collections", Description: "Portable collections containing records.", Columns: entityColumns(
		columnSpec{Name: "workspace_id", Type: "TEXT", Description: "Owning portable workspace ID."},
		columnSpec{Name: "container_id", Type: "TEXT", Description: "Parent portable container ID."},
		columnSpec{Name: "name", Type: "TEXT", Nullable: true, Description: "Observed collection name."},
	)},
	{Name: "records", Description: "Portable records and nested records.", Columns: entityColumns(
		columnSpec{Name: "kind", Type: "TEXT", Description: "record or nested-record classification."},
		columnSpec{Name: "workspace_id", Type: "TEXT", Description: "Owning portable workspace ID."},
		columnSpec{Name: "collection_id", Type: "TEXT", Description: "Owning portable collection ID."},
		columnSpec{Name: "parent_record_id", Type: "TEXT", Nullable: true, Description: "Parent portable record ID."},
		columnSpec{Name: "title", Type: "TEXT", Nullable: true, Description: "Observed title."},
		columnSpec{Name: "description", Type: "TEXT", Nullable: true, Description: "Observed source description."},
		columnSpec{Name: "text_content", Type: "TEXT", Nullable: true, Description: "Observed plain-text content."},
		columnSpec{Name: "status", Type: "TEXT", Nullable: true, Description: "Observed status name."},
		columnSpec{Name: "status_type", Type: "TEXT", Nullable: true, Description: "Observed status category."},
		columnSpec{Name: "priority", Type: "TEXT", Nullable: true, Description: "Observed priority."},
		columnSpec{Name: "archived", Type: "INTEGER", Nullable: true, Description: "Observed archived flag."},
		columnSpec{Name: "date_created_source", Type: "TEXT", Nullable: true, Description: "Unmodified source timestamp value."},
		columnSpec{Name: "date_updated_source", Type: "TEXT", Nullable: true, Description: "Unmodified source timestamp value."},
		columnSpec{Name: "date_closed_source", Type: "TEXT", Nullable: true, Description: "Unmodified source timestamp value."},
		columnSpec{Name: "date_done_source", Type: "TEXT", Nullable: true, Description: "Unmodified source timestamp value."},
		columnSpec{Name: "start_date_source", Type: "TEXT", Nullable: true, Description: "Unmodified source timestamp value."},
		columnSpec{Name: "due_date_source", Type: "TEXT", Nullable: true, Description: "Unmodified source timestamp value."},
		columnSpec{Name: "time_estimate_ms", Type: "INTEGER", Nullable: true, Description: "Observed time estimate in milliseconds."},
		columnSpec{Name: "time_spent_ms", Type: "INTEGER", Nullable: true, Description: "Observed time spent in milliseconds."},
		columnSpec{Name: "points", Type: "REAL", Nullable: true, Description: "Observed points value."},
	)},
	{Name: "identities", Description: "Portable identities observed on supported records and comments.", Columns: entityColumns(
		columnSpec{Name: "username", Type: "TEXT", Nullable: true, Description: "Observed display name."},
		columnSpec{Name: "email", Type: "TEXT", Nullable: true, Description: "Observed email when supplied by the API."},
	)},
	{Name: "record_identities", Description: "Ordered roles connecting identities to records.", Columns: []columnSpec{
		{Name: "record_id", Type: "TEXT", Description: "Portable record ID."},
		{Name: "identity_id", Type: "TEXT", Description: "Portable identity ID."},
		{Name: "role", Type: "TEXT", Description: "Observed role such as creator, assignee, or watcher."},
		{Name: "position", Type: "INTEGER", Description: "Stable observed array position."},
	}},
	{Name: "record_tags", Description: "Ordered tags observed on records.", Columns: []columnSpec{
		{Name: "record_id", Type: "TEXT", Description: "Portable record ID."},
		{Name: "name", Type: "TEXT", Description: "Observed tag name."},
		{Name: "position", Type: "INTEGER", Description: "Stable observed array position."},
	}},
	{Name: "comments", Description: "Portable record comments and threaded replies.", Columns: entityColumns(
		columnSpec{Name: "workspace_id", Type: "TEXT", Description: "Owning portable workspace ID."},
		columnSpec{Name: "record_id", Type: "TEXT", Description: "Owning portable record ID."},
		columnSpec{Name: "parent_comment_id", Type: "TEXT", Nullable: true, Description: "Parent portable comment for a threaded reply."},
		columnSpec{Name: "author_identity_id", Type: "TEXT", Nullable: true, Description: "Observed author identity."},
		columnSpec{Name: "body_text", Type: "TEXT", Nullable: true, Description: "Best direct text projection without invented formatting."},
		columnSpec{Name: "body_json", Type: "TEXT", Nullable: true, Description: "Canonical observed structured comment body."},
		columnSpec{Name: "date_created_source", Type: "TEXT", Nullable: true, Description: "Unmodified source timestamp value."},
	)},
	{Name: "field_definitions", Description: "Portable field definitions with source definition evidence.", Columns: entityColumns(
		columnSpec{Name: "name", Type: "TEXT", Nullable: true, Description: "Observed field name."},
		columnSpec{Name: "field_type", Type: "TEXT", Nullable: true, Description: "Observed source field type."},
		columnSpec{Name: "semantics_state", Type: "TEXT", Description: "Whether only observed/source definition semantics are preserved."},
		columnSpec{Name: "definition_json", Type: "TEXT", Description: "Canonical source definition JSON."},
	)},
	{Name: "record_field_values", Description: "Observed values without executable computed-field claims.", Columns: []columnSpec{
		{Name: "record_id", Type: "TEXT", Description: "Portable record ID."},
		{Name: "field_id", Type: "TEXT", Description: "Portable field definition ID."},
		{Name: "observed_value_json", Type: "TEXT", Description: "Canonical observed JSON value, including explicit null."},
		{Name: "semantics_state", Type: "TEXT", Description: "Always OBSERVED_ONLY in M4."},
	}},
	{Name: "relationships", Description: "Portable directed or undirected record relationships.", Columns: entityColumns(
		columnSpec{Name: "kind", Type: "TEXT", Description: "Portable relationship kind."},
		columnSpec{Name: "from_record_id", Type: "TEXT", Nullable: true, Description: "Resolved portable source endpoint."},
		columnSpec{Name: "to_record_id", Type: "TEXT", Nullable: true, Description: "Resolved portable target endpoint."},
		columnSpec{Name: "from_source_id", Type: "TEXT", Description: "Original source endpoint ID."},
		columnSpec{Name: "to_source_id", Type: "TEXT", Description: "Original source endpoint ID."},
		columnSpec{Name: "resolution_state", Type: "TEXT", Description: "RESOLVED or explicit partial external state."},
		columnSpec{Name: "source_metadata_json", Type: "TEXT", Description: "Canonical observed relationship metadata."},
	)},
	{Name: "attachments", Description: "Expected attachment metadata and binary retrieval outcomes.", Columns: entityColumns(
		columnSpec{Name: "workspace_id", Type: "TEXT", Description: "Owning portable workspace ID."},
		columnSpec{Name: "record_id", Type: "TEXT", Description: "Owning portable record ID."},
		columnSpec{Name: "filename", Type: "TEXT", Nullable: true, Description: "Original observed filename."},
		columnSpec{Name: "media_type", Type: "TEXT", Nullable: true, Description: "Observed media type."},
		columnSpec{Name: "expected_size", Type: "INTEGER", Nullable: true, Description: "Observed expected byte size."},
		columnSpec{Name: "source_url", Type: "TEXT", Nullable: true, Description: "Source URL with query and fragment removed."},
		columnSpec{Name: "download_status", Type: "TEXT", Description: "RETRIEVED or UNRESOLVED."},
		columnSpec{Name: "local_path", Type: "TEXT", Nullable: true, Description: "Archive-relative binary path."},
		columnSpec{Name: "archived_size", Type: "INTEGER", Nullable: true, Description: "Retrieved byte size."},
		columnSpec{Name: "checksum", Type: "TEXT", Nullable: true, Description: "SHA-256 of retrieved binary."},
		columnSpec{Name: "error", Type: "TEXT", Nullable: true, Description: "Sanitized explicit unresolved reason."},
	)},
}

func entityColumns(extra ...columnSpec) []columnSpec {
	base := []columnSpec{
		{Name: "id", Type: "TEXT", Description: "Deterministic portable ID."},
	}
	base = append(base, extra...)
	return append(base,
		columnSpec{Name: "source_provider", Type: "TEXT", Description: "Original source provider."},
		columnSpec{Name: "source_type", Type: "TEXT", Description: "Original source object type."},
		columnSpec{Name: "source_id", Type: "TEXT", Description: "Original source identifier."},
		columnSpec{Name: "source_raw_path", Type: "TEXT", Description: "Archive-relative immutable raw evidence path."},
		columnSpec{Name: "source_id_composite", Type: "INTEGER", Description: "1 only when the API exposes no relationship object ID."},
	)
}

func portableSchemaDocument() schemaDocument {
	return schemaDocument{
		SchemaVersion: 1,
		Format:        "alih-portable-sqlite",
		Database:      "alih.db",
		Tables:        portableTableSpecs,
		Semantics: []string{
			"All portable IDs are deterministic mappings; original source IDs remain in source_id columns.",
			"Null means the source did not provide a usable value; Alih does not invent replacements.",
			"Custom Field observed values are data, not executable source semantics.",
			"M4 creation does not imply M5 verification.",
		},
	}
}
