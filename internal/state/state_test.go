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

package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"alih/internal/connector"
)

var (
	testStart    = time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	testEnd      = time.Date(2026, 8, 31, 9, 5, 0, 0, time.UTC)
	testDigest   = "sha256:" + strings.Repeat("ab", 32)
	testDigestB  = "sha256:" + strings.Repeat("cd", 32)
	testDestRoot = testAbsolutePath("home", "tester", "Alih")
	testArchive  = filepath.Join(testDestRoot, "workspace", "2026-08-31T090000Z", "archive")
	testReport   = filepath.Join(testDestRoot, "workspace", "2026-08-31T090000Z", "recovery-report.html")
)

func testScope() Scope {
	return Scope{Connector: "clickup", WorkspaceID: "9001", Destination: testDestRoot}
}

func testCapabilities() []connector.Capability {
	return []connector.Capability{
		{
			ID: connector.CapabilityItems, Name: "Tasks and subtasks",
			Requirement: connector.CapabilityRequired, Implementation: connector.CapabilitySupported,
			State: connector.CapabilitySupported, Availability: connector.CapabilityAvailabilityAvailable,
		},
		{
			ID: connector.CapabilityComments, Name: "Comments",
			Requirement: connector.CapabilityRequired, Implementation: connector.CapabilitySupported,
			State: connector.CapabilitySupported, Availability: connector.CapabilityAvailabilityAvailable,
		},
	}
}

func testAssessment(t testing.TB) connector.OperationalAssessment {
	t.Helper()
	assessment, err := connector.HealthyAssessment("clickup", connector.HealthBasisBackup, testEnd,
		connector.AuthenticationAuthenticated, testCapabilities())
	if err != nil {
		t.Fatalf("build assessment: %v", err)
	}
	return assessment
}

func succeededAttempt() Attempt {
	ended := testEnd
	return Attempt{
		OperationID: "20260831T090000Z-0badc0de", Operation: OperationBackup, Stage: StageFinalize,
		Outcome: OutcomeSucceeded, StartedAt: testStart, EndedAt: &ended,
		ArchivePath: testArchive, ReportPath: testReport,
		AlihVersion: "dev",
	}
}

func testRecord(t testing.TB) Record {
	t.Helper()
	assessment := testAssessment(t)
	attempt := succeededAttempt()
	success := succeededAttempt()
	completed := testEnd
	return Record{
		SchemaVersion: SchemaVersion,
		Scope:         testScope(),
		WorkspaceName: "Example Workspace",
		Account:       &connector.Identity{ID: "u-1", Name: "Tester"},
		AlihVersion:   "dev",
		UpdatedAt:     testEnd,
		Assessment:    &assessment,

		CapabilitySchemaVersion: connector.CapabilitySchemaVersion,
		Capabilities:            testCapabilities(),

		LastAttempt: &attempt,
		LastSuccess: &success,
		LastVerification: &Verification{
			Result: ResultVerified, VerifiedAt: testEnd, AlihVersion: "dev",
			Archive: ArchiveIdentity{
				Path: testArchive, ManifestChecksum: testDigest, LogicalDigest: testDigestB,
				ArchiveStatus: "CREATED_UNVERIFIED", CompletedAt: &completed,
			},
		},
	}
}

func TestMarshalIsCanonicalAndRoundTrips(t *testing.T) {
	t.Parallel()
	record := testRecord(t)
	// Deliberately unordered input: canonical output must not depend on it.
	record.Capabilities = []connector.Capability{testCapabilities()[1], testCapabilities()[0]}
	record.LastVerification.Limitations = []string{"attachment content unavailable", "a limitation"}
	record.UpdatedAt = testEnd.In(time.FixedZone("WITA", 8*3600))

	first, err := Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded, err := Unmarshal(first)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	second, err := Marshal(decoded)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("canonical form is not stable:\n%s\n%s", first, second)
	}
	if !decoded.UpdatedAt.Equal(testEnd) || decoded.UpdatedAt.Location() != time.UTC {
		t.Fatalf("updated_at = %s (%s), want %s UTC", decoded.UpdatedAt, decoded.UpdatedAt.Location(), testEnd)
	}
	if decoded.Capabilities[0].ID != connector.CapabilityComments {
		t.Fatalf("capabilities were not canonically ordered: %#v", decoded.Capabilities)
	}
	if decoded.LastVerification.Limitations[0] != "a limitation" {
		t.Fatalf("limitations were not canonically ordered: %#v", decoded.LastVerification.Limitations)
	}
	if !bytes.HasSuffix(first, []byte("\n")) {
		t.Fatal("canonical state does not end with a newline")
	}
}

