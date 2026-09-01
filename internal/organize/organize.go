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

// Package organize builds a deterministic, disposable human-readable view of
// one independently verified Alih archive. The canonical archive is opened
// read-only and is verified again before the staged view is published.
package organize

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"alih/internal/archive"
	"alih/internal/buildinfo"
	"alih/internal/sqliteutil"
	"alih/internal/verify"
)

const SchemaVersion = 1

type Verifier interface {
	Verify(string) (verify.Report, error)
}

type Options struct {
	Verifier    Verifier
	AlihVersion string
	// BeforePublish is a test seam for interruption and disk-failure behavior.
	// Production callers leave it nil.
	BeforePublish func() error
}

type Service struct{ options Options }

func New(verifier Verifier, alihVersion string) *Service {
	return &Service{options: Options{Verifier: verifier, AlihVersion: alihVersion}}
}

func (service *Service) Build(ctx context.Context, archivePath, outputPath string) (Result, error) {
	if service == nil {
		return Result{}, errors.New("organized-view service is unavailable")
	}
	return Build(ctx, archivePath, outputPath, service.options)
}

type Result struct {
	SchemaVersion    int    `json:"schema_version"`
	OutputPath       string `json:"output_path"`
	Verification     string `json:"verification_result"`
	ManifestChecksum string `json:"manifest_checksum"`
	Files            int    `json:"files"`
	Attachments      int    `json:"attachments"`
}

type provenanceDocument struct {
	SchemaVersion    int               `json:"schema_version"`
	Kind             string            `json:"kind"`
	GeneratedBy      string            `json:"generated_by"`
	ManifestChecksum string            `json:"manifest_checksum"`
	Verification     string            `json:"verification_result"`
	Limitations      []string          `json:"limitations"`
	Entries          []provenanceEntry `json:"entries"`
}

type provenanceEntry struct {
	Path               string `json:"path"`
	PortableID         string `json:"portable_id"`
	SourceProvider     string `json:"source_provider"`
	SourceType         string `json:"source_type"`
	SourceID           string `json:"source_id"`
	SourceRawPath      string `json:"source_raw_path"`
	AttachmentChecksum string `json:"attachment_checksum,omitempty"`
}

type sourceRef struct {
	Provider string
	Type     string
	ID       string
	RawPath  string
}

type namedEntity struct {
	ID       string
	Kind     string
	Name     *string
	ParentID *string
	Source   sourceRef
}

type hierarchy struct {
	Container       namedEntity
	ParentContainer *namedEntity
	Collection      *namedEntity
}

type record struct {
	ID, Kind, WorkspaceID, CollectionID string
	ParentRecordID                      *string
	Title, Description, TextContent     *string
	Status, StatusType, Priority        *string
	Archived                            *bool
	Created, Updated, Closed, Done      *string
	Start, Due                          *string
	EstimateMS, SpentMS                 *int64
	Points                              *float64
	Source                              sourceRef
}

