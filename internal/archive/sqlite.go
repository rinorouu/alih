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

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"

	_ "github.com/mattn/go-sqlite3"

	"alih/internal/model"
)

const sqliteDDL = `
PRAGMA page_size = 4096;
PRAGMA auto_vacuum = NONE;
PRAGMA application_id = 1095510088;
PRAGMA user_version = 1;

CREATE TABLE archive_metadata (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE workspaces (
  id TEXT PRIMARY KEY,
  name TEXT,
  source_provider TEXT NOT NULL,
  source_type TEXT NOT NULL,
  source_id TEXT NOT NULL,
  source_raw_path TEXT NOT NULL,
  source_id_composite INTEGER NOT NULL CHECK(source_id_composite IN (0,1)),
  UNIQUE(source_provider, source_type, source_id)
) WITHOUT ROWID;

CREATE TABLE containers (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) DEFERRABLE INITIALLY DEFERRED,
  parent_id TEXT REFERENCES containers(id) DEFERRABLE INITIALLY DEFERRED,
  name TEXT,
  source_provider TEXT NOT NULL,
  source_type TEXT NOT NULL,
  source_id TEXT NOT NULL,
  source_raw_path TEXT NOT NULL,
  source_id_composite INTEGER NOT NULL CHECK(source_id_composite IN (0,1)),
  UNIQUE(source_provider, source_type, source_id)
) WITHOUT ROWID;

CREATE TABLE collections (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) DEFERRABLE INITIALLY DEFERRED,
  container_id TEXT NOT NULL REFERENCES containers(id) DEFERRABLE INITIALLY DEFERRED,
  name TEXT,
  source_provider TEXT NOT NULL,
  source_type TEXT NOT NULL,
  source_id TEXT NOT NULL,
  source_raw_path TEXT NOT NULL,
  source_id_composite INTEGER NOT NULL CHECK(source_id_composite IN (0,1)),
  UNIQUE(source_provider, source_type, source_id)
) WITHOUT ROWID;

CREATE TABLE records (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) DEFERRABLE INITIALLY DEFERRED,
  collection_id TEXT NOT NULL REFERENCES collections(id) DEFERRABLE INITIALLY DEFERRED,
  parent_record_id TEXT REFERENCES records(id) DEFERRABLE INITIALLY DEFERRED,
  title TEXT,
  description TEXT,
  text_content TEXT,
  status TEXT,
  status_type TEXT,
  priority TEXT,
  archived INTEGER CHECK(archived IS NULL OR archived IN (0,1)),
  date_created_source TEXT,
  date_updated_source TEXT,
  date_closed_source TEXT,
  date_done_source TEXT,
  start_date_source TEXT,
  due_date_source TEXT,
  time_estimate_ms INTEGER,
  time_spent_ms INTEGER,
  points REAL,
  source_provider TEXT NOT NULL,
  source_type TEXT NOT NULL,
  source_id TEXT NOT NULL,
  source_raw_path TEXT NOT NULL,
  source_id_composite INTEGER NOT NULL CHECK(source_id_composite IN (0,1)),
  UNIQUE(source_provider, source_type, source_id)
) WITHOUT ROWID;

CREATE TABLE identities (
  id TEXT PRIMARY KEY,
  username TEXT,
  email TEXT,
  source_provider TEXT NOT NULL,
  source_type TEXT NOT NULL,
  source_id TEXT NOT NULL,
  source_raw_path TEXT NOT NULL,
  source_id_composite INTEGER NOT NULL CHECK(source_id_composite IN (0,1)),
  UNIQUE(source_provider, source_type, source_id)
) WITHOUT ROWID;

CREATE TABLE record_identities (
  record_id TEXT NOT NULL REFERENCES records(id) DEFERRABLE INITIALLY DEFERRED,
  identity_id TEXT NOT NULL REFERENCES identities(id) DEFERRABLE INITIALLY DEFERRED,
  role TEXT NOT NULL,
  position INTEGER NOT NULL,
  PRIMARY KEY(record_id, role, position),
  UNIQUE(record_id, identity_id, role)
) WITHOUT ROWID;

CREATE TABLE record_tags (
  record_id TEXT NOT NULL REFERENCES records(id) DEFERRABLE INITIALLY DEFERRED,
  name TEXT NOT NULL,
  position INTEGER NOT NULL,
  PRIMARY KEY(record_id, position)
) WITHOUT ROWID;

CREATE TABLE comments (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) DEFERRABLE INITIALLY DEFERRED,
  record_id TEXT NOT NULL REFERENCES records(id) DEFERRABLE INITIALLY DEFERRED,
  parent_comment_id TEXT REFERENCES comments(id) DEFERRABLE INITIALLY DEFERRED,
  author_identity_id TEXT REFERENCES identities(id) DEFERRABLE INITIALLY DEFERRED,
  body_text TEXT,
  body_json TEXT,
  date_created_source TEXT,
  source_provider TEXT NOT NULL,
  source_type TEXT NOT NULL,
  source_id TEXT NOT NULL,
  source_raw_path TEXT NOT NULL,
  source_id_composite INTEGER NOT NULL CHECK(source_id_composite IN (0,1)),
  UNIQUE(source_provider, source_type, source_id)
) WITHOUT ROWID;

CREATE TABLE field_definitions (
  id TEXT PRIMARY KEY,
  name TEXT,
  field_type TEXT,
  semantics_state TEXT NOT NULL,
  definition_json TEXT NOT NULL,
  source_provider TEXT NOT NULL,
  source_type TEXT NOT NULL,
  source_id TEXT NOT NULL,
  source_raw_path TEXT NOT NULL,
  source_id_composite INTEGER NOT NULL CHECK(source_id_composite IN (0,1)),
  UNIQUE(source_provider, source_type, source_id)
) WITHOUT ROWID;

CREATE TABLE record_field_values (
  record_id TEXT NOT NULL REFERENCES records(id) DEFERRABLE INITIALLY DEFERRED,
  field_id TEXT NOT NULL REFERENCES field_definitions(id) DEFERRABLE INITIALLY DEFERRED,
  observed_value_json TEXT NOT NULL,
  semantics_state TEXT NOT NULL,
  PRIMARY KEY(record_id, field_id)
) WITHOUT ROWID;

CREATE TABLE relationships (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  from_record_id TEXT REFERENCES records(id) DEFERRABLE INITIALLY DEFERRED,
  to_record_id TEXT REFERENCES records(id) DEFERRABLE INITIALLY DEFERRED,
  from_source_id TEXT NOT NULL,
  to_source_id TEXT NOT NULL,
  resolution_state TEXT NOT NULL,
  source_metadata_json TEXT NOT NULL,
  source_provider TEXT NOT NULL,
  source_type TEXT NOT NULL,
  source_id TEXT NOT NULL,
  source_raw_path TEXT NOT NULL,
  source_id_composite INTEGER NOT NULL CHECK(source_id_composite IN (0,1)),
  UNIQUE(source_provider, source_type, source_id)
) WITHOUT ROWID;

CREATE TABLE attachments (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) DEFERRABLE INITIALLY DEFERRED,
  record_id TEXT NOT NULL REFERENCES records(id) DEFERRABLE INITIALLY DEFERRED,
  filename TEXT,
  media_type TEXT,
  expected_size INTEGER,
  source_url TEXT,
  download_status TEXT NOT NULL CHECK(download_status IN ('RETRIEVED','UNRESOLVED')),
  local_path TEXT,
  archived_size INTEGER,
  checksum TEXT,
  error TEXT,
  source_provider TEXT NOT NULL,
  source_type TEXT NOT NULL,
  source_id TEXT NOT NULL,
  source_raw_path TEXT NOT NULL,
  source_id_composite INTEGER NOT NULL CHECK(source_id_composite IN (0,1)),
  UNIQUE(source_provider, source_type, source_id),
  CHECK((download_status = 'RETRIEVED' AND local_path IS NOT NULL AND archived_size IS NOT NULL AND checksum IS NOT NULL AND error IS NULL)
     OR (download_status = 'UNRESOLVED' AND local_path IS NULL AND archived_size IS NULL AND checksum IS NULL AND error IS NOT NULL))
) WITHOUT ROWID;

CREATE INDEX idx_containers_parent ON containers(parent_id);
CREATE INDEX idx_collections_container ON collections(container_id);
CREATE INDEX idx_records_collection ON records(collection_id);
CREATE INDEX idx_records_parent ON records(parent_record_id);
CREATE INDEX idx_comments_record ON comments(record_id);
CREATE INDEX idx_comments_parent ON comments(parent_comment_id);
CREATE INDEX idx_attachments_record ON attachments(record_id);
CREATE INDEX idx_relationships_from ON relationships(from_record_id);
CREATE INDEX idx_relationships_to ON relationships(to_record_id);
`

