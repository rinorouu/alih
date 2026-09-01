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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	// The store creates its own directory, exactly as it does for a real user.
	store, err := NewStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func storedRecord(t *testing.T) Record {
	t.Helper()
	record := testRecord(t)
	record.Revision = 0
	return record
}

func TestFirstSaveCreatesAPrivateRecord(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	saved, err := store.Save(storedRecord(t))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Revision != 1 {
		t.Fatalf("revision = %d, want 1", saved.Revision)
	}
	loaded, err := store.Load(testScope())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Revision != 1 || loaded.Scope != testScope() || loaded.WorkspaceName != "Example Workspace" {
		t.Fatalf("loaded record = %#v", loaded)
	}
	if runtime.GOOS == "windows" {
		return
	}
	fileInfo, err := os.Lstat(store.Path(testScope()))
	if err != nil {
		t.Fatalf("stat record: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("record permissions = %04o, want 0600", fileInfo.Mode().Perm())
	}
	directoryInfo, err := os.Lstat(store.Root())
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory permissions = %04o, want 0700", directoryInfo.Mode().Perm())
	}
}

func TestSaveRefusesToDiscardAnotherWritersRevision(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	first, err := store.Save(storedRecord(t))
	if err != nil {
		t.Fatalf("first save: %v", err)
	}

	stale := storedRecord(t)
	stale.WorkspaceName = "Stale writer"
	_, err = store.Save(stale)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save error = %v, want ErrConflict", err)
	}

	current, err := store.Load(testScope())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if current.Revision != first.Revision || current.WorkspaceName != "Example Workspace" {
		t.Fatalf("refused write still changed the record: %#v", current)
	}

	current.WorkspaceName = "Second writer"
	next, err := store.Save(current)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if next.Revision != 2 {
		t.Fatalf("revision = %d, want 2", next.Revision)
	}
}

func TestUpdateWritesTheFirstRecordAndThenAdvancesIt(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	scope := testScope()
	template := storedRecord(t)

	created, err := store.Update(scope, func(record *Record) error {
		*record = template
		return nil
	})
	if err != nil {
		t.Fatalf("first update: %v", err)
	}
	if created.Revision != 1 {
		t.Fatalf("first revision = %d, want 1", created.Revision)
	}

	updated, err := store.Update(scope, func(record *Record) error {
		record.WorkspaceName = "Renamed at the source"
		record.UpdatedAt = testEnd.Add(time.Minute)
		return nil
	})
	if err != nil {
		t.Fatalf("second update: %v", err)
	}
	if updated.Revision != 2 || updated.WorkspaceName != "Renamed at the source" {
		t.Fatalf("updated record = %#v", updated)
	}

	failure := errors.New("mutation refused")
	if _, err := store.Update(scope, func(*Record) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("update error = %v, want the mutation error", err)
	}
	if current, err := store.Load(scope); err != nil || current.Revision != 2 {
		t.Fatalf("refused mutation changed the record: %#v (%v)", current, err)
	}
}

func TestLoadReportsNothingRecordedRatherThanFailure(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	if _, err := store.Load(testScope()); !errors.Is(err, ErrNotRecorded) {
		t.Fatalf("load error = %v, want ErrNotRecorded", err)
	}
	empty, err := NewStore(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := empty.Load(testScope()); !errors.Is(err, ErrNotRecorded) {
		t.Fatalf("missing directory error = %v, want ErrNotRecorded", err)
	}
}

func TestUnreadableStateIsNeverRepairedOrOverwritten(t *testing.T) {
	t.Parallel()
	valid, err := Marshal(func() Record {
		record := storedRecord(t)
		record.Revision = 1
		return record
	}())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	tests := []struct {
		name    string
		content []byte
		want    error
	}{
		{"truncated", valid[:len(valid)/2], ErrCorrupt},
		{"not json", []byte("this is not state\n"), ErrCorrupt},
		{"future schema", []byte(strings.Replace(string(valid), `"schema_version": 3`, `"schema_version": 99`, 1)), ErrFutureSchema},
		{"oversized", append([]byte("{\"schema_version\":1,\"pad\":\""), append([]byte(strings.Repeat("p", maxStateFile)), []byte("\"}")...)...), ErrCorrupt},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newTestStore(t)
			path := store.Path(testScope())
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("create directory: %v", err)
			}
			if err := os.WriteFile(path, test.content, 0o600); err != nil {
				t.Fatalf("write state: %v", err)
			}
			if _, err := store.Load(testScope()); !errors.Is(err, test.want) {
				t.Fatalf("load error = %v, want %v", err, test.want)
			}
			if _, err := store.Save(storedRecord(t)); !errors.Is(err, test.want) {
				t.Fatalf("save error = %v, want %v", err, test.want)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(after) != string(test.content) {
				t.Fatal("unreadable state was overwritten")
			}
		})
	}
}

func TestTooPermissiveOrIrregularStateIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission and symlink semantics are not enforced on Windows")
	}
	t.Parallel()

	t.Run("file", func(t *testing.T) {
		t.Parallel()
		store := newTestStore(t)
		if _, err := store.Save(storedRecord(t)); err != nil {
			t.Fatalf("save: %v", err)
		}
		if err := os.Chmod(store.Path(testScope()), 0o644); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if _, err := store.Load(testScope()); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("load error = %v, want ErrCorrupt", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		t.Parallel()
		store := newTestStore(t)
		if _, err := store.Save(storedRecord(t)); err != nil {
			t.Fatalf("save: %v", err)
		}
		if err := os.Chmod(store.Root(), 0o755); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if _, err := store.Load(testScope()); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("load error = %v, want ErrCorrupt", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		store := newTestStore(t)
		elsewhere := filepath.Join(t.TempDir(), "elsewhere.json")
		content, err := Marshal(func() Record {
			record := storedRecord(t)
			record.Revision = 1
			return record
		}())
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(elsewhere, content, 0o600); err != nil {
			t.Fatalf("write target: %v", err)
		}
		if err := os.MkdirAll(store.Root(), 0o700); err != nil {
			t.Fatalf("create directory: %v", err)
		}
		if err := os.Symlink(elsewhere, store.Path(testScope())); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if _, err := store.Load(testScope()); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("load error = %v, want ErrCorrupt", err)
		}
	})
}

func TestInterruptedWriteLeavesThePreviousRecordIntact(t *testing.T) {
	t.Parallel()
	boundaries := []struct {
		name   string
		break_ func(*Store, error)
	}{
		{"create", func(store *Store, failure error) {
			store.fs.createTemp = func(string, string) (*os.File, error) { return nil, failure }
		}},
		{"write", func(store *Store, failure error) {
			store.fs.write = func(*os.File, []byte) (int, error) { return 0, failure }
		}},
		{"sync", func(store *Store, failure error) {
			store.fs.sync = func(*os.File) error { return failure }
		}},
		{"rename", func(store *Store, failure error) {
			store.fs.rename = func(string, string) error { return failure }
		}},
	}
	for _, boundary := range boundaries {
		boundary := boundary
		t.Run(boundary.name, func(t *testing.T) {
			t.Parallel()
			store := newTestStore(t)
			first, err := store.Save(storedRecord(t))
			if err != nil {
				t.Fatalf("first save: %v", err)
			}
			before, err := os.ReadFile(store.Path(testScope()))
			if err != nil {
				t.Fatalf("read record: %v", err)
			}

			failure := errors.New("interrupted at " + boundary.name)
			boundary.break_(store, failure)
			next := first
			next.WorkspaceName = "Never committed"
			if _, err := store.Save(next); !errors.Is(err, failure) {
				t.Fatalf("save error = %v, want the injected failure", err)
			}

			after, err := os.ReadFile(store.Path(testScope()))
			if err != nil {
				t.Fatalf("read record: %v", err)
			}
			if string(after) != string(before) {
				t.Fatal("an interrupted write changed the committed record")
			}
			entries, err := os.ReadDir(store.Root())
			if err != nil {
				t.Fatalf("read directory: %v", err)
			}
			if len(entries) != 1 {
				names := make([]string, 0, len(entries))
				for _, entry := range entries {
					names = append(names, entry.Name())
				}
				t.Fatalf("interrupted write left %v behind", names)
			}
		})
	}
}

