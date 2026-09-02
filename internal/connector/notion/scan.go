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

package notion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"alih/internal/connector"
)

// source is everything one traversal established, in Notion's own words. Core
// never sees this type.
type source struct {
	databases   []database
	dataSources []dataSource
	pages       []page
	blocks      []block
	// limitations are honest statements about what this traversal could not
	// establish. They travel into the archive rather than being dropped.
	limitations []string
	// truncated records that a data source hit Notion's query result ceiling,
	// which makes the run incomplete rather than complete-with-fewer-rows.
	truncated bool
	// rawPathFor resolves an object's path inside the sealed raw tree. It is
	// set when a source is rebuilt from evidence and nil during a live run.
	rawPathFor func(objectType, id string) string
	// workspaceID is the workspace this traversal covered.
	workspaceID string
}

type database struct {
	ID    string
	Title string
	Raw   json.RawMessage
}

type dataSource struct {
	ID         string
	DatabaseID string
	Name       string
	Properties map[string]propertySchema
	Raw        json.RawMessage
}

type propertySchema struct {
	ID   string
	Name string
	Type string
	Raw  json.RawMessage
}

type page struct {
	ID           string
	DataSourceID string
	Title        string
	CreatedTime  string
	EditedTime   string
	Archived     bool
	Properties   map[string]json.RawMessage
	Raw          json.RawMessage
	rawPath      string
}

// block is one node of a page's content tree. Depth is whatever Notion has;
// this adapter does not impose a semantic maximum.
type block struct {
	ID       string
	PageID   string
	ParentID string
	Type     string
	Position int
	Text     string
	Raw      json.RawMessage
	rawPath  string
}

