package verify

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/snapshot"
)

// checkSourceObjectReconciliation compares the source object index recorded by
// the M3 extraction with what the portable database actually archived. This is
// the primary no-silent-loss check: every expected source object must be
// present exactly once, and the archive must not contain entities the source
// traversal never observed.
func (v *verification) checkSourceObjectReconciliation(database *portableDatabase, evidence snapshot.Evidence) {
	index, findings := buildIndex(database)
	expected := make(map[string]struct{}, len(evidence.SourceObjects))
	for _, object := range evidence.SourceObjects {
		key := object.Type + "\x00" + object.ID
		expected[key] = struct{}{}
		if _, archived := index.bySource[key]; !archived {
			findings = append(findings, fmt.Sprintf("expected source %s %q is missing from the portable archive", object.Type, object.ID))
		}
	}
	for key, object := range index.bySource {
		if _, wanted := expected[key]; !wanted {
			parts := strings.SplitN(key, "\x00", 2)
			findings = append(findings, fmt.Sprintf("archived %s contains source %s %q that the M3 source index does not record", object.Table, parts[0], parts[1]))
		}
	}
	v.decide("source_object_reconciliation",
		fmt.Sprintf("all %d source objects recorded by the M3 extraction are archived exactly once and the archive contains nothing else", len(expected)),
		"the portable archive does not contain exactly the source objects the extraction recorded",
		findings)
}

