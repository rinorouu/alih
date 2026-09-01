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

package clickup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"alih/internal/connector"
	"alih/internal/model"
	"alih/internal/snapshot"
)

type rawNamedObject struct {
	ID   json.RawMessage `json:"id"`
	Name json.RawMessage `json:"name"`
}

type rawUser struct {
	ID       json.RawMessage `json:"id"`
	Username json.RawMessage `json:"username"`
	Email    json.RawMessage `json:"email"`
}

type rawTaskDetail struct {
	ID           json.RawMessage `json:"id"`
	Name         json.RawMessage `json:"name"`
	Description  json.RawMessage `json:"description"`
	TextContent  json.RawMessage `json:"text_content"`
	Archived     json.RawMessage `json:"archived"`
	DateCreated  json.RawMessage `json:"date_created"`
	DateUpdated  json.RawMessage `json:"date_updated"`
	DateClosed   json.RawMessage `json:"date_closed"`
	DateDone     json.RawMessage `json:"date_done"`
	StartDate    json.RawMessage `json:"start_date"`
	DueDate      json.RawMessage `json:"due_date"`
	TimeEstimate json.RawMessage `json:"time_estimate"`
	TimeSpent    json.RawMessage `json:"time_spent"`
	Points       json.RawMessage `json:"points"`
	Status       json.RawMessage `json:"status"`
	Priority     json.RawMessage `json:"priority"`
	Creator      json.RawMessage `json:"creator"`
	Assignees    json.RawMessage `json:"assignees"`
	Watchers     json.RawMessage `json:"watchers"`
	Tags         json.RawMessage `json:"tags"`
	CustomFields json.RawMessage `json:"custom_fields"`
	Attachments  json.RawMessage `json:"attachments"`
	Dependencies json.RawMessage `json:"dependencies"`
	LinkedTasks  json.RawMessage `json:"linked_tasks"`
}

type rawComment struct {
	ID          json.RawMessage `json:"id"`
	Date        json.RawMessage `json:"date"`
	User        json.RawMessage `json:"user"`
	Comment     json.RawMessage `json:"comment"`
	CommentText json.RawMessage `json:"comment_text"`
}

type namedEvidence struct {
	name    *string
	rawPath string
}

type taskEvidence struct {
	detail  rawTaskDetail
	rawPath string
}

type commentEvidence struct {
	comment rawComment
	rawPath string
}

type fieldEvidence struct {
	definition json.RawMessage
	rawPath    string
}

// NormalizeSnapshot is the ClickUp-specific M4 adapter. It consumes validated
// immutable M3 evidence and emits only source-agnostic portable model types.
func NormalizeSnapshot(evidence snapshot.Evidence) (model.Archive, error) {
	if evidence.Connector != "clickup" {
		return model.Archive{}, fmt.Errorf("ClickUp adapter cannot normalize connector %q", evidence.Connector)
	}
	provider := evidence.Connector
	names := map[string]map[string]namedEvidence{
		"space": {}, "folder": {}, "list": {},
	}
	fields := make(map[string]fieldEvidence)
	tasks := make(map[string]taskEvidence)
	comments := make(map[string]commentEvidence)

	for _, response := range evidence.SortedResponses() {
		switch response.Operation {
		case "list Spaces":
			if err := collectNamedArray(response, "spaces", names["space"]); err != nil {
				return model.Archive{}, err
			}
		case "list Folders":
			if err := collectNamedArray(response, "folders", names["folder"]); err != nil {
				return model.Archive{}, err
			}
		case "list folderless Lists", "list Lists in Folder":
			if err := collectNamedArray(response, "lists", names["list"]); err != nil {
				return model.Archive{}, err
			}
		case "list Custom Fields":
			if err := collectFieldDefinitions(response, fields); err != nil {
				return model.Archive{}, err
			}
		case "get Task inventory details":
			var detail rawTaskDetail
			if err := decodeRawResponse(response, &detail); err != nil {
				return model.Archive{}, err
			}
			id, err := rawID(detail.ID)
			if err != nil {
				return model.Archive{}, fmt.Errorf("%s Task id: %w", response.RawPath, err)
			}
			if _, duplicate := tasks[id]; duplicate {
				return model.Archive{}, fmt.Errorf("duplicate Task detail for source id %q", id)
			}
			tasks[id] = taskEvidence{detail: detail, rawPath: archiveRawPath(response.RawPath)}
		case "list Task comments", "list threaded comment replies":
			if err := collectComments(response, comments); err != nil {
				return model.Archive{}, err
			}
		}
	}

	portable := model.Archive{
		Connector:               provider,
		CapabilitySchemaVersion: evidence.CapabilitySchemaVersion,
		Capabilities:            connector.CanonicalCapabilities(evidence.CapabilitySchemaVersion, evidence.Capabilities),
		Limitations: []string{
			"The source API does not provide an atomic snapshot; records may reflect different moments within the M3 traversal.",
			"Custom Field values are observed values only; Alih does not claim executable ClickUp formula or computed-field semantics.",
		},
	}
	workspaceName := nullableStringValue(evidence.Workspace.Name)
	portable.Workspace = model.Workspace{
		ID: model.PortableID(provider, "workspace", evidence.Workspace.ID), Name: workspaceName,
		Source: model.SourceRef{Provider: provider, Type: "workspace", ID: evidence.Workspace.ID, RawPath: "raw/inventory.json"},
	}

	index := make(map[string]connector.SourceObject, len(evidence.SourceObjects))
	for _, object := range evidence.SourceObjects {
		index[object.Type+"\x00"+object.ID] = object
	}
	if err := normalizeHierarchy(&portable, provider, evidence.SourceObjects, names); err != nil {
		return model.Archive{}, err
	}
	identityBySource := make(map[string]model.Identity)
	if err := normalizeTasks(&portable, provider, evidence.SourceObjects, tasks, fields, identityBySource); err != nil {
		return model.Archive{}, err
	}
	if err := normalizeComments(&portable, provider, evidence.SourceObjects, comments, identityBySource); err != nil {
		return model.Archive{}, err
	}
	if err := normalizeFields(&portable, provider, evidence.SourceObjects, fields); err != nil {
		return model.Archive{}, err
	}
	if err := normalizeTaskDetails(&portable, provider, evidence.SourceObjects, tasks, index); err != nil {
		return model.Archive{}, err
	}
	for _, identity := range identityBySource {
		portable.Identities = append(portable.Identities, identity)
	}
	sortPortable(&portable)
	if err := reconcileSourceObjects(evidence.SourceObjects, portable); err != nil {
		return model.Archive{}, err
	}
	return portable, nil
}

