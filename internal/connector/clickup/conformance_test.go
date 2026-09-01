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

package clickup_test

import (
	"testing"

	"alih/internal/conformance"
	"alih/internal/connector/clickup"
)

// TestClickUpConformsToTheConnectorContract runs the shared conformance suite
// against the reference connector.
//
// This file is also the proof that the suite is reusable: it lives in the
// connector's own package, imports nothing but the suite and the adapter, and
// is exactly what a second connector's test would look like.
//
// ClickUp passes because it satisfies Alih's contracts. Where a contract needs
// a live provider the suite skips rather than reaching api.clickup.com, so the
// contracts asserted here are the ones that hold without network access:
// identity, capability declaration, credential scoping and credential hosts.
// The full pipeline is covered separately by the fake connector, which can be
// driven end to end without a network.
func TestClickUpConformsToTheConnectorContract(t *testing.T) {
	t.Parallel()

	conformance.Run(t, conformance.Subject{
		Connector:  clickup.NewClient(nil),
		Normalizer: clickup.Normalizer{},
		// ClickUp serves attachments from its API host when the archived
		// reference is an API URL, so Alih's credential is expected there.
		AttachmentIntent: conformance.AttachmentsAreAuthenticated,
		FieldSemantics:   clickup.FieldSemantics{},
		// One representative failure per situation ClickUp can actually
		// produce, so the suite can prove each carries a distinct Core reason
		// rather than collapsing into UNKNOWN_FAILURE.
		SampleErrors: []error{
			&clickup.Error{Kind: clickup.ErrorAuthentication, Operation: "authenticate", StatusCode: 401},
			&clickup.Error{Kind: clickup.ErrorRateLimit, Operation: "list containers", StatusCode: 429},
			&clickup.Error{Kind: clickup.ErrorAPI, Operation: "list records", StatusCode: 503},
			&clickup.Error{Kind: clickup.ErrorNetwork, Operation: "list records"},
			&clickup.Error{Kind: clickup.ErrorResponse, Operation: "list records"},
		},
		// No credential and no HTTP client are supplied, so the pipeline
		// contracts skip instead of contacting the provider.
	})
}
