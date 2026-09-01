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

package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"alih/internal/connector"
	"alih/internal/model"
	"alih/internal/snapshot"
	"alih/internal/verify"
)

// This file is a paper adapter made executable. It is not a second connector
// and it names no SaaS: it is the smallest thing that satisfies every
// interface Core requires, built only to answer one question — can a provider
// that is not ClickUp traverse the whole operational boundary without Core
// learning anything about it?
//
// Everything provider-specific lives here. If Core needed a change that this
// file could not express, that is a blocker, and it is recorded as one.

const (
	fakeConnectorName = "example"
	fakeWorkspaceID   = "ws-1"
	fakeWorkspaceName = "Example Workspace"
	fakeAttachment    = "example attachment payload\n"
)

// fakeSource is the provider's own shape. Core never sees this type.
type fakeSource struct {
	sections []fakeSection
}

type fakeSection struct {
	ID, Name string
	boards   []fakeBoard
}

type fakeBoard struct {
	ID, Name string
	items    []fakeItem
}

type fakeItem struct {
	ID, Title, State string
	parentID         string
	note             string
	fieldValue       string
	attachment       *fakeAttachmentRef
	dependsOn        string
}

type fakeAttachmentRef struct {
	ID, Filename, MediaType string
	Size                    int64
}

func defaultFakeSource() fakeSource {
	return fakeSource{sections: []fakeSection{{
		ID: "sec-1", Name: "Delivery",
		boards: []fakeBoard{{
			ID: "brd-1", Name: "Current work",
			items: []fakeItem{
				{
					ID: "itm-1", Title: "First item", State: "open", note: "the first note",
					fieldValue: `"opt-a"`, dependsOn: "itm-2",
					attachment: &fakeAttachmentRef{ID: "att-1", Filename: "notes.txt", MediaType: "text/plain", Size: int64(len(fakeAttachment))},
				},
				{ID: "itm-2", Title: "Second item", State: "done"},
				{ID: "itm-3", Title: "Nested item", State: "open", parentID: "itm-1"},
			},
		}},
	}}}
}

// ---------------------------------------------------------------------------
// Authenticator, Scanner, Extractor, CapabilityProvider
// ---------------------------------------------------------------------------

type fakeConnector struct {
	source     fakeSource
	authErr    error
	scanErr    error
	extractErr error
	workspaces []connector.Workspace
}

func newFakeConnector() *fakeConnector {
	return &fakeConnector{
		source:     defaultFakeSource(),
		workspaces: []connector.Workspace{{ID: fakeWorkspaceID, Name: fakeWorkspaceName}},
	}
}

func (c *fakeConnector) Name() string { return fakeConnectorName }

// DisplayName is how this connector names itself in a recovery report.
func (c *fakeConnector) DisplayName() string { return "Example Provider" }

func (c *fakeConnector) Authenticate(_ context.Context, credential string) (connector.Authentication, error) {
	if c.authErr != nil {
		return connector.Authentication{}, c.authErr
	}
	if strings.TrimSpace(credential) == "" {
		return connector.Authentication{}, errors.New("example: credential is required")
	}
	assessment, err := connector.HealthyAssessment(fakeConnectorName, connector.HealthBasisAuthentication,
		fakeClock(), connector.AuthenticationAuthenticated, c.capabilities())
	if err != nil {
		return connector.Authentication{}, err
	}
	return connector.Authentication{
		Identity:   connector.Identity{ID: "acct-1", Name: "Example Account"},
		Workspaces: c.workspaces,
		Assessment: assessment,
	}, nil
}