func normalizeHierarchy(portable *model.Archive, provider string, objects []connector.SourceObject, names map[string]map[string]namedEvidence) error {
	for _, object := range objects {
		switch object.Type {
		case "space", "folder":
			named, found := names[object.Type][object.ID]
			if !found {
				return fmt.Errorf("raw evidence is missing %s source id %q", object.Type, object.ID)
			}
			var parentID *string
			if object.Type == "folder" {
				value := model.PortableID(provider, "space", object.ParentID)
				parentID = &value
			}
			portable.Containers = append(portable.Containers, model.Container{
				ID: model.PortableID(provider, object.Type, object.ID), Kind: object.Type,
				WorkspaceID: portable.Workspace.ID, ParentID: parentID, Name: named.name,
				Source: model.SourceRef{Provider: provider, Type: object.Type, ID: object.ID, RawPath: named.rawPath},
			})
		case "list":
			named, found := names["list"][object.ID]
			if !found {
				return fmt.Errorf("raw evidence is missing list source id %q", object.ID)
			}
			if object.ParentType != "space" && object.ParentType != "folder" {
				return fmt.Errorf("List %q has unsupported parent type %q", object.ID, object.ParentType)
			}
			portable.Collections = append(portable.Collections, model.Collection{
				ID: model.PortableID(provider, "list", object.ID), WorkspaceID: portable.Workspace.ID,
				ContainerID: model.PortableID(provider, object.ParentType, object.ParentID), Name: named.name,
				Source: model.SourceRef{Provider: provider, Type: "list", ID: object.ID, RawPath: named.rawPath},
			})
		}
	}
	return nil
}