func TestStoredStateNeverCarriesCredentialFields(t *testing.T) {
	t.Parallel()
	content, err := Marshal(testRecord(t))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(content, &generic); err != nil {
		t.Fatalf("decode: %v", err)
	}
	allowed := map[string]struct{}{
		"schema_version": {}, "revision": {}, "scope": {}, "workspace_name": {}, "account": {},
		"alih_version": {}, "updated_at": {}, "operational_assessment": {},
		"capability_schema_version": {}, "capabilities": {},
		"last_attempt": {}, "last_success": {}, "last_verification": {}, "notifications": {},
	}
	for key := range generic {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("state document contains unexpected top-level field %q", key)
		}
	}
	for _, forbidden := range []string{"token", "credential", "secret", "authorization", "password"} {
		if strings.Contains(strings.ToLower(string(content)), forbidden) {
			t.Fatalf("state document mentions %q", forbidden)
		}
	}
}

func TestUnmarshalRejectsUnknownTrailingAndFutureContent(t *testing.T) {
	t.Parallel()
	valid, err := Marshal(testRecord(t))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	unknown := strings.Replace(string(valid), `"revision": 0,`, `"revision": 0,`+"\n  \"surprise\": true,", 1)
	if _, err := Unmarshal([]byte(unknown)); err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("unknown field accepted: %v", err)
	}
	if _, err := Unmarshal(append(append([]byte(nil), valid...), []byte("{}")...)); err == nil {
		t.Fatal("trailing content accepted")
	}
	if _, err := Unmarshal([]byte(`{"schema_version":`)); err == nil {
		t.Fatal("truncated JSON accepted")
	}

	future := strings.Replace(string(valid), `"schema_version": 3`, `"schema_version": 4`, 1)
	_, err = Unmarshal([]byte(future))
	if !errors.Is(err, ErrFutureSchema) {
		t.Fatalf("future schema error = %v, want ErrFutureSchema", err)
	}
}

func TestStateVersionOneMigratesWithoutChangingItsScopeKey(t *testing.T) {
	t.Parallel()
	valid, err := Marshal(testRecord(t))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	legacy := strings.Replace(string(valid), `"schema_version": 3`, `"schema_version": 1`, 1)
	migrated, err := Unmarshal([]byte(legacy))
	if err != nil {
		t.Fatalf("read state v1: %v", err)
	}
	if migrated.SchemaVersion != SchemaVersion || migrated.Notifications != nil {
		t.Fatalf("migrated record = %#v", migrated)
	}
	if migrated.Scope.Key() != testScope().Key() {
		t.Fatal("schema migration changed the state file identity")
	}
}

func TestStateVersionTwoNotificationProjectionMigrates(t *testing.T) {
	t.Parallel()
	record := testRecord(t)
	record.Notifications = &NotificationState{CheckedAt: testEnd, DestinationIDs: []string{"ops"}}
	valid, err := Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	legacy := strings.Replace(string(valid), `"schema_version": 3`, `"schema_version": 2`, 1)
	migrated, err := Unmarshal([]byte(legacy))
	if err != nil {
		t.Fatalf("read state v2: %v", err)
	}
	if migrated.SchemaVersion != SchemaVersion || migrated.Notifications == nil || migrated.Notifications.DestinationIDs[0] != "ops" {
		t.Fatalf("migrated record = %#v", migrated)
	}
}

