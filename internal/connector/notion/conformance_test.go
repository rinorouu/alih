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
	"context"
	"net/http"
	"testing"
	"time"

	"alih/internal/conformance"
)

// TestNotionConformsToTheConnectorContract runs Alih's shared conformance suite
// against this connector.
//
// Unlike the ClickUp conformance test, this one supplies a fake transport and a
// fake credential, so the archive pipeline contract actually runs rather than
// skipping: Notion is driven through real extraction, archive, verification,
// report and organize implementations without touching the network.
func TestNotionConformsToTheConnectorContract(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	client := NewClient(fixture.client())
	client.sleep = func(context.Context, time.Duration) error { return nil }

	conformance.Run(t, conformance.Subject{
		Connector:  client,
		Normalizer: Normalizer{},
		// Notion serves files from signed storage URLs that carry their own
		// authorisation. Alih must send its credential to no attachment host,
		// which is exactly the fail-closed default, so this connector declares
		// no credential hosts at all.
		AttachmentIntent: conformance.AttachmentsAreAnonymous,
		FieldSemantics:   FieldSemantics{},
		Credential:       fakeToken,
		// The archive writer only uses this client to download attachments.
		// This connector archives none in v0, so it never fetches anything.
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			t.Errorf("the archive writer attempted an attachment download: %s", r.URL)
			return jsonResponse(r, http.StatusNotFound, `{}`)
		})},
		SampleErrors: []error{
			&Error{Kind: ErrorAuthentication, Operation: "get authorized bot", StatusCode: 401},
			&Error{Kind: ErrorRateLimit, Operation: "query data source", StatusCode: 429},
			&Error{Kind: ErrorForbidden, Operation: "get block children", StatusCode: 403},
			&Error{Kind: ErrorNetwork, Operation: "search connected content"},
			&Error{Kind: ErrorAPI, Operation: "get database", StatusCode: 503},
		},
	})
}
