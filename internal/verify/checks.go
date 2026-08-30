package verify

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/model"
	"alih/internal/snapshot"
)

// archivedObject locates one portable entity by its retained source identity.
type archivedObject struct {
	Table string
	ID    string
}

// portableIndex is a by-identifier view over the loaded portable database.
type portableIndex struct {
	bySource      map[string]archivedObject
	workspaces    map[string]struct{}
	containers    map[string]containerRow
	collections   map[string]collectionRow
	records       map[string]recordRow
	identities    map[string]struct{}
	comments      map[string]commentRow
	fields        map[string]fieldRow
	relationships map[string]relationshipRow
	attachments   map[string]attachmentRow
}

func buildIndex(database *portableDatabase) (portableIndex, []string) {
	index := portableIndex{
		bySource: map[string]archivedObject{}, workspaces: map[string]struct{}{},
		containers: map[string]containerRow{}, collections: map[string]collectionRow{},
		records: map[string]recordRow{}, identities: map[string]struct{}{},
		comments: map[string]commentRow{}, fields: map[string]fieldRow{},
		relationships: map[string]relationshipRow{}, attachments: map[string]attachmentRow{},
	}
	var conflicts []string
	add := func(table, sourceType, sourceID, portableID string) {
		key := sourceType + "\x00" + sourceID
		if existing, duplicate := index.bySource[key]; duplicate {
			conflicts = append(conflicts, fmt.Sprintf("source %s %q is archived twice, in %s and %s", sourceType, sourceID, existing.Table, table))
			return
		}
		index.bySource[key] = archivedObject{Table: table, ID: portableID}
	}
	for _, row := range database.Workspaces {
		index.workspaces[row.ID] = struct{}{}
		add("workspaces", row.Source.Type, row.Source.ID, row.ID)
	}
	for _, row := range database.Containers {
		index.containers[row.Entity.ID] = row
		add("containers", row.Entity.Source.Type, row.Entity.Source.ID, row.Entity.ID)
	}
	for _, row := range database.Collections {
		index.collections[row.Entity.ID] = row
		add("collections", row.Entity.Source.Type, row.Entity.Source.ID, row.Entity.ID)
	}
	for _, row := range database.Records {
		index.records[row.Entity.ID] = row
		// The M3 source index distinguishes nested records by kind while the
		// portable row retains the provider's own object type.
		add("records", row.Kind, row.Entity.Source.ID, row.Entity.ID)
	}
	for _, row := range database.Identities {
		index.identities[row.ID] = struct{}{}
	}
	for _, row := range database.Comments {
		index.comments[row.Entity.ID] = row
		add("comments", row.Entity.Source.Type, row.Entity.Source.ID, row.Entity.ID)
	}
	for _, row := range database.Fields {
		index.fields[row.Entity.ID] = row
		add("field_definitions", row.Entity.Source.Type, row.Entity.Source.ID, row.Entity.ID)
	}
	for _, row := range database.Relationships {
		index.relationships[row.Entity.ID] = row
		add("relationships", row.Entity.Source.Type, row.Entity.Source.ID, row.Entity.ID)
	}
	for _, row := range database.Attachments {
		index.attachments[row.Entity.ID] = row
		add("attachments", row.Entity.Source.Type, row.Entity.Source.ID, row.Entity.ID)
	}
	return index, conflicts
}

// checkSQLite runs SQLite's own integrity checks and then loads the portable
// content. The database is opened strictly read-only and immutable so that
// verification cannot alter the evidence it is verifying.
func (v *verification) checkSQLite() (*portableDatabase, bool) {
	path := filepath.Join(v.root, "alih.db")
	database, err := openReadOnly(path)
	if err != nil {
		v.fail("sqlite_integrity", "alih.db could not be opened as a SQLite database", []string{err.Error()})
		return nil, false
	}
	problems, err := integrityCheck(database)
	if err == nil {
		v.foreignKeys, err = foreignKeyCheck(database)
	}
	_ = database.Close()
	if err != nil {
		v.fail("sqlite_integrity", "alih.db integrity could not be established", []string{err.Error()})
		return nil, false
	}
	if len(problems) > 0 {
		v.fail("sqlite_integrity", "alih.db failed its own SQLite integrity check", problems)
		return nil, false
	}
	loaded, err := loadPortableDatabase(path)
	if err != nil {
		v.fail("sqlite_integrity", "the portable content of alih.db could not be read", []string{err.Error()})
		return nil, false
	}
	v.pass("sqlite_integrity", "alih.db passes PRAGMA integrity_check and its portable tables are readable")
	return loaded, true
}

