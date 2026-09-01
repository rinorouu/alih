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

// Package exporter coordinates the M3-to-M4 boundary. It selects the adapter
// that can normalize a snapshot's evidence by the connector that produced it,
// and imports no adapter itself: the composition root decides which adapters
// exist, so adding one does not mean editing this package.
package exporter

import (
	"context"
	"fmt"
	"net/http"

	"alih/internal/archive"
	"alih/internal/buildinfo"
	"alih/internal/connector"
	"alih/internal/model"
	"alih/internal/snapshot"
)

// Normalizer turns one connector's raw M3 evidence into the portable model.
// It is the only provider-specific step in the M3-to-M4 boundary, and it is
// supplied rather than imported.
type Normalizer interface {
	// Connector is the connector name this adapter normalizes, matching the
	// name recorded in the raw snapshot.
	Connector() string
	// DisplayName is how the connector names itself to a person. It is sealed
	// into the archive so a recovery report can name the provider correctly
	// without Core knowing any provider's name.
	DisplayName() string
	NormalizeSnapshot(snapshot.Evidence) (model.Archive, error)
}

type Service struct {
	httpClient  *http.Client
	alihVersion string
	normalizers map[string]Normalizer
}

// New creates the M4 export service using the running build's identity.
func New(httpClient *http.Client, normalizers ...Normalizer) *Service {
	return NewWithVersion(httpClient, "", normalizers...)
}

// NewWithVersion creates the service with an injected release identity, so an
// archive records the version of the application that actually produced it.
func NewWithVersion(httpClient *http.Client, alihVersion string, normalizers ...Normalizer) *Service {
	registry := make(map[string]Normalizer, len(normalizers))
	for _, normalizer := range normalizers {
		if normalizer == nil {
			continue
		}
		registry[normalizer.Connector()] = normalizer
	}
	return &Service{
		httpClient: httpClient, alihVersion: buildinfo.Resolve(alihVersion), normalizers: registry,
	}
}

func (service *Service) Name() string { return "m4-portable-archive" }

// Connector reports the connector this service can export, when it holds
// exactly one adapter.
//
// It exists so a command that has no authenticator wired -- "alih export" reads
// a snapshot rather than a source -- can still tell a person which credential
// variable to set. The answer comes from the registered normalizer rather than
// from a value Core stores separately, so there is one source of truth. With
// several adapters registered the connector is whatever the snapshot records,
// which is not knowable here, and the empty answer says exactly that.
func (service *Service) Connector() string {
	if len(service.normalizers) != 1 {
		return ""
	}
	for name := range service.normalizers {
		return name
	}
	return ""
}

func (service *Service) Export(ctx context.Context, snapshotPath, outputPath, credential string) (archive.Summary, error) {
	evidence, err := snapshot.LoadComplete(snapshotPath)
	if err != nil {
		return archive.Summary{}, fmt.Errorf("load completed M3 snapshot: %w", err)
	}
	normalizer, known := service.normalizers[evidence.Connector]
	if !known {
		return archive.Summary{}, fmt.Errorf("no M4 adapter for connector %q", evidence.Connector)
	}
	portable, err := normalizer.NormalizeSnapshot(evidence)
	if err != nil {
		return archive.Summary{}, fmt.Errorf("normalize %s raw evidence: %w", evidence.Connector, err)
	}
	// Which hosts may receive the credential is the connector's knowledge, not
	// Core's. A normalizer that declares nothing gets nothing attached, which
	// is the safe answer for a provider whose attachments are pre-signed.
	var credentialHosts []string
	if provider, ok := normalizer.(connector.CredentialHostProvider); ok {
		credentialHosts = provider.CredentialHosts()
	}
	return archive.Build(ctx, evidence, portable, outputPath, archive.Options{
		HTTPClient:           service.httpClient,
		Credential:           credential,
		AlihVersion:          service.alihVersion,
		ConnectorDisplayName: normalizer.DisplayName(),
		CredentialHosts:      credentialHosts,
	})
}
