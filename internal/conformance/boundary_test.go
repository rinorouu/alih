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

package conformance

import (
	"context"
	"errors"
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"alih/internal/connector"
	"alih/internal/event"
	"alih/internal/exporter"
	"alih/internal/model"
	"alih/internal/oplock"
	"alih/internal/snapshot"
	"alih/internal/state"
	"alih/internal/verifier"
)

const adapterPackage = "alih/internal/connector/clickup"

func moduleRoot(t *testing.T) string {
	t.Helper()
	directory, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not find the module root")
		}
		directory = parent
	}
}

// TestOnlyTheCompositionRootImportsAnAdapter is the structural half of the
// reuse question. Core coordinators may know that connectors exist; they may
// not know which one. If this test fails, a second connector can no longer be
// added by wiring alone.
func TestOnlyTheCompositionRootImportsAnAdapter(t *testing.T) {
	t.Parallel()

	root := moduleRoot(t)
	allowed := map[string]bool{
		// The composition root chooses the adapters this build ships.
		"cmd/alih": true,
	}

	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "internal/connector/clickup" || allowed[relative] {
			return nil
		}
		parsed, err := build.ImportDir(filepath.Dir(path), build.ImportComment)
		if err != nil {
			return nil
		}
		for _, imported := range parsed.Imports {
			if imported == adapterPackage {
				offenders = append(offenders, relative)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, offender := range unique(offenders) {
		t.Errorf("package %s imports the ClickUp adapter directly; only the composition root may", offender)
	}
}

func unique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	var result []string
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// TestAnUnknownConnectorIsRefusedRatherThanGuessed proves the coordinators
// fail closed on evidence they have no adapter for, instead of falling back to
// whichever adapter happens to be first.
func TestAnUnknownConnectorIsRefusedRatherThanGuessed(t *testing.T) {
	t.Parallel()

	fake := newFakeConnector()
	root := t.TempDir()
	snapshotPath := filepath.Join(root, "m3")
	session, err := snapshot.Begin(snapshotPath, fake.Name(), fake.workspaces[0],
		connector.Identity{ID: "acct-1", Name: "Example Account"})
	if err != nil {
		t.Fatal(err)
	}
	extraction, err := fake.Extract(context.Background(), "example-credential", fake.workspaces[0], session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Complete(extraction); err != nil {
		t.Fatal(err)
	}

	// An exporter that was given no adapter for this connector must refuse.
	empty := exporter.New(nil)
	if _, err := empty.Export(context.Background(), snapshotPath, filepath.Join(root, "archive"), "credential"); err == nil {
		t.Fatal("an exporter with no adapter produced an archive")
	} else if !strings.Contains(err.Error(), fakeConnectorName) {
		t.Errorf("the refusal does not name the connector it could not handle: %v", err)
	}

	// An exporter given a different connector's adapter must also refuse,
	// rather than normalizing foreign evidence with the wrong reader.
	wrong := exporter.New(nil, wrongNormalizer{})
	if _, err := wrong.Export(context.Background(), snapshotPath, filepath.Join(root, "archive2"), "credential"); err == nil {
		t.Fatal("an exporter with the wrong adapter produced an archive")
	}
}

type wrongNormalizer struct{}

func (wrongNormalizer) Connector() string   { return "a-different-connector" }
func (wrongNormalizer) DisplayName() string { return "A Different Connector" }
func (wrongNormalizer) NormalizeSnapshot(snapshot.Evidence) (model.Archive, error) {
	return model.Archive{}, errors.New("this adapter should never be selected")
}

// TestVerificationWithoutAnInterpreterStaysFailSafe proves an archive from a
// connector this build has no field interpreter for is still verified, with
// its field values left unproven rather than guessed.
func TestVerificationWithoutAnInterpreterStaysFailSafe(t *testing.T) {
	t.Parallel()

	archivePath, _ := buildFakeArchive(t, 200)

	bare := verifier.New()
	report, err := bare.Verify(archivePath)
	if err != nil {
		t.Fatalf("verify without an interpreter: %v", err)
	}
	if report.Failed() {
		t.Fatalf("an archive was failed merely for lacking an interpreter: %s", report.Result)
	}
	var proven bool
	for _, claim := range report.NotProven {
		if strings.Contains(strings.ToLower(claim), "field") {
			proven = true
		}
	}
	if !proven {
		t.Errorf("field values were neither proven nor declared unproven: %v", report.NotProven)
	}

	// With the adapter's own interpreter, the same archive proves more.
	informed := verifier.New(fakeFieldSemantics{})
	informedReport, err := informed.Verify(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(informedReport.NotProven) >= len(report.NotProven) {
		t.Errorf("supplying the connector's interpreter proved nothing extra: %v vs %v",
			informedReport.NotProven, report.NotProven)
	}
}

// TestTwoConnectorsNeverCollideInOperationalState proves the identity keys the
// operational layers use separate connectors, so one connector's run can never
// overwrite or be mistaken for another's.
func TestTwoConnectorsNeverCollideInOperationalState(t *testing.T) {
	t.Parallel()

	destination := filepath.Join(t.TempDir(), "Alih")
	first := state.Scope{Connector: "example", WorkspaceID: "ws-1", Destination: destination}
	// Same workspace identifier, same destination, different connector.
	second := state.Scope{Connector: "other-example", WorkspaceID: "ws-1", Destination: destination}

	if first.Key() == second.Key() {
		t.Fatal("two connectors share one operational state file")
	}

	store, err := state.NewStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []state.Scope{first, second} {
		if _, err := store.Update(scope, func(record *state.Record) error {
			record.WorkspaceName = "Shared identifier, different provider"
			record.UpdatedAt = fakeClock()
			record.AlihVersion = "test-version"
			return nil
		}); err != nil {
			t.Fatalf("record %s: %v", scope.Connector, err)
		}
	}
	records, unreadable, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(unreadable) != 0 {
		t.Fatalf("unreadable state: %v", unreadable)
	}
	if len(records) != 2 {
		t.Fatalf("recorded %d scopes, want one per connector", len(records))
	}

	// The same separation must hold for the lock that prevents overlap, or two
	// connectors backing up the same destination would exclude one another.
	lockRoot := filepath.Join(t.TempDir(), "locks")
	firstLock, err := oplock.Path(lockRoot, first)
	if err != nil {
		t.Fatal(err)
	}
	secondLock, err := oplock.Path(lockRoot, second)
	if err != nil {
		t.Fatal(err)
	}
	if firstLock == secondLock {
		t.Fatal("two connectors share one operation lock")
	}

	// And for recorded history, so a status summary cannot mix them.
	firstSource := event.Source{Connector: "example", WorkspaceID: "ws-1", Destination: destination}
	secondSource := event.Source{Connector: "other-example", WorkspaceID: "ws-1", Destination: destination}
	if firstSource == secondSource {
		t.Fatal("two connectors share one event source identity")
	}
}
