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
	"sort"
	"strings"

	"alih/internal/model"
	"alih/internal/snapshot"
)

// Normalizer turns Notion raw evidence into Alih's portable model.
type Normalizer struct{}

// Connector is the connector name this adapter normalizes.
func (Normalizer) Connector() string { return "notion" }

// DisplayName is the human name sealed into archives this adapter produces.
func (Normalizer) DisplayName() string { return "Notion" }

// Normalizer deliberately does not implement connector.CredentialHostProvider.
//
// Notion serves files from signed storage URLs that carry their own
// authorisation in the query string and expire. Alih's credential belongs on
// api.notion.com and nowhere else, and nothing in this connector downloads from
// the API host. Declaring no hosts is therefore the honest answer, and Alih's
// fail-closed default already means exactly that: the credential is attached to
// nothing. Adding a declaration to "be safe" would widen where the secret
// travels for no benefit.

// NormalizeSnapshot turns a completed Notion snapshot into the portable model.
func (Normalizer) NormalizeSnapshot(evidence snapshot.Evidence) (model.Archive, error) {
	return NormalizeSnapshot(evidence)
}

// NormalizeSnapshot reads sealed raw evidence and produces the portable model.
//
// The interesting part is the block tree. Alih's portable model has containers,
// collections and records, and a record may point at a parent record. That
// parent pointer has no depth limit, so a Notion page's content nests as
// records under the page without inventing an entity kind and without renaming
// anything into another provider's vocabulary. The tree survives as a tree.
func NormalizeSnapshot(evidence snapshot.Evidence) (model.Archive, error) {
	if evidence.Connector != "notion" {
		return model.Archive{}, fmt.Errorf("Notion adapter cannot normalize connector %q", evidence.Connector)
	}
	state, err := rebuild(evidence)
	if err != nil {
		return model.Archive{}, err
	}

	provider := evidence.Connector
	workspaceID := evidence.Workspace.ID
	archive := model.Archive{
		Connector: provider,
		Workspace: model.Workspace{
			ID:     model.PortableID(provider, "workspace", workspaceID),
			Name:   optional(evidence.Workspace.Name),
			Source: model.SourceRef{Provider: provider, Type: "workspace", ID: workspaceID, RawPath: "raw/inventory.json"},
		},
		CapabilitySchemaVersion: evidence.CapabilitySchemaVersion,
		Capabilities:            evidence.Capabilities,
		Limitations:             state.limitations,
	}

	for _, db := range state.databases {
		archive.Containers = append(archive.Containers, model.Container{
			ID:          model.PortableID(provider, kindDatabase, db.ID),
			Kind:        kindDatabase,
			WorkspaceID: archive.Workspace.ID,
			Name:        optional(db.Title),
			Source:      sourceRef(provider, kindDatabase, db.ID, state.pathFor(kindDatabase, db.ID)),
		})
	}
	for _, ds := range state.dataSources {
		archive.Collections = append(archive.Collections, model.Collection{
			ID:          model.PortableID(provider, kindDataSource, ds.ID),
			WorkspaceID: archive.Workspace.ID,
			ContainerID: model.PortableID(provider, kindDatabase, ds.DatabaseID),
			Name:        optional(ds.Name),
			Source:      sourceRef(provider, kindDataSource, ds.ID, state.pathFor(kindDataSource, ds.ID)),
		})
		for _, property := range sortedProperties(ds.Properties) {
			definition, semantics := fieldDefinition(provider, ds, property, state.pathFor(kindDataSource, ds.ID))
			archive.FieldDefinitions = append(archive.FieldDefinitions, definition)
			_ = semantics
		}
	}

	for _, p := range state.pages {
		archive.Records = append(archive.Records, model.Record{
			ID:              model.PortableID(provider, kindPage, p.ID),
			Kind:            kindPage,
			WorkspaceID:     archive.Workspace.ID,
			CollectionID:    model.PortableID(provider, kindDataSource, p.DataSourceID),
			Title:           optional(p.Title),
			Archived:        boolPointer(p.Archived),
			CreatedAtSource: optional(p.CreatedTime),
			UpdatedAtSource: optional(p.EditedTime),
			Source:          sourceRef(provider, kindPage, p.ID, p.rawPath),
		})
		archive.RecordFieldValues = append(archive.RecordFieldValues,
			fieldValues(provider, state, p)...)
	}

	// Blocks are records nested under their page, or under another block. The
	// depth is whatever Notion had.
	pageCollection := map[string]string{}
	for _, p := range state.pages {
		pageCollection[p.ID] = p.DataSourceID
	}
	for _, b := range state.blocks {
		parent := model.PortableID(provider, kindPage, b.PageID)
		if b.ParentID != "" {
			parent = model.PortableID(provider, kindBlock, b.ParentID)
		}
		archive.Records = append(archive.Records, model.Record{
			ID:             model.PortableID(provider, kindBlock, b.ID),
			Kind:           kindBlock,
			WorkspaceID:    archive.Workspace.ID,
			CollectionID:   model.PortableID(provider, kindDataSource, pageCollection[b.PageID]),
			ParentRecordID: &parent,
			Title:          optional(b.Type),
			TextContent:    optional(b.Text),
			Source:         sourceRef(provider, kindBlock, b.ID, b.rawPath),
		})
	}

	return archive, nil
}

