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
	"fmt"
	"io"
)

// WriteOperationalAssessmentText provides the shared human renderer used by
// current commands and the future status command.
func WriteOperationalAssessmentText(output io.Writer, assessment OperationalAssessment) error {
	if err := ValidateOperationalAssessment(assessment); err != nil {
		return err
	}
	CanonicalizeOperationalAssessment(&assessment)
	// The connector is named because an installation may have more than one,
	// and a health line that does not say whose health it is cannot be acted on.
	if _, err := fmt.Fprintf(output, "Connector health: %s (%s)\nAuthentication: %s (%s) — %s\nEvidence basis: %s at %s\nReason: %s — %s\n",
		assessment.Health.State, assessment.Health.Connector,
		assessment.Authentication.State, assessment.Authentication.Reason,
		assessment.Authentication.Message, assessment.Health.Basis,
		assessment.Health.ObservedAt.Format("2006-01-02T15:04:05Z07:00"), assessment.Health.Reason,
		assessment.Health.Message); err != nil {
		return err
	}
	for _, capability := range assessment.Health.Capabilities {
		if capability.State == HealthHealthy {
			continue
		}
		if _, err := fmt.Fprintf(output, "Affected capability: %s (%s, %s, retryable=%t)\n",
			capability.ID, capability.Requirement, capability.Reason, capability.Retryable); err != nil {
			return err
		}
	}
	return nil
}

// WriteOperationalAssessmentJSON is a stable machine-readable renderer.
func WriteOperationalAssessmentJSON(output io.Writer, assessment OperationalAssessment) error {
	if err := ValidateOperationalAssessment(assessment); err != nil {
		return err
	}
	CanonicalizeOperationalAssessment(&assessment)
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(true)
	return encoder.Encode(assessment)
}
