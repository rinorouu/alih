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

package verify

import (
	"encoding/json"
	"fmt"

	"alih/internal/archive"
)

// checkCustomFields validates the portable custom-field evidence.
//
// Three separate claims are handled differently on purpose:
//   - every observed value must reference an archived field definition, and
//     every definition must agree with the source definition JSON archived
//     beside it. Those are provable and any violation fails;
//   - an observed value may be validated against its source definition only as
//     far as the connector can prove the semantics, for example enumerated
//     option sets;
//   - computed fields such as formulas are archived as observed values only.
//     Alih never claims executable source semantics for them, so that part of
//     the check is reported UNPROVEN rather than passed.
func (v *verification) checkCustomFields(database *portableDatabase, manifest archive.Manifest) {
	var findings []string
	var unproven []string

	definitions := make(map[string]fieldRow, len(database.Fields))
	for _, row := range database.Fields {
		definitions[row.Entity.ID] = row
		var definition map[string]json.RawMessage
		if err := json.Unmarshal([]byte(row.DefinitionJSON), &definition); err != nil {
			findings = append(findings, fmt.Sprintf("field definition %q does not contain valid source definition JSON: %v", row.Entity.Source.ID, err))
			continue
		}
		if identifier, present := definition["id"]; present {
			var value string
			if err := json.Unmarshal(identifier, &value); err != nil || value != row.Entity.Source.ID {
				findings = append(findings, fmt.Sprintf("field definition %q does not match the source identifier inside its own archived definition", row.Entity.Source.ID))
			}
		} else {
			findings = append(findings, fmt.Sprintf("archived definition for field %q contains no source identifier", row.Entity.Source.ID))
		}
		if difference := compareDefinitionString(definition, "type", row.FieldType); difference != "" {
			findings = append(findings, fmt.Sprintf("field %q %s", row.Entity.Source.ID, difference))
		}
		if difference := compareDefinitionString(definition, "name", row.Name); difference != "" {
			findings = append(findings, fmt.Sprintf("field %q %s", row.Entity.Source.ID, difference))
		}
		switch row.SemanticsState {
		case "SOURCE_DEFINITION_ONLY":
		case "OBSERVED_ONLY_NO_EXECUTION":
			unproven = append(unproven, fmt.Sprintf("field %q is a computed source field: its archived definition and values are evidence only and Alih does not claim executable source semantics for it", displayFieldName(row)))
		default:
			findings = append(findings, fmt.Sprintf("field %q declares unknown semantics state %q", row.Entity.Source.ID, row.SemanticsState))
		}
	}

	semantics := v.options.FieldSemantics
	usable := semantics != nil && semantics.Connector() == manifest.Connector
	validated, unprovable := 0, 0
	for _, value := range database.FieldValues {
		definition, archived := definitions[value.FieldID]
		if !archived {
			findings = append(findings, fmt.Sprintf("an observed value on record %q references field definition %q that is not archived", value.RecordID, value.FieldID))
			continue
		}
		if value.SemanticsState != "OBSERVED_ONLY" {
			findings = append(findings, fmt.Sprintf("observed value for field %q declares semantics state %q; schema.json states observed values are always OBSERVED_ONLY", definition.Entity.Source.ID, value.SemanticsState))
			continue
		}
		if !json.Valid([]byte(value.ObservedValueJSON)) {
			findings = append(findings, fmt.Sprintf("observed value for field %q is not valid JSON", definition.Entity.Source.ID))
			continue
		}
		if !usable {
			unprovable++
			continue
		}
		fieldType := ""
		if definition.FieldType != nil {
			fieldType = *definition.FieldType
		}
		verdict, reason := semantics.ValidateFieldValue(fieldType, []byte(definition.DefinitionJSON), []byte(value.ObservedValueJSON))
		switch verdict {
		case FieldValueValid:
			validated++
		case FieldValueInvalid:
			findings = append(findings, fmt.Sprintf("observed value for field %q contradicts its archived source definition: %s", displayFieldName(definition), reason))
		default:
			unprovable++
		}
	}
	if unprovable > 0 {
		if usable {
			unproven = append(unproven, fmt.Sprintf("%d observed custom-field values have no provable value semantics in the archived source definition and are preserved as observed data only", unprovable))
		} else {
			unproven = append(unproven, fmt.Sprintf("no field-value semantics are available for connector %q, so %d observed values were checked for reference and structure only", manifest.Connector, unprovable))
		}
	}

	if len(findings) > 0 {
		v.fail("custom_field_evidence", "archived custom-field values or definitions contradict the archived source definitions", findings)
		return
	}
	if len(unproven) > 0 {
		v.unproven("custom_field_evidence",
			fmt.Sprintf("%d custom-field definitions are internally consistent and %d observed values were validated against their source definitions, but some custom-field semantics cannot be proven from the archive.", len(database.Fields), validated),
			unproven)
		return
	}
	v.pass("custom_field_evidence", fmt.Sprintf("all %d custom-field definitions are internally consistent and all %d observed values are valid against their archived source definitions", len(database.Fields), validated))
}

func compareDefinitionString(definition map[string]json.RawMessage, key string, archived *string) string {
	raw, present := definition[key]
	if !present || string(raw) == "null" {
		if archived != nil {
			return fmt.Sprintf("records %s %q although its archived source definition provides none", key, *archived)
		}
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		if archived != nil {
			return fmt.Sprintf("records a %s that its archived source definition does not express as text", key)
		}
		return ""
	}
	if archived == nil {
		return fmt.Sprintf("records no %s although its archived source definition provides %q", key, value)
	}
	if *archived != value {
		return fmt.Sprintf("records %s %q but its archived source definition states %q", key, *archived, value)
	}
	return ""
}

func displayFieldName(row fieldRow) string {
	if row.Name != nil && *row.Name != "" {
		return *row.Name
	}
	return row.Entity.Source.ID
}
