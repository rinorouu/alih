// Copyright 2026 rinorouu
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

// Package archive writes the source-agnostic M4 portable archive. It does not
// implement independent M5 verification or an M6 report.
package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"alih/internal/connector"
	"alih/internal/model"
	"alih/internal/snapshot"
)

const (
	// ArchiveSchemaVersion is the version of the archive manifest format.
	// Version 2 replaced the ambiguous "created_at" field, which actually held
	// the M3 extraction time, with two explicitly named instants.
	ArchiveSchemaVersion = 2

	StatusCreatedUnverified = "CREATED_UNVERIFIED"
	StatusIncomplete        = "INCOMPLETE"
	StatusFailed            = "FAILED"
	maxAttachmentBytes      = int64(1 << 30)
	maxDownloadAttempts     = 3
)

type EntityCount struct {
	Expected   int `json:"expected"`
	Archived   int `json:"archived"`
	Unresolved int `json:"unresolved"`
}

type FileRecord struct {
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Checksum string `json:"checksum"`
}

type AttachmentRecord struct {
	ID           string  `json:"id"`
	SourceID     string  `json:"source_id"`
	RecordID     string  `json:"record_id"`
	Filename     *string `json:"filename"`
	MediaType    *string `json:"media_type"`
	ExpectedSize *int64  `json:"expected_size"`
	Status       string  `json:"status"`
	LocalPath    *string `json:"local_path"`
	ArchivedSize *int64  `json:"archived_size"`
	Checksum     *string `json:"checksum"`
	Error        *string `json:"error"`
}

type Discrepancy struct {
	Kind     string `json:"kind"`
	SourceID string `json:"source_id,omitempty"`
	Message  string `json:"message"`
}

type Manifest struct {
	SchemaVersion int    `json:"schema_version"`
	AlihVersion   string `json:"alih_version"`
	Status        string `json:"status"`
	// SourceSnapshotCompletedAt is the instant the M3 raw extraction finished
	// reading the source. It says nothing about when this archive was built.
	SourceSnapshotCompletedAt time.Time `json:"source_snapshot_completed_at"`
	// ArchiveCompletedAt is the instant this archive's content was complete
	// and its manifest sealed. It is read from a clock at that moment, never
	// derived from filesystem timestamps, and is null when the archive was
	// never completed. It is the one field that legitimately differs between
	// two archives built from identical evidence.
	ArchiveCompletedAt *time.Time          `json:"archive_completed_at"`
	Connector          string              `json:"connector"`
	Source             connector.Workspace `json:"source"`
	// ExtractedBy names the authenticated account whose access produced this
	// archive. The archive is that identity's view of the source and nothing
	// wider; it is absent for snapshots taken before Alih recorded it.
	ExtractedBy   *connector.Identity    `json:"extracted_by,omitempty"`
	InputSnapshot InputSnapshot          `json:"input_snapshot"`
	Inventory     map[string]EntityCount `json:"inventory"`
	Observed      map[string]int         `json:"observed_entities"`
	Capabilities  []connector.Capability `json:"capabilities"`
	Attachments   []AttachmentRecord     `json:"attachments"`
	Files         []FileRecord           `json:"files"`
	Limitations   []string               `json:"limitations"`
	Discrepancies []Discrepancy          `json:"discrepancies"`
	Verification  VerificationState      `json:"verification"`
}

type InputSnapshot struct {
	LogicalDigest string `json:"logical_inventory_digest"`
	Status        string `json:"status"`
	Atomic        bool   `json:"atomic"`
}

type VerificationState struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

type Summary struct {
	Path        string
	Status      string
	Inventory   map[string]EntityCount
	Observed    map[string]int
	Attachments []AttachmentRecord
}

type Options struct {
	HTTPClient *http.Client
	Credential string
	Sleep      func(context.Context, time.Duration) error
	// Now supplies the archive completion instant. It exists so that the
	// timestamp is an injected observation rather than a hidden global, and so
	// tests can prove it is neither a filesystem mtime nor the snapshot time.
	Now func() time.Time
}

