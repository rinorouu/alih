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

package event

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"alih/internal/state"
)

func newTestSink(t *testing.T) *FileSink {
	t.Helper()
	sink, err := NewFileSink(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("new sink: %v", err)
	}
	return sink
}

func TestEmitAppendsOnePrivateLinePerEvent(t *testing.T) {
	t.Parallel()
	sink := newTestSink(t)
	if err := sink.Emit(startedEvent()); err != nil {
		t.Fatalf("emit started: %v", err)
	}
	if err := sink.Emit(completedEvent()); err != nil {
		t.Fatalf("emit completed: %v", err)
	}

	content, err := os.ReadFile(sink.Path())
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("log has %d lines, want 2", len(lines))
	}
	events, unreadable, err := Read(sink.Directory())
	if err != nil || unreadable != 0 {
		t.Fatalf("read: %v (unreadable %d)", err, unreadable)
	}
	if len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 3 {
		t.Fatalf("events = %#v", events)
	}
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Lstat(sink.Path())
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log permissions = %04o, want 0600", info.Mode().Perm())
	}
	directoryInfo, err := os.Lstat(sink.Directory())
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory permissions = %04o, want 0700", directoryInfo.Mode().Perm())
	}
}

func TestRecordedHistoryIsNeverRewritten(t *testing.T) {
	t.Parallel()
	sink := newTestSink(t)
	if err := sink.Emit(startedEvent()); err != nil {
		t.Fatalf("emit: %v", err)
	}
	first, err := os.ReadFile(sink.Path())
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if err := sink.Emit(completedEvent()); err != nil {
		t.Fatalf("emit: %v", err)
	}
	second, err := os.ReadFile(sink.Path())
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.HasPrefix(string(second), string(first)) {
		t.Fatal("appending an event rewrote earlier history")
	}
}