// checkRawEvidence revalidates the immutable M3 evidence copied into the
// archive: its run record, its per-response checksums and its logical
// inventory digest. This makes the manifest's expected counts provable from
// archived evidence rather than merely asserted by the manifest.
func (v *verification) checkRawEvidence(manifest archive.Manifest, manifestOK bool) (snapshot.Evidence, bool) {
	evidence, err := snapshot.LoadComplete(filepath.Join(v.root, "raw"))
	if err != nil {
		v.fail("raw_evidence_integrity", "the archived raw M3 evidence is missing, incomplete or does not match its own checksums", []string{err.Error()})
		return snapshot.Evidence{}, false
	}
	var findings []string
	if manifestOK {
		if evidence.Connector != manifest.Connector {
			findings = append(findings, fmt.Sprintf("raw evidence connector %q does not match manifest connector %q", evidence.Connector, manifest.Connector))
		}
		if evidence.Workspace.ID != manifest.Source.ID {
			findings = append(findings, fmt.Sprintf("raw evidence workspace %q does not match manifest workspace %q", evidence.Workspace.ID, manifest.Source.ID))
		}
		if evidence.LogicalDigest != manifest.InputSnapshot.LogicalDigest {
			findings = append(findings, fmt.Sprintf("recomputed raw logical inventory digest %s does not match the manifest digest %s", evidence.LogicalDigest, manifest.InputSnapshot.LogicalDigest))
		}
		findings = append(findings, compareCapabilities(evidence.Capabilities, manifest.Capabilities)...)
	}
	ok := v.decide("raw_evidence_integrity",
		fmt.Sprintf("archived raw M3 evidence revalidates: %d raw responses match their recorded checksums and the logical inventory digest is reproducible", len(evidence.Responses)),
		"the archived raw M3 evidence does not agree with the manifest",
		findings)
	return evidence, ok
}

func compareCapabilities(expected, actual []connector.Capability) []string {
	format := func(capability connector.Capability) string {
		return fmt.Sprintf("%s=%s", capability.Name, capability.State)
	}
	expectedSet := map[string]struct{}{}
	for _, capability := range expected {
		expectedSet[format(capability)] = struct{}{}
	}
	actualSet := map[string]struct{}{}
	for _, capability := range actual {
		actualSet[format(capability)] = struct{}{}
	}
	var findings []string
	for value := range expectedSet {
		if _, present := actualSet[value]; !present {
			findings = append(findings, fmt.Sprintf("source capability %s recorded in the raw evidence is absent from the manifest", value))
		}
	}
	for value := range actualSet {
		if _, present := expectedSet[value]; !present {
			findings = append(findings, fmt.Sprintf("manifest declares capability %s that the raw evidence does not record", value))
		}
	}
	return findings
}

// checkSchemaConsistency proves that the archived schema description actually
// describes the archived database.
func (v *verification) checkSchemaConsistency(database *portableDatabase) {
	content, err := readArchiveFile(v.root, "schema.json")
	if err != nil {
		v.fail("schema_consistency", "schema.json could not be read", []string{err.Error()})
		return
	}
	document, err := parseSchemaDocument(content)
	if err != nil {
		v.fail("schema_consistency", "schema.json is not a usable portable schema description", []string{err.Error()})
		return
	}
	var findings []string
	if document.SchemaVersion != 1 {
		findings = append(findings, fmt.Sprintf("schema.json declares unsupported schema_version %d", document.SchemaVersion))
	}
	if document.Database != "alih.db" {
		findings = append(findings, fmt.Sprintf("schema.json describes database %q rather than alih.db", document.Database))
	}
	declared := map[string]struct{}{}
	for _, table := range document.Tables {
		declared[table.Name] = struct{}{}
		actual, present := database.Columns[table.Name]
		if !present {
			findings = append(findings, fmt.Sprintf("schema.json declares table %q which is absent from alih.db", table.Name))
			continue
		}
		expected := make([]string, 0, len(table.Columns))
		for _, column := range table.Columns {
			expected = append(expected, column.Name)
		}
		if strings.Join(expected, ",") != strings.Join(actual, ",") {
			findings = append(findings, fmt.Sprintf("table %q columns are [%s] but schema.json declares [%s]", table.Name, strings.Join(actual, ","), strings.Join(expected, ",")))
		}
	}
	for _, table := range database.Tables {
		if _, present := declared[table]; !present {
			findings = append(findings, fmt.Sprintf("alih.db contains table %q that schema.json does not describe", table))
		}
	}
	v.decide("schema_consistency",
		fmt.Sprintf("schema.json describes all %d portable tables with exactly the columns present in alih.db", len(document.Tables)),
		"schema.json does not describe the archived database",
		findings)
}

