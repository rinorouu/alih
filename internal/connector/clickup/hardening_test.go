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
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"alih/internal/connector"
)

// bulkFixture serves a Workspace of arbitrary size from one List so that
// pagination, ordering and duplicate protection can be exercised at a scale a
// hand-written fixture cannot reach.
type bulkFixture struct {
	t         *testing.T
	token     string
	tasks     int
	perPage   int
	subtaskOf map[string]string
	requests  int
}

func (fixture *bulkFixture) taskID(index int) string { return fmt.Sprintf("t%05d", index) }

func (fixture *bulkFixture) roundTrip(request *http.Request) (*http.Response, error) {
	fixture.requests++
	path := request.URL.Path
	query := request.URL.Query()
	switch {
	case path == "/api/v2/team/w1/space":
		if query.Get("archived") == "false" {
			return fixtureResponse(http.StatusOK, `{"spaces":[{"id":"s1","name":"Space"}]}`), nil
		}
		return fixtureResponse(http.StatusOK, `{"spaces":[]}`), nil
	case path == "/api/v2/space/s1/folder":
		return fixtureResponse(http.StatusOK, `{"folders":[]}`), nil
	case path == "/api/v2/space/s1/list":
		if query.Get("archived") == "false" {
			return fixtureResponse(http.StatusOK, `{"lists":[{"id":"l1","name":"List"}]}`), nil
		}
		return fixtureResponse(http.StatusOK, `{"lists":[]}`), nil
	case strings.HasSuffix(path, "/field"):
		return fixtureResponse(http.StatusOK, `{"fields":[]}`), nil
	case path == "/api/v2/list/l1/task":
		return fixture.taskPage(query), nil
	case strings.HasSuffix(path, "/comment"):
		return fixtureResponse(http.StatusOK, `{"comments":[]}`), nil
	case strings.HasPrefix(path, "/api/v2/task/"):
		id := strings.TrimPrefix(path, "/api/v2/task/")
		parent := "null"
		if of, nested := fixture.subtaskOf[id]; nested {
			parent = `"` + of + `"`
		}
		return fixtureResponse(http.StatusOK, taskDetailJSON(id, "l1", parent, `[]`, `[]`, `[]`)), nil
	}
	fixture.t.Errorf("unexpected request %s", path)
	return fixtureResponse(http.StatusNotFound, `{}`), nil
}

func (fixture *bulkFixture) taskPage(query map[string][]string) *http.Response {
	if query["archived"][0] == "true" {
		return fixtureResponse(http.StatusOK, `{"tasks":[]}`)
	}
	page := 0
	fmt.Sscanf(query["page"][0], "%d", &page)
	start := page * fixture.perPage
	if start >= fixture.tasks {
		return fixtureResponse(http.StatusOK, `{"tasks":[]}`)
	}
	end := start + fixture.perPage
	if end > fixture.tasks {
		end = fixture.tasks
	}
	var builder strings.Builder
	builder.WriteString(`{"tasks":[`)
	for index := start; index < end; index++ {
		if index > start {
			builder.WriteByte(',')
		}
		id := fixture.taskID(index)
		parent := "null"
		if of, nested := fixture.subtaskOf[id]; nested {
			parent = `"` + of + `"`
		}
		fmt.Fprintf(&builder, `{"id":%q,"parent":%s,"list":{"id":"l1"}}`, id, parent)
	}
	builder.WriteString(`]}`)
	return fixtureResponse(http.StatusOK, builder.String())
}