func normalizeTasks(portable *model.Archive, provider string, objects []connector.SourceObject, tasks map[string]taskEvidence, fields map[string]fieldEvidence, identities map[string]model.Identity) error {
	records := recordObjectIndex(objects)
	for _, object := range objects {
		if object.Type != "task" && object.Type != "subtask" {
			continue
		}
		evidence, found := tasks[object.ID]
		if !found {
			return fmt.Errorf("raw evidence is missing Task detail source id %q", object.ID)
		}
		detail := evidence.detail
		title, err := rawNullableString(detail.Name)
		if err != nil || title == nil {
			return fmt.Errorf("Task %q name is unavailable or invalid", object.ID)
		}
		description, err := rawNullableString(detail.Description)
		if err != nil {
			return fmt.Errorf("Task %q description: %w", object.ID, err)
		}
		textContent, err := rawNullableString(detail.TextContent)
		if err != nil {
			return fmt.Errorf("Task %q text_content: %w", object.ID, err)
		}
		status, statusType, err := rawStatus(detail.Status)
		if err != nil {
			return fmt.Errorf("Task %q status: %w", object.ID, err)
		}
		priority, err := rawPriority(detail.Priority)
		if err != nil {
			return fmt.Errorf("Task %q priority: %w", object.ID, err)
		}
		archived, err := rawNullableBool(detail.Archived)
		if err != nil {
			return fmt.Errorf("Task %q archived: %w", object.ID, err)
		}
		record := model.Record{
			ID: model.PortableID(provider, "task", object.ID), Kind: object.Type,
			WorkspaceID: portable.Workspace.ID, Title: title, Description: description, TextContent: textContent,
			Status: status, StatusType: statusType, Priority: priority, Archived: archived,
			Source: model.SourceRef{Provider: provider, Type: "task", ID: object.ID, RawPath: evidence.rawPath},
		}
		if object.Type == "task" {
			record.CollectionID = model.PortableID(provider, "list", object.ParentID)
		} else {
			parent := model.PortableID(provider, "task", object.ParentID)
			record.ParentRecordID = &parent
			collectionSourceID, err := taskCollectionSourceID(records, object.ID, make(map[string]struct{}))
			if err != nil {
				return fmt.Errorf("Subtask %q: %w", object.ID, err)
			}
			record.CollectionID = model.PortableID(provider, "list", collectionSourceID)
		}
		for raw, target := range map[*json.RawMessage]**string{
			&detail.DateCreated: &record.CreatedAtSource, &detail.DateUpdated: &record.UpdatedAtSource,
			&detail.DateClosed: &record.ClosedAtSource, &detail.DateDone: &record.DoneAtSource,
			&detail.StartDate: &record.StartAtSource, &detail.DueDate: &record.DueAtSource,
		} {
			value, err := rawNullableScalar(*raw)
			if err != nil {
				return fmt.Errorf("Task %q timestamp: %w", object.ID, err)
			}
			*target = value
		}
		record.TimeEstimateMS, err = rawNullableInt64(detail.TimeEstimate)
		if err != nil {
			return fmt.Errorf("Task %q time_estimate: %w", object.ID, err)
		}
		record.TimeSpentMS, err = rawNullableInt64(detail.TimeSpent)
		if err != nil {
			return fmt.Errorf("Task %q time_spent: %w", object.ID, err)
		}
		record.Points, err = rawNullableFloat64(detail.Points)
		if err != nil {
			return fmt.Errorf("Task %q points: %w", object.ID, err)
		}
		portable.Records = append(portable.Records, record)

		if err := collectTaskPeople(portable, provider, record.ID, evidence.rawPath, detail, identities); err != nil {
			return fmt.Errorf("Task %q identities: %w", object.ID, err)
		}
		if err := collectTaskTags(portable, record.ID, detail.Tags); err != nil {
			return fmt.Errorf("Task %q tags: %w", object.ID, err)
		}
		if err := collectTaskFieldValues(portable, provider, record.ID, detail.CustomFields, fields); err != nil {
			return fmt.Errorf("Task %q Custom Fields: %w", object.ID, err)
		}
	}
	return nil
}

func normalizeComments(portable *model.Archive, provider string, objects []connector.SourceObject, comments map[string]commentEvidence, identities map[string]model.Identity) error {
	objectByID := make(map[string]connector.SourceObject)
	for _, object := range objects {
		if object.Type == "comment" {
			objectByID[object.ID] = object
		}
	}
	var recordForComment func(string, map[string]struct{}) (string, error)
	recordForComment = func(id string, seen map[string]struct{}) (string, error) {
		if _, duplicate := seen[id]; duplicate {
			return "", errors.New("Comment parent hierarchy contains a cycle")
		}
		seen[id] = struct{}{}
		object, found := objectByID[id]
		if !found {
			return "", fmt.Errorf("Comment %q is missing from source index", id)
		}
		if object.ParentType == "task" || object.ParentType == "subtask" {
			return model.PortableID(provider, "task", object.ParentID), nil
		}
		if object.ParentType != "comment" {
			return "", fmt.Errorf("Comment %q has unsupported parent type %q", id, object.ParentType)
		}
		return recordForComment(object.ParentID, seen)
	}
	for _, object := range objects {
		if object.Type != "comment" {
			continue
		}
		evidence, found := comments[object.ID]
		if !found {
			return fmt.Errorf("raw evidence is missing Comment source id %q", object.ID)
		}
		recordID, err := recordForComment(object.ID, make(map[string]struct{}))
		if err != nil {
			return err
		}
		comment := model.Comment{
			ID: model.PortableID(provider, "comment", object.ID), WorkspaceID: portable.Workspace.ID,
			RecordID: recordID,
			Source:   model.SourceRef{Provider: provider, Type: "comment", ID: object.ID, RawPath: evidence.rawPath},
		}
		if object.ParentType == "comment" {
			parent := model.PortableID(provider, "comment", object.ParentID)
			comment.ParentCommentID = &parent
		}
		comment.CreatedAtSource, err = rawNullableScalar(evidence.comment.Date)
		if err != nil {
			return fmt.Errorf("Comment %q date: %w", object.ID, err)
		}
		structuredBody := evidence.comment.Comment
		if !isMissingOrNull(structuredBody) {
			comment.BodyJSON, err = canonicalJSON(structuredBody)
			if err != nil {
				return fmt.Errorf("Comment %q body: %w", object.ID, err)
			}
		}
		if !isMissingOrNull(evidence.comment.CommentText) {
			comment.BodyText, err = rawNullableString(evidence.comment.CommentText)
			if err != nil {
				return fmt.Errorf("Comment %q comment_text: %w", object.ID, err)
			}
		} else if !isMissingOrNull(structuredBody) {
			comment.BodyText = renderedCommentText(structuredBody)
		}
		if !isMissingOrNull(evidence.comment.User) {
			identity, err := normalizeIdentity(provider, evidence.comment.User, evidence.rawPath)
			if err != nil {
				return fmt.Errorf("Comment %q author: %w", object.ID, err)
			}
			if err := mergeIdentity(identities, identity); err != nil {
				return err
			}
			comment.AuthorIdentityID = &identity.ID
		}
		portable.Comments = append(portable.Comments, comment)
	}
	return nil
}

