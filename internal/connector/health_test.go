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
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAggregateCapabilityHealth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []CapabilityHealth
		want HealthState
	}{
		{"empty is unknown", nil, HealthUnknown},
		{"all healthy", []CapabilityHealth{{ID: CapabilityItems, Requirement: CapabilityRequired, State: HealthHealthy, Reason: HealthReasonNone}}, HealthHealthy},
		{"unknown is not healthy", []CapabilityHealth{{ID: CapabilityItems, Requirement: CapabilityRequired, State: HealthUnknown, Reason: HealthReasonUnknownFailure}}, HealthUnknown},
		{"required unavailable dominates", []CapabilityHealth{
			{ID: CapabilityItems, Requirement: CapabilityRequired, State: HealthUnavailable, Reason: HealthReasonCapabilityRemoved},
			{ID: CapabilityComments, Requirement: CapabilityOptional, State: HealthDegraded, Reason: HealthReasonRateLimited},
		}, HealthUnavailable},
		{"optional unavailable degrades", []CapabilityHealth{{ID: CapabilityComments, Requirement: CapabilityOptional, State: HealthUnavailable, Reason: HealthReasonCapabilityRemoved}}, HealthDegraded},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state, _, _ := AggregateCapabilityHealth(test.in)
			if state != test.want {
				t.Fatalf("AggregateCapabilityHealth() state = %s, want %s", state, test.want)
			}
		})
	}
}

func TestAggregateCapabilityHealthIsOrderIndependent(t *testing.T) {
	t.Parallel()
	a := []CapabilityHealth{
		{ID: CapabilityItems, Requirement: CapabilityRequired, State: HealthDegraded, Reason: HealthReasonRateLimited},
		{ID: CapabilityComments, Requirement: CapabilityRequired, State: HealthDegraded, Reason: HealthReasonAPIBehaviorChanged, Retryable: true},
	}
	b := []CapabilityHealth{a[1], a[0]}
	sa, ra, ta := AggregateCapabilityHealth(a)
	sb, rb, tb := AggregateCapabilityHealth(b)
	if sa != sb || ra != rb || ta != tb {
		t.Fatalf("aggregation depends on order: (%s,%s,%t) != (%s,%s,%t)", sa, ra, ta, sb, rb, tb)
	}
}

func TestOperationalAssessmentRenderersAreStableAndSafe(t *testing.T) {
	t.Parallel()
	observed := time.Date(2026, 8, 31, 12, 30, 0, 0, time.UTC)
	assessment, err := HealthyAssessment("clickup", HealthBasisScan, observed, AuthenticationAuthenticated, []Capability{{
		ID: CapabilityItems, Name: "Tasks", Requirement: CapabilityRequired,
		Implementation: CapabilitySupported, Availability: CapabilityAvailabilityAvailable, State: CapabilitySupported,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var first, second bytes.Buffer
	if err := WriteOperationalAssessmentJSON(&first, assessment); err != nil {
		t.Fatal(err)
	}
	assessment.Health.Capabilities = append([]CapabilityHealth(nil), assessment.Health.Capabilities...)
	if err := WriteOperationalAssessmentJSON(&second, assessment); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatalf("JSON renderer is not stable:\n%s\n%s", first.String(), second.String())
	}
	var decoded OperationalAssessment
	if err := json.Unmarshal(first.Bytes(), &decoded); err != nil {
		t.Fatalf("rendered JSON invalid: %v", err)
	}
	var human bytes.Buffer
	if err := WriteOperationalAssessmentText(&human, assessment); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Connector health: HEALTHY", "Authentication: AUTHENTICATED", "Evidence basis: SCAN", observed.Format(time.RFC3339)} {
		if !strings.Contains(human.String(), want) {
			t.Fatalf("human renderer missing %q:\n%s", want, human.String())
		}
	}
}

func TestValidateOperationalAssessmentRejectsUnsafeText(t *testing.T) {
	t.Parallel()
	assessment, err := HealthyAssessment("clickup", HealthBasisScan, time.Now(), AuthenticationAuthenticated, nil)
	if err != nil {
		t.Fatal(err)
	}
	assessment.Health.Message = "unsafe\nterminal"
	if err := ValidateOperationalAssessment(assessment); err == nil {
		t.Fatal("ValidateOperationalAssessment() accepted control character")
	}
}

// FuzzAggregateCapabilityHealthNeverInventsHealth is the property behind the
// aggregation table: a false HEALTHY is the single most dangerous answer this
// function can give, so no combination of inputs may produce one that the
// individual observations do not support.
func FuzzAggregateCapabilityHealthNeverInventsHealth(f *testing.F) {
	f.Add(uint(0), uint(0), 0)
	f.Add(uint(1), uint(2), 3)
	f.Add(uint(0xdeadbeef), uint(0x5eed), 7)

	states := []HealthState{HealthHealthy, HealthDegraded, HealthUnavailable, HealthUnknown}
	requirements := []CapabilityRequirement{CapabilityRequired, CapabilityOptional}
	ids := []CapabilityID{CapabilityItems, CapabilityComments, CapabilityAttachmentContent, CapabilityRawEvidence}

	f.Fuzz(func(t *testing.T, stateBits, requirementBits uint, count int) {
		if count < 0 {
			count = -count
		}
		count %= len(ids) + 1

		observations := make([]CapabilityHealth, 0, count)
		for index := 0; index < count; index++ {
			state := states[(stateBits>>(uint(index)*2))%uint(len(states))]
			requirement := requirements[(requirementBits>>uint(index))%uint(len(requirements))]
			reason := HealthReasonNone
			if state != HealthHealthy {
				reason = HealthReasonUnknownFailure
			}
			observations = append(observations, CapabilityHealth{
				ID: ids[index], Requirement: requirement, State: state, Reason: reason,
			})
		}

		aggregate, _, _ := AggregateCapabilityHealth(observations)

		if len(observations) == 0 {
			if aggregate != HealthUnknown {
				t.Fatalf("no evidence aggregated to %s, want %s", aggregate, HealthUnknown)
			}
			return
		}
		for _, observation := range observations {
			// Nothing less than healthy may aggregate to healthy.
			if observation.State != HealthHealthy && aggregate == HealthHealthy {
				t.Fatalf("%s capability %s aggregated to HEALTHY: %#v", observation.State, observation.ID, observations)
			}
			// A required capability that is not available cannot be softened
			// into a degradation, which is what a clean backup claim rests on.
			if observation.Requirement == CapabilityRequired && observation.State == HealthUnavailable &&
				aggregate != HealthUnavailable {
				t.Fatalf("a required unavailable capability aggregated to %s: %#v", aggregate, observations)
			}
			// An unknown required observation must never be resolved in
			// Alih's favour.
			if observation.Requirement == CapabilityRequired && observation.State == HealthUnknown &&
				aggregate == HealthHealthy {
				t.Fatalf("an unknown required capability aggregated to HEALTHY: %#v", observations)
			}
		}
		// The answer must not depend on the order observations arrived in.
		reversed := make([]CapabilityHealth, len(observations))
		for index, observation := range observations {
			reversed[len(observations)-1-index] = observation
		}
		if other, _, _ := AggregateCapabilityHealth(reversed); other != aggregate {
			t.Fatalf("order changed the aggregate: %s then %s", aggregate, other)
		}
	})
}
