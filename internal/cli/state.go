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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/event"
	"alih/internal/notify"
	"alih/internal/state"
	"alih/internal/verify"
)

const (
	maxEventTextBytes = 512
	maxEventPathBytes = 4096
)

// Alih's own bounded description of each stage failure. Provider text, response
// bodies, and raw error strings are deliberately never recorded in state; the
// detailed error still reaches the operator on stderr.
var stageFailureMessages = map[string]string{
	state.StagePrepare:      "The run failed while preparing its local working directory.",
	state.StageAuthenticate: "The run failed while establishing authenticated access.",
	state.StageScan:         "The run failed while inventorying the source.",
	state.StageExtract:      "The run failed while extracting raw source evidence.",
	state.StageExport:       "The run failed while building the portable archive.",
	state.StageVerify:       "The run failed while independently verifying the archive.",
	state.StageReport:       "The run failed while producing the recovery report.",
	state.StageFinalize:     "The run failed while publishing the completed backup.",
}

// operationState records what an operation did, without ever deciding whether
// that operation succeeded. A state failure is reported and then set aside: an
// archive that was sealed and verified stays sealed and verified whether or not
// Alih managed to write a note about it.
type operationState struct {
	app     *App
	store   *state.Store
	scope   state.Scope
	attempt state.Attempt
	active  bool

	// Events are the history of the same transitions. They are emitted after
	// the fact they describe exists, and losing them can never change what the
	// operation actually proved.
	sink      event.Sink
	source    event.Source
	sequence  int
	recording bool

	// Notification delivery is a synchronous consumer of the same envelope.
	// Its failures are recorded separately and never disable the durable event
	// log or change the operation's result.
	ctx                  context.Context
	notifier             notify.Notifier
	notificationTargets  []notify.Destination
	notificationSnapshot *state.NotificationState
}

// operationStart is everything known about a run before it does any work.
type operationStart struct {
	operation     string
	scope         state.Scope
	startedAt     time.Time
	workspaceName string
	identity      connector.Identity
	ctx           context.Context
	scheduleID    string
}

// beginOperation records that an operation started, together with who it is
// acting as and what the source calls itself. Neither is an identity claim
// beyond the run that observed it. The returned value is always usable; when
// state is unavailable it simply records nothing further.
func (a *App) beginOperation(start operationStart) *operationState {
	recorder := &operationState{app: a, scope: start.scope.Normalize()}
	if start.ctx == nil {
		start.ctx = context.Background()
	}
	recorder.ctx = start.ctx
	store, err := state.NewStore(a.options.StateRoot)
	if err != nil {
		a.warnState(err)
		return recorder
	}
	operationID, err := state.NewOperationID(start.startedAt, a.options.Entropy)
	if err != nil {
		a.warnState(err)
		return recorder
	}
	recorder.store = store
	recorder.active = true
	recorder.sink = a.eventSink(store)
	recorder.recording = recorder.sink != nil
	recorder.source = event.Source{
		Connector:   recorder.scope.Connector,
		WorkspaceID: recorder.scope.WorkspaceID,
		Destination: recorder.scope.Destination,
	}
	recorder.configureNotifications()
	recorder.attempt = state.Attempt{
		OperationID: operationID,
		Operation:   start.operation,
		Stage:       state.StagePrepare,
		Outcome:     state.OutcomeStarted,
		StartedAt:   start.startedAt.UTC(),
		AlihVersion: a.recordedVersion(),
		ScheduleID:  start.scheduleID,
	}
	recorder.write(func(record *state.Record) {
		attempt := recorder.attempt
		record.LastAttempt = &attempt
		record.WorkspaceName = start.workspaceName
		if start.identity.ID != "" || start.identity.Name != "" {
			account := start.identity
			record.Account = &account
		}
		recorder.applyNotificationConfiguration(record)
	})
	recorder.emit(event.TypeOperationStarted, state.StagePrepare, event.OutcomeStarted,
		"The "+start.operation+" started.", nil)
	return recorder
}

