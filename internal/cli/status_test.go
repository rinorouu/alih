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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"alih/internal/connector"
	"alih/internal/state"
)

// refusingAuthenticator fails the test if status ever contacts the source.
type refusingAuthenticator struct {
	t      *testing.T
	result connector.Authentication
	err    error
	calls  int
	allow  bool
}

func (s *refusingAuthenticator) Name() string { return "clickup" }
func (s *refusingAuthenticator) Authenticate(context.Context, string) (connector.Authentication, error) {
	s.calls++
	if !s.allow {
		s.t.Error("status contacted the source without being asked to refresh")
	}
	return s.result, s.err
}

func statusApp(t *testing.T, stateRoot string, options Options) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	options.StateRoot = stateRoot
	if options.Now == nil {
		options.Now = func() time.Time { return alphaTestTime }
	}
	return New(stdout, stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), options), stdout, stderr
}

func seedRecord(t *testing.T, stateRoot string, mutate func(*state.Record)) state.Record {
	t.Helper()
	store := stateStoreAt(t, stateRoot)
	scope := backupScope("clickup", "100", filepath.Join(t.TempDir(), "Alih"))
	record, err := store.Update(scope, func(record *state.Record) error {
		record.WorkspaceName = "Seeded Workspace"
		record.UpdatedAt = alphaTestTime
		record.AlihVersion = "dev"
		mutate(record)
		return nil
	})
	if err != nil {
		t.Fatalf("seed record: %v", err)
	}
	return record
}

func TestStatusWithoutAnyRecordIsUnknownAndSaysSo(t *testing.T) {
	t.Parallel()
	app, stdout, stderr := statusApp(t, filepath.Join(t.TempDir(), "state"), Options{})
	if code := app.Run([]string{"status"}); code != statusExitUnknown {
		t.Fatalf("code=%d, want %d; stderr=%q", code, statusExitUnknown, stderr.String())
	}
	for _, expected := range []string{"Status: UNKNOWN", "No operation has been recorded yet.", "Source contact: none"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("status output missing %q:\n%s", expected, stdout.String())
		}
	}
}

func TestStatusMakesNoSourceRequestByDefault(t *testing.T) {
	t.Parallel()
	stateRoot := filepath.Join(t.TempDir(), "state")
	seedRecord(t, stateRoot, func(record *state.Record) {
		ended := alphaTestTime
		record.LastAttempt = &state.Attempt{
			OperationID: "20260830T123000Z-0000beef", Operation: state.OperationBackup,
			Stage: state.StageFinalize, Outcome: state.OutcomeSucceeded,
			StartedAt: alphaTestTime.Add(-time.Minute), EndedAt: &ended,
			ArchivePath: filepath.Join(t.TempDir(), "archive"),
		}
	})
	authenticator := &refusingAuthenticator{t: t}
	app, stdout, _ := statusApp(t, stateRoot, Options{Authenticator: authenticator})

	app.Run([]string{"status"})
	if authenticator.calls != 0 {
		t.Fatalf("status made %d authentication requests", authenticator.calls)
	}
	if !strings.Contains(stdout.String(), "Source contact: none") {
		t.Fatalf("status did not state that it stayed offline:\n%s", stdout.String())
	}
}

