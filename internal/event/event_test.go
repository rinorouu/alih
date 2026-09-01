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

package event

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"alih/internal/state"
)

var testTime = time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)

func testSource() Source {
	return Source{Connector: "clickup", WorkspaceID: "100", Destination: "/home/tester/Alih"}
}

func startedEvent() Event {
	return Event{
		SchemaVersion: SchemaVersion, Type: TypeOperationStarted,
		OperationID: "20260901T080000Z-0badc0de", Sequence: 1, RecordedAt: testTime,
		Source: testSource(), Operation: state.OperationBackup, Stage: state.StagePrepare,
		Outcome: OutcomeStarted, Message: "The backup started.", AlihVersion: "dev",
	}
}

func completedEvent() Event {
	e := startedEvent()
	e.Type, e.Sequence, e.Stage, e.Outcome = TypeOperationCompleted, 3, state.StageFinalize, OutcomeSucceeded
	e.Message = "The backup completed and was published."
	e.Metadata = map[string]string{
		"archive_path": "/home/tester/Alih/ws/2026-09-01T080000Z/archive",
		"report_path":  "/home/tester/Alih/ws/2026-09-01T080000Z/recovery-report.html",
	}
	return e
}

func TestMarshalIsOneCanonicalLineAndRoundTrips(t *testing.T) {
	t.Parallel()
	line, err := Marshal(completedEvent())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Count(line, []byte("\n")) != 1 || !bytes.HasSuffix(line, []byte("\n")) {
		t.Fatalf("an event is not exactly one line: %q", line)
	}
	decoded, err := Unmarshal(bytes.TrimSuffix(line, []byte("\n")))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	again, err := Marshal(decoded)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if !bytes.Equal(line, again) {
		t.Fatalf("canonical form is not stable:\n%s\n%s", line, again)
	}
}

func TestUnmarshalRefusesUnknownFieldsAndNewerSchemas(t *testing.T) {
	t.Parallel()
	line, err := Marshal(startedEvent())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	valid := strings.TrimSuffix(string(line), "\n")

	unknown := strings.Replace(valid, `"sequence":1`, `"sequence":1,"surprise":true`, 1)
	if _, err := Unmarshal([]byte(unknown)); err == nil {
		t.Fatal("an unknown field was accepted")
	}
	future := strings.Replace(valid, `"schema_version":2`, `"schema_version":3`, 1)
	if _, err := Unmarshal([]byte(future)); err == nil || !strings.Contains(err.Error(), "newer Alih") {
		t.Fatalf("future schema error = %v", err)
	}
	if _, err := Unmarshal([]byte(`{"schema_version":2`)); err == nil {
		t.Fatal("a truncated line was accepted")
	}
}

func TestVersionOneEventMigratesWithoutInventingScheduleEvidence(t *testing.T) {
	t.Parallel()
	line, err := Marshal(startedEvent())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	legacy := strings.Replace(string(line), `"schema_version":2`, `"schema_version":1`, 1)
	migrated, err := Unmarshal([]byte(strings.TrimSpace(legacy)))
	if err != nil {
		t.Fatalf("read event v1: %v", err)
	}
	if migrated.SchemaVersion != SchemaVersion || migrated.Metadata["schedule_id"] != "" {
		t.Fatalf("migrated event = %#v", migrated)
	}
}

