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

package connector

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// HealthSchemaVersion identifies the provider-neutral operational assessment.
const HealthSchemaVersion = 1

// HealthState describes whether the assessed scope can operate. UNKNOWN is an
// explicit lack of evidence and must never be interpreted as healthy.
type HealthState string

const (
	HealthHealthy     HealthState = "HEALTHY"
	HealthDegraded    HealthState = "DEGRADED"
	HealthUnavailable HealthState = "UNAVAILABLE"
	HealthUnknown     HealthState = "UNKNOWN"
)

// AuthenticationState is deliberately independent of provider health.
type AuthenticationState string

const (
	AuthenticationUnknown       AuthenticationState = "UNKNOWN"
	AuthenticationAuthenticated AuthenticationState = "AUTHENTICATED"
	AuthenticationRequired      AuthenticationState = "REQUIRED"
	AuthenticationRejected      AuthenticationState = "REJECTED"
)

// HealthBasis identifies the real operation that produced an observation.
type HealthBasis string

const (
	HealthBasisAuthentication HealthBasis = "AUTHENTICATION"
	HealthBasisScan           HealthBasis = "SCAN"
	HealthBasisExtraction     HealthBasis = "EXTRACTION"
	HealthBasisBackup         HealthBasis = "BACKUP"
)

// HealthReason is stable machine-readable cause identity. Message is only a
// safe operator explanation and must never be used for control flow.
type HealthReason string

const (
	HealthReasonNone                   HealthReason = "NONE"
	HealthReasonAuthenticationRequired HealthReason = "AUTHENTICATION_REQUIRED"
	HealthReasonAuthenticationRejected HealthReason = "AUTHENTICATION_REJECTED"
	HealthReasonRateLimited            HealthReason = "RATE_LIMITED"
	HealthReasonUpstreamUnavailable    HealthReason = "UPSTREAM_UNAVAILABLE"
	HealthReasonNetworkFailure         HealthReason = "NETWORK_FAILURE"
	HealthReasonUnsupportedResponse    HealthReason = "UNSUPPORTED_RESPONSE"
	HealthReasonCapabilityRemoved      HealthReason = "CAPABILITY_REMOVED"
	HealthReasonAPIBehaviorChanged     HealthReason = "API_BEHAVIOR_CHANGED"
	HealthReasonOperationCancelled     HealthReason = "OPERATION_CANCELLED"
	HealthReasonCapabilityFailed       HealthReason = "CAPABILITY_FAILED"
	HealthReasonUnknownFailure         HealthReason = "UNKNOWN_FAILURE"
)

// CapabilityHealth scopes a health observation to one declared capability.
type CapabilityHealth struct {
	ID          CapabilityID          `json:"id"`
	Requirement CapabilityRequirement `json:"requirement"`
	State       HealthState           `json:"state"`
	Reason      HealthReason          `json:"reason"`
	Retryable   bool                  `json:"retryable"`
	Message     string                `json:"message"`
}

// Health is a point-in-time observation, not a durable availability promise.
type Health struct {
	SchemaVersion int                `json:"schema_version"`
	Connector     string             `json:"connector"`
	State         HealthState        `json:"state"`
	Basis         HealthBasis        `json:"basis"`
	ObservedAt    time.Time          `json:"observed_at"`
	Reason        HealthReason       `json:"reason"`
	Retryable     bool               `json:"retryable"`
	Message       string             `json:"message"`
	Capabilities  []CapabilityHealth `json:"capabilities"`
}

// AuthenticationObservation describes credentials without folding them into
// upstream health.
type AuthenticationObservation struct {
	State      AuthenticationState `json:"state"`
	ObservedAt time.Time           `json:"observed_at"`
	Reason     HealthReason        `json:"reason"`
	Message    string              `json:"message"`
}

// OperationalAssessment keeps health and authentication observations together
// while preserving their independent meanings.
type OperationalAssessment struct {
	SchemaVersion  int                       `json:"schema_version"`
	Health         Health                    `json:"health"`
	Authentication AuthenticationObservation `json:"authentication"`
}

// OperationalError is implemented by typed connector errors that can expose a
// safe provider-neutral assessment without Core parsing error strings.
type OperationalError interface {
	error
	OperationalAssessment(time.Time) OperationalAssessment
}

// AssessmentFromError extracts a typed assessment through wrapped errors.
func AssessmentFromError(err error, observedAt time.Time) (OperationalAssessment, bool) {
	var operational OperationalError
	if !errors.As(err, &operational) {
		return OperationalAssessment{}, false
	}
	assessment := operational.OperationalAssessment(observedAt.UTC())
	return assessment, ValidateOperationalAssessment(assessment) == nil
}

// HealthyAssessment records successful evidence from a real operation.
func HealthyAssessment(connectorName string, basis HealthBasis, observedAt time.Time, authentication AuthenticationState, capabilities []Capability) (OperationalAssessment, error) {
	observations := make([]CapabilityHealth, 0, len(capabilities))
	for _, capability := range capabilities {
		if capability.ID == "" || capability.Availability != CapabilityAvailabilityAvailable {
			continue
		}
		observations = append(observations, CapabilityHealth{
			ID: capability.ID, Requirement: capability.Requirement, State: HealthHealthy,
			Reason: HealthReasonNone, Message: "Capability completed successfully.",
		})
	}
	health := Health{
		SchemaVersion: HealthSchemaVersion, Connector: connectorName, State: HealthHealthy,
		Basis: basis, ObservedAt: observedAt.UTC(), Reason: HealthReasonNone,
		Message: "The observed operation completed successfully.", Capabilities: observations,
	}
	authReason, authMessage := HealthReasonNone, "Authentication succeeded."
	if authentication == AuthenticationUnknown {
		authMessage = "Authentication was not established by this observation."
	}
	assessment := OperationalAssessment{
		SchemaVersion: HealthSchemaVersion,
		Health:        health,
		Authentication: AuthenticationObservation{
			State: authentication, ObservedAt: observedAt.UTC(), Reason: authReason, Message: authMessage,
		},
	}
	CanonicalizeOperationalAssessment(&assessment)
	return assessment, ValidateOperationalAssessment(assessment)
}