func TestTheLogIsBoundedAndDropsWholeFilesNotLines(t *testing.T) {
	t.Parallel()
	sink := newTestSink(t)
	sink.maxBytes = 2048

	for index := 1; index <= 40; index++ {
		e := startedEvent()
		e.OperationID = fmt.Sprintf("20260901T0800%02dZ-0badc0de", index%100)
		e.Sequence = index
		if err := sink.Emit(e); err != nil {
			t.Fatalf("emit %d: %v", index, err)
		}
	}

	current, err := os.Stat(sink.Path())
	if err != nil {
		t.Fatalf("stat current log: %v", err)
	}
	if current.Size() > sink.maxBytes {
		t.Fatalf("current log is %d bytes, want at most %d", current.Size(), sink.maxBytes)
	}
	rotated, err := os.Stat(filepath.Join(sink.Directory(), RotatedFilename))
	if err != nil {
		t.Fatalf("stat rotated log: %v", err)
	}
	if rotated.Size() > sink.maxBytes {
		t.Fatalf("rotated log is %d bytes, want at most %d", rotated.Size(), sink.maxBytes)
	}
	entries, err := os.ReadDir(sink.Directory())
	if err != nil {
		t.Fatalf("read directory: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("event log grew to %d files, want a bounded two", len(entries))
	}

	events, unreadable, err := Read(sink.Directory())
	if err != nil || unreadable != 0 {
		t.Fatalf("read: %v (unreadable %d)", err, unreadable)
	}
	if len(events) == 0 {
		t.Fatal("rotation dropped the entire history")
	}
	// Whole files are dropped, so every surviving line is still a whole event.
	for _, recorded := range events {
		if err := Validate(recorded); err != nil {
			t.Fatalf("rotation left a damaged event: %v", err)
		}
	}
}

func TestAWriteFailureIsReportedAndNeverSilentlySwallowed(t *testing.T) {
	t.Parallel()
	failure := errors.New("disk is full")
	boundaries := []struct {
		name   string
		break_ func(*FileSink)
	}{
		{"open", func(sink *FileSink) {
			sink.fileOps.open = func(string) (*os.File, error) { return nil, failure }
		}},
		{"write", func(sink *FileSink) {
			sink.fileOps.write = func(*os.File, []byte) (int, error) { return 0, failure }
		}},
		{"sync", func(sink *FileSink) {
			sink.fileOps.sync = func(*os.File) error { return failure }
		}},
	}
	for _, boundary := range boundaries {
		boundary := boundary
		t.Run(boundary.name, func(t *testing.T) {
			t.Parallel()
			sink := newTestSink(t)
			boundary.break_(sink)
			if err := sink.Emit(startedEvent()); !errors.Is(err, failure) {
				t.Fatalf("emit error = %v, want the injected failure", err)
			}
		})
	}

	t.Run("partial write", func(t *testing.T) {
		t.Parallel()
		sink := newTestSink(t)
		sink.fileOps.write = func(file *os.File, content []byte) (int, error) {
			return file.Write(content[:len(content)/2])
		}
		err := sink.Emit(startedEvent())
		if err == nil || !strings.Contains(err.Error(), "wrote") {
			t.Fatalf("a partial write was reported as %v", err)
		}
		// The half-written line is damage the reader must survive, not repair.
		events, unreadable, readErr := Read(sink.Directory())
		if readErr != nil {
			t.Fatalf("read: %v", readErr)
		}
		if len(events) != 0 || unreadable != 1 {
			t.Fatalf("events = %#v, unreadable = %d", events, unreadable)
		}
	})
}

func TestADamagedLineNeverHidesTheRestOfTheHistory(t *testing.T) {
	t.Parallel()
	sink := newTestSink(t)
	if err := sink.Emit(startedEvent()); err != nil {
		t.Fatalf("emit: %v", err)
	}
	file, err := os.OpenFile(sink.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := file.WriteString("{ this line is not an event\n"); err != nil {
		t.Fatalf("write damage: %v", err)
	}
	file.Close()
	if err := sink.Emit(completedEvent()); err != nil {
		t.Fatalf("emit after damage: %v", err)
	}

	events, unreadable, err := Read(sink.Directory())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("readable events = %d, want the two undamaged ones", len(events))
	}
	if unreadable != 1 {
		t.Fatalf("unreadable lines = %d, want 1", unreadable)
	}
}

func TestConcurrentOperationsEachRecordEveryEvent(t *testing.T) {
	t.Parallel()
	sink := newTestSink(t)
	const operations = 8

	var group sync.WaitGroup
	for index := 0; index < operations; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			started := startedEvent()
			started.OperationID = fmt.Sprintf("20260901T0800%02dZ-0badc0de", index)
			completed := completedEvent()
			completed.OperationID = started.OperationID
			if err := sink.Emit(started); err != nil {
				t.Errorf("emit started: %v", err)
				return
			}
			if err := sink.Emit(completed); err != nil {
				t.Errorf("emit completed: %v", err)
			}
		}(index)
	}
	group.Wait()

	events, unreadable, err := Read(sink.Directory())
	if err != nil || unreadable != 0 {
		t.Fatalf("read: %v (unreadable %d)", err, unreadable)
	}
	if len(events) != operations*2 {
		t.Fatalf("recorded %d events, want %d", len(events), operations*2)
	}
	Order(events)
	for index := 0; index < len(events); index += 2 {
		if events[index].Sequence != 1 || events[index+1].Sequence != 3 {
			t.Fatalf("an operation's events are out of order: %#v", events[index:index+2])
		}
	}
}

func TestMultiSinkAttemptsEverySinkAndReportsEveryFailure(t *testing.T) {
	t.Parallel()
	first := errors.New("first sink failed")
	working := &countingSink{}
	sinks := MultiSink{&failingSink{err: first}, working, nil}

	err := sinks.Emit(startedEvent())
	if !errors.Is(err, first) {
		t.Fatalf("error = %v, want the failing sink reported", err)
	}
	if working.calls != 1 {
		t.Fatalf("a working sink received %d events; one broken consumer hid the transition", working.calls)
	}
}

type failingSink struct{ err error }

func (s *failingSink) Emit(Event) error { return s.err }

type countingSink struct {
	mu    sync.Mutex
	calls int
}

func (s *countingSink) Emit(Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return nil
}

