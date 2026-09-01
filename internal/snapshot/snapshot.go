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

// Package snapshot persists M3 raw source evidence. It deliberately has no
// portable-model, archive-manifest, attachment-download, or verification logic.
package snapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"alih/internal/buildinfo"
	"alih/internal/connector"
)

// schemaVersion 2 records the provider-neutral inventory vocabulary. Version 1
// snapshots remain readable and keep their original digest basis.
const schemaVersion = 2

type requestRecord struct {
	Sequence   int    `json:"sequence"`
	Operation  string `json:"operation"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Query      string `json:"query,omitempty"`
	Attempt    int    `json:"attempt"`
	StatusCode int    `json:"status_code,omitempty"`
	Outcome    string `json:"outcome"`
	Retrying   bool   `json:"retrying,omitempty"`
	RawPath    string `json:"raw_path,omitempty"`
	Bytes      int    `json:"bytes,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Error      string `json:"error,omitempty"`
}

type sourceDescriptor struct {
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
}

// identityRecord names the authenticated account whose access produced this
// extraction. An archive is always one identity's view of the source, and
// recording which one is the only part of that Alih can establish.
type identityRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type requestSummary struct {
	Attempts            int `json:"attempts"`
	SuccessfulResponses int `json:"successful_responses"`
	FailedAttempts      int `json:"failed_attempts"`
	RetriedAttempts     int `json:"retried_attempts"`
}

type runRecord struct {
	SchemaVersion    int               `json:"schema_version"`
	Kind             string            `json:"kind"`
	Status           string            `json:"status"`
	AlihVersion      string            `json:"alih_version,omitempty"`
	Connector        string            `json:"connector"`
	Source           sourceDescriptor  `json:"source"`
	StartedAt        time.Time         `json:"started_at"`
	FinishedAt       time.Time         `json:"finished_at"`
	ExtractedBy      *identityRecord   `json:"extracted_by,omitempty"`
	Requests         requestSummary    `json:"requests"`
	Consistency      consistencyRecord `json:"source_consistency"`
	LogicalDigest    string            `json:"logical_inventory_digest,omitempty"`
	CapabilityDigest string            `json:"capability_digest,omitempty"`
	Failure          string            `json:"failure,omitempty"`
}

type consistencyRecord struct {
	Atomic bool   `json:"atomic"`
	Note   string `json:"note"`
}

type inventoryRecord struct {
	SchemaVersion           int                              `json:"schema_version"`
	Kind                    string                           `json:"kind"`
	Connector               string                           `json:"connector"`
	WorkspaceID             string                           `json:"workspace_id"`
	Counts                  connector.Inventory              `json:"counts"`
	CapabilitySchemaVersion int                              `json:"capability_schema_version,omitempty"`
	Capabilities            []connector.Capability           `json:"capabilities"`
	CapabilityDigest        string                           `json:"capability_digest,omitempty"`
	OperationalAssessment   *connector.OperationalAssessment `json:"operational_assessment,omitempty"`
	SourceObjects           []connector.SourceObject         `json:"source_objects"`
	LogicalDigest           string                           `json:"logical_digest"`
}

type digestInput struct {
	Connector     string                   `json:"connector"`
	WorkspaceID   string                   `json:"workspace_id"`
	Counts        any                      `json:"counts"`
	SourceObjects []connector.SourceObject `json:"source_objects"`
}

// Summary describes a successfully finalized raw snapshot.
type Summary struct {
	Path            string
	LogicalDigest   string
	Requests        int
	RawResponses    int
	RetriedAttempts int
}

// Session owns a private staging directory until it is finalized as either a
// complete target or an explicitly failed evidence directory.
type Session struct {
	mu            sync.Mutex
	targetPath    string
	stagingPath   string
	connectorName string
	workspace     connector.Workspace
	identity      connector.Identity
	secrets       []string
	startedAt     time.Time
	alihVersion   string
	consistency   Consistency
	records       []requestRecord
	rawResponses  int
	closed        bool
}

