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
