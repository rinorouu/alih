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

import "testing"

const dropDownDefinition = `{"id":"f1","name":"Workstream","type":"drop_down","type_config":{"options":[{"id":"o1","name":"Engineering","orderindex":0},{"id":"o2","name":"Sales","orderindex":1}]}}`

func TestValidateFieldValueProvesEnumeratedSelectionsOnly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		fieldType   string
		definition  string
		observed    string
		wantVerdict string
	}{
		{"option identifier", "drop_down", dropDownDefinition, `"o2"`, "VALID"},
		{"option order index", "drop_down", dropDownDefinition, `1`, "VALID"},
		{"unknown option", "drop_down", dropDownDefinition, `"o9"`, "INVALID"},
		{"order index outside the option set", "drop_down", dropDownDefinition, `7`, "INVALID"},
		{"no value observed", "drop_down", dropDownDefinition, `null`, "VALID"},
		{"labels selection", "labels", dropDownDefinition, `["o1","o2"]`, "VALID"},
		{"labels selection with an unknown option", "labels", dropDownDefinition, `["o1","o9"]`, "INVALID"},
		{"labels selection that is not an array", "labels", dropDownDefinition, `"o1"`, "INVALID"},
		{"definition without options", "drop_down", `{"id":"f1","type":"drop_down"}`, `"o1"`, "UNPROVEN"},
		{"computed field", "formula", `{"id":"f1","type":"formula"}`, `"42"`, "UNPROVEN"},
		{"plain text field", "short_text", `{"id":"f1","type":"short_text"}`, `"anything"`, "UNPROVEN"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			verdict, reason := FieldSemantics{}.ValidateFieldValue(testCase.fieldType, []byte(testCase.definition), []byte(testCase.observed))
			if verdict != testCase.wantVerdict {
				t.Fatalf("verdict = %s (%s), want %s", verdict, reason, testCase.wantVerdict)
			}
			if verdict != "VALID" && reason == "" {
				t.Fatal("a non-valid verdict must explain itself")
			}
		})
	}
}

func TestFieldSemanticsIdentifiesItsConnector(t *testing.T) {
	t.Parallel()

	if name := (FieldSemantics{}).Connector(); name != "clickup" {
		t.Fatalf("Connector() = %q", name)
	}
}