// Build creates output as a new immutable-by-convention derived tree. Existing
// output is never merged, repaired, or replaced.
func Build(ctx context.Context, archivePath, outputPath string, options Options) (result Result, err error) {
	if options.Verifier == nil {
		return Result{}, errors.New("organized view requires an independent verifier")
	}
	archiveRoot, output, err := validateRoots(archivePath, outputPath)
	if err != nil {
		return Result{}, err
	}
	report, err := options.Verifier.Verify(archiveRoot)
	if err != nil {
		return Result{}, fmt.Errorf("verify archive before organization: %w", err)
	}
	if report.Result != verify.ResultVerified && report.Result != verify.ResultVerifiedWithLimitations {
		return Result{}, fmt.Errorf("refuse to organize archive with verification result %s", safeValue(report.Result))
	}
	_, manifest, manifestChecksum, err := readManifest(archiveRoot)
	if err != nil {
		return Result{}, err
	}
	database, err := openReadOnly(filepath.Join(archiveRoot, "alih.db"))
	if err != nil {
		return Result{}, fmt.Errorf("open verified archive database: %w", err)
	}
	defer database.Close()

	parent := filepath.Dir(output)
	if err := prepareParent(parent); err != nil {
		return Result{}, err
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(output)+".partial-")
	if err != nil {
		return Result{}, fmt.Errorf("create organized-view staging directory: %w", err)
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		_ = os.Remove(staging)
		return Result{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()

	builder := viewBuilder{
		ctx: ctx, archiveRoot: archiveRoot, root: staging, database: database,
		manifest: manifest, report: report, manifestChecksum: manifestChecksum,
		version: buildinfo.Resolve(options.AlihVersion),
	}
	if err := builder.build(); err != nil {
		return Result{}, err
	}
	if options.BeforePublish != nil {
		if err := options.BeforePublish(); err != nil {
			return Result{}, fmt.Errorf("before organized-view publication: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	secondReport, err := options.Verifier.Verify(archiveRoot)
	if err != nil || (secondReport.Result != verify.ResultVerified && secondReport.Result != verify.ResultVerifiedWithLimitations) {
		if err != nil {
			return Result{}, fmt.Errorf("verify archive before publication: %w", err)
		}
		return Result{}, fmt.Errorf("archive no longer verifies before publication: %s", safeValue(secondReport.Result))
	}
	_, _, secondChecksum, err := readManifest(archiveRoot)
	if err != nil || secondChecksum != manifestChecksum {
		if err != nil {
			return Result{}, fmt.Errorf("re-read archive manifest before publication: %w", err)
		}
		return Result{}, errors.New("archive manifest changed while organized view was being built")
	}
	if err := syncDirectory(staging); err != nil {
		return Result{}, fmt.Errorf("sync organized-view staging directory: %w", err)
	}
	if err := os.Rename(staging, output); err != nil {
		return Result{}, fmt.Errorf("publish organized view: %w", err)
	}
	published = true
	_ = syncDirectory(parent)
	return Result{
		SchemaVersion: SchemaVersion, OutputPath: output, Verification: report.Result,
		ManifestChecksum: manifestChecksum, Files: builder.files, Attachments: builder.attachments,
	}, nil
}

type viewBuilder struct {
	ctx              context.Context
	archiveRoot      string
	root             string
	database         *sql.DB
	manifest         archive.Manifest
	report           verify.Report
	manifestChecksum string
	version          string
	entryFile        *os.File
	entryWriter      *bufio.Writer
	entryCount       int
	files            int
	attachments      int
	workspacePath    string
}

func (builder *viewBuilder) build() error {
	entryFile, err := os.OpenFile(filepath.Join(builder.root, ".provenance-entries.partial"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	builder.entryFile = entryFile
	builder.entryWriter = bufio.NewWriterSize(entryFile, 32*1024)
	defer func() {
		if builder.entryFile != nil {
			_ = builder.entryFile.Close()
		}
	}()
	workspace, err := builder.workspace()
	if err != nil {
		return err
	}
	builder.workspacePath = component(value(workspace.Name, "Workspace"), workspace.ID)
	readme := builder.rootReadme(workspace)
	if err := builder.write("README.md", []byte(readme), entryFor("README.md", workspace)); err != nil {
		return err
	}
	workspaceIndex := filepath.ToSlash(filepath.Join(builder.workspacePath, "index.md"))
	if err := builder.write(workspaceIndex, []byte(builder.entityIndex("Workspace", workspace, nil)), entryFor(workspaceIndex, workspace)); err != nil {
		return err
	}
	if err := builder.buildContainers(); err != nil {
		return err
	}
	if err := builder.buildCollections(); err != nil {
		return err
	}
	if err := builder.buildRecords(); err != nil {
		return err
	}
	return builder.writeProvenance()
}

func (builder *viewBuilder) workspace() (namedEntity, error) {
	var result namedEntity
	row := builder.database.QueryRowContext(builder.ctx, `SELECT id,name,source_provider,source_type,source_id,source_raw_path FROM workspaces ORDER BY id LIMIT 1`)
	if err := row.Scan(&result.ID, &result.Name, &result.Source.Provider, &result.Source.Type, &result.Source.ID, &result.Source.RawPath); err != nil {
		return namedEntity{}, fmt.Errorf("read workspace: %w", err)
	}
	return result, nil
}

func (builder *viewBuilder) buildContainers() error {
	rows, err := builder.database.QueryContext(builder.ctx, `SELECT c.id,c.kind,c.name,c.parent_id,c.source_provider,c.source_type,c.source_id,c.source_raw_path,
		p.id,p.kind,p.name,p.parent_id,p.source_provider,p.source_type,p.source_id,p.source_raw_path
		FROM containers c LEFT JOIN containers p ON p.id=c.parent_id ORDER BY c.id`)
	if err != nil {
		return fmt.Errorf("read containers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := builder.ctx.Err(); err != nil {
			return err
		}
		hierarchy, err := scanHierarchy(rows, false)
		if err != nil {
			return err
		}
		path := filepath.ToSlash(filepath.Join(builder.containerPath(hierarchy), "index.md"))
		if err := builder.write(path, []byte(builder.entityIndex(titleWord(hierarchy.Container.Kind), hierarchy.Container, hierarchy.ParentContainer)), entryFor(path, hierarchy.Container)); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (builder *viewBuilder) buildCollections() error {
	rows, err := builder.database.QueryContext(builder.ctx, `SELECT x.id,x.name,x.source_provider,x.source_type,x.source_id,x.source_raw_path,
		c.id,c.kind,c.name,c.parent_id,c.source_provider,c.source_type,c.source_id,c.source_raw_path,
		p.id,p.kind,p.name,p.parent_id,p.source_provider,p.source_type,p.source_id,p.source_raw_path
		FROM collections x JOIN containers c ON c.id=x.container_id LEFT JOIN containers p ON p.id=c.parent_id ORDER BY x.id`)
	if err != nil {
		return fmt.Errorf("read collections: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := builder.ctx.Err(); err != nil {
			return err
		}
		hierarchy, err := scanCollectionHierarchy(rows)
		if err != nil {
			return err
		}
		path := filepath.ToSlash(filepath.Join(builder.collectionPath(hierarchy), "index.md"))
		if err := builder.write(path, []byte(builder.entityIndex("Collection", *hierarchy.Collection, &hierarchy.Container)), entryFor(path, *hierarchy.Collection)); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (builder *viewBuilder) buildRecords() error {
	rows, err := builder.database.QueryContext(builder.ctx, `SELECT r.id,r.kind,r.workspace_id,r.collection_id,r.parent_record_id,r.title,r.description,r.text_content,
		r.status,r.status_type,r.priority,r.archived,r.date_created_source,r.date_updated_source,r.date_closed_source,r.date_done_source,r.start_date_source,r.due_date_source,
		r.time_estimate_ms,r.time_spent_ms,r.points,r.source_provider,r.source_type,r.source_id,r.source_raw_path,
		x.id,x.name,x.source_provider,x.source_type,x.source_id,x.source_raw_path,
		c.id,c.kind,c.name,c.parent_id,c.source_provider,c.source_type,c.source_id,c.source_raw_path,
		p.id,p.kind,p.name,p.parent_id,p.source_provider,p.source_type,p.source_id,p.source_raw_path
		FROM records r JOIN collections x ON x.id=r.collection_id JOIN containers c ON c.id=x.container_id
		LEFT JOIN containers p ON p.id=c.parent_id ORDER BY r.id`)
	if err != nil {
		return fmt.Errorf("read records: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := builder.ctx.Err(); err != nil {
			return err
		}
		item, hierarchy, err := scanRecordHierarchy(rows)
		if err != nil {
			return err
		}
		directory := filepath.Join(builder.collectionPath(hierarchy), "records", component(value(item.Title, item.Kind), item.ID))
		path := filepath.ToSlash(filepath.Join(directory, "record.md"))
		content, err := builder.recordMarkdown(item)
		if err != nil {
			return err
		}
		if err := builder.write(path, []byte(content), provenanceEntry{Path: path, PortableID: item.ID, SourceProvider: item.Source.Provider, SourceType: item.Source.Type, SourceID: item.Source.ID, SourceRawPath: item.Source.RawPath}); err != nil {
			return err
		}
		if err := builder.copyAttachments(item.ID, directory); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (builder *viewBuilder) writeProvenance() error {
	if err := builder.entryWriter.Flush(); err != nil {
		return err
	}
	if _, err := builder.entryFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	document := provenanceDocument{
		SchemaVersion: SchemaVersion, Kind: "alih_organized_view", GeneratedBy: builder.version,
		ManifestChecksum: builder.manifestChecksum, Verification: builder.report.Result,
		Limitations: append([]string(nil), builder.report.Limitations...),
	}
	sort.Strings(document.Limitations)
	header := struct {
		SchemaVersion    int      `json:"schema_version"`
		Kind             string   `json:"kind"`
		GeneratedBy      string   `json:"generated_by"`
		ManifestChecksum string   `json:"manifest_checksum"`
		Verification     string   `json:"verification_result"`
		Limitations      []string `json:"limitations"`
	}{document.SchemaVersion, document.Kind, document.GeneratedBy, document.ManifestChecksum, document.Verification, document.Limitations}
	encoded, err := json.MarshalIndent(header, "", "  ")
	if err != nil {
		return err
	}
	output, err := os.OpenFile(filepath.Join(builder.root, "provenance.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	writer := bufio.NewWriterSize(output, 32*1024)
	headerPrefix := strings.TrimRight(strings.TrimSuffix(string(encoded), "}"), " \n\t")
	if _, err = writer.WriteString(headerPrefix); err == nil {
		_, err = writer.WriteString(",\n  \"entries\": [\n")
	}
	reader := bufio.NewReaderSize(builder.entryFile, 32*1024)
	for index := 0; err == nil; index++ {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(line, "\n")
			if index > 0 {
				_, err = writer.WriteString(",\n")
			}
			if err == nil {
				_, err = writer.WriteString("    " + line)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			err = readErr
		}
	}
	if err == nil {
		_, err = writer.WriteString("\n  ]\n}\n")
	}
	if err == nil {
		err = writer.Flush()
	}
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := builder.entryFile.Close(); err != nil {
		return err
	}
	builder.entryFile = nil
	if err := os.Remove(filepath.Join(builder.root, ".provenance-entries.partial")); err != nil {
		return err
	}
	builder.files++
	return nil
}

func (builder *viewBuilder) write(relative string, content []byte, entry provenanceEntry) error {
	if err := builder.writeWithoutEntry(relative, content); err != nil {
		return err
	}
	entry.Path = filepath.ToSlash(relative)
	return builder.recordEntry(entry)
}

func (builder *viewBuilder) recordEntry(entry provenanceEntry) error {
	if builder.entryWriter == nil {
		return errors.New("provenance stream is unavailable")
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := builder.entryWriter.Write(encoded); err != nil {
		return err
	}
	if err := builder.entryWriter.WriteByte('\n'); err != nil {
		return err
	}
	builder.entryCount++
	return nil
}

func (builder *viewBuilder) writeWithoutEntry(relative string, content []byte) error {
	if err := builder.ctx.Err(); err != nil {
		return err
	}
	if !safeRelative(relative) {
		return fmt.Errorf("refuse unsafe organized-view path %q", relative)
	}
	path := filepath.Join(builder.root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(content)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	builder.files++
	return nil
}

func (builder *viewBuilder) copyAttachments(recordID, recordDirectory string) error {
	rows, err := builder.database.QueryContext(builder.ctx, `SELECT id,filename,download_status,local_path,checksum,source_provider,source_type,source_id,source_raw_path
		FROM attachments WHERE record_id=? ORDER BY id`, recordID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, status string
		var filename, localPath, checksum *string
		var source sourceRef
		if err := rows.Scan(&id, &filename, &status, &localPath, &checksum, &source.Provider, &source.Type, &source.ID, &source.RawPath); err != nil {
			return err
		}
		if status != "RETRIEVED" || localPath == nil || checksum == nil {
			return fmt.Errorf("verified archive contains unresolved attachment %s", id)
		}
		name := attachmentComponent(value(filename, "attachment"), id)
		relative := filepath.ToSlash(filepath.Join(recordDirectory, "attachments", name))
		if !safeRelative(*localPath) {
			return fmt.Errorf("attachment %s records unsafe archive path", id)
		}
		if err := builder.copyAttachmentFile(filepath.Join(builder.archiveRoot, filepath.FromSlash(*localPath)), relative, *checksum); err != nil {
			return fmt.Errorf("copy attachment %s: %w", id, err)
		}
		if err := builder.recordEntry(provenanceEntry{
			Path: relative, PortableID: id, SourceProvider: source.Provider, SourceType: source.Type,
			SourceID: source.ID, SourceRawPath: source.RawPath, AttachmentChecksum: *checksum,
		}); err != nil {
			return err
		}
		builder.files++
		builder.attachments++
	}
	return rows.Err()
}

func (builder *viewBuilder) copyAttachmentFile(source, relative, expected string) error {
	if !safeRelative(relative) {
		return errors.New("unsafe attachment output path")
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("attachment source is not a regular archive file")
	}
	destination := filepath.Join(builder.root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(output, hash), &contextReader{ctx: builder.ctx, reader: input})
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return fmt.Errorf("checksum %s does not match %s", actual, expected)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func openReadOnly(path string) (*sql.DB, error) {
	database, err := sql.Open("sqlite3", sqliteutil.FileURI(path)+"?mode=ro&immutable=1&_query_only=1")
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(4)
	if err := database.Ping(); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func readManifest(root string) ([]byte, archive.Manifest, string, error) {
	content, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return nil, archive.Manifest{}, "", fmt.Errorf("read verified manifest: %w", err)
	}
	var manifest archive.Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return nil, archive.Manifest{}, "", fmt.Errorf("decode verified manifest: %w", err)
	}
	digest := sha256.Sum256(content)
	return content, manifest, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validateRoots(archivePath, outputPath string) (string, string, error) {
	if strings.TrimSpace(archivePath) == "" || strings.TrimSpace(outputPath) == "" {
		return "", "", errors.New("archive and output paths are required")
	}
	archiveRoot, err := filepath.Abs(archivePath)
	if err != nil {
		return "", "", err
	}
	output, err := filepath.Abs(outputPath)
	if err != nil {
		return "", "", err
	}
	info, err := os.Lstat(archiveRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("archive path must be a real directory")
	}
	if err := rejectSymlinkComponents(archiveRoot); err != nil {
		return "", "", fmt.Errorf("archive path traversal: %w", err)
	}
	if _, err := os.Lstat(output); err == nil {
		return "", "", fmt.Errorf("organized-view output already exists: %s", output)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", "", err
	}
	if within(archiveRoot, output) || within(output, archiveRoot) {
		return "", "", errors.New("organized-view output and canonical archive must not contain one another")
	}
	return archiveRoot, output, nil
}

func prepareParent(path string) error {
	ancestor := filepath.Clean(path)
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return err
		}
		ancestor = parent
	}
	if err := rejectSymlinkComponents(ancestor); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create organized-view parent: %w", err)
	}
	return rejectSymlinkComponents(path)
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func rejectSymlinkComponents(path string) error {
	current := filepath.Clean(path)
	for {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("organized-view parent contains a symlink or non-directory component: %s", current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func safeRelative(path string) bool {
	clean := filepath.Clean(filepath.FromSlash(path))
	return path != "" && !filepath.IsAbs(clean) && clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

func safeValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "UNKNOWN"
	}
	return value
}

func titleWord(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) == 0 {
		return "Container"
	}
	upper := []rune(strings.ToUpper(string(runes[0])))
	if len(upper) > 0 {
		runes[0] = upper[0]
	}
	return string(runes)
}
