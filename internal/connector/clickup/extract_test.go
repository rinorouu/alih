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
	"reflect"
	"strings"
	"testing"
	"time"

	"alih/internal/connector"
)

type memoryEvidence struct {
	responses []connector.RawResponse
	failures  []connector.RequestFailure
}

func (evidence *memoryEvidence) RecordResponse(response connector.RawResponse) error {
	evidence.responses = append(evidence.responses, response)
	return nil
}

func (evidence *memoryEvidence) RecordFailure(failure connector.RequestFailure) error {
	evidence.failures = append(evidence.failures, failure)
	return nil
}

func TestExtractRepeatedUnchangedSourceProducesEquivalentLogicalInventory(t *testing.T) {
	t.Parallel()

	workspace := connector.Workspace{ID: "w1", Name: "Test Workspace"}
	results := make([]connector.ExtractionResult, 0, 2)
	requestKeys := make([][]string, 0, 2)
	for run := 0; run < 2; run++ {
		fixture := &scanFixture{t: t, token: "extract-secret"}
		client := fixtureClient(t, fixture.roundTrip)
		client.now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
		evidence := &memoryEvidence{}
		result, err := client.Extract(context.Background(), fixture.token, workspace, evidence)
		if err != nil {
			t.Fatalf("Extract() run %d error = %v", run+1, err)
		}
		if len(evidence.responses) == 0 || len(evidence.failures) != 0 {
			t.Fatalf("run %d evidence responses=%d failures=%d", run+1, len(evidence.responses), len(evidence.failures))
		}
		keys := make([]string, 0, len(evidence.responses))
		for _, response := range evidence.responses {
			keys = append(keys, response.Operation+" "+response.Path+"?"+response.Query.Encode())
			if strings.Contains(string(response.Body), fixture.token) {
				t.Fatal("fixture credential appeared in raw provider evidence")
			}
		}
		results = append(results, result)
		requestKeys = append(requestKeys, keys)
	}
	if !reflect.DeepEqual(results[0], results[1]) {
		t.Fatalf("unchanged source inventories differ:\nfirst: %#v\nsecond: %#v", results[0], results[1])
	}
	if !reflect.DeepEqual(requestKeys[0], requestKeys[1]) {
		t.Fatalf("unchanged source traversals differ:\nfirst: %#v\nsecond: %#v", requestKeys[0], requestKeys[1])
	}

	wantObjects := map[string]bool{
		"workspace:w1": true, "space:s1": true, "folder:f1": true,
		"list:l1": true, "list:l2": true, "task:t1": true,
		"subtask:t2": true, "task:t3": true, "comment:c1": true,
		"comment:r1": true, "attachment:a1": true, "custom_field:cf-list": true,
		"relationship.task_dependency:t1->t2": true,
		"relationship.task_link:t1<->t3":      true,
	}
	for _, object := range results[0].SourceObjects {
		delete(wantObjects, object.Type+":"+object.ID)
	}
	if len(wantObjects) != 0 {
		t.Fatalf("source ID index omitted expected objects: %#v", wantObjects)
	}
	for _, capability := range results[0].Capabilities {
		if capability.ID == connector.CapabilityRawEvidence && capability.Availability != connector.CapabilityAvailabilityAvailable {
			t.Fatalf("completed extraction raw evidence availability = %s", capability.Availability)
		}
		if capability.ID == connector.CapabilityAttachmentContent && capability.Availability != connector.CapabilityAvailabilityUnknown {
			t.Fatalf("extraction prematurely observed attachment content as %s", capability.Availability)
		}
	}
}

func TestExtractFailureReturnsNoPartialInventoryAndAccountsForAttempts(t *testing.T) {
	t.Parallel()

	fixture := &scanFixture{t: t, token: "extract-secret", mode: "page_failure"}
	client := fixtureClient(t, fixture.roundTrip)
	evidence := &memoryEvidence{}
	result, err := client.Extract(context.Background(), fixture.token, connector.Workspace{ID: "w1", Name: "Test"}, evidence)
	if err == nil {
		t.Fatal("Extract() error = nil")
	}
	if !reflect.DeepEqual(result, connector.ExtractionResult{}) {
		t.Fatalf("failed extraction returned partial logical inventory: %#v", result)
	}
	if len(evidence.responses) == 0 {
		t.Fatal("successful responses before failure were not retained as evidence")
	}
	if len(evidence.failures) != maxRequestAttempts {
		t.Fatalf("failed attempts = %d, want %d", len(evidence.failures), maxRequestAttempts)
	}
	for index, failure := range evidence.failures {
		wantRetrying := index < maxRequestAttempts-1
		if failure.Retrying != wantRetrying || failure.StatusCode != 503 {
			t.Fatalf("failure %d = %#v", index, failure)
		}
		if strings.Contains(failure.Error, fixture.token) {
			t.Fatal("failure accounting exposed the credential")
		}
	}
}
