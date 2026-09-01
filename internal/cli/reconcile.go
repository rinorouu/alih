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

package cli

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"alih/internal/connector"
	"alih/internal/state"
)

// Reconciliation walks a backup destination and rebuilds what Alih can prove
// about it. It is deliberately opt-in: a directory tree is not authoritative,
// and reconstructing state from it costs a full independent verification of
// every archive found.
//
// Nothing here trusts a directory name. Which workspace an archive belongs to
// is read from the archive's own sealed manifest; the destination is the path
// the operator asked Alih to scan. A bundle that cannot be verified now is
// reported and left alone rather than recorded as a weaker kind of success.

// maxReconcileDepth bounds the walk. The published layout is
// <root>/<workspace>/<timestamp>/archive, so anything deeper is not a bundle
// Alih wrote and is not searched.
const maxReconcileDepth = 4

type reconcileFinding struct {
	Path   string `json:"path"`
	Detail string `json:"detail,omitempty"`
}

type reconcileOutcome struct {
	Root        string             `json:"root"`
	Recorded    []reconcileFinding `json:"recorded,omitempty"`
	Skipped     []reconcileFinding `json:"skipped,omitempty"`
	Failed      []string           `json:"failed_working_states,omitempty"`
	Abandoned   []string           `json:"abandoned_working_states,omitempty"`
	NotFollowed []reconcileFinding `json:"not_followed,omitempty"`
}

// reconcileDestination verifies every archive found under root and records what
// verification proves. It never deletes, moves, or rewrites anything it finds.
func (a *App) reconcileDestination(store *state.Store, root string) reconcileOutcome {
	outcome := reconcileOutcome{Root: root}
	if a.options.Verifier == nil {
		outcome.Skipped = append(outcome.Skipped, reconcileFinding{
			Path: root, Detail: "no verifier is configured, so no archive could be proven",
		})
		return outcome
	}
	info, err := os.Lstat(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		outcome.Skipped = append(outcome.Skipped, reconcileFinding{
			Path: root, Detail: "the backup destination does not exist yet",
		})
		return outcome
	case err != nil:
		outcome.Skipped = append(outcome.Skipped, reconcileFinding{
			Path: root, Detail: "the backup destination could not be read",
		})
		return outcome
	case !info.IsDir():
		outcome.Skipped = append(outcome.Skipped, reconcileFinding{
			Path: root, Detail: "the backup destination is not a directory",
		})
		return outcome
	}

	archives, working := discoverBundles(root, &outcome)
	outcome.Failed = working.failed
	outcome.Abandoned = working.abandoned

	sort.Strings(archives)
	type dated struct {
		path      string
		completed time.Time
	}
	ordered := make([]dated, 0, len(archives))
	for _, archivePath := range archives {
		identity, manifest, err := readSealedArchive(archivePath)
		if err != nil {
			outcome.Skipped = append(outcome.Skipped, reconcileFinding{
				Path: archivePath, Detail: "its sealed manifest could not be read",
			})
			continue
		}
		if manifest.ArchiveCompletedAt == nil || identity.ArchiveStatus == "" {
			outcome.Skipped = append(outcome.Skipped, reconcileFinding{
				Path: archivePath, Detail: "it does not record when it was completed",
			})
			continue
		}
		ordered = append(ordered, dated{path: archivePath, completed: manifest.ArchiveCompletedAt.UTC()})
	}
	// Oldest first, so that the newest evidence is the state that remains.
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].completed.Before(ordered[j].completed) })

	for _, candidate := range ordered {
		a.reconcileArchive(store, root, candidate.path, &outcome)
	}
	sort.Slice(outcome.Recorded, func(i, j int) bool { return outcome.Recorded[i].Path < outcome.Recorded[j].Path })
	sort.Slice(outcome.Skipped, func(i, j int) bool { return outcome.Skipped[i].Path < outcome.Skipped[j].Path })
	return outcome
}