func TestConcurrentUpdatesNeverProduceAnUnreadableRecord(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	scope := testScope()
	template := storedRecord(t)
	if _, err := store.Update(scope, func(record *Record) error {
		*record = template
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const writers = 8
	var group sync.WaitGroup
	results := make([]error, writers)
	for index := 0; index < writers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_, results[index] = store.Update(scope, func(record *Record) error {
				record.UpdatedAt = testEnd.Add(time.Duration(index+1) * time.Minute)
				return nil
			})
		}(index)
	}
	group.Wait()

	succeeded := 0
	for index, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrConflict):
		default:
			t.Fatalf("writer %d failed unexpectedly: %v", index, err)
		}
	}
	if succeeded == 0 {
		t.Fatal("no concurrent writer made progress")
	}
	final, err := store.Load(scope)
	if err != nil {
		t.Fatalf("load after concurrency: %v", err)
	}
	if final.Revision != uint64(succeeded)+1 {
		t.Fatalf("revision = %d, want %d successful writes plus the seed", final.Revision, succeeded)
	}
}

func TestListSeparatesReadableRecordsFromUnreadableFiles(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	first := storedRecord(t)
	if _, err := store.Save(first); err != nil {
		t.Fatalf("save first: %v", err)
	}
	second := storedRecord(t)
	second.Scope.WorkspaceID = "9002"
	if _, err := store.Save(second); err != nil {
		t.Fatalf("save second: %v", err)
	}

	if err := os.WriteFile(filepath.Join(store.Root(), "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write broken record: %v", err)
	}
	content, err := os.ReadFile(store.Path(first.Scope))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.Root(), "moved.json"), content, 0o600); err != nil {
		t.Fatalf("write moved record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.Root(), ".hidden.json"), content, 0o600); err != nil {
		t.Fatalf("write hidden record: %v", err)
	}

	records, unreadable, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("readable records = %d, want 2", len(records))
	}
	if records[0].Scope.WorkspaceID != "9001" || records[1].Scope.WorkspaceID != "9002" {
		t.Fatalf("records are not ordered by scope: %#v", records)
	}
	if len(unreadable) != 2 {
		t.Fatalf("unreadable files = %v, want the broken and moved records", unreadable)
	}
}

func TestDefaultStoreLivesBesideTheUserConfiguration(t *testing.T) {
	t.Parallel()
	store, err := NewStore("")
	if err != nil {
		t.Skipf("this environment has no user configuration directory: %v", err)
	}
	want := filepath.Join(stateDirectory, stateSubdir)
	if !strings.HasSuffix(store.Root(), want) {
		t.Fatalf("default root = %q, want it to end with %q", store.Root(), want)
	}
}

func TestReadersNeverObserveAPartiallyWrittenRecord(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	scope := testScope()
	template := storedRecord(t)
	if _, err := store.Update(scope, func(record *Record) error {
		*record = template
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Readers deliberately do not take the writer lock: the guarantee under test
	// is the atomic rename, not mutual exclusion inside one process.
	reader, err := NewStore(store.Root())
	if err != nil {
		t.Fatalf("new reader store: %v", err)
	}

	done := make(chan struct{})
	var readerGroup sync.WaitGroup
	readerGroup.Add(1)
	go func() {
		defer readerGroup.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			record, err := reader.Load(scope)
			if err != nil {
				t.Errorf("reader observed unreadable state: %v", err)
				return
			}
			if err := Validate(record); err != nil {
				t.Errorf("reader observed an invalid record: %v", err)
				return
			}
		}
	}()

	for index := 0; index < 50; index++ {
		if _, err := store.Update(scope, func(record *Record) error {
			record.UpdatedAt = testEnd.Add(time.Duration(index) * time.Second)
			return nil
		}); err != nil {
			t.Fatalf("write %d: %v", index, err)
		}
	}
	close(done)
	readerGroup.Wait()
}