// Authenticate validates the integration token and reports the workspace it is
// installed in.
func (c *Client) Authenticate(ctx context.Context, credential string) (connector.Authentication, error) {
	payload, err := c.get(ctx, credential, "/users/me", nil, "get authorized bot")
	if err != nil {
		return connector.Authentication{}, err
	}
	var envelope struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Bot  struct {
			WorkspaceID   string `json:"workspace_id"`
			WorkspaceName string `json:"workspace_name"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return connector.Authentication{}, &Error{Kind: ErrorResponse, Operation: "get authorized bot", Cause: err}
	}
	if strings.TrimSpace(envelope.Bot.WorkspaceID) == "" {
		return connector.Authentication{}, &Error{Kind: ErrorResponse, Operation: "get authorized bot",
			Cause: errors.New("Notion returned no workspace identity for this token")}
	}

	assessment, err := connector.HealthyAssessment(c.Name(), connector.HealthBasisAuthentication,
		nowUTC(), connector.AuthenticationAuthenticated, nil)
	if err != nil {
		return connector.Authentication{}, &Error{Kind: ErrorResponse, Operation: "get authorized bot", Cause: err}
	}
	return connector.Authentication{
		Identity:   connector.Identity{ID: envelope.ID, Name: envelope.Name},
		Workspaces: []connector.Workspace{{ID: envelope.Bot.WorkspaceID, Name: envelope.Bot.WorkspaceName}},
		Assessment: assessment,
	}, nil
}

// Scan inventories what the integration can reach, without emitting evidence.
func (c *Client) Scan(ctx context.Context, credential string, workspace connector.Workspace) (connector.ScanResult, error) {
	result, _, err := c.traverse(ctx, credential, workspace)
	return result, err
}

// Extract performs the same traversal while recording every response verbatim.
func (c *Client) Extract(ctx context.Context, credential string, workspace connector.Workspace, sink connector.RawEvidenceSink) (connector.ExtractionResult, error) {
	if sink == nil {
		return connector.ExtractionResult{}, &Error{Kind: ErrorResponse, Operation: "start raw extraction",
			Cause: errors.New("raw evidence sink is required")}
	}
	recording := *c
	recording.evidence = sink
	result, state, err := recording.traverse(ctx, credential, workspace)
	if err != nil {
		return connector.ExtractionResult{}, err
	}
	capabilities, err := c.observedCapabilities(true)
	if err != nil {
		return connector.ExtractionResult{}, &Error{Kind: ErrorResponse, Operation: "describe extraction capabilities", Cause: err}
	}
	result.Capabilities = capabilities
	return connector.ExtractionResult{ScanResult: result, SourceObjects: sourceObjects(state)}, nil
}

// traverse walks everything the integration can see and fails closed. A partial
// traversal is an error, never a smaller inventory.
func (c *Client) traverse(ctx context.Context, credential string, workspace connector.Workspace) (connector.ScanResult, *source, error) {
	state := &source{workspaceID: workspace.ID}

	// Notion's search returns only what has been explicitly connected to this
	// integration, and offers no way to enumerate the rest. That is a real
	// limit on what any Notion backup can claim, so it is recorded rather than
	// glossed over.
	state.limitations = append(state.limitations,
		"Notion only exposes pages and data sources explicitly connected to this integration; content that was never shared is invisible to the API and is not part of this archive.")

	if err := c.discoverDataSources(ctx, credential, state); err != nil {
		return connector.ScanResult{}, nil, err
	}
	for index := range state.dataSources {
		if err := c.queryDataSource(ctx, credential, state, &state.dataSources[index]); err != nil {
			return connector.ScanResult{}, nil, err
		}
	}
	for index := range state.pages {
		if err := c.readBlockTree(ctx, credential, state, state.pages[index]); err != nil {
			return connector.ScanResult{}, nil, err
		}
	}

	capabilities, err := c.observedCapabilities(false)
	if err != nil {
		return connector.ScanResult{}, nil, &Error{Kind: ErrorResponse, Operation: "describe scan capabilities", Cause: err}
	}
	assessment, err := connector.HealthyAssessment(c.Name(), connector.HealthBasisScan,
		nowUTC(), connector.AuthenticationAuthenticated, capabilities)
	if err != nil {
		return connector.ScanResult{}, nil, &Error{Kind: ErrorResponse, Operation: "describe scan health", Cause: err}
	}

	return connector.ScanResult{
		Workspace:               workspace,
		Inventory:               state.inventory(),
		CapabilitySchemaVersion: connector.CapabilitySchemaVersion,
		Capabilities:            capabilities,
		Assessment:              assessment,
	}, state, nil
}

// discoverDataSources finds the data sources the integration can reach, then
// reads each one's parent database and property schema.
//
// A database is a container that holds one or more data sources; the rows live
// in the data source, not the database. Search returns data sources rather than
// databases, so the parent is resolved from each one.
func (c *Client) discoverDataSources(ctx context.Context, credential string, state *source) error {
	seenDatabase := map[string]bool{}

	err := c.paginate(ctx, "search connected content", func(cursor string) ([]byte, error) {
		body := map[string]any{
			"page_size": maxPageSize,
			"filter":    map[string]string{"property": "object", "value": "data_source"},
		}
		if cursor != "" {
			body["start_cursor"] = cursor
		}
		return c.post(ctx, credential, "/search", body, "search connected content")
	}, func(result json.RawMessage) error {
		var envelope struct {
			ID     string `json:"id"`
			Object string `json:"object"`
		}
		if err := json.Unmarshal(result, &envelope); err != nil {
			return &Error{Kind: ErrorResponse, Operation: "search connected content", Cause: err}
		}
		if envelope.Object != "data_source" || envelope.ID == "" {
			return nil
		}
		return c.readDataSource(ctx, credential, state, envelope.ID, seenDatabase)
	})
	return err
}

func (c *Client) readDataSource(ctx context.Context, credential string, state *source, id string, seenDatabase map[string]bool) error {
	payload, err := c.get(ctx, credential, "/data_sources/"+id, nil, "get data source")
	if err != nil {
		return err
	}
	var envelope struct {
		ID     string `json:"id"`
		Title  []richText
		Name   string `json:"name"`
		Parent struct {
			Type       string `json:"type"`
			DatabaseID string `json:"database_id"`
		} `json:"parent"`
		Properties map[string]struct {
			ID   string          `json:"id"`
			Name string          `json:"name"`
			Type string          `json:"type"`
			Raw  json.RawMessage `json:"-"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return &Error{Kind: ErrorResponse, Operation: "get data source", Cause: err}
	}

	// Keep each property's own JSON so its provider-defined shape survives.
	var rawProperties struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	_ = json.Unmarshal(payload, &rawProperties)

	properties := make(map[string]propertySchema, len(envelope.Properties))
	for name, property := range envelope.Properties {
		properties[name] = propertySchema{
			ID: property.ID, Name: name, Type: property.Type, Raw: rawProperties.Properties[name],
		}
	}

	databaseID := envelope.Parent.DatabaseID
	if databaseID != "" && !seenDatabase[databaseID] {
		seenDatabase[databaseID] = true
		if err := c.readDatabase(ctx, credential, state, databaseID); err != nil {
			return err
		}
	}
	state.dataSources = append(state.dataSources, dataSource{
		ID: envelope.ID, DatabaseID: databaseID, Name: firstNonEmpty(envelope.Name, "Untitled"),
		Properties: properties, Raw: payload,
	})
	return nil
}

