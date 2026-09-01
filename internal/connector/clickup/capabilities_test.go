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
	"strings"
	"testing"

	"alih/internal/connector"
)

func TestCapabilityContractContainsOnlyCurrentClickUpImplementation(t *testing.T) {
	t.Parallel()

	client := NewClient(nil)
	contract := client.CapabilityContract()
	if err := connector.ValidateCapabilityContract(contract); err != nil {
		t.Fatal(err)
	}
	want := map[connector.CapabilityID]bool{
		connector.CapabilityWorkspaceData: true, connector.CapabilityItems: true,
		connector.CapabilityComments: true, connector.CapabilityAttachmentMetadata: true,
		connector.CapabilityAttachmentContent: true, connector.CapabilityCustomFields: true,
		connector.CapabilityRelationships: true, connector.CapabilityRawEvidence: true,
	}
	for _, capability := range contract.Capabilities {
		if !want[capability.ID] {
			t.Errorf("unexpected capability %q", capability.ID)
		}
		delete(want, capability.ID)
		if capability.Requirement != connector.CapabilityRequired || capability.Availability != connector.CapabilityAvailabilityUnknown {
			t.Errorf("declared capability = %#v", capability)
		}
		if strings.Contains(strings.ToLower(capability.Name+" "+capability.Note), "docs") ||
			strings.Contains(strings.ToLower(capability.Name+" "+capability.Note), "whiteboard") ||
			strings.Contains(strings.ToLower(capability.Name+" "+capability.Note), "automation") {
			t.Errorf("speculative capability remains: %#v", capability)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing current capabilities: %#v", want)
	}
}
