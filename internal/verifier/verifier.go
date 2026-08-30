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
