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
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"alih/internal/connector"
	"alih/internal/connector/clickup"
	"alih/internal/event"
	"alih/internal/state"
)

// recordingSink keeps every event a run emitted, in emission order.
type recordingSink struct {
	mu     sync.Mutex
	events []event.Event
	err    error
}

func (s *recordingSink) Emit(e event.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := event.Validate(e); err != nil {
		return fmt.Errorf("invalid event: %w", err)
	}
	s.events = append(s.events, e)
	return s.err
}

func (s *recordingSink) types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.events))
	for _, e := range s.events {
		names = append(names, string(e.Type))
	}
	return names
}

func TestASuccessfulBackupEmitsItsSequenceInOrder(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	sink := &recordingSink{}
	h.app.options.StateRoot = filepath.Join(t.TempDir(), "state")
	h.app.options.EventSink = sink

	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, h.stderr.String())
	}

	want := []string{"operation.started", "verification.recorded", "operation.completed"}
	if got := sink.types(); !equalStrings(got, want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}
	for index, recorded := range sink.events {
		if recorded.Sequence != index+1 {
			t.Fatalf("event %d has sequence %d", index, recorded.Sequence)
		}
		if recorded.OperationID != sink.events[0].OperationID {
			t.Fatalf("event %d belongs to another operation", index)
		}
		if recorded.Source.WorkspaceID != "100" || recorded.Source.Destination != h.root {
			t.Fatalf("event %d has source %#v", index, recorded.Source)
		}
		if recorded.Operation != state.OperationBackup {
			t.Fatalf("event %d has operation %q", index, recorded.Operation)
		}
	}

	completed := sink.events[2]
	if completed.Metadata["archive_path"] != filepath.Join(h.finalRoot(), backupArchiveDirectory) {
		t.Fatalf("completion metadata = %#v", completed.Metadata)
	}
	if sink.events[1].Metadata["result"] != "VERIFIED" || sink.events[1].Outcome != event.OutcomeSucceeded {
		t.Fatalf("verification event = %#v", sink.events[1])
	}
}

// TestCompletionIsNeverEmittedBeforeThePublishedBundleExists is the claim that
// matters most: a consumer reacting to operation.completed must be able to
// open the archive that event names.
func TestCompletionIsNeverEmittedBeforeThePublishedBundleExists(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	sink := &checkingSink{t: t}
	h.app.options.StateRoot = filepath.Join(t.TempDir(), "state")
	h.app.options.EventSink = sink

	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, h.stderr.String())
	}
	if !sink.sawCompletion {
		t.Fatal("no completion event was emitted")
	}
}

// checkingSink inspects the filesystem at the moment each event is emitted.
type checkingSink struct {
	t             *testing.T
	sawCompletion bool
}

func (s *checkingSink) Emit(e event.Event) error {
	switch e.Type {
	case event.TypeOperationCompleted:
		s.sawCompletion = true
		archivePath := e.Metadata["archive_path"]
		if info, err := os.Stat(filepath.Join(archivePath, state.ManifestFilename)); err != nil || !info.Mode().IsRegular() {
			s.t.Errorf("completion was announced before the archive existed at %q: %v", archivePath, err)
		}
		if info, err := os.Stat(e.Metadata["report_path"]); err != nil || !info.Mode().IsRegular() {
			s.t.Errorf("completion was announced before the recovery report existed: %v", err)
		}
	case event.TypeVerificationRecorded:
		if !s.sawCompletion && e.Outcome != event.OutcomeSucceeded {
			s.t.Errorf("verification event = %#v", e)
		}
	}
	return nil
}

func TestAFailedBackupEmitsTheStageReasonAndHealthSeparately(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	sink := &recordingSink{}
	h.app.options.StateRoot = filepath.Join(t.TempDir(), "state")
	h.app.options.EventSink = sink
	h.scanner.err = &clickup.Error{
		Kind: clickup.ErrorNetwork, Operation: "list Spaces",
		Cause: errors.New("provider text that must not travel"),
	}

	if code := h.app.Run([]string{"backup"}); code != 1 {
		t.Fatalf("code=%d", code)
	}
	want := []string{"operation.started", "operation.failed", "connector.unhealthy"}
	if got := sink.types(); !equalStrings(got, want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}

	failed := sink.events[1]
	if failed.Stage != state.StageScan || failed.Outcome != event.OutcomeFailed {
		t.Fatalf("failure event = %#v", failed)
	}
	if failed.Metadata["reason"] != string(connector.HealthReasonNetworkFailure) {
		t.Fatalf("failure reason = %q", failed.Metadata["reason"])
	}
	unhealthy := sink.events[2]
	if unhealthy.Metadata["health_state"] != string(connector.HealthUnavailable) || unhealthy.Outcome != "" {
		t.Fatalf("health event = %#v", unhealthy)
	}
	for _, recorded := range sink.events {
		encoded := fmt.Sprintf("%#v", recorded)
		if strings.Contains(encoded, "provider text that must not travel") || strings.Contains(encoded, h.token) {
			t.Fatalf("an event carried untrusted text or the credential: %s", encoded)
		}
	}
	if len(sink.events) != 3 {
		t.Fatalf("a failed run emitted %d events", len(sink.events))
	}
	for _, recorded := range sink.events {
		if recorded.Type == event.TypeOperationCompleted {
			t.Fatal("a failed run announced a completed operation")
		}
	}
}

