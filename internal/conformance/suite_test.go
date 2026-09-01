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

package conformance

import (
	"net/http"
	"testing"
	"time"

	"alih/internal/connector"
	"alih/internal/model"
	"alih/internal/snapshot"
)

// referenceSubject is the fake, deliberately non-ClickUp connector as a
// conformance Subject. It is what a connector author's own test would build.
func referenceSubject() Subject {
	fake := newFakeConnector()
	return Subject{
		Connector:  fake,
		Normalizer: fakeNormalizer{fake},
		// The fake's attachments are served from a pre-signed URL, so no
		// credential should travel with them.
		AttachmentIntent: AttachmentsAreAnonymous,
		FieldSemantics:   fakeFieldSemantics{},
		Credential:       "conformance-fake-credential-never-real",
		SampleErrors:     fakeSampleErrors(),
		HTTPClient:       attachmentClient(http.StatusOK),
	}
}

// fakeNormalizer adapts the fake connector to the exporter's normalizer
// contract, which is a separate interface from extraction on purpose.
type fakeNormalizer struct{ fake *fakeConnector }

func (n fakeNormalizer) Connector() string   { return n.fake.Name() }
func (n fakeNormalizer) DisplayName() string { return n.fake.DisplayName() }
func (n fakeNormalizer) NormalizeSnapshot(evidence snapshot.Evidence) (model.Archive, error) {
	return n.fake.NormalizeSnapshot(evidence)
}

// TestTheReferenceConnectorConforms is the suite proving itself: a connector
// that satisfies Alih's contracts passes every one of them.
func TestTheReferenceConnectorConforms(t *testing.T) {
	t.Parallel()
	Run(t, referenceSubject())
}

// TestTheSuiteRunsWhatTheSubjectSupports proves the suite degrades honestly. A
// subject that offers only identity still gets its identity checked, and the
// contracts it cannot support are skipped rather than failed or silently
// passed.
func TestTheSuiteRunsWhatTheSubjectSupports(t *testing.T) {
	t.Parallel()

	minimal := Subject{
		Connector:        identityOnlyConnector{},
		AttachmentIntent: AttachmentsAreNotSupported,
	}
	Run(t, minimal)
}

type identityOnlyConnector struct{}

func (identityOnlyConnector) Name() string { return "minimal-source" }

var _ connector.Connector = identityOnlyConnector{}

// fakeSampleErrors are the failures the reference connector can produce, one
// per situation, so the error contract has something real to classify.
func fakeSampleErrors() []error {
	return []error{
		fakeOperationalError{state: connector.HealthUnavailable, reason: connector.HealthReasonUpstreamUnavailable, auth: connector.AuthenticationAuthenticated},
		fakeOperationalError{state: connector.HealthDegraded, reason: connector.HealthReasonRateLimited, auth: connector.AuthenticationAuthenticated},
		fakeOperationalError{state: connector.HealthHealthy, reason: connector.HealthReasonNone, auth: connector.AuthenticationRejected},
	}
}

type fakeOperationalError struct {
	state  connector.HealthState
	reason connector.HealthReason
	auth   connector.AuthenticationState
}

func (e fakeOperationalError) Error() string {
	return "example provider failure: " + string(e.reason)
}

func (e fakeOperationalError) OperationalAssessment(observed time.Time) connector.OperationalAssessment {
	return connector.OperationalAssessment{
		SchemaVersion: connector.HealthSchemaVersion,
		Health: connector.Health{
			SchemaVersion: connector.HealthSchemaVersion, Connector: fakeConnectorName,
			State: e.state, Basis: connector.HealthBasisScan, ObservedAt: observed.UTC(),
			Reason: e.reason, Retryable: e.state != connector.HealthHealthy,
			Message: "the example provider reported " + string(e.reason),
		},
		Authentication: connector.AuthenticationObservation{
			State: e.auth, ObservedAt: observed.UTC(), Reason: e.reason,
			Message: "the example provider reported " + string(e.reason),
		},
	}
}