func TestValidateRefusesContradictoryOrUnsafeEvents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Event)
		want   string
	}{
		{"unknown type", func(e *Event) { e.Type = "backup.almost" }, "unknown event type"},
		{"no position", func(e *Event) { e.Sequence = 0 }, "not a position"},
		{"no time", func(e *Event) { e.RecordedAt = time.Time{} }, "records no time"},
		{"relative destination", func(e *Event) { e.Source.Destination = "Alih" }, "absolute path"},
		{"unknown operation", func(e *Event) { e.Operation = "restore" }, "unknown operation"},
		{"unknown stage", func(e *Event) { e.Stage = "publish" }, "unknown stage"},
		{"control character", func(e *Event) { e.Message = "done\x07" }, "control character"},
		{"invalid utf-8", func(e *Event) { e.Message = string([]byte{0xff, 0xfe}) }, "valid UTF-8"},
		{"completed cannot have failed", func(e *Event) {
			e.Type, e.Outcome = TypeOperationCompleted, OutcomeFailed
		}, "cannot carry outcome"},
		{"started cannot claim success", func(e *Event) { e.Outcome = OutcomeSucceeded }, "cannot carry outcome"},
		{"health events carry no operation outcome", func(e *Event) {
			e.Type = TypeConnectorUnhealthy
			e.Metadata = map[string]string{"health_state": "UNAVAILABLE", "reason": "NETWORK_FAILURE"}
		}, "does not describe an operation outcome"},
		{"metadata outside the allowlist", func(e *Event) {
			e.Metadata = map[string]string{"credential": "pk_secret"}
		}, "does not allow metadata"},
		{"missing required metadata", func(e *Event) {
			e.Type, e.Outcome, e.Stage = TypeOperationFailed, OutcomeFailed, state.StageScan
			e.Metadata = map[string]string{"failed_path": "/tmp/x.failed"}
		}, `requires metadata "reason"`},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			e := startedEvent()
			test.mutate(&e)
			Canonicalize(&e)
			err := Validate(e)
			if err == nil {
				t.Fatalf("event accepted; want %q", test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to mention %q", err, test.want)
			}
		})
	}
}

func TestNoEventTypeCanCarryACredentialOrProviderBody(t *testing.T) {
	t.Parallel()
	forbidden := []string{"credential", "token", "authorization", "response_body", "error", "url", "password"}
	for eventType, allowed := range metadataAllowlist {
		for _, key := range forbidden {
			if _, permitted := allowed[key]; permitted {
				t.Fatalf("event type %q allows metadata %q", eventType, key)
			}
		}
		for key := range allowed {
			e := startedEvent()
			e.Type = eventType
			e.Metadata = map[string]string{key: "pk_live_secret_value"}
			// The allowlist bounds what can be recorded; it cannot judge what a
			// caller puts in an allowed field, so the caller-side redaction in
			// the CLI is what keeps secrets out of these values.
			if err := validateText("metadata "+key, e.Metadata[key], maxTextBytes, false); err != nil {
				t.Fatalf("allowed metadata %q rejects ordinary text: %v", key, err)
			}
		}
	}
}

func TestOrderingUsesPositionNotTheWallClock(t *testing.T) {
	t.Parallel()
	first := startedEvent()
	second := completedEvent()
	// The clock moved backwards between the two events.
	second.RecordedAt = testTime.Add(-time.Hour)
	other := startedEvent()
	other.OperationID = "20260901T090000Z-0000feed"

	events := []Event{second, other, first}
	Order(events)
	if events[0].OperationID != first.OperationID || events[0].Sequence != 1 {
		t.Fatalf("ordering did not start at the first position: %#v", events[0])
	}
	if events[1].Sequence != 3 {
		t.Fatalf("a backwards clock reordered one operation: %#v", events[1])
	}
	if events[2].OperationID != other.OperationID {
		t.Fatalf("operations were interleaved: %#v", events[2])
	}
}

func TestDuplicateAppendsCollapseToOneEvent(t *testing.T) {
	t.Parallel()
	replayed := []Event{startedEvent(), completedEvent(), startedEvent(), completedEvent()}
	unique := Deduplicate(replayed)
	if len(unique) != 2 {
		t.Fatalf("deduplicated to %d events, want 2", len(unique))
	}
	if unique[0].Sequence != 1 || unique[1].Sequence != 3 {
		t.Fatalf("deduplication kept the wrong positions: %#v", unique)
	}
}

func TestMarshalRefusesAnEventThatWouldSplitIntoTwoLines(t *testing.T) {
	t.Parallel()
	e := startedEvent()
	e.Message = "first line\nsecond line"
	if _, err := Marshal(e); err == nil {
		t.Fatal("an event containing a line break was encoded")
	}
}

