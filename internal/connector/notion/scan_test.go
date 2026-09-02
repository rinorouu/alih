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
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"alih/internal/connector"
)

func testClient(t *testing.T) (*Client, *notionFixture) {
	t.Helper()
	fixture := newFixture(t)
	client := NewClient(fixture.client())
	client.sleep = func(context.Context, time.Duration) error { return nil }
	return client, fixture
}

func TestAuthenticateReportsTheBotAndItsWorkspace(t *testing.T) {
	t.Parallel()
	client, _ := testClient(t)

	authentication, err := client.Authenticate(context.Background(), fakeToken)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if authentication.Identity.ID != "bot-1" {
		t.Errorf("identity = %+v", authentication.Identity)
	}
	if len(authentication.Workspaces) != 1 || authentication.Workspaces[0].ID != "ws-notion-1" {
		t.Fatalf("workspaces = %+v", authentication.Workspaces)
	}
}

func TestAnEmptyCredentialIsRefusedBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	client, fixture := testClient(t)

	if _, err := client.Authenticate(context.Background(), "   "); err == nil {
		t.Fatal("an empty credential was accepted")
	}
	if len(fixture.requests) != 0 {
		t.Errorf("a request was made with no credential: %s", fixture.describe())
	}
}

// TestTheBlockTreeIsWalkedToItsFullDepth is the point of choosing Notion. A
// connector that assumed ClickUp's fixed hierarchy would stop at level one.
func TestTheBlockTreeIsWalkedToItsFullDepth(t *testing.T) {
	t.Parallel()
	client, fixture := testClient(t)

	result, state, err := client.traverse(context.Background(), fakeToken, connector.Workspace{ID: "ws-notion-1"})
	if err != nil {
		t.Fatalf("traverse: %v", err)
	}
	if len(state.blocks) != 4 {
		t.Fatalf("found %d blocks, want the whole four-level tree: %s", len(state.blocks), fixture.describe())
	}
	for _, id := range []string{"b1", "b2", "b3", "b4"} {
		if fixture.called(http.MethodGet, "/v1/blocks/"+id+"/children") == 0 && id != "b4" {
			t.Errorf("never descended into block %s", id)
		}
	}
	// The deepest block must still know its page, so it lands in a collection.
	deepest := state.blocks[len(state.blocks)-1]
	if deepest.ID != "b4" || deepest.ParentID != "b3" || deepest.PageID != "page-1" {
		t.Errorf("deepest block = %+v", deepest)
	}
	// Every block is nested: a top-level block sits under its page, and the
	// rest sit under the block above them.
	if result.Inventory.NestedRecords != 4 {
		t.Errorf("nested records = %d, want every block counted as nested", result.Inventory.NestedRecords)
	}
}

func TestCursorPaginationFollowsEveryPage(t *testing.T) {
	t.Parallel()
	client, fixture := testClient(t)

	_, state, err := client.traverse(context.Background(), fakeToken, connector.Workspace{ID: "ws-notion-1"})
	if err != nil {
		t.Fatalf("traverse: %v", err)
	}
	if len(state.dataSources) != 2 {
		t.Fatalf("found %d data sources; the second search page was not followed: %s",
			len(state.dataSources), fixture.describe())
	}
	if fixture.called(http.MethodPost, "/v1/search") != 2 {
		t.Errorf("search was called %d times, want 2", fixture.called(http.MethodPost, "/v1/search"))
	}
}

func TestPaginationRefusesAMalformedOrLoopingCursor(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name, body, want string
	}{
		{"more results but no cursor", `{"object":"list","results":[],"has_more":true,"next_cursor":null}`, "supplied no cursor"},
		{"empty cursor", `{"object":"list","results":[],"has_more":true,"next_cursor":"  "}`, "supplied no cursor"},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			client := NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return jsonResponse(r, 200, testCase.body)
			})})
			err := client.paginate(context.Background(), "test",
				func(string) ([]byte, error) { return []byte(testCase.body), nil },
				func(json.RawMessage) error { return nil })
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func TestARepeatedCursorIsRefusedRatherThanLooped(t *testing.T) {
	t.Parallel()
	client := NewClient(nil)
	calls := 0
	err := client.paginate(context.Background(), "test",
		func(string) ([]byte, error) {
			calls++
			return []byte(`{"object":"list","results":[],"has_more":true,"next_cursor":"same"}`), nil
		},
		func(json.RawMessage) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "repeated pagination cursor") {
		t.Fatalf("error = %v", err)
	}
	if calls > 3 {
		t.Errorf("made %d calls before noticing the loop", calls)
	}
}