// Begin creates a private staging area. targetPath must not already exist.
// identity is the authenticated account performing the extraction; it is
// recorded so the archive states whose access it represents. secrets are held
// only in memory and used to reject or redact accidental credential exposure;
// they are never written to the snapshot.
// Options carries provenance that the caller, not this package, is the source
// of truth for. An empty AlihVersion falls back to the build's own identity so
// that a snapshot can never claim a version nobody built.
type Options struct {
	AlihVersion string
	// Consistency is what the connector claims about reading its source in one
	// pass. Alih cannot know this for a provider it was not told about, so the
	// zero value is the conservative claim: not atomic, described in
	// provider-neutral words. A connector that can prove otherwise says so.
	Consistency Consistency
}

// Consistency is a connector's claim about point-in-time consistency.
type Consistency struct {
	Atomic bool
	Note   string
}

// defaultConsistencyNote is what Alih records when a connector states nothing.
// It says only what is true of any API traversal, and names no provider.
const defaultConsistencyNote = "The source API does not provide an atomic snapshot; records may reflect different moments within the traversal."

func (c Consistency) resolve() Consistency {
	if strings.TrimSpace(c.Note) == "" {
		c.Note = defaultConsistencyNote
	}
	return c
}

// Begin starts a raw extraction session using the running build's identity.
func Begin(targetPath, connectorName string, workspace connector.Workspace, identity connector.Identity, secrets ...string) (*Session, error) {
	return BeginWithOptions(targetPath, connectorName, workspace, identity, Options{}, secrets...)
}

// BeginWithOptions starts a raw extraction session with injected provenance.
func BeginWithOptions(targetPath, connectorName string, workspace connector.Workspace, identity connector.Identity, options Options, secrets ...string) (*Session, error) {
	if strings.TrimSpace(targetPath) == "" {
		return nil, errors.New("raw snapshot output path is required")
	}
	if strings.TrimSpace(connectorName) == "" {
		return nil, errors.New("connector name is required")
	}
	absolute, err := filepath.Abs(targetPath)
	if err != nil {
		return nil, fmt.Errorf("resolve output path: %w", err)
	}
	if _, err := os.Lstat(absolute); err == nil {
		return nil, fmt.Errorf("output path already exists: %s", absolute)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect output path: %w", err)
	}
	parent := filepath.Dir(absolute)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("create output parent: %w", err)
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(absolute)+".partial-")
	if err != nil {
		return nil, fmt.Errorf("create extraction staging directory: %w", err)
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		return nil, fmt.Errorf("protect extraction staging directory: %w", err)
	}
	if err := os.Mkdir(filepath.Join(staging, "raw"), 0o700); err != nil {
		return nil, fmt.Errorf("create raw evidence directory: %w", err)
	}

	filteredSecrets := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			filteredSecrets = append(filteredSecrets, secret)
		}
	}
	session := &Session{
		targetPath: absolute, stagingPath: staging, connectorName: connectorName,
		workspace: workspace, identity: identity, secrets: filteredSecrets, startedAt: time.Now().UTC(),
		alihVersion: buildinfo.Resolve(options.AlihVersion),
		consistency: options.Consistency.resolve(),
	}
	if session.containsSecret([]byte(identity.ID + "\x00" + identity.Name)) {
		_ = os.RemoveAll(staging)
		return nil, errors.New("authenticated identity contains a configured credential")
	}
	if session.containsSecret([]byte(connectorName + "\x00" + workspace.ID + "\x00" + workspace.Name)) {
		_ = os.RemoveAll(staging)
		return nil, errors.New("source metadata contains a configured credential")
	}
	inProgress := session.runRecord("IN_PROGRESS", session.startedAt, requestSummary{})
	if err := writeJSONReplace(filepath.Join(staging, "run.json"), inProgress); err != nil {
		_ = os.RemoveAll(staging)
		return nil, fmt.Errorf("write extraction start record: %w", err)
	}
	return session, nil
}

