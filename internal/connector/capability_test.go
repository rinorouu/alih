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

package connector

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func validContractFixture() CapabilityContract {
	return CapabilityContract{
		SchemaVersion: CapabilitySchemaVersion,
		Connector:     "fixture",
		Capabilities: []Capability{
			{
				ID: CapabilityItems, Name: "Items", Requirement: CapabilityRequired,
				Implementation: CapabilitySupported, Availability: CapabilityAvailabilityUnknown,
				State: CapabilitySupported, Note: "records and nested records",
			},
			{
				ID: CapabilityComments, Name: "Comments", Requirement: CapabilityOptional,
				Implementation: CapabilityPartial, Availability: CapabilityAvailabilityUnknown,
				State: CapabilityPartial, Note: "record comments only",
			},
		},
	}
}

func TestCapabilityContractValidationAndDeterministicJSON(t *testing.T) {
	t.Parallel()

	contract := validContractFixture()
	contract.Capabilities[0], contract.Capabilities[1] = contract.Capabilities[1], contract.Capabilities[0]
	contract.Capabilities = CanonicalCapabilities(contract.SchemaVersion, contract.Capabilities)
	if err := ValidateCapabilityContract(contract); err != nil {
		t.Fatal(err)
	}
	first, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	contract.Capabilities = CanonicalCapabilities(contract.SchemaVersion, contract.Capabilities)
	second, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical contract JSON changed: %s != %s", first, second)
	}
	if contract.Capabilities[0].ID != CapabilityComments || contract.Capabilities[1].ID != CapabilityItems {
		t.Fatalf("capability order = %#v", contract.Capabilities)
	}
	firstDigest, err := CapabilityDigest(contract.SchemaVersion, contract.Connector, contract.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	contract.Capabilities[0].Note = "changed semantics"
	secondDigest, err := CapabilityDigest(contract.SchemaVersion, contract.Connector, contract.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest == secondDigest || !strings.HasPrefix(firstDigest, "sha256:") {
		t.Fatalf("capability digests = %q and %q", firstDigest, secondDigest)
	}
}

func TestCapabilityContractRejectsAmbiguousRecords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func(*CapabilityContract)
		want string
	}{
		{"schema", func(value *CapabilityContract) { value.SchemaVersion = 99 }, "unsupported capability schema"},
		{"connector", func(value *CapabilityContract) { value.Connector = "" }, "connector is empty"},
		{"empty", func(value *CapabilityContract) { value.Capabilities = nil }, "capability set is empty"},
		{"id", func(value *CapabilityContract) { value.Capabilities[0].ID = "Bad.ID" }, "invalid id"},
		{"duplicate", func(value *CapabilityContract) { value.Capabilities[1].ID = value.Capabilities[0].ID }, "duplicate capability"},
		{"requirement", func(value *CapabilityContract) { value.Capabilities[0].Requirement = "MAYBE" }, "unknown requirement"},
		{"implementation", func(value *CapabilityContract) { value.Capabilities[0].Implementation = CapabilityFailed }, "invalid implementation"},
		{"projection", func(value *CapabilityContract) { value.Capabilities[0].State = CapabilityPartial }, "conflicts with implementation"},
		{"availability", func(value *CapabilityContract) { value.Capabilities[0].Availability = "MAYBE" }, "unknown availability"},
		{"control", func(value *CapabilityContract) { value.Capabilities[0].Note = "unsafe\ntext" }, "control character"},
		{"long note", func(value *CapabilityContract) { value.Capabilities[0].Note = strings.Repeat("x", 1025) }, "exceeds 1024"},
	}
	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			contract := validContractFixture()
			testCase.edit(&contract)
			err := ValidateCapabilityContract(contract)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestObserveCapabilitiesSeparatesImplementationAndAvailability(t *testing.T) {
	t.Parallel()

	contract := validContractFixture()
	observed, err := ObserveCapabilities(contract, map[CapabilityID]CapabilityAvailability{
		CapabilityItems: CapabilityAvailabilityAvailable,
	})
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[CapabilityID]Capability, len(observed))
	for _, capability := range observed {
		byID[capability.ID] = capability
	}
	if byID[CapabilityItems].Implementation != CapabilitySupported || byID[CapabilityItems].State != CapabilitySupported || byID[CapabilityItems].Availability != CapabilityAvailabilityAvailable {
		t.Fatalf("observed item capability = %#v", byID[CapabilityItems])
	}
	if byID[CapabilityComments].Availability != CapabilityAvailabilityUnknown {
		t.Fatalf("unobserved capability = %#v", byID[CapabilityComments])
	}
	if !reflect.DeepEqual(contract, validContractFixture()) {
		t.Fatal("ObserveCapabilities mutated the declaration")
	}
	if _, err := ObserveCapabilities(contract, map[CapabilityID]CapabilityAvailability{"undeclared": CapabilityAvailabilityAvailable}); err == nil {
		t.Fatal("ObserveCapabilities accepted an undeclared capability")
	}
}

func TestLegacyCapabilityEvidenceRemainsReadableWithoutInference(t *testing.T) {
	t.Parallel()

	legacy := []Capability{{Name: "Old scope", State: CapabilityUnknown, Note: "not established"}}
	if err := ValidateCapabilities(0, legacy); err != nil {
		t.Fatal(err)
	}
	if got := CanonicalCapabilities(0, legacy); !reflect.DeepEqual(got, legacy) {
		t.Fatalf("legacy capability changed: %#v", got)
	}
	mixed := append([]Capability(nil), legacy...)
	mixed[0].ID = CapabilityItems
	if err := ValidateCapabilities(0, mixed); err == nil {
		t.Fatal("legacy evidence accepted versioned fields without a schema version")
	}
	if err := ValidateCapabilities(0, nil); err != nil {
		t.Fatalf("pre-contract empty capability evidence is no longer readable: %v", err)
	}
}