// TestATruncatedQueryIsRecordedAsALimitation covers Notion's 10,000 result
// ceiling. Treating it as the end of the data would produce a short archive
// that looks complete.
func TestATruncatedQueryIsRecordedAsALimitation(t *testing.T) {
	t.Parallel()
	client := NewClient(nil)
	state := &source{}

	err := client.paginateWithStatus(context.Background(), "query data source",
		func(string) ([]byte, error) {
			return []byte(`{"object":"list","results":[],"has_more":false,"next_cursor":null,
				"request_status":{"type":"incomplete","reason":"query_result_limit_reached"}}`), nil
		},
		func(json.RawMessage) error { return nil },
		func(incomplete bool, reason string) {
			if incomplete {
				state.truncated = true
				state.limitations = append(state.limitations, "truncated: "+reason)
			}
		})
	if err != nil {
		t.Fatalf("paginate: %v", err)
	}
	if !state.truncated {
		t.Fatal("a query Notion reported as incomplete was treated as complete")
	}
	if len(state.limitations) != 1 || !strings.Contains(state.limitations[0], "query_result_limit_reached") {
		t.Errorf("limitations = %v", state.limitations)
	}
}

func TestAccessScopeLimitationIsAlwaysRecorded(t *testing.T) {
	t.Parallel()
	client, _ := testClient(t)

	_, state, err := client.traverse(context.Background(), fakeToken, connector.Workspace{ID: "ws-notion-1"})
	if err != nil {
		t.Fatalf("traverse: %v", err)
	}
	var stated bool
	for _, limitation := range state.limitations {
		if strings.Contains(limitation, "explicitly connected to this integration") {
			stated = true
		}
	}
	if !stated {
		t.Fatalf("the archive does not disclose that Notion only exposes connected content: %v", state.limitations)
	}
}

func TestInventoryUsesNotionsOwnVocabulary(t *testing.T) {
	t.Parallel()
	client, _ := testClient(t)

	result, _, err := client.traverse(context.Background(), fakeToken, connector.Workspace{ID: "ws-notion-1"})
	if err != nil {
		t.Fatalf("traverse: %v", err)
	}
	for _, kind := range []string{"database"} {
		if _, present := result.Inventory.ContainerKinds[kind]; !present {
			t.Errorf("container kinds do not include %q: %v", kind, result.Inventory.ContainerKinds)
		}
	}
	for _, kind := range []string{"page", "block"} {
		if _, present := result.Inventory.RecordKinds[kind]; !present {
			t.Errorf("record kinds do not include %q: %v", kind, result.Inventory.RecordKinds)
		}
	}
	for _, foreign := range []string{"space", "folder", "list", "task", "subtask"} {
		if _, present := result.Inventory.ContainerKinds[foreign]; present {
			t.Errorf("inventory borrowed another provider's word %q", foreign)
		}
		if _, present := result.Inventory.RecordKinds[foreign]; present {
			t.Errorf("inventory borrowed another provider's word %q", foreign)
		}
	}
}

func TestRateLimitIsRetriedThenMappedHonestly(t *testing.T) {
	t.Parallel()
	client, fixture := testClient(t)
	fixture.rateLimitOnce["/v1/users/me"] = true

	if _, err := client.Authenticate(context.Background(), fakeToken); err != nil {
		t.Fatalf("a single rate limit should have been retried: %v", err)
	}
	if fixture.called(http.MethodGet, "/v1/users/me") != 2 {
		t.Errorf("users/me called %d times, want a retry", fixture.called(http.MethodGet, "/v1/users/me"))
	}
}

func TestPersistentFailureIsNotHidden(t *testing.T) {
	t.Parallel()
	client, fixture := testClient(t)
	fixture.failures["/v1/users/me"] = http.StatusInternalServerError

	_, err := client.Authenticate(context.Background(), fakeToken)
	if err == nil {
		t.Fatal("a persistently failing provider was reported as success")
	}
	assessment, ok := connector.AssessmentFromError(err, time.Unix(0, 0).UTC())
	if !ok {
		t.Fatalf("error carries no operational assessment: %v", err)
	}
	if assessment.Health.State != connector.HealthUnavailable {
		t.Errorf("health = %q, want UNAVAILABLE", assessment.Health.State)
	}
}

func TestErrorsNeverEchoTheCredential(t *testing.T) {
	t.Parallel()
	client, fixture := testClient(t)
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		fixture.failures["/v1/users/me"] = status
		_, err := client.Authenticate(context.Background(), fakeToken)
		if err == nil {
			t.Fatalf("status %d was accepted", status)
		}
		if strings.Contains(err.Error(), fakeToken) {
			t.Fatalf("status %d echoed the credential: %v", status, err)
		}
	}
}

func TestARejectedTokenBlamesTheCredentialNotTheProvider(t *testing.T) {
	t.Parallel()
	client, fixture := testClient(t)
	fixture.failures["/v1/users/me"] = http.StatusUnauthorized

	_, err := client.Authenticate(context.Background(), fakeToken)
	assessment, ok := connector.AssessmentFromError(err, time.Unix(0, 0).UTC())
	if !ok {
		t.Fatalf("no assessment: %v", err)
	}
	if assessment.Authentication.State != connector.AuthenticationRejected {
		t.Errorf("authentication = %q, want REJECTED", assessment.Authentication.State)
	}
	if assessment.Health.State != connector.HealthHealthy {
		t.Errorf("health = %q; a bad token must not blame the provider", assessment.Health.State)
	}
}
