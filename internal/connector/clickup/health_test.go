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
	"strings"
	"testing"
	"time"

	"alih/internal/connector"
)

func TestErrorOperationalAssessment(t *testing.T) {
	t.Parallel()
	observed := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		err        *Error
		health     connector.HealthState
		auth       connector.AuthenticationState
		reason     connector.HealthReason
		capability connector.CapabilityID
		retryable  bool
	}{
		{"missing credential", &Error{Kind: ErrorAuthentication, Operation: "validate credential"}, connector.HealthUnknown, connector.AuthenticationRequired, connector.HealthReasonAuthenticationRequired, "", false},
		{"401 separates auth", &Error{Kind: ErrorAuthentication, Operation: "get authorized user", StatusCode: 401}, connector.HealthHealthy, connector.AuthenticationRejected, connector.HealthReasonNone, "", false},
		{"403 separates auth", &Error{Kind: ErrorAuthentication, Operation: "get authorized workspaces", StatusCode: 403}, connector.HealthHealthy, connector.AuthenticationRejected, connector.HealthReasonNone, "", false},
		{"404 removed item capability", &Error{Kind: ErrorAPI, Operation: "list Tasks", StatusCode: 404}, connector.HealthUnavailable, connector.AuthenticationAuthenticated, connector.HealthReasonCapabilityRemoved, connector.CapabilityItems, false},
		{"429 rate limit", &Error{Kind: ErrorRateLimit, Operation: "list Task comments", StatusCode: 429}, connector.HealthDegraded, connector.AuthenticationAuthenticated, connector.HealthReasonRateLimited, connector.CapabilityComments, true},
		{"network", &Error{Kind: ErrorNetwork, Operation: "list Spaces", Cause: errors.New("dial failed")}, connector.HealthUnavailable, connector.AuthenticationUnknown, connector.HealthReasonNetworkFailure, connector.CapabilityWorkspaceData, true},
		{"timeout", &Error{Kind: ErrorNetwork, Operation: "list Tasks", Cause: context.DeadlineExceeded}, connector.HealthUnavailable, connector.AuthenticationUnknown, connector.HealthReasonNetworkFailure, connector.CapabilityItems, true},
		{"cancel", &Error{Kind: ErrorNetwork, Operation: "list Tasks", Cause: context.Canceled}, connector.HealthUnknown, connector.AuthenticationUnknown, connector.HealthReasonOperationCancelled, connector.CapabilityItems, false},
		{"malformed response", &Error{Kind: ErrorResponse, Operation: "list Tasks", Cause: errors.New("secret provider body\n")}, connector.HealthUnavailable, connector.AuthenticationAuthenticated, connector.HealthReasonUnsupportedResponse, connector.CapabilityItems, false},
		{"500", &Error{Kind: ErrorAPI, Operation: "list Custom Fields", StatusCode: 500}, connector.HealthUnavailable, connector.AuthenticationAuthenticated, connector.HealthReasonUpstreamUnavailable, connector.CapabilityCustomFields, true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assessment := test.err.OperationalAssessment(observed)
			if err := connector.ValidateOperationalAssessment(assessment); err != nil {
				t.Fatalf("assessment invalid: %v", err)
			}
			if assessment.Health.State != test.health || assessment.Authentication.State != test.auth || assessment.Health.Reason != test.reason || assessment.Health.Retryable != test.retryable {
				t.Fatalf("assessment = health %s auth %s reason %s retryable %t", assessment.Health.State, assessment.Authentication.State, assessment.Health.Reason, assessment.Health.Retryable)
			}
			if !assessment.Health.ObservedAt.Equal(observed) {
				t.Fatalf("observed_at = %s", assessment.Health.ObservedAt)
			}
			if test.capability != "" {
				found := false
				for _, capability := range assessment.Health.Capabilities {
					found = found || capability.ID == test.capability
				}
				if !found {
					t.Fatalf("affected capabilities %#v omit %s", assessment.Health.Capabilities, test.capability)
				}
			}
			encoded := assessment.Health.Message + assessment.Authentication.Message
			if strings.Contains(encoded, "secret provider body") || strings.ContainsAny(encoded, "\r\n") {
				t.Fatalf("assessment leaked uncontrolled error text: %q", encoded)
			}
		})
	}
}

func TestAssessmentFromWrappedClickUpError(t *testing.T) {
	t.Parallel()
	err := errors.New("outer: " + (&Error{Kind: ErrorAPI, Operation: "list Tasks", StatusCode: http.StatusNotFound}).Error())
	if _, ok := connector.AssessmentFromError(err, time.Now()); ok {
		t.Fatal("AssessmentFromError parsed untyped text")
	}
	wrapper := errors.Join(errors.New("outer"), &Error{Kind: ErrorAPI, Operation: "list Tasks", StatusCode: http.StatusNotFound})
	if assessment, ok := connector.AssessmentFromError(wrapper, time.Now()); !ok || assessment.Health.Reason != connector.HealthReasonCapabilityRemoved {
		t.Fatalf("typed wrapped assessment = %#v, %t", assessment, ok)
	}
}

func TestAssessmentBasisReflectsTheObservingWork(t *testing.T) {
	t.Parallel()
	observed := time.Date(2026, 8, 31, 11, 0, 0, 0, time.UTC)
	tests := []struct {
		operation string
		kind      ErrorKind
		basis     connector.HealthBasis
		auth      connector.AuthenticationState
	}{
		{"validate credential", ErrorAuthentication, connector.HealthBasisAuthentication, connector.AuthenticationRequired},
		{"get authorized workspaces", ErrorResponse, connector.HealthBasisAuthentication, connector.AuthenticationUnknown},
		{"describe authentication health", ErrorResponse, connector.HealthBasisAuthentication, connector.AuthenticationUnknown},
		{"start raw extraction", ErrorResponse, connector.HealthBasisExtraction, connector.AuthenticationAuthenticated},
		{"describe extraction health", ErrorResponse, connector.HealthBasisExtraction, connector.AuthenticationAuthenticated},
		{"list Tasks", ErrorResponse, connector.HealthBasisScan, connector.AuthenticationAuthenticated},
	}
	for _, test := range tests {
		test := test
		t.Run(test.operation, func(t *testing.T) {
			t.Parallel()
			assessment := (&Error{Kind: test.kind, Operation: test.operation}).OperationalAssessment(observed)
			if err := connector.ValidateOperationalAssessment(assessment); err != nil {
				t.Fatalf("assessment invalid: %v", err)
			}
			if assessment.Health.Basis != test.basis {
				t.Fatalf("basis = %s, want %s", assessment.Health.Basis, test.basis)
			}
			if assessment.Authentication.State != test.auth {
				t.Fatalf("authentication = %s, want %s", assessment.Authentication.State, test.auth)
			}
		})
	}
}
