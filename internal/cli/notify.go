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
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"alih/internal/event"
	"alih/internal/notify"
	"alih/internal/state"
)

const notifyHelpText = `Check notification configuration or explicitly replay one recorded event.

Usage:
  alih notify [--json]
  alih notify --test DESTINATION_ID [--json]

Without --test, this command only validates the private notifications.json
configuration and prints what would be eligible for delivery. It makes no
network request. --test explicitly performs one live delivery by replaying the
newest locally recorded event selected by that destination. The original event
identity and idempotency key are retained so a destination can reject a repeat.

Configuration lives in the user configuration directory at
alih/notifications.json. The file and its parent directory must be private.
Webhook bearer values are read only from the environment variable named by
secret_env; URL paths, query strings, fragments, secrets, and response bodies
are never rendered in status or command output.
`

type notificationCheckDestination struct {
	ID        string   `json:"id"`
	Enabled   bool     `json:"enabled"`
	Type      string   `json:"type"`
	SafeURL   string   `json:"safe_url"`
	Events    []string `json:"events"`
	SecretEnv string   `json:"secret_env,omitempty"`
}

type notificationCheckDocument struct {
	SchemaVersion int                            `json:"schema_version"`
	Kind          string                         `json:"kind"`
	Configured    bool                           `json:"configured"`
	Configuration string                         `json:"configuration"`
	Destinations  []notificationCheckDestination `json:"destinations"`
	TestResult    *notify.Result                 `json:"test_result,omitempty"`
}

func (a *App) runNotify(args []string) int {
	flags := flag.NewFlagSet("alih notify", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() { fmt.Fprint(a.stdout, notifyHelpText) }
	asJSON := flags.Bool("json", false, "print a stable machine-readable configuration check")
	testDestination := flags.String("test", "", "explicitly replay the newest selected event to this destination ID")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(a.stderr, "alih notify: positional arguments are not accepted")
		return 2
	}

	path, pathErr := notify.Path(a.options.NotificationRoot)
	if pathErr != nil {
		fmt.Fprintf(a.stderr, "alih notify: %s\n", safeError(pathErr, ""))
		return 1
	}
	document := notificationCheckDocument{
		SchemaVersion: notify.SchemaVersion, Kind: "notification_configuration",
		Configuration: path, Destinations: []notificationCheckDestination{},
	}
	config, err := notify.Load(a.options.NotificationRoot)
	if errors.Is(err, notify.ErrNotConfigured) {
		if strings.TrimSpace(*testDestination) != "" {
			fmt.Fprintln(a.stderr, "alih notify: no notification configuration exists; no delivery was attempted")
			return 1
		}
		return a.writeNotificationCheck(document, *asJSON)
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "alih notify: configuration is not usable: %s\n", safeError(err, ""))
		return 1
	}
	document.Configured = true
	for _, destination := range config.Destinations {
		events := append([]string(nil), destination.Events...)
		sort.Strings(events)
		document.Destinations = append(document.Destinations, notificationCheckDestination{
			ID: destination.ID, Enabled: destination.Enabled, Type: destination.Type,
			SafeURL: destination.SafeURL(), Events: events, SecretEnv: destination.SecretEnv,
		})
	}
	sort.Slice(document.Destinations, func(i, j int) bool {
		return document.Destinations[i].ID < document.Destinations[j].ID
	})

	if strings.TrimSpace(*testDestination) == "" {
		return a.writeNotificationCheck(document, *asJSON)
	}
	destination, found := configuredDestination(config.EnabledDestinations(), strings.TrimSpace(*testDestination))
	if !found {
		fmt.Fprintf(a.stderr, "alih notify: enabled destination %q was not found; no delivery was attempted\n",
			displayValue(strings.TrimSpace(*testDestination)))
		return 1
	}
	store, err := state.NewStore(a.options.StateRoot)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih notify: local event history is unavailable: %s\n", safeError(err, ""))
		return 1
	}
	history, _, err := event.Read(store.Root())
	if err != nil {
		fmt.Fprintf(a.stderr, "alih notify: local event history is unavailable: %s\n", safeError(err, ""))
		return 1
	}
	recorded, found := latestSelectedEvent(history, destination)
	if !found {
		fmt.Fprintf(a.stderr, "alih notify: destination %q has no selected recorded event to replay; no delivery was attempted\n",
			destination.ID)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := a.notificationTransport().Deliver(ctx, destination, recorded)
	document.TestResult = &result
	a.recordNotificationResult(store, state.Scope{
		Connector: recorded.Source.Connector, WorkspaceID: recorded.Source.WorkspaceID,
		Destination: recorded.Source.Destination,
	}, string(recorded.Type), result)
	if !result.Delivered {
		a.appendStandaloneNotificationProblem(store, recorded.Source, string(recorded.Type), result)
	}
	code := 0
	if !result.Delivered {
		code = 1
	}
	if *asJSON {
		if writeCode := a.writeNotificationCheck(document, true); writeCode != 0 {
			return writeCode
		}
		return code
	}
	fmt.Fprintln(a.stdout, result.Describe())
	return code
}