// Build creates a new archive directory. A supported attachment failure still
// creates an explicit INCOMPLETE archive and must be treated as a non-clean
// command result by the caller.
func Build(ctx context.Context, evidence snapshot.Evidence, portable model.Archive, targetPath string, options Options) (Summary, error) {
	if err := validatePortable(evidence, portable); err != nil {
		return Summary{}, err
	}
	if err := ensureTreeExcludesSecret(evidence.RootPath, options.Credential); err != nil {
		return Summary{}, err
	}
	absolute, err := filepath.Abs(targetPath)
	if err != nil {
		return Summary{}, err
	}
	if nested, err := pathWithin(evidence.RootPath, absolute); err != nil {
		return Summary{}, fmt.Errorf("compare archive and snapshot paths: %w", err)
	} else if nested {
		return Summary{}, errors.New("archive output path must not be inside the raw snapshot")
	}
	if _, err := os.Lstat(absolute); err == nil {
		return Summary{}, fmt.Errorf("archive output path already exists: %s", absolute)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Summary{}, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return Summary{}, err
	}
	staging, err := os.MkdirTemp(filepath.Dir(absolute), "."+filepath.Base(absolute)+".partial-")
	if err != nil {
		return Summary{}, err
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		_ = os.Remove(staging)
		return Summary{}, err
	}
	fail := func(cause error) (Summary, error) {
		failedPath, preserveErr := preserveFailedArchive(staging, absolute, evidence, portable, cause)
		if preserveErr != nil {
			return Summary{Path: staging, Status: StatusFailed}, fmt.Errorf("%v; preserve failed archive: %w", cause, preserveErr)
		}
		return Summary{Path: failedPath, Status: StatusFailed}, cause
	}

	if err := os.Mkdir(filepath.Join(staging, "attachments"), 0o700); err != nil {
		return fail(err)
	}
	if err := copyRawTree(evidence.RootPath, filepath.Join(staging, "raw")); err != nil {
		return fail(fmt.Errorf("copy immutable raw evidence: %w", err))
	}
	portable.Attachments = append([]model.Attachment(nil), portable.Attachments...)
	downloadAttachments(ctx, filepath.Join(staging, "attachments"), &portable, options)

	metadata := map[string]string{
		"alih_version":                   "0.0.1",
		"archive_schema_version":         strconv.Itoa(ArchiveSchemaVersion),
		"archive_status":                 archiveStatus(portable.Attachments),
		"connector":                      evidence.Connector,
		"source_workspace_id":            evidence.Workspace.ID,
		"source_snapshot_logical_digest": evidence.LogicalDigest,
		"verification_status":            "NOT_RUN",
		"source_snapshot_atomic":         "false",
	}
	if err := writeSQLite(filepath.Join(staging, "alih.db"), portable, metadata); err != nil {
		return fail(fmt.Errorf("write alih.db: %w", err))
	}
	if err := writeJSON(filepath.Join(staging, "schema.json"), portableSchemaDocument()); err != nil {
		return fail(fmt.Errorf("write schema.json: %w", err))
	}

	// Every archive member except manifest.json is now written, so this is the
	// instant the archive's content is complete.
	completedAt := clock(options)().UTC()
	manifest, err := buildManifest(staging, evidence, portable, &completedAt)
	if err != nil {
		return fail(err)
	}
	if err := writeJSON(filepath.Join(staging, "manifest.json"), manifest); err != nil {
		return fail(fmt.Errorf("write manifest.json: %w", err))
	}
	if err := os.Rename(staging, absolute); err != nil {
		return fail(fmt.Errorf("finalize archive: %w", err))
	}
	return Summary{
		Path: absolute, Status: manifest.Status, Inventory: manifest.Inventory,
		Observed: manifest.Observed, Attachments: manifest.Attachments,
	}, nil
}

func validatePortable(evidence snapshot.Evidence, portable model.Archive) error {
	if portable.Connector != evidence.Connector || portable.Workspace.Source.ID != evidence.Workspace.ID {
		return errors.New("portable model source does not match raw snapshot")
	}
	countKind := func(kind string) int {
		count := 0
		for _, value := range portable.Containers {
			if value.Kind == kind {
				count++
			}
		}
		return count
	}
	countRecords := func(kind string) int {
		count := 0
		for _, value := range portable.Records {
			if value.Kind == kind {
				count++
			}
		}
		return count
	}
	checks := []struct {
		name             string
		expected, actual int
	}{
		{"spaces", evidence.Inventory.Spaces, countKind("space")},
		{"folders", evidence.Inventory.Folders, countKind("folder")},
		{"lists", evidence.Inventory.Lists, len(portable.Collections)},
		{"tasks", evidence.Inventory.Tasks, countRecords("task")},
		{"subtasks", evidence.Inventory.Subtasks, countRecords("subtask")},
		{"comments", evidence.Inventory.Comments, len(portable.Comments)},
		{"attachments", evidence.Inventory.Attachments, len(portable.Attachments)},
		{"custom_fields", evidence.Inventory.CustomFields, len(portable.FieldDefinitions)},
		{"relationships", evidence.Inventory.Relationships, len(portable.Relationships)},
	}
	for _, check := range checks {
		if check.expected != check.actual {
			return fmt.Errorf("portable %s count mismatch: expected %d from M3, normalized %d", check.name, check.expected, check.actual)
		}
	}
	return nil
}

