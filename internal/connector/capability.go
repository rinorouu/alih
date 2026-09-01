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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// CapabilitySchemaVersion identifies the provider-neutral capability contract
// carried by new snapshots, archives, verification results and reports.
const CapabilitySchemaVersion = 1

// CapabilityID is stable machine identity. Display labels may change without
// changing these values.
type CapabilityID string

const (
	CapabilityWorkspaceData      CapabilityID = "workspace_data"
	CapabilityItems              CapabilityID = "items"
	CapabilityComments           CapabilityID = "comments"
	CapabilityAttachmentMetadata CapabilityID = "attachment_metadata"
	CapabilityAttachmentContent  CapabilityID = "attachment_content"
	CapabilityCustomFields       CapabilityID = "custom_fields"
	CapabilityRelationships      CapabilityID = "relationships"
	CapabilityRawEvidence        CapabilityID = "raw_evidence"
)

// CapabilityRequirement controls whether a run may make a clean result claim
// when the capability cannot be obtained within its declared implementation
// scope.
type CapabilityRequirement string

const (
	CapabilityRequired CapabilityRequirement = "REQUIRED"
	CapabilityOptional CapabilityRequirement = "OPTIONAL"
)

// CapabilityAvailability describes one operation's observation independently
// from the connector's implementation support.
type CapabilityAvailability string

const (
	CapabilityAvailabilityAvailable   CapabilityAvailability = "AVAILABLE"
	CapabilityAvailabilityUnavailable CapabilityAvailability = "UNAVAILABLE"
	CapabilityAvailabilityUnknown     CapabilityAvailability = "UNKNOWN"
	CapabilityAvailabilityFailed      CapabilityAvailability = "FAILED"
)

// CapabilityContract is a deterministic connector declaration. Availability
// is UNKNOWN until a real operation establishes more.
type CapabilityContract struct {
	SchemaVersion int          `json:"schema_version"`
	Connector     string       `json:"connector"`
	Capabilities  []Capability `json:"capabilities"`
}

// ValidateCapabilityContract rejects ambiguous or internally inconsistent
// declarations before Core relies on them.
func ValidateCapabilityContract(contract CapabilityContract) error {
	if contract.SchemaVersion != CapabilitySchemaVersion {
		return fmt.Errorf("unsupported capability schema version %d", contract.SchemaVersion)
	}
	if strings.TrimSpace(contract.Connector) == "" {
		return errors.New("capability contract connector is empty")
	}
	if err := validateCapabilityText("connector", contract.Connector, 64, false); err != nil {
		return err
	}
	return ValidateCapabilities(contract.SchemaVersion, contract.Capabilities)
}