// CapabilityContract declares what this adapter implements, without any
// network access, exactly as the ClickUp adapter does.
func (c *fakeConnector) CapabilityContract() connector.CapabilityContract {
	return connector.CapabilityContract{
		SchemaVersion: connector.CapabilitySchemaVersion,
		Connector:     fakeConnectorName,
		Capabilities: []connector.Capability{
			contractRecord(connector.CapabilityWorkspaceData, "Workspace hierarchy"),
			contractRecord(connector.CapabilityItems, "Items and nested items"),
			contractRecord(connector.CapabilityComments, "Item comments"),
			contractRecord(connector.CapabilityAttachmentMetadata, "Attachment metadata"),
			contractRecord(connector.CapabilityAttachmentContent, "Attachment content"),
			contractRecord(connector.CapabilityCustomFields, "Custom fields"),
			contractRecord(connector.CapabilityRelationships, "Item relationships"),
			contractRecord(connector.CapabilityRawEvidence, "Raw evidence"),
		},
	}
}

func contractRecord(id connector.CapabilityID, label string) connector.Capability {
	return connector.Capability{
		ID: id, Name: label,
		Requirement:    connector.CapabilityRequired,
		Implementation: connector.CapabilitySupported,
		// A declaration cannot know what a run will observe, so availability is
		// UNKNOWN until an operation establishes otherwise.
		Availability: connector.CapabilityAvailabilityUnknown,
		State:        connector.CapabilitySupported,
	}
}

func (c *fakeConnector) capabilities() []connector.Capability {
	// Attachment content and raw evidence are deliberately left at the
	// declaration's UNKNOWN: scanning establishes neither, and M4 refines
	// attachment content only after it actually attempts retrieval.
	capabilities, err := connector.ObserveCapabilities(c.CapabilityContract(),
		map[connector.CapabilityID]connector.CapabilityAvailability{
			connector.CapabilityWorkspaceData:      connector.CapabilityAvailabilityAvailable,
			connector.CapabilityItems:              connector.CapabilityAvailabilityAvailable,
			connector.CapabilityComments:           connector.CapabilityAvailabilityAvailable,
			connector.CapabilityAttachmentMetadata: connector.CapabilityAvailabilityAvailable,
			connector.CapabilityCustomFields:       connector.CapabilityAvailabilityAvailable,
			connector.CapabilityRelationships:      connector.CapabilityAvailabilityAvailable,
			connector.CapabilityRawEvidence:        connector.CapabilityAvailabilityAvailable,
		})
	if err != nil {
		panic("example connector produced an invalid capability observation: " + err.Error())
	}
	return capabilities
}

func (c *fakeConnector) inventory() connector.Inventory {
	inventory := connector.Inventory{
		Containers: len(c.source.sections), CustomFields: 1,
		ContainerKinds: map[string]int{"section": len(c.source.sections)},
		RecordKinds:    map[string]int{},
	}
	for _, section := range c.source.sections {
		inventory.Collections += len(section.boards)
		for _, board := range section.boards {
			for _, item := range board.items {
				inventory.Records++
				if item.parentID != "" {
					inventory.NestedRecords++
					inventory.RecordKinds["nested_item"]++
				} else {
					inventory.RecordKinds["item"]++
				}
				if item.note != "" {
					inventory.Comments++
				}
				if item.attachment != nil {
					inventory.Attachments++
				}
				if item.dependsOn != "" {
					inventory.Relationships++
				}
			}
		}
	}
	return inventory
}

func (c *fakeConnector) Scan(_ context.Context, _ string, workspace connector.Workspace) (connector.ScanResult, error) {
	if c.scanErr != nil {
		return connector.ScanResult{}, c.scanErr
	}
	capabilities := c.capabilities()
	assessment, err := connector.HealthyAssessment(fakeConnectorName, connector.HealthBasisScan,
		fakeClock(), connector.AuthenticationAuthenticated, capabilities)
	if err != nil {
		return connector.ScanResult{}, err
	}
	return connector.ScanResult{
		Workspace:               workspace,
		Inventory:               c.inventory(),
		CapabilitySchemaVersion: connector.CapabilitySchemaVersion,
		Capabilities:            capabilities,
		Assessment:              assessment,
	}, nil
}

