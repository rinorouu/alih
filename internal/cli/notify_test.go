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

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"alih/internal/event"
	"alih/internal/notify"
	"alih/internal/state"
)

type stubNotifier struct {
	calls  []event.Event
	result func(notify.Destination, event.Event) notify.Result
}

func (stub *stubNotifier) Deliver(_ context.Context, destination notify.Destination, recorded event.Event) notify.Result {
	stub.calls = append(stub.calls, recorded)
	if stub.result != nil {
		return stub.result(destination, recorded)
	}
	return notify.Result{
		DestinationID: destination.ID, IdempotencyKey: notify.IdempotencyKey(recorded),
		Delivered: true, Attempts: 1, Reason: notify.ReasonDelivered,
		Message: "The destination accepted the notification.", ObservedAt: alphaTestTime,
	}
}

func writeNotificationConfig(t *testing.T, root, eventType string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create notification config root: %v", err)
	}
	config := `{
  "schema_version": 1,
  "destinations": [{
    "id": "ops",
    "enabled": true,
    "type": "webhook",
    "url": "https://hooks.example.invalid/private/token-value?secret=query#fragment",
    "events": [` + strconvQuote(eventType) + `],
    "secret_env": "ALIH_NOTIFY_OPS_TOKEN",
    "max_attempts": 1
  }]
}`
	if err := os.WriteFile(filepath.Join(root, "notifications.json"), []byte(config), 0o600); err != nil {
		t.Fatalf("write notification config: %v", err)
	}
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestNotifyConfigCheckMakesNoDeliveryAndRedactsTheURL(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "config")
	writeNotificationConfig(t, root, string(event.TypeOperationFailed))
	notifier := &stubNotifier{}
	app, stdout, stderr := statusApp(t, filepath.Join(t.TempDir(), "state"), Options{
		NotificationRoot: root, Notifier: notifier,
	})
	if code := app.Run([]string{"notify"}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if len(notifier.calls) != 0 {
		t.Fatalf("config check made %d deliveries", len(notifier.calls))
	}
	for _, forbidden := range []string{"private", "token-value", "secret=query", "fragment"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("config check exposed %q:\n%s", forbidden, stdout.String())
		}
	}
	if !strings.Contains(stdout.String(), "configuration check made no delivery") ||
		!strings.Contains(stdout.String(), "https://hooks.example.invalid/…") {
		t.Fatalf("config check output:\n%s", stdout.String())
	}
}

func TestNotifyWithoutConfigurationIsTheSilentNoEgressDefault(t *testing.T) {
	t.Parallel()
	notifier := &stubNotifier{}
	app, stdout, _ := statusApp(t, filepath.Join(t.TempDir(), "state"), Options{
		NotificationRoot: filepath.Join(t.TempDir(), "missing"), Notifier: notifier,
	})
	if code := app.Run([]string{"notify", "--json"}); code != 0 {
		t.Fatalf("code=%d output=%s", code, stdout.String())
	}
	if len(notifier.calls) != 0 || !strings.Contains(stdout.String(), `"configured": false`) {
		t.Fatalf("calls=%d output=%s", len(notifier.calls), stdout.String())
	}
}