// checkParentLinkage rebuilds the hierarchy from the archived M3 source index
// and proves the portable parent pointers still describe it. A silently
// re-parented record is detected here even when every reference resolves.
func (v *verification) checkParentLinkage(database *portableDatabase, evidence snapshot.Evidence) {
	index, _ := buildIndex(database)
	objects := make(map[string]connector.SourceObject, len(evidence.SourceObjects))
	for _, object := range evidence.SourceObjects {
		objects[object.Type+"\x00"+object.ID] = object
	}
	workspaceID := ""
	if len(database.Workspaces) == 1 {
		workspaceID = database.Workspaces[0].ID
	}
	var findings []string
	equals := func(actual *string, want string) bool { return actual != nil && *actual == want }

	// ancestor walks up the source index until it reaches an object archived
	// into one of the accepted tables.
	ancestor := func(object connector.SourceObject, tables ...string) (archivedObject, bool) {
		accepted := map[string]struct{}{}
		for _, table := range tables {
			accepted[table] = struct{}{}
		}
		seen := map[string]struct{}{object.Type + "\x00" + object.ID: {}}
		current := object
		for current.ParentType != "" {
			key := current.ParentType + "\x00" + current.ParentID
			if _, repeat := seen[key]; repeat {
				return archivedObject{}, false
			}
			seen[key] = struct{}{}
			parent, known := index.bySource[key]
			if !known {
				return archivedObject{}, false
			}
			if _, wanted := accepted[parent.Table]; wanted {
				return parent, true
			}
			current, known = objects[key]
			if !known {
				return archivedObject{}, false
			}
		}
		return archivedObject{}, false
	}

	for _, object := range evidence.SourceObjects {
		child, archived := index.bySource[object.Type+"\x00"+object.ID]
		if !archived {
			continue // already reported by source object reconciliation
		}
		var parent archivedObject
		hasParent := false
		if object.ParentType != "" {
			parent, hasParent = index.bySource[object.ParentType+"\x00"+object.ParentID]
			if !hasParent {
				findings = append(findings, fmt.Sprintf("source %s %q declares parent %s %q which is not archived", object.Type, object.ID, object.ParentType, object.ParentID))
				continue
			}
		}
		switch child.Table {
		case "workspaces":
			if hasParent {
				findings = append(findings, fmt.Sprintf("archived workspace %q unexpectedly declares a parent", object.ID))
			}
		case "containers":
			row := index.containers[child.ID]
			switch {
			case !hasParent:
				if row.ParentID != nil {
					findings = append(findings, fmt.Sprintf("container for source %s %q has a parent the source index does not record", object.Type, object.ID))
				}
			case parent.Table == "workspaces":
				if row.ParentID != nil {
					findings = append(findings, fmt.Sprintf("top-level container for source %s %q must not have a container parent", object.Type, object.ID))
				}
				if row.WorkspaceID != parent.ID {
					findings = append(findings, fmt.Sprintf("container for source %s %q is not owned by its source workspace", object.Type, object.ID))
				}
			case parent.Table == "containers":
				if !equals(row.ParentID, parent.ID) {
					findings = append(findings, fmt.Sprintf("container for source %s %q no longer points at source %s %q", object.Type, object.ID, object.ParentType, object.ParentID))
				}
			default:
				findings = append(findings, fmt.Sprintf("container for source %s %q has unsupported parent table %s", object.Type, object.ID, parent.Table))
			}
		case "collections":
			row := index.collections[child.ID]
			if !hasParent || parent.Table != "containers" {
				findings = append(findings, fmt.Sprintf("collection for source %s %q is not contained by an archived container", object.Type, object.ID))
			} else if row.ContainerID != parent.ID {
				findings = append(findings, fmt.Sprintf("collection for source %s %q no longer points at source %s %q", object.Type, object.ID, object.ParentType, object.ParentID))
			}
			if workspaceID != "" && row.WorkspaceID != workspaceID {
				findings = append(findings, fmt.Sprintf("collection for source %s %q is not owned by the archived workspace", object.Type, object.ID))
			}
		case "records":
			row := index.records[child.ID]
			switch {
			case !hasParent:
				findings = append(findings, fmt.Sprintf("record for source %s %q has no parent in the M3 source index", object.Type, object.ID))
			case parent.Table == "collections":
				if row.ParentRecordID != nil {
					findings = append(findings, fmt.Sprintf("record for source %s %q is nested although the source index places it directly in a collection", object.Type, object.ID))
				}
				if row.CollectionID != parent.ID {
					findings = append(findings, fmt.Sprintf("record for source %s %q no longer belongs to source %s %q", object.Type, object.ID, object.ParentType, object.ParentID))
				}
			case parent.Table == "records":
				if !equals(row.ParentRecordID, parent.ID) {
					findings = append(findings, fmt.Sprintf("nested record for source %s %q no longer points at parent source %s %q", object.Type, object.ID, object.ParentType, object.ParentID))
				}
				collection, found := ancestor(object, "collections")
				if !found {
					findings = append(findings, fmt.Sprintf("nested record for source %s %q cannot be traced to an archived collection", object.Type, object.ID))
				} else if row.CollectionID != collection.ID {
					findings = append(findings, fmt.Sprintf("nested record for source %s %q is filed under a collection its source ancestry does not support", object.Type, object.ID))
				}
			default:
				findings = append(findings, fmt.Sprintf("record for source %s %q has unsupported parent table %s", object.Type, object.ID, parent.Table))
			}
		case "comments":
			row := index.comments[child.ID]
			switch {
			case !hasParent:
				findings = append(findings, fmt.Sprintf("comment for source %s %q has no parent in the M3 source index", object.Type, object.ID))
			case parent.Table == "records":
				if row.ParentCommentID != nil {
					findings = append(findings, fmt.Sprintf("comment for source %s %q is threaded although the source index attaches it directly to a record", object.Type, object.ID))
				}
				if row.RecordID != parent.ID {
					findings = append(findings, fmt.Sprintf("comment for source %s %q no longer belongs to source %s %q", object.Type, object.ID, object.ParentType, object.ParentID))
				}
			case parent.Table == "comments":
				if !equals(row.ParentCommentID, parent.ID) {
					findings = append(findings, fmt.Sprintf("threaded comment for source %s %q no longer points at parent source %s %q", object.Type, object.ID, object.ParentType, object.ParentID))
				}
				record, found := ancestor(object, "records")
				if !found {
					findings = append(findings, fmt.Sprintf("threaded comment for source %s %q cannot be traced to an archived record", object.Type, object.ID))
				} else if row.RecordID != record.ID {
					findings = append(findings, fmt.Sprintf("threaded comment for source %s %q is filed under a record its source ancestry does not support", object.Type, object.ID))
				}
			default:
				findings = append(findings, fmt.Sprintf("comment for source %s %q has unsupported parent table %s", object.Type, object.ID, parent.Table))
			}
		case "attachments":
			row := index.attachments[child.ID]
			if !hasParent || parent.Table != "records" {
				findings = append(findings, fmt.Sprintf("attachment for source %s %q is not attached to an archived record", object.Type, object.ID))
			} else if row.RecordID != parent.ID {
				findings = append(findings, fmt.Sprintf("attachment for source %s %q no longer belongs to source %s %q", object.Type, object.ID, object.ParentType, object.ParentID))
			}
		}
	}
	v.decide("hierarchy_reconstruction",
		"the archived hierarchy and ownership pointers reproduce the parent relationships recorded by the M3 source index",
		"the archived hierarchy does not reproduce the parent relationships recorded by the M3 source index",
		findings)
}