// eventSink returns where this run records its history. An explicitly injected
// sink wins; otherwise the log lives beside the operational state it describes.
func (a *App) eventSink(store *state.Store) event.Sink {
	if a.options.EventSink != nil {
		return a.options.EventSink
	}
	sink, err := event.NewFileSink(store.Root())
	if err != nil {
		a.warnEvent(err)
		return nil
	}
	return sink
}

// emit records one transition. The first failure disables recording for the
// rest of the run: a log that has already refused a write should not be probed
// again and again, and the operation itself is unaffected either way.
func (o *operationState) emit(eventType event.Type, stage, outcome, message string, metadata map[string]string) {
	if o == nil {
		return
	}
	o.sequence++
	if o.attempt.ScheduleID != "" {
		withSchedule := make(map[string]string, len(metadata)+1)
		for key, value := range metadata {
			withSchedule[key] = value
		}
		withSchedule["schedule_id"] = o.attempt.ScheduleID
		metadata = withSchedule
	}
	recorded := event.Event{
		SchemaVersion: event.SchemaVersion, Type: eventType,
		OperationID: o.attempt.OperationID, Sequence: o.sequence,
		RecordedAt: o.app.observedAt(), Source: o.source,
		Operation: o.attempt.Operation, Stage: stage, Outcome: outcome,
		Message: safeEventText(message), Metadata: safeEventMetadata(metadata),
		AlihVersion: o.app.recordedVersion(),
	}
	if o.recording && o.sink != nil {
		if err := o.sink.Emit(recorded); err != nil {
			o.app.warnEvent(err)
			o.recording = false
		}
	}
	o.deliverNotifications(recorded)
}

// skip records a deliberate non-entry into the pipeline. It is neither source
// failure nor success and therefore carries no connector assessment.
func (o *operationState) skip(stage, reason string) {
	if o == nil || !o.active {
		return
	}
	ended := o.app.observedAt()
	o.attempt.Stage = stage
	o.attempt.Outcome = state.OutcomeSkipped
	o.attempt.EndedAt = &ended
	o.attempt.Error = nil
	o.attempt.SkipReason = safeEventText(reason)
	o.write(func(record *state.Record) {
		attempt := o.attempt
		record.LastAttempt = &attempt
	})
	o.emit(event.TypeOperationSkipped, stage, event.OutcomeSkipped,
		"The operation was skipped because its scope was already active.",
		map[string]string{"reason": o.attempt.SkipReason})
}

// safeEventText bounds and cleans Alih's own wording before it becomes history.
func safeEventText(value string) string {
	cleaned := displayValue(strings.TrimSpace(value))
	if len(cleaned) > maxEventTextBytes {
		cleaned = cleaned[:maxEventTextBytes-3] + "..."
	}
	return cleaned
}

func safeEventMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	safe := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cleaned := displayValue(strings.TrimSpace(value))
		if cleaned == "" {
			continue
		}
		if len(cleaned) > maxEventPathBytes {
			cleaned = cleaned[:maxEventPathBytes]
		}
		safe[key] = cleaned
	}
	if len(safe) == 0 {
		return nil
	}
	return safe
}

// warnEvent reports that Alih could not record its own history. It never
// changes an exit code: the work the event describes already happened.
func (a *App) warnEvent(err error) {
	fmt.Fprintf(a.stderr, "alih: operational event could not be recorded: %s\n", safeError(err, ""))
	fmt.Fprintln(a.stderr, "alih: the result above is unaffected; the local event history may be incomplete.")
}

