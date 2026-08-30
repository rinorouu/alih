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

package verify

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	_ "github.com/mattn/go-sqlite3"
)

// sourceRef is the retained provider identity of one archived portable entity.
type sourceRef struct {
	Provider  string
	Type      string
	ID        string
	RawPath   string
	Composite bool
}

type entityRow struct {
	ID     string
	Source sourceRef
}

type containerRow struct {
	Entity      entityRow
	Kind        string
	WorkspaceID string
	ParentID    *string
}

type collectionRow struct {
	Entity      entityRow
	WorkspaceID string
	ContainerID string
}

type recordRow struct {
	Entity         entityRow
	Kind           string
	WorkspaceID    string
	CollectionID   string
	ParentRecordID *string
}

type recordIdentityRow struct {
	RecordID   string
	IdentityID string
	Role       string
	Position   int
}

type recordTagRow struct {
	RecordID string
	Name     string
	Position int
}

type commentRow struct {
	Entity           entityRow
	WorkspaceID      string
	RecordID         string
	ParentCommentID  *string
	AuthorIdentityID *string
}

type fieldRow struct {
	Entity         entityRow
	Name           *string
	FieldType      *string
	SemanticsState string
	DefinitionJSON string
}

type fieldValueRow struct {
	RecordID          string
	FieldID           string
	ObservedValueJSON string
	SemanticsState    string
}

type relationshipRow struct {
	Entity          entityRow
	Kind            string
	FromRecordID    *string
	ToRecordID      *string
	FromSourceID    string
	ToSourceID      string
	ResolutionState string
}

type attachmentRow struct {
	Entity         entityRow
	WorkspaceID    string
	RecordID       string
	Filename       *string
	MediaType      *string
	ExpectedSize   *int64
	DownloadStatus string
	LocalPath      *string
	ArchivedSize   *int64
	Checksum       *string
	Error          *string
}

// portableDatabase is the fully loaded content of alih.db. M4 already builds
// the whole archive in memory, so loading it back for verification keeps the
// checks explicit and auditable rather than spread across ad-hoc queries.
type portableDatabase struct {
	Metadata         map[string]string
	Tables           []string
	Columns          map[string][]string
	Workspaces       []entityRow
	Containers       []containerRow
	Collections      []collectionRow
	Records          []recordRow
	Identities       []entityRow
	RecordIdentities []recordIdentityRow
	RecordTags       []recordTagRow
	Comments         []commentRow
	Fields           []fieldRow
	FieldValues      []fieldValueRow
	Relationships    []relationshipRow
	Attachments      []attachmentRow
}