// checkRawPaths proves that the raw evidence pointer retained on every
// portable entity still resolves inside the archive.
func (v *verification) checkRawPaths(database *portableDatabase, files map[string]archiveFile) {
	referenced := map[string]struct{}{}
	collect := func(source sourceRef) { referenced[source.RawPath] = struct{}{} }
	for _, row := range database.Workspaces {
		collect(row.Source)
	}
	for _, row := range database.Containers {
		collect(row.Entity.Source)
	}
	for _, row := range database.Collections {
		collect(row.Entity.Source)
	}
	for _, row := range database.Records {
		collect(row.Entity.Source)
	}
	for _, row := range database.Identities {
		collect(row.Source)
	}
	for _, row := range database.Comments {
		collect(row.Entity.Source)
	}
	for _, row := range database.Fields {
		collect(row.Entity.Source)
	}
	for _, row := range database.Relationships {
		collect(row.Entity.Source)
	}
	for _, row := range database.Attachments {
		collect(row.Entity.Source)
	}
	var findings []string
	for path := range referenced {
		if path == "" {
			findings = append(findings, "a portable entity retains no raw evidence path")
			continue
		}
		if !strings.HasPrefix(path, "raw/") || strings.Contains(path, "..") {
			findings = append(findings, fmt.Sprintf("raw evidence path %q does not point inside the archived raw evidence", path))
			continue
		}
		if _, present := files[path]; !present {
			findings = append(findings, fmt.Sprintf("raw evidence path %q is missing from the archive", path))
		}
	}
	v.decide("raw_evidence_references",
		fmt.Sprintf("all %d distinct raw evidence paths retained by portable entities exist inside the archive", len(referenced)),
		"at least one portable entity points at raw evidence that is not in the archive",
		findings)
}

// checkCounts reconciles the counts expected by the archived M3 inventory, the
// counts claimed by the manifest and the rows actually present in alih.db.
func (v *verification) checkCounts(database *portableDatabase, manifest archive.Manifest, evidence snapshot.Evidence) {
	containerKind := func(kind string) int {
		count := 0
		for _, row := range database.Containers {
			if row.Kind == kind {
				count++
			}
		}
		return count
	}
	recordKind := func(kind string) int {
		count := 0
		for _, row := range database.Records {
			if row.Kind == kind {
				count++
			}
		}
		return count
	}
	retrieved := 0
	for _, row := range database.Attachments {
		if row.DownloadStatus == "RETRIEVED" {
			retrieved++
		}
	}
	entities := []struct {
		name     string
		expected int
		archived int
		total    int
	}{
		{"spaces", evidence.Inventory.Spaces, containerKind("space"), containerKind("space")},
		{"folders", evidence.Inventory.Folders, containerKind("folder"), containerKind("folder")},
		{"lists", evidence.Inventory.Lists, len(database.Collections), len(database.Collections)},
		{"tasks", evidence.Inventory.Tasks, recordKind("task"), recordKind("task")},
		{"subtasks", evidence.Inventory.Subtasks, recordKind("subtask"), recordKind("subtask")},
		{"comments", evidence.Inventory.Comments, len(database.Comments), len(database.Comments)},
		{"attachments", evidence.Inventory.Attachments, retrieved, len(database.Attachments)},
		{"custom_fields", evidence.Inventory.CustomFields, len(database.Fields), len(database.Fields)},
		{"relationships", evidence.Inventory.Relationships, len(database.Relationships), len(database.Relationships)},
	}
	var findings []string
	for _, entity := range entities {
		status := CheckPass
		if entity.expected != entity.total {
			status = CheckFail
			findings = append(findings, fmt.Sprintf("%s: the M3 extraction recorded %d but the archive holds %d", entity.name, entity.expected, entity.total))
		} else if entity.archived != entity.total {
			// Represented but not fully retrieved; attachment_integrity
			// reports the unresolved supported entities themselves.
			status = CheckIncomplete
		}
		claimed, present := manifest.Inventory[entity.name]
		if !present {
			status = CheckFail
			findings = append(findings, fmt.Sprintf("%s: the manifest states no inventory for this entity", entity.name))
		} else {
			if claimed.Expected != entity.expected {
				status = CheckFail
				findings = append(findings, fmt.Sprintf("%s: the manifest expects %d but the archived M3 inventory records %d", entity.name, claimed.Expected, entity.expected))
			}
			if claimed.Archived != entity.archived {
				status = CheckFail
				findings = append(findings, fmt.Sprintf("%s: the manifest claims %d archived but the archive holds %d", entity.name, claimed.Archived, entity.archived))
			}
			if claimed.Unresolved != entity.total-entity.archived {
				status = CheckFail
				findings = append(findings, fmt.Sprintf("%s: the manifest claims %d unresolved but the archive shows %d", entity.name, claimed.Unresolved, entity.total-entity.archived))
			}
		}
		v.counts = append(v.counts, Reconciliation{
			Entity: entity.name, Expected: entity.expected, Archived: entity.archived,
			Unresolved: entity.total - entity.archived, Status: status,
		})
	}
	observed := map[string]int{
		"workspaces":            len(database.Workspaces),
		"identities":            len(database.Identities),
		"record_identity_roles": len(database.RecordIdentities),
		"record_tags":           len(database.RecordTags),
		"record_field_values":   len(database.FieldValues),
	}
	names := make([]string, 0, len(observed))
	for name := range observed {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		claimed, present := manifest.Observed[name]
		if !present {
			findings = append(findings, fmt.Sprintf("%s: the manifest records no observed entity count", name))
			continue
		}
		if claimed != observed[name] {
			findings = append(findings, fmt.Sprintf("%s: the manifest claims %d but the archive holds %d", name, claimed, observed[name]))
		}
	}
	v.decide("count_reconciliation",
		"expected, manifest and archived counts agree for every supported entity",
		"expected, manifest and archived counts do not agree",
		findings)
}