// TestScanHandlesLargePaginatedWorkspace covers PRD section 21's pagination
// requirement at a scale where an accidental quadratic path or a pagination
// off-by-one would show up.
func TestScanHandlesLargePaginatedWorkspace(t *testing.T) {
	t.Parallel()

	const total, perPage = 2000, 100
	subtasks := map[string]string{}
	fixture := &bulkFixture{t: t, token: "bulk-token", tasks: total, perPage: perPage, subtaskOf: subtasks}
	// Every second task is nested under its predecessor, so ancestry has to be
	// resolved for half the Workspace.
	for index := 1; index < total; index += 2 {
		subtasks[fixture.taskID(index)] = fixture.taskID(index - 1)
	}
	client := fixtureClient(t, fixture.roundTrip)

	started := time.Now()
	result, err := client.Scan(context.Background(), fixture.token, connector.Workspace{ID: "w1", Name: "Bulk"})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)

	if result.Inventory.Tasks != total/2 || result.Inventory.Subtasks != total/2 {
		t.Fatalf("inventory tasks=%d subtasks=%d, want %d each", result.Inventory.Tasks, result.Inventory.Subtasks, total/2)
	}
	if result.Inventory.Lists != 1 || result.Inventory.Spaces != 1 {
		t.Fatalf("hierarchy inventory = %#v", result.Inventory)
	}
	// The traversal costs one detail request and one comment request per task,
	// plus pagination and hierarchy overhead. Holding it under three per task
	// catches a runaway or repeated traversal without pinning the exact plan.
	if fixture.requests > 3*total+64 {
		t.Fatalf("traversal issued %d requests for %d tasks, which is not linear", fixture.requests, total)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("traversal of %d tasks took %s", total, elapsed)
	}
}

// TestNormalizeLargeWorkspaceStaysLinear guards the ancestry lookup that used
// to rescan the whole source index for every nested record.
func TestNormalizeLargeWorkspaceStaysLinear(t *testing.T) {
	t.Parallel()

	const total, perPage = 3000, 250
	subtasks := map[string]string{}
	fixture := &bulkFixture{t: t, token: "bulk-token", tasks: total, perPage: perPage, subtaskOf: subtasks}
	// A deep chain: each nested record must walk its ancestors to find the
	// collection it belongs to.
	for index := 1; index < total; index++ {
		subtasks[fixture.taskID(index)] = fixture.taskID(index - 1)
	}
	client := fixtureClient(t, fixture.roundTrip)
	evidence := &memoryEvidence{}

	started := time.Now()
	extraction, err := client.Extract(context.Background(), fixture.token, connector.Workspace{ID: "w1", Name: "Bulk"}, evidence)
	if err != nil {
		t.Fatal(err)
	}
	// Resolving ancestry by rescanning the source index made this chain cost
	// roughly the cube of its length; an explicit bound keeps the regression a
	// failure rather than a test-suite hang.
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("normalizing a %d deep record chain took %s", total, elapsed)
	}
	if extraction.Inventory.Tasks != 1 || extraction.Inventory.Subtasks != total-1 {
		t.Fatalf("inventory = %#v", extraction.Inventory)
	}
	if len(extraction.SourceObjects) != total+3 {
		t.Fatalf("source objects = %d, want %d", len(extraction.SourceObjects), total+3)
	}
}

// TestScanFailsClosedWhenPaginationNeverTerminates covers the PRD section 22
// invariant "pagination incomplete". A source that always returns a full page
// must exhaust the bound and fail rather than loop forever or truncate.
func TestScanFailsClosedWhenPaginationNeverTerminates(t *testing.T) {
	original := maxTaskPages
	maxTaskPages = 4
	defer func() { maxTaskPages = original }()

	pages := 0
	client := fixtureClient(t, func(request *http.Request) (*http.Response, error) {
		switch {
		case request.URL.Path == "/api/v2/team/w1/space":
			if request.URL.Query().Get("archived") == "false" {
				return fixtureResponse(http.StatusOK, `{"spaces":[{"id":"s1","name":"Space"}]}`), nil
			}
			return fixtureResponse(http.StatusOK, `{"spaces":[]}`), nil
		case request.URL.Path == "/api/v2/space/s1/folder":
			return fixtureResponse(http.StatusOK, `{"folders":[]}`), nil
		case request.URL.Path == "/api/v2/space/s1/list":
			if request.URL.Query().Get("archived") == "false" {
				return fixtureResponse(http.StatusOK, `{"lists":[{"id":"l1","name":"List"}]}`), nil
			}
			return fixtureResponse(http.StatusOK, `{"lists":[]}`), nil
		case strings.HasSuffix(request.URL.Path, "/field"):
			return fixtureResponse(http.StatusOK, `{"fields":[]}`), nil
		case request.URL.Path == "/api/v2/list/l1/task":
			pages++
			// Never returns an empty page and never repeats an id, so only the
			// bound can stop the traversal.
			return fixtureResponse(http.StatusOK, fmt.Sprintf(`{"tasks":[{"id":"t%d","parent":null,"list":{"id":"l1"}}]}`, pages)), nil
		}
		return fixtureResponse(http.StatusNotFound, `{}`), nil
	})

	_, err := client.Scan(context.Background(), "token", connector.Workspace{ID: "w1", Name: "Endless"})
	if err == nil {
		t.Fatal("Scan() accepted a traversal whose pagination never terminated")
	}
	if !strings.Contains(err.Error(), "pagination did not terminate") {
		t.Fatalf("error = %v", err)
	}
	if pages > maxTaskPages {
		t.Fatalf("traversal issued %d pages, past its own bound of %d", pages, maxTaskPages)
	}
}