func TestValidateRejectsAmbiguousOrOverclaimingRecords(t *testing.T) {
	t.Parallel()
	ended := testEnd
	tests := []struct {
		name   string
		mutate func(*Record)
		want   string
	}{
		{"wrong schema version", func(r *Record) { r.SchemaVersion = 0 }, "unsupported state schema version"},
		{"empty connector", func(r *Record) { r.Scope.Connector = "  " }, "connector is empty"},
		{"empty workspace", func(r *Record) { r.Scope.WorkspaceID = "" }, "workspace id is empty"},
		{"relative destination", func(r *Record) { r.Scope.Destination = "relative/path" }, "absolute path"},
		{"no update time", func(r *Record) { r.UpdatedAt = time.Time{} }, "no update time"},
		{"control character", func(r *Record) { r.WorkspaceName = "bad\x07name" }, "control character"},
		{"invalid utf-8", func(r *Record) { r.WorkspaceName = string([]byte{0xff, 0xfe}) }, "valid UTF-8"},
		{"unknown operation", func(r *Record) { r.LastAttempt.Operation = "restore" }, "unknown operation"},
		{"unknown stage", func(r *Record) { r.LastAttempt.Stage = "publish" }, "unknown stage"},
		{"unknown outcome", func(r *Record) { r.LastAttempt.Outcome = "MAYBE" }, "unknown outcome"},
		{"skipped without reason", func(r *Record) {
			r.LastAttempt.Outcome = OutcomeSkipped
			r.LastAttempt.Error = nil
			r.LastAttempt.SkipReason = ""
		}, "skip reason"},
		{"unsafe schedule id", func(r *Record) { r.LastAttempt.ScheduleID = "../daily" }, "unsafe character"},
		{"ended before started", func(r *Record) {
			earlier := testStart.Add(-time.Hour)
			r.LastAttempt.EndedAt = &earlier
		}, "ended before it started"},
		{"started with an end time", func(r *Record) {
			r.LastAttempt.Outcome = OutcomeStarted
			r.LastAttempt.EndedAt = &ended
		}, "records an end time"},
		{"failure without a reason", func(r *Record) {
			r.LastAttempt.Outcome = OutcomeFailed
			r.LastAttempt.Error = nil
		}, "failed without a recorded reason"},
		{"success carrying an error", func(r *Record) {
			r.LastAttempt.Error = &SafeError{Stage: StageVerify, Reason: connector.HealthReasonNone, Message: "x"}
		}, "succeeded but records an error"},
		{"success without an archive", func(r *Record) {
			attempt := succeededAttempt()
			attempt.ArchivePath = ""
			r.LastSuccess = &attempt
			r.LastAttempt = &attempt
		}, "no archive path"},
		{"success without an end", func(r *Record) {
			attempt := succeededAttempt()
			attempt.EndedAt = nil
			r.LastSuccess = &attempt
			r.LastAttempt = &attempt
		}, "no end time"},
		{"attempt older than the success it supersedes", func(r *Record) {
			attempt := succeededAttempt()
			attempt.StartedAt = testStart.Add(-time.Hour)
			attempt.Outcome = OutcomeFailed
			attempt.Error = &SafeError{Stage: StageScan, Reason: connector.HealthReasonNetworkFailure, Message: "unreachable"}
			r.LastAttempt = &attempt
		}, "started before the last success"},
		{"unknown error reason", func(r *Record) {
			attempt := succeededAttempt()
			attempt.Outcome = OutcomeFailed
			attempt.Error = &SafeError{Stage: StageScan, Reason: "MADE_UP", Message: "x"}
			r.LastAttempt = &attempt
			r.LastSuccess = nil
		}, "unknown reason"},
		{"unverifiable result", func(r *Record) { r.LastVerification.Result = "PROBABLY_FINE" }, "unknown result"},
		{"relative verified archive", func(r *Record) { r.LastVerification.Archive.Path = "archive" }, "must be absolute"},
		{"malformed digest", func(r *Record) { r.LastVerification.Archive.ManifestChecksum = "sha256:zz" }, "digest"},
		{"unprefixed digest", func(r *Record) { r.LastVerification.Archive.LogicalDigest = strings.Repeat("ab", 32) }, "not a sha256 digest"},
		{"invalid assessment", func(r *Record) { r.Assessment.Health.State = connector.HealthUnavailable }, "operational assessment"},
		{"capability without an identity", func(r *Record) { r.Capabilities[0].ID = "" }, "capability contract"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			record := testRecord(t)
			test.mutate(&record)
			Canonicalize(&record)
			err := Validate(record)
			if err == nil {
				t.Fatalf("record was accepted; want %q", test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to mention %q", err, test.want)
			}
		})
	}
}