// checkArchiveMetadata reconciles the values the database states about itself
// with the manifest that claims to describe it.
func (v *verification) checkArchiveMetadata(database *portableDatabase, manifest archive.Manifest) {
	var findings []string
	expect := func(key, want string) {
		got, present := database.Metadata[key]
		if !present {
			findings = append(findings, fmt.Sprintf("alih.db archive_metadata is missing %q", key))
			return
		}
		if got != want {
			findings = append(findings, fmt.Sprintf("alih.db archive_metadata %q is %q but the manifest implies %q", key, got, want))
		}
	}
	expect("connector", manifest.Connector)
	expect("source_workspace_id", manifest.Source.ID)
	expect("source_snapshot_logical_digest", manifest.InputSnapshot.LogicalDigest)
	expect("archive_status", manifest.Status)
	expect("alih_version", manifest.AlihVersion)
	expect("archive_schema_version", strconv.Itoa(manifest.SchemaVersion))
	expect("source_snapshot_atomic", strconv.FormatBool(manifest.InputSnapshot.Atomic))
	if status := database.Metadata["verification_status"]; status == "" {
		findings = append(findings, "alih.db archive_metadata is missing \"verification_status\"")
	}
	if len(database.Workspaces) != 1 {
		findings = append(findings, fmt.Sprintf("alih.db contains %d workspace rows; a portable archive describes exactly one", len(database.Workspaces)))
	} else if database.Workspaces[0].Source.ID != manifest.Source.ID {
		findings = append(findings, fmt.Sprintf("archived workspace source id %q does not match the manifest workspace %q", database.Workspaces[0].Source.ID, manifest.Source.ID))
	}
	v.decide("archive_metadata_consistency",
		"alih.db archive_metadata agrees with manifest.json about connector, workspace, snapshot digest, version and status",
		"alih.db and manifest.json disagree about what this archive is",
		findings)
}

// checkPortableIdentifiers proves that every portable identifier is still the
// deterministic function of the retained source identity it claims to be.
func (v *verification) checkPortableIdentifiers(database *portableDatabase) {
	var findings []string
	checked := 0
	verifyEntity := func(table, namespace string, entity entityRow) {
		checked++
		if entity.Source.Provider == "" || entity.Source.Type == "" || entity.Source.ID == "" {
			findings = append(findings, fmt.Sprintf("%s row %q does not retain a complete source identity", table, entity.ID))
			return
		}
		expected := model.PortableID(entity.Source.Provider, namespace, entity.Source.ID)
		if entity.ID != expected {
			findings = append(findings, fmt.Sprintf("%s row %q is not the deterministic portable identifier of source %s %q", table, entity.ID, entity.Source.Type, entity.Source.ID))
		}
	}
	for _, row := range database.Workspaces {
		verifyEntity("workspaces", row.Source.Type, row)
	}
	for _, row := range database.Containers {
		verifyEntity("containers", row.Entity.Source.Type, row.Entity)
		if row.Kind != row.Entity.Source.Type {
			findings = append(findings, fmt.Sprintf("container %q is classified %q but retains source type %q", row.Entity.ID, row.Kind, row.Entity.Source.Type))
		}
	}
	for _, row := range database.Collections {
		verifyEntity("collections", row.Entity.Source.Type, row.Entity)
	}
	for _, row := range database.Records {
		verifyEntity("records", row.Entity.Source.Type, row.Entity)
	}
	for _, row := range database.Identities {
		// Identities are the one portable entity whose namespace is fixed by
		// the portable model rather than by the provider's own object type.
		verifyEntity("identities", "identity", row)
	}
	for _, row := range database.Comments {
		verifyEntity("comments", row.Entity.Source.Type, row.Entity)
	}
	for _, row := range database.Fields {
		verifyEntity("field_definitions", row.Entity.Source.Type, row.Entity)
	}
	for _, row := range database.Relationships {
		verifyEntity("relationships", row.Entity.Source.Type, row.Entity)
	}
	for _, row := range database.Attachments {
		verifyEntity("attachments", row.Entity.Source.Type, row.Entity)
	}
	v.decide("portable_identifier_derivation",
		fmt.Sprintf("all %d portable identifiers are reproducible from the original source identifiers retained in the archive", checked),
		"at least one portable identifier is not derivable from the source identity stored beside it",
		findings)
}