func (a *App) reconcileArchive(store *state.Store, root, archivePath string, outcome *reconcileOutcome) {
	identity, manifest, err := readSealedArchive(archivePath)
	if err != nil {
		outcome.Skipped = append(outcome.Skipped, reconcileFinding{
			Path: archivePath, Detail: "its sealed manifest could not be read",
		})
		return
	}
	if strings.TrimSpace(manifest.Connector) == "" || strings.TrimSpace(manifest.Source.ID) == "" {
		outcome.Skipped = append(outcome.Skipped, reconcileFinding{
			Path: archivePath, Detail: "its manifest does not say which source it came from",
		})
		return
	}

	report, err := a.options.Verifier.Verify(archivePath)
	if err != nil || !state.ValidResult(report.Result) {
		outcome.Skipped = append(outcome.Skipped, reconcileFinding{
			Path: archivePath, Detail: "it could not be verified now, so nothing about it was recorded",
		})
		return
	}
	verification := a.verificationRecord(report, identity)
	scope := backupScope(displayValue(manifest.Connector), displayValue(manifest.Source.ID), root)

	proven := report.Result == state.ResultVerified || report.Result == state.ResultVerifiedWithLimitations
	started := manifest.SourceSnapshotCompletedAt.UTC()
	ended := manifest.ArchiveCompletedAt.UTC()
	if started.IsZero() || started.After(ended) {
		started = ended
	}
	// The operation identity is derived from the archive itself, so reconciling
	// the same archive twice never invents a second run.
	checksum := strings.TrimPrefix(identity.ManifestChecksum, "sha256:")
	if len(checksum) > 16 {
		checksum = checksum[:16]
	}
	if _, decodeErr := hex.DecodeString(checksum); decodeErr != nil {
		checksum = "unknown"
	}
	reconstructed := state.Attempt{
		OperationID: "reconciled-" + checksum, Operation: state.OperationBackup,
		Stage: state.StageFinalize, Outcome: state.OutcomeSucceeded,
		StartedAt: started, EndedAt: &ended, ArchivePath: identity.Path,
		AlihVersion: displayValue(manifest.AlihVersion),
	}
	if reportPath := filepath.Join(filepath.Dir(identity.Path), backupReportFilename); regularFileExists(reportPath) {
		reconstructed.ReportPath = reportPath
	}

	detail := "recorded the verification of this archive"
	if _, err := store.Update(scope, func(record *state.Record) error {
		record.UpdatedAt = a.observedAt()
		if record.WorkspaceName == "" {
			record.WorkspaceName = displayValue(manifest.Source.Name)
		}
		if record.Account == nil && manifest.ExtractedBy != nil {
			account := *manifest.ExtractedBy
			account.ID, account.Name = displayValue(account.ID), displayValue(account.Name)
			record.Account = &account
		}
		if record.CapabilitySchemaVersion == 0 && len(record.Capabilities) == 0 && len(manifest.Capabilities) > 0 {
			record.CapabilitySchemaVersion = manifest.CapabilitySchemaVersion
			record.Capabilities = manifest.Capabilities
		}
		record.LastVerification = &verification

		// The archive carries the operational assessment of the run that wrote
		// it. Restoring it recovers real evidence with its original observation
		// time, which status will present as the aged observation it is. It is
		// never allowed to overwrite a more recent observation.
		if assessment := manifest.OperationalAssessment; assessment != nil &&
			connector.ValidateOperationalAssessment(*assessment) == nil &&
			(record.Assessment == nil || assessment.Health.ObservedAt.After(record.Assessment.Health.ObservedAt)) {
			observed := *assessment
			record.Assessment = &observed
		}

		// A verified archive is evidence that a backup succeeded, but only
		// evidence about that archive: an older one never replaces a newer run
		// Alih already recorded.
		if proven && supersedesRecordedSuccess(record, ended) {
			success := reconstructed
			record.LastSuccess = &success
			if record.LastAttempt == nil || record.LastAttempt.StartedAt.Before(success.StartedAt) {
				attempt := success
				record.LastAttempt = &attempt
			}
			detail = "recorded this archive as the last successful backup"
		}
		return nil
	}); err != nil {
		a.warnState(err)
		outcome.Skipped = append(outcome.Skipped, reconcileFinding{
			Path: archivePath, Detail: "its state could not be written",
		})
		return
	}
	a.emitStandaloneVerification(store, scope, verification)
	outcome.Recorded = append(outcome.Recorded, reconcileFinding{Path: archivePath, Detail: detail})
}