func TestStatusAfterAVerifiedBackupIsHealthy(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%q", code, h.stderr.String())
	}

	app, stdout, stderr := statusApp(t, stateRoot, Options{})
	if code := app.Run([]string{"status"}); code != statusExitHealthy {
		t.Fatalf("code=%d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	archivePath := filepath.Join(h.finalRoot(), backupArchiveDirectory)
	for _, expected := range []string{
		"Status: HEALTHY", "Workspace: Example Workspace (ID: 100)", "Connector: clickup",
		"Last attempt: backup SUCCEEDED at the finalize stage",
		"Last successful backup: the attempt above",
		"Last verification: VERIFIED", "Verified archive: " + archivePath + " [PRESENT]",
		"Connector health: HEALTHY", "Authentication: AUTHENTICATED",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("status output missing %q:\n%s", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "Attention:") {
		t.Fatalf("a healthy scope reported attention:\n%s", stdout.String())
	}
}

func TestStatusShowsEnabledScheduleWhoseNativeArtifactsAreMissing(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%q", code, h.stderr.String())
	}
	configRoot := filepath.Join(t.TempDir(), "config")
	writeScheduleConfig(t, configRoot, h.root)
	home := filepath.Join(t.TempDir(), "home")
	app, stdout, _ := statusApp(t, stateRoot, Options{
		ScheduleRoot: configRoot, SchedulePlatform: runtime.GOOS,
		ExecutablePath: filepath.Join(home, "bin", "alih"), UserHome: home, UserID: "1000",
	})
	if code := app.Run([]string{"status"}); code != statusExitAttention {
		t.Fatalf("status code=%d output=%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Schedule daily-main: enabled=true") ||
		!strings.Contains(stdout.String(), "artifacts match: false") {
		t.Fatalf("status did not report missing native schedule artifacts:\n%s", stdout.String())
	}
}

func TestStatusNeedsAttentionWhenTheVerifiedArchiveNoLongerMatches(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%q", code, h.stderr.String())
	}
	manifest := filepath.Join(h.finalRoot(), backupArchiveDirectory, state.ManifestFilename)
	content, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := os.WriteFile(manifest, append(content, '\n'), 0o600); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	app, stdout, _ := statusApp(t, stateRoot, Options{})
	if code := app.Run([]string{"status"}); code != statusExitAttention {
		t.Fatalf("code=%d, want %d:\n%s", code, statusExitAttention, stdout.String())
	}
	if !strings.Contains(stdout.String(), "has changed since it was verified") ||
		!strings.Contains(stdout.String(), "[CHANGED]") {
		t.Fatalf("status did not explain the weakened claim:\n%s", stdout.String())
	}
}

func TestStatusNeedsAttentionAfterAFailedOrInterruptedRun(t *testing.T) {
	t.Parallel()

	t.Run("failed", func(t *testing.T) {
		t.Parallel()
		h := newBackupHarness(t, "VERIFIED")
		stateRoot := filepath.Join(t.TempDir(), "state")
		h.app.options.StateRoot = stateRoot
		h.exporter.err = errors.New("export stopped")
		if code := h.app.Run([]string{"backup"}); code != 1 {
			t.Fatalf("backup code=%d", code)
		}
		app, stdout, _ := statusApp(t, stateRoot, Options{})
		if code := app.Run([]string{"status"}); code != statusExitAttention {
			t.Fatalf("code=%d, want %d:\n%s", code, statusExitAttention, stdout.String())
		}
		if !strings.Contains(stdout.String(), "the last backup attempt failed at the export stage") {
			t.Fatalf("status did not name the failed stage:\n%s", stdout.String())
		}
	})

	t.Run("interrupted", func(t *testing.T) {
		t.Parallel()
		stateRoot := filepath.Join(t.TempDir(), "state")
		seedRecord(t, stateRoot, func(record *state.Record) {
			record.LastAttempt = &state.Attempt{
				OperationID: "20260830T123000Z-0000beef", Operation: state.OperationBackup,
				Stage: state.StagePrepare, Outcome: state.OutcomeStarted, StartedAt: alphaTestTime,
			}
		})
		app, stdout, _ := statusApp(t, stateRoot, Options{})
		if code := app.Run([]string{"status"}); code != statusExitAttention {
			t.Fatalf("code=%d, want %d:\n%s", code, statusExitAttention, stdout.String())
		}
		if !strings.Contains(stdout.String(), "may have been interrupted") {
			t.Fatalf("status did not report the interrupted run:\n%s", stdout.String())
		}
	})
}

func TestStatusMarksAnOldObservationAsStaleWithoutCallingItAFailure(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%q", code, h.stderr.String())
	}

	later := alphaTestTime.Add(72 * time.Hour)
	app, stdout, _ := statusApp(t, stateRoot, Options{Now: func() time.Time { return later }})
	if code := app.Run([]string{"status"}); code != statusExitHealthy {
		t.Fatalf("a stale observation was treated as a failure: code=%d\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "stale") {
		t.Fatalf("status presented an old observation as current:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "observed 3d ago") {
		t.Fatalf("status did not report the observation age:\n%s", stdout.String())
	}
}

func TestStatusJSONIsPureStableAndFreeOfTheCredential(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%q", code, h.stderr.String())
	}

	app, stdout, stderr := statusApp(t, stateRoot, Options{})
	if code := app.Run([]string{"status", "--json"}); code != statusExitHealthy {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON mode wrote diagnostics to stdout's stream: %q", stderr.String())
	}
	var document statusDocument
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("status --json is not a single JSON document: %v\n%s", err, stdout.String())
	}
	if document.SchemaVersion != statusSchemaVersion || document.Kind != "operational_status" {
		t.Fatalf("document identity = %d %q", document.SchemaVersion, document.Kind)
	}
	if document.Status != StatusHealthy || !document.Offline || len(document.Scopes) != 1 {
		t.Fatalf("document = %#v", document)
	}
	scope := document.Scopes[0]
	if scope.Verification == nil || scope.Verification.Condition != string(state.ArchivePresent) {
		t.Fatalf("verification = %#v", scope.Verification)
	}
	if scope.LastSuccess == nil || scope.Health == nil || scope.Authentication == nil {
		t.Fatalf("scope = %#v", scope)
	}
	if strings.Contains(stdout.String(), h.token) {
		t.Fatal("status --json exposed the credential")
	}

	second, _, _ := statusApp(t, stateRoot, Options{})
	secondOut := &bytes.Buffer{}
	second.stdout = secondOut
	second.Run([]string{"status", "--json"})
	if secondOut.String() != stdout.String() {
		t.Fatal("two identical status reads produced different documents")
	}
}