func TestNotificationProblemIsLocalHistoryWithoutAnOutcome(t *testing.T) {
	t.Parallel()
	problem := startedEvent()
	problem.Type = TypeNotificationProblem
	problem.Stage = state.StageNotify
	problem.Outcome = ""
	problem.Message = "A configured notification destination did not accept an event."
	problem.Metadata = map[string]string{
		"destination_id": "ops", "event_type": string(TypeOperationCompleted),
		"reason": "NETWORK_FAILURE", "retryable": "true",
		"idempotency_key": strings.Repeat("ab", 16), "attempts": "3",
	}
	if err := Validate(problem); err != nil {
		t.Fatalf("valid notification problem: %v", err)
	}
	problem.Metadata["url"] = "https://secret.example/path"
	if err := Validate(problem); err == nil || !strings.Contains(err.Error(), "does not allow metadata") {
		t.Fatalf("unsafe notification metadata error = %v", err)
	}
}

func TestScheduledOverlapIsAnExplicitSkipNotAFailureOrSuccess(t *testing.T) {
	t.Parallel()
	skipped := startedEvent()
	skipped.Type = TypeOperationSkipped
	skipped.Stage = state.StagePrepare
	skipped.Outcome = OutcomeSkipped
	skipped.Message = "The operation was skipped because its scope was already active."
	skipped.Metadata = map[string]string{"reason": "OPERATION_OVERLAP", "schedule_id": "daily-main"}
	if err := Validate(skipped); err != nil {
		t.Fatalf("valid scheduled skip: %v", err)
	}
	skipped.Outcome = OutcomeSucceeded
	if err := Validate(skipped); err == nil {
		t.Fatal("scheduled skip claimed success")
	}
}

// FuzzUnmarshalNeverPanicsAndNeverAcceptsAnUnsafeEvent drives the event reader
// with arbitrary bytes. Recorded history is read back by status and replayed by
// a live notification test, so an accepted line must be an event that is safe
// to render and safe to send.
func FuzzUnmarshalNeverPanicsAndNeverAcceptsAnUnsafeEvent(f *testing.F) {
	line, err := Marshal(startedEvent())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(line)
	completed, err := Marshal(completedEvent())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(completed)
	f.Add([]byte(`{"schema_version":2}`))
	f.Add([]byte(`{"schema_version":1,"type":"operation.started"}`))
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Add([]byte(`{"schema_version":99}`))

	f.Fuzz(func(t *testing.T, content []byte) {
		recorded, err := Unmarshal(content)
		if err != nil {
			return
		}
		if recorded.SchemaVersion != SchemaVersion {
			t.Fatalf("accepted an event left at schema %d", recorded.SchemaVersion)
		}
		if err := Validate(recorded); err != nil {
			t.Fatalf("accepted an event that does not validate: %v", err)
		}
		if !KnownType(recorded.Type) {
			t.Fatalf("accepted the unknown event type %q", recorded.Type)
		}
		// Metadata is an allowlist, so nothing arbitrary can ride along.
		for key, value := range recorded.Metadata {
			if strings.ContainsAny(key, "\n\r") || strings.ContainsAny(value, "\n\r") {
				t.Fatalf("accepted metadata that would split the log: %q=%q", key, value)
			}
		}
		// An accepted event must survive being written back as exactly one line,
		// because the log is line-oriented and a second line would be read as a
		// second event.
		encoded, err := Marshal(recorded)
		if err != nil {
			t.Fatalf("an accepted event cannot be written back: %v", err)
		}
		if bytes.Count(encoded, []byte("\n")) != 1 || !bytes.HasSuffix(encoded, []byte("\n")) {
			t.Fatalf("an accepted event does not encode as one line: %q", encoded)
		}
		again, err := Unmarshal(bytes.TrimSuffix(encoded, []byte("\n")))
		if err != nil {
			t.Fatalf("an accepted event does not round-trip: %v", err)
		}
		if again.OperationID != recorded.OperationID || again.Sequence != recorded.Sequence {
			t.Fatal("a round trip changed the event position")
		}
	})
}
