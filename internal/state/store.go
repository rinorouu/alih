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

package state

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	stateDirectory = "alih"
	stateSubdir    = "state"
	stateExtension = ".json"
	maxStateFile   = 256 * 1024
)

// Store failure identities. They are separate because they demand different
// human responses: nothing recorded yet is normal, a corrupt or future file is
// not, and a conflict means another operation wrote first.
var (
	// ErrNotRecorded means Alih has never written state for this scope. It is
	// an absence of evidence, never a statement about the source.
	ErrNotRecorded = errors.New("no operational state has been recorded")
	// ErrCorrupt means the stored file exists but cannot be trusted. Alih never
	// repairs or overwrites it on its own.
	ErrCorrupt = errors.New("operational state is unreadable")
	// ErrFutureSchema means the file was written by a newer Alih.
	ErrFutureSchema = errors.New("operational state was written by a newer Alih")
	// ErrConflict means the record changed since it was read, so writing would
	// discard another operation's update.
	ErrConflict = errors.New("operational state changed since it was read")
)

// Store persists one JSON document per scope. A document is replaced by an
// atomic rename, so a concurrent reader always sees either the previous record
// or the next one, never a partial write.
//
// Writers use a compare-and-set on Revision rather than a lock file. A crashed
// process therefore cannot leave a lock that permanently blocks every later
// write. Within one process the read-modify-write is serialized, so a revision
// is never lost. Across processes the compare-and-set narrows but does not
// close the window between reading and renaming: two simultaneous runs can
// cost one update. That failure understates what happened and never corrupts
// the file or invents a success, which is the direction Alih must fail in.
type Store struct {
	root string
	fs   fileOperations
	mu   sync.Mutex
}

// fileOperations exists so tests can fail a write at each boundary that a real
// interrupted process can fail at: create, write, sync, and rename.
type fileOperations struct {
	createTemp func(directory, pattern string) (*os.File, error)
	write      func(*os.File, []byte) (int, error)
	sync       func(*os.File) error
	rename     func(oldPath, newPath string) error
}

func defaultFileOperations() fileOperations {
	return fileOperations{
		createTemp: os.CreateTemp,
		write:      func(file *os.File, content []byte) (int, error) { return file.Write(content) },
		sync:       func(file *os.File) error { return file.Sync() },
		rename:     os.Rename,
	}
}

// NewStore returns a store rooted at root. An empty root resolves to the user
// configuration directory, beside the credential store, so that operational
// state follows the user rather than any single backup destination.
func NewStore(root string) (*Store, error) {
	resolved := strings.TrimSpace(root)
	if resolved == "" {
		directory, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("resolve user configuration directory: %w", err)
		}
		resolved = filepath.Join(directory, stateDirectory, stateSubdir)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	return &Store{root: absolute, fs: defaultFileOperations()}, nil
}

// Root returns the directory this store reads and writes, without creating it.
func (s *Store) Root() string { return s.root }

// Path returns the file a scope is stored in, without creating anything.
func (s *Store) Path(scope Scope) string {
	return filepath.Join(s.root, scope.Normalize().Key()+stateExtension)
}

// Load returns the recorded state for scope. A missing record is ErrNotRecorded;
// an unreadable one is ErrCorrupt or ErrFutureSchema and is left untouched.
func (s *Store) Load(scope Scope) (Record, error) {
	return s.loadPath(s.Path(scope))
}

