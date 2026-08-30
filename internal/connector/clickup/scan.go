// Copyright 2026 rinorouu
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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"alih/internal/connector"
)

const (
	maxTaskPages    = 100000
	maxCommentPages = 100000
)

type locationRecord struct {
	ID         string
	Name       string
	ParentType string
	ParentID   string
}

type taskRecord struct {
	ID       string
	ListID   string
	ParentID string
}

type commentRecord struct {
	ID         string
	ParentType string
	ParentID   string
}

type relationshipRecord struct {
	ID        string
	Kind      string
	FromID    string
	ToID      string
	Composite bool
}

type scanState struct {
	spaces        map[string]locationRecord
	folders       map[string]locationRecord
	lists         map[string]locationRecord
	tasks         map[string]taskRecord
	comments      map[string]commentRecord
	attachments   map[string]string
	customFields  map[string]string
	relationships map[string]relationshipRecord
}

func newScanState() *scanState {
	return &scanState{
		spaces:        make(map[string]locationRecord),
		folders:       make(map[string]locationRecord),
		lists:         make(map[string]locationRecord),
		tasks:         make(map[string]taskRecord),
		comments:      make(map[string]commentRecord),
		attachments:   make(map[string]string),
		customFields:  make(map[string]string),
		relationships: make(map[string]relationshipRecord),
	}
}

// Scan performs the complete read-only M2 traversal for one Workspace.
func (c *Client) Scan(ctx context.Context, credential string, workspace connector.Workspace) (connector.ScanResult, error) {
	result, _, err := c.scan(ctx, credential, workspace)
	return result, err
}

// Extract performs M3 raw extraction by recording the exact successful API
// responses produced by the already-validated M2 traversal. It returns no
// partial logical inventory when any request, parse, or reconciliation fails.
func (c *Client) Extract(ctx context.Context, credential string, workspace connector.Workspace, sink connector.RawEvidenceSink) (connector.ExtractionResult, error) {
	if sink == nil {
		return connector.ExtractionResult{}, responseError("start raw extraction", errors.New("raw evidence sink is required"))
	}
	recordingClient := *c
	recordingClient.evidence = sink
	result, state, err := recordingClient.scan(ctx, credential, workspace)
	if err != nil {
		return connector.ExtractionResult{}, err
	}
	return connector.ExtractionResult{
		ScanResult:    result,
		SourceObjects: sourceObjects(workspace, state),
	}, nil
}

func (c *Client) scan(ctx context.Context, credential string, workspace connector.Workspace) (connector.ScanResult, *scanState, error) {
	if strings.TrimSpace(credential) == "" || strings.ContainsAny(credential, "\r\n") {
		return connector.ScanResult{}, nil, &Error{
			Kind:      ErrorAuthentication,
			Operation: "validate scan credential",
			Cause:     errors.New("personal token is empty or malformed"),
		}
	}
	if err := validatePathID(workspace.ID); err != nil {
		return connector.ScanResult{}, nil, responseError("validate Workspace for scan", err)
	}

	state := newScanState()
	if err := c.scanHierarchy(ctx, credential, workspace.ID, state); err != nil {
		return connector.ScanResult{}, nil, err
	}
	if err := c.scanCustomFields(ctx, credential, workspace.ID, state); err != nil {
		return connector.ScanResult{}, nil, err
	}
	if err := c.scanTasks(ctx, credential, state); err != nil {
		return connector.ScanResult{}, nil, err
	}
	if err := validateTaskParents(state.tasks); err != nil {
		return connector.ScanResult{}, nil, responseError("validate Task hierarchy", err)
	}
	if err := c.scanTaskDetails(ctx, credential, state); err != nil {
		return connector.ScanResult{}, nil, err
	}

	var tasks, subtasks int
	for _, task := range state.tasks {
		if task.ParentID == "" {
			tasks++
		} else {
			subtasks++
		}
	}

	result := connector.ScanResult{
		Workspace: workspace,
		Inventory: connector.Inventory{
			Spaces:        len(state.spaces),
			Folders:       len(state.folders),
			Lists:         len(state.lists),
			Tasks:         tasks,
			Subtasks:      subtasks,
			Comments:      len(state.comments),
			Attachments:   len(state.attachments),
			CustomFields:  len(state.customFields),
			Relationships: len(state.relationships),
		},
		Capabilities: m2Capabilities(),
	}
	return result, state, nil
}