// fail records a stage failure with a stable reason. A typed connector failure
// supplies the reason and Alih's bounded message; anything else stays an
// explicitly unknown failure rather than borrowing untrusted text.
func (o *operationState) fail(stage string, err error, failedPath string) {
	if o == nil || !o.active {
		return
	}
	reason := connector.HealthReasonUnknownFailure
	message := stageFailureMessages[stage]
	if message == "" {
		message = "The run failed."
	}
	assessment, typed := connector.AssessmentFromError(err, o.app.observedAt())
	if typed {
		reason = assessment.Health.Reason
		message = assessment.Health.Message
	}
	ended := o.app.observedAt()
	o.attempt.Stage = stage
	o.attempt.Outcome = state.OutcomeFailed
	o.attempt.EndedAt = &ended
	o.attempt.FailedPath = failedPath
	o.attempt.Error = &state.SafeError{Stage: stage, Reason: reason, Message: message}
	o.write(func(record *state.Record) {
		attempt := o.attempt
		record.LastAttempt = &attempt
		if typed {
			observed := assessment
			record.Assessment = &observed
		}
	})
	o.emit(event.TypeOperationFailed, stage, event.OutcomeFailed, message, map[string]string{
		"reason": string(reason), "failed_path": failedPath,
	})
	if typed {
		o.emitAssessment(stage, assessment)
	}
}

// emitAssessment reports what an observation established about the connector
// and the credential, separately, because a rejected credential and an
// unreachable provider demand different responses.
func (o *operationState) emitAssessment(stage string, assessment connector.OperationalAssessment) {
	health := assessment.Health
	if health.State == connector.HealthDegraded || health.State == connector.HealthUnavailable {
		o.emit(event.TypeConnectorUnhealthy, stage, "", health.Message, map[string]string{
			"health_state": string(health.State), "reason": string(health.Reason),
		})
	}
	authentication := assessment.Authentication
	if authentication.State == connector.AuthenticationRejected ||
		authentication.State == connector.AuthenticationRequired {
		o.emit(event.TypeAuthenticationProblem, stage, "", authentication.Message, map[string]string{
			"authentication_state": string(authentication.State), "reason": string(authentication.Reason),
		})
	}
}

// succeed records that the operation completed. A backup additionally becomes
// the last known successful backup; an operation that produces no archive never
// does, because there would be nothing for a later claim to point at.
func (o *operationState) succeed(stage string, result operationResult) {
	if o == nil || !o.active {
		return
	}
	ended := o.app.observedAt()
	o.attempt.Stage = stage
	o.attempt.Outcome = state.OutcomeSucceeded
	o.attempt.EndedAt = &ended
	o.attempt.Error = nil
	o.attempt.ArchivePath = result.archivePath
	o.attempt.ReportPath = result.reportPath
	o.write(func(record *state.Record) {
		attempt := o.attempt
		record.LastAttempt = &attempt
		if attempt.ArchivePath != "" {
			success := attempt
			record.LastSuccess = &success
		}
		if result.assessment != nil && connector.ValidateOperationalAssessment(*result.assessment) == nil {
			observed := *result.assessment
			record.Assessment = &observed
		}
		if len(result.capabilities) > 0 &&
			connector.ValidateCapabilities(result.capabilitySchemaVersion, result.capabilities) == nil {
			record.CapabilitySchemaVersion = result.capabilitySchemaVersion
			record.Capabilities = connector.CanonicalCapabilities(result.capabilitySchemaVersion, result.capabilities)
		}
		if result.verification != nil {
			verification := *result.verification
			record.LastVerification = &verification
		}
	})
	// History follows the order the facts happened in: the archive was verified
	// before it was published, so the verification is recorded first.
	if result.verification != nil {
		o.emitVerification(*result.verification)
	}
	o.emit(event.TypeOperationCompleted, stage, event.OutcomeSucceeded,
		"The "+o.attempt.Operation+" completed.", map[string]string{
			"archive_path": result.archivePath, "report_path": result.reportPath,
		})
}