// RecordResponse writes the exact successful provider response body under a
// deterministic sequence path and records its checksum in requests.json.
func (session *Session) RecordResponse(response connector.RawResponse) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return errors.New("raw snapshot session is already closed")
	}
	sequence := len(session.records) + 1
	base := requestRecord{
		Sequence: sequence, Operation: session.redact(response.Operation), Method: response.Method,
		Path: session.redact(response.Path), Query: session.redact(response.Query.Encode()),
		Attempt: response.Attempt, StatusCode: response.StatusCode,
	}
	if session.containsSecret(response.Body) {
		base.Outcome = "OMITTED_SECRET"
		base.Error = "response body contained a configured credential and was not persisted"
		session.records = append(session.records, base)
		return errors.New(base.Error)
	}
	relative := filepath.ToSlash(filepath.Join("raw", fmt.Sprintf("%06d.json", session.rawResponses+1)))
	absolute := filepath.Join(session.stagingPath, filepath.FromSlash(relative))
	if err := writeExclusive(absolute, response.Body); err != nil {
		base.Outcome = "WRITE_FAILED"
		base.Error = "failed to persist raw response"
		session.records = append(session.records, base)
		return err
	}
	digest := sha256.Sum256(response.Body)
	base.Outcome = "SUCCESS"
	base.RawPath = relative
	base.Bytes = len(response.Body)
	base.SHA256 = "sha256:" + hex.EncodeToString(digest[:])
	session.records = append(session.records, base)
	session.rawResponses++
	return nil
}

// RecordFailure records a sanitized failed request attempt without preserving
// its provider error body, which may echo credentials or sensitive content.
func (session *Session) RecordFailure(failure connector.RequestFailure) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return errors.New("raw snapshot session is already closed")
	}
	session.records = append(session.records, requestRecord{
		Sequence: len(session.records) + 1, Operation: session.redact(failure.Operation),
		Method: failure.Method, Path: session.redact(failure.Path), Query: session.redact(failure.Query.Encode()),
		Attempt: failure.Attempt, StatusCode: failure.StatusCode, Outcome: "FAILED_ATTEMPT",
		Retrying: failure.Retrying, Error: session.redact(failure.Error),
	})
	return nil
}