func validateTaskParents(tasks map[string]taskRecord) error {
	for _, task := range tasks {
		if task.ParentID == "" {
			continue
		}
		if _, found := tasks[task.ParentID]; !found {
			return fmt.Errorf("subtask %q references missing parent Task %q", task.ID, task.ParentID)
		}
	}
	return nil
}

type namedSourceObject struct {
	ID   json.RawMessage `json:"id"`
	Name string          `json:"name"`
}

func (c *Client) scanHierarchy(ctx context.Context, credential, workspaceID string, state *scanState) error {
	for _, archived := range []bool{false, true} {
		var response struct {
			Spaces json.RawMessage `json:"spaces"`
		}
		query := url.Values{"archived": {fmt.Sprint(archived)}}
		if err := c.getWithQuery(ctx, credential, "/team/"+workspaceID+"/space", query, "list Spaces", &response); err != nil {
			return err
		}
		var spaces []namedSourceObject
		if err := decodeRequiredArray(response.Spaces, &spaces); err != nil {
			return responseError("list Spaces", err)
		}
		for index, source := range spaces {
			if err := addLocation(state.spaces, source, "workspace", workspaceID, fmt.Sprintf("Space %d", index)); err != nil {
				return responseError("list Spaces", err)
			}
		}
	}

	for _, spaceID := range sortedLocationIDs(state.spaces) {
		for _, archived := range []bool{false, true} {
			query := url.Values{"archived": {fmt.Sprint(archived)}}
			var foldersResponse struct {
				Folders json.RawMessage `json:"folders"`
			}
			if err := c.getWithQuery(ctx, credential, "/space/"+spaceID+"/folder", query, "list Folders", &foldersResponse); err != nil {
				return err
			}
			var folders []namedSourceObject
			if err := decodeRequiredArray(foldersResponse.Folders, &folders); err != nil {
				return responseError("list Folders", err)
			}
			for index, source := range folders {
				if err := addLocation(state.folders, source, "space", spaceID, fmt.Sprintf("Folder %d in Space %s", index, spaceID)); err != nil {
					return responseError("list Folders", err)
				}
			}

			var listsResponse struct {
				Lists json.RawMessage `json:"lists"`
			}
			if err := c.getWithQuery(ctx, credential, "/space/"+spaceID+"/list", query, "list folderless Lists", &listsResponse); err != nil {
				return err
			}
			var lists []namedSourceObject
			if err := decodeRequiredArray(listsResponse.Lists, &lists); err != nil {
				return responseError("list folderless Lists", err)
			}
			for index, source := range lists {
				if err := addLocation(state.lists, source, "space", spaceID, fmt.Sprintf("folderless List %d in Space %s", index, spaceID)); err != nil {
					return responseError("list folderless Lists", err)
				}
			}
		}
	}

	for _, folderID := range sortedLocationIDs(state.folders) {
		for _, archived := range []bool{false, true} {
			query := url.Values{"archived": {fmt.Sprint(archived)}}
			var response struct {
				Lists json.RawMessage `json:"lists"`
			}
			if err := c.getWithQuery(ctx, credential, "/folder/"+folderID+"/list", query, "list Lists in Folder", &response); err != nil {
				return err
			}
			var lists []namedSourceObject
			if err := decodeRequiredArray(response.Lists, &lists); err != nil {
				return responseError("list Lists in Folder", err)
			}
			for index, source := range lists {
				if err := addLocation(state.lists, source, "folder", folderID, fmt.Sprintf("List %d in Folder %s", index, folderID)); err != nil {
					return responseError("list Lists in Folder", err)
				}
			}
		}
	}
	return nil
}

func addLocation(destination map[string]locationRecord, source namedSourceObject, parentType, parentID, label string) error {
	id, err := parseID(source.ID)
	if err != nil {
		return fmt.Errorf("%s has invalid id: %w", label, err)
	}
	if err := validatePathID(id); err != nil {
		return fmt.Errorf("%s has unsafe id: %w", label, err)
	}
	record := locationRecord{ID: id, Name: source.Name, ParentType: parentType, ParentID: parentID}
	if existing, found := destination[id]; found {
		if existing.Name != record.Name || existing.ParentType != record.ParentType || existing.ParentID != record.ParentID {
			return fmt.Errorf("source id %q was returned with conflicting hierarchy data", id)
		}
		return nil
	}
	destination[id] = record
	return nil
}