func downloadAttachments(ctx context.Context, directory string, portable *model.Archive, options Options) {
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	clientCopy := *client
	priorRedirect := client.CheckRedirect
	clientCopy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != "https" {
			request.Header.Del("Authorization")
			return errors.New("attachment redirect must use HTTPS")
		}
		if !strings.EqualFold(request.URL.Hostname(), "api.clickup.com") {
			request.Header.Del("Authorization")
		}
		if priorRedirect != nil {
			return priorRedirect(request, via)
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepWithContext
	}
	for index := range portable.Attachments {
		attachment := &portable.Attachments[index]
		if attachment.DownloadStatus == "UNRESOLVED" {
			continue
		}
		if attachment.ExpectedSize != nil && *attachment.ExpectedSize > maxAttachmentBytes {
			markAttachmentUnresolved(attachment, fmt.Sprintf("expected size exceeds M4 limit of %d bytes", maxAttachmentBytes))
			continue
		}
		extension := safeExtension(attachment.Filename)
		relative := filepath.ToSlash(filepath.Join("attachments", attachment.ID+extension))
		destination := filepath.Join(directory, attachment.ID+extension)
		var lastError error
		for attempt := 1; attempt <= maxDownloadAttempts; attempt++ {
			retrying, err := downloadAttachmentAttempt(ctx, &clientCopy, destination, attachment, options.Credential)
			if err == nil {
				attachment.DownloadStatus = "RETRIEVED"
				attachment.LocalPath = stringPointer(relative)
				lastError = nil
				break
			}
			lastError = err
			if !retrying || attempt == maxDownloadAttempts {
				break
			}
			if err := sleep(ctx, time.Duration(1<<(attempt-1))*250*time.Millisecond); err != nil {
				lastError = err
				break
			}
		}
		if lastError != nil {
			markAttachmentUnresolved(attachment, sanitizeAttachmentError(lastError, options.Credential))
		}
	}
}