func (a *App) writeNotificationCheck(document notificationCheckDocument, asJSON bool) int {
	if asJSON {
		encoder := json.NewEncoder(a.stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(document); err != nil {
			fmt.Fprintf(a.stderr, "alih notify: encode result: %v\n", err)
			return 1
		}
		return 0
	}
	if !document.Configured {
		fmt.Fprintln(a.stdout, "Notifications: not configured (no delivery).")
		fmt.Fprintf(a.stdout, "Configuration: %s\n", document.Configuration)
		return 0
	}
	fmt.Fprintf(a.stdout, "Notifications: configured (%d destination(s)); configuration check made no delivery.\n",
		len(document.Destinations))
	for _, destination := range document.Destinations {
		status := "disabled"
		if destination.Enabled {
			status = "enabled"
		}
		fmt.Fprintf(a.stdout, "- %s: %s, %s, %s\n", destination.ID, status, destination.Type, destination.SafeURL)
		fmt.Fprintf(a.stdout, "  Events: %s\n", strings.Join(destination.Events, ", "))
		if destination.SecretEnv != "" {
			fmt.Fprintf(a.stdout, "  Secret source: environment variable %s (value not read)\n", destination.SecretEnv)
		}
	}
	return 0
}

func configuredDestination(destinations []notify.Destination, id string) (notify.Destination, bool) {
	for _, destination := range destinations {
		if destination.ID == id {
			return destination, true
		}
	}
	return notify.Destination{}, false
}

func latestSelectedEvent(history []event.Event, destination notify.Destination) (event.Event, bool) {
	ordered := append([]event.Event(nil), history...)
	event.Order(ordered)
	for index := len(ordered) - 1; index >= 0; index-- {
		if ordered[index].Type != event.TypeNotificationProblem && destination.Wants(ordered[index].Type) {
			return ordered[index], true
		}
	}
	return event.Event{}, false
}

func (a *App) notificationTransport() notify.Notifier {
	if a.options.Notifier != nil {
		return a.options.Notifier
	}
	return notify.NewWebhookNotifier(nil, nil, a.observedAt, a.recordedVersion())
}

func (o *operationState) configureNotifications() {
	if o == nil {
		return
	}
	checkedAt := o.app.observedAt()
	config, err := notify.Load(o.app.options.NotificationRoot)
	if errors.Is(err, notify.ErrNotConfigured) {
		return
	}
	if err != nil {
		o.app.warnNotificationConfiguration(err)
		o.notificationSnapshot = &state.NotificationState{
			CheckedAt: checkedAt,
			Problem: &state.NotificationProblem{
				Reason:     state.NotificationReasonConfigurationInvalid,
				Message:    "The notification configuration is not usable; no delivery was attempted.",
				ObservedAt: checkedAt,
			},
		}
		return
	}
	o.notificationTargets = config.EnabledDestinations()
	o.notifier = o.app.notificationTransport()
	ids := make([]string, 0, len(o.notificationTargets))
	for _, destination := range o.notificationTargets {
		ids = append(ids, destination.ID)
	}
	o.notificationSnapshot = &state.NotificationState{CheckedAt: checkedAt, DestinationIDs: ids}
}

func (o *operationState) applyNotificationConfiguration(record *state.Record) {
	if o == nil || record == nil {
		return
	}
	if o.notificationSnapshot == nil {
		record.Notifications = nil
		return
	}
	snapshot := *o.notificationSnapshot
	snapshot.DestinationIDs = append([]string(nil), o.notificationSnapshot.DestinationIDs...)
	if o.notificationSnapshot.Problem != nil {
		problem := *o.notificationSnapshot.Problem
		snapshot.Problem = &problem
	}
	configured := make(map[string]struct{}, len(snapshot.DestinationIDs))
	for _, id := range snapshot.DestinationIDs {
		configured[id] = struct{}{}
	}
	if snapshot.Problem == nil && record.Notifications != nil {
		for _, delivery := range record.Notifications.LastDeliveries {
			if _, keep := configured[delivery.DestinationID]; keep {
				snapshot.LastDeliveries = append(snapshot.LastDeliveries, delivery)
			}
		}
	}
	record.Notifications = &snapshot
}

func (o *operationState) deliverNotifications(recorded event.Event) {
	if o == nil || o.notifier == nil || recorded.Type == event.TypeNotificationProblem {
		return
	}
	ctx := o.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	for _, destination := range o.notificationTargets {
		if !destination.Wants(recorded.Type) {
			continue
		}
		result := o.notifier.Deliver(ctx, destination, recorded)
		o.recordNotificationResult(string(recorded.Type), result)
		if result.Delivered {
			continue
		}
		o.app.warnNotification(result)
		o.emitNotificationProblem(string(recorded.Type), result)
	}
}

func (o *operationState) recordNotificationResult(eventType string, result notify.Result) {
	if o == nil {
		return
	}
	o.write(func(record *state.Record) {
		upsertNotificationDelivery(record, eventType, result, o.app.observedAt())
	})
}

func (a *App) recordNotificationResult(store *state.Store, scope state.Scope, eventType string, result notify.Result) {
	if store == nil {
		return
	}
	if _, err := store.Load(scope); err != nil {
		if !errors.Is(err, state.ErrNotRecorded) {
			a.warnState(err)
		}
		return
	}
	if _, err := store.Update(scope, func(record *state.Record) error {
		record.UpdatedAt = a.observedAt()
		upsertNotificationDelivery(record, eventType, result, a.observedAt())
		return nil
	}); err != nil {
		a.warnState(err)
	}
}

func upsertNotificationDelivery(record *state.Record, eventType string, result notify.Result, observedAt time.Time) {
	if record == nil {
		return
	}
	if record.Notifications == nil {
		record.Notifications = &state.NotificationState{CheckedAt: observedAt.UTC()}
	}
	found := false
	for _, id := range record.Notifications.DestinationIDs {
		if id == result.DestinationID {
			found = true
			break
		}
	}
	if !found {
		record.Notifications.DestinationIDs = append(record.Notifications.DestinationIDs, result.DestinationID)
	}
	delivery := state.NotificationDelivery{
		DestinationID: result.DestinationID, EventType: eventType,
		IdempotencyKey: result.IdempotencyKey, Delivered: result.Delivered,
		Attempts: result.Attempts, Reason: string(result.Reason), Retryable: result.Retryable,
		Message: result.Message, ObservedAt: result.ObservedAt,
	}
	replaced := false
	for index := range record.Notifications.LastDeliveries {
		if record.Notifications.LastDeliveries[index].DestinationID == result.DestinationID {
			record.Notifications.LastDeliveries[index] = delivery
			replaced = true
			break
		}
	}
	if !replaced {
		record.Notifications.LastDeliveries = append(record.Notifications.LastDeliveries, delivery)
	}
}

func (o *operationState) emitNotificationProblem(eventType string, result notify.Result) {
	if o == nil || !o.recording || o.sink == nil {
		return
	}
	o.sequence++
	problem := event.Event{
		SchemaVersion: event.SchemaVersion, Type: event.TypeNotificationProblem,
		OperationID: o.attempt.OperationID, Sequence: o.sequence,
		RecordedAt: o.app.observedAt(), Source: o.source,
		Operation: o.attempt.Operation, Stage: state.StageNotify,
		Message: "A configured notification destination did not accept an event.",
		Metadata: map[string]string{
			"destination_id": result.DestinationID, "event_type": eventType,
			"reason": string(result.Reason), "retryable": strconv.FormatBool(result.Retryable),
			"idempotency_key": result.IdempotencyKey, "attempts": strconv.Itoa(result.Attempts),
		},
		AlihVersion: o.app.recordedVersion(),
	}
	if o.attempt.ScheduleID != "" {
		problem.Metadata["schedule_id"] = o.attempt.ScheduleID
	}
	if err := o.sink.Emit(problem); err != nil {
		o.app.warnEvent(err)
		o.recording = false
	}
}

func (a *App) appendStandaloneNotificationProblem(store *state.Store, source event.Source, eventType string, result notify.Result) {
	if store == nil {
		return
	}
	sink := a.eventSink(store)
	if sink == nil {
		return
	}
	operationID, err := state.NewOperationID(a.observedAt(), a.options.Entropy)
	if err != nil {
		a.warnEvent(err)
		return
	}
	problem := event.Event{
		SchemaVersion: event.SchemaVersion, Type: event.TypeNotificationProblem,
		OperationID: operationID, Sequence: 1, RecordedAt: a.observedAt(), Source: source,
		Operation: state.OperationNotify, Stage: state.StageNotify,
		Message: "A configured notification destination did not accept an event.",
		Metadata: map[string]string{
			"destination_id": result.DestinationID, "event_type": eventType,
			"reason": string(result.Reason), "retryable": strconv.FormatBool(result.Retryable),
			"idempotency_key": result.IdempotencyKey, "attempts": strconv.Itoa(result.Attempts),
		},
		AlihVersion: a.recordedVersion(),
	}
	if err := sink.Emit(problem); err != nil {
		a.warnEvent(err)
	}
}

func (a *App) warnNotificationConfiguration(err error) {
	fmt.Fprintf(a.stderr, "alih: notification configuration is not usable: %s\n", safeError(err, ""))
	fmt.Fprintln(a.stderr, "alih: no notification was attempted; the operation result is unaffected.")
}

func (a *App) warnNotification(result notify.Result) {
	fmt.Fprintf(a.stderr, "alih: notification %s\n", result.Describe())
	fmt.Fprintln(a.stderr, "alih: the operation result and any sealed archive are unaffected; status records the delivery problem.")
}
