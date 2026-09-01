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

// Package event is Alih's local record of what its own operations did.
//
// An event is history, never evidence and never authority: the operational
// state layer remains the answer to "what is true now", and losing the event
// log cannot make status wrong. Events exist so that a consumer can react to a
// transition without parsing command output.
//
// Every event describes a transition Alih actually performs. There is no
// general event bus, no user-defined type, no provider-specific name, and no
// free-form payload: metadata keys are allowlisted per type, so a credential,
// a response body, or a raw error object has no field to travel in.
package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"alih/internal/state"
)

// SchemaVersion identifies the event envelope contract. Version 2 adds
// schedule correlation and explicit operation.skipped. Version 1 remains
// readable and is lifted in memory without inventing schedule fields.
const SchemaVersion = 2

// Type is the stable machine identity of a transition. Consumers switch on
// this value; the message is for people and must never drive behaviour.
type Type string

const (
	// TypeOperationStarted is emitted once an operation has a destination and
	// is about to do work. It never means anything about the source yet.
	TypeOperationStarted Type = "operation.started"
	// TypeOperationCompleted is emitted only after everything that defines the
	// operation's success has happened. For a backup that means after
	// verification, reporting, and atomic publication of the bundle.
	TypeOperationCompleted Type = "operation.completed"
	// TypeOperationFailed names the stage that failed and the stable reason.
	TypeOperationFailed Type = "operation.failed"
	// TypeOperationSkipped means Alih deliberately did not enter a pipeline,
	// currently because the same operation scope was already locked.
	TypeOperationSkipped Type = "operation.skipped"
	// TypeVerificationRecorded reports an independent verification result,
	// passing or failing, together with the identity of the archive it judged.
	TypeVerificationRecorded Type = "verification.recorded"
	// TypeConnectorUnhealthy is emitted when an observation established that
	// the connector was degraded or unavailable.
	TypeConnectorUnhealthy Type = "connector.unhealthy"
	// TypeAuthenticationProblem separates a credential problem from provider
	// health, exactly as the health model does.
	TypeAuthenticationProblem Type = "authentication.problem"
	// TypeNotificationProblem reports that an explicitly configured
	// destination could not accept an event. It is recorded locally and is
	// never itself eligible for notification, preventing recursive delivery.
	TypeNotificationProblem Type = "notification.problem"
)

// Outcomes are reused from the operational state vocabulary so that an event
// and the state it describes can never disagree about what happened.
const (
	OutcomeStarted   = state.OutcomeStarted
	OutcomeSucceeded = state.OutcomeSucceeded
	OutcomeFailed    = state.OutcomeFailed
	OutcomeSkipped   = state.OutcomeSkipped
)

const (
	maxTextBytes     = 512
	maxOperationID   = 64
	maxPathBytes     = 4096
	maxMetadataItems = 8
)

// Source is the scope the event belongs to. It is the same identity the
// operational state layer uses, so an event and a state record can be
// correlated without inference.
type Source struct {
	Connector   string `json:"connector"`
	WorkspaceID string `json:"workspace_id"`
	Destination string `json:"destination"`
}