// Extract emits the provider's own response bytes into the evidence sink and
// returns a source-object index into them. Core learns nothing about the shape
// of those bytes.
func (c *fakeConnector) Extract(ctx context.Context, credential string, workspace connector.Workspace, sink connector.RawEvidenceSink) (connector.ExtractionResult, error) {
	if c.extractErr != nil {
		return connector.ExtractionResult{}, c.extractErr
	}
	scan, err := c.Scan(ctx, credential, workspace)
	if err != nil {
		return connector.ExtractionResult{}, err
	}
	objects := []connector.SourceObject{{Type: "workspace", ID: workspace.ID}}

	record := func(operation, path string, body any) error {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		return sink.RecordResponse(connector.RawResponse{
			Operation: operation, Method: http.MethodGet, Path: path,
			Query: url.Values{}, Attempt: 1, StatusCode: http.StatusOK, Body: encoded,
		})
	}

	if err := record("list sections", "/workspaces/"+workspace.ID+"/sections", c.source.sections); err != nil {
		return connector.ExtractionResult{}, err
	}
	for _, section := range c.source.sections {
		objects = append(objects, connector.SourceObject{
			Type: "section", ID: section.ID, ParentType: "workspace", ParentID: workspace.ID,
		})
		for _, board := range section.boards {
			if err := record("get board", "/boards/"+board.ID, board); err != nil {
				return connector.ExtractionResult{}, err
			}
			objects = append(objects, connector.SourceObject{
				Type: "board", ID: board.ID, ParentType: "section", ParentID: section.ID,
			})
			for _, item := range board.items {
				if err := record("get item", "/items/"+item.ID, item); err != nil {
					return connector.ExtractionResult{}, err
				}
				kind, parentType, parentID := "item", "board", board.ID
				if item.parentID != "" {
					kind, parentType, parentID = "nested_item", "item", item.parentID
				}
				objects = append(objects, connector.SourceObject{
					Type: kind, ID: item.ID, ParentType: parentType, ParentID: parentID,
				})
				if item.note != "" {
					objects = append(objects, connector.SourceObject{
						Type: "comment", ID: item.ID + "-note", ParentType: "item", ParentID: item.ID,
					})
				}
				if item.attachment != nil {
					objects = append(objects, connector.SourceObject{
						Type: "attachment", ID: item.attachment.ID, ParentType: "item", ParentID: item.ID,
					})
				}
				if item.dependsOn != "" {
					objects = append(objects, connector.SourceObject{
						Type: "relationship.depends_on", ID: item.ID + "->" + item.dependsOn,
						ParentType: "item", ParentID: item.ID, Composite: true,
					})
				}
			}
		}
	}
	if err := record("list fields", "/workspaces/"+workspace.ID+"/fields",
		map[string]any{"id": "fld-1", "name": "Track", "type": "select", "options": []string{"opt-a", "opt-b"}}); err != nil {
		return connector.ExtractionResult{}, err
	}
	objects = append(objects, connector.SourceObject{Type: "custom_field", ID: "fld-1"})

	return connector.ExtractionResult{ScanResult: scan, SourceObjects: objects}, nil
}

// ---------------------------------------------------------------------------
// Normalizer: the provider's evidence to the portable model
// ---------------------------------------------------------------------------