func normalizeFields(portable *model.Archive, provider string, objects []connector.SourceObject, fields map[string]fieldEvidence) error {
	for _, object := range objects {
		if object.Type != "custom_field" {
			continue
		}
		field, found := fields[object.ID]
		if !found {
			return fmt.Errorf("raw evidence is missing Custom Field source id %q", object.ID)
		}
		var definition struct {
			Name json.RawMessage `json:"name"`
			Type json.RawMessage `json:"type"`
		}
		if err := json.Unmarshal(field.definition, &definition); err != nil {
			return err
		}
		name, err := rawNullableString(definition.Name)
		if err != nil {
			return fmt.Errorf("Custom Field %q name: %w", object.ID, err)
		}
		fieldType, err := rawNullableString(definition.Type)
		if err != nil {
			return fmt.Errorf("Custom Field %q type: %w", object.ID, err)
		}
		state := "SOURCE_DEFINITION_ONLY"
		if fieldType != nil && isComputedFieldType(*fieldType) {
			state = "OBSERVED_ONLY_NO_EXECUTION"
		}
		portable.FieldDefinitions = append(portable.FieldDefinitions, model.FieldDefinition{
			ID: model.PortableID(provider, "custom_field", object.ID), Name: name, FieldType: fieldType,
			SemanticsState: state, DefinitionJSON: field.definition,
			Source: model.SourceRef{Provider: provider, Type: "custom_field", ID: object.ID, RawPath: field.rawPath},
		})
	}
	return nil
}