func (s *countingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestLatestReturnsTheNewestEventsOfOneScope(t *testing.T) {
	t.Parallel()
	mine := []Event{startedEvent(), completedEvent()}
	other := startedEvent()
	other.Source.WorkspaceID = "200"
	other.OperationID = "20260901T090000Z-0000feed"

	latest := Latest(append(mine, other), testSource(), 1)
	if len(latest) != 1 || latest[0].Type != TypeOperationCompleted {
		t.Fatalf("latest = %#v", latest)
	}
	if unfiltered := Latest(append(mine, other), testSource(), 0); len(unfiltered) != 2 {
		t.Fatalf("unlimited latest = %#v", unfiltered)
	}
	if none := Latest(mine, Source{Connector: "clickup", WorkspaceID: "999", Destination: testAbsolutePath("x")}, 5); len(none) != 0 {
		t.Fatalf("a scope with no events returned %#v", none)
	}
}

func TestSinkRequiresADirectoryItWasGiven(t *testing.T) {
	t.Parallel()
	if _, err := NewFileSink("  "); err == nil {
		t.Fatal("a sink was created without being told where to write")
	}
	sink := newTestSink(t)
	if !strings.HasSuffix(sink.Path(), filepath.Join("state", LogFilename)) {
		t.Fatalf("log path = %q", sink.Path())
	}
	if state.SchemaVersion < 1 {
		t.Fatal("events reuse the operational state vocabulary")
	}
}

func TestHistorySurvivesARestartAndAReplayedAppend(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "state")
	first, err := NewFileSink(directory)
	if err != nil {
		t.Fatalf("new sink: %v", err)
	}
	if err := first.Emit(startedEvent()); err != nil {
		t.Fatalf("emit: %v", err)
	}

	// A new process opens the same log and continues where the last one stopped.
	second, err := NewFileSink(directory)
	if err != nil {
		t.Fatalf("new sink after restart: %v", err)
	}
	if err := second.Emit(completedEvent()); err != nil {
		t.Fatalf("emit after restart: %v", err)
	}
	// The same event is appended twice, as a replay would.
	if err := second.Emit(completedEvent()); err != nil {
		t.Fatalf("replayed emit: %v", err)
	}

	events, unreadable, err := Read(directory)
	if err != nil || unreadable != 0 {
		t.Fatalf("read: %v (unreadable %d)", err, unreadable)
	}
	if len(events) != 2 {
		t.Fatalf("history has %d events after a replay, want 2 distinct ones", len(events))
	}
	Order(events)
	if events[0].Sequence != 1 || events[1].Sequence != 3 {
		t.Fatalf("history lost its order across the restart: %#v", events)
	}
	// The replayed line is still on disk; deduplication happens on read, so no
	// recorded byte was ever rewritten.
	content, err := os.ReadFile(filepath.Join(directory, LogFilename))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Count(string(content), "\n") != 3 {
		t.Fatalf("the log was rewritten instead of appended to:\n%s", content)
	}
}

func TestQuotesAndMarkupSurviveEncodingAsOneLine(t *testing.T) {
	t.Parallel()
	e := startedEvent()
	e.Message = `The "backup" started <b>&</b> \ continued`
	line, err := Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Count(string(line), "\n") != 1 {
		t.Fatalf("encoded event is not one line: %q", line)
	}
	decoded, err := Unmarshal(line[:len(line)-1])
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Message != e.Message {
		t.Fatalf("message changed through encoding: %q", decoded.Message)
	}
}

// This test deliberately does not call t.Parallel(). runtime.NumGoroutine()
// counts the whole process, so running alongside this package's other parallel
// tests measured their goroutines rather than this one's and failed roughly
// two runs in five under -race.
func TestEmittingStartsNoBackgroundWork(t *testing.T) {
	sink := newTestSink(t)
	counter := &countingSink{}
	sinks := MultiSink{sink, counter}

	runtime.GC()
	before := runtime.NumGoroutine()
	for index := 0; index < 20; index++ {
		e := startedEvent()
		e.Sequence = index + 1
		if err := sinks.Emit(e); err != nil {
			t.Fatalf("emit %d: %v", index, err)
		}
		// The load-bearing assertion: by the time Emit returns, every sink has
		// already run. A sink handed to a background worker would not have.
		if calls := counter.count(); calls != index+1 {
			t.Fatalf("after emit %d the sink had been called %d times; delivery must be synchronous",
				index, calls)
		}
	}

	// A goroutine leak is a slower signal than the check above, so allow the
	// runtime a moment to retire anything transient before insisting on it.
	// A real background worker would still be resident when this gives up.
	var after int
	for attempt := 0; attempt < 50; attempt++ {
		runtime.GC()
		if after = runtime.NumGoroutine(); after <= before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("emitting left %d goroutines running; delivery must be synchronous", after-before)
}