func TestARejectedCredentialIsItsOwnEvent(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	sink := &recordingSink{}
	h.app.options.StateRoot = filepath.Join(t.TempDir(), "state")
	h.app.options.EventSink = sink
	h.exporter.err = &clickup.Error{
		Kind: clickup.ErrorAuthentication, Operation: "get authorized user", StatusCode: http.StatusUnauthorized,
	}

	if code := h.app.Run([]string{"backup"}); code != 1 {
		t.Fatalf("code=%d", code)
	}
	want := []string{"operation.started", "operation.failed", "authentication.problem"}
	if got := sink.types(); !equalStrings(got, want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}
	problem := sink.events[2]
	if problem.Metadata["authentication_state"] != string(connector.AuthenticationRejected) {
		t.Fatalf("authentication event = %#v", problem)
	}
	// The provider answered, so its health is not called into question.
	for _, recorded := range sink.events {
		if recorded.Type == event.TypeConnectorUnhealthy {
			t.Fatal("a rejected credential was reported as an unhealthy provider")
		}
	}
}

func TestAnEventFailureNeverChangesWhatTheRunProved(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	h.app.options.StateRoot = filepath.Join(t.TempDir(), "state")
	h.app.options.EventSink = &recordingSink{err: errors.New("event log is full")}

	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("a sealed and verified backup was downgraded by an event failure: code=%d stderr=%q",
			code, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "operational event could not be recorded") {
		t.Fatalf("the event failure was not reported: %q", h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "ALIH — BACKUP COMPLETE") {
		t.Fatal("the completed backup was no longer reported to the user")
	}
	// State is a separate concern and must still be correct.
	record := loadOnlyRecord(t, h.app.options.StateRoot)
	if record.LastSuccess == nil || record.LastVerification == nil {
		t.Fatalf("an event failure damaged the operational state: %#v", record)
	}
}

func TestEventsAreWrittenToTheBoundedLogBesideTheState(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot

	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, h.stderr.String())
	}
	events, unreadable, err := event.Read(stateRoot)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if unreadable != 0 {
		t.Fatalf("the log contains %d unreadable lines", unreadable)
	}
	if len(events) != 3 {
		t.Fatalf("recorded %d events, want 3", len(events))
	}
	content, err := os.ReadFile(filepath.Join(stateRoot, event.LogFilename))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if strings.Contains(string(content), h.token) {
		t.Fatal("the event log contains the credential")
	}
}

func TestManualVerifyEmitsItsOwnVerificationHistory(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%q", code, h.stderr.String())
	}

	sink := &recordingSink{}
	h.app.options.EventSink = sink
	h.verifier.result = "INCOMPLETE"
	archivePath := filepath.Join(h.finalRoot(), backupArchiveDirectory)
	if code := h.app.Run([]string{"verify", "--archive", archivePath}); code != 1 {
		t.Fatalf("verify code=%d", code)
	}

	if got := sink.types(); !equalStrings(got, []string{"verification.recorded"}) {
		t.Fatalf("event sequence = %v", got)
	}
	recorded := sink.events[0]
	if recorded.Outcome != event.OutcomeFailed || recorded.Metadata["result"] != "INCOMPLETE" {
		t.Fatalf("verification event = %#v", recorded)
	}
	if recorded.Operation != state.OperationVerify || recorded.OperationID == "" {
		t.Fatalf("verification event identity = %#v", recorded)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func TestStatusSummarisesTheRecordedHistory(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%q", code, h.stderr.String())
	}

	app, stdout, _ := statusApp(t, stateRoot, Options{})
	if code := app.Run([]string{"status"}); code != statusExitHealthy {
		t.Fatalf("code=%d:\n%s", code, stdout.String())
	}
	for _, expected := range []string{
		"Recent activity: 3 events recorded, 0 failed",
		"Last event: operation.completed (SUCCEEDED)",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("status output missing %q:\n%s", expected, stdout.String())
		}
	}
}