// TestGetExhaustsRetriesAndFailsClosed covers retry exhaustion: three failed
// attempts must produce an error and a complete failure ledger, never a
// partial success.
func TestGetExhaustsRetriesAndFailsClosed(t *testing.T) {
	t.Parallel()

	attempts := 0
	client := fixtureClient(t, func(*http.Request) (*http.Response, error) {
		attempts++
		return fixtureResponse(http.StatusServiceUnavailable, `{"err":"down","ECODE":"OOPS"}`), nil
	})
	evidence := &memoryEvidence{}
	client.evidence = evidence

	var destination struct{}
	err := client.get(context.Background(), "token", "/test", "exhaust retries", &destination)
	if err == nil {
		t.Fatal("get() returned success after every attempt failed")
	}
	var clickUpError *Error
	if !errors.As(err, &clickUpError) || clickUpError.Kind != ErrorAPI || clickUpError.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("error = %#v", err)
	}
	if attempts != maxRequestAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, maxRequestAttempts)
	}
	if len(evidence.responses) != 0 {
		t.Fatalf("a failed request produced raw success evidence: %#v", evidence.responses)
	}
	if len(evidence.failures) != maxRequestAttempts {
		t.Fatalf("failure ledger = %d entries, want %d", len(evidence.failures), maxRequestAttempts)
	}
	// Only the attempts that were actually followed by another try may claim it.
	for index, failure := range evidence.failures {
		wantRetrying := index < maxRequestAttempts-1
		if failure.Retrying != wantRetrying {
			t.Errorf("attempt %d retrying=%v, want %v", index+1, failure.Retrying, wantRetrying)
		}
	}
}

func TestRetryDelayIgnoresUnusableRateLimitHeaders(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	cases := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"absent", "", 250 * time.Millisecond},
		{"malformed", "not-a-number", 250 * time.Millisecond},
		{"in the past", "900", 250 * time.Millisecond},
		{"zero", "0", 250 * time.Millisecond},
		{"beyond the cap", "999999", maxRetryDelay},
		{"usable", "1030", 30 * time.Second},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			headers := http.Header{}
			if testCase.header != "" {
				headers.Set("X-RateLimit-Reset", testCase.header)
			}
			if delay := retryDelay(1, headers, now); delay != testCase.want {
				t.Fatalf("retryDelay = %s, want %s", delay, testCase.want)
			}
		})
	}
	// Backoff grows but never past the cap.
	if delay := retryDelay(30, nil, now); delay != maxRetryDelay {
		t.Fatalf("unbounded backoff = %s, want the %s cap", delay, maxRetryDelay)
	}
}

func TestNonRetryableStatusIsNotRetried(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusUnauthorized} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			attempts := 0
			client := fixtureClient(t, func(*http.Request) (*http.Response, error) {
				attempts++
				return fixtureResponse(status, `{"err":"no"}`), nil
			})
			evidence := &memoryEvidence{}
			client.evidence = evidence
			var destination struct{}
			if err := client.get(context.Background(), "token", "/test", "no retry", &destination); err == nil {
				t.Fatal("get() succeeded on a client error")
			}
			if attempts != 1 {
				t.Fatalf("attempts = %d, want 1: a client error must not be retried", attempts)
			}
			if len(evidence.failures) != 1 || evidence.failures[0].Retrying {
				t.Fatalf("failure ledger = %#v", evidence.failures)
			}
		})
	}
}