func (c *Client) readDatabase(ctx context.Context, credential string, state *source, id string) error {
	payload, err := c.get(ctx, credential, "/databases/"+id, nil, "get database")
	if err != nil {
		return err
	}
	var envelope struct {
		ID    string     `json:"id"`
		Title []richText `json:"title"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return &Error{Kind: ErrorResponse, Operation: "get database", Cause: err}
	}
	state.databases = append(state.databases, database{
		ID: envelope.ID, Title: firstNonEmpty(plainText(envelope.Title), "Untitled"), Raw: payload,
	})
	return nil
}

// queryDataSource reads every row of one data source.
//
// Notion caps a query at 10,000 results and says so in request_status rather
// than by failing. Treating that as the end of the data would silently produce
// a short archive that looks complete, so it is recorded as truncation.
func (c *Client) queryDataSource(ctx context.Context, credential string, state *source, source *dataSource) error {
	return c.paginateWithStatus(ctx, "query data source", func(cursor string) ([]byte, error) {
		body := map[string]any{"page_size": maxPageSize}
		if cursor != "" {
			body["start_cursor"] = cursor
		}
		return c.post(ctx, credential, "/data_sources/"+source.ID+"/query", body, "query data source")
	}, func(result json.RawMessage) error {
		var envelope struct {
			ID             string                     `json:"id"`
			Object         string                     `json:"object"`
			Archived       bool                       `json:"archived"`
			InTrash        bool                       `json:"in_trash"`
			CreatedTime    string                     `json:"created_time"`
			LastEditedTime string                     `json:"last_edited_time"`
			Properties     map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(result, &envelope); err != nil {
			return &Error{Kind: ErrorResponse, Operation: "query data source", Cause: err}
		}
		if envelope.Object != "page" || envelope.ID == "" {
			return nil
		}
		state.pages = append(state.pages, page{
			ID: envelope.ID, DataSourceID: source.ID, Title: pageTitle(envelope.Properties),
			CreatedTime: envelope.CreatedTime, EditedTime: envelope.LastEditedTime,
			Archived: envelope.Archived || envelope.InTrash, Properties: envelope.Properties, Raw: result,
		})
		return nil
	}, func(incomplete bool, reason string) {
		if !incomplete {
			return
		}
		state.truncated = true
		state.limitations = append(state.limitations, fmt.Sprintf(
			"Data source %s returned more rows than one Notion query can paginate (%s); this archive does not contain all of its rows.",
			source.ID, firstNonEmpty(reason, "query result limit reached")))
	})
}

// readBlockTree walks a page's content to exhaustion.
//
// Notion returns one level at a time and marks a node with has_children, so
// depth is a property of the source rather than of this code. There is
// deliberately no semantic depth cap: the only bound is the request guard in
// the client, and reaching it fails the run instead of truncating it.
func (c *Client) readBlockTree(ctx context.Context, credential string, state *source, owner page) error {
	type frame struct {
		id    string
		depth int
	}
	queue := []frame{{id: owner.ID}}
	visited := map[string]bool{owner.ID: true}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		position := 0

		err := c.paginate(ctx, "get block children", func(cursor string) ([]byte, error) {
			query := map[string][]string{"page_size": {fmt.Sprint(maxPageSize)}}
			if cursor != "" {
				query["start_cursor"] = []string{cursor}
			}
			return c.get(ctx, credential, "/blocks/"+current.id+"/children", query, "get block children")
		}, func(result json.RawMessage) error {
			var envelope struct {
				ID          string `json:"id"`
				Object      string `json:"object"`
				Type        string `json:"type"`
				HasChildren bool   `json:"has_children"`
			}
			if err := json.Unmarshal(result, &envelope); err != nil {
				return &Error{Kind: ErrorResponse, Operation: "get block children", Cause: err}
			}
			if envelope.Object != "block" || envelope.ID == "" {
				return nil
			}
			parent := current.id
			if parent == owner.ID {
				parent = ""
			}
			state.blocks = append(state.blocks, block{
				ID: envelope.ID, PageID: owner.ID, ParentID: parent, Type: envelope.Type,
				Position: position, Text: blockText(result, envelope.Type), Raw: result,
			})
			position++

			if envelope.HasChildren {
				// A malformed response could name a block already seen. Following
				// it would loop forever, so the cycle is refused rather than walked.
				if visited[envelope.ID] {
					return &Error{Kind: ErrorResponse, Operation: "get block children",
						Cause: fmt.Errorf("block %s appears as its own descendant", envelope.ID)}
				}
				visited[envelope.ID] = true
				queue = append(queue, frame{id: envelope.ID, depth: current.depth + 1})
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// paginate walks a Notion cursor-paginated list to exhaustion.
//
// next_cursor is treated as opaque: it is echoed back, never parsed. A page
// that claims has_more without supplying a cursor would loop forever, so it is
// refused as a malformed response.
func (c *Client) paginate(ctx context.Context, operation string, fetch func(cursor string) ([]byte, error), each func(json.RawMessage) error) error {
	return c.paginateWithStatus(ctx, operation, fetch, each, nil)
}

func (c *Client) paginateWithStatus(ctx context.Context, operation string,
	fetch func(cursor string) ([]byte, error), each func(json.RawMessage) error,
	status func(incomplete bool, reason string)) error {

	cursor := ""
	seenCursors := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return &Error{Kind: ErrorNetwork, Operation: operation, Cause: err}
		}
		payload, err := fetch(cursor)
		if err != nil {
			return err
		}
		var envelope struct {
			Results       []json.RawMessage `json:"results"`
			HasMore       bool              `json:"has_more"`
			NextCursor    *string           `json:"next_cursor"`
			RequestStatus *struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"request_status"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return &Error{Kind: ErrorResponse, Operation: operation, Cause: err}
		}
		for _, result := range envelope.Results {
			if err := each(result); err != nil {
				return err
			}
		}
		if status != nil && envelope.RequestStatus != nil {
			status(envelope.RequestStatus.Type == "incomplete", envelope.RequestStatus.Reason)
		}
		if !envelope.HasMore {
			return nil
		}
		if envelope.NextCursor == nil || strings.TrimSpace(*envelope.NextCursor) == "" {
			return &Error{Kind: ErrorResponse, Operation: operation,
				Cause: errors.New("Notion reported more results but supplied no cursor")}
		}
		next := *envelope.NextCursor
		if seenCursors[next] {
			return &Error{Kind: ErrorResponse, Operation: operation,
				Cause: fmt.Errorf("Notion repeated pagination cursor %q", next)}
		}
		seenCursors[next] = true
		cursor = next
	}
}