func normalizeTaskDetails(portable *model.Archive, provider string, objects []connector.SourceObject, tasks map[string]taskEvidence, index map[string]connector.SourceObject) error {
	recordSources := make(map[string]struct{})
	for _, object := range objects {
		if object.Type == "task" || object.Type == "subtask" {
			recordSources[object.ID] = struct{}{}
		}
	}
	relationships := make(map[string]model.Relationship)
	attachments := make(map[string]model.Attachment)
	taskIDs := make([]string, 0, len(tasks))
	for taskID := range tasks {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	for _, taskID := range taskIDs {
		evidence := tasks[taskID]
		recordID := model.PortableID(provider, "task", taskID)
		var rawAttachments []json.RawMessage
		if err := decodeOptionalArray(evidence.detail.Attachments, &rawAttachments); err != nil {
			return fmt.Errorf("Task %q attachments: %w", taskID, err)
		}
		for _, raw := range rawAttachments {
			attachment, sourceID, err := normalizeAttachment(provider, portable.Workspace.ID, recordID, evidence.rawPath, raw)
			if err != nil {
				return fmt.Errorf("Task %q attachment: %w", taskID, err)
			}
			if _, duplicate := attachments[sourceID]; duplicate {
				return fmt.Errorf("duplicate Attachment source id %q in Task details", sourceID)
			}
			attachments[sourceID] = attachment
		}

		var dependencies []json.RawMessage
		if err := decodeOptionalArray(evidence.detail.Dependencies, &dependencies); err != nil {
			return fmt.Errorf("Task %q dependencies: %w", taskID, err)
		}
		for _, raw := range dependencies {
			var dependency struct {
				TaskID    json.RawMessage `json:"task_id"`
				DependsOn json.RawMessage `json:"depends_on"`
			}
			if err := json.Unmarshal(raw, &dependency); err != nil {
				return err
			}
			from, err := rawID(dependency.TaskID)
			if err != nil {
				return err
			}
			to, err := rawID(dependency.DependsOn)
			if err != nil {
				return err
			}
			id := from + "->" + to
			key := "task_dependency\x00" + id
			if _, exists := relationships[key]; !exists {
				relationships[key] = portableRelationship(provider, "task_dependency", id, from, to, evidence.rawPath, raw, recordSources)
			}
		}
		var links []json.RawMessage
		if err := decodeOptionalArray(evidence.detail.LinkedTasks, &links); err != nil {
			return fmt.Errorf("Task %q linked_tasks: %w", taskID, err)
		}
		for _, raw := range links {
			var link struct {
				TaskID json.RawMessage `json:"task_id"`
				LinkID json.RawMessage `json:"link_id"`
			}
			if err := json.Unmarshal(raw, &link); err != nil {
				return err
			}
			left, err := rawID(link.TaskID)
			if err != nil {
				return err
			}
			right, err := rawID(link.LinkID)
			if err != nil {
				return err
			}
			if right < left {
				left, right = right, left
			}
			id := left + "<->" + right
			key := "task_link\x00" + id
			if _, exists := relationships[key]; !exists {
				relationships[key] = portableRelationship(provider, "task_link", id, left, right, evidence.rawPath, raw, recordSources)
			}
		}
	}
	for sourceID, attachment := range attachments {
		if _, expected := index["attachment\x00"+sourceID]; !expected {
			return fmt.Errorf("Task detail contains Attachment %q absent from M3 source index", sourceID)
		}
		portable.Attachments = append(portable.Attachments, attachment)
	}
	for _, relationship := range relationships {
		sourceType := "relationship." + relationship.Kind
		if _, expected := index[sourceType+"\x00"+relationship.Source.ID]; !expected {
			return fmt.Errorf("Task detail contains %s %q absent from M3 source index", sourceType, relationship.Source.ID)
		}
		portable.Relationships = append(portable.Relationships, relationship)
	}
	return nil
}

func collectNamedArray(response snapshot.EvidenceResponse, field string, destination map[string]namedEvidence) error {
	var envelope map[string]json.RawMessage
	if err := decodeRawResponse(response, &envelope); err != nil {
		return err
	}
	var values []rawNamedObject
	if err := decodeRequiredRawArray(envelope[field], &values); err != nil {
		return fmt.Errorf("%s %s: %w", response.RawPath, field, err)
	}
	for _, value := range values {
		id, err := rawID(value.ID)
		if err != nil {
			return fmt.Errorf("%s %s id: %w", response.RawPath, field, err)
		}
		name, err := rawNullableString(value.Name)
		if err != nil {
			return fmt.Errorf("%s %s %q name: %w", response.RawPath, field, id, err)
		}
		rawPath := archiveRawPath(response.RawPath)
		if existing, duplicate := destination[id]; duplicate {
			if !equalNullableString(existing.name, name) {
				return fmt.Errorf("source id %q has conflicting names", id)
			}
			continue
		}
		destination[id] = namedEvidence{name: name, rawPath: rawPath}
	}
	return nil
}

func collectFieldDefinitions(response snapshot.EvidenceResponse, destination map[string]fieldEvidence) error {
	var envelope map[string]json.RawMessage
	if err := decodeRawResponse(response, &envelope); err != nil {
		return err
	}
	var values []json.RawMessage
	if err := decodeRequiredRawArray(envelope["fields"], &values); err != nil {
		return fmt.Errorf("%s fields: %w", response.RawPath, err)
	}
	for _, value := range values {
		var identity struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(value, &identity); err != nil {
			return err
		}
		id, err := rawID(identity.ID)
		if err != nil {
			return err
		}
		canonical, err := canonicalJSON(value)
		if err != nil {
			return err
		}
		if existing, duplicate := destination[id]; duplicate {
			if !bytes.Equal(existing.definition, canonical) {
				return fmt.Errorf("Custom Field %q has conflicting source definitions", id)
			}
			continue
		}
		destination[id] = fieldEvidence{definition: canonical, rawPath: archiveRawPath(response.RawPath)}
	}
	return nil
}

func collectComments(response snapshot.EvidenceResponse, destination map[string]commentEvidence) error {
	var envelope map[string]json.RawMessage
	if err := decodeRawResponse(response, &envelope); err != nil {
		return err
	}
	var values []rawComment
	if err := decodeRequiredRawArray(envelope["comments"], &values); err != nil {
		return fmt.Errorf("%s comments: %w", response.RawPath, err)
	}
	for _, value := range values {
		id, err := rawID(value.ID)
		if err != nil {
			return err
		}
		if _, duplicate := destination[id]; duplicate {
			return fmt.Errorf("duplicate Comment source id %q", id)
		}
		destination[id] = commentEvidence{comment: value, rawPath: archiveRawPath(response.RawPath)}
	}
	return nil
}

func collectTaskPeople(portable *model.Archive, provider, recordID, rawPath string, detail rawTaskDetail, identities map[string]model.Identity) error {
	roles := []struct {
		role string
		raw  json.RawMessage
		many bool
	}{{"creator", detail.Creator, false}, {"assignee", detail.Assignees, true}, {"watcher", detail.Watchers, true}}
	for _, role := range roles {
		if isMissingOrNull(role.raw) {
			continue
		}
		users := []json.RawMessage{role.raw}
		if role.many {
			if err := json.Unmarshal(role.raw, &users); err != nil {
				return err
			}
		}
		for position, raw := range users {
			identity, err := normalizeIdentity(provider, raw, rawPath)
			if err != nil {
				return err
			}
			if err := mergeIdentity(identities, identity); err != nil {
				return err
			}
			portable.RecordIdentities = append(portable.RecordIdentities, model.RecordIdentity{
				RecordID: recordID, IdentityID: identity.ID, Role: role.role, Position: position,
			})
		}
	}
	return nil
}

func collectTaskTags(portable *model.Archive, recordID string, raw json.RawMessage) error {
	if isMissingOrNull(raw) {
		return nil
	}
	var tags []struct {
		Name json.RawMessage `json:"name"`
	}
	if err := json.Unmarshal(raw, &tags); err != nil {
		return err
	}
	for position, tag := range tags {
		name, err := rawNullableString(tag.Name)
		if err != nil || name == nil {
			return errors.New("tag name is missing or invalid")
		}
		portable.RecordTags = append(portable.RecordTags, model.RecordTag{RecordID: recordID, Name: *name, Position: position})
	}
	return nil
}

func collectTaskFieldValues(portable *model.Archive, provider, recordID string, raw json.RawMessage, fields map[string]fieldEvidence) error {
	if isMissingOrNull(raw) {
		return nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	seen := make(map[string]struct{})
	for _, value := range values {
		var field map[string]json.RawMessage
		if err := json.Unmarshal(value, &field); err != nil {
			return err
		}
		id, err := rawID(field["id"])
		if err != nil {
			return err
		}
		if _, known := fields[id]; !known {
			return fmt.Errorf("field %q is absent from accessible field definitions", id)
		}
		observed, present := field["value"]
		if !present {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate observed value for field %q", id)
		}
		seen[id] = struct{}{}
		canonical, err := canonicalJSON(observed)
		if err != nil {
			return err
		}
		portable.RecordFieldValues = append(portable.RecordFieldValues, model.RecordFieldValue{
			RecordID: recordID, FieldID: model.PortableID(provider, "custom_field", id),
			ObservedValueJSON: canonical, SemanticsState: "OBSERVED_ONLY",
		})
	}
	return nil
}

func normalizeIdentity(provider string, raw json.RawMessage, rawPath string) (model.Identity, error) {
	var user rawUser
	if err := json.Unmarshal(raw, &user); err != nil {
		return model.Identity{}, err
	}
	sourceID, err := rawID(user.ID)
	if err != nil {
		return model.Identity{}, err
	}
	username, err := rawNullableString(user.Username)
	if err != nil {
		return model.Identity{}, err
	}
	email, err := rawNullableString(user.Email)
	if err != nil {
		return model.Identity{}, err
	}
	return model.Identity{
		ID: model.PortableID(provider, "identity", sourceID), Username: username, Email: email,
		Source: model.SourceRef{Provider: provider, Type: "user", ID: sourceID, RawPath: rawPath},
	}, nil
}

func mergeIdentity(destination map[string]model.Identity, identity model.Identity) error {
	if existing, found := destination[identity.Source.ID]; found {
		if !equalNullableString(existing.Username, identity.Username) || !equalNullableString(existing.Email, identity.Email) {
			return fmt.Errorf("Identity source id %q has conflicting observed metadata", identity.Source.ID)
		}
		return nil
	}
	destination[identity.Source.ID] = identity
	return nil
}

func normalizeAttachment(provider, workspaceID, recordID, rawPath string, raw json.RawMessage) (model.Attachment, string, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return model.Attachment{}, "", err
	}
	sourceID, err := rawID(value["id"])
	if err != nil {
		return model.Attachment{}, "", err
	}
	filename, err := rawNullableString(value["title"])
	if err != nil {
		return model.Attachment{}, "", err
	}
	mediaType, err := rawNullableString(value["mimetype"])
	if err != nil {
		return model.Attachment{}, "", err
	}
	expectedSize, err := rawNullableInt64(value["size"])
	if err != nil {
		return model.Attachment{}, "", err
	}
	sourceURL, err := rawNullableString(value["url"])
	if err != nil {
		return model.Attachment{}, "", err
	}
	downloadURL, err := rawNullableString(value["url_w_query"])
	if err != nil {
		return model.Attachment{}, "", err
	}
	if downloadURL == nil {
		downloadURL = sourceURL
	}
	var safeSourceURL *string
	if sourceURL != nil {
		sanitized := stripURLQuery(*sourceURL)
		if sanitized != "" {
			safeSourceURL = &sanitized
		}
	}
	attachment := model.Attachment{
		ID: model.PortableID(provider, "attachment", sourceID), WorkspaceID: workspaceID,
		RecordID: recordID, Filename: filename, MediaType: mediaType, ExpectedSize: expectedSize,
		SourceURL: safeSourceURL, DownloadStatus: "PENDING",
		Source: model.SourceRef{Provider: provider, Type: "attachment", ID: sourceID, RawPath: rawPath},
	}
	if downloadURL == nil || strings.TrimSpace(*downloadURL) == "" {
		attachment.DownloadStatus = "UNRESOLVED"
		message := "source attachment metadata did not provide a download URL"
		attachment.Error = &message
	} else {
		attachment.DownloadURL = *downloadURL
	}
	return attachment, sourceID, nil
}

func portableRelationship(provider, kind, sourceID, from, to, rawPath string, raw json.RawMessage, recordSources map[string]struct{}) model.Relationship {
	resolution := "RESOLVED"
	var fromID, toID *string
	if _, found := recordSources[from]; found {
		value := model.PortableID(provider, "task", from)
		fromID = &value
	} else {
		resolution = "PARTIAL_EXTERNAL"
	}
	if _, found := recordSources[to]; found {
		value := model.PortableID(provider, "task", to)
		toID = &value
	} else {
		resolution = "PARTIAL_EXTERNAL"
	}
	metadata, _ := canonicalJSON(raw)
	return model.Relationship{
		ID: model.PortableID(provider, "relationship."+kind, sourceID), Kind: kind,
		FromRecordID: fromID, ToRecordID: toID, FromSourceID: from, ToSourceID: to,
		ResolutionState: resolution, SourceMetadataJSON: metadata,
		Source: model.SourceRef{Provider: provider, Type: "relationship." + kind, ID: sourceID, RawPath: rawPath, IDComposite: true},
	}
}

func reconcileSourceObjects(expected []connector.SourceObject, portable model.Archive) error {
	actual := make(map[string]struct{})
	add := func(source model.SourceRef) { actual[source.Type+"\x00"+source.ID] = struct{}{} }
	add(portable.Workspace.Source)
	for _, item := range portable.Containers {
		add(item.Source)
	}
	for _, item := range portable.Collections {
		add(item.Source)
	}
	for _, item := range portable.Records {
		typeName := item.Kind
		actual[typeName+"\x00"+item.Source.ID] = struct{}{}
	}
	for _, item := range portable.Comments {
		add(item.Source)
	}
	for _, item := range portable.Attachments {
		add(item.Source)
	}
	for _, item := range portable.FieldDefinitions {
		add(item.Source)
	}
	for _, item := range portable.Relationships {
		add(item.Source)
	}
	for _, source := range expected {
		key := source.Type + "\x00" + source.ID
		if _, found := actual[key]; !found {
			return fmt.Errorf("portable model omitted expected %s source id %q", source.Type, source.ID)
		}
		delete(actual, key)
	}
	if len(actual) != 0 {
		keys := make([]string, 0, len(actual))
		for key := range actual {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return fmt.Errorf("portable model contains source objects absent from M3 inventory: %q", keys[0])
	}
	return nil
}

func sortPortable(portable *model.Archive) {
	sort.Slice(portable.Containers, func(i, j int) bool { return portable.Containers[i].ID < portable.Containers[j].ID })
	sort.Slice(portable.Collections, func(i, j int) bool { return portable.Collections[i].ID < portable.Collections[j].ID })
	sort.Slice(portable.Records, func(i, j int) bool { return portable.Records[i].ID < portable.Records[j].ID })
	sort.Slice(portable.Identities, func(i, j int) bool { return portable.Identities[i].ID < portable.Identities[j].ID })
	sort.Slice(portable.RecordIdentities, func(i, j int) bool {
		a, b := portable.RecordIdentities[i], portable.RecordIdentities[j]
		if a.RecordID != b.RecordID {
			return a.RecordID < b.RecordID
		}
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.Position < b.Position
	})
	sort.Slice(portable.RecordTags, func(i, j int) bool {
		a, b := portable.RecordTags[i], portable.RecordTags[j]
		if a.RecordID != b.RecordID {
			return a.RecordID < b.RecordID
		}
		return a.Position < b.Position
	})
	sort.Slice(portable.Comments, func(i, j int) bool { return portable.Comments[i].ID < portable.Comments[j].ID })
	sort.Slice(portable.FieldDefinitions, func(i, j int) bool { return portable.FieldDefinitions[i].ID < portable.FieldDefinitions[j].ID })
	sort.Slice(portable.RecordFieldValues, func(i, j int) bool {
		a, b := portable.RecordFieldValues[i], portable.RecordFieldValues[j]
		if a.RecordID != b.RecordID {
			return a.RecordID < b.RecordID
		}
		return a.FieldID < b.FieldID
	})
	sort.Slice(portable.Relationships, func(i, j int) bool { return portable.Relationships[i].ID < portable.Relationships[j].ID })
	sort.Slice(portable.Attachments, func(i, j int) bool { return portable.Attachments[i].ID < portable.Attachments[j].ID })
	portable.Capabilities = connector.CanonicalCapabilities(portable.CapabilitySchemaVersion, portable.Capabilities)
	sort.Strings(portable.Limitations)
}

func decodeRawResponse(response snapshot.EvidenceResponse, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(response.Body))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode %s: %w", response.RawPath, err)
	}
	return nil
}