// checkAttachments proves that every retrieved binary still exists with the
// recorded size and checksum, that no unrecorded binary was added, and that
// unresolved supported attachments keep the archive out of a verified state.
func (v *verification) checkAttachments(database *portableDatabase, manifest archive.Manifest, files map[string]archiveFile) {
	recorded := make(map[string]archive.FileRecord, len(manifest.Files))
	for _, file := range manifest.Files {
		recorded[file.Path] = file
	}
	manifestAttachments := make(map[string]archive.AttachmentRecord, len(manifest.Attachments))
	for _, attachment := range manifest.Attachments {
		manifestAttachments[attachment.ID] = attachment
	}

	var findings []string
	unresolved := make([]string, 0)
	claimed := map[string]struct{}{}
	retrieved := 0
	for _, row := range database.Attachments {
		record, present := manifestAttachments[row.Entity.ID]
		if !present {
			findings = append(findings, fmt.Sprintf("attachment source %q is archived in alih.db but absent from the manifest", row.Entity.Source.ID))
		} else if difference := compareAttachmentRecord(row, record); difference != "" {
			findings = append(findings, difference)
		}
		delete(manifestAttachments, row.Entity.ID)

		switch row.DownloadStatus {
		case "UNRESOLVED":
			if row.LocalPath != nil || row.ArchivedSize != nil || row.Checksum != nil {
				findings = append(findings, fmt.Sprintf("unresolved attachment source %q still claims retrieved binary evidence", row.Entity.Source.ID))
			}
			reason := "no reason recorded"
			if row.Error != nil {
				reason = *row.Error
			}
			unresolved = append(unresolved, fmt.Sprintf("attachment source %q was expected but not archived: %s", row.Entity.Source.ID, reason))
		case "RETRIEVED":
			retrieved++
			if row.LocalPath == nil || row.ArchivedSize == nil || row.Checksum == nil {
				findings = append(findings, fmt.Sprintf("attachment source %q claims RETRIEVED without a local path, size and checksum", row.Entity.Source.ID))
				continue
			}
			path := *row.LocalPath
			if !strings.HasPrefix(path, "attachments/") || strings.Contains(path, "..") {
				findings = append(findings, fmt.Sprintf("attachment source %q points outside the archived attachments directory: %q", row.Entity.Source.ID, path))
				continue
			}
			claimed[path] = struct{}{}
			if _, present := files[path]; !present {
				findings = append(findings, fmt.Sprintf("attachment binary %s recorded for source %q is missing from the archive", path, row.Entity.Source.ID))
				continue
			}
			size, checksum, err := fileDigest(filepath.Join(v.root, filepath.FromSlash(path)))
			if err != nil {
				findings = append(findings, fmt.Sprintf("attachment binary %s could not be read: %v", path, err))
				continue
			}
			if size != *row.ArchivedSize {
				findings = append(findings, fmt.Sprintf("attachment binary %s is %d bytes but the archive records %d", path, size, *row.ArchivedSize))
			}
			if checksum != *row.Checksum {
				findings = append(findings, fmt.Sprintf("attachment binary %s checksum %s does not match the recorded checksum %s", path, checksum, *row.Checksum))
			}
			if row.ExpectedSize != nil && *row.ExpectedSize != *row.ArchivedSize {
				findings = append(findings, fmt.Sprintf("attachment source %q was expected to be %d bytes but %d bytes were archived", row.Entity.Source.ID, *row.ExpectedSize, *row.ArchivedSize))
			}
			if file, present := recorded[path]; present {
				if file.Checksum != *row.Checksum || file.Bytes != *row.ArchivedSize {
					findings = append(findings, fmt.Sprintf("attachment binary %s is recorded differently in the manifest file list than in alih.db", path))
				}
			}
		default:
			findings = append(findings, fmt.Sprintf("attachment source %q has unknown download status %q", row.Entity.Source.ID, row.DownloadStatus))
		}
	}
	for id := range manifestAttachments {
		findings = append(findings, fmt.Sprintf("the manifest lists attachment %q that alih.db does not archive", id))
	}
	for path := range files {
		if !strings.HasPrefix(path, "attachments/") {
			continue
		}
		if _, expected := claimed[path]; !expected {
			findings = append(findings, fmt.Sprintf("%s is present in the archive but no archived attachment claims it", path))
		}
	}
	if len(findings) > 0 {
		v.fail("attachment_integrity", "archived attachment binaries do not match the evidence recorded for them", findings)
		return
	}
	if len(unresolved) > 0 {
		v.record("attachment_integrity", CheckIncomplete,
			fmt.Sprintf("%d retrieved attachments match their recorded size and checksum, but %d supported attachments remain unresolved", retrieved, len(unresolved)),
			unresolved)
		return
	}
	v.pass("attachment_integrity", fmt.Sprintf("all %d supported attachments are archived and match their recorded size and SHA-256 checksum", retrieved))
}