// ValidateCapabilities validates either current contract v1 records or legacy
// records from artifacts created before a capability schema version existed.
// Legacy validation preserves old evidence without inventing stable IDs or new
// semantics for it.
func ValidateCapabilities(schemaVersion int, capabilities []Capability) error {
	if len(capabilities) == 0 {
		if schemaVersion == 0 {
			// Some pre-contract M3 fixtures/artifacts had no capability records.
			// Their absence remains legacy unknown scope; version 1 forbids it.
			return nil
		}
		return errors.New("capability set is empty")
	}
	if schemaVersion != 0 && schemaVersion != CapabilitySchemaVersion {
		return fmt.Errorf("unsupported capability schema version %d", schemaVersion)
	}

	seen := make(map[string]struct{}, len(capabilities))
	for index, capability := range capabilities {
		if strings.TrimSpace(capability.Name) == "" {
			return fmt.Errorf("capability %d has an empty name", index)
		}
		if err := validateCapabilityText("capability name", capability.Name, 128, false); err != nil {
			return fmt.Errorf("capability %d: %w", index, err)
		}
		if err := validateCapabilityText("capability note", capability.Note, 1024, true); err != nil {
			return fmt.Errorf("capability %q: %w", capability.Name, err)
		}
		if !validCapabilityState(capability.State) {
			return fmt.Errorf("capability %q has unknown state %q", capability.Name, capability.State)
		}

		key := capability.Name
		if schemaVersion == CapabilitySchemaVersion {
			if !validCapabilityID(capability.ID) {
				return fmt.Errorf("capability %q has invalid id %q", capability.Name, capability.ID)
			}
			key = string(capability.ID)
			if capability.Requirement != CapabilityRequired && capability.Requirement != CapabilityOptional {
				return fmt.Errorf("capability %q has unknown requirement %q", capability.ID, capability.Requirement)
			}
			if !validImplementationState(capability.Implementation) {
				return fmt.Errorf("capability %q has invalid implementation state %q", capability.ID, capability.Implementation)
			}
			if capability.State != capability.Implementation {
				return fmt.Errorf("capability %q legacy state %q conflicts with implementation %q", capability.ID, capability.State, capability.Implementation)
			}
			if !validAvailability(capability.Availability) {
				return fmt.Errorf("capability %q has unknown availability %q", capability.ID, capability.Availability)
			}
		} else if capability.ID != "" || capability.Requirement != "" || capability.Implementation != "" || capability.Availability != "" {
			return fmt.Errorf("legacy capability %q mixes versioned capability fields without a schema version", capability.Name)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate capability identity %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// CanonicalCapabilities returns an independent, stable ordering by machine ID
// for contract v1, or by display name for legacy evidence.
func CanonicalCapabilities(schemaVersion int, capabilities []Capability) []Capability {
	result := append([]Capability(nil), capabilities...)
	sort.Slice(result, func(i, j int) bool {
		if schemaVersion == CapabilitySchemaVersion && result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// ObserveCapabilities copies a validated declaration and applies explicit run
// observations. Every observation must refer to a declared stable ID.
func ObserveCapabilities(contract CapabilityContract, observations map[CapabilityID]CapabilityAvailability) ([]Capability, error) {
	if err := ValidateCapabilityContract(contract); err != nil {
		return nil, err
	}
	result := CanonicalCapabilities(contract.SchemaVersion, contract.Capabilities)
	found := make(map[CapabilityID]struct{}, len(result))
	for index := range result {
		found[result[index].ID] = struct{}{}
		if availability, observed := observations[result[index].ID]; observed {
			if !validAvailability(availability) {
				return nil, fmt.Errorf("capability %q has unknown observed availability %q", result[index].ID, availability)
			}
			result[index].Availability = availability
		}
	}
	for id := range observations {
		if _, declared := found[id]; !declared {
			return nil, fmt.Errorf("observation refers to undeclared capability %q", id)
		}
	}
	return result, nil
}

// CapabilityDigest binds the connector identity, schema version and canonical
// capability records. Legacy capability evidence has no invented digest.
func CapabilityDigest(schemaVersion int, connectorName string, capabilities []Capability) (string, error) {
	if schemaVersion == 0 {
		return "", nil
	}
	contract := CapabilityContract{
		SchemaVersion: schemaVersion,
		Connector:     connectorName,
		Capabilities:  CanonicalCapabilities(schemaVersion, capabilities),
	}
	if err := ValidateCapabilityContract(contract); err != nil {
		return "", err
	}
	payload, err := json.Marshal(contract)
	if err != nil {
		return "", fmt.Errorf("encode capability contract: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validCapabilityID(id CapabilityID) bool {
	if id == "" {
		return false
	}
	for _, character := range id {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validCapabilityState(state CapabilityState) bool {
	switch state {
	case CapabilitySupported, CapabilityPartial, CapabilityUnsupported, CapabilityUnavailable, CapabilityUnknown, CapabilityFailed:
		return true
	default:
		return false
	}
}

func validImplementationState(state CapabilityState) bool {
	switch state {
	case CapabilitySupported, CapabilityPartial, CapabilityUnsupported, CapabilityUnknown:
		return true
	default:
		return false
	}
}

func validAvailability(availability CapabilityAvailability) bool {
	switch availability {
	case CapabilityAvailabilityAvailable, CapabilityAvailabilityUnavailable, CapabilityAvailabilityUnknown, CapabilityAvailabilityFailed:
		return true
	default:
		return false
	}
}

func validateCapabilityText(label, value string, limit int, allowEmpty bool) error {
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is empty", label)
	}
	if len(value) > limit {
		return fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}