// emitVerification records an independent verification result, passing or
// failing, together with the identity of the archive it judged.
func (o *operationState) emitVerification(verification state.Verification) {
	outcome := event.OutcomeFailed
	message := "Verification did not prove this archive: " + verification.Result + "."
	if verification.Result == state.ResultVerified || verification.Result == state.ResultVerifiedWithLimitations {
		outcome = event.OutcomeSucceeded
		message = "Verification proved this archive: " + verification.Result + "."
	}
	o.emit(event.TypeVerificationRecorded, state.StageVerify, outcome, message, map[string]string{
		"result":            verification.Result,
		"archive_path":      verification.Archive.Path,
		"manifest_checksum": verification.Archive.ManifestChecksum,
		"archive_status":    verification.Archive.ArchiveStatus,
	})
}

// operationResult carries what an operation proved, so that state records the
// same evidence the command reported to the user.
type operationResult struct {
	archivePath             string
	reportPath              string
	assessment              *connector.OperationalAssessment
	capabilitySchemaVersion int
	capabilities            []connector.Capability
	verification            *state.Verification
}

// maxManifestBytes bounds the manifest read. An archive manifest is Alih's own
// file, but a bound keeps a damaged or hostile directory from being read into
// memory without limit.
const maxManifestBytes = 32 << 20

// archiveIdentity reads the sealed manifest once and derives both the checksum
// of its exact bytes and the digests it records. The checksum is what later
// proves the verified archive is still the archive on disk.
func archiveIdentity(archivePath string) (state.ArchiveIdentity, error) {
	identity, _, err := readSealedArchive(archivePath)
	return identity, err
}

// readSealedArchive additionally returns what the manifest says about itself,
// for callers that must learn a scope from the archive rather than from the
// directory it happens to sit in.
func readSealedArchive(archivePath string) (state.ArchiveIdentity, archive.Manifest, error) {
	absolute, err := absolutePath(archivePath)
	if err != nil {
		return state.ArchiveIdentity{}, archive.Manifest{}, err
	}
	manifestPath := filepath.Join(absolute, state.ManifestFilename)
	file, err := os.Open(manifestPath)
	if err != nil {
		return state.ArchiveIdentity{}, archive.Manifest{}, fmt.Errorf("read archive manifest: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return state.ArchiveIdentity{}, archive.Manifest{}, fmt.Errorf("read archive manifest: %w", err)
	}
	if len(content) > maxManifestBytes {
		return state.ArchiveIdentity{}, archive.Manifest{}, fmt.Errorf("archive manifest exceeds %d bytes", maxManifestBytes)
	}
	checksum := sha256.Sum256(content)
	var manifest archive.Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return state.ArchiveIdentity{}, archive.Manifest{}, fmt.Errorf("decode archive manifest: %w", err)
	}
	return state.ArchiveIdentity{
		Path:             absolute,
		ManifestChecksum: "sha256:" + hex.EncodeToString(checksum[:]),
		LogicalDigest:    manifest.InputSnapshot.LogicalDigest,
		CapabilityDigest: manifest.InputSnapshot.CapabilityDigest,
		ArchiveStatus:    manifest.Status,
		CompletedAt:      manifest.ArchiveCompletedAt,
	}, manifest, nil
}

// verificationRecord preserves a verification result outside the archive it
// describes, because a sealed manifest correctly keeps saying NOT_RUN.
func (a *App) verificationRecord(report verify.Report, identity state.ArchiveIdentity) state.Verification {
	return state.Verification{
		Result:      report.Result,
		VerifiedAt:  a.observedAt(),
		Archive:     identity,
		Limitations: state.SafeLimitations(report.Limitations),
		AlihVersion: a.recordedVersion(),
	}
}