func decodeRequiredRawArray(raw json.RawMessage, destination any) error {
	if isMissingOrNull(raw) {
		return errors.New("required array is missing")
	}
	return json.Unmarshal(raw, destination)
}

func decodeOptionalArray(raw json.RawMessage, destination any) error {
	if isMissingOrNull(raw) {
		return nil
	}
	return json.Unmarshal(raw, destination)
}

func rawID(raw json.RawMessage) (string, error) {
	value, err := rawNullableScalar(raw)
	if err != nil || value == nil || strings.TrimSpace(*value) == "" {
		return "", errors.New("missing or invalid source id")
	}
	return *value, nil
}

func rawNullableScalar(raw json.RawMessage) (*string, error) {
	if isMissingOrNull(raw) {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return &text, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		value := number.String()
		return &value, nil
	}
	return nil, errors.New("value is not a string, number, or null")
}

func rawNullableString(raw json.RawMessage) (*string, error) {
	if isMissingOrNull(raw) {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New("value is not a string or null")
	}
	return &value, nil
}

func rawNullableBool(raw json.RawMessage) (*bool, error) {
	if isMissingOrNull(raw) {
		return nil, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New("value is not a boolean or null")
	}
	return &value, nil
}

func rawNullableInt64(raw json.RawMessage) (*int64, error) {
	value, err := rawNullableScalar(raw)
	if err != nil || value == nil {
		return nil, err
	}
	parsed, err := strconv.ParseInt(*value, 10, 64)
	if err != nil {
		return nil, errors.New("value is not an integer")
	}
	return &parsed, nil
}

