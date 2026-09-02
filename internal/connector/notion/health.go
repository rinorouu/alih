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

package notion

import (
	"time"

	"alih/internal/connector"
)

// OperationalAssessment maps a Notion failure onto Alih's provider-neutral
// health vocabulary.
//
// This is the whole error boundary: Core never reads a Notion message or code,
// it reads the reason produced here. Health and authentication stay separate
// because a rejected token says nothing about whether Notion is up, and telling
// an operator the provider is down when their token expired sends them to the
// wrong place.
func (e *Error) OperationalAssessment(observedAt time.Time) connector.OperationalAssessment {
	healthState := connector.HealthHealthy
	reason := connector.HealthReasonNone
	message := "Notion responded normally."
	retryable := false

	authState := connector.AuthenticationAuthenticated
	authReason := connector.HealthReasonNone
	authMessage := "The integration token was accepted."

	switch e.Kind {
	case ErrorAuthentication:
		// The credential is the problem. Notion itself is fine.
		authState, authReason = connector.AuthenticationRejected, connector.HealthReasonAuthenticationRejected
		authMessage = "Notion rejected the integration token."
		message = "Notion did not accept the credential; the provider itself reported no fault."

	case ErrorForbidden, ErrorNotFound:
		// Notion returns 404 for content an integration cannot see, so a
		// missing object and an unshared object are indistinguishable from
		// outside. Neither is a provider fault and neither is a bad token.
		healthState, reason = connector.HealthDegraded, connector.HealthReasonCapabilityRemoved
		message = "Notion did not make some requested content available to this integration."

	case ErrorRateLimit:
		healthState, reason, retryable = connector.HealthDegraded, connector.HealthReasonRateLimited, true
		message = "Notion rate limited the connector after bounded retries."

	case ErrorNetwork:
		healthState, reason, retryable = connector.HealthUnavailable, connector.HealthReasonNetworkFailure, true
		message = "Notion could not be reached."
		authState, authReason = connector.AuthenticationUnknown, connector.HealthReasonNetworkFailure
		authMessage = "Authentication could not be established because Notion could not be reached."

	case ErrorResponse:
		healthState, reason = connector.HealthDegraded, connector.HealthReasonUnsupportedResponse
		message = "Notion returned a response this connector could not read."
		authState, authReason = connector.AuthenticationUnknown, connector.HealthReasonUnsupportedResponse
		authMessage = "Authentication could not be established from an unreadable response."

	default:
		switch {
		case e.StatusCode >= 500:
			healthState, reason, retryable = connector.HealthUnavailable, connector.HealthReasonUpstreamUnavailable, true
			message = "Notion remained unavailable after bounded retries."
		default:
			healthState, reason = connector.HealthDegraded, connector.HealthReasonAPIBehaviorChanged
			message = "Notion rejected an operation this connector requires."
		}
		authState, authReason = connector.AuthenticationUnknown, reason
		authMessage = "Authentication could not be established from the failed provider response."
	}

	assessment := connector.OperationalAssessment{
		SchemaVersion: connector.HealthSchemaVersion,
		Health: connector.Health{
			SchemaVersion: connector.HealthSchemaVersion, Connector: "notion", State: healthState,
			Basis: operationBasis(e.Operation), ObservedAt: observedAt.UTC(), Reason: reason,
			Retryable: retryable, Message: message,
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
	case "validate credential", "get authorized bot":
		return connector.HealthBasisAuthentication
	default:
		return connector.HealthBasisScan
	}
}