func writeSQLite(path string, portable model.Archive, metadata map[string]string) error {
	databaseURL := (&url.URL{Scheme: "file", Path: path}).String()
	dsn := databaseURL + "?_foreign_keys=on&_journal_mode=DELETE&_synchronous=FULL&_busy_timeout=5000"
	database, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return err
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	if _, err := database.Exec(sqliteDDL); err != nil {
		return fmt.Errorf("create portable schema: %w", err)
	}
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	metadataKeys := sortedMapKeys(metadata)
	for _, key := range metadataKeys {
		if _, err := tx.Exec(`INSERT INTO archive_metadata(key,value) VALUES(?,?)`, key, metadata[key]); err != nil {
			return fail(err)
		}
	}
	workspaceArgs := []any{portable.Workspace.ID, nullableString(portable.Workspace.Name)}
	workspaceArgs = append(workspaceArgs, sourceArgs(portable.Workspace.Source)...)
	if _, err := tx.Exec(`INSERT INTO workspaces VALUES(?,?,?,?,?,?,?)`, workspaceArgs...); err != nil {
		return fail(fmt.Errorf("insert workspace: %w", err))
	}
	for _, value := range portable.Containers {
		args := []any{value.ID, value.Kind, value.WorkspaceID, nullableString(value.ParentID), nullableString(value.Name)}
		args = append(args, sourceArgs(value.Source)...)
		if _, err := tx.Exec(`INSERT INTO containers VALUES(?,?,?,?,?,?,?,?,?,?)`, args...); err != nil {
			return fail(err)
		}
	}
	for _, value := range portable.Collections {
		args := []any{value.ID, value.WorkspaceID, value.ContainerID, nullableString(value.Name)}
		args = append(args, sourceArgs(value.Source)...)
		if _, err := tx.Exec(`INSERT INTO collections VALUES(?,?,?,?,?,?,?,?,?)`, args...); err != nil {
			return fail(err)
		}
	}
	for _, value := range portable.Records {
		args := []any{
			value.ID, value.Kind, value.WorkspaceID, value.CollectionID, nullableString(value.ParentRecordID),
			nullableString(value.Title), nullableString(value.Description), nullableString(value.TextContent),
			nullableString(value.Status), nullableString(value.StatusType), nullableString(value.Priority), nullableBool(value.Archived),
			nullableString(value.CreatedAtSource), nullableString(value.UpdatedAtSource), nullableString(value.ClosedAtSource),
			nullableString(value.DoneAtSource), nullableString(value.StartAtSource), nullableString(value.DueAtSource),
			nullableInt64(value.TimeEstimateMS), nullableInt64(value.TimeSpentMS), nullableFloat64(value.Points),
		}
		args = append(args, sourceArgs(value.Source)...)
		if _, err := tx.Exec(`INSERT INTO records VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...); err != nil {
			return fail(err)
		}
	}
	for _, value := range portable.Identities {
		args := []any{value.ID, nullableString(value.Username), nullableString(value.Email)}
		args = append(args, sourceArgs(value.Source)...)
		if _, err := tx.Exec(`INSERT INTO identities VALUES(?,?,?,?,?,?,?,?)`, args...); err != nil {
			return fail(err)
		}
	}
	for _, value := range portable.RecordIdentities {
		if _, err := tx.Exec(`INSERT INTO record_identities VALUES(?,?,?,?)`, value.RecordID, value.IdentityID, value.Role, value.Position); err != nil {
			return fail(err)
		}
	}
	for _, value := range portable.RecordTags {
		if _, err := tx.Exec(`INSERT INTO record_tags VALUES(?,?,?)`, value.RecordID, value.Name, value.Position); err != nil {
			return fail(err)
		}
	}
	for _, value := range portable.Comments {
		args := []any{value.ID, value.WorkspaceID, value.RecordID, nullableString(value.ParentCommentID), nullableString(value.AuthorIdentityID), nullableString(value.BodyText), nullableJSON(value.BodyJSON), nullableString(value.CreatedAtSource)}
		args = append(args, sourceArgs(value.Source)...)
		if _, err := tx.Exec(`INSERT INTO comments VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...); err != nil {
			return fail(err)
		}
	}
	for _, value := range portable.FieldDefinitions {
		args := []any{value.ID, nullableString(value.Name), nullableString(value.FieldType), value.SemanticsState, string(value.DefinitionJSON)}
		args = append(args, sourceArgs(value.Source)...)
		if _, err := tx.Exec(`INSERT INTO field_definitions VALUES(?,?,?,?,?,?,?,?,?,?)`, args...); err != nil {
			return fail(err)
		}
	}
	for _, value := range portable.RecordFieldValues {
		if _, err := tx.Exec(`INSERT INTO record_field_values VALUES(?,?,?,?)`, value.RecordID, value.FieldID, string(value.ObservedValueJSON), value.SemanticsState); err != nil {
			return fail(err)
		}
	}
	for _, value := range portable.Relationships {
		args := []any{value.ID, value.Kind, nullableString(value.FromRecordID), nullableString(value.ToRecordID), value.FromSourceID, value.ToSourceID, value.ResolutionState, string(value.SourceMetadataJSON)}
		args = append(args, sourceArgs(value.Source)...)
		if _, err := tx.Exec(`INSERT INTO relationships VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...); err != nil {
			return fail(err)
		}
	}
	for _, value := range portable.Attachments {
		args := []any{
			value.ID, value.WorkspaceID, value.RecordID, nullableString(value.Filename), nullableString(value.MediaType),
			nullableInt64(value.ExpectedSize), nullableString(value.SourceURL), value.DownloadStatus,
			nullableString(value.LocalPath), nullableInt64(value.ArchivedSize), nullableString(value.Checksum), nullableString(value.Error),
		}
		args = append(args, sourceArgs(value.Source)...)
		if _, err := tx.Exec(`INSERT INTO attachments VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...); err != nil {
			return fail(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := checkSQLite(database); err != nil {
		return err
	}
	if _, err := database.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("finalize deterministic SQLite layout: %w", err)
	}
	if err := checkSQLite(database); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("protect portable SQLite file: %w", err)
	}
	return nil
}

func checkSQLite(database *sql.DB) error {
	rows, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("SQLite foreign_key_check reported a violation")
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var result string
	if err := database.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("SQLite integrity_check returned %q", result)
	}
	return nil
}

func sourceArgs(source model.SourceRef) []any {
	composite := 0
	if source.IDComposite {
		composite = 1
	}
	return []any{source.Provider, source.Type, source.ID, source.RawPath, composite}
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func nullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	if *value {
		return 1
	}
	return 0
}
func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
func nullableFloat64(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}
func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func metadataJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func metadataInteger(value int) string { return strconv.Itoa(value) }