func rawNullableFloat64(raw json.RawMessage) (*float64, error) {
	value, err := rawNullableScalar(raw)
	if err != nil || value == nil {
		return nil, err
	}
	parsed, err := strconv.ParseFloat(*value, 64)
	if err != nil {
		return nil, errors.New("value is not numeric")
	}
	return &parsed, nil
}

func rawStatus(raw json.RawMessage) (*string, *string, error) {
	if isMissingOrNull(raw) {
		return nil, nil, nil
	}
	var value struct {
		Status json.RawMessage `json:"status"`
		Type   json.RawMessage `json:"type"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, nil, err
	}
	status, err := rawNullableString(value.Status)
	if err != nil {
		return nil, nil, err
	}
	statusType, err := rawNullableString(value.Type)
	return status, statusType, err
}

func rawPriority(raw json.RawMessage) (*string, error) {
	if isMissingOrNull(raw) {
		return nil, nil
	}
	var value struct {
		Priority json.RawMessage `json:"priority"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return rawNullableScalar(value.Priority)
}

func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func renderedCommentText(raw json.RawMessage) *string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return &text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return nil
	}
	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(part.Text)
	}
	value := builder.String()
	return &value
}

func isMissingOrNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func archiveRawPath(path string) string {
	return filepath.ToSlash(filepath.Join("raw", filepath.FromSlash(path)))
}