// TestRecordedHistoryNeverDecidesTheStatus is the boundary that keeps events
// honest: state is the authority on what is true, history only says what
// happened.
func TestRecordedHistoryNeverDecidesTheStatus(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%q", code, h.stderr.String())
	}

	// A failure that exists only in history, with no corresponding state.
	sink, err := event.NewFileSink(stateRoot)
	if err != nil {
		t.Fatalf("new sink: %v", err)
	}
	if err := sink.Emit(event.Event{
		SchemaVersion: event.SchemaVersion, Type: event.TypeOperationFailed,
		OperationID: "20260830T130000Z-0000dead", Sequence: 2, RecordedAt: alphaTestTime,
		Source:    event.Source{Connector: "clickup", WorkspaceID: "100", Destination: h.root},
		Operation: state.OperationBackup, Stage: state.StageScan, Outcome: event.OutcomeFailed,
		Message: "An older run failed.", Metadata: map[string]string{"reason": "NETWORK_FAILURE"},
	}); err != nil {
		t.Fatalf("emit historical failure: %v", err)
	}

	app, stdout, _ := statusApp(t, stateRoot, Options{})
	if code := app.Run([]string{"status"}); code != statusExitHealthy {
		t.Fatalf("history changed the verdict: code=%d\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "4 events recorded, 1 failed") {
		t.Fatalf("status did not report the recorded failure:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "Attention:") {
		t.Fatalf("a past failure became a present problem:\n%s", stdout.String())
	}
}

func TestDamagedHistoryIsReportedWithoutChangingTheStatus(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%q", code, h.stderr.String())
	}
	logPath := filepath.Join(stateRoot, event.LogFilename)
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if err := os.WriteFile(logPath, append(before, []byte("{ damaged line\n")...), 0o600); err != nil {
		t.Fatalf("damage log: %v", err)
	}

	app, stdout, _ := statusApp(t, stateRoot, Options{})
	if code := app.Run([]string{"status"}); code != statusExitHealthy {
		t.Fatalf("damaged history changed the exit code: %d\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Damaged event history: 1 line(s) could not be read.") {
		t.Fatalf("status did not report the damage:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "does not depend on that history") {
		t.Fatalf("status did not explain what the damage means:\n%s", stdout.String())
	}
	after, err := os.ReadFile(logPath)
	if err != nil || string(after) != string(append(before, []byte("{ damaged line\n")...)) {
		t.Fatal("status rewrote the damaged history")
	}
}

func TestAScopeWithoutHistorySaysSo(t *testing.T) {
	t.Parallel()
	stateRoot := filepath.Join(t.TempDir(), "state")
	seedRecord(t, stateRoot, func(record *state.Record) {})
	app, stdout, _ := statusApp(t, stateRoot, Options{})
	app.Run([]string{"status"})
	if !strings.Contains(stdout.String(), "Recent activity: no events recorded") {
		t.Fatalf("status invented activity:\n%s", stdout.String())
	}
}

// TestCancellationAtEveryStageFailsWithoutClaimingCompletion covers the whole
// pipeline: wherever an interruption surfaces, the run records a failure at
// that stage and never announces a completed backup.
func TestCancellationAtEveryStageFailsWithoutClaimingCompletion(t *testing.T) {
	t.Parallel()
	stages := []struct {
		name   string
		stage  string
		break_ func(*backupHarness, error)
	}{
		{"scan", state.StageScan, func(h *backupHarness, err error) { h.scanner.err = err }},
		{"extract", state.StageExtract, func(h *backupHarness, err error) { h.extractor.err = err }},
		{"export", state.StageExport, func(h *backupHarness, err error) { h.exporter.err = err }},
		{"verify", state.StageVerify, func(h *backupHarness, err error) { h.verifier.err = err }},
		{"report", state.StageReport, func(h *backupHarness, err error) { h.reporter.err = err }},
	}
	for _, candidate := range stages {
		candidate := candidate
		t.Run(candidate.name, func(t *testing.T) {
			t.Parallel()
			h := newBackupHarness(t, "VERIFIED")
			sink := &recordingSink{}
			h.app.options.StateRoot = filepath.Join(t.TempDir(), "state")
			h.app.options.EventSink = sink
			candidate.break_(h, context.Canceled)

			if code := h.app.Run([]string{"backup"}); code != 1 {
				t.Fatalf("an interrupted run returned %d", code)
			}
			types := sink.types()
			if len(types) < 2 || types[0] != "operation.started" || types[1] != "operation.failed" {
				t.Fatalf("event sequence = %v", types)
			}
			if sink.events[1].Stage != candidate.stage {
				t.Fatalf("failure recorded at stage %q, want %q", sink.events[1].Stage, candidate.stage)
			}
			for _, recorded := range sink.events {
				if recorded.Type == event.TypeOperationCompleted {
					t.Fatal("an interrupted run announced a completed operation")
				}
				if recorded.Type == event.TypeVerificationRecorded {
					t.Fatal("an interrupted run claimed a verification it never finished")
				}
			}
			record := loadOnlyRecord(t, h.app.options.StateRoot)
			if record.LastSuccess != nil {
				t.Fatal("an interrupted run became the last successful backup")
			}
		})
	}
}

func TestAnUnboundedProviderMessageIsTruncatedNotRecordedWhole(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("x", maxEventTextBytes*4)
	safe := safeEventText(huge)
	if len(safe) > maxEventTextBytes {
		t.Fatalf("message length = %d, want at most %d", len(safe), maxEventTextBytes)
	}
	if !strings.HasSuffix(safe, "...") {
		t.Fatalf("a truncated message was not marked as truncated: %q", safe[len(safe)-8:])
	}
	metadata := safeEventMetadata(map[string]string{"reason": "a\x07b", "failed_path": "  ", "archive_path": "/tmp/x"})
	if metadata["reason"] != "a b" {
		t.Fatalf("metadata was not cleaned: %q", metadata["reason"])
	}
	if _, present := metadata["failed_path"]; present {
		t.Fatal("an empty metadata value was recorded")
	}
}
