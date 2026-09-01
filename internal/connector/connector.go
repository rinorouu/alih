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

// Package connector defines the source-neutral boundary for SaaS connectors.
package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// Connector identifies a source connector without exposing provider-specific
// details to the core application. Operational methods will be introduced by
// the milestones that define their behavior and correctness requirements.
type Connector interface {
	Name() string
}

// Identity is the authenticated account reported by a source connector.
type Identity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Workspace is a source workspace accessible to the authenticated identity.
type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Authentication is the source-neutral result of credential validation and
// workspace discovery. It is intentionally limited to the M1 contract.
type Authentication struct {
	Identity   Identity              `json:"identity"`
	Workspaces []Workspace           `json:"workspaces"`
	Assessment OperationalAssessment `json:"operational_assessment"`
}

// Authenticator validates a credential and discovers the identity and
// workspaces available through it without modifying source data.
type Authenticator interface {
	Connector
	Authenticate(ctx context.Context, credential string) (Authentication, error)
}

// Inventory contains source counts established by a completed M2 traversal.
//
// The totals are provider-neutral because Core reconciles against them and must
// not require a connector to describe its objects in another provider's words.
// ContainerKinds and RecordKinds carry the connector's own vocabulary and its
// counts; Core never interprets those keys, it only checks that the portable
// rows carrying each kind add up to what the connector said.
type Inventory struct {
	Containers    int `json:"containers"`
	Collections   int `json:"collections"`
	Records       int `json:"records"`
	NestedRecords int `json:"nested_records"`
	Comments      int `json:"comments"`
	Attachments   int `json:"attachments"`
	CustomFields  int `json:"custom_fields"`
	Relationships int `json:"relationships"`

	ContainerKinds map[string]int `json:"container_kinds,omitempty"`
	RecordKinds    map[string]int `json:"record_kinds,omitempty"`
}

// legacyInventory is the vocabulary Alih used before the portable model was
// made provider-neutral. It is read, never written: a snapshot recorded by an
// earlier release must keep meaning exactly what it meant when it was sealed.
type legacyInventory struct {
	Spaces        int `json:"spaces"`
	Folders       int `json:"folders"`
	Lists         int `json:"lists"`
	Tasks         int `json:"tasks"`
	Subtasks      int `json:"subtasks"`
	Comments      int `json:"comments"`
	Attachments   int `json:"attachments"`
	CustomFields  int `json:"custom_fields"`
	Relationships int `json:"relationships"`
}

// UnmarshalJSON reads either vocabulary. A legacy document is mapped onto the
// neutral totals with its original kind names preserved, so nothing an older
// release recorded is lost or reinterpreted.
func (inventory *Inventory) UnmarshalJSON(content []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(content, &probe); err != nil {
		return err
	}
	_, hasSpaces := probe["spaces"]
	_, hasLists := probe["lists"]
	if hasSpaces || hasLists {
		var legacy legacyInventory
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&legacy); err != nil {
			return fmt.Errorf("decode legacy inventory: %w", err)
		}
		*inventory = legacy.neutral()
		return nil
	}

	type plain Inventory
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*inventory = Inventory(decoded)
	return nil
}

// neutral projects the legacy vocabulary onto the neutral one without losing
// the distinction between a space and a folder, or a task and a subtask.
func (legacy legacyInventory) neutral() Inventory {
	inventory := Inventory{
		Containers:    legacy.Spaces + legacy.Folders,
		Collections:   legacy.Lists,
		Records:       legacy.Tasks + legacy.Subtasks,
		NestedRecords: legacy.Subtasks,
		Comments:      legacy.Comments,
		Attachments:   legacy.Attachments,
		CustomFields:  legacy.CustomFields,
		Relationships: legacy.Relationships,
	}
	inventory.ContainerKinds = map[string]int{"space": legacy.Spaces, "folder": legacy.Folders}
	inventory.RecordKinds = map[string]int{"task": legacy.Tasks, "subtask": legacy.Subtasks}
	return inventory
}

// Legacy projects an inventory back into the vocabulary an earlier release
// recorded. It exists so a digest taken over a legacy snapshot can be
// reproduced byte for byte, and it is meaningful only for such a snapshot.
func (inventory Inventory) Legacy() any {
	return legacyInventory{
		Spaces:        inventory.ContainerKinds["space"],
		Folders:       inventory.ContainerKinds["folder"],
		Lists:         inventory.Collections,
		Tasks:         inventory.RecordKinds["task"],
		Subtasks:      inventory.RecordKinds["subtask"],
		Comments:      inventory.Comments,
		Attachments:   inventory.Attachments,
		CustomFields:  inventory.CustomFields,
		Relationships: inventory.Relationships,
	}
}

// CapabilityState records what the connector can establish without turning
// unknown or partial source behavior into a success claim.
type CapabilityState string

const (
	CapabilitySupported   CapabilityState = "SUPPORTED"
	CapabilityPartial     CapabilityState = "PARTIAL"
	CapabilityUnsupported CapabilityState = "UNSUPPORTED"
	CapabilityUnavailable CapabilityState = "UNAVAILABLE"
	CapabilityUnknown     CapabilityState = "UNKNOWN"
	CapabilityFailed      CapabilityState = "FAILED"
)

