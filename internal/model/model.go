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

// Package model defines Alih's source-agnostic M4 portable model.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"alih/internal/connector"
)

// SourceRef retains the provider identity of every portable entity and a
// pointer back to the immutable raw response from which it was normalized.
type SourceRef struct {
	Provider    string
	Type        string
	ID          string
	RawPath     string
	IDComposite bool
}

type Workspace struct {
	ID     string
	Name   *string
	Source SourceRef
}

type Container struct {
	ID          string
	Kind        string
	WorkspaceID string
	ParentID    *string
	Name        *string
	Source      SourceRef
}

type Collection struct {
	ID          string
	WorkspaceID string
	ContainerID string
	Name        *string
	Source      SourceRef
}

type Record struct {
	ID              string
	Kind            string
	WorkspaceID     string
	CollectionID    string
	ParentRecordID  *string
	Title           *string
	Description     *string
	TextContent     *string
	Status          *string
	StatusType      *string
	Priority        *string
	Archived        *bool
	CreatedAtSource *string
	UpdatedAtSource *string
	ClosedAtSource  *string
	DoneAtSource    *string
	StartAtSource   *string
	DueAtSource     *string
	TimeEstimateMS  *int64
	TimeSpentMS     *int64
	Points          *float64
	Source          SourceRef
}

type Identity struct {
	ID       string
	Username *string
	Email    *string
	Source   SourceRef
}

type RecordIdentity struct {
	RecordID   string
	IdentityID string
	Role       string
	Position   int
}

type RecordTag struct {
	RecordID string
	Name     string
	Position int
}

type Comment struct {
	ID               string
	WorkspaceID      string
	RecordID         string
	ParentCommentID  *string
	AuthorIdentityID *string
	BodyText         *string
	BodyJSON         json.RawMessage
	CreatedAtSource  *string
	Source           SourceRef
}

type FieldDefinition struct {
	ID             string
	Name           *string
	FieldType      *string
	SemanticsState string
	DefinitionJSON json.RawMessage
	Source         SourceRef
}

type RecordFieldValue struct {
	RecordID          string
	FieldID           string
	ObservedValueJSON json.RawMessage
	SemanticsState    string
}

type Relationship struct {
	ID                 string
	Kind               string
	FromRecordID       *string
	ToRecordID         *string
	FromSourceID       string
	ToSourceID         string
	ResolutionState    string
	SourceMetadataJSON json.RawMessage
	Source             SourceRef
}

type Attachment struct {
	ID             string
	WorkspaceID    string
	RecordID       string
	Filename       *string
	MediaType      *string
	ExpectedSize   *int64
	SourceURL      *string
	DownloadURL    string
	DownloadStatus string
	LocalPath      *string
	ArchivedSize   *int64
	Checksum       *string
	Error          *string
	Source         SourceRef
}

// Archive is the complete source-agnostic in-memory representation consumed by
// the M4 writer. Capabilities retain source limitations without changing the
// portable entity schema.
type Archive struct {
	Connector         string
	Workspace         Workspace
	Containers        []Container
	Collections       []Collection
	Records           []Record
	Identities        []Identity
	RecordIdentities  []RecordIdentity
	RecordTags        []RecordTag
	Comments          []Comment
	FieldDefinitions  []FieldDefinition
	RecordFieldValues []RecordFieldValue
	Relationships     []Relationship
	Attachments       []Attachment
	Capabilities      []connector.Capability
	Limitations       []string
}

// PortableID deterministically maps an original provider identifier into the
// portable namespace while SourceRef continues to retain the original value.
func PortableID(provider, sourceType, sourceID string) string {
	digest := sha256.Sum256([]byte(provider + "\x00" + sourceType + "\x00" + sourceID))
	return "alih_" + hex.EncodeToString(digest[:])
}