type customFieldResponse struct {
	Fields json.RawMessage `json:"fields"`
}

func (c *Client) scanCustomFields(ctx context.Context, credential, workspaceID string, state *scanState) error {
	paths := []string{"/team/" + workspaceID + "/field"}
	for _, id := range sortedLocationIDs(state.spaces) {
		paths = append(paths, "/space/"+id+"/field")
	}
	for _, id := range sortedLocationIDs(state.folders) {
		paths = append(paths, "/folder/"+id+"/field")
	}
	for _, id := range sortedLocationIDs(state.lists) {
		paths = append(paths, "/list/"+id+"/field")
	}

	for _, path := range paths {
		var response customFieldResponse
		if err := c.get(ctx, credential, path, "list Custom Fields", &response); err != nil {
			return err
		}
		var fields []namedSourceObject
		if err := decodeRequiredArray(response.Fields, &fields); err != nil {
			return responseError("list Custom Fields", err)
		}
		for index, field := range fields {
			id, err := parseID(field.ID)
			if err != nil {
				return responseError("list Custom Fields", fmt.Errorf("field %d has invalid id: %w", index, err))
			}
			if existing, found := state.customFields[id]; found && existing != field.Name {
				return responseError("list Custom Fields", fmt.Errorf("field id %q has conflicting names", id))
			}
			state.customFields[id] = field.Name
		}
	}
	return nil
}

type taskListResponse struct {
	Tasks json.RawMessage `json:"tasks"`
}

type taskListItem struct {
	ID     json.RawMessage `json:"id"`
	Parent json.RawMessage `json:"parent"`
	List   struct {
		ID json.RawMessage `json:"id"`
	} `json:"list"`
}

func (c *Client) scanTasks(ctx context.Context, credential string, state *scanState) error {
	for _, listID := range sortedLocationIDs(state.lists) {
		for _, archived := range []bool{false, true} {
			seenTraversal := make(map[string]struct{})
			terminated := false
			for page := 0; page < maxTaskPages; page++ {
				query := url.Values{
					"archived":       {fmt.Sprint(archived)},
					"include_closed": {"true"},
					"order_by":       {"id"},
					"reverse":        {"false"},
					"subtasks":       {"true"},
					"page":           {fmt.Sprint(page)},
				}
				var response taskListResponse
				if err := c.getWithQuery(ctx, credential, "/list/"+listID+"/task", query, "list Tasks", &response); err != nil {
					return err
				}
				var tasks []taskListItem
				if err := decodeRequiredArray(response.Tasks, &tasks); err != nil {
					return responseError("list Tasks", err)
				}
				if len(tasks) == 0 {
					terminated = true
					break
				}
				for index, source := range tasks {
					task, err := parseTaskListItem(source, listID)
					if err != nil {
						return responseError("list Tasks", fmt.Errorf("page %d task %d: %w", page, index, err))
					}
					if _, duplicate := seenTraversal[task.ID]; duplicate {
						return responseError("list Tasks", fmt.Errorf("duplicate task id %q across pagination", task.ID))
					}
					seenTraversal[task.ID] = struct{}{}
					if existing, found := state.tasks[task.ID]; found {
						if existing.ListID != task.ListID || existing.ParentID != task.ParentID {
							return responseError("list Tasks", fmt.Errorf("task id %q has conflicting source data", task.ID))
						}
						continue
					}
					state.tasks[task.ID] = task
				}
			}
			if !terminated {
				return responseError("list Tasks", fmt.Errorf("pagination did not terminate within %d pages for List %s", maxTaskPages, listID))
			}
		}
	}
	return nil
}

func parseTaskListItem(source taskListItem, expectedListID string) (taskRecord, error) {
	id, err := parseID(source.ID)
	if err != nil {
		return taskRecord{}, fmt.Errorf("invalid id: %w", err)
	}
	if err := validatePathID(id); err != nil {
		return taskRecord{}, fmt.Errorf("unsafe id: %w", err)
	}
	parentID, hasParent, err := parseNullableID(source.Parent)
	if err != nil {
		return taskRecord{}, fmt.Errorf("invalid parent id: %w", err)
	}
	if !hasParent {
		parentID = ""
	}
	listID, err := parseID(source.List.ID)
	if err != nil {
		return taskRecord{}, fmt.Errorf("invalid home List id: %w", err)
	}
	if listID != expectedListID {
		return taskRecord{}, fmt.Errorf("home List id %q does not match traversed List %q", listID, expectedListID)
	}
	return taskRecord{ID: id, ListID: listID, ParentID: parentID}, nil
}