// Capability describes one explicitly scoped source capability.
type Capability struct {
	// ID is the stable provider-neutral domain identity. It is absent only on
	// artifacts created before capability contract version 1.
	ID CapabilityID `json:"id,omitempty"`
	// Name is a human-facing label and must never be used as machine identity.
	Name string `json:"name"`
	// Requirement states whether failure of this capability prevents a clean
	// operation within the connector's declared scope.
	Requirement CapabilityRequirement `json:"requirement,omitempty"`
	// Implementation describes connector support independently from whether a
	// particular run could obtain the capability.
	Implementation CapabilityState `json:"implementation,omitempty"`
	// Availability is the observation made by this run. UNKNOWN means the run
	// did not establish availability; it does not mean unsupported.
	Availability CapabilityAvailability `json:"availability,omitempty"`
	// State is the legacy compatibility projection of Implementation. New
	// capability contract v1 records must keep the two equal.
	State CapabilityState `json:"state"`
	Note  string          `json:"note"`
}

// CapabilityProvider declares the provider-neutral capabilities implemented
// by a connector without requiring Core to know provider endpoints.
type CapabilityProvider interface {
	Connector
	CapabilityContract() CapabilityContract
}

// ScanResult is returned only after every supported M2 traversal terminates
// successfully. A connector error means no trustworthy inventory is available.
type ScanResult struct {
	Workspace               Workspace             `json:"workspace"`
	Inventory               Inventory             `json:"inventory"`
	CapabilitySchemaVersion int                   `json:"capability_schema_version"`
	Capabilities            []Capability          `json:"capabilities"`
	Assessment              OperationalAssessment `json:"operational_assessment"`
}

// Scanner inventories one authenticated Workspace through read-only source
// operations. It must fail rather than return partial counts.
type Scanner interface {
	Connector
	Scan(ctx context.Context, credential string, workspace Workspace) (ScanResult, error)
}

// RawResponse is one successful, read-only source API response. Body contains
// the exact response bytes returned by the provider; request headers are
// intentionally absent so credentials cannot become snapshot metadata.
type RawResponse struct {
	Operation  string
	Method     string
	Path       string
	Query      url.Values
	Attempt    int
	StatusCode int
	Body       []byte
}

// RequestFailure accounts for an API attempt that produced no usable raw
// response. Error must be safe for local persistence and must not contain a
// credential or an unfiltered provider response body.
type RequestFailure struct {
	Operation  string
	Method     string
	Path       string
	Query      url.Values
	Attempt    int
	StatusCode int
	Retrying   bool
	Error      string
}

// RawEvidenceSink persists provider responses separately from any future
// normalized or portable representation.
type RawEvidenceSink interface {
	RecordResponse(RawResponse) error
	RecordFailure(RequestFailure) error
}

// SourceObject identifies one logical source object using the provider's
// original identifier. Composite is true only when the source API exposes no
// identifier for the represented relationship and Alih must use stable source
// endpoint identifiers instead.
type SourceObject struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	ParentType string `json:"parent_type,omitempty"`
	ParentID   string `json:"parent_id,omitempty"`
	Composite  bool   `json:"composite,omitempty"`
}

// ExtractionResult is the M3 source-level inventory. SourceObjects is not a
// normalized or portable model; it is a stable index into the raw evidence.
type ExtractionResult struct {
	ScanResult
	SourceObjects []SourceObject
}

// Extractor performs the same fail-closed traversal as Scanner while emitting
// raw API evidence. It must not return a partial ExtractionResult.
type Extractor interface {
	Connector
	Extract(ctx context.Context, credential string, workspace Workspace, sink RawEvidenceSink) (ExtractionResult, error)
}

// CredentialHostProvider is implemented by a connector whose archived
// attachments must be fetched with its own credential.
//
// It exists because deciding which host may receive a credential is a fact
// about a provider, not about Alih. Core previously carried one provider's API
// hostname in the archive writer and attached the credential only there, which
// worked for exactly one connector and silently sent no credential for any
// other. Making the connector declare its hosts keeps that knowledge in the
// adapter that owns it.
//
// The contract is deliberately fail-closed. A connector that does not implement
// this interface, or returns nothing, gets no credential on any attachment
// request: an attachment served from a signed URL needs no credential, and
// guessing wrongly would leak one to whatever host a source named.
//
// Hosts are compared case-insensitively against the URL host, without port and
// without wildcards. A redirect away from the declared set has the credential
// stripped before it is followed.
//
// It is a single method on purpose. The declaration is an optional capability
// that Core type-asserts on whichever adapter object it already holds, so it
// composes with the extraction and normalization interfaces without forcing a
// connector to restate its identity for a third time.
type CredentialHostProvider interface {
	// CredentialHosts returns the hostnames that may receive this connector's
	// credential. Returning nil means no host may.
	CredentialHosts() []string
}