func TestTransientNetworkFailureIsRetriedThenFailsClosed(t *testing.T) {
	t.Parallel()

	attempts := 0
	client := fixtureClient(t, func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("connection reset by peer")
	})
	evidence := &memoryEvidence{}
	client.evidence = evidence
	var destination struct{}
	err := client.get(context.Background(), "token", "/test", "network", &destination)

	var clickUpError *Error
	if !errors.As(err, &clickUpError) || clickUpError.Kind != ErrorNetwork {
		t.Fatalf("error = %#v", err)
	}
	if attempts != maxRequestAttempts || len(evidence.failures) != maxRequestAttempts {
		t.Fatalf("attempts=%d failures=%d", attempts, len(evidence.failures))
	}
}

// TestInterruptedExtractionReturnsNoInventory covers PRD section 21's
// interrupted extraction category: a cancelled traversal must not yield a
// partial logical inventory.
func TestInterruptedExtractionReturnsNoInventory(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	fixture := &scanFixture{t: t, token: "interrupt-token"}
	client := fixtureClient(t, func(request *http.Request) (*http.Response, error) {
		// Interrupt once the traversal has reached task details, so partial
		// state genuinely exists in memory when cancellation lands.
		if strings.HasPrefix(request.URL.Path, "/api/v2/task/") {
			cancel()
			return nil, ctx.Err()
		}
		return fixture.roundTrip(request)
	})
	evidence := &memoryEvidence{}

	result, err := client.Extract(ctx, "interrupt-token", connector.Workspace{ID: "w1", Name: "Test"}, evidence)
	if err == nil {
		t.Fatal("Extract() returned a result for an interrupted traversal")
	}
	if result.Inventory != (connector.Inventory{}) || len(result.SourceObjects) != 0 {
		t.Fatalf("interrupted extraction leaked a partial inventory: %#v", result)
	}
	// A cancelled attempt must not be recorded as retryable work in progress.
	for _, failure := range evidence.failures {
		if failure.Retrying {
			t.Errorf("cancelled attempt was recorded as retrying: %#v", failure)
		}
	}
}

func TestOversizedResponseIsRejected(t *testing.T) {
	t.Parallel()

	client := fixtureClient(t, func(*http.Request) (*http.Response, error) {
		body := `{"padding":"` + strings.Repeat("x", maxResponseBody+16) + `"}`
		return fixtureResponse(http.StatusOK, body), nil
	})
	evidence := &memoryEvidence{}
	client.evidence = evidence
	var destination struct{}
	if err := client.get(context.Background(), "token", "/test", "oversized", &destination); err == nil {
		t.Fatal("an oversized response body was accepted")
	}
	if len(evidence.responses) != 0 {
		t.Fatal("an oversized response was persisted as raw evidence")
	}
}

// TestUnknownProviderFieldsDoNotBreakExtraction covers PRD section 21's
// "unknown fields" category.
func TestUnknownProviderFieldsDoNotBreakExtraction(t *testing.T) {
	t.Parallel()

	fixture := &scanFixture{t: t, token: "unknown-fields"}
	client := fixtureClient(t, func(request *http.Request) (*http.Response, error) {
		response, err := fixture.roundTrip(request)
		if err != nil || response.StatusCode != http.StatusOK {
			return response, err
		}
		body := drainFixtureBody(t, response)
		// Inject a field no Alih version knows about at the envelope level.
		injected := strings.Replace(body, "{", `{"future_provider_field":{"nested":[1,2,3]},`, 1)
		return fixtureResponse(http.StatusOK, injected), nil
	})
	evidence := &memoryEvidence{}

	result, err := client.Extract(context.Background(), fixture.token, connector.Workspace{ID: "w1", Name: "Test"}, evidence)
	if err != nil {
		t.Fatalf("an unrecognised provider field broke extraction: %v", err)
	}
	if result.Inventory.Tasks == 0 || len(result.SourceObjects) == 0 {
		t.Fatalf("inventory = %#v", result.Inventory)
	}
}

func drainFixtureBody(t *testing.T, response *http.Response) string {
	t.Helper()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	return string(content)
}