// NormalizeSnapshot is this adapter's M3-to-portable step. It has the same
// signature as the ClickUp adapter's, which is what makes registration
// possible at all.
func (c *fakeConnector) NormalizeSnapshot(evidence snapshot.Evidence) (model.Archive, error) {
	if evidence.Connector != fakeConnectorName {
		return model.Archive{}, fmt.Errorf("example adapter cannot normalize connector %q", evidence.Connector)
	}
	// Raw paths come from the evidence responses, so every portable row points
	// at the exact bytes it was derived from. The mapping from an object to the
	// response that produced it is provider knowledge and stays here.
	responsePath := make(map[string]string, len(evidence.Responses))
	for _, response := range evidence.Responses {
		// The snapshot records a path relative to the M3 root; the archive
		// seals that tree under raw/, so a portable row must point there.
		responsePath[response.Path] = "raw/" + response.RawPath
	}
	rawPathFor := func(sourceType, id string) string {
		switch sourceType {
		case "workspace", "account":
			return "raw/inventory.json"
		case "section":
			return responsePath["/workspaces/"+evidence.Workspace.ID+"/sections"]
		case "custom_field":
			return responsePath["/workspaces/"+evidence.Workspace.ID+"/fields"]
		case "item", "nested_item":
			return responsePath["/items/"+id]
		case "comment":
			return responsePath["/items/"+strings.TrimSuffix(id, "-note")]
		case "attachment":
			for _, section := range c.source.sections {
				for _, board := range section.boards {
					for _, item := range board.items {
						if item.attachment != nil && item.attachment.ID == id {
							return responsePath["/items/"+item.ID]
						}
					}
				}
			}
		case "relationship.depends_on":
			return responsePath["/items/"+strings.SplitN(id, "->", 2)[0]]
		case "board":
			return responsePath["/boards/"+id]
		}
		return ""
	}
	reference := func(sourceType, id string, composite bool) model.SourceRef {
		return model.SourceRef{
			Provider: fakeConnectorName, Type: sourceType, ID: id,
			RawPath: rawPathFor(sourceType, id), IDComposite: composite,
		}
	}
	portableID := func(sourceType, id string) string {
		return model.PortableID(fakeConnectorName, sourceType, id)
	}
	text := func(value string) *string { return &value }

	workspaceID := portableID("workspace", evidence.Workspace.ID)
	archive := model.Archive{
		Connector: fakeConnectorName,
		Workspace: model.Workspace{
			ID: workspaceID, Name: text(evidence.Workspace.Name),
			Source: reference("workspace", evidence.Workspace.ID, false),
		},
		CapabilitySchemaVersion: evidence.CapabilitySchemaVersion,
		Capabilities:            append([]connector.Capability(nil), evidence.Capabilities...),
	}
	identityID := portableID("identity", "acct-1")
	archive.Identities = append(archive.Identities, model.Identity{
		ID: identityID, Username: text("Example Account"),
		Source: reference("account", "acct-1", false),
	})
	fieldID := portableID("custom_field", "fld-1")
	archive.FieldDefinitions = append(archive.FieldDefinitions, model.FieldDefinition{
		ID: fieldID, Name: text("Track"), FieldType: text("select"),
		SemanticsState: "SOURCE_DEFINITION_ONLY",
		DefinitionJSON: json.RawMessage(`{"id":"fld-1","name":"Track","type":"select","options":["opt-a","opt-b"]}`),
		Source:         reference("custom_field", "fld-1", false),
	})

	for _, section := range c.source.sections {
		containerID := portableID("section", section.ID)
		archive.Containers = append(archive.Containers, model.Container{
			ID: containerID, Kind: "section", WorkspaceID: workspaceID,
			Name: text(section.Name), Source: reference("section", section.ID, false),
		})
		for _, board := range section.boards {
			collectionID := portableID("board", board.ID)
			archive.Collections = append(archive.Collections, model.Collection{
				ID: collectionID, WorkspaceID: workspaceID, ContainerID: containerID,
				Name: text(board.Name), Source: reference("board", board.ID, false),
			})
			for _, item := range board.items {
				sourceType, kind := "item", "item"
				if item.parentID != "" {
					sourceType, kind = "nested_item", "nested_item"
				}
				recordID := portableID(sourceType, item.ID)
				entry := model.Record{
					ID: recordID, Kind: kind, WorkspaceID: workspaceID, CollectionID: collectionID,
					Title: text(item.Title), Status: text(item.State),
					Source: reference(sourceType, item.ID, false),
				}
				if item.parentID != "" {
					parent := portableID("item", item.parentID)
					entry.ParentRecordID = &parent
				}
				archive.Records = append(archive.Records, entry)
				archive.RecordIdentities = append(archive.RecordIdentities, model.RecordIdentity{
					RecordID: recordID, IdentityID: identityID, Role: "creator", Position: 0,
				})
				if item.note != "" {
					archive.Comments = append(archive.Comments, model.Comment{
						ID: portableID("comment", item.ID+"-note"), WorkspaceID: workspaceID,
						RecordID: recordID, AuthorIdentityID: &identityID, BodyText: text(item.note),
						Source: reference("comment", item.ID+"-note", false),
					})
				}
				if item.fieldValue != "" {
					archive.RecordFieldValues = append(archive.RecordFieldValues, model.RecordFieldValue{
						RecordID: recordID, FieldID: fieldID,
						ObservedValueJSON: json.RawMessage(item.fieldValue),
						SemanticsState:    "OBSERVED_ONLY",
					})
				}
				if item.attachment != nil {
					size := item.attachment.Size
					archive.Attachments = append(archive.Attachments, model.Attachment{
						ID: portableID("attachment", item.attachment.ID), WorkspaceID: workspaceID,
						RecordID: recordID, Filename: text(item.attachment.Filename),
						MediaType: text(item.attachment.MediaType), ExpectedSize: &size,
						SourceURL:      text("https://files.example.test/" + item.attachment.ID),
						DownloadURL:    "https://files.example.test/" + item.attachment.ID + "?signature=private",
						DownloadStatus: "PENDING",
						Source:         reference("attachment", item.attachment.ID, false),
					})
				}
				if item.dependsOn != "" {
					target := portableID("item", item.dependsOn)
					from := recordID
					archive.Relationships = append(archive.Relationships, model.Relationship{
						ID:   portableID("relationship.depends_on", item.ID+"->"+item.dependsOn),
						Kind: "depends_on", FromRecordID: &from, ToRecordID: &target,
						FromSourceID: item.ID, ToSourceID: item.dependsOn,
						ResolutionState: "RESOLVED",
						Source:          reference("relationship.depends_on", item.ID+"->"+item.dependsOn, true),
					})
				}
			}
		}
	}
	archive.Limitations = []string{
		"Example connector fixture: this provider is not a real service.",
	}
	return archive, nil
}

