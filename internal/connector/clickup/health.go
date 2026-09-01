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

package clickup

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"alih/internal/connector"
)

// OperationalAssessment maps the typed, sanitized connector failure into the
// provider-neutral model. Provider text and raw response bodies are never used.
func (e *Error) OperationalAssessment(observedAt time.Time) connector.OperationalAssessment {
	healthState, reason, message, retryable := connector.HealthUnavailable, connector.HealthReasonUnknownFailure, "The connector operation failed.", false
	authState, authReason, authMessage := connector.AuthenticationAuthenticated, connector.HealthReasonNone, "Authentication succeeded before the failure."

	switch e.Kind {
	case ErrorAuthentication:
		healthState = connector.HealthHealthy
		reason = connector.HealthReasonNone
		message = "The provider responded; connector health is separate from credential validity."
		if e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden {
			authState, authReason, authMessage = connector.AuthenticationRejected, connector.HealthReasonAuthenticationRejected, "The provider rejected the credential."
		} else {
			healthState = connector.HealthUnknown
			reason = connector.HealthReasonAuthenticationRequired
			message = "Provider health was not established because no valid credential was available."
			authState, authReason, authMessage = connector.AuthenticationRequired, connector.HealthReasonAuthenticationRequired, "A valid credential is required."
		}
	case ErrorRateLimit:
		healthState, reason, message, retryable = connector.HealthDegraded, connector.HealthReasonRateLimited, "The provider continued to rate-limit the operation after bounded retries.", true
	case ErrorNetwork:
		if errors.Is(e.Cause, context.Canceled) {
			healthState, reason, message = connector.HealthUnknown, connector.HealthReasonOperationCancelled, "The operation was cancelled before connector health could be established."
			authState, authReason, authMessage = connector.AuthenticationUnknown, connector.HealthReasonOperationCancelled, "Authentication was not established before cancellation."
		} else {
			healthState, reason, message, retryable = connector.HealthUnavailable, connector.HealthReasonNetworkFailure, "The provider could not be reached after bounded retries.", true
			authState, authReason, authMessage = connector.AuthenticationUnknown, connector.HealthReasonNetworkFailure, "Authentication could not be established through the failed network operation."
		}
	case ErrorResponse:
		healthState, reason, message = connector.HealthUnavailable, connector.HealthReasonUnsupportedResponse, "The provider response was incompatible with the connector contract."
	case ErrorAPI:
		switch {
		case e.StatusCode == http.StatusNotFound:
			healthState, reason, message = connector.HealthUnavailable, connector.HealthReasonCapabilityRemoved, "A required provider endpoint or capability was not found."
		case e.StatusCode >= 500:
			healthState, reason, message, retryable = connector.HealthUnavailable, connector.HealthReasonUpstreamUnavailable, "The provider remained unavailable after bounded retries.", true
		default:
			healthState, reason, message = connector.HealthDegraded, connector.HealthReasonAPIBehaviorChanged, "The provider rejected an operation required by the connector contract."
		}
	}
	basis := operationBasis(e.Operation)
	if basis == connector.HealthBasisAuthentication && e.Kind != ErrorAuthentication {
		authState, authReason, authMessage = connector.AuthenticationUnknown, reason, "Authentication could not be established from the failed provider response."
	}

	capabilities := affectedCapabilities(e.Operation, healthState, reason, retryable, message)
	if len(capabilities) > 0 {
		healthState, reason, retryable = connector.AggregateCapabilityHealth(capabilities)
	}
	assessment := connector.OperationalAssessment{
		SchemaVersion: connector.HealthSchemaVersion,
		Health: connector.Health{
			SchemaVersion: connector.HealthSchemaVersion, Connector: "clickup", State: healthState,
			Basis: basis, ObservedAt: observedAt.UTC(), Reason: reason, Retryable: retryable,
			Message: message, Capabilities: capabilities,
		},
		Authentication: connector.AuthenticationObservation{
			State: authState, ObservedAt: observedAt.UTC(), Reason: authReason, Message: authMessage,
		},
	}
	connector.CanonicalizeOperationalAssessment(&assessment)
	return assessment
}

func operationBasis(operation string) connector.HealthBasis {
	switch operation {
	case "validate credential", "get authorized user", "get authorized workspaces", "describe authentication health":
		return connector.HealthBasisAuthentication
	case "start raw extraction", "describe extraction capabilities", "describe extraction health":
		return connector.HealthBasisExtraction
	default:
		return connector.HealthBasisScan
	}
}

func affectedCapabilities(operation string, state connector.HealthState, reason connector.HealthReason, retryable bool, message string) []connector.CapabilityHealth {
	ids := operationCapabilities(operation)
	result := make([]connector.CapabilityHealth, 0, len(ids))
	for _, id := range ids {
		result = append(result, connector.CapabilityHealth{
			ID: id, Requirement: capabilityRequirement(id), State: state,
			Reason: reason, Retryable: retryable, Message: message,
		})
	}
	return result
}

func operationCapabilities(operation string) []connector.CapabilityID {
	var ids []connector.CapabilityID
	switch operation {
	case "list Spaces", "list Folders", "list folderless Lists", "list Lists in Folder", "validate Workspace for scan":
		ids = []connector.CapabilityID{connector.CapabilityWorkspaceData}
	case "list Custom Fields":
		ids = []connector.CapabilityID{connector.CapabilityCustomFields}
	case "list Tasks", "validate Task hierarchy":
		ids = []connector.CapabilityID{connector.CapabilityItems, connector.CapabilityRelationships}
	case "get Task inventory details":
		ids = []connector.CapabilityID{
			connector.CapabilityItems, connector.CapabilityAttachmentMetadata,
			connector.CapabilityCustomFields, connector.CapabilityRelationships,
		}
	case "list Task comments", "list threaded comment replies":
		ids = []connector.CapabilityID{connector.CapabilityComments}
	case "start raw extraction", "describe extraction capabilities":
		ids = []connector.CapabilityID{connector.CapabilityRawEvidence}
	case "describe scan capabilities", "describe scan health":
		ids = []connector.CapabilityID{
			connector.CapabilityWorkspaceData, connector.CapabilityItems, connector.CapabilityComments,
			connector.CapabilityAttachmentMetadata, connector.CapabilityCustomFields, connector.CapabilityRelationships,
		}
	case "describe extraction health":
		ids = []connector.CapabilityID{connector.CapabilityRawEvidence}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func capabilityRequirement(id connector.CapabilityID) connector.CapabilityRequirement {
	// ClickUp's current contract declares every implemented extraction capability
	// required. Keeping this local avoids teaching Core provider-specific scope.
	return connector.CapabilityRequired
}
