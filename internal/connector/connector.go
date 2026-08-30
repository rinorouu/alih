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

// Package connector defines the source-neutral boundary for SaaS connectors.
package connector

import (
	"context"
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
	Identity   Identity
	Workspaces []Workspace
}

// Authenticator validates a credential and discovers the identity and
// workspaces available through it without modifying source data.
type Authenticator interface {
	Connector
	Authenticate(ctx context.Context, credential string) (Authentication, error)
}

// Inventory contains source counts established by a completed M2 traversal.
// Comments and attachments are deliberately scoped to tasks in V0 M2.
type Inventory struct {
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
	Name  string          `json:"name"`
	State CapabilityState `json:"state"`
	Note  string          `json:"note"`
}

// ScanResult is returned only after every supported M2 traversal terminates
// successfully. A connector error means no trustworthy inventory is available.
type ScanResult struct {
	Workspace    Workspace
	Inventory    Inventory
	Capabilities []Capability
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