func stripURLQuery(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func nullableStringValue(value string) *string { return &value }

func equalNullableString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func isComputedFieldType(value string) bool {
	switch strings.ToLower(value) {
	case "formula", "rollup", "calculation", "automatic_progress", "progress_auto":
		return true
	default:
		return false
	}
}

// recordObjectIndex indexes the record-bearing source objects by original id so
// that ancestry lookups stay linear in the size of the Workspace rather than
// rescanning the whole source index for every nested record.
func recordObjectIndex(objects []connector.SourceObject) map[string]connector.SourceObject {
	index := make(map[string]connector.SourceObject, len(objects))
	for _, object := range objects {
		if object.Type == "task" || object.Type == "subtask" {
			index[object.ID] = object
		}
	}
	return index
}

func taskCollectionSourceID(records map[string]connector.SourceObject, id string, seen map[string]struct{}) (string, error) {
	if _, duplicate := seen[id]; duplicate {
		return "", errors.New("Task parent hierarchy contains a cycle")
	}
	seen[id] = struct{}{}
	object, found := records[id]
	if !found {
		return "", fmt.Errorf("Task parent source id %q is missing", id)
	}
	if object.Type == "task" {
		if object.ParentType != "list" {
			return "", fmt.Errorf("Task %q has parent type %q, want list", id, object.ParentType)
		}
		return object.ParentID, nil
	}
	return taskCollectionSourceID(records, object.ParentID, seen)
}

// Normalizer adapts the package-level NormalizeSnapshot into the interface the
// M3-to-M4 coordinator selects by connector name, so Core can hold a set of
// adapters without importing any of them.
type Normalizer struct{}

// Connector names the connector this adapter normalizes.
func (Normalizer) Connector() string { return "clickup" }

// DisplayName is the human name recorded in archives this adapter produces.
func (Normalizer) DisplayName() string { return "ClickUp" }

// NormalizeSnapshot turns ClickUp raw evidence into the portable model.
func (Normalizer) NormalizeSnapshot(evidence snapshot.Evidence) (model.Archive, error) {
	return NormalizeSnapshot(evidence)
}