// AggregateCapabilityHealth deterministically derives a connector state from
// scoped observations. A required unavailable capability dominates; optional
// inability is degradation; any unknown prevents a healthy aggregate.
func AggregateCapabilityHealth(observations []CapabilityHealth) (HealthState, HealthReason, bool) {
	if len(observations) == 0 {
		return HealthUnknown, HealthReasonUnknownFailure, false
	}
	canonical := append([]CapabilityHealth(nil), observations...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].ID < canonical[j].ID })
	state, reason, retryable := HealthHealthy, HealthReasonNone, false
	for _, observation := range canonical {
		candidate := observation.State
		if observation.Requirement == CapabilityOptional && candidate == HealthUnavailable {
			candidate = HealthDegraded
		}
		if healthSeverity(candidate) > healthSeverity(state) {
			state, reason, retryable = candidate, observation.Reason, observation.Retryable
		} else if candidate == state && candidate != HealthHealthy {
			retryable = retryable || observation.Retryable
		}
	}
	return state, reason, retryable
}

// CanonicalizeOperationalAssessment makes serialized output stable.
func CanonicalizeOperationalAssessment(assessment *OperationalAssessment) {
	if assessment == nil {
		return
	}
	sort.Slice(assessment.Health.Capabilities, func(i, j int) bool {
		return assessment.Health.Capabilities[i].ID < assessment.Health.Capabilities[j].ID
	})
}

// ValidateOperationalAssessment rejects ambiguous, unsafe, or contradictory
// observations before they reach JSON, reports, or future status state.
func ValidateOperationalAssessment(assessment OperationalAssessment) error {
	if assessment.SchemaVersion != HealthSchemaVersion || assessment.Health.SchemaVersion != HealthSchemaVersion {
		return errors.New("unsupported operational health schema")
	}
	if strings.TrimSpace(assessment.Health.Connector) == "" {
		return errors.New("health connector is empty")
	}
	if !validHealthState(assessment.Health.State) || !validBasis(assessment.Health.Basis) || !validReason(assessment.Health.Reason) {
		return errors.New("health contains an unknown state, basis, or reason")
	}
	if assessment.Health.ObservedAt.IsZero() || assessment.Authentication.ObservedAt.IsZero() {
		return errors.New("operational observation time is missing")
	}
	if !validAuthenticationState(assessment.Authentication.State) || !validReason(assessment.Authentication.Reason) {
		return errors.New("authentication observation is invalid")
	}
	for label, value := range map[string]string{
		"connector": assessment.Health.Connector, "health message": assessment.Health.Message,
		"authentication message": assessment.Authentication.Message,
	} {
		if err := validateCapabilityText(label, value, 512, false); err != nil {
			return err
		}
	}
	seen := make(map[CapabilityID]struct{}, len(assessment.Health.Capabilities))
	for _, capability := range assessment.Health.Capabilities {
		if !validCapabilityID(capability.ID) || !validHealthState(capability.State) || !validReason(capability.Reason) {
			return fmt.Errorf("invalid health observation for capability %q", capability.ID)
		}
		if capability.Requirement != CapabilityRequired && capability.Requirement != CapabilityOptional {
			return fmt.Errorf("invalid requirement for capability %q", capability.ID)
		}
		if err := validateCapabilityText("capability health message", capability.Message, 512, false); err != nil {
			return fmt.Errorf("capability %q: %w", capability.ID, err)
		}
		if _, duplicate := seen[capability.ID]; duplicate {
			return fmt.Errorf("duplicate capability health %q", capability.ID)
		}
		seen[capability.ID] = struct{}{}
	}
	if len(assessment.Health.Capabilities) > 0 {
		state, reason, retryable := AggregateCapabilityHealth(assessment.Health.Capabilities)
		if assessment.Health.State != state || assessment.Health.Reason != reason || assessment.Health.Retryable != retryable {
			return errors.New("connector health conflicts with its capability observations")
		}
	}
	return nil
}

func healthSeverity(state HealthState) int {
	switch state {
	case HealthUnavailable:
		return 3
	case HealthDegraded:
		return 2
	case HealthUnknown:
		return 1
	default:
		return 0
	}
}

func validHealthState(state HealthState) bool {
	return state == HealthHealthy || state == HealthDegraded || state == HealthUnavailable || state == HealthUnknown
}

func validAuthenticationState(state AuthenticationState) bool {
	return state == AuthenticationUnknown || state == AuthenticationAuthenticated || state == AuthenticationRequired || state == AuthenticationRejected
}

func validBasis(basis HealthBasis) bool {
	return basis == HealthBasisAuthentication || basis == HealthBasisScan || basis == HealthBasisExtraction || basis == HealthBasisBackup
}

func validReason(reason HealthReason) bool {
	switch reason {
	case HealthReasonNone, HealthReasonAuthenticationRequired, HealthReasonAuthenticationRejected,
		HealthReasonRateLimited, HealthReasonUpstreamUnavailable, HealthReasonNetworkFailure,
		HealthReasonUnsupportedResponse, HealthReasonCapabilityRemoved, HealthReasonAPIBehaviorChanged,
		HealthReasonOperationCancelled, HealthReasonCapabilityFailed, HealthReasonUnknownFailure:
		return true
	default:
		return false
	}
}
