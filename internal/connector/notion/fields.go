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
	"encoding/json"
	"fmt"

	"alih/internal/verify"
)

// FieldSemantics interprets archived Notion property evidence during
// verification. It keeps Notion's value semantics inside this adapter; Core
// only learns whether a value was proven.
type FieldSemantics struct{}

// Connector is the connector whose archives this interpreter understands.
func (FieldSemantics) Connector() string { return "notion" }

// ValidateFieldValue reports whether an observed property value is consistent
// with its archived definition.
//
// Only enumerated properties expose an option set that makes an observed value
// provable. Everything else is preserved as evidence with no semantic claim —
// including formula and rollup, which Notion computes server-side and Alih
// never re-executes. Returning UNPROVEN is the honest answer, and is why the
// contract exists: a guess would be worse than an admission.
func (FieldSemantics) ValidateFieldValue(fieldType string, definitionJSON, observedJSON []byte) (string, string) {
	if string(observedJSON) == "null" {
		return verify.FieldValueValid, "no value was observed"
	}
	switch fieldType {
	case "select", "multi_select", "status":
	default:
		return verify.FieldValueUnproven, fmt.Sprintf(
			"Notion property type %q exposes no option set that makes an observed value provable", fieldType)
	}

	options, err := declaredOptions(fieldType, definitionJSON)
	if err != nil {
		return verify.FieldValueUnproven, "the archived property definition could not be read as a Notion property"
	}
	if len(options) == 0 {
		return verify.FieldValueUnproven, "the archived property definition declares no options to validate against"
	}

	observed, err := observedOptionNames(fieldType, observedJSON)
	if err != nil {
		return verify.FieldValueUnproven, "the observed value could not be read as a Notion property value"
	}
	for _, name := range observed {
		if !options[name] {
			return verify.FieldValueInvalid, fmt.Sprintf(
				"observed option %q is not declared by the archived property definition", name)
		}
	}
	return verify.FieldValueValid, "every observed option is declared by the archived property definition"
}

func declaredOptions(fieldType string, definitionJSON []byte) (map[string]bool, error) {
	var definition map[string]json.RawMessage
	if err := json.Unmarshal(definitionJSON, &definition); err != nil {
		return nil, err
	}
	payload, present := definition[fieldType]
	if !present {
		return nil, fmt.Errorf("definition carries no %q configuration", fieldType)
	}
	var configuration struct {
		Options []struct {
			Name string `json:"name"`
		} `json:"options"`
		Groups []struct {
			Name string `json:"name"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(payload, &configuration); err != nil {
		return nil, err
	}
	options := make(map[string]bool, len(configuration.Options))
	for _, option := range configuration.Options {
		options[option.Name] = true
	}
	return options, nil
}

func observedOptionNames(fieldType string, observedJSON []byte) ([]string, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(observedJSON, &envelope); err != nil {
		return nil, err
	}
	payload, present := envelope[fieldType]
	if !present {
		return nil, fmt.Errorf("observed value carries no %q payload", fieldType)
	}
	type option struct {
		Name string `json:"name"`
	}
	if fieldType == "multi_select" {
		var many []option
		if err := json.Unmarshal(payload, &many); err != nil {
			return nil, err
		}
		names := make([]string, 0, len(many))
		for _, item := range many {
			names = append(names, item.Name)
		}
		return names, nil
	}
	if string(payload) == "null" {
		return nil, nil
	}
	var single option
	if err := json.Unmarshal(payload, &single); err != nil {
		return nil, err
	}
	return []string{single.Name}, nil
}