func TestStatusReportsUnreadableStateWithoutRewritingIt(t *testing.T) {
	t.Parallel()
	stateRoot := filepath.Join(t.TempDir(), "state")
	seedRecord(t, stateRoot, func(record *state.Record) {})
	broken := filepath.Join(stateRoot, "broken.json")
	content := []byte("{ this is not state")
	if err := os.WriteFile(broken, content, 0o600); err != nil {
		t.Fatalf("write broken state: %v", err)
	}

	app, stdout, _ := statusApp(t, stateRoot, Options{})
	if code := app.Run([]string{"status"}); code != statusExitUnreadable {
		t.Fatalf("code=%d, want %d:\n%s", code, statusExitUnreadable, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Unreadable state file: "+broken) {
		t.Fatalf("status did not name the unreadable file:\n%s", stdout.String())
	}
	after, err := os.ReadFile(broken)
	if err != nil || string(after) != string(content) {
		t.Fatalf("status modified the unreadable file: %v", err)
	}
}

func TestStatusRefreshMakesOneRequestAndUpdatesOnlyWhatItCovers(t *testing.T) {
	t.Parallel()
	stateRoot := filepath.Join(t.TempDir(), "state")
	covered := seedRecord(t, stateRoot, func(record *state.Record) {})
	uncovered := backupScope("clickup", "200", filepath.Join(t.TempDir(), "Alih"))
	if _, err := stateStoreAt(t, stateRoot).Update(uncovered, func(record *state.Record) error {
		record.WorkspaceName = "Not accessible now"
		record.UpdatedAt = alphaTestTime
		return nil
	}); err != nil {
		t.Fatalf("seed second scope: %v", err)
	}

	assessment, err := connector.HealthyAssessment("clickup", connector.HealthBasisAuthentication,
		alphaTestTime, connector.AuthenticationAuthenticated, nil)
	if err != nil {
		t.Fatalf("build assessment: %v", err)
	}
	authenticator := &refusingAuthenticator{t: t, allow: true, result: connector.Authentication{
		Identity:   connector.Identity{ID: "u1", Name: "Tester"},
		Workspaces: []connector.Workspace{{ID: "100", Name: "Seeded Workspace"}},
		Assessment: assessment,
	}}
	app, stdout, stderr := statusApp(t, stateRoot, Options{
		Authenticator:   authenticator,
		CredentialStore: &stubCredentialStore{loaded: "pk_status_secret"},
	})

	if code := app.Run([]string{"status", "--refresh"}); code != statusExitUnknown {
		// The uncovered scope has no observation of its own and stays unknown.
		t.Fatalf("code=%d, want %d; stdout=%s stderr=%s", code, statusExitUnknown, stdout.String(), stderr.String())
	}
	if authenticator.calls != 1 {
		t.Fatalf("refresh made %d authentication requests, want exactly 1", authenticator.calls)
	}
	if !strings.Contains(stdout.String(), "Source contact: one authentication request") {
		t.Fatalf("status did not disclose the request it made:\n%s", stdout.String())
	}

	store := stateStoreAt(t, stateRoot)
	refreshed, err := store.Load(covered.Scope)
	if err != nil {
		t.Fatalf("load refreshed record: %v", err)
	}
	if refreshed.Assessment == nil || refreshed.Assessment.Health.Basis != connector.HealthBasisAuthentication {
		t.Fatalf("covered scope was not refreshed: %#v", refreshed.Assessment)
	}
	untouched, err := store.Load(uncovered)
	if err != nil {
		t.Fatalf("load untouched record: %v", err)
	}
	if untouched.Assessment != nil {
		t.Fatal("refresh described a workspace the authentication did not cover")
	}
}

func TestStatusRefreshRecordsARejectedCredentialForEveryScopeOfThatConnector(t *testing.T) {
	t.Parallel()
	stateRoot := filepath.Join(t.TempDir(), "state")
	seeded := seedRecord(t, stateRoot, func(record *state.Record) {})
	authenticator := &refusingAuthenticator{t: t, allow: true, err: &authRejection{}}
	app, stdout, _ := statusApp(t, stateRoot, Options{
		Authenticator:   authenticator,
		CredentialStore: &stubCredentialStore{loaded: "pk_status_secret"},
	})

	if code := app.Run([]string{"status", "--refresh"}); code != statusExitAttention {
		t.Fatalf("code=%d, want %d:\n%s", code, statusExitAttention, stdout.String())
	}
	record, err := stateStoreAt(t, stateRoot).Load(seeded.Scope)
	if err != nil {
		t.Fatalf("load record: %v", err)
	}
	if record.Assessment == nil || record.Assessment.Authentication.State != connector.AuthenticationRejected {
		t.Fatalf("rejected credential was not recorded: %#v", record.Assessment)
	}
	if !strings.Contains(stdout.String(), "authentication was REJECTED") {
		t.Fatalf("status did not report the rejection:\n%s", stdout.String())
	}
}

// authRejection is a typed connector failure: a provider that answered and
// refused the credential, which says nothing bad about the provider itself.
type authRejection struct{}

func (e *authRejection) Error() string { return "credential rejected" }
func (e *authRejection) OperationalAssessment(observedAt time.Time) connector.OperationalAssessment {
	return connector.OperationalAssessment{
		SchemaVersion: connector.HealthSchemaVersion,
		Health: connector.Health{
			SchemaVersion: connector.HealthSchemaVersion, Connector: "clickup",
			State: connector.HealthHealthy, Basis: connector.HealthBasisAuthentication,
			ObservedAt: observedAt.UTC(), Reason: connector.HealthReasonNone,
			Message: "The provider responded and refused the credential.",
		},
		Authentication: connector.AuthenticationObservation{
			State: connector.AuthenticationRejected, ObservedAt: observedAt.UTC(),
			Reason: connector.HealthReasonAuthenticationRejected, Message: "The provider rejected the credential.",
		},
	}
}

func TestStatusRejectsArgumentsAndDocumentsItsExitCodes(t *testing.T) {
	t.Parallel()
	app, _, stderr := statusApp(t, filepath.Join(t.TempDir(), "state"), Options{})
	if code := app.Run([]string{"status", "everything"}); code != 2 {
		t.Fatalf("code=%d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "positional arguments are not accepted") {
		t.Fatalf("stderr=%q", stderr.String())
	}

	helpApp, stdout, _ := statusApp(t, filepath.Join(t.TempDir(), "state"), Options{})
	if code := helpApp.Run([]string{"status", "--help"}); code != 0 {
		t.Fatalf("help code=%d", code)
	}
	for _, expected := range []string{
		"makes no source request", "--refresh", "Exit codes:",
		"1  at least one scope needs attention", "4  local state exists but cannot be read",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("status help missing %q:\n%s", expected, stdout.String())
		}
	}
}

func TestStatusNamesTheCapabilityThatWasNotObtained(t *testing.T) {
	t.Parallel()
	stateRoot := filepath.Join(t.TempDir(), "state")
	seedRecord(t, stateRoot, func(record *state.Record) {
		record.CapabilitySchemaVersion = connector.CapabilitySchemaVersion
		record.Capabilities = []connector.Capability{
			{
				ID: connector.CapabilityItems, Name: "Tasks and subtasks",
				Requirement: connector.CapabilityRequired, Implementation: connector.CapabilitySupported,
				State: connector.CapabilitySupported, Availability: connector.CapabilityAvailabilityAvailable,
			},
			{
				ID: connector.CapabilityAttachmentContent, Name: "Attachment content",
				Requirement: connector.CapabilityRequired, Implementation: connector.CapabilitySupported,
				State: connector.CapabilitySupported, Availability: connector.CapabilityAvailabilityFailed,
			},
		}
		ended := alphaTestTime
		record.LastAttempt = &state.Attempt{
			OperationID: "20260830T123000Z-0000beef", Operation: state.OperationBackup,
			Stage: state.StageFinalize, Outcome: state.OutcomeSucceeded,
			StartedAt: alphaTestTime.Add(-time.Minute), EndedAt: &ended,
			ArchivePath: filepath.Join(t.TempDir(), "archive"),
		}
	})

	app, stdout, _ := statusApp(t, stateRoot, Options{})
	app.Run([]string{"status"})
	for _, expected := range []string{
		"Capabilities: 2 recorded, 1 not fully available",
		"attachment_content: FAILED (REQUIRED)",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("status output missing %q:\n%s", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "items: ") {
		t.Fatalf("an available capability was listed as a limitation:\n%s", stdout.String())
	}
}

func TestStatusSurvivesAClockThatMovedBackwards(t *testing.T) {
	t.Parallel()
	h := newBackupHarness(t, "VERIFIED")
	stateRoot := filepath.Join(t.TempDir(), "state")
	h.app.options.StateRoot = stateRoot
	if code := h.app.Run([]string{"backup"}); code != 0 {
		t.Fatalf("backup code=%d stderr=%q", code, h.stderr.String())
	}

	// The system clock now reads earlier than the recorded observation.
	earlier := alphaTestTime.Add(-48 * time.Hour)
	app, stdout, stderr := statusApp(t, stateRoot, Options{Now: func() time.Time { return earlier }})
	if code := app.Run([]string{"status", "--json"}); code != statusExitHealthy {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var document statusDocument
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("status --json: %v", err)
	}
	scope := document.Scopes[0]
	if scope.Health.AgeSeconds < 0 || scope.Verification.AgeSeconds < 0 {
		t.Fatalf("a backwards clock produced a negative age: %#v", scope)
	}
	if scope.Health.Stale {
		t.Fatal("a backwards clock made a fresh observation look stale")
	}
}