// Complete validates and writes the source-level logical inventory, then makes
// the staging directory visible at the requested target in one rename.
func (session *Session) Complete(result connector.ExtractionResult) (Summary, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return Summary{}, errors.New("raw snapshot session is already closed")
	}
	if result.Workspace.ID != session.workspace.ID {
		return Summary{}, errors.New("extraction result Workspace does not match snapshot source")
	}
	objects := append([]connector.SourceObject(nil), result.SourceObjects...)
	sortSourceObjects(objects)
	for _, object := range objects {
		if session.containsSecret([]byte(object.Type + "\x00" + object.ID + "\x00" + object.ParentType + "\x00" + object.ParentID)) {
			return Summary{}, errors.New("source object index contains a configured credential")
		}
	}
	if err := validateSourceObjects(objects); err != nil {
		return Summary{}, fmt.Errorf("validate source object index: %w", err)
	}
	for _, capability := range result.Capabilities {
		serialized := strings.Join([]string{
			string(capability.ID), capability.Name, string(capability.Requirement),
			string(capability.Implementation), string(capability.Availability),
			string(capability.State), capability.Note,
		}, "\x00")
		if session.containsSecret([]byte(serialized)) {
			return Summary{}, errors.New("capability contract contains a configured credential")
		}
	}
	if err := connector.ValidateCapabilities(result.CapabilitySchemaVersion, result.Capabilities); err != nil {
		return Summary{}, fmt.Errorf("validate capability contract: %w", err)
	}
	capabilityDigest, err := connector.CapabilityDigest(result.CapabilitySchemaVersion, session.connectorName, result.Capabilities)
	if err != nil {
		return Summary{}, fmt.Errorf("digest capability contract: %w", err)
	}
	digest, err := logicalDigest(schemaVersion, session.connectorName, session.workspace.ID, result.Inventory, objects)
	if err != nil {
		return Summary{}, err
	}
	inventory := inventoryRecord{
		SchemaVersion: schemaVersion, Kind: "raw_source_inventory", Connector: session.connectorName,
		WorkspaceID: session.workspace.ID, Counts: result.Inventory,
		CapabilitySchemaVersion: result.CapabilitySchemaVersion,
		Capabilities:            connector.CanonicalCapabilities(result.CapabilitySchemaVersion, result.Capabilities),
		CapabilityDigest:        capabilityDigest,
		SourceObjects:           objects, LogicalDigest: digest,
	}
	if result.Assessment.SchemaVersion != 0 {
		if err := connector.ValidateOperationalAssessment(result.Assessment); err != nil {
			return Summary{}, fmt.Errorf("validate operational assessment: %w", err)
		}
		assessment := result.Assessment
		connector.CanonicalizeOperationalAssessment(&assessment)
		encodedAssessment, err := json.Marshal(assessment)
		if err != nil {
			return Summary{}, fmt.Errorf("encode operational assessment: %w", err)
		}
		if session.containsSecret(encodedAssessment) {
			return Summary{}, errors.New("operational assessment contains a configured credential")
		}
		inventory.OperationalAssessment = &assessment
	}
	finishedAt := time.Now().UTC()
	if err := writeJSONReplace(filepath.Join(session.stagingPath, "inventory.json"), inventory); err != nil {
		return Summary{}, fmt.Errorf("write source inventory: %w", err)
	}
	if err := writeJSONReplace(filepath.Join(session.stagingPath, "requests.json"), session.records); err != nil {
		return Summary{}, fmt.Errorf("write request ledger: %w", err)
	}
	summary := summarize(session.records)
	run := session.runRecord("COMPLETE", finishedAt, summary)
	run.LogicalDigest = digest
	run.CapabilityDigest = capabilityDigest
	if err := writeJSONReplace(filepath.Join(session.stagingPath, "run.json"), run); err != nil {
		return Summary{}, fmt.Errorf("write extraction run record: %w", err)
	}
	if err := os.Rename(session.stagingPath, session.targetPath); err != nil {
		return Summary{}, fmt.Errorf("finalize raw snapshot: %w", err)
	}
	session.closed = true
	return Summary{
		Path: session.targetPath, LogicalDigest: digest, Requests: summary.Attempts,
		RawResponses: summary.SuccessfulResponses, RetriedAttempts: summary.RetriedAttempts,
	}, nil
}

// Fail preserves partial evidence at targetPath + ".failed" and marks it
// FAILED. It never turns an interrupted or incomplete traversal into success.
func (session *Session) Fail(reason error) (string, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return "", errors.New("raw snapshot session is already closed")
	}
	finishedAt := time.Now().UTC()
	summary := summarize(session.records)
	if err := writeJSONReplace(filepath.Join(session.stagingPath, "requests.json"), session.records); err != nil {
		return session.stagingPath, fmt.Errorf("write failed request ledger: %w", err)
	}
	run := session.runRecord("FAILED", finishedAt, summary)
	if reason != nil {
		run.Failure = session.redact(reason.Error())
	}
	if err := writeJSONReplace(filepath.Join(session.stagingPath, "run.json"), run); err != nil {
		return session.stagingPath, fmt.Errorf("write failed extraction run record: %w", err)
	}
	failedPath := session.targetPath + ".failed"
	if _, err := os.Lstat(failedPath); err == nil {
		return session.stagingPath, fmt.Errorf("failed evidence path already exists: %s", failedPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return session.stagingPath, fmt.Errorf("inspect failed evidence path: %w", err)
	}
	if err := os.Rename(session.stagingPath, failedPath); err != nil {
		return session.stagingPath, fmt.Errorf("preserve failed extraction evidence: %w", err)
	}
	session.closed = true
	return failedPath, nil
}