func compareAttachmentRecord(row attachmentRow, record archive.AttachmentRecord) string {
	mismatch := func(field string) string {
		return fmt.Sprintf("attachment source %q has a different %s in alih.db than in the manifest", row.Entity.Source.ID, field)
	}
	switch {
	case record.SourceID != row.Entity.Source.ID:
		return mismatch("source identifier")
	case record.RecordID != row.RecordID:
		return mismatch("owning record")
	case record.Status != row.DownloadStatus:
		return mismatch("download status")
	case !equalStrings(record.LocalPath, row.LocalPath):
		return mismatch("local path")
	case !equalStrings(record.Checksum, row.Checksum):
		return mismatch("checksum")
	case !equalInt64s(record.ArchivedSize, row.ArchivedSize):
		return mismatch("archived size")
	case !equalInt64s(record.ExpectedSize, row.ExpectedSize):
		return mismatch("expected size")
	case (record.Error == nil) != (row.Error == nil):
		return mismatch("unresolved reason")
	}
	return ""
}

// checkDiscrepancies proves that the manifest discloses exactly the
// discrepancies the archived data supports: nothing invented, nothing hidden.
func (v *verification) checkDiscrepancies(database *portableDatabase, manifest archive.Manifest) {
	expected := map[string]struct{}{}
	for _, row := range database.Attachments {
		if row.DownloadStatus != "RETRIEVED" {
			expected["ATTACHMENT_UNRESOLVED\x00"+row.Entity.Source.ID] = struct{}{}
		}
	}
	for _, row := range database.Relationships {
		if row.ResolutionState != "RESOLVED" {
			expected["RELATIONSHIP_"+row.ResolutionState+"\x00"+row.Entity.Source.ID] = struct{}{}
		}
	}
	declared := map[string]struct{}{}
	var findings []string
	for _, discrepancy := range manifest.Discrepancies {
		key := discrepancy.Kind + "\x00" + discrepancy.SourceID
		declared[key] = struct{}{}
		if _, supported := expected[key]; !supported {
			findings = append(findings, fmt.Sprintf("the manifest declares discrepancy %s for %q that the archived data does not support", discrepancy.Kind, discrepancy.SourceID))
		}
	}
	for key := range expected {
		if _, present := declared[key]; !present {
			parts := strings.SplitN(key, "\x00", 2)
			findings = append(findings, fmt.Sprintf("the archive contains %s for %q that the manifest does not disclose", parts[0], parts[1]))
		}
	}
	v.decide("discrepancy_reconciliation",
		fmt.Sprintf("the manifest discloses exactly the %d discrepancies the archived data supports", len(expected)),
		"the manifest does not disclose exactly the discrepancies the archived data supports",
		findings)
}