// Event is one durable record of one transition.
//
// Ordering is by (OperationID, Sequence), never by RecordedAt: a clock that
// moves backwards must not be able to reorder history. RecordedAt is kept as
// the observation it is.
type Event struct {
	SchemaVersion int               `json:"schema_version"`
	Type          Type              `json:"type"`
	OperationID   string            `json:"operation_id"`
	Sequence      int               `json:"sequence"`
	RecordedAt    time.Time         `json:"recorded_at"`
	Source        Source            `json:"source"`
	Operation     string            `json:"operation"`
	Stage         string            `json:"stage,omitempty"`
	Outcome       string            `json:"outcome,omitempty"`
	Message       string            `json:"message"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	AlihVersion   string            `json:"alih_version,omitempty"`
}

// metadataAllowlist is the complete set of metadata a type may carry. A key
// that is not listed here cannot be recorded, which is what keeps credentials,
// response bodies, and raw error objects out of the log by construction.
var metadataAllowlist = map[Type]map[string]struct{}{
	TypeOperationStarted: {"schedule_id": {}},
	TypeOperationCompleted: {
		"archive_path": {}, "report_path": {}, "schedule_id": {},
	},
	TypeOperationFailed: {
		"reason": {}, "failed_path": {}, "schedule_id": {},
	},
	TypeOperationSkipped: {
		"reason": {}, "schedule_id": {},
	},
	TypeVerificationRecorded: {
		"result": {}, "archive_path": {}, "manifest_checksum": {}, "archive_status": {}, "schedule_id": {},
	},
	TypeConnectorUnhealthy: {
		"health_state": {}, "reason": {}, "schedule_id": {},
	},
	TypeAuthenticationProblem: {
		"authentication_state": {}, "reason": {}, "schedule_id": {},
	},
	TypeNotificationProblem: {
		"destination_id": {}, "event_type": {}, "reason": {}, "retryable": {},
		"idempotency_key": {}, "attempts": {}, "schedule_id": {},
	},
}

// requiredMetadata names what a type must carry to be worth recording at all.
var requiredMetadata = map[Type][]string{
	TypeOperationFailed:       {"reason"},
	TypeOperationSkipped:      {"reason"},
	TypeVerificationRecorded:  {"result", "archive_path", "manifest_checksum"},
	TypeConnectorUnhealthy:    {"health_state", "reason"},
	TypeAuthenticationProblem: {"authentication_state", "reason"},
	TypeNotificationProblem:   {"destination_id", "event_type", "reason", "idempotency_key"},
}

// expectedOutcome fixes the relationship between a type and its outcome, so an
// event can never claim a completed operation with a failed outcome.
var expectedOutcome = map[Type][]string{
	TypeOperationStarted:     {OutcomeStarted},
	TypeOperationCompleted:   {OutcomeSucceeded},
	TypeOperationFailed:      {OutcomeFailed},
	TypeOperationSkipped:     {OutcomeSkipped},
	TypeVerificationRecorded: {OutcomeSucceeded, OutcomeFailed},
}

// pathMetadata names the keys whose values are local filesystem paths, which
// are allowed to be longer than ordinary message text.
var pathMetadata = map[string]struct{}{
	"archive_path": {}, "report_path": {}, "failed_path": {},
}

// KnownType reports whether a name is one of Alih's own event types. Consumers
// use it to reject a selection that would silently never match anything.
func KnownType(candidate Type) bool {
	_, known := metadataAllowlist[candidate]
	return known
}

// Types returns every event type, in a stable order, for help text and
// configuration validation messages.
func Types() []Type {
	names := make([]Type, 0, len(metadataAllowlist))
	for name := range metadataAllowlist {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

// Canonicalize puts an event into its stored form.
func Canonicalize(e *Event) {
	if e == nil {
		return
	}
	e.SchemaVersion = SchemaVersion
	e.RecordedAt = e.RecordedAt.UTC()
	e.Source.Connector = strings.TrimSpace(e.Source.Connector)
	e.Source.WorkspaceID = strings.TrimSpace(e.Source.WorkspaceID)
	if destination := strings.TrimSpace(e.Source.Destination); destination != "" {
		e.Source.Destination = filepath.Clean(destination)
	}
	if len(e.Metadata) == 0 {
		e.Metadata = nil
	}
}

// Validate rejects an event that is ambiguous, unsafe, internally
// contradictory, or that carries anything outside its allowlist.
func Validate(e Event) error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported event schema version %d", e.SchemaVersion)
	}
	if _, known := metadataAllowlist[e.Type]; !known {
		return fmt.Errorf("unknown event type %q", e.Type)
	}
	if err := validateText("operation id", e.OperationID, maxOperationID, false); err != nil {
		return err
	}
	if e.Sequence < 1 {
		return fmt.Errorf("event sequence %d is not a position in its operation", e.Sequence)
	}
	if e.RecordedAt.IsZero() {
		return errors.New("event records no time")
	}
	if err := validateText("connector", e.Source.Connector, maxTextBytes, false); err != nil {
		return err
	}
	if err := validateText("workspace id", e.Source.WorkspaceID, maxTextBytes, false); err != nil {
		return err
	}
	if err := validateText("destination", e.Source.Destination, maxPathBytes, false); err != nil {
		return err
	}
	if !filepath.IsAbs(e.Source.Destination) {
		return errors.New("event destination must be an absolute path")
	}
	if !validOperation(e.Operation) {
		return fmt.Errorf("unknown operation %q", e.Operation)
	}
	if e.Stage != "" && !validStage(e.Stage) {
		return fmt.Errorf("unknown stage %q", e.Stage)
	}
	if err := validateOutcome(e); err != nil {
		return err
	}
	if err := validateText("message", e.Message, maxTextBytes, false); err != nil {
		return err
	}
	if err := validateText("alih version", e.AlihVersion, maxTextBytes, true); err != nil {
		return err
	}
	return validateMetadata(e)
}

func validateOutcome(e Event) error {
	expected, requiresOutcome := expectedOutcome[e.Type]
	if !requiresOutcome {
		if e.Outcome != "" {
			return fmt.Errorf("event type %q does not describe an operation outcome", e.Type)
		}
		return nil
	}
	for _, candidate := range expected {
		if e.Outcome == candidate {
			return nil
		}
	}
	return fmt.Errorf("event type %q cannot carry outcome %q", e.Type, e.Outcome)
}

func validateMetadata(e Event) error {
	allowed := metadataAllowlist[e.Type]
	if len(e.Metadata) > maxMetadataItems {
		return fmt.Errorf("event carries more than %d metadata entries", maxMetadataItems)
	}
	for _, key := range sortedKeys(e.Metadata) {
		if _, permitted := allowed[key]; !permitted {
			return fmt.Errorf("event type %q does not allow metadata %q", e.Type, key)
		}
		limit := maxTextBytes
		if _, isPath := pathMetadata[key]; isPath {
			limit = maxPathBytes
		}
		if err := validateText("metadata "+key, e.Metadata[key], limit, false); err != nil {
			return err
		}
	}
	for _, key := range requiredMetadata[e.Type] {
		if strings.TrimSpace(e.Metadata[key]) == "" {
			return fmt.Errorf("event type %q requires metadata %q", e.Type, key)
		}
	}
	return nil
}

// Marshal renders one event as the single canonical JSON line it is stored as.
func Marshal(e Event) ([]byte, error) {
	Canonicalize(&e)
	if err := Validate(e); err != nil {
		return nil, err
	}
	content, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if bytesContainNewline(content) {
		// A newline inside a record would split one event into two lines and
		// silently corrupt every later read.
		return nil, errors.New("encoded event contains a line break")
	}
	return append(content, '\n'), nil
}

// Unmarshal parses one stored line strictly. Unknown fields and unsupported
// versions are refused rather than guessed at.
func Unmarshal(line []byte) (Event, error) {
	var probe struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return Event{}, fmt.Errorf("event is not valid JSON: %w", err)
	}
	if probe.SchemaVersion > SchemaVersion {
		return Event{}, fmt.Errorf("event schema version %d was written by a newer Alih", probe.SchemaVersion)
	}
	if probe.SchemaVersion < 1 {
		return Event{}, fmt.Errorf("unsupported event schema version %d", probe.SchemaVersion)
	}
	decoder := json.NewDecoder(strings.NewReader(string(line)))
	decoder.DisallowUnknownFields()
	var e Event
	if err := decoder.Decode(&e); err != nil {
		return Event{}, fmt.Errorf("event could not be decoded: %w", err)
	}
	if e.SchemaVersion == 1 {
		e.SchemaVersion = SchemaVersion
	}
	Canonicalize(&e)
	if err := Validate(e); err != nil {
		return Event{}, err
	}
	return e, nil
}

// Order sorts events the way consumers must read them: by operation, then by
// position within that operation. The wall clock is never used for ordering.
func Order(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].OperationID != events[j].OperationID {
			return events[i].OperationID < events[j].OperationID
		}
		return events[i].Sequence < events[j].Sequence
	})
}

// Deduplicate removes repeats of the same position in the same operation, which
// is what a replayed or twice-appended write looks like.
func Deduplicate(events []Event) []Event {
	type position struct {
		operationID string
		sequence    int
	}
	seen := make(map[position]struct{}, len(events))
	unique := make([]Event, 0, len(events))
	for _, e := range events {
		key := position{operationID: e.OperationID, sequence: e.Sequence}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, e)
	}
	return unique
}

func sortedKeys(metadata map[string]string) []string {
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func bytesContainNewline(content []byte) bool {
	for _, character := range content {
		if character == '\n' || character == '\r' {
			return true
		}
	}
	return false
}

func validateText(label, value string, limit int, allowEmpty bool) error {
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is empty", label)
	}
	if len(value) > limit {
		return fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", label)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

// The operation and stage vocabularies belong to the operational state
// contract. Reading them from there rather than repeating them here means an
// event and the state it describes can never disagree about a name.
func validOperation(operation string) bool { return state.ValidOperation(operation) }

func validStage(stage string) bool { return state.ValidStage(stage) }