// ---------------------------------------------------------------------------
// Field semantics
// ---------------------------------------------------------------------------

type fakeFieldSemantics struct{}

func (fakeFieldSemantics) Connector() string { return fakeConnectorName }

// ValidateFieldValue proves an observed selection against the archived
// definition and returns UNPROVEN rather than guessing, exactly as the
// contract requires.
func (fakeFieldSemantics) ValidateFieldValue(fieldType string, definitionJSON, observedJSON []byte) (string, string) {
	if fieldType != "select" {
		return verify.FieldValueUnproven, "Alih does not claim semantics for this field type."
	}
	var definition struct {
		Options []string `json:"options"`
	}
	if err := json.Unmarshal(definitionJSON, &definition); err != nil || len(definition.Options) == 0 {
		return verify.FieldValueUnproven, "The archived field definition does not enumerate its options."
	}
	var observed string
	if err := json.Unmarshal(observedJSON, &observed); err != nil {
		return verify.FieldValueUnproven, "The observed value is not a single selection."
	}
	for _, option := range definition.Options {
		if option == observed {
			return verify.FieldValueValid, "The observed selection exists in the archived definition."
		}
	}
	return verify.FieldValueInvalid, "The observed selection is not in the archived definition."
}

// ---------------------------------------------------------------------------
// Test transport and clock
// ---------------------------------------------------------------------------

func fakeClock() time.Time { return time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC) }

type fakeRoundTrip func(*http.Request) (*http.Response, error)

func (function fakeRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

// Compile-time proof that the paper adapter really does satisfy every
// interface Core requires of a connector. If Core grows a required method,
// this stops compiling, which is the point.
var (
	_ connector.Connector          = (*fakeConnector)(nil)
	_ connector.Authenticator      = (*fakeConnector)(nil)
	_ connector.Scanner            = (*fakeConnector)(nil)
	_ connector.Extractor          = (*fakeConnector)(nil)
	_ connector.CapabilityProvider = (*fakeConnector)(nil)
	_ verify.FieldSemantics        = fakeFieldSemantics{}
)