type taskDetailResponse struct {
	ID           json.RawMessage `json:"id"`
	Parent       json.RawMessage `json:"parent"`
	Attachments  json.RawMessage `json:"attachments"`
	Dependencies json.RawMessage `json:"dependencies"`
	LinkedTasks  json.RawMessage `json:"linked_tasks"`
	List         struct {
		ID json.RawMessage `json:"id"`
	} `json:"list"`
}

type attachmentResponse struct {
	ID json.RawMessage `json:"id"`
}

type dependencyResponse struct {
	TaskID    json.RawMessage `json:"task_id"`
	DependsOn json.RawMessage `json:"depends_on"`
}

type linkedTaskResponse struct {
	TaskID json.RawMessage `json:"task_id"`
	LinkID json.RawMessage `json:"link_id"`
}

func (c *Client) scanTaskDetails(ctx context.Context, credential string, state *scanState) error {
	for _, taskID := range sortedTaskIDs(state.tasks) {
		expected := state.tasks[taskID]
		var detail taskDetailResponse
		if err := c.get(ctx, credential, "/task/"+taskID, "get Task inventory details", &detail); err != nil {
			return err
		}
		parsed, err := parseTaskListItem(taskListItem{ID: detail.ID, Parent: detail.Parent, List: detail.List}, expected.ListID)
		if err != nil {
			return responseError("get Task inventory details", err)
		}
		if parsed.ID != expected.ID || parsed.ParentID != expected.ParentID {
			return responseError("get Task inventory details", fmt.Errorf("Task %q detail conflicts with paginated inventory", taskID))
		}

		var attachments []attachmentResponse
		if err := decodeRequiredArray(detail.Attachments, &attachments); err != nil {
			return responseError("get Task inventory details", fmt.Errorf("Task %s attachments: %w", taskID, err))
		}
		for index, attachment := range attachments {
			id, err := parseID(attachment.ID)
			if err != nil {
				return responseError("get Task inventory details", fmt.Errorf("Task %s attachment %d has invalid id: %w", taskID, index, err))
			}
			if owner, duplicate := state.attachments[id]; duplicate && owner != taskID {
				return responseError("get Task inventory details", fmt.Errorf("attachment id %q appears on multiple Tasks", id))
			}
			state.attachments[id] = taskID
		}

		var dependencies []dependencyResponse
		if err := decodeRequiredArray(detail.Dependencies, &dependencies); err != nil {
			return responseError("get Task inventory details", fmt.Errorf("Task %s dependencies: %w", taskID, err))
		}
		for index, dependency := range dependencies {
			task, err := parseID(dependency.TaskID)
			if err != nil {
				return responseError("get Task inventory details", fmt.Errorf("Task %s dependency %d task_id: %w", taskID, index, err))
			}
			dependsOn, err := parseID(dependency.DependsOn)
			if err != nil {
				return responseError("get Task inventory details", fmt.Errorf("Task %s dependency %d depends_on: %w", taskID, index, err))
			}
			if taskID != task && taskID != dependsOn {
				return responseError("get Task inventory details", fmt.Errorf("Task %s dependency %d does not reference the containing Task", taskID, index))
			}
			id := task + "->" + dependsOn
			state.relationships["task_dependency:"+id] = relationshipRecord{
				ID: id, Kind: "task_dependency", FromID: task, ToID: dependsOn, Composite: true,
			}
		}

		var links []linkedTaskResponse
		if err := decodeRequiredArray(detail.LinkedTasks, &links); err != nil {
			return responseError("get Task inventory details", fmt.Errorf("Task %s linked_tasks: %w", taskID, err))
		}
		for index, link := range links {
			leftEndpoint, err := parseID(link.TaskID)
			if err != nil {
				return responseError("get Task inventory details", fmt.Errorf("Task %s link %d task_id: %w", taskID, index, err))
			}
			rightEndpoint, err := parseID(link.LinkID)
			if err != nil {
				return responseError("get Task inventory details", fmt.Errorf("Task %s link %d link_id: %w", taskID, index, err))
			}
			if taskID != leftEndpoint && taskID != rightEndpoint {
				return responseError("get Task inventory details", fmt.Errorf("Task %s link %d does not reference the containing Task", taskID, index))
			}
			left, right := leftEndpoint, rightEndpoint
			if right < left {
				left, right = right, left
			}
			id := left + "<->" + right
			record := relationshipRecord{ID: id, Kind: "task_link", FromID: left, ToID: right, Composite: true}
			key := "task_link:" + id
			if existing, duplicate := state.relationships[key]; duplicate && existing != record {
				return responseError("get Task inventory details", fmt.Errorf("task link %q has conflicting source data", id))
			}
			state.relationships[key] = record
		}

		if err := c.scanTaskComments(ctx, credential, taskID, state); err != nil {
			return err
		}
	}
	return nil
}

