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
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"alih/internal/snapshot"
)

// rebuild reconstructs Notion's own shape from sealed raw evidence.
//
// Normalization reads the archived bytes rather than any in-memory state, so an
// archive can be re-normalized years later from evidence alone. The source
// object index says what exists; the responses say what it looked like.
func rebuild(evidence snapshot.Evidence) (*source, error) {
	state := &source{workspaceID: evidence.Workspace.ID}

	// Index every recorded response by the object it describes.
	type recorded struct {
		body    json.RawMessage
		rawPath string
	}
	byPath := make(map[string]recorded, len(evidence.Responses))
	var listResponses []recorded

	for _, response := range evidence.Responses {
		entry := recorded{body: response.Body, rawPath: archiveRawPath(response.RawPath)}
		switch response.Operation {
		case "get database", "get data source":
			byPath[strings.TrimPrefix(response.Path, "/")] = entry
		case "search connected content", "query data source", "get block children":
			listResponses = append(listResponses, entry)
		}
	}

	pathFor := func(objectType, id string) string {
		switch objectType {
		case kindDatabase:
			if entry, ok := byPath["databases/"+id]; ok {
				return entry.rawPath
			}
		case kindDataSource:
			if entry, ok := byPath["data_sources/"+id]; ok {
				return entry.rawPath
			}
		}
		return ""
	}

	// Databases and data sources come from their own single-object responses.
	for _, object := range evidence.SourceObjects {
		switch object.Type {
		case kindDatabase:
			entry, ok := byPath["databases/"+object.ID]
			if !ok {
				return nil, fmt.Errorf("raw evidence has no response for database %s", object.ID)
			}
			var envelope struct {
				Title []richText `json:"title"`
			}
			_ = json.Unmarshal(entry.body, &envelope)
			state.databases = append(state.databases, database{
				ID: object.ID, Title: firstNonEmpty(plainText(envelope.Title), "Untitled"), Raw: entry.body,
			})
		case kindDataSource:
			entry, ok := byPath["data_sources/"+object.ID]
			if !ok {
				return nil, fmt.Errorf("raw evidence has no response for data source %s", object.ID)
			}
			var envelope struct {
				Name       string                     `json:"name"`
				Properties map[string]json.RawMessage `json:"properties"`
			}
			_ = json.Unmarshal(entry.body, &envelope)
			properties := make(map[string]propertySchema, len(envelope.Properties))
			for name, raw := range envelope.Properties {
				var shape struct {
					ID   string `json:"id"`
					Type string `json:"type"`
				}
				_ = json.Unmarshal(raw, &shape)
				properties[name] = propertySchema{ID: shape.ID, Name: name, Type: shape.Type, Raw: raw}
			}
			state.dataSources = append(state.dataSources, dataSource{
				ID: object.ID, DatabaseID: object.ParentID,
				Name: firstNonEmpty(envelope.Name, "Untitled"), Properties: properties, Raw: entry.body,
			})
		}
	}

	// Pages and blocks arrive inside list responses. Index them by id so the
	// source object list decides what exists and in what order.
	pages := map[string]recorded{}
	blocks := map[string]recorded{}
	for _, entry := range listResponses {
		var envelope struct {
			Results []json.RawMessage `json:"results"`
		}
		if err := json.Unmarshal(entry.body, &envelope); err != nil {
			continue
		}
		for _, result := range envelope.Results {
			var shape struct {
				ID     string `json:"id"`
				Object string `json:"object"`
			}
			if err := json.Unmarshal(result, &shape); err != nil || shape.ID == "" {
				continue
			}
			item := recorded{body: result, rawPath: entry.rawPath}
			switch shape.Object {
			case "page":
				pages[shape.ID] = item
			case "block":
				blocks[shape.ID] = item
			}
		}
	}

	for _, object := range evidence.SourceObjects {
		switch object.Type {
		case kindPage:
			entry, ok := pages[object.ID]
			if !ok {
				return nil, fmt.Errorf("raw evidence has no response for page %s", object.ID)
			}
			var envelope struct {
				Archived       bool                       `json:"archived"`
				InTrash        bool                       `json:"in_trash"`
				CreatedTime    string                     `json:"created_time"`
				LastEditedTime string                     `json:"last_edited_time"`
				Properties     map[string]json.RawMessage `json:"properties"`
			}
			_ = json.Unmarshal(entry.body, &envelope)
			state.pages = append(state.pages, page{
				ID: object.ID, DataSourceID: object.ParentID, Title: pageTitle(envelope.Properties),
				CreatedTime: envelope.CreatedTime, EditedTime: envelope.LastEditedTime,
				Archived: envelope.Archived || envelope.InTrash, Properties: envelope.Properties,
				Raw: entry.body, rawPath: entry.rawPath,
			})
		case kindBlock:
			entry, ok := blocks[object.ID]
			if !ok {
				return nil, fmt.Errorf("raw evidence has no response for block %s", object.ID)
			}
			var envelope struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(entry.body, &envelope)
			parentID, pageID := "", object.ParentID
			if object.ParentType == kindBlock {
				parentID = object.ParentID
				pageID = ""
			}
			state.blocks = append(state.blocks, block{
				ID: object.ID, PageID: pageID, ParentID: parentID, Type: envelope.Type,
				Text: blockText(entry.body, envelope.Type), Raw: entry.body, rawPath: entry.rawPath,
			})
		}
	}

	// A block records its parent block, not its page. Resolve the owning page
	// by walking up, so every block still belongs to a collection.
	owner := map[string]string{}
	for _, p := range state.pages {
		owner[p.ID] = p.ID
	}
	for changed := true; changed; {
		changed = false
		for index := range state.blocks {
			b := &state.blocks[index]
			if b.PageID != "" {
				owner[b.ID] = b.PageID
				continue
			}
			if resolved, ok := owner[b.ParentID]; ok {
				b.PageID = resolved
				owner[b.ID] = resolved
				changed = true
			}
		}
	}
	for _, b := range state.blocks {
		if b.PageID == "" {
			return nil, fmt.Errorf("block %s could not be traced back to a page", b.ID)
		}
	}

	state.rawPathFor = pathFor
	return state, nil
}

func archiveRawPath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Join("raw", filepath.FromSlash(path)))
}

// pathFor resolves an object's sealed raw-evidence path, or the empty string
// when the object had no single-object response of its own.
func (s *source) pathFor(objectType, id string) string {
	if s.rawPathFor == nil {
		return ""
	}
	return s.rawPathFor(objectType, id)
}