func supersedesRecordedSuccess(record *state.Record, completed time.Time) bool {
	if record.LastSuccess == nil || record.LastSuccess.EndedAt == nil {
		return true
	}
	return completed.After(*record.LastSuccess.EndedAt)
}

type workingStates struct {
	failed    []string
	abandoned []string
}

// discoverBundles finds sealed archives, preserved failures, and abandoned
// working directories. Symbolic links are never followed: a link could point
// anywhere, and following one would let a directory outside the destination
// masquerade as part of it.
func discoverBundles(root string, outcome *reconcileOutcome) ([]string, workingStates) {
	var archives []string
	var working workingStates
	rootDepth := len(strings.Split(filepath.Clean(root), string(os.PathSeparator)))

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			outcome.NotFollowed = append(outcome.NotFollowed, reconcileFinding{
				Path: path, Detail: "it could not be read",
			})
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			outcome.NotFollowed = append(outcome.NotFollowed, reconcileFinding{
				Path: path, Detail: "it is a symbolic link, which could point anywhere outside the destination",
			})
			return fs.SkipDir
		}
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, ".failed"):
			working.failed = append(working.failed, path)
			return fs.SkipDir
		case strings.Contains(name, ".partial-"):
			working.abandoned = append(working.abandoned, path)
			return fs.SkipDir
		}
		if len(strings.Split(filepath.Clean(path), string(os.PathSeparator)))-rootDepth >= maxReconcileDepth {
			return fs.SkipDir
		}
		if regularFileExists(filepath.Join(path, state.ManifestFilename)) {
			archives = append(archives, path)
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		outcome.NotFollowed = append(outcome.NotFollowed, reconcileFinding{
			Path: root, Detail: "it could not be walked completely",
		})
	}
	sort.Strings(working.failed)
	sort.Strings(working.abandoned)
	sort.Slice(outcome.NotFollowed, func(i, j int) bool { return outcome.NotFollowed[i].Path < outcome.NotFollowed[j].Path })
	return archives, working
}

func regularFileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func writeReconcileText(output io.Writer, outcome reconcileOutcome) {
	fmt.Fprintf(output, "\nReconciled destination: %s\n", outcome.Root)
	for _, finding := range outcome.Recorded {
		fmt.Fprintf(output, "  %s\n    %s\n", finding.Path, finding.Detail)
	}
	for _, finding := range outcome.Skipped {
		fmt.Fprintf(output, "  %s\n    Not recorded: %s\n", finding.Path, finding.Detail)
	}
	for _, path := range outcome.Failed {
		fmt.Fprintf(output, "  %s\n    Preserved failed run; it is not a backup.\n", path)
	}
	for _, path := range outcome.Abandoned {
		fmt.Fprintf(output, "  %s\n    Abandoned working directory; it is not a backup.\n", path)
	}
	for _, finding := range outcome.NotFollowed {
		fmt.Fprintf(output, "  %s\n    Not followed: %s\n", finding.Path, finding.Detail)
	}
	if len(outcome.Recorded)+len(outcome.Skipped)+len(outcome.Failed)+len(outcome.Abandoned)+len(outcome.NotFollowed) == 0 {
		fmt.Fprintln(output, "  No archive, failed run, or working directory was found there.")
	}
}