type commentsResponse struct {
	Comments json.RawMessage `json:"comments"`
}

type commentResponse struct {
	ID         json.RawMessage `json:"id"`
	Date       json.RawMessage `json:"date"`
	ReplyCount json.RawMessage `json:"reply_count"`
}

type rootComment struct {
	ID         string
	ReplyCount int
}

func (c *Client) scanTaskComments(ctx context.Context, credential, taskID string, state *scanState) error {
	query := make(url.Values)
	rootComments := make([]rootComment, 0)
	terminated := false
	for page := 0; page < maxCommentPages; page++ {
		var response commentsResponse
		if err := c.getWithQuery(ctx, credential, "/task/"+taskID+"/comment", query, "list Task comments", &response); err != nil {
			return err
		}
		var comments []commentResponse
		if err := decodeRequiredArray(response.Comments, &comments); err != nil {
			return responseError("list Task comments", err)
		}
		if len(comments) == 0 {
			terminated = true
			break
		}
		for index, comment := range comments {
			id, err := parseID(comment.ID)
			if err != nil {
				return responseError("list Task comments", fmt.Errorf("page %d comment %d has invalid id: %w", page, index, err))
			}
			if _, duplicate := state.comments[id]; duplicate {
				return responseError("list Task comments", fmt.Errorf("duplicate comment id %q across pagination", id))
			}
			replyCount, err := parseCount(comment.ReplyCount)
			if err != nil {
				return responseError("list Task comments", fmt.Errorf("comment %q has invalid reply_count: %w", id, err))
			}
			state.comments[id] = commentRecord{ID: id, ParentType: "task", ParentID: taskID}
			rootComments = append(rootComments, rootComment{ID: id, ReplyCount: replyCount})
		}
		last := comments[len(comments)-1]
		lastID, err := parseID(last.ID)
		if err != nil {
			return responseError("list Task comments", fmt.Errorf("invalid pagination comment id: %w", err))
		}
		lastDate, err := parseID(last.Date)
		if err != nil {
			return responseError("list Task comments", fmt.Errorf("invalid pagination comment date: %w", err))
		}
		query = url.Values{"start": {lastDate}, "start_id": {lastID}}
	}
	if !terminated {
		return responseError("list Task comments", fmt.Errorf("pagination did not terminate within %d pages for Task %s", maxCommentPages, taskID))
	}

	for _, root := range rootComments {
		if root.ReplyCount == 0 {
			continue
		}
		if err := validatePathID(root.ID); err != nil {
			return responseError("list threaded comment replies", err)
		}
		var response commentsResponse
		if err := c.get(ctx, credential, "/comment/"+root.ID+"/reply", "list threaded comment replies", &response); err != nil {
			return err
		}
		var replies []commentResponse
		if err := decodeRequiredArray(response.Comments, &replies); err != nil {
			return responseError("list threaded comment replies", err)
		}
		if len(replies) != root.ReplyCount {
			return responseError("list threaded comment replies", fmt.Errorf("comment %q declared %d replies but API returned %d", root.ID, root.ReplyCount, len(replies)))
		}
		for index, reply := range replies {
			id, err := parseID(reply.ID)
			if err != nil {
				return responseError("list threaded comment replies", fmt.Errorf("reply %d has invalid id: %w", index, err))
			}
			if _, duplicate := state.comments[id]; duplicate {
				return responseError("list threaded comment replies", fmt.Errorf("duplicate comment id %q", id))
			}
			state.comments[id] = commentRecord{ID: id, ParentType: "comment", ParentID: root.ID}
		}
	}
	return nil
}

