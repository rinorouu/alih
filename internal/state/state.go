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

// Package state records what Alih locally knows about its own operations.
//
// A record is a projection of work that already happened; it is never evidence
// on its own. Nothing here contacts a source, and a credential is never stored,
// derived, or accepted. Missing, ambiguous, or unreadable state stays exactly
// that: it is never repaired into a claim Alih cannot support.
package state

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"alih/internal/connector"
)

// SchemaVersion identifies the local operational state contract. Version 2
// added notifications; version 3 adds schedule correlation and explicit
// overlap skips. Earlier versions remain readable and are upgraded in memory;
// they are rewritten only when a real state transition is saved.
const SchemaVersion = 3

// scopeKeyVersion is deliberately independent of the document schema. Scope
// file names shipped with state v1 and must not move merely because an additive
// field was introduced.
const scopeKeyVersion = 1

// Operations name the user-invoked work that can update state.
const (
	OperationBackup  = "backup"
	OperationScan    = "scan"
	OperationExtract = "extract"
	OperationExport  = "export"
	OperationVerify  = "verify"
	OperationReport  = "report"
	OperationAuth    = "auth"
	OperationNotify  = "notify"
)

// Stages name the pipeline steps an operation moves through. They are stable
// machine identities and deliberately independent of human CLI wording.
const (
	// StagePrepare covers the local work a run does before it reads the source:
	// resolving its destination and creating its private working directory. It
	// is a stage of its own because a failure there proves nothing about the
	// source or the credential.
	StagePrepare      = "prepare"
	StageAuthenticate = "authenticate"
	StageScan         = "scan"
	StageExtract      = "extract"
	StageExport       = "export"
	StageVerify       = "verify"
	StageReport       = "report"
	StageFinalize     = "finalize"
	StageNotify       = "notify"
)

// Operations returns every operation name in canonical order. It exists so
// that packages which must accept the same vocabulary — the event contract in
// particular — read it from here instead of keeping a second copy that can
// silently fall behind.
func Operations() []string {
	return []string{
		OperationBackup, OperationScan, OperationExtract, OperationExport,
		OperationVerify, OperationReport, OperationAuth, OperationNotify,
	}
}

// Stages returns every stage name in the order a run moves through them.
func Stages() []string {
	return []string{
		StagePrepare, StageAuthenticate, StageScan, StageExtract,
		StageExport, StageVerify, StageReport, StageFinalize, StageNotify,
	}
}

// ValidOperation reports whether name is a recorded operation.
func ValidOperation(name string) bool { return contains(Operations(), name) }

// ValidStage reports whether name is a recorded stage.
func ValidStage(name string) bool { return contains(Stages(), name) }

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// Outcomes describe how far an attempt got. STARTED that is never updated means
// the process ended without recording an end; status must read that as an
// interrupted attempt, never as a success.
const (
	OutcomeStarted   = "STARTED"
	OutcomeSucceeded = "SUCCEEDED"
	OutcomeFailed    = "FAILED"
	OutcomeSkipped   = "SKIPPED"
)

// Verification results mirror the independent verifier's vocabulary. They are
// duplicated as validated strings so that this package depends only on the
// connector contract and never on the verification implementation.
const (
	ResultVerified                = "VERIFIED"
	ResultVerifiedWithLimitations = "VERIFIED_WITH_LIMITATIONS"
	ResultIncomplete              = "INCOMPLETE"
	ResultFailed                  = "FAILED"
)

const (
	maxTextBytes       = 512
	maxPathBytes       = 4096
	maxLimitations     = 32
	maxCapabilities    = 64
	maxOperationIDText = 64
)

// Scope is the identity of one operational state record: which connector, which
// source workspace, and which local destination the work was written to. The
// workspace display name is deliberately not part of the identity, because a
// source may rename it at any time without becoming a different workspace.
type Scope struct {
	Connector   string `json:"connector"`
	WorkspaceID string `json:"workspace_id"`
	Destination string `json:"destination"`
}