// fieldDefinition archives one data source property. The definition JSON must
// carry id, name and type so verification can prove the archived definition is
// the one a value was observed against.
func fieldDefinition(provider string, ds dataSource, property propertySchema, rawPath string) (model.FieldDefinition, string) {
	definition := map[string]any{
		"id":   property.ID,
		"name": property.Name,
		"type": property.Type,
	}
	if len(property.Raw) > 0 {
		var extra map[string]json.RawMessage
		if err := json.Unmarshal(property.Raw, &extra); err == nil {
			for key, value := range extra {
				if key == "id" || key == "name" || key == "type" {
					continue
				}
				definition[key] = value
			}
		}
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		encoded = []byte(`{}`)
	}
	// Notion computes formula and rollup values server-side. Alih records what
	// it observed and never claims to have executed those semantics.
	semantics := "SOURCE_DEFINITION_ONLY"
	switch property.Type {
	case "formula", "rollup", "created_time", "created_by", "last_edited_time", "last_edited_by", "unique_id":
		semantics = "OBSERVED_ONLY_NO_EXECUTION"
	}
	return model.FieldDefinition{
		ID:             model.PortableID(provider, "property", property.ID),
		Name:           optional(property.Name),
		FieldType:      optional(property.Type),
		SemanticsState: semantics,
		DefinitionJSON: encoded,
		Source:         sourceRef(provider, "property", property.ID, rawPath),
	}, semantics
}

func fieldValues(provider string, state *source, p page) []model.RecordFieldValue {
	schema := map[string]propertySchema{}
	for _, ds := range state.dataSources {
		if ds.ID != p.DataSourceID {
			continue
		}
		schema = ds.Properties
	}
	names := make([]string, 0, len(p.Properties))
	for name := range p.Properties {
		names = append(names, name)
	}
	sort.Strings(names)

	values := make([]model.RecordFieldValue, 0, len(names))
	for _, name := range names {
		property, known := schema[name]
		if !known {
			continue
		}
		observed := p.Properties[name]
		if !json.Valid(observed) {
			continue
		}
		values = append(values, model.RecordFieldValue{
			RecordID:          model.PortableID(provider, kindPage, p.ID),
			FieldID:           model.PortableID(provider, "property", property.ID),
			ObservedValueJSON: observed,
			// An observed value is always OBSERVED_ONLY. Alih recorded what it
			// saw; it did not execute Notion's semantics to produce it.
			SemanticsState: "OBSERVED_ONLY",
		})
	}
	return values
}

func sortedProperties(properties map[string]propertySchema) []propertySchema {
	sorted := make([]propertySchema, 0, len(properties))
	for _, property := range properties {
		sorted = append(sorted, property)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	return sorted
}

// sourceRef retains the original identifier and the path back into the sealed
// raw tree, so every archived row can be traced to the response it came from.
func sourceRef(provider, objectType, id, rawPath string) model.SourceRef {
	return model.SourceRef{Provider: provider, Type: objectType, ID: id, RawPath: rawPath}
}

func optional(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func boolPointer(value bool) *bool { return &value }
