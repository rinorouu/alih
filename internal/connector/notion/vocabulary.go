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
	"strings"
	"time"

	"alih/internal/connector"
)

// Notion's own words for its own objects. Core stores these without
// interpreting them, and never requires another provider's nouns.
const (
	kindDatabase   = "database"
	kindDataSource = "data_source"
	kindPage       = "page"
	kindBlock      = "block"
)

func nowUTC() time.Time { return time.Now().UTC() }

// inventory reports counts in Alih's neutral totals while carrying Notion's own
// kind names beside them.
//
// The shapes line up honestly rather than by force: a database contains data
// sources, a data source contains pages, and a page's blocks are records nested
// under it. Nothing is renamed to look like another provider.
func (s *source) inventory() connector.Inventory {
	// Every block is a nested record. In the portable model a block's parent is
	// either the page it belongs to or the block above it, so a top-level block
	// is nested under its page even though Notion gives it no parent block.
	// Counting only blocks with a parent *block* undercounts, and Core's
	// structural count from the parent pointer is the one that decides.
	nested := len(s.blocks)
	return connector.Inventory{
		Containers:    len(s.databases),
		Collections:   len(s.dataSources),
		Records:       len(s.pages) + len(s.blocks),
		NestedRecords: nested,
		CustomFields:  s.propertyCount(),
		ContainerKinds: map[string]int{
			kindDatabase: len(s.databases),
		},
		RecordKinds: map[string]int{
			kindPage:  len(s.pages),
			kindBlock: len(s.blocks),
		},
	}
}

func (s *source) propertyCount() int {
	total := 0
	for _, ds := range s.dataSources {
		total += len(ds.Properties)
	}
	return total
}

// sourceObjects is the stable index into raw evidence. Every object Alih
// archives must appear here exactly once, under Notion's own type names.
func sourceObjects(s *source) []connector.SourceObject {
	objects := make([]connector.SourceObject, 0,
		len(s.databases)+len(s.dataSources)+len(s.pages)+len(s.blocks)+1)

	// The workspace and each data source property are archived too, so they
	// must be indexed. Anything the archive contains but the index omits is a
	// reconciliation failure, which is exactly how this was found.
	objects = append(objects, connector.SourceObject{Type: "workspace", ID: s.workspaceID})
	for _, ds := range s.dataSources {
		for _, property := range ds.Properties {
			objects = append(objects, connector.SourceObject{
				Type: "property", ID: property.ID, ParentType: kindDataSource, ParentID: ds.ID,
			})
		}
	}
	for _, db := range s.databases {
		objects = append(objects, connector.SourceObject{Type: kindDatabase, ID: db.ID})
	}
	for _, ds := range s.dataSources {
		object := connector.SourceObject{Type: kindDataSource, ID: ds.ID}
		if ds.DatabaseID != "" {
			object.ParentType, object.ParentID = kindDatabase, ds.DatabaseID
		}
		objects = append(objects, object)
	}
	for _, p := range s.pages {
		objects = append(objects, connector.SourceObject{
			Type: kindPage, ID: p.ID, ParentType: kindDataSource, ParentID: p.DataSourceID,
		})
	}
	for _, b := range s.blocks {
		object := connector.SourceObject{Type: kindBlock, ID: b.ID, ParentType: kindPage, ParentID: b.PageID}
		if b.ParentID != "" {
			object.ParentType, object.ParentID = kindBlock, b.ParentID
		}
		objects = append(objects, object)
	}
	return objects
}

// richText is Notion's inline text shape. Only its plain rendering is used for
// titles; the full structure survives verbatim in raw evidence.
type richText struct {
	PlainText string `json:"plain_text"`
}

func plainText(parts []richText) string {
	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(part.PlainText)
	}
	return strings.TrimSpace(builder.String())
}

// pageTitle finds the title property of a row without assuming its name, since
// a Notion user may call the title column anything.
func pageTitle(properties map[string]json.RawMessage) string {
	for _, raw := range properties {
		var envelope struct {
			Type  string     `json:"type"`
			Title []richText `json:"title"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Type != "title" {
			continue
		}
		if title := plainText(envelope.Title); title != "" {
			return title
		}
	}
	return ""
}

// blockText renders the plain text a block carries, if it carries any. Notion
// nests it under the block's own type key, so the type selects the field.
func blockText(raw json.RawMessage, blockType string) string {
	if blockType == "" {
		return ""
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	payload, present := envelope[blockType]
	if !present {
		return ""
	}
	var body struct {
		RichText []richText `json:"rich_text"`
		Caption  []richText `json:"caption"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return ""
	}
	if text := plainText(body.RichText); text != "" {
		return text
	}
	return plainText(body.Caption)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
