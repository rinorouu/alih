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
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	// LogFilename is the current event log; RotatedFilename holds the previous
	// one. Two bounded files is the whole retention policy: disk usage has a
	// ceiling, the newest history is always intact, and what is dropped is
	// dropped explicitly rather than by silent truncation.
	LogFilename     = "events.jsonl"
	RotatedFilename = "events.1.jsonl"

	defaultMaxLogBytes = 1 << 20
	maxReadBytes       = 8 << 20
	maxLineBytes       = 64 << 10
)

// Sink receives events synchronously. There is no queue, no goroutine, and no
// background delivery: an operation that emits an event either recorded it or
// was told why it could not.
type Sink interface {
	Emit(Event) error
}

// MultiSink fans one event out to several sinks. Every sink is attempted even
// when an earlier one fails, so one broken consumer cannot hide a transition
// from the others; the failures are returned together.
type MultiSink []Sink

// Emit delivers to every sink and joins whatever went wrong.
func (sinks MultiSink) Emit(e Event) error {
	var failures []error
	for _, sink := range sinks {
		if sink == nil {
			continue
		}
		if err := sink.Emit(e); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// FileSink appends events to a bounded local log. Writes are append-only: an
// event that was recorded is never rewritten, and rotation drops whole files
// rather than editing one in place.
type FileSink struct {
	directory string
	maxBytes  int64
	mu        sync.Mutex
	fileOps   sinkFileOperations
}

// sinkFileOperations exists so tests can fail a write at the boundaries a real
// interrupted or full disk fails at.
type sinkFileOperations struct {
	open   func(path string) (*os.File, error)
	write  func(*os.File, []byte) (int, error)
	sync   func(*os.File) error
	rename func(oldPath, newPath string) error
}

func defaultSinkFileOperations() sinkFileOperations {
	return sinkFileOperations{
		open: func(path string) (*os.File, error) {
			return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		},
		write:  func(file *os.File, content []byte) (int, error) { return file.Write(content) },
		sync:   func(file *os.File) error { return file.Sync() },
		rename: os.Rename,
	}
}

// NewFileSink writes the log into directory. The directory is required rather
// than resolved from the environment, so no caller can start writing history
// somewhere it did not choose.
func NewFileSink(directory string) (*FileSink, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("event log directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve event log directory: %w", err)
	}
	return &FileSink{directory: absolute, maxBytes: defaultMaxLogBytes, fileOps: defaultSinkFileOperations()}, nil
}

// Directory returns where this sink writes, without creating anything.
func (sink *FileSink) Directory() string { return sink.directory }

// Path returns the current log file.
func (sink *FileSink) Path() string { return filepath.Join(sink.directory, LogFilename) }

// Emit appends one canonical line. A failure here is reported to the caller and
// never repaired by rewriting earlier history.
func (sink *FileSink) Emit(e Event) error {
	line, err := Marshal(e)
	if err != nil {
		return err
	}
	if len(line) > maxLineBytes {
		return fmt.Errorf("event exceeds %d bytes", maxLineBytes)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()

	if err := os.MkdirAll(sink.directory, 0o700); err != nil {
		return fmt.Errorf("create event log directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(sink.directory, 0o700); err != nil {
			return fmt.Errorf("secure event log directory: %w", err)
		}
	}
	if err := sink.rotateIfFull(int64(len(line))); err != nil {
		return err
	}

	path := sink.Path()
	file, err := sink.fileOps.open(path)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer file.Close()
	written, err := sink.fileOps.write(file, line)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	if written != len(line) {
		// A short write leaves a partial line. Readers already skip a line they
		// cannot parse, so the log stays usable; the caller is told the event
		// was not recorded.
		return fmt.Errorf("append event: wrote %d of %d bytes", written, len(line))
	}
	if err := sink.fileOps.sync(file); err != nil {
		return fmt.Errorf("sync event log: %w", err)
	}
	return nil
}

func (sink *FileSink) rotateIfFull(incoming int64) error {
	path := sink.Path()
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect event log: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("event log is not a regular file")
	}
	if info.Size()+incoming <= sink.maxBytes {
		return nil
	}
	if err := sink.fileOps.rename(path, filepath.Join(sink.directory, RotatedFilename)); err != nil {
		return fmt.Errorf("rotate event log: %w", err)
	}
	return nil
}

// Read returns the recorded events, oldest first, together with the number of
// lines that could not be read. A damaged line is skipped and counted, never
// repaired: the rest of the history stays available.
func Read(directory string) ([]Event, int, error) {
	var events []Event
	unreadable := 0
	for _, name := range []string{RotatedFilename, LogFilename} {
		fileEvents, skipped, err := readLogFile(filepath.Join(directory, name))
		if err != nil {
			return nil, 0, err
		}
		events = append(events, fileEvents...)
		unreadable += skipped
	}
	return Deduplicate(events), unreadable, nil
}

func readLogFile(path string) ([]Event, int, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("open event log: %w", err)
	}
	defer file.Close()

	var events []Event
	unreadable := 0
	scanner := bufio.NewScanner(io.LimitReader(file, maxReadBytes))
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		recorded, err := Unmarshal([]byte(line))
		if err != nil {
			unreadable++
			continue
		}
		events = append(events, recorded)
	}
	if err := scanner.Err(); err != nil {
		// A line too long or an unreadable tail is damage, not a reason to lose
		// everything that was read successfully.
		unreadable++
	}
	return events, unreadable, nil
}

// Latest returns the most recent events for one scope, newest first, with the
// count of failures among the events considered.
func Latest(events []Event, source Source, limit int) []Event {
	matching := make([]Event, 0, len(events))
	for _, candidate := range events {
		if candidate.Source == source {
			matching = append(matching, candidate)
		}
	}
	Order(matching)
	if limit > 0 && len(matching) > limit {
		matching = matching[len(matching)-limit:]
	}
	reversed := make([]Event, 0, len(matching))
	for index := len(matching) - 1; index >= 0; index-- {
		reversed = append(reversed, matching[index])
	}
	return reversed
}