// checkReferentialIntegrity resolves every portable reference explicitly
// instead of trusting the declared SQLite foreign keys, so a tampered schema
// cannot hide a broken reference.
func (v *verification) checkReferentialIntegrity(database *portableDatabase) {
	index, findings := buildIndex(database)
	findings = append(findings, v.foreignKeys...)
	workspaceID := ""
	if len(database.Workspaces) == 1 {
		workspaceID = database.Workspaces[0].ID
	}
	reference := func(description, value string, exists bool) {
		if !exists {
			findings = append(findings, fmt.Sprintf("%s references %q which is not archived", description, value))
		}
	}
	owner := func(description, value string) {
		if workspaceID != "" && value != workspaceID {
			findings = append(findings, fmt.Sprintf("%s is owned by workspace %q which is not the archived workspace", description, value))
		}
	}
	for _, row := range database.Containers {
		owner("container "+row.Entity.ID, row.WorkspaceID)
		if row.ParentID != nil {
			_, exists := index.containers[*row.ParentID]
			reference("container "+row.Entity.ID, *row.ParentID, exists)
		}
	}
	for _, row := range database.Collections {
		owner("collection "+row.Entity.ID, row.WorkspaceID)
		_, exists := index.containers[row.ContainerID]
		reference("collection "+row.Entity.ID, row.ContainerID, exists)
	}
	for _, row := range database.Records {
		owner("record "+row.Entity.ID, row.WorkspaceID)
		_, exists := index.collections[row.CollectionID]
		reference("record "+row.Entity.ID, row.CollectionID, exists)
		if row.ParentRecordID != nil {
			_, exists := index.records[*row.ParentRecordID]
			reference("record "+row.Entity.ID, *row.ParentRecordID, exists)
		}
	}
	for _, row := range database.RecordIdentities {
		_, recordExists := index.records[row.RecordID]
		reference("record identity role "+row.Role, row.RecordID, recordExists)
		_, identityExists := index.identities[row.IdentityID]
		reference("record identity role "+row.Role, row.IdentityID, identityExists)
	}
	for _, row := range database.RecordTags {
		_, exists := index.records[row.RecordID]
		reference("record tag "+row.Name, row.RecordID, exists)
	}
	for _, row := range database.Comments {
		owner("comment "+row.Entity.ID, row.WorkspaceID)
		_, exists := index.records[row.RecordID]
		reference("comment "+row.Entity.ID, row.RecordID, exists)
		if row.ParentCommentID != nil {
			_, exists := index.comments[*row.ParentCommentID]
			reference("comment "+row.Entity.ID, *row.ParentCommentID, exists)
		}
		if row.AuthorIdentityID != nil {
			_, exists := index.identities[*row.AuthorIdentityID]
			reference("comment "+row.Entity.ID, *row.AuthorIdentityID, exists)
		}
	}
	for _, row := range database.FieldValues {
		_, recordExists := index.records[row.RecordID]
		reference("record field value", row.RecordID, recordExists)
		_, fieldExists := index.fields[row.FieldID]
		reference("record field value", row.FieldID, fieldExists)
	}
	for _, row := range database.Relationships {
		if row.FromRecordID != nil {
			_, exists := index.records[*row.FromRecordID]
			reference("relationship "+row.Entity.ID, *row.FromRecordID, exists)
		}
		if row.ToRecordID != nil {
			_, exists := index.records[*row.ToRecordID]
			reference("relationship "+row.Entity.ID, *row.ToRecordID, exists)
		}
	}
	for _, row := range database.Attachments {
		owner("attachment "+row.Entity.ID, row.WorkspaceID)
		_, exists := index.records[row.RecordID]
		reference("attachment "+row.Entity.ID, row.RecordID, exists)
	}
	findings = append(findings, containerCycles(database)...)
	findings = append(findings, recordCycles(database)...)
	findings = append(findings, commentCycles(database)...)
	findings = append(findings, v.relationshipEvidence(database, index)...)

	v.decide("referential_integrity",
		"PRAGMA foreign_key_check is clean and every archived portable reference resolves to an archived entity without a parent cycle",
		"at least one archived portable reference is broken",
		findings)
}