func sourceObjects(workspace connector.Workspace, state *scanState) []connector.SourceObject {
	objects := []connector.SourceObject{{Type: "workspace", ID: workspace.ID}}
	for _, record := range state.spaces {
		objects = append(objects, connector.SourceObject{Type: "space", ID: record.ID, ParentType: record.ParentType, ParentID: record.ParentID})
	}
	for _, record := range state.folders {
		objects = append(objects, connector.SourceObject{Type: "folder", ID: record.ID, ParentType: record.ParentType, ParentID: record.ParentID})
	}
	for _, record := range state.lists {
		objects = append(objects, connector.SourceObject{Type: "list", ID: record.ID, ParentType: record.ParentType, ParentID: record.ParentID})
	}
	for _, record := range state.tasks {
		objectType, parentType, parentID := "task", "list", record.ListID
		if record.ParentID != "" {
			objectType, parentType, parentID = "subtask", taskSourceType(state, record.ParentID), record.ParentID
		}
		objects = append(objects, connector.SourceObject{Type: objectType, ID: record.ID, ParentType: parentType, ParentID: parentID})
	}
	for _, record := range state.comments {
		parentType := record.ParentType
		if parentType == "task" {
			parentType = taskSourceType(state, record.ParentID)
		}
		objects = append(objects, connector.SourceObject{Type: "comment", ID: record.ID, ParentType: parentType, ParentID: record.ParentID})
	}
	for id, taskID := range state.attachments {
		objects = append(objects, connector.SourceObject{Type: "attachment", ID: id, ParentType: taskSourceType(state, taskID), ParentID: taskID})
	}
	for id := range state.customFields {
		objects = append(objects, connector.SourceObject{Type: "custom_field", ID: id})
	}
	for _, record := range state.relationships {
		parentID := record.FromID
		if _, found := state.tasks[parentID]; !found {
			parentID = record.ToID
		}
		objects = append(objects, connector.SourceObject{
			Type: "relationship." + record.Kind, ID: record.ID,
			ParentType: taskSourceType(state, parentID), ParentID: parentID, Composite: record.Composite,
		})
	}
	sort.Slice(objects, func(left, right int) bool {
		a, b := objects[left], objects[right]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if a.ParentType != b.ParentType {
			return a.ParentType < b.ParentType
		}
		return a.ParentID < b.ParentID
	})
	return objects
}

func taskSourceType(state *scanState, taskID string) string {
	if task, found := state.tasks[taskID]; found && task.ParentID != "" {
		return "subtask"
	}
	return "task"
}

func parseCount(raw json.RawMessage) (int, error) {
	value, err := parseID(raw)
	if err != nil {
		return 0, err
	}
	count, err := strconv.ParseUint(value, 10, 31)
	if err != nil {
		return 0, errors.New("value is not a non-negative integer count")
	}
	return int(count), nil
}

func decodeRequiredArray(raw json.RawMessage, destination any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return errors.New("missing required array")
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("invalid array: %w", err)
	}
	return nil
}

func parseNullableID(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, errors.New("missing value")
	}
	if string(raw) == "null" {
		return "", false, nil
	}
	id, err := parseID(raw)
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func validatePathID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("empty value")
	}
	if strings.ContainsAny(id, "/?#\r\n") {
		return errors.New("value cannot be used as a path segment")
	}
	return nil
}

func sortedLocationIDs(records map[string]locationRecord) []string {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sortedTaskIDs(records map[string]taskRecord) []string {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func m2Capabilities() []connector.Capability {
	return []connector.Capability{
		{Name: "Tasks/subtasks", State: connector.CapabilitySupported, Note: "active, closed, and archived home-List tasks"},
		{Name: "Task comments", State: connector.CapabilitySupported, Note: "paginated comments and threaded replies"},
		{Name: "Task attachments", State: connector.CapabilitySupported, Note: "metadata inventory; downloads are outside M2"},
		{Name: "Custom fields", State: connector.CapabilitySupported, Note: "accessible definitions across hierarchy levels"},
		{Name: "Task relationships", State: connector.CapabilityPartial, Note: "dependencies and task links only"},
		{Name: "Docs", State: connector.CapabilityPartial, Note: "official API exists; Docs are outside M2 inventory"},
		{Name: "Whiteboards", State: connector.CapabilityUnknown, Note: "not established by M2"},
		{Name: "Automations", State: connector.CapabilityUnknown, Note: "not established by M2"},
	}
}
