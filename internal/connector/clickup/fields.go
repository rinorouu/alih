// Copyright 2026 rinorouu
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
	"encoding/json"
	"fmt"
	"strings"
)

// FieldSemantics interprets archived ClickUp Custom Field evidence during M5
// verification. It keeps ClickUp-specific value semantics inside the connector
// and it never guesses: any value whose correctness cannot be established from
// the archived source definition is reported as unproven rather than valid.
type FieldSemantics struct{}

// Connector identifies the source this interpreter is valid for.
func (FieldSemantics) Connector() string { return "clickup" }

type fieldOption struct {
	ID         *string          `json:"id"`
	Name       *string          `json:"name"`
	OrderIndex *json.RawMessage `json:"orderindex"`
}

type fieldDefinitionEnvelope struct {
	TypeConfig struct {
		Options []fieldOption `json:"options"`
	} `json:"type_config"`
}

// ValidateFieldValue reports whether an observed value is consistent with the
// archived field definition. Only enumerated field types expose an option set
// that makes an observed value provable; every other ClickUp field type is
// preserved as observed data with no semantic claim.
func (FieldSemantics) ValidateFieldValue(fieldType string, definitionJSON, observedJSON []byte) (string, string) {
	if string(observedJSON) == "null" {
		return "VALID", "no value was observed"
	}
	normalized := strings.ToLower(strings.TrimSpace(fieldType))
	if normalized != "drop_down" && normalized != "labels" {
		return "UNPROVEN", fmt.Sprintf("ClickUp field type %q exposes no option set that makes an observed value provable", fieldType)
	}
	var definition fieldDefinitionEnvelope
	if err := json.Unmarshal(definitionJSON, &definition); err != nil {
		return "UNPROVEN", "the archived field definition could not be read as a ClickUp field definition"
	}
	if len(definition.TypeConfig.Options) == 0 {
		return "UNPROVEN", "the archived field definition declares no options to validate against"
	}
	// A dropdown selection is archived either as the option identifier or as
	// the option order index, so both forms are accepted as evidence.
	options := map[string]struct{}{}
	for _, option := range definition.TypeConfig.Options {
		if option.ID != nil {
			options[*option.ID] = struct{}{}
		}
		if option.OrderIndex != nil {
			options[scalarToken(*option.OrderIndex)] = struct{}{}
		}
	}

	if normalized == "labels" {
		var selected []json.RawMessage
		if err := json.Unmarshal(observedJSON, &selected); err != nil {
			return "INVALID", "a labels value must be an array of option identifiers"
		}
		for _, raw := range selected {
			if reason := matchesOption(raw, options); reason != "" {
				return "INVALID", reason
			}
		}
		return "VALID", ""
	}
	if reason := matchesOption(observedJSON, options); reason != "" {
		return "INVALID", reason
	}
	return "VALID", ""
}

func matchesOption(raw json.RawMessage, options map[string]struct{}) string {
	token := scalarToken(raw)
	if token == "" {
		return fmt.Sprintf("observed selection %s is not a scalar option reference", strings.TrimSpace(string(raw)))
	}
	if _, known := options[token]; known {
		return ""
	}
	return fmt.Sprintf("option %q is not defined by the archived field definition", token)
}

// scalarToken renders a JSON scalar as the plain text used to compare an
// observed selection with the option set in the archived definition. It
// returns an empty string for values that are not scalars.
func scalarToken(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	if strings.HasPrefix(trimmed, "\"") {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return ""
		}
		return value
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return ""
	}
	return trimmed
}