// Key is the stable, filesystem-safe identity of a scope. It is a digest rather
// than a readable name so that provider-controlled text never decides a path.
func (s Scope) Key() string {
	normalized := s.Normalize()
	digest := sha256.Sum256([]byte(strings.Join([]string{
		fmt.Sprintf("v%d", scopeKeyVersion), normalized.Connector, normalized.WorkspaceID, normalized.Destination,
	}, "\x00")))
	return hex.EncodeToString(digest[:16])
}

// Normalize returns the scope in the form used for identity and storage.
func (s Scope) Normalize() Scope {
	s.Connector = strings.TrimSpace(s.Connector)
	s.WorkspaceID = strings.TrimSpace(s.WorkspaceID)
	if destination := strings.TrimSpace(s.Destination); destination != "" {
		s.Destination = filepath.Clean(destination)
	}
	return s
}

// SafeError is Alih's own bounded description of a failure. It never carries a
// provider response body, a credential, or uncontrolled provider text.
type SafeError struct {
	Stage   string                 `json:"stage"`
	Reason  connector.HealthReason `json:"reason"`
	Message string                 `json:"message"`
}

// Attempt records one run of one operation. StartedAt and EndedAt come from an
// injected clock so that ordering is a recorded fact rather than a guess.
type Attempt struct {
	OperationID string     `json:"operation_id"`
	Operation   string     `json:"operation"`
	Stage       string     `json:"stage"`
	Outcome     string     `json:"outcome"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	ArchivePath string     `json:"archive_path,omitempty"`
	ReportPath  string     `json:"report_path,omitempty"`
	FailedPath  string     `json:"failed_path,omitempty"`
	AlihVersion string     `json:"alih_version,omitempty"`
	Error       *SafeError `json:"error,omitempty"`
	ScheduleID  string     `json:"schedule_id,omitempty"`
	SkipReason  string     `json:"skip_reason,omitempty"`
}

// ArchiveIdentity is enough to detect that the archive a claim refers to is
// gone or is no longer the archive that was verified. The manifest checksum is
// used because a sealed manifest cannot contain its own checksum.
type ArchiveIdentity struct {
	Path             string     `json:"path"`
	ManifestChecksum string     `json:"manifest_checksum"`
	LogicalDigest    string     `json:"logical_inventory_digest"`
	CapabilityDigest string     `json:"capability_digest,omitempty"`
	ArchiveStatus    string     `json:"archive_status"`
	CompletedAt      *time.Time `json:"archive_completed_at,omitempty"`
}

// Verification is the post-seal verification result that cannot live inside the
// immutable archive, whose own manifest correctly stays NOT_RUN forever.
type Verification struct {
	Result      string          `json:"result"`
	VerifiedAt  time.Time       `json:"verified_at"`
	Archive     ArchiveIdentity `json:"archive"`
	Limitations []string        `json:"limitations,omitempty"`
	AlihVersion string          `json:"alih_version,omitempty"`
}

// Notification configuration and delivery states are deliberately small,
// provider-neutral operational facts. They contain destination identifiers and
// Alih-owned outcome text, never a URL, header, credential, or response body.
const (
	NotificationReasonConfigurationInvalid = "CONFIGURATION_INVALID"
	NotificationReasonDelivered            = "DELIVERED"
	NotificationReasonRejected             = "REJECTED"
	NotificationReasonRateLimited          = "RATE_LIMITED"
	NotificationReasonDestinationDown      = "DESTINATION_UNAVAILABLE"
	NotificationReasonNetworkFailure       = "NETWORK_FAILURE"
	NotificationReasonTimeout              = "TIMEOUT"
	NotificationReasonRedirectRefused      = "REDIRECT_REFUSED"
	NotificationReasonInvalidResponse      = "INVALID_RESPONSE"
	NotificationReasonSecretMissing        = "SECRET_MISSING"
	NotificationReasonPayload              = "PAYLOAD_NOT_DELIVERABLE"
	NotificationReasonCancelled            = "CANCELLED"
)

// NotificationProblem records why configured delivery could not even be
// attempted. Message is bounded Alih-owned text rather than the underlying
// configuration error, which could contain a sensitive local path.
type NotificationProblem struct {
	Reason     string    `json:"reason"`
	Message    string    `json:"message"`
	ObservedAt time.Time `json:"observed_at"`
}

// NotificationDelivery is the latest result for one configured destination.
// The idempotency key identifies the event but cannot reveal its payload.
type NotificationDelivery struct {
	DestinationID  string    `json:"destination_id"`
	EventType      string    `json:"event_type"`
	IdempotencyKey string    `json:"idempotency_key"`
	Delivered      bool      `json:"delivered"`
	Attempts       int       `json:"attempts"`
	Reason         string    `json:"reason"`
	Retryable      bool      `json:"retryable"`
	Message        string    `json:"message"`
	ObservedAt     time.Time `json:"observed_at"`
}

// NotificationState is the notification projection for one operational scope.
// Destinations are IDs only. A missing field means no notification
// configuration was observed for the run, which is the normal default.
type NotificationState struct {
	CheckedAt      time.Time              `json:"checked_at"`
	DestinationIDs []string               `json:"destination_ids,omitempty"`
	Problem        *NotificationProblem   `json:"problem,omitempty"`
	LastDeliveries []NotificationDelivery `json:"last_deliveries,omitempty"`
}

// Record is the complete local operational state for one scope. Every field is
// either a persisted fact or absent; absence is never rendered as success.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	Revision      uint64 `json:"revision"`
	Scope         Scope  `json:"scope"`

	WorkspaceName string              `json:"workspace_name,omitempty"`
	Account       *connector.Identity `json:"account,omitempty"`
	AlihVersion   string              `json:"alih_version,omitempty"`
	UpdatedAt     time.Time           `json:"updated_at"`

	Assessment              *connector.OperationalAssessment `json:"operational_assessment,omitempty"`
	CapabilitySchemaVersion int                              `json:"capability_schema_version,omitempty"`
	Capabilities            []connector.Capability           `json:"capabilities,omitempty"`

	LastAttempt      *Attempt           `json:"last_attempt,omitempty"`
	LastSuccess      *Attempt           `json:"last_success,omitempty"`
	LastVerification *Verification      `json:"last_verification,omitempty"`
	Notifications    *NotificationState `json:"notifications,omitempty"`
}

// NewOperationID returns a per-run identity. The clock and randomness are
// injected so that an operation ID is reproducible under test and never
// silently depends on process-global state.
func NewOperationID(now time.Time, entropy io.Reader) (string, error) {
	if entropy == nil {
		entropy = rand.Reader
	}
	suffix := make([]byte, 4)
	if _, err := io.ReadFull(entropy, suffix); err != nil {
		return "", fmt.Errorf("operation id entropy: %w", err)
	}
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix), nil
}

// Canonicalize puts a record into its stable stored form: UTC instants, ordered
// capabilities, ordered limitations, and a normalized scope. It does not decide
// whether the record is valid.
func Canonicalize(record *Record) {
	if record == nil {
		return
	}
	record.Scope = record.Scope.Normalize()
	record.UpdatedAt = record.UpdatedAt.UTC()
	canonicalizeAttempt(record.LastAttempt)
	canonicalizeAttempt(record.LastSuccess)
	if record.Assessment != nil {
		connector.CanonicalizeOperationalAssessment(record.Assessment)
		record.Assessment.Health.ObservedAt = record.Assessment.Health.ObservedAt.UTC()
		record.Assessment.Authentication.ObservedAt = record.Assessment.Authentication.ObservedAt.UTC()
	}
	if len(record.Capabilities) > 0 {
		record.Capabilities = connector.CanonicalCapabilities(record.CapabilitySchemaVersion, record.Capabilities)
	}
	if record.LastVerification != nil {
		record.LastVerification.VerifiedAt = record.LastVerification.VerifiedAt.UTC()
		if completed := record.LastVerification.Archive.CompletedAt; completed != nil {
			utc := completed.UTC()
			record.LastVerification.Archive.CompletedAt = &utc
		}
		limitations := append([]string(nil), record.LastVerification.Limitations...)
		sort.Strings(limitations)
		record.LastVerification.Limitations = limitations
	}
	if record.Notifications != nil {
		record.Notifications.CheckedAt = record.Notifications.CheckedAt.UTC()
		sort.Strings(record.Notifications.DestinationIDs)
		if record.Notifications.Problem != nil {
			record.Notifications.Problem.ObservedAt = record.Notifications.Problem.ObservedAt.UTC()
		}
		for index := range record.Notifications.LastDeliveries {
			record.Notifications.LastDeliveries[index].ObservedAt = record.Notifications.LastDeliveries[index].ObservedAt.UTC()
		}
		sort.Slice(record.Notifications.LastDeliveries, func(i, j int) bool {
			return record.Notifications.LastDeliveries[i].DestinationID < record.Notifications.LastDeliveries[j].DestinationID
		})
	}
}

func canonicalizeAttempt(attempt *Attempt) {
	if attempt == nil {
		return
	}
	attempt.StartedAt = attempt.StartedAt.UTC()
	if attempt.EndedAt != nil {
		ended := attempt.EndedAt.UTC()
		attempt.EndedAt = &ended
	}
}

// Validate rejects a record that is ambiguous, unsafe, internally inconsistent,
// or that claims more than its own fields prove.
func Validate(record Record) error {
	if record.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported state schema version %d", record.SchemaVersion)
	}
	scope := record.Scope.Normalize()
	if err := validateText("connector", scope.Connector, maxTextBytes, false); err != nil {
		return err
	}
	if err := validateText("workspace id", scope.WorkspaceID, maxTextBytes, false); err != nil {
		return err
	}
	if err := validateText("destination", scope.Destination, maxPathBytes, false); err != nil {
		return err
	}
	if !filepath.IsAbs(scope.Destination) {
		return errors.New("destination must be an absolute path")
	}
	if err := validateText("workspace name", record.WorkspaceName, maxTextBytes, true); err != nil {
		return err
	}
	if err := validateText("alih version", record.AlihVersion, maxTextBytes, true); err != nil {
		return err
	}
	if record.Account != nil {
		if err := validateText("account id", record.Account.ID, maxTextBytes, true); err != nil {
			return err
		}
		if err := validateText("account name", record.Account.Name, maxTextBytes, true); err != nil {
			return err
		}
	}
	if record.UpdatedAt.IsZero() {
		return errors.New("state records no update time")
	}
	if record.Assessment != nil {
		if err := connector.ValidateOperationalAssessment(*record.Assessment); err != nil {
			return fmt.Errorf("recorded operational assessment: %w", err)
		}
	}
	if len(record.Capabilities) > maxCapabilities {
		return fmt.Errorf("state records more than %d capabilities", maxCapabilities)
	}
	if len(record.Capabilities) > 0 {
		if err := connector.ValidateCapabilities(record.CapabilitySchemaVersion, record.Capabilities); err != nil {
			return fmt.Errorf("recorded capability contract: %w", err)
		}
	}
	if err := validateAttempt("last attempt", record.LastAttempt); err != nil {
		return err
	}
	if err := validateAttempt("last success", record.LastSuccess); err != nil {
		return err
	}
	if record.LastSuccess != nil {
		if record.LastSuccess.Outcome != OutcomeSucceeded {
			return errors.New("last success is not a succeeded attempt")
		}
		if record.LastSuccess.EndedAt == nil {
			return errors.New("last success records no end time")
		}
		if strings.TrimSpace(record.LastSuccess.ArchivePath) == "" {
			return errors.New("last success records no archive path")
		}
		if record.LastAttempt != nil && record.LastAttempt.StartedAt.Before(record.LastSuccess.StartedAt) {
			return errors.New("last attempt started before the last success it supersedes")
		}
	}
	if err := validateVerification(record.LastVerification); err != nil {
		return err
	}
	return validateNotifications(record.Notifications)
}

func validateNotifications(notifications *NotificationState) error {
	if notifications == nil {
		return nil
	}
	if notifications.CheckedAt.IsZero() {
		return errors.New("notifications record no configuration check time")
	}
	if len(notifications.DestinationIDs) > 8 {
		return errors.New("notifications record more than 8 destinations")
	}
	seenIDs := make(map[string]struct{}, len(notifications.DestinationIDs))
	for _, id := range notifications.DestinationIDs {
		if err := validateNotificationID("notification destination id", id); err != nil {
			return err
		}
		if _, duplicate := seenIDs[id]; duplicate {
			return fmt.Errorf("duplicate notification destination id %q", id)
		}
		seenIDs[id] = struct{}{}
	}
	if notifications.Problem != nil {
		if notifications.Problem.Reason != NotificationReasonConfigurationInvalid {
			return fmt.Errorf("unknown notification configuration reason %q", notifications.Problem.Reason)
		}
		if notifications.Problem.ObservedAt.IsZero() {
			return errors.New("notification configuration problem records no time")
		}
		if err := validateText("notification configuration message", notifications.Problem.Message, maxTextBytes, false); err != nil {
			return err
		}
	}
	if len(notifications.LastDeliveries) > 8 {
		return errors.New("notifications record more than 8 delivery results")
	}
	seenDeliveries := make(map[string]struct{}, len(notifications.LastDeliveries))
	for index, delivery := range notifications.LastDeliveries {
		if err := validateNotificationDelivery(index, delivery); err != nil {
			return err
		}
		if _, configured := seenIDs[delivery.DestinationID]; !configured {
			return fmt.Errorf("notification delivery destination %q is not configured", delivery.DestinationID)
		}
		if _, duplicate := seenDeliveries[delivery.DestinationID]; duplicate {
			return fmt.Errorf("duplicate notification delivery for %q", delivery.DestinationID)
		}
		seenDeliveries[delivery.DestinationID] = struct{}{}
	}
	return nil
}

func validateNotificationDelivery(index int, delivery NotificationDelivery) error {
	prefix := fmt.Sprintf("notification delivery %d", index)
	if err := validateNotificationID(prefix+" destination id", delivery.DestinationID); err != nil {
		return err
	}
	if err := validateText(prefix+" event type", delivery.EventType, maxTextBytes, false); err != nil {
		return err
	}
	if len(delivery.IdempotencyKey) != 32 {
		return fmt.Errorf("%s idempotency key is not a 16-byte hex digest", prefix)
	}
	if _, err := hex.DecodeString(delivery.IdempotencyKey); err != nil {
		return fmt.Errorf("%s idempotency key is not hexadecimal", prefix)
	}
	if delivery.Attempts < 0 || delivery.Attempts > 5 {
		return fmt.Errorf("%s attempts is outside the supported bound", prefix)
	}
	if !validNotificationReason(delivery.Reason) {
		return fmt.Errorf("%s has unknown reason %q", prefix, delivery.Reason)
	}
	if delivery.Delivered != (delivery.Reason == NotificationReasonDelivered) {
		return fmt.Errorf("%s delivered flag conflicts with reason %q", prefix, delivery.Reason)
	}
	if delivery.Delivered && delivery.Retryable {
		return fmt.Errorf("%s delivered result cannot be retryable", prefix)
	}
	if err := validateText(prefix+" message", delivery.Message, maxTextBytes, false); err != nil {
		return err
	}
	if delivery.ObservedAt.IsZero() {
		return fmt.Errorf("%s records no time", prefix)
	}
	return nil
}

func validateNotificationID(label, value string) error {
	if err := validateText(label, value, 64, false); err != nil {
		return err
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9', character == '-', character == '_':
		default:
			return fmt.Errorf("%s may only contain letters, digits, hyphen, and underscore", label)
		}
	}
	return nil
}

func validNotificationReason(reason string) bool {
	switch reason {
	case NotificationReasonDelivered, NotificationReasonRejected, NotificationReasonRateLimited,
		NotificationReasonDestinationDown, NotificationReasonNetworkFailure, NotificationReasonTimeout,
		NotificationReasonRedirectRefused, NotificationReasonInvalidResponse, NotificationReasonSecretMissing,
		NotificationReasonPayload, NotificationReasonCancelled:
		return true
	default:
		return false
	}
}

func validateAttempt(label string, attempt *Attempt) error {
	if attempt == nil {
		return nil
	}
	if err := validateText(label+" operation id", attempt.OperationID, maxOperationIDText, false); err != nil {
		return err
	}
	if !validOperation(attempt.Operation) {
		return fmt.Errorf("%s has unknown operation %q", label, attempt.Operation)
	}
	if !validStage(attempt.Stage) {
		return fmt.Errorf("%s has unknown stage %q", label, attempt.Stage)
	}
	if !validOutcome(attempt.Outcome) {
		return fmt.Errorf("%s has unknown outcome %q", label, attempt.Outcome)
	}
	if attempt.StartedAt.IsZero() {
		return fmt.Errorf("%s records no start time", label)
	}
	if attempt.EndedAt != nil && attempt.EndedAt.Before(attempt.StartedAt) {
		return fmt.Errorf("%s ended before it started", label)
	}
	if attempt.Outcome == OutcomeStarted && attempt.EndedAt != nil {
		return fmt.Errorf("%s is still started but records an end time", label)
	}
	if attempt.Outcome == OutcomeFailed && attempt.Error == nil {
		return fmt.Errorf("%s failed without a recorded reason", label)
	}
	if attempt.Outcome == OutcomeSucceeded && attempt.Error != nil {
		return fmt.Errorf("%s succeeded but records an error", label)
	}
	if attempt.Outcome == OutcomeSkipped {
		if attempt.EndedAt == nil {
			return fmt.Errorf("%s skipped without an end time", label)
		}
		if attempt.Error != nil {
			return fmt.Errorf("%s skipped but records an operation error", label)
		}
		if err := validateText(label+" skip reason", attempt.SkipReason, maxTextBytes, false); err != nil {
			return err
		}
	} else if attempt.SkipReason != "" {
		return fmt.Errorf("%s records a skip reason without being skipped", label)
	}
	if attempt.ScheduleID != "" {
		if err := validateLocalID(label+" schedule id", attempt.ScheduleID); err != nil {
			return err
		}
	}
	for field, value := range map[string]string{
		"archive path": attempt.ArchivePath, "report path": attempt.ReportPath, "failed path": attempt.FailedPath,
	} {
		if err := validateText(label+" "+field, value, maxPathBytes, true); err != nil {
			return err
		}
	}
	if err := validateText(label+" alih version", attempt.AlihVersion, maxTextBytes, true); err != nil {
		return err
	}
	if attempt.Error == nil {
		return nil
	}
	if attempt.Error.Stage != "" && !validStage(attempt.Error.Stage) {
		return fmt.Errorf("%s error has unknown stage %q", label, attempt.Error.Stage)
	}
	if !validReason(attempt.Error.Reason) {
		return fmt.Errorf("%s error has unknown reason %q", label, attempt.Error.Reason)
	}
	return validateText(label+" error message", attempt.Error.Message, maxTextBytes, false)
}

func validateVerification(verification *Verification) error {
	if verification == nil {
		return nil
	}
	if !validResult(verification.Result) {
		return fmt.Errorf("last verification has unknown result %q", verification.Result)
	}
	if verification.VerifiedAt.IsZero() {
		return errors.New("last verification records no time")
	}
	if err := validateText("verified archive path", verification.Archive.Path, maxPathBytes, false); err != nil {
		return err
	}
	if !filepath.IsAbs(verification.Archive.Path) {
		return errors.New("verified archive path must be absolute")
	}
	if err := validateDigest("verified manifest checksum", verification.Archive.ManifestChecksum); err != nil {
		return err
	}
	if err := validateDigest("verified logical inventory digest", verification.Archive.LogicalDigest); err != nil {
		return err
	}
	if verification.Archive.CapabilityDigest != "" {
		if err := validateDigest("verified capability digest", verification.Archive.CapabilityDigest); err != nil {
			return err
		}
	}
	if err := validateText("verified archive status", verification.Archive.ArchiveStatus, maxTextBytes, false); err != nil {
		return err
	}
	if len(verification.Limitations) > maxLimitations {
		return fmt.Errorf("last verification records more than %d limitations", maxLimitations)
	}
	for index, limitation := range verification.Limitations {
		if err := validateText(fmt.Sprintf("verification limitation %d", index), limitation, maxTextBytes, false); err != nil {
			return err
		}
	}
	return validateText("verification alih version", verification.AlihVersion, maxTextBytes, true)
}

func validateDigest(label, value string) error {
	if err := validateText(label, value, maxTextBytes, false); err != nil {
		return err
	}
	encoded, found := strings.CutPrefix(value, "sha256:")
	if !found {
		return fmt.Errorf("%s is not a sha256 digest", label)
	}
	if len(encoded) != 64 {
		return fmt.Errorf("%s is not a 32-byte digest", label)
	}
	if _, err := hex.DecodeString(encoded); err != nil {
		return fmt.Errorf("%s is not hexadecimal", label)
	}
	return nil
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

func validOperation(operation string) bool {
	switch operation {
	case OperationBackup, OperationScan, OperationExtract, OperationExport,
		OperationVerify, OperationReport, OperationAuth, OperationNotify:
		return true
	default:
		return false
	}
}

func validStage(stage string) bool {
	switch stage {
	case StagePrepare, StageAuthenticate, StageScan, StageExtract, StageExport, StageVerify, StageReport, StageFinalize, StageNotify:
		return true
	default:
		return false
	}
}

func validOutcome(outcome string) bool {
	return outcome == OutcomeStarted || outcome == OutcomeSucceeded || outcome == OutcomeFailed || outcome == OutcomeSkipped
}

func validateLocalID(label, value string) error {
	if err := validateText(label, value, 64, false); err != nil {
		return err
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9', character == '-', character == '_':
		default:
			return fmt.Errorf("%s contains an unsafe character", label)
		}
	}
	return nil
}

// ValidResult reports whether a verifier result can be recorded. A caller that
// produced something else has nothing meaningful to store.
func ValidResult(result string) bool { return validResult(result) }

func validResult(result string) bool {
	switch result {
	case ResultVerified, ResultVerifiedWithLimitations, ResultIncomplete, ResultFailed:
		return true
	default:
		return false
	}
}

func validReason(reason connector.HealthReason) bool {
	assessment := connector.OperationalAssessment{
		SchemaVersion: connector.HealthSchemaVersion,
		Health: connector.Health{
			SchemaVersion: connector.HealthSchemaVersion, Connector: "probe",
			State: connector.HealthUnknown, Basis: connector.HealthBasisScan,
			ObservedAt: time.Unix(0, 0).UTC(), Reason: reason, Message: "probe",
		},
		Authentication: connector.AuthenticationObservation{
			State: connector.AuthenticationUnknown, ObservedAt: time.Unix(0, 0).UTC(),
			Reason: connector.HealthReasonNone, Message: "probe",
		},
	}
	return connector.ValidateOperationalAssessment(assessment) == nil
}

// Marshal renders a record as canonical, stable JSON. Two equal records always
// produce byte-identical output.
func Marshal(record Record) ([]byte, error) {
	Canonicalize(&record)
	if err := Validate(record); err != nil {
		return nil, err
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

// Unmarshal parses stored state strictly. An unknown field, trailing content,
// or an unsupported version is an error, never a silently ignored difference.
func Unmarshal(content []byte) (Record, error) {
	var probe struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(content, &probe); err != nil {
		return Record{}, fmt.Errorf("state is not valid JSON: %w", err)
	}
	if probe.SchemaVersion > SchemaVersion {
		return Record{}, fmt.Errorf("%w: state schema version %d", ErrFutureSchema, probe.SchemaVersion)
	}
	if probe.SchemaVersion < 1 {
		return Record{}, fmt.Errorf("unsupported state schema version %d", probe.SchemaVersion)
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("state could not be decoded: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Record{}, err
	}
	// Versions 1 and 2 had no schedule fields. Their other fields are additive,
	// so migration is an explicit version lift with absence preserved.
	if record.SchemaVersion < SchemaVersion {
		record.SchemaVersion = SchemaVersion
	}
	Canonicalize(&record)
	if err := Validate(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("state contains trailing content")
	}
	return nil
}