func downloadAttachmentAttempt(ctx context.Context, client *http.Client, destination string, attachment *model.Attachment, credential string) (bool, error) {
	parsed, err := url.Parse(attachment.DownloadURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return false, errors.New("source supplied an invalid or non-HTTPS attachment download URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("Accept", "*/*")
	request.Header.Set("User-Agent", "alih-v0")
	if strings.EqualFold(parsed.Hostname(), "api.clickup.com") && credential != "" {
		request.Header.Set("Authorization", credential)
	}
	response, err := client.Do(request)
	if err != nil {
		return ctx.Err() == nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
		retry := response.StatusCode == 429 || response.StatusCode == 500 || response.StatusCode == 502 || response.StatusCode == 503 || response.StatusCode == 504
		return retry, fmt.Errorf("attachment endpoint returned HTTP %d", response.StatusCode)
	}
	temporary := destination + ".partial"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, maxAttachmentBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written > maxAttachmentBytes {
		_ = os.Remove(temporary)
		if copyErr != nil {
			return true, copyErr
		}
		if closeErr != nil {
			return false, closeErr
		}
		return false, errors.New("attachment exceeded M4 size limit")
	}
	if attachment.ExpectedSize != nil && written != *attachment.ExpectedSize {
		_ = os.Remove(temporary)
		return true, fmt.Errorf("attachment size mismatch: expected %d bytes, received %d", *attachment.ExpectedSize, written)
	}
	if credential != "" {
		contains, err := fileContains(temporary, []byte(credential))
		if err != nil {
			_ = os.Remove(temporary)
			return false, err
		}
		if contains {
			_ = os.Remove(temporary)
			return false, errors.New("attachment body contained the configured credential and was omitted")
		}
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return false, err
	}
	checksum := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	attachment.ArchivedSize = &written
	attachment.Checksum = &checksum
	attachment.Error = nil
	return false, nil
}

func markAttachmentUnresolved(attachment *model.Attachment, message string) {
	attachment.DownloadStatus = "UNRESOLVED"
	attachment.LocalPath = nil
	attachment.ArchivedSize = nil
	attachment.Checksum = nil
	attachment.Error = stringPointer(message)
}

// extractedIdentity carries the extracting account forward when the snapshot
// recorded one. It is never invented when the snapshot did not.
func extractedIdentity(evidence snapshot.Evidence) *connector.Identity {
	if strings.TrimSpace(evidence.ExtractedBy.ID) == "" {
		return nil
	}
	identity := evidence.ExtractedBy
	return &identity
}

func clock(options Options) func() time.Time {
	if options.Now != nil {
		return options.Now
	}
	return time.Now
}

func buildManifest(staging string, evidence snapshot.Evidence, portable model.Archive, completedAt *time.Time) (Manifest, error) {
	status := archiveStatus(portable.Attachments)
	inventory := manifestInventory(evidence.Inventory, portable)
	attachments := make([]AttachmentRecord, 0, len(portable.Attachments))
	discrepancies := make([]Discrepancy, 0)
	for _, attachment := range portable.Attachments {
		attachments = append(attachments, AttachmentRecord{
			ID: attachment.ID, SourceID: attachment.Source.ID, RecordID: attachment.RecordID,
			Filename: attachment.Filename, MediaType: attachment.MediaType, ExpectedSize: attachment.ExpectedSize,
			Status: attachment.DownloadStatus, LocalPath: attachment.LocalPath, ArchivedSize: attachment.ArchivedSize,
			Checksum: attachment.Checksum, Error: attachment.Error,
		})
		if attachment.DownloadStatus != "RETRIEVED" {
			discrepancies = append(discrepancies, Discrepancy{Kind: "ATTACHMENT_UNRESOLVED", SourceID: attachment.Source.ID, Message: valueOr(attachment.Error, "attachment unresolved")})
		}
	}
	for _, relationship := range portable.Relationships {
		if relationship.ResolutionState != "RESOLVED" {
			discrepancies = append(discrepancies, Discrepancy{
				Kind: "RELATIONSHIP_" + relationship.ResolutionState, SourceID: relationship.Source.ID,
				Message: "relationship metadata and original endpoint IDs were archived, but at least one endpoint is outside the archived record set",
			})
		}
	}
	sort.Slice(discrepancies, func(i, j int) bool {
		if discrepancies[i].Kind != discrepancies[j].Kind {
			return discrepancies[i].Kind < discrepancies[j].Kind
		}
		return discrepancies[i].SourceID < discrepancies[j].SourceID
	})
	files, err := collectFileRecords(staging)
	if err != nil {
		return Manifest{}, err
	}
	limitations := append([]string(nil), portable.Limitations...)
	limitations = append(limitations, "Archive creation completed without independent M5 verification; verification status is NOT_RUN.")
	sort.Strings(limitations)
	return Manifest{
		SchemaVersion: ArchiveSchemaVersion, AlihVersion: "0.0.1", Status: status,
		SourceSnapshotCompletedAt: evidence.FinishedAt, ArchiveCompletedAt: completedAt,
		Connector: evidence.Connector, Source: evidence.Workspace, ExtractedBy: extractedIdentity(evidence),
		InputSnapshot: InputSnapshot{LogicalDigest: evidence.LogicalDigest, Status: "COMPLETE", Atomic: false},
		Inventory:     inventory,
		Observed: map[string]int{
			"workspaces": 1, "identities": len(portable.Identities), "record_identity_roles": len(portable.RecordIdentities),
			"record_tags": len(portable.RecordTags), "record_field_values": len(portable.RecordFieldValues),
		},
		Capabilities: append([]connector.Capability(nil), portable.Capabilities...),
		Attachments:  attachments, Files: files, Limitations: limitations, Discrepancies: discrepancies,
		Verification: VerificationState{Status: "NOT_RUN", Note: "Independent M5 verification has not been implemented or executed."},
	}, nil
}

func manifestInventory(expected connector.Inventory, portable model.Archive) map[string]EntityCount {
	spaces, folders, tasks, subtasks := 0, 0, 0, 0
	for _, value := range portable.Containers {
		if value.Kind == "space" {
			spaces++
		} else if value.Kind == "folder" {
			folders++
		}
	}
	for _, value := range portable.Records {
		if value.Kind == "task" {
			tasks++
		} else if value.Kind == "subtask" {
			subtasks++
		}
	}
	retrieved := 0
	for _, value := range portable.Attachments {
		if value.DownloadStatus == "RETRIEVED" {
			retrieved++
		}
	}
	return map[string]EntityCount{
		"spaces":        {Expected: expected.Spaces, Archived: spaces},
		"folders":       {Expected: expected.Folders, Archived: folders},
		"lists":         {Expected: expected.Lists, Archived: len(portable.Collections)},
		"tasks":         {Expected: expected.Tasks, Archived: tasks},
		"subtasks":      {Expected: expected.Subtasks, Archived: subtasks},
		"comments":      {Expected: expected.Comments, Archived: len(portable.Comments)},
		"attachments":   {Expected: expected.Attachments, Archived: retrieved, Unresolved: expected.Attachments - retrieved},
		"custom_fields": {Expected: expected.CustomFields, Archived: len(portable.FieldDefinitions)},
		"relationships": {Expected: expected.Relationships, Archived: len(portable.Relationships)},
	}
}

func archiveStatus(attachments []model.Attachment) string {
	for _, attachment := range attachments {
		if attachment.DownloadStatus != "RETRIEVED" {
			return StatusIncomplete
		}
	}
	return StatusCreatedUnverified
}

func preserveFailedArchive(staging, target string, evidence snapshot.Evidence, portable model.Archive, cause error) (string, error) {
	manifest := Manifest{
		// A failed archive was never completed, so it records no completion
		// instant rather than borrowing one from the source snapshot.
		SchemaVersion: ArchiveSchemaVersion, AlihVersion: "0.0.1", Status: StatusFailed,
		SourceSnapshotCompletedAt: evidence.FinishedAt, ArchiveCompletedAt: nil,
		Connector: evidence.Connector, Source: evidence.Workspace, ExtractedBy: extractedIdentity(evidence),
		InputSnapshot: InputSnapshot{LogicalDigest: evidence.LogicalDigest, Status: "COMPLETE", Atomic: false},
		Capabilities:  append([]connector.Capability(nil), portable.Capabilities...),
		Limitations:   append([]string(nil), portable.Limitations...),
		Discrepancies: []Discrepancy{{Kind: "ARCHIVE_BUILD_FAILED", Message: sanitizedFailure(cause)}},
		Verification:  VerificationState{Status: "NOT_RUN", Note: "Archive construction failed before M5 verification."},
	}
	_ = writeJSON(filepath.Join(staging, "manifest.json"), manifest)
	failedPath := target + ".failed"
	if _, err := os.Lstat(failedPath); err == nil {
		return staging, fmt.Errorf("failed archive path already exists: %s", failedPath)
	}
	if err := os.Rename(staging, failedPath); err != nil {
		return staging, err
	}
	return failedPath, nil
}

func collectFileRecords(root string) ([]FileRecord, error) {
	var records []FileRecord
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("archive contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() == "manifest.json" && filepath.Dir(path) == root {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		records = append(records, FileRecord{Path: filepath.ToSlash(relative), Bytes: int64(len(content)), Checksum: "sha256:" + hex.EncodeToString(digest[:])})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	return records, nil
}

func copyRawTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("raw snapshot contains symlink %s", relative)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("raw snapshot contains non-regular file %s", relative)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputErr, outputErr := input.Close(), output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputErr != nil {
			return inputErr
		}
		return outputErr
	})
}

func pathWithin(root, candidate string) (bool, error) {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

func ensureTreeExcludesSecret(root, secret string) error {
	if secret == "" {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("raw snapshot contains a symlink")
		}
		contains, err := fileContains(path, []byte(secret))
		if err != nil {
			return err
		}
		if contains {
			return errors.New("raw snapshot contains the configured credential; archive creation refused")
		}
		return nil
	})
}

func fileContains(path string, needle []byte) (bool, error) {
	if len(needle) == 0 {
		return false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	buffer := make([]byte, 64*1024+len(needle)-1)
	carry := 0
	for {
		read, readErr := file.Read(buffer[carry:])
		total := carry + read
		if bytes.Contains(buffer[:total], needle) {
			return true, nil
		}
		if readErr == io.EOF {
			return false, nil
		}
		if readErr != nil {
			return false, readErr
		}
		carry = min(len(needle)-1, total)
		copy(buffer[:carry], buffer[total-carry:total])
	}
}

func safeExtension(filename *string) string {
	if filename == nil {
		return ""
	}
	extension := strings.ToLower(filepath.Ext(*filename))
	if len(extension) < 2 || len(extension) > 12 {
		return ""
	}
	for _, character := range extension[1:] {
		letter := character >= 'a' && character <= 'z'
		digit := character >= '0' && character <= '9'
		if !letter && !digit {
			return ""
		}
	}
	return extension
}

func writeJSON(path string, value any) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(path, content, 0o600)
}

func sleepWithContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func sanitizeAttachmentError(err error, credential string) string {
	if err == nil {
		return "attachment download failed"
	}
	message := err.Error()
	if credential != "" {
		message = strings.ReplaceAll(message, credential, "[REDACTED]")
	}
	if len(message) > 512 {
		message = message[:512]
	}
	return strings.Map(func(character rune) rune {
		if character < 0x20 {
			return ' '
		}
		return character
	}, message)
}

func sanitizedFailure(err error) string {
	if err == nil {
		return "archive construction failed"
	}
	message := err.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func stringPointer(value string) *string { return &value }
func valueOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}