// emitStandaloneVerification records the history of a verification that was
// not part of a larger operation, such as `alih verify` or reconciliation. It
// carries its own operation identity so the event correlates with nothing it
// did not actually do.
func (a *App) emitStandaloneVerification(store *state.Store, scope state.Scope, verification state.Verification) {
	sink := a.eventSink(store)
	if sink == nil {
		return
	}
	operationID, err := state.NewOperationID(a.observedAt(), a.options.Entropy)
	if err != nil {
		a.warnEvent(err)
		return
	}
	recorder := &operationState{
		app: a, store: store, scope: scope, active: true,
		sink: sink, recording: true,
		ctx: context.Background(),
		source: event.Source{
			Connector: scope.Connector, WorkspaceID: scope.WorkspaceID, Destination: scope.Destination,
		},
		attempt: state.Attempt{OperationID: operationID, Operation: state.OperationVerify},
	}
	recorder.configureNotifications()
	recorder.write(func(record *state.Record) { recorder.applyNotificationConfiguration(record) })
	recorder.emitVerification(verification)
}

// recordVerification stores the result of a manual verification, but only for
// an archive Alih already has a record of. A directory name is not evidence of
// which workspace or destination an archive belongs to, so an unknown archive
// never causes a new scope to be invented.
func (a *App) recordVerification(archivePath string, report verify.Report) {
	if !state.ValidResult(report.Result) {
		return
	}
	absolute, err := absolutePath(archivePath)
	if err != nil {
		return
	}
	store, err := state.NewStore(a.options.StateRoot)
	if err != nil {
		a.warnState(err)
		return
	}
	records, _, err := store.List()
	if err != nil {
		a.warnState(err)
		return
	}
	for _, record := range records {
		if !recordRefersToArchive(record, absolute) {
			continue
		}
		identity, err := archiveIdentity(absolute)
		if err != nil {
			a.warnState(err)
			return
		}
		verification := a.verificationRecord(report, identity)
		if _, err := store.Update(record.Scope, func(stored *state.Record) error {
			stored.UpdatedAt = a.observedAt()
			stored.LastVerification = &verification
			return nil
		}); err != nil {
			a.warnState(err)
			return
		}
		a.emitStandaloneVerification(store, record.Scope, verification)
		return
	}
}

func recordRefersToArchive(record state.Record, archivePath string) bool {
	for _, candidate := range []string{
		attemptArchivePath(record.LastSuccess),
		attemptArchivePath(record.LastAttempt),
		verifiedArchivePath(record.LastVerification),
	} {
		if candidate != "" && candidate == archivePath {
			return true
		}
	}
	return false
}

func attemptArchivePath(attempt *state.Attempt) string {
	if attempt == nil {
		return ""
	}
	return attempt.ArchivePath
}

func verifiedArchivePath(verification *state.Verification) string {
	if verification == nil {
		return ""
	}
	return verification.Archive.Path
}

func (o *operationState) write(mutate func(*state.Record)) {
	if o == nil || !o.active || o.store == nil {
		return
	}
	if _, err := o.store.Update(o.scope, func(record *state.Record) error {
		record.AlihVersion = o.app.recordedVersion()
		record.UpdatedAt = o.app.observedAt()
		mutate(record)
		return nil
	}); err != nil {
		o.app.warnState(err)
		// One failure disables further writes for this run: a record that has
		// already refused an update must not be probed again and again.
		o.active = false
	}
}

// warnState tells the operator that Alih could not record what it did. It never
// changes an exit code, because the work itself is unaffected.
func (a *App) warnState(err error) {
	fmt.Fprintf(a.stderr, "alih: operational state could not be recorded: %s\n", safeError(err, ""))
	fmt.Fprintln(a.stderr, "alih: the result above is unaffected; local status may be incomplete until this is fixed.")
}

func (a *App) recordedVersion() string {
	if version := displayValue(a.options.Version); version != "" {
		return version
	}
	return "dev"
}

// backupScope identifies state by connector, source workspace, and the local
// destination root the bundle was written to.
func backupScope(connectorName, workspaceID, destination string) state.Scope {
	return state.Scope{Connector: connectorName, WorkspaceID: workspaceID, Destination: destination}.Normalize()
}

func absolutePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	return absolute, nil
}
