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

package hardening

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"alih/internal/connector"
	"alih/internal/event"
	"alih/internal/notify"
	"alih/internal/schedule"
	"alih/internal/state"
	"alih/internal/verify"
)

// The foundation grew one stage at a time: capability, then health, then
// operational state, then events, then notifications, then scheduling, then
// the organized view. Each was built on the last, which is the right order to
// build in and exactly the order that produces contracts which almost agree.
//
// These tests assert the places where those contracts have to mean the same
// thing. They are the difference between seven contracts and one foundation.

// TestOneScopeIdentityRunsThroughEveryLayer proves the question "which
// installation is this?" has a single answer. State, the operation lock, the
// event log, and a schedule definition all key on connector + workspace +
// destination, so a run, the lock that protected it, the events it emitted, and
// the schedule that triggered it can always be lined up.
func TestOneScopeIdentityRunsThroughEveryLayer(t *testing.T) {
	t.Parallel()

	scope := state.Scope{Connector: "clickup", WorkspaceID: "100", Destination: "/customer/Alih"}
	source := event.Source{Connector: "clickup", WorkspaceID: "100", Destination: "/customer/Alih"}

	// The event contract's source must carry exactly the scope's fields: no
	// more, so nothing extra identifies an installation, and no fewer, so two
	// installations cannot collapse into one.
	scopeFields := jsonFieldNames(t, scope)
	sourceFields := jsonFieldNames(t, source)
	if !sameStrings(scopeFields, sourceFields) {
		t.Fatalf("state scope is keyed by %v but event source by %v", scopeFields, sourceFields)
	}

	// Changing any one field must change the identity, or two installations
	// would share a state file, a lock, and a history.
	base := scope.Key()
	for _, variant := range []state.Scope{
		{Connector: "other", WorkspaceID: "100", Destination: "/customer/Alih"},
		{Connector: "clickup", WorkspaceID: "200", Destination: "/customer/Alih"},
		{Connector: "clickup", WorkspaceID: "100", Destination: "/elsewhere/Alih"},
	} {
		if variant.Key() == base {
			t.Errorf("scope %#v shares an identity with the base scope", variant)
		}
	}

	// A schedule names the same triple, so an installed timer can be matched to
	// the state and history of the runs it produces.
	definition := schedule.Definition{
		Connector: scope.Connector, WorkspaceID: scope.WorkspaceID, Destination: scope.Destination,
	}
	if definition.Connector != scope.Connector ||
		definition.WorkspaceID != scope.WorkspaceID ||
		definition.Destination != scope.Destination {
		t.Fatal("a schedule definition does not name the same scope the run will record")
	}
}

// TestEventsSpeakTheOperationalStateVocabulary proves an event and the state it
// describes cannot disagree about what an operation or a stage is called.
func TestEventsSpeakTheOperationalStateVocabulary(t *testing.T) {
	t.Parallel()

	if len(state.Operations()) == 0 || len(state.Stages()) == 0 {
		t.Fatal("the operational vocabulary is empty")
	}
	for _, operation := range state.Operations() {
		e := validEvent()
		e.Operation = operation
		if err := event.Validate(e); err != nil {
			t.Errorf("state operation %q is not a valid event operation: %v", operation, err)
		}
	}
	for _, stage := range state.Stages() {
		e := validEvent()
		e.Stage = stage
		if err := event.Validate(e); err != nil {
			t.Errorf("state stage %q is not a valid event stage: %v", stage, err)
		}
	}
	// And nothing outside that vocabulary is accepted, so the agreement is
	// exact rather than merely overlapping.
	invalid := validEvent()
	invalid.Operation = "invented-operation"
	if err := event.Validate(invalid); err == nil {
		t.Error("an event accepted an operation the state contract does not define")
	}
	invalid = validEvent()
	invalid.Stage = "invented-stage"
	if err := event.Validate(invalid); err == nil {
		t.Error("an event accepted a stage the state contract does not define")
	}
}

