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

// Package verifier coordinates M5 verification without putting ClickUp
// specifics inside the core verifier.
package verifier

import (
	"alih/internal/connector/clickup"
	"alih/internal/verify"
)

// Service verifies an archive using the connector-specific evidence
// interpreters Alih currently ships.
type Service struct{}

// New creates the M5 verification service.
func New() *Service { return &Service{} }

// Name identifies this application service.
func (service *Service) Name() string { return "m5-verification" }

// Verify reads the archive at path and reports what can be proven from it. It
// performs no network access and never writes to the archive.
func (service *Service) Verify(path string) (verify.Report, error) {
	return verify.Archive(path, verify.Options{FieldSemantics: clickup.FieldSemantics{}})
}