func (session *Session) runRecord(status string, finishedAt time.Time, requests requestSummary) runRecord {
	var extractedBy *identityRecord
	if strings.TrimSpace(session.identity.ID) != "" {
		extractedBy = &identityRecord{
			ID: session.redact(session.identity.ID), Name: session.redact(session.identity.Name),
		}
	}
	return runRecord{
		SchemaVersion: schemaVersion, Kind: "raw_extraction_run", Status: status,
		AlihVersion: session.alihVersion,
		Connector:   session.connectorName,
		ExtractedBy: extractedBy,
		Source:      sourceDescriptor{WorkspaceID: session.redact(session.workspace.ID), WorkspaceName: session.redact(session.workspace.Name)},
		StartedAt:   session.startedAt, FinishedAt: finishedAt, Requests: requests,
		Consistency: consistencyRecord{
			Atomic: session.consistency.Atomic,
			Note:   session.consistency.Note,
		},
	}
}

func summarize(records []requestRecord) requestSummary {
	var summary requestSummary
	summary.Attempts = len(records)
	for _, record := range records {
		switch record.Outcome {
		case "SUCCESS":
			summary.SuccessfulResponses++
		case "FAILED_ATTEMPT":
			summary.FailedAttempts++
			if record.Retrying {
				summary.RetriedAttempts++
			}
		default:
			summary.FailedAttempts++
		}
	}
	return summary
}

// logicalDigest binds the connector, the workspace, the counts, and the source
// index into one identity.
//
// The counts are encoded in the vocabulary the snapshot was recorded in, not
// the vocabulary the running build prefers. A snapshot sealed by an earlier
// release must reproduce its own digest exactly, or every archive built from it
// would stop verifying the day Alih changed a field name.
func logicalDigest(snapshotSchema int, connectorName, workspaceID string, counts connector.Inventory, objects []connector.SourceObject) (string, error) {
	encoded := any(counts)
	if snapshotSchema < 2 {
		encoded = counts.Legacy()
	}
	payload, err := json.Marshal(digestInput{
		Connector: connectorName, WorkspaceID: workspaceID, Counts: encoded, SourceObjects: objects,
	})
	if err != nil {
		return "", fmt.Errorf("encode logical inventory: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func sortSourceObjects(objects []connector.SourceObject) {
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
}

func validateSourceObjects(objects []connector.SourceObject) error {
	seen := make(map[string]struct{}, len(objects))
	for index, object := range objects {
		if strings.TrimSpace(object.Type) == "" || strings.TrimSpace(object.ID) == "" {
			return fmt.Errorf("object %d has an empty source type or id", index)
		}
		key := object.Type + "\x00" + object.ID
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate source object %q id %q", object.Type, object.ID)
		}
		seen[key] = struct{}{}
	}
	for _, object := range objects {
		if object.ParentType == "" && object.ParentID == "" {
			continue
		}
		if object.ParentType == "" || object.ParentID == "" {
			return fmt.Errorf("source object %q id %q has an incomplete parent reference", object.Type, object.ID)
		}
		if _, found := seen[object.ParentType+"\x00"+object.ParentID]; !found {
			return fmt.Errorf("source object %q id %q references missing parent %q id %q", object.Type, object.ID, object.ParentType, object.ParentID)
		}
	}
	return nil
}

func writeExclusive(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func writeJSONReplace(path string, value any) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(path, content, 0o600)
}

func (session *Session) containsSecret(content []byte) bool {
	for _, secret := range session.secrets {
		if bytes.Contains(content, []byte(secret)) {
			return true
		}
	}
	return false
}

func (session *Session) redact(value string) string {
	for _, secret := range session.secrets {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	value = strings.Map(func(character rune) rune {
		if character < 0x20 && character != '\t' {
			return ' '
		}
		return character
	}, value)
	return strings.TrimSpace(value)
}

var _ connector.RawEvidenceSink = (*Session)(nil)