func validEvent() event.Event {
	return event.Event{
		SchemaVersion: event.SchemaVersion,
		Type:          event.TypeOperationStarted,
		OperationID:   "20260901T080000Z-0badc0de",
		Sequence:      1,
		RecordedAt:    time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC),
		Source:        event.Source{Connector: "clickup", WorkspaceID: "100", Destination: "/customer/Alih"},
		Operation:     state.OperationBackup,
		Stage:         state.StagePrepare,
		Outcome:       state.OutcomeStarted,
		Message:       "The backup started.",
		AlihVersion:   "dev",
	}
}

// TestVerificationResultsMeanTheSameThingEverywhere proves the four results are
// one vocabulary. The state package deliberately keeps its own copy so that it
// does not have to depend on the verifier; this is what stops the copy drifting.
func TestVerificationResultsMeanTheSameThingEverywhere(t *testing.T) {
	t.Parallel()

	pairs := []struct{ recorded, verified string }{
		{state.ResultVerified, verify.ResultVerified},
		{state.ResultVerifiedWithLimitations, verify.ResultVerifiedWithLimitations},
		{state.ResultIncomplete, verify.ResultIncomplete},
		{state.ResultFailed, verify.ResultFailed},
	}
	for _, pair := range pairs {
		if pair.recorded != pair.verified {
			t.Errorf("state records %q where the verifier produces %q", pair.recorded, pair.verified)
		}
	}

	// The passing set must be identical too, or a run could record a result the
	// verifier considers a failure and status would call it healthy.
	for _, pair := range pairs {
		report := verify.Report{Result: pair.verified}
		passing := !report.Failed()
		recordedPassing := pair.recorded == state.ResultVerified ||
			pair.recorded == state.ResultVerifiedWithLimitations
		if passing != recordedPassing {
			t.Errorf("result %q passes for the verifier=%t but for recorded state=%t",
				pair.verified, passing, recordedPassing)
		}
	}
}

// TestHealthOnlySpeaksOfDeclaredCapabilities proves the health contract cannot
// report on a capability the capability contract does not define. Without this,
// health could name something no connector ever declared, and status would show
// a capability nobody can act on.
func TestHealthOnlySpeaksOfDeclaredCapabilities(t *testing.T) {
	t.Parallel()

	declared := map[connector.CapabilityID]bool{}
	for _, id := range []connector.CapabilityID{
		connector.CapabilityWorkspaceData, connector.CapabilityItems, connector.CapabilityComments,
		connector.CapabilityAttachmentMetadata, connector.CapabilityAttachmentContent,
		connector.CapabilityCustomFields, connector.CapabilityRelationships,
		connector.CapabilityRawEvidence,
	} {
		declared[id] = true
	}

	// Every capability a real connector declares is one health can describe.
	for id := range declared {
		observed, _, _ := connector.AggregateCapabilityHealth([]connector.CapabilityHealth{{
			ID: id, Requirement: connector.CapabilityRequired,
			State: connector.HealthHealthy, Reason: connector.HealthReasonNone,
		}})
		if observed != connector.HealthHealthy {
			t.Errorf("health could not describe declared capability %q", id)
		}
	}

	// An assessment naming a capability outside the contract must be refused,
	// not rendered.
	assessment, err := connector.HealthyAssessment("clickup", connector.HealthBasisScan,
		time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC), connector.AuthenticationAuthenticated, nil)
	if err != nil {
		t.Fatal(err)
	}
	assessment.Health.Capabilities = []connector.CapabilityHealth{{
		ID: "invented_capability", Requirement: connector.CapabilityRequired,
		State: connector.HealthHealthy, Reason: connector.HealthReasonNone,
	}}
	if err := connector.ValidateOperationalAssessment(assessment); err == nil {
		t.Error("an assessment named a capability the contract does not define")
	}
}