// openReadOnly opens the archived database without any possibility of writing
// to it. immutable=1 also prevents SQLite from creating journal or WAL files
// beside the archived evidence.
func openReadOnly(path string) (*sql.DB, error) {
	databaseURL := (&url.URL{Scheme: "file", Path: path}).String()
	database, err := sql.Open("sqlite3", databaseURL+"?mode=ro&immutable=1&_query_only=1")
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func integrityCheck(database *sql.DB) ([]string, error) {
	var problems []string
	rows, err := database.Query(`PRAGMA integrity_check`)
	if err != nil {
		return nil, fmt.Errorf("run integrity_check: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		if value != "ok" {
			problems = append(problems, "integrity_check: "+value)
		}
	}
	return problems, rows.Err()
}

// foreignKeyCheck reports SQLite's own view of the declared foreign keys. It is
// reported with the explicit referential checks rather than with database
// integrity, because a tampered schema can drop a constraint without
// corrupting the file.
func foreignKeyCheck(database *sql.DB) ([]string, error) {
	rows, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return nil, fmt.Errorf("run foreign_key_check: %w", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var problems []string
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		problems = append(problems, fmt.Sprintf("SQLite foreign_key_check reported a violation: %v", values))
	}
	return problems, rows.Err()
}

func loadPortableDatabase(path string) (*portableDatabase, error) {
	database, err := openReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer database.Close()

	loaded := &portableDatabase{Metadata: map[string]string{}, Columns: map[string][]string{}}
	if err := scanRows(database, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`, func(rows *sql.Rows) error {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		loaded.Tables = append(loaded.Tables, name)
		return nil
	}); err != nil {
		return nil, err
	}
	for _, table := range loaded.Tables {
		columns, err := tableColumns(database, table)
		if err != nil {
			return nil, err
		}
		loaded.Columns[table] = columns
	}
	if err := scanRows(database, `SELECT key,value FROM archive_metadata`, func(rows *sql.Rows) error {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		if _, duplicate := loaded.Metadata[key]; duplicate {
			return fmt.Errorf("duplicate archive_metadata key %q", key)
		}
		loaded.Metadata[key] = value
		return nil
	}); err != nil {
		return nil, err
	}

	if err := scanRows(database, `SELECT id,`+sourceColumns+` FROM workspaces ORDER BY id`, func(rows *sql.Rows) error {
		row := entityRow{}
		values := append([]any{&row.ID}, sourcePointers(&row.Source)...)
		if err := rows.Scan(values...); err != nil {
			return err
		}
		loaded.Workspaces = append(loaded.Workspaces, row)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanRows(database, `SELECT id,kind,workspace_id,parent_id,`+sourceColumns+` FROM containers ORDER BY id`, func(rows *sql.Rows) error {
		row := containerRow{}
		var parent sql.NullString
		values := append([]any{&row.Entity.ID, &row.Kind, &row.WorkspaceID, &parent}, sourcePointers(&row.Entity.Source)...)
		if err := rows.Scan(values...); err != nil {
			return err
		}
		row.ParentID = nullableString(parent)
		loaded.Containers = append(loaded.Containers, row)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanRows(database, `SELECT id,workspace_id,container_id,`+sourceColumns+` FROM collections ORDER BY id`, func(rows *sql.Rows) error {
		row := collectionRow{}
		values := append([]any{&row.Entity.ID, &row.WorkspaceID, &row.ContainerID}, sourcePointers(&row.Entity.Source)...)
		if err := rows.Scan(values...); err != nil {
			return err
		}
		loaded.Collections = append(loaded.Collections, row)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanRows(database, `SELECT id,kind,workspace_id,collection_id,parent_record_id,`+sourceColumns+` FROM records ORDER BY id`, func(rows *sql.Rows) error {
		row := recordRow{}
		var parent sql.NullString
		values := append([]any{&row.Entity.ID, &row.Kind, &row.WorkspaceID, &row.CollectionID, &parent}, sourcePointers(&row.Entity.Source)...)
		if err := rows.Scan(values...); err != nil {
			return err
		}
		row.ParentRecordID = nullableString(parent)
		loaded.Records = append(loaded.Records, row)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanRows(database, `SELECT id,`+sourceColumns+` FROM identities ORDER BY id`, func(rows *sql.Rows) error {
		row := entityRow{}
		values := append([]any{&row.ID}, sourcePointers(&row.Source)...)
		if err := rows.Scan(values...); err != nil {
			return err
		}
		loaded.Identities = append(loaded.Identities, row)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanRows(database, `SELECT record_id,identity_id,role,position FROM record_identities ORDER BY record_id,role,position`, func(rows *sql.Rows) error {
		row := recordIdentityRow{}
		if err := rows.Scan(&row.RecordID, &row.IdentityID, &row.Role, &row.Position); err != nil {
			return err
		}
		loaded.RecordIdentities = append(loaded.RecordIdentities, row)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanRows(database, `SELECT record_id,name,position FROM record_tags ORDER BY record_id,position`, func(rows *sql.Rows) error {
		row := recordTagRow{}
		if err := rows.Scan(&row.RecordID, &row.Name, &row.Position); err != nil {
			return err
		}
		loaded.RecordTags = append(loaded.RecordTags, row)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanRows(database, `SELECT id,workspace_id,record_id,parent_comment_id,author_identity_id,`+sourceColumns+` FROM comments ORDER BY id`, func(rows *sql.Rows) error {
		row := commentRow{}
		var parent, author sql.NullString
		values := append([]any{&row.Entity.ID, &row.WorkspaceID, &row.RecordID, &parent, &author}, sourcePointers(&row.Entity.Source)...)
		if err := rows.Scan(values...); err != nil {
			return err
		}
		row.ParentCommentID = nullableString(parent)
		row.AuthorIdentityID = nullableString(author)
		loaded.Comments = append(loaded.Comments, row)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanRows(database, `SELECT id,name,field_type,semantics_state,definition_json,`+sourceColumns+` FROM field_definitions ORDER BY id`, func(rows *sql.Rows) error {
		row := fieldRow{}
		var name, fieldType sql.NullString
		values := append([]any{&row.Entity.ID, &name, &fieldType, &row.SemanticsState, &row.DefinitionJSON}, sourcePointers(&row.Entity.Source)...)
		if err := rows.Scan(values...); err != nil {
			return err
		}
		row.Name = nullableString(name)
		row.FieldType = nullableString(fieldType)
		loaded.Fields = append(loaded.Fields, row)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanRows(database, `SELECT record_id,field_id,observed_value_json,semantics_state FROM record_field_values ORDER BY record_id,field_id`, func(rows *sql.Rows) error {
		row := fieldValueRow{}
		if err := rows.Scan(&row.RecordID, &row.FieldID, &row.ObservedValueJSON, &row.SemanticsState); err != nil {
			return err
		}
		loaded.FieldValues = append(loaded.FieldValues, row)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanRows(database, `SELECT id,kind,from_record_id,to_record_id,from_source_id,to_source_id,resolution_state,`+sourceColumns+` FROM relationships ORDER BY id`, func(rows *sql.Rows) error {
		row := relationshipRow{}
		var from, to sql.NullString
		values := append([]any{&row.Entity.ID, &row.Kind, &from, &to, &row.FromSourceID, &row.ToSourceID, &row.ResolutionState}, sourcePointers(&row.Entity.Source)...)
		if err := rows.Scan(values...); err != nil {
			return err
		}
		row.FromRecordID = nullableString(from)
		row.ToRecordID = nullableString(to)
		loaded.Relationships = append(loaded.Relationships, row)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scanRows(database, `SELECT id,workspace_id,record_id,filename,media_type,expected_size,download_status,local_path,archived_size,checksum,error,`+sourceColumns+` FROM attachments ORDER BY id`, func(rows *sql.Rows) error {
		row := attachmentRow{}
		var filename, mediaType, localPath, checksum, failure sql.NullString
		var expected, archived sql.NullInt64
		values := append([]any{
			&row.Entity.ID, &row.WorkspaceID, &row.RecordID, &filename, &mediaType, &expected,
			&row.DownloadStatus, &localPath, &archived, &checksum, &failure,
		}, sourcePointers(&row.Entity.Source)...)
		if err := rows.Scan(values...); err != nil {
			return err
		}
		row.Filename = nullableString(filename)
		row.MediaType = nullableString(mediaType)
		row.ExpectedSize = nullableInt64(expected)
		row.LocalPath = nullableString(localPath)
		row.ArchivedSize = nullableInt64(archived)
		row.Checksum = nullableString(checksum)
		row.Error = nullableString(failure)
		loaded.Attachments = append(loaded.Attachments, row)
		return nil
	}); err != nil {
		return nil, err
	}
	return loaded, nil
}

const sourceColumns = `source_provider,source_type,source_id,source_raw_path,source_id_composite`

func sourcePointers(source *sourceRef) []any {
	return []any{&source.Provider, &source.Type, &source.ID, &source.RawPath, &source.Composite}
}

func scanRows(database *sql.DB, query string, handle func(*sql.Rows) error) error {
	rows, err := database.Query(query)
	if err != nil {
		return fmt.Errorf("query %s: %w", query, err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := handle(rows); err != nil {
			return fmt.Errorf("read %s: %w", query, err)
		}
	}
	return rows.Err()
}

func tableColumns(database *sql.DB, table string) ([]string, error) {
	var columns []string
	rows, err := database.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("table %q exposes no columns", table)
	}
	return columns, nil
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

// schemaDocument is the archived description of the portable representation.
// It is parsed independently instead of being assumed to match the writer.
type schemaDocument struct {
	SchemaVersion int    `json:"schema_version"`
	Format        string `json:"format"`
	Database      string `json:"database"`
	Tables        []struct {
		Name    string `json:"name"`
		Columns []struct {
			Name string `json:"name"`
		} `json:"columns"`
	} `json:"tables"`
	Semantics []string `json:"semantics"`
}

func parseSchemaDocument(content []byte) (schemaDocument, error) {
	var document schemaDocument
	if err := json.Unmarshal(content, &document); err != nil {
		return schemaDocument{}, err
	}
	if len(document.Tables) == 0 {
		return schemaDocument{}, errors.New("schema.json declares no tables")
	}
	return document, nil
}
