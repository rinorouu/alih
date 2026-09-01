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

// Package verifier coordinates M5 verification. It selects the field-semantics
// interpreter belonging to the connector that produced an archive, and imports
// no adapter itself: the composition root decides which adapters exist, so
// adding one does not mean editing this package.
package verifier

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"alih/internal/verify"
)

// maxManifestBytes bounds the manifest read that selects an interpreter. The
// verifier reads the manifest again, in full, as part of verification itself.
const maxManifestBytes = 8 << 20

// Service verifies an archive using the connector-specific evidence
// interpreters this build was given.
type Service struct {
	semantics map[string]verify.FieldSemantics
}

// New creates the M5 verification service. Each interpreter is registered
// under the connector it declares.
func New(semantics ...verify.FieldSemantics) *Service {
	registry := make(map[string]verify.FieldSemantics, len(semantics))
	for _, interpreter := range semantics {
		if interpreter == nil {
			continue
		}
		registry[interpreter.Connector()] = interpreter
	}
	return &Service{semantics: registry}
}

// Name identifies this application service.
func (service *Service) Name() string { return "m5-verification" }

// Verify reads the archive at path and reports what can be proven from it. It
// performs no network access and never writes to the archive.
//
// An archive from a connector this build has no interpreter for is still
// verified; its custom-field values are simply left unproven rather than
// guessed at, which is the same fail-safe answer as having no interpreter at
// all.
func (service *Service) Verify(path string) (verify.Report, error) {
	return verify.Archive(path, verify.Options{
		FieldSemantics: service.interpreterFor(path),
	})
}

// interpreterFor selects by the connector the archive itself records. A
// missing, unreadable, or unknown connector yields no interpreter rather than
// a guess.
func (service *Service) interpreterFor(path string) verify.FieldSemantics {
	file, err := os.Open(filepath.Join(path, "manifest.json"))
	if err != nil {
		return nil
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxManifestBytes))
	if err != nil {
		return nil
	}
	var manifest struct {
		Connector string `json:"connector"`
	}
	if err := json.NewDecoder(bytes.NewReader(content)).Decode(&manifest); err != nil {
		return nil
	}
	interpreter, known := service.semantics[manifest.Connector]
	if !known {
		return nil
	}
	return interpreter
}