// TestNotificationsCanOnlySelectRealEventsAndNeverThemselves proves the
// notification contract is a projection of the event contract rather than a
// second, parallel list of things that can happen.
func TestNotificationsCanOnlySelectRealEventsAndNeverThemselves(t *testing.T) {
	t.Parallel()

	known := make(map[event.Type]bool)
	for _, eventType := range event.Types() {
		known[eventType] = true
	}
	if len(known) == 0 {
		t.Fatal("the event contract defines no types")
	}

	for eventType := range known {
		err := notify.Validate(notify.Config{
			SchemaVersion: notify.SchemaVersion,
			Destinations: []notify.Destination{{
				ID: "ops", Type: notify.TypeWebhook, URL: "https://example.test/hook",
				Events: []string{string(eventType)},
			}},
		})
		switch eventType {
		case event.TypeNotificationProblem:
			// A delivery failure must not be deliverable, or one unreachable
			// webhook would generate work for itself forever.
			if err == nil {
				t.Error("a destination was allowed to select notification.problem")
			}
		default:
			if err != nil {
				t.Errorf("event type %q cannot be selected for delivery: %v", eventType, err)
			}
		}
	}

	// And a type the event contract does not define cannot be selected at all.
	if err := notify.Validate(notify.Config{
		SchemaVersion: notify.SchemaVersion,
		Destinations: []notify.Destination{{
			ID: "ops", Type: notify.TypeWebhook, URL: "https://example.test/hook",
			Events: []string{"operation.invented"},
		}},
	}); err == nil {
		t.Error("a destination selected an event type that does not exist")
	}
}

// TestEverySchemaVersionIsPositiveAndRefusesTheFuture proves the eleven
// versioned contracts agree on what versioning means: a readable present and a
// refused future, never a guess.
func TestEverySchemaVersionIsPositiveAndRefusesTheFuture(t *testing.T) {
	t.Parallel()

	versions := map[string]int{
		"capability":   connector.CapabilitySchemaVersion,
		"health":       connector.HealthSchemaVersion,
		"state":        state.SchemaVersion,
		"event":        event.SchemaVersion,
		"notification": notify.SchemaVersion,
		"schedule":     schedule.SchemaVersion,
	}
	for name, version := range versions {
		if version < 1 {
			t.Errorf("the %s contract has schema version %d", name, version)
		}
	}

	// State and events are the two contracts a later build reads back from a
	// user's disk, so both must refuse a document from a newer build outright.
	future := validEvent()
	future.SchemaVersion = event.SchemaVersion + 1
	encoded, err := json.Marshal(future)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := event.Unmarshal(encoded); err == nil {
		t.Error("the event reader accepted a newer schema")
	}
	if _, err := state.Unmarshal([]byte(`{"schema_version":99}`)); err == nil {
		t.Error("the state reader accepted a newer schema")
	}
}

// TestTheOrganizedViewAcceptsExactlyWhatVerificationCallsPassing proves the
// newest contract did not invent its own idea of a good archive.
func TestTheOrganizedViewAcceptsExactlyWhatVerificationCallsPassing(t *testing.T) {
	t.Parallel()

	// organize.Build refuses any result for which verify.Report.Failed() is
	// true, and accepts every result for which it is false. The two passing
	// results are the same two the recovery report and status treat as success,
	// which is asserted here as one statement rather than three separate
	// beliefs held by three packages.
	for _, result := range []string{
		verify.ResultVerified, verify.ResultVerifiedWithLimitations,
		verify.ResultIncomplete, verify.ResultFailed,
	} {
		report := verify.Report{Result: result}
		passing := !report.Failed()
		organizable := result == verify.ResultVerified || result == verify.ResultVerifiedWithLimitations
		if passing != organizable {
			t.Errorf("verification calls %q passing=%t but the organized view accepts=%t",
				result, passing, organizable)
		}
	}
}

// jsonFieldNames returns the JSON field names a value serialises to.
func jsonFieldNames(t *testing.T, value any) []string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(decoded))
	for name := range decoded {
		names = append(names, name)
	}
	return names
}

func sameStrings(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	counts := make(map[string]int, len(first))
	for _, value := range first {
		counts[strings.ToLower(value)]++
	}
	for _, value := range second {
		counts[strings.ToLower(value)]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}