func TestValidRecordIsAccepted(t *testing.T) {
	t.Parallel()
	record := testRecord(t)
	Canonicalize(&record)
	if err := Validate(record); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
}

func TestNotificationStateIsCanonicalAndRejectsContradictions(t *testing.T) {
	t.Parallel()
	base := func() Record {
		record := testRecord(t)
		record.Notifications = &NotificationState{
			CheckedAt:      testEnd,
			DestinationIDs: []string{"second", "first"},
			LastDeliveries: []NotificationDelivery{
				{
					DestinationID: "second", EventType: "operation.completed",
					IdempotencyKey: strings.Repeat("ab", 16), Delivered: false, Attempts: 3,
					Reason: NotificationReasonRateLimited, Retryable: true,
					Message: "The destination is rate limiting notifications.", ObservedAt: testEnd,
				},
				{
					DestinationID: "first", EventType: "operation.completed",
					IdempotencyKey: strings.Repeat("cd", 16), Delivered: true, Attempts: 1,
					Reason:  NotificationReasonDelivered,
					Message: "The destination accepted the notification.", ObservedAt: testEnd,
				},
			},
		}
		return record
	}
	record := base()
	Canonicalize(&record)
	if err := Validate(record); err != nil {
		t.Fatalf("valid notification state: %v", err)
	}
	if record.Notifications.DestinationIDs[0] != "first" ||
		record.Notifications.LastDeliveries[0].DestinationID != "first" {
		t.Fatalf("notification state was not canonicalized: %#v", record.Notifications)
	}

	tests := []struct {
		name   string
		mutate func(*NotificationState)
	}{
		{"no check time", func(n *NotificationState) { n.CheckedAt = time.Time{} }},
		{"duplicate destination", func(n *NotificationState) { n.DestinationIDs = []string{"first", "first"} }},
		{"unsafe destination", func(n *NotificationState) { n.DestinationIDs[0] = "../ops" }},
		{"delivery for unknown destination", func(n *NotificationState) { n.DestinationIDs = []string{"first"} }},
		{"malformed idempotency key", func(n *NotificationState) { n.LastDeliveries[0].IdempotencyKey = "not-a-digest" }},
		{"unknown reason", func(n *NotificationState) { n.LastDeliveries[0].Reason = "MAYBE" }},
		{"delivery contradiction", func(n *NotificationState) { n.LastDeliveries[0].Delivered = true }},
		{"too many attempts", func(n *NotificationState) { n.LastDeliveries[0].Attempts = 6 }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			record := base()
			test.mutate(record.Notifications)
			Canonicalize(&record)
			if err := Validate(record); err == nil {
				t.Fatal("invalid notification state was accepted")
			}
		})
	}
}

func TestNotificationConfigurationProblemContainsOnlySafeOperatorText(t *testing.T) {
	t.Parallel()
	record := testRecord(t)
	record.Notifications = &NotificationState{
		CheckedAt: testEnd,
		Problem: &NotificationProblem{
			Reason:     NotificationReasonConfigurationInvalid,
			Message:    "The notification configuration is not usable; no delivery was attempted.",
			ObservedAt: testEnd,
		},
	}
	content, err := Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"https://", "Authorization", "Bearer ", "token-value"} {
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("notification state exposed %q: %s", forbidden, content)
		}
	}
}