// relationshipEvidence proves that each relationship's declared resolution
// state is exactly what the archived record set supports.
func (v *verification) relationshipEvidence(database *portableDatabase, index portableIndex) []string {
	var findings []string
	recordSource := map[string]string{}
	for _, row := range database.Records {
		recordSource[row.Entity.Source.ID] = row.Entity.ID
	}
	for _, row := range database.Relationships {
		if row.FromSourceID == "" || row.ToSourceID == "" {
			findings = append(findings, fmt.Sprintf("relationship %q does not retain both original endpoint identifiers", row.Entity.ID))
			continue
		}
		resolved := true
		endpoints := []struct {
			label    string
			sourceID string
			portable *string
		}{
			{"from", row.FromSourceID, row.FromRecordID},
			{"to", row.ToSourceID, row.ToRecordID},
		}
		for _, endpoint := range endpoints {
			archivedID, archived := recordSource[endpoint.sourceID]
			switch {
			case archived && endpoint.portable == nil:
				findings = append(findings, fmt.Sprintf("relationship %q leaves its %s endpoint unresolved although source record %q is archived", row.Entity.ID, endpoint.label, endpoint.sourceID))
			case archived && *endpoint.portable != archivedID:
				findings = append(findings, fmt.Sprintf("relationship %q resolves its %s endpoint to %q instead of the archived record for source %q", row.Entity.ID, endpoint.label, *endpoint.portable, endpoint.sourceID))
			case !archived && endpoint.portable != nil:
				findings = append(findings, fmt.Sprintf("relationship %q resolves its %s endpoint although source record %q is not archived", row.Entity.ID, endpoint.label, endpoint.sourceID))
			case !archived:
				resolved = false
			}
		}
		if resolved && row.ResolutionState != "RESOLVED" {
			findings = append(findings, fmt.Sprintf("relationship %q is marked %s although both endpoints are archived", row.Entity.ID, row.ResolutionState))
		}
		if !resolved && row.ResolutionState == "RESOLVED" {
			findings = append(findings, fmt.Sprintf("relationship %q claims RESOLVED although an endpoint is outside the archived record set", row.Entity.ID))
		}
	}
	return findings
}

func containerCycles(database *portableDatabase) []string {
	parents := map[string]*string{}
	for _, row := range database.Containers {
		parents[row.Entity.ID] = row.ParentID
	}
	return detectCycles("container", parents)
}

func recordCycles(database *portableDatabase) []string {
	parents := map[string]*string{}
	for _, row := range database.Records {
		parents[row.Entity.ID] = row.ParentRecordID
	}
	return detectCycles("record", parents)
}

func commentCycles(database *portableDatabase) []string {
	parents := map[string]*string{}
	for _, row := range database.Comments {
		parents[row.Entity.ID] = row.ParentCommentID
	}
	return detectCycles("comment", parents)
}

func detectCycles(kind string, parents map[string]*string) []string {
	var findings []string
	reported := map[string]struct{}{}
	for start := range parents {
		seen := map[string]struct{}{start: {}}
		current := start
		for {
			parent, known := parents[current]
			if !known || parent == nil {
				break
			}
			if _, repeat := seen[*parent]; repeat {
				if _, already := reported[start]; !already {
					reported[start] = struct{}{}
					findings = append(findings, fmt.Sprintf("%s %q is part of a parent cycle", kind, start))
				}
				break
			}
			seen[*parent] = struct{}{}
			current = *parent
		}
	}
	return findings
}