// checkSourceConsistencyScope records what the archive itself says about the
// consistency of its source snapshot. A non-atomic source snapshot can never
// support a point-in-time consistency claim, however clean the archive is.
func (v *verification) checkSourceConsistencyScope(database *portableDatabase, manifest archive.Manifest, databaseOK bool) {
	if databaseOK {
		if recorded := database.Metadata["source_snapshot_atomic"]; recorded != "" && recorded != boolText(manifest.InputSnapshot.Atomic) {
			v.fail("source_consistency_scope", "alih.db and manifest.json disagree about whether the source snapshot was atomic",
				[]string{fmt.Sprintf("alih.db records source_snapshot_atomic=%q, manifest records %v", recorded, manifest.InputSnapshot.Atomic)})
			return
		}
	}
	if !manifest.InputSnapshot.Atomic {
		v.unproven("source_consistency_scope",
			"Point-in-time consistency is not claimed: the source snapshot this archive was built from is recorded as non-atomic, so archived records may reflect different moments of the source traversal.", nil)
		return
	}
	v.pass("source_consistency_scope", "the source snapshot this archive was built from is recorded as atomic")
}

// checkAccessScopeCompleteness addresses PRD section 22's "authentication scope
// insufficient and undetected". A narrower credential simply returns fewer
// objects, and those fewer objects then reconcile perfectly against each other,
// so no archive-internal evidence can distinguish a complete Workspace from a
// partial one. Alih therefore makes the condition permanently
// detected-as-undetectable: the claim can never be established, so it can never
// be silently absent from a clean VERIFIED result.
func (v *verification) checkAccessScopeCompleteness(manifest archive.Manifest) {
	identity := "an account this archive does not name"
	if manifest.ExtractedBy != nil && strings.TrimSpace(manifest.ExtractedBy.ID) != "" {
		identity = fmt.Sprintf("%s (id %s)", manifest.ExtractedBy.Name, manifest.ExtractedBy.ID)
	}
	findings := []string{
		"This archive contains what " + identity + " could reach through the official API.",
		"A credential with narrower permissions returns fewer objects, and those fewer objects reconcile perfectly with each other, so archive-internal evidence cannot tell a complete Workspace from a partial one.",
		"Establishing this would require re-reading the source under a wider credential, which is outside what verifying an archive can do.",
	}
	if manifest.ExtractedBy == nil {
		findings = append(findings, "This archive does not record which account extracted it, so its access scope cannot even be attributed.")
	}
	v.unproven("access_scope_completeness",
		"Whether the credential used for this extraction could see the entire Workspace is not established, and cannot be established from the archive alone.",
		findings)
}

// checkLimitationPreservation keeps source limitations exactly as the source
// connector reported them. Passing verification never upgrades a PARTIAL,
// UNSUPPORTED, UNAVAILABLE or UNKNOWN capability.
func (v *verification) checkLimitationPreservation(manifest archive.Manifest) {
	var findings []string
	var limited []string
	for _, capability := range manifest.Capabilities {
		switch capability.State {
		case connector.CapabilitySupported:
		case connector.CapabilityPartial, connector.CapabilityUnsupported, connector.CapabilityUnavailable,
			connector.CapabilityUnknown, connector.CapabilityFailed:
			limited = append(limited, fmt.Sprintf("%s remains %s and verification does not change that: %s", capability.Name, capability.State, capability.Note))
		default:
			findings = append(findings, fmt.Sprintf("capability %q has unknown state %q", capability.Name, capability.State))
		}
	}
	if len(manifest.Capabilities) == 0 {
		findings = append(findings, "the manifest declares no source capabilities, so the scope of this archive cannot be established")
	}
	if len(findings) > 0 {
		v.fail("limitation_preservation", "the archive does not state a usable source capability scope", findings)
		return
	}
	if len(limited) > 0 {
		v.unproven("limitation_preservation",
			fmt.Sprintf("%d source capabilities are not fully supported; their data is outside anything this verification can prove.", len(limited)),
			limited)
		return
	}
	v.pass("limitation_preservation", "every declared source capability is SUPPORTED and is preserved unchanged by verification")
}

func readArchiveFile(root, name string) ([]byte, error) {
	path := filepath.Join(root, filepath.FromSlash(name))
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	return os.ReadFile(path)
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func equalStrings(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalInt64s(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
