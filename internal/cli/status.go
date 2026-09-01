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
	"io"
	"sort"
	"strings"
	"time"

	"alih/internal/connector"
	"alih/internal/event"
	"alih/internal/schedule"
	"alih/internal/state"
)

// Status outcomes. They are ordered by how much attention they demand, and the
// process exit code follows the same order.
const (
	StatusHealthy    = "HEALTHY"
	StatusUnknown    = "UNKNOWN"
	StatusAttention  = "ATTENTION"
	StatusUnreadable = "UNREADABLE"
)

// Exit codes are part of the automation contract and are documented in help.
const (
	statusExitHealthy    = 0
	statusExitAttention  = 1
	statusExitUnknown    = 3
	statusExitUnreadable = 4
)

// staleAfter is when a stored observation stops being presented as a current
// one. It never turns a healthy record into a failing one; it only stops Alih
// from implying that an old observation still describes the source right now.
const staleAfter = 24 * time.Hour

// statusSchemaVersion identifies the machine-readable status contract.
const statusSchemaVersion = 2

type statusObservation struct {
	State      string    `json:"state"`
	Reason     string    `json:"reason,omitempty"`
	Message    string    `json:"message,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
	AgeSeconds int64     `json:"age_seconds"`
	Stale      bool      `json:"stale"`
}

type statusArchive struct {
	Path             string     `json:"path"`
	Condition        string     `json:"condition"`
	Result           string     `json:"result"`
	VerifiedAt       time.Time  `json:"verified_at"`
	AgeSeconds       int64      `json:"age_seconds"`
	ManifestChecksum string     `json:"manifest_checksum"`
	ArchiveStatus    string     `json:"archive_status"`
	CompletedAt      *time.Time `json:"archive_completed_at,omitempty"`
	Limitations      []string   `json:"limitations,omitempty"`
}

// statusEvent is one recorded transition, summarised. Events are history and
// never authority: they add context to a scope, and nothing here may change the
// verdict that the operational state layer already established.
type statusEvent struct {
	Type        string    `json:"type"`
	Outcome     string    `json:"outcome,omitempty"`
	Stage       string    `json:"stage,omitempty"`
	OperationID string    `json:"operation_id"`
	Sequence    int       `json:"sequence"`
	RecordedAt  time.Time `json:"recorded_at"`
	AgeSeconds  int64     `json:"age_seconds"`
	Message     string    `json:"message"`
}

type statusActivity struct {
	RecordedEvents int          `json:"recorded_events"`
	FailedEvents   int          `json:"failed_events"`
	Last           *statusEvent `json:"last_event,omitempty"`
}

type statusNotifications struct {
	CheckedAt      time.Time                    `json:"checked_at"`
	AgeSeconds     int64                        `json:"age_seconds"`
	DestinationIDs []string                     `json:"destination_ids,omitempty"`
	Problem        *state.NotificationProblem   `json:"problem,omitempty"`
	LastDeliveries []state.NotificationDelivery `json:"last_deliveries,omitempty"`
}

type statusScope struct {
	Connector     string              `json:"connector"`
	WorkspaceID   string              `json:"workspace_id"`
	WorkspaceName string              `json:"workspace_name,omitempty"`
	Destination   string              `json:"destination"`
	Account       *connector.Identity `json:"account,omitempty"`
	Status        string              `json:"status"`
	Attention     []string            `json:"attention,omitempty"`

	Health         *statusObservation `json:"health,omitempty"`
	Authentication *statusObservation `json:"authentication,omitempty"`

	CapabilitySchemaVersion int                    `json:"capability_schema_version,omitempty"`
	Capabilities            []connector.Capability `json:"capabilities,omitempty"`

	LastAttempt  *state.Attempt `json:"last_attempt,omitempty"`
	LastSuccess  *state.Attempt `json:"last_success,omitempty"`
	Verification *statusArchive `json:"last_verification,omitempty"`

	StateRevision uint64    `json:"state_revision"`
	StateUpdated  time.Time `json:"state_updated_at"`

	RecentActivity *statusActivity      `json:"recent_activity,omitempty"`
	Notifications  *statusNotifications `json:"notifications,omitempty"`
}

type statusDocument struct {
	SchemaVersion   int           `json:"schema_version"`
	Kind            string        `json:"kind"`
	AlihVersion     string        `json:"alih_version"`
	GeneratedAt     time.Time     `json:"generated_at"`
	Status          string        `json:"status"`
	Offline         bool          `json:"offline"`
	StateDirectory  string        `json:"state_directory"`
	Scopes          []statusScope `json:"scopes"`
	UnreadableState []string      `json:"unreadable_state_files,omitempty"`
	// Reconciliation is present only when the operator asked Alih to read the
	// destination. Status never walks a filesystem on its own.
	Reconciliation *reconcileOutcome `json:"reconciliation,omitempty"`
	// UnreadableEventLines counts damaged history. It never changes the status
	// itself, because the event log is a record of what happened and not the
	// authority on what is true now.
	UnreadableEventLines int              `json:"unreadable_event_lines,omitempty"`
	Schedules            []statusSchedule `json:"schedules,omitempty"`
	ScheduleProblem      string           `json:"schedule_problem,omitempty"`
}

type statusSchedule struct {
	ID               string           `json:"id"`
	Enabled          bool             `json:"enabled"`
	WorkspaceID      string           `json:"workspace_id"`
	Destination      string           `json:"destination"`
	Cadence          schedule.Cadence `json:"cadence"`
	ArtifactsMatch   *bool            `json:"artifacts_match,omitempty"`
	ChangedArtifacts []string         `json:"changed_artifacts,omitempty"`
}

func (a *App) runStatus(args []string) int {
	flags := flag.NewFlagSet("alih status", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() { fmt.Fprint(a.stdout, statusHelpText) }
	asJSON := flags.Bool("json", false, "print the machine-readable status document")
	refresh := flags.Bool("refresh", false, "make one authentication request before reporting")
	reconcile := flags.Bool("reconcile", false, "verify archives found in the backup destination and record what they prove")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(a.stderr, "alih status: positional arguments are not accepted")
		return 2
	}

	store, err := state.NewStore(a.options.StateRoot)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih status: %s\n", safeError(err, ""))
		return statusExitUnreadable
	}
	if *refresh {
		// The only outbound request status can ever make is this one, and only
		// because the operator asked for it on the command line.
		a.refreshObservations(store)
	}
	var reconciliation *reconcileOutcome
	if *reconcile {
		root, err := a.backupRoot()
		if err != nil {
			fmt.Fprintf(a.stderr, "alih status: --reconcile could not resolve the backup destination: %s\n", safeError(err, ""))
			return statusExitUnreadable
		}
		outcome := a.reconcileDestination(store, root)
		reconciliation = &outcome
	}
	records, unreadable, err := store.List()
	if err != nil {
		fmt.Fprintf(a.stderr, "alih status: %s\n", safeError(err, ""))
		return statusExitUnreadable
	}

	history, unreadableEvents, historyErr := event.Read(store.Root())
	if historyErr != nil {
		// History that cannot be read is reported, never fatal: status is still
		// accurate without it.
		fmt.Fprintf(a.stderr, "alih status: event history could not be read: %s\n", safeError(historyErr, ""))
	}

	document := a.buildStatus(store, records, unreadable, !*refresh)
	a.attachScheduleStatus(&document)
	document.Reconciliation = reconciliation
	document.UnreadableEventLines = unreadableEvents
	attachActivity(&document, history, a.observedAt())
	if *asJSON {
		encoder := json.NewEncoder(a.stdout)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(true)
		if err := encoder.Encode(document); err != nil {
			fmt.Fprintf(a.stderr, "alih status: encode status: %v\n", err)
			return statusExitUnreadable
		}
	} else {
		writeStatusText(a.stdout, document)
	}
	return statusExitCode(document.Status)
}

func (a *App) attachScheduleStatus(document *statusDocument) {
	if document == nil {
		return
	}
	config, err := schedule.Load(a.options.ScheduleRoot)
	if errors.Is(err, schedule.ErrNotConfigured) {
		return
	}
	if err != nil {
		document.ScheduleProblem = "The local schedule configuration is not usable."
		document.Status = worseStatus(document.Status, StatusAttention)
		fmt.Fprintf(a.stderr, "alih status: schedule configuration is not usable: %s\n", safeError(err, ""))
		return
	}
	for _, definition := range config.Schedules {
		described := statusSchedule{
			ID: definition.ID, Enabled: definition.Enabled, WorkspaceID: definition.WorkspaceID,
			Destination: definition.Destination, Cadence: definition.Cadence,
		}
		plan, planErr := a.schedulePlan(definition, a.schedulePlatform())
		if planErr == nil {
			installed, changed, inspectErr := schedule.Inspect(plan)
			if inspectErr == nil {
				described.ArtifactsMatch = &installed
				described.ChangedArtifacts = changed
				if definition.Enabled && !installed {
					document.Status = worseStatus(document.Status, StatusAttention)
				}
			}
		} else if definition.Enabled {
			document.Status = worseStatus(document.Status, StatusAttention)
		}
		document.Schedules = append(document.Schedules, described)
	}
}

func statusExitCode(status string) int {
	switch status {
	case StatusUnreadable:
		return statusExitUnreadable
	case StatusAttention:
		return statusExitAttention
	case StatusUnknown:
		return statusExitUnknown
	default:
		return statusExitHealthy
	}
}

func (a *App) buildStatus(store *state.Store, records []state.Record, unreadable []string, offline bool) statusDocument {
	now := a.observedAt()
	document := statusDocument{
		SchemaVersion: statusSchemaVersion, Kind: "operational_status",
		AlihVersion: a.recordedVersion(), GeneratedAt: now, Offline: offline,
		StateDirectory: store.Root(), Scopes: make([]statusScope, 0, len(records)),
		UnreadableState: append([]string(nil), unreadable...),
	}
	worst := StatusHealthy
	if len(records) == 0 {
		worst = StatusUnknown
	}
	for _, record := range records {
		scope := describeRecord(record, now)
		document.Scopes = append(document.Scopes, scope)
		worst = worseStatus(worst, scope.Status)
	}
	if len(unreadable) > 0 {
		worst = worseStatus(worst, StatusUnreadable)
	}
	document.Status = worst
	return document
}

// describeRecord turns one stored record into what can be said about it right
// now. Everything it reports is either persisted evidence or an observation
// made here about the local filesystem; nothing is inferred from absence.
func describeRecord(record state.Record, now time.Time) statusScope {
	scope := statusScope{
		Connector: record.Scope.Connector, WorkspaceID: record.Scope.WorkspaceID,
		WorkspaceName: record.WorkspaceName, Destination: record.Scope.Destination,
		Account: record.Account, LastAttempt: record.LastAttempt, LastSuccess: record.LastSuccess,
		CapabilitySchemaVersion: record.CapabilitySchemaVersion, Capabilities: record.Capabilities,
		StateRevision: record.Revision, StateUpdated: record.UpdatedAt,
	}
	var attention []string
	unknown := false

	if record.Assessment != nil {
		health := record.Assessment.Health
		scope.Health = &statusObservation{
			State: string(health.State), Reason: string(health.Reason), Message: health.Message,
			ObservedAt: health.ObservedAt, AgeSeconds: ageSeconds(health.ObservedAt, now),
			Stale: state.Age(health.ObservedAt, now) > staleAfter,
		}
		authentication := record.Assessment.Authentication
		scope.Authentication = &statusObservation{
			State: string(authentication.State), Reason: string(authentication.Reason),
			Message: authentication.Message, ObservedAt: authentication.ObservedAt,
			AgeSeconds: ageSeconds(authentication.ObservedAt, now),
			Stale:      state.Age(authentication.ObservedAt, now) > staleAfter,
		}
		switch health.State {
		case connector.HealthDegraded, connector.HealthUnavailable:
			attention = append(attention, fmt.Sprintf("connector health was %s (%s)", health.State, health.Reason))
		case connector.HealthUnknown:
			unknown = true
		}
		switch authentication.State {
		case connector.AuthenticationRejected, connector.AuthenticationRequired:
			attention = append(attention, fmt.Sprintf("authentication was %s", authentication.State))
		case connector.AuthenticationUnknown:
			unknown = true
		}
	} else {
		unknown = true
	}

	switch {
	case record.LastAttempt == nil:
		unknown = true
	case record.LastAttempt.Outcome == state.OutcomeFailed:
		attention = append(attention, fmt.Sprintf("the last %s attempt failed at the %s stage",
			record.LastAttempt.Operation, record.LastAttempt.Stage))
	case record.LastAttempt.Outcome == state.OutcomeStarted:
		attention = append(attention, fmt.Sprintf("the last %s attempt never recorded an end and may have been interrupted",
			record.LastAttempt.Operation))
	case record.LastAttempt.Outcome == state.OutcomeSkipped:
		attention = append(attention, fmt.Sprintf("the last %s attempt was skipped (%s)",
			record.LastAttempt.Operation, record.LastAttempt.SkipReason))
	}

	if record.LastVerification != nil {
		verification := *record.LastVerification
		condition := state.InspectArchive(verification.Archive)
		scope.Verification = &statusArchive{
			Path: verification.Archive.Path, Condition: string(condition), Result: verification.Result,
			VerifiedAt: verification.VerifiedAt, AgeSeconds: ageSeconds(verification.VerifiedAt, now),
			ManifestChecksum: verification.Archive.ManifestChecksum,
			ArchiveStatus:    verification.Archive.ArchiveStatus,
			CompletedAt:      verification.Archive.CompletedAt,
			Limitations:      verification.Limitations,
		}
		if verification.Result != state.ResultVerified && verification.Result != state.ResultVerifiedWithLimitations {
			attention = append(attention, "the last verification result was "+verification.Result)
		}
		switch condition {
		case state.ArchiveMissing:
			attention = append(attention, "the verified archive is no longer at its recorded path")
		case state.ArchiveChanged:
			attention = append(attention, "the verified archive has changed since it was verified")
		case state.ArchiveUnreadable:
			attention = append(attention, "the verified archive can no longer be read")
		}
	} else if record.LastSuccess != nil {
		unknown = true
	}

	if notifications := record.Notifications; notifications != nil {
		scope.Notifications = &statusNotifications{
			CheckedAt: notifications.CheckedAt, AgeSeconds: ageSeconds(notifications.CheckedAt, now),
			DestinationIDs: append([]string(nil), notifications.DestinationIDs...),
			Problem:        notifications.Problem,
			LastDeliveries: append([]state.NotificationDelivery(nil), notifications.LastDeliveries...),
		}
		if notifications.Problem != nil {
			attention = append(attention, "notification configuration needs attention ("+notifications.Problem.Reason+")")
		}
		for _, delivery := range notifications.LastDeliveries {
			if !delivery.Delivered {
				attention = append(attention, fmt.Sprintf("notification destination %s did not accept %s (%s)",
					delivery.DestinationID, delivery.EventType, delivery.Reason))
			}
		}
	}

	sort.Strings(attention)
	scope.Attention = attention
	switch {
	case len(attention) > 0:
		scope.Status = StatusAttention
	case unknown:
		scope.Status = StatusUnknown
	default:
		scope.Status = StatusHealthy
	}
	return scope
}

// attachActivity summarises the recorded history of each scope. It adds
// context only: the status of a scope was already decided from persisted state.
func attachActivity(document *statusDocument, history []event.Event, now time.Time) {
	if document == nil {
		return
	}
	for index := range document.Scopes {
		scope := &document.Scopes[index]
		source := event.Source{
			Connector: scope.Connector, WorkspaceID: scope.WorkspaceID, Destination: scope.Destination,
		}
		recorded := event.Latest(history, source, 0)
		activity := statusActivity{RecordedEvents: len(recorded)}
		for _, entry := range recorded {
			if entry.Outcome == event.OutcomeFailed || entry.Type == event.TypeNotificationProblem {
				activity.FailedEvents++
			}
		}
		if len(recorded) > 0 {
			newest := recorded[0]
			activity.Last = &statusEvent{
				Type: string(newest.Type), Outcome: newest.Outcome, Stage: newest.Stage,
				OperationID: newest.OperationID, Sequence: newest.Sequence,
				RecordedAt: newest.RecordedAt, AgeSeconds: ageSeconds(newest.RecordedAt, now),
				Message: newest.Message,
			}
		}
		scope.RecentActivity = &activity
	}
}

func ageSeconds(observedAt, now time.Time) int64 {
	age := state.Age(observedAt, now)
	if age < 0 {
		// A clock that moved backwards must not be rendered as a negative age
		// or as a fresher observation than it is.
		return 0
	}
	return int64(age / time.Second)
}

func worseStatus(current, candidate string) string {
	if statusSeverity(candidate) > statusSeverity(current) {
		return candidate
	}
	return current
}

func statusSeverity(status string) int {
	switch status {
	case StatusUnreadable:
		return 3
	case StatusAttention:
		return 2
	case StatusUnknown:
		return 1
	default:
		return 0
	}
}

// refreshObservations makes the one outbound request status is ever allowed to
// make: a credential validation. It updates health and authentication for the
// scopes that request actually covers, and never invents a new scope.
func (a *App) refreshObservations(store *state.Store) {
	if a.options.Authenticator == nil {
		fmt.Fprintln(a.stderr, "alih status: --refresh is unavailable because no connector is configured")
		return
	}
	records, _, err := store.List()
	if err != nil {
		a.warnState(err)
		return
	}
	token, _, err := a.authenticationToken()
	if err != nil {
		fmt.Fprintf(a.stderr, "alih status: --refresh could not load a credential: %s\n", safeError(err, ""))
		return
	}
	connectorName := a.options.Authenticator.Name()
	authentication, authErr := a.options.Authenticator.Authenticate(context.Background(), token)
	if authErr != nil {
		fmt.Fprintf(a.stderr, "alih status: --refresh failed: %s\n", safeError(authErr, token))
		assessment, typed := connector.AssessmentFromError(authErr, a.observedAt())
		if !typed {
			return
		}
		// A rejected or unreachable credential is a fact about the connector,
		// so it applies to every scope that uses it.
		for _, record := range records {
			if record.Scope.Connector != connectorName {
				continue
			}
			a.applyAssessment(store, record.Scope, assessment)
		}
		return
	}
	accessible := make(map[string]struct{}, len(authentication.Workspaces))
	for _, workspace := range authentication.Workspaces {
		accessible[workspace.ID] = struct{}{}
	}
	for _, record := range records {
		if record.Scope.Connector != connectorName {
			continue
		}
		if _, ok := accessible[record.Scope.WorkspaceID]; !ok {
			// This observation says nothing about a workspace it did not cover.
			continue
		}
		a.applyAssessment(store, record.Scope, authentication.Assessment)
	}
}

func (a *App) applyAssessment(store *state.Store, scope state.Scope, assessment connector.OperationalAssessment) {
	if connector.ValidateOperationalAssessment(assessment) != nil {
		return
	}
	if _, err := store.Update(scope, func(record *state.Record) error {
		observed := assessment
		record.Assessment = &observed
		record.UpdatedAt = a.observedAt()
		record.AlihVersion = a.recordedVersion()
		return nil
	}); err != nil {
		a.warnState(err)
	}
}

func writeStatusText(output io.Writer, document statusDocument) {
	fmt.Fprintln(output, "ALIH — OPERATIONAL STATUS")
	fmt.Fprintf(output, "\nStatus: %s\n", document.Status)
	fmt.Fprintf(output, "Observed: %s\n", formatInstant(document.GeneratedAt))
	if document.Offline {
		fmt.Fprintln(output, "Source contact: none (status reports recorded observations only)")
	} else {
		fmt.Fprintln(output, "Source contact: one authentication request was made for --refresh")
	}
	fmt.Fprintf(output, "State directory: %s\n", document.StateDirectory)

	if len(document.Scopes) == 0 {
		fmt.Fprintln(output, "\nNo operation has been recorded yet.")
		fmt.Fprintln(output, "Run \"alih backup\" to create a verified backup, or \"alih --help\" to see the workflow.")
	}
	for _, scope := range document.Scopes {
		fmt.Fprintf(output, "\n%s\n", strings.Repeat("-", 60))
		fmt.Fprintf(output, "Workspace: %s (ID: %s)\n", displayValue(scope.WorkspaceName), displayValue(scope.WorkspaceID))
		fmt.Fprintf(output, "Connector: %s\n", displayValue(scope.Connector))
		fmt.Fprintf(output, "Destination: %s\n", scope.Destination)
		fmt.Fprintf(output, "Scope status: %s\n", scope.Status)
		if scope.Account != nil {
			fmt.Fprintf(output, "Acting as: %s (ID: %s)\n", displayValue(scope.Account.Name), displayValue(scope.Account.ID))
		}
		writeStatusObservation(output, "Connector health", scope.Health)
		writeStatusObservation(output, "Authentication", scope.Authentication)
		writeStatusCapabilities(output, scope)
		writeStatusNotifications(output, scope.Notifications)
		writeStatusAttempt(output, "Last attempt", scope.LastAttempt)
		switch {
		case scope.LastSuccess == nil:
			fmt.Fprintln(output, "Last successful backup: none recorded")
		case scope.LastAttempt != nil && scope.LastAttempt.OperationID == scope.LastSuccess.OperationID:
			// The same run; repeating every line of it would only pad the report.
			fmt.Fprintln(output, "Last successful backup: the attempt above")
		default:
			writeStatusAttempt(output, "Last successful backup", scope.LastSuccess)
		}
		if verification := scope.Verification; verification != nil {
			fmt.Fprintf(output, "Last verification: %s at %s (%s ago)\n",
				verification.Result, formatInstant(verification.VerifiedAt), formatAge(verification.AgeSeconds))
			fmt.Fprintf(output, "Verified archive: %s [%s]\n", verification.Path, verification.Condition)
			for _, limitation := range verification.Limitations {
				fmt.Fprintf(output, "  Limitation: %s\n", displayValue(limitation))
			}
		} else {
			fmt.Fprintln(output, "Last verification: none recorded")
		}
		writeStatusActivity(output, scope.RecentActivity)
		for _, reason := range scope.Attention {
			fmt.Fprintf(output, "Attention: %s\n", displayValue(reason))
		}
	}

	if document.Reconciliation != nil {
		writeReconcileText(output, *document.Reconciliation)
	}
	if document.ScheduleProblem != "" {
		fmt.Fprintf(output, "\nSchedule attention: %s\n", displayValue(document.ScheduleProblem))
	}
	for _, scheduled := range document.Schedules {
		artifact := "not inspected"
		if scheduled.ArtifactsMatch != nil {
			artifact = fmt.Sprintf("artifacts match: %t", *scheduled.ArtifactsMatch)
		}
		fmt.Fprintf(output, "\nSchedule %s: enabled=%t, %s at %s %s, workspace %s, %s\n",
			scheduled.ID, scheduled.Enabled, scheduled.Cadence.Frequency, scheduled.Cadence.At,
			scheduled.Cadence.Timezone, scheduled.WorkspaceID, artifact)
	}

	for _, path := range document.UnreadableState {
		fmt.Fprintf(output, "\nUnreadable state file: %s\n", path)
		fmt.Fprintln(output, "ALIH did not modify or replace it; local status is incomplete until it is resolved.")
	}
	if document.UnreadableEventLines > 0 {
		fmt.Fprintf(output, "\nDamaged event history: %d line(s) could not be read.\n", document.UnreadableEventLines)
		fmt.Fprintln(output, "The status above does not depend on that history and is unaffected.")
	}
}

func writeStatusNotifications(output io.Writer, notifications *statusNotifications) {
	if notifications == nil {
		fmt.Fprintln(output, "Notifications: not configured or not yet observed")
		return
	}
	if notifications.Problem != nil {
		fmt.Fprintf(output, "Notifications: configuration problem (%s)\n", notifications.Problem.Reason)
		fmt.Fprintf(output, "  %s\n", displayValue(notifications.Problem.Message))
		return
	}
	fmt.Fprintf(output, "Notifications: %d enabled destination(s), checked %s ago\n",
		len(notifications.DestinationIDs), formatAge(notifications.AgeSeconds))
	for _, delivery := range notifications.LastDeliveries {
		status := "not delivered"
		if delivery.Delivered {
			status = "delivered"
		}
		fmt.Fprintf(output, "  %s: %s %s (%s, %d attempt(s))\n", delivery.DestinationID,
			delivery.EventType, status, delivery.Reason, delivery.Attempts)
	}
}

func writeStatusActivity(output io.Writer, activity *statusActivity) {
	if activity == nil || activity.RecordedEvents == 0 {
		fmt.Fprintln(output, "Recent activity: no events recorded")
		return
	}
	fmt.Fprintf(output, "Recent activity: %d events recorded, %d failed\n",
		activity.RecordedEvents, activity.FailedEvents)
	if last := activity.Last; last != nil {
		outcome := last.Outcome
		if outcome == "" {
			outcome = "OBSERVED"
		}
		fmt.Fprintf(output, "Last event: %s (%s) %s ago — %s\n",
			last.Type, outcome, formatAge(last.AgeSeconds), displayValue(last.Message))
	}
}

// writeStatusCapabilities reports the recorded capability scope. A capability
// that was not obtained is named individually, because that is exactly the
// detail a complete-looking backup would otherwise hide.
func writeStatusCapabilities(output io.Writer, scope statusScope) {
	if len(scope.Capabilities) == 0 {
		fmt.Fprintln(output, "Capabilities: not recorded")
		return
	}
	var limited []connector.Capability
	for _, capability := range scope.Capabilities {
		if capability.Availability != connector.CapabilityAvailabilityAvailable {
			limited = append(limited, capability)
		}
	}
	fmt.Fprintf(output, "Capabilities: %d recorded, %d not fully available\n", len(scope.Capabilities), len(limited))
	for _, capability := range limited {
		identity := string(capability.ID)
		if identity == "" {
			identity = displayValue(capability.Name)
		}
		availability := string(capability.Availability)
		if availability == "" {
			// Pre-contract evidence records support but no observation.
			availability = "UNRECORDED"
		}
		fmt.Fprintf(output, "  %s: %s (%s)\n", identity, availability, capability.Requirement)
	}
}

func writeStatusObservation(output io.Writer, label string, observation *statusObservation) {
	if observation == nil {
		fmt.Fprintf(output, "%s: not recorded\n", label)
		return
	}
	staleness := ""
	if observation.Stale {
		staleness = ", stale"
	}
	fmt.Fprintf(output, "%s: %s (observed %s ago%s)\n", label, observation.State,
		formatAge(observation.AgeSeconds), staleness)
	if observation.Reason != "" && observation.Reason != string(connector.HealthReasonNone) {
		fmt.Fprintf(output, "  Reason: %s — %s\n", observation.Reason, displayValue(observation.Message))
	}
}

func writeStatusAttempt(output io.Writer, label string, attempt *state.Attempt) {
	if attempt == nil {
		return
	}
	when := formatInstant(attempt.StartedAt)
	if attempt.EndedAt != nil {
		when = formatInstant(*attempt.EndedAt)
	}
	fmt.Fprintf(output, "%s: %s %s at the %s stage (%s)\n", label, attempt.Operation, attempt.Outcome, attempt.Stage, when)
	if attempt.ArchivePath != "" {
		fmt.Fprintf(output, "  Archive: %s\n", attempt.ArchivePath)
	}
	if attempt.ReportPath != "" {
		fmt.Fprintf(output, "  Recovery report: %s\n", attempt.ReportPath)
	}
	if attempt.FailedPath != "" {
		fmt.Fprintf(output, "  Failed working state: %s\n", attempt.FailedPath)
	}
	if attempt.Error != nil {
		fmt.Fprintf(output, "  Reason: %s — %s\n", attempt.Error.Reason, displayValue(attempt.Error.Message))
	}
	if attempt.ScheduleID != "" {
		fmt.Fprintf(output, "  Schedule: %s\n", attempt.ScheduleID)
	}
	if attempt.SkipReason != "" {
		fmt.Fprintf(output, "  Skip reason: %s\n", displayValue(attempt.SkipReason))
	}
}

func formatInstant(instant time.Time) string {
	if instant.IsZero() {
		return "unknown"
	}
	return instant.UTC().Format("2006-01-02T15:04:05Z07:00")
}

func formatAge(seconds int64) string {
	age := time.Duration(seconds) * time.Second
	switch {
	case age < time.Minute:
		return fmt.Sprintf("%ds", seconds)
	case age < time.Hour:
		return fmt.Sprintf("%dm", seconds/60)
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", seconds/3600)
	default:
		return fmt.Sprintf("%dd", seconds/86400)
	}
}
