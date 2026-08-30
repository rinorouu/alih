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

// Package exporter coordinates the M3-to-M4 boundary without putting
// ClickUp-specific normalization inside the core archive writer.
package exporter

import (
	"context"
	"fmt"
	"net/http"

	"alih/internal/archive"
	"alih/internal/connector/clickup"
	"alih/internal/snapshot"
)

type Service struct {
	httpClient *http.Client
}

func New(httpClient *http.Client) *Service { return &Service{httpClient: httpClient} }

func (service *Service) Name() string { return "m4-portable-archive" }

func (service *Service) Export(ctx context.Context, snapshotPath, outputPath, credential string) (archive.Summary, error) {
	evidence, err := snapshot.LoadComplete(snapshotPath)
	if err != nil {
		return archive.Summary{}, fmt.Errorf("load completed M3 snapshot: %w", err)
	}
	if evidence.Connector != "clickup" {
		return archive.Summary{}, fmt.Errorf("no M4 adapter for connector %q", evidence.Connector)
	}
	portable, err := clickup.NormalizeSnapshot(evidence)
	if err != nil {
		return archive.Summary{}, fmt.Errorf("normalize ClickUp raw evidence: %w", err)
	}
	return archive.Build(ctx, evidence, portable, outputPath, archive.Options{
		HTTPClient: service.httpClient,
		Credential: credential,
	})
}