func TestScopeKeyIdentifiesConnectorWorkspaceAndDestination(t *testing.T) {
	t.Parallel()
	base := testScope()
	spaced := Scope{Connector: " clickup ", WorkspaceID: " 9001 ", Destination: testDestRoot + "/"}
	if base.Key() != spaced.Key() {
		t.Fatal("scope key changed under whitespace and trailing-separator normalization")
	}
	for _, different := range []Scope{
		{Connector: "other", WorkspaceID: "9001", Destination: testDestRoot},
		{Connector: "clickup", WorkspaceID: "9002", Destination: testDestRoot},
		{Connector: "clickup", WorkspaceID: "9001", Destination: testAbsolutePath("mnt", "backup")},
	} {
		if different.Key() == base.Key() {
			t.Fatalf("scope %#v collides with the base scope key", different)
		}
	}
	if len(base.Key()) != 32 {
		t.Fatalf("scope key %q is not a 16-byte digest", base.Key())
	}
}

func TestNewOperationIDUsesInjectedClockAndEntropy(t *testing.T) {
	t.Parallel()
	id, err := NewOperationID(testStart, bytes.NewReader([]byte{0xde, 0xad, 0xbe, 0xef}))
	if err != nil {
		t.Fatalf("new operation id: %v", err)
	}
	if id != "20260831T090000Z-deadbeef" {
		t.Fatalf("operation id = %q", id)
	}
	if _, err := NewOperationID(testStart, bytes.NewReader([]byte{0x01})); err == nil {
		t.Fatal("exhausted entropy was accepted")
	}
}

func TestAgeIsMeasuredInUTC(t *testing.T) {
	t.Parallel()
	observed := testStart.In(time.FixedZone("WITA", 8*3600))
	if age := Age(observed, testStart.Add(90*time.Minute)); age != 90*time.Minute {
		t.Fatalf("age = %s, want 1h30m0s", age)
	}
}

// FuzzUnmarshalNeverPanicsAndNeverAcceptsAnInvalidRecord drives the state
// reader with arbitrary bytes. The reader guards a file Alih rewrites on every
// run, so the property that matters is not which documents it rejects but that
// anything it accepts is a record the rest of Alih may safely act on.
func FuzzUnmarshalNeverPanicsAndNeverAcceptsAnInvalidRecord(f *testing.F) {
	valid, err := Marshal(testRecord(f))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(`{"schema_version":3}`))
	f.Add([]byte(`{"schema_version":1,"revision":0}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"schema_version":99}`))

	f.Fuzz(func(t *testing.T, content []byte) {
		record, err := Unmarshal(content)
		if err != nil {
			return
		}
		// Accepting a document is a promise about it.
		if record.SchemaVersion != SchemaVersion {
			t.Fatalf("accepted a record left at schema %d", record.SchemaVersion)
		}
		if err := Validate(record); err != nil {
			t.Fatalf("accepted a record that does not validate: %v", err)
		}
		if record.Scope.Key() == "" {
			t.Fatal("accepted a record with no state-file identity")
		}
		// An accepted record must survive being written back out and read again,
		// or a later run would not find what this one recorded.
		encoded, err := Marshal(record)
		if err != nil {
			t.Fatalf("an accepted record cannot be written back: %v", err)
		}
		again, err := Unmarshal(encoded)
		if err != nil {
			t.Fatalf("an accepted record does not round-trip: %v", err)
		}
		if again.Scope.Key() != record.Scope.Key() {
			t.Fatal("a round trip changed the state-file identity")
		}
	})
}

// testAbsolutePath builds a deterministic absolute path that satisfies
// filepath.IsAbs on every supported platform. Scope identity is compared as a
// string, so the value must not vary by machine, but Windows does not consider
// a rooted path absolute unless it names a volume. Hard-coded POSIX literals
// therefore failed validation on Windows while passing everywhere else.
func testAbsolutePath(elements ...string) string {
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	return filepath.Join(append([]string{root}, elements...)...)
}