func TestNotifyLiveTestReplaysARealSelectedEventAndRecordsFailure(t *testing.T) {
	t.Parallel()
	stateRoot := filepath.Join(t.TempDir(), "state")
	configRoot := filepath.Join(t.TempDir(), "config")
	writeNotificationConfig(t, configRoot, string(event.TypeOperationFailed))
	record := seedRecord(t, stateRoot, func(record *state.Record) {})
	sink, err := event.NewFileSink(stateRoot)
	if err != nil {
		t.Fatalf("event sink: %v", err)
	}
	recorded := event.Event{
		SchemaVersion: event.SchemaVersion, Type: event.TypeOperationFailed,
		OperationID: "20260901T080000Z-0badc0de", Sequence: 2, RecordedAt: alphaTestTime,
		Source:    event.Source{Connector: record.Scope.Connector, WorkspaceID: record.Scope.WorkspaceID, Destination: record.Scope.Destination},
		Operation: state.OperationBackup, Stage: state.StageScan, Outcome: event.OutcomeFailed,
		Message:  "The run failed while inventorying the source.",
		Metadata: map[string]string{"reason": "NETWORK_FAILURE"}, AlihVersion: "dev",
	}
	if err := sink.Emit(recorded); err != nil {
		t.Fatalf("record event: %v", err)
	}
	notifier := &stubNotifier{result: func(destination notify.Destination, event event.Event) notify.Result {
		return notify.Result{
			DestinationID: destination.ID, IdempotencyKey: notify.IdempotencyKey(event),
			Attempts: 1, Reason: notify.ReasonNetworkFailure, Retryable: true,
			Message: "The destination could not be reached.", ObservedAt: alphaTestTime,
		}
	}}
	app, stdout, stderr := statusApp(t, stateRoot, Options{
		NotificationRoot: configRoot, Notifier: notifier,
	})
	if code := app.Run([]string{"notify", "--test", "ops", "--json"}); code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if len(notifier.calls) != 1 || notifier.calls[0].OperationID != recorded.OperationID {
		t.Fatalf("replayed events = %#v", notifier.calls)
	}
	updated, err := stateStoreAt(t, stateRoot).Load(record.Scope)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if updated.Notifications == nil || len(updated.Notifications.LastDeliveries) != 1 ||
		updated.Notifications.LastDeliveries[0].Delivered {
		t.Fatalf("notification state = %#v", updated.Notifications)
	}
	history, _, err := event.Read(stateRoot)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	problems := 0
	for _, entry := range history {
		if entry.Type == event.TypeNotificationProblem {
			problems++
		}
	}
	if problems != 1 {
		t.Fatalf("notification problem events = %d; history=%#v", problems, history)
	}

	status, statusOut, _ := statusApp(t, stateRoot, Options{})
	if code := status.Run([]string{"status"}); code != statusExitAttention {
		t.Fatalf("status code=%d output=%s", code, statusOut.String())
	}
	if !strings.Contains(statusOut.String(), "notification destination ops did not accept operation.failed") {
		t.Fatalf("status did not expose delivery failure:\n%s", statusOut.String())
	}
}

func TestNotificationFailureDoesNotChangeSuccessfulBackupOrRecurse(t *testing.T) {
	t.Parallel()
	harness := newBackupHarness(t, state.ResultVerified)
	stateRoot := filepath.Join(t.TempDir(), "state")
	configRoot := filepath.Join(t.TempDir(), "config")
	writeNotificationConfig(t, configRoot, string(event.TypeOperationCompleted))
	notifier := &stubNotifier{result: func(destination notify.Destination, recorded event.Event) notify.Result {
		archivePath := recorded.Metadata["archive_path"]
		if _, err := os.Stat(filepath.Join(archivePath, state.ManifestFilename)); err != nil {
			t.Errorf("notification ran before archive publication: %v", err)
		}
		return notify.Result{
			DestinationID: destination.ID, IdempotencyKey: notify.IdempotencyKey(recorded),
			Attempts: 1, Reason: notify.ReasonNetworkFailure, Retryable: true,
			Message: "The destination could not be reached.", ObservedAt: alphaTestTime,
		}
	}}
	harness.app.options.StateRoot = stateRoot
	harness.app.options.NotificationRoot = configRoot
	harness.app.options.Notifier = notifier
	if code := harness.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%s", code, harness.stderr.String())
	}
	if len(notifier.calls) != 1 || notifier.calls[0].Type != event.TypeOperationCompleted {
		t.Fatalf("notification calls = %#v", notifier.calls)
	}
	archivePath := filepath.Join(harness.finalRoot(), backupArchiveDirectory)
	if _, err := os.Stat(filepath.Join(archivePath, state.ManifestFilename)); err != nil {
		t.Fatalf("successful archive was changed or removed: %v", err)
	}
	if !strings.Contains(harness.stdout.String(), "ALIH — BACKUP COMPLETE") ||
		!strings.Contains(harness.stderr.String(), "operation result and any sealed archive are unaffected") {
		t.Fatalf("stdout=%s stderr=%s", harness.stdout.String(), harness.stderr.String())
	}
}

func TestUnselectedEventsDoNotNotify(t *testing.T) {
	t.Parallel()
	harness := newBackupHarness(t, state.ResultVerified)
	configRoot := filepath.Join(t.TempDir(), "config")
	writeNotificationConfig(t, configRoot, string(event.TypeOperationFailed))
	notifier := &stubNotifier{}
	harness.app.options.StateRoot = filepath.Join(t.TempDir(), "state")
	harness.app.options.NotificationRoot = configRoot
	harness.app.options.Notifier = notifier
	if code := harness.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%s", code, harness.stderr.String())
	}
	if len(notifier.calls) != 0 {
		t.Fatalf("unselected events caused %d deliveries", len(notifier.calls))
	}
}