func (s *Store) loadPath(path string) (Record, error) {
	if err := validateSecureDirectory(filepath.Dir(path)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Record{}, ErrNotRecorded
		}
		return Record{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, ErrNotRecorded
	}
	if err != nil {
		return Record{}, fmt.Errorf("inspect operational state: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Record{}, fmt.Errorf("%w: %s is not a regular file", ErrCorrupt, path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return Record{}, fmt.Errorf("%w: permissions %04o are too broad; require 0600", ErrCorrupt, info.Mode().Perm())
	}
	file, err := os.Open(path)
	if err != nil {
		return Record{}, fmt.Errorf("open operational state: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxStateFile+1))
	if err != nil {
		return Record{}, fmt.Errorf("read operational state: %w", err)
	}
	if len(content) > maxStateFile {
		return Record{}, fmt.Errorf("%w: file exceeds %d bytes", ErrCorrupt, maxStateFile)
	}
	record, err := Unmarshal(content)
	if err != nil {
		if errors.Is(err, ErrFutureSchema) {
			return Record{}, err
		}
		return Record{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return record, nil
}

// Save writes record as the next revision of its scope. The caller must pass
// the record it read: if the stored revision has moved on, or the stored file
// has become unreadable, the write is refused instead of discarding it.
func (s *Store) Save(record Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(record)
}

func (s *Store) save(record Record) (Record, error) {
	record.SchemaVersion = SchemaVersion
	Canonicalize(&record)
	if err := Validate(record); err != nil {
		return Record{}, err
	}
	path := s.Path(record.Scope)
	current, err := s.loadPath(path)
	switch {
	case err == nil:
		if current.Revision != record.Revision {
			return Record{}, fmt.Errorf("%w: stored revision %d, writing revision %d",
				ErrConflict, current.Revision, record.Revision)
		}
		if current.Scope != record.Scope {
			return Record{}, fmt.Errorf("%w: stored scope does not match %s", ErrCorrupt, path)
		}
	case errors.Is(err, ErrNotRecorded):
		if record.Revision != 0 {
			return Record{}, fmt.Errorf("%w: nothing is stored for revision %d", ErrConflict, record.Revision)
		}
	default:
		// A corrupt or future-schema file is never silently replaced.
		return Record{}, err
	}

	next := record
	next.Revision = record.Revision + 1
	content, err := Marshal(next)
	if err != nil {
		return Record{}, err
	}
	if err := s.writeAtomically(path, content); err != nil {
		return Record{}, err
	}
	return next, nil
}

// Update reads, mutates, and writes one record in a single call. mutate must
// treat an ErrNotRecorded record as a first write: it receives a zero record
// carrying only the scope.
func (s *Store) Update(scope Scope, mutate func(*Record) error) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.Load(scope)
	switch {
	case err == nil:
	case errors.Is(err, ErrNotRecorded):
		record = Record{SchemaVersion: SchemaVersion, Scope: scope.Normalize()}
	default:
		return Record{}, err
	}
	if mutate != nil {
		if err := mutate(&record); err != nil {
			return Record{}, err
		}
	}
	record.Scope = scope.Normalize()
	return s.save(record)
}

// List returns every readable record, sorted by scope, together with the paths
// that could not be read. An unreadable file never removes the others from view.
func (s *Store) List() ([]Record, []string, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read state directory: %w", err)
	}
	var records []Record
	var unreadable []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), stateExtension) || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(s.root, entry.Name())
		record, err := s.loadPath(path)
		if err != nil {
			if !errors.Is(err, ErrNotRecorded) {
				unreadable = append(unreadable, path)
			}
			continue
		}
		if record.Scope.Key()+stateExtension != entry.Name() {
			// The file name is derived from the scope; a mismatch means the file
			// was moved or renamed and its identity can no longer be trusted.
			unreadable = append(unreadable, path)
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Scope.Connector != records[j].Scope.Connector {
			return records[i].Scope.Connector < records[j].Scope.Connector
		}
		if records[i].Scope.WorkspaceID != records[j].Scope.WorkspaceID {
			return records[i].Scope.WorkspaceID < records[j].Scope.WorkspaceID
		}
		return records[i].Scope.Destination < records[j].Scope.Destination
	})
	sort.Strings(unreadable)
	return records, unreadable, nil
}

func (s *Store) writeAtomically(path string, content []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	// The directory is tightened before it is checked: Alih owns this directory,
	// and MkdirAll leaves an inherited umask in place on an existing path.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("secure state directory: %w", err)
		}
	}
	if err := validateSecureDirectory(directory); err != nil {
		return err
	}

	temporary, err := s.fs.createTemp(directory, ".state-*")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary state file: %w", err)
	}
	if _, err := s.fs.write(temporary, content); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	if err := s.fs.sync(temporary); err != nil {
		return fmt.Errorf("sync state file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close state file: %w", err)
	}
	if err := s.fs.rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit state file: %w", err)
	}
	committed = true
	syncDirectory(directory)
	return nil
}

// syncDirectory makes the rename itself durable where the platform supports it.
// A failure here does not invalidate the write that already succeeded.
func syncDirectory(directory string) {
	if runtime.GOOS == "windows" {
		return
	}
	handle, err := os.Open(directory)
	if err != nil {
		return
	}
	defer handle.Close()
	_ = handle.Sync()
}

func validateSecureDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: state directory path is not a directory", ErrCorrupt)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: state directory permissions %04o are too broad; require 0700", ErrCorrupt, info.Mode().Perm())
	}
	return nil
}

// Age reports how old an observation is at now. It exists so that callers can
// present staleness explicitly instead of implying a cached fact is current.
func Age(observedAt, now time.Time) time.Duration {
	return now.UTC().Sub(observedAt.UTC())
}
