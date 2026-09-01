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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"alih/internal/organize"
)

type stubOrganizer struct {
	result      organize.Result
	err         error
	archive     string
	output      string
	calls       int
	cancellable bool
}

func (o *stubOrganizer) Build(ctx context.Context, archivePath, outputPath string) (organize.Result, error) {
	o.calls++
	o.archive, o.output = archivePath, outputPath
	o.cancellable = ctx.Done() != nil
	return o.result, o.err
}

func organizeApp(t *testing.T, organizer organizedViewBuilder) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	app := New(stdout, stderr, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Organizer: organizer})
	return app, stdout, stderr
}

func successfulOrganizer() *stubOrganizer {
	return &stubOrganizer{result: organize.Result{
		SchemaVersion: organize.SchemaVersion, OutputPath: "/tmp/view",
		Verification: "VERIFIED_WITH_LIMITATIONS", ManifestChecksum: "sha256:abc",
		Files: 9, Attachments: 1,
	}}
}

func TestOrganizeReportsThePublishedView(t *testing.T) {
	t.Parallel()
	organizer := successfulOrganizer()
	app, stdout, stderr := organizeApp(t, organizer)

	if code := app.Run([]string{"organize", "--archive", "/tmp/archive", "--output", "/tmp/view"}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if organizer.archive != "/tmp/archive" || organizer.output != "/tmp/view" {
		t.Fatalf("organizer received archive=%q output=%q", organizer.archive, organizer.output)
	}
	// Generation must observe interruption so an interrupted run removes its
	// staging directory instead of leaving a partial view behind.
	if !organizer.cancellable {
		t.Error("organization was given a context that can never be cancelled")
	}
	for _, expected := range []string{
		"Organized view: /tmp/view",
		"Verification: VERIFIED_WITH_LIMITATIONS",
		"Manifest checksum: sha256:abc",
		"Files: 9 (1 attachments)",
		"The canonical archive was not modified.",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("stdout is missing %q:\n%s", expected, stdout.String())
		}
	}
}

func TestOrganizeJSONKeepsStdoutMachineReadable(t *testing.T) {
	t.Parallel()
	app, stdout, stderr := organizeApp(t, successfulOrganizer())

	if code := app.Run([]string{"organize", "--archive", "/tmp/archive", "--output", "/tmp/view", "--json"}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var decoded organize.Result
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout.String())
	}
	if decoded.SchemaVersion != organize.SchemaVersion || decoded.Files != 9 || decoded.Attachments != 1 {
		t.Fatalf("decoded = %+v", decoded)
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON mode wrote diagnostics to stderr: %q", stderr.String())
	}
}

func TestOrganizeFailureSaysNothingWasPublished(t *testing.T) {
	t.Parallel()
	organizer := &stubOrganizer{err: errors.New("refuse to organize archive with verification result INCOMPLETE")}
	app, stdout, stderr := organizeApp(t, organizer)

	if code := app.Run([]string{"organize", "--archive", "/tmp/archive", "--output", "/tmp/view", "--json"}); code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("a failed organization wrote to stdout: %q", stdout.String())
	}
	for _, expected := range []string{"INCOMPLETE", "no organized view was published", "canonical archive was not modified"} {
		if !strings.Contains(stderr.String(), expected) {
			t.Errorf("stderr is missing %q:\n%s", expected, stderr.String())
		}
	}
}

func TestOrganizeRejectsUnusableInvocations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
	}{
		{"no arguments", []string{"organize"}},
		{"missing output", []string{"organize", "--archive", "/tmp/archive"}},
		{"missing archive", []string{"organize", "--output", "/tmp/view"}},
		{"blank archive", []string{"organize", "--archive", "  ", "--output", "/tmp/view"}},
		{"positional argument", []string{"organize", "--archive", "/tmp/archive", "--output", "/tmp/view", "extra"}},
		{"unknown flag", []string{"organize", "--archive", "/tmp/archive", "--output", "/tmp/view", "--force"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			organizer := successfulOrganizer()
			app, _, _ := organizeApp(t, organizer)
			if code := app.Run(testCase.args); code != 2 {
				t.Fatalf("code=%d, want 2", code)
			}
			if organizer.calls != 0 {
				t.Fatal("a usage error still started an organization")
			}
		})
	}
}

func TestOrganizeWithoutDependenciesFailsClosed(t *testing.T) {
	t.Parallel()
	app, stdout, stderr := organizeApp(t, nil)

	if code := app.Run([]string{"organize", "--archive", "/tmp/archive", "--output", "/tmp/view"}); code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "unavailable") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestOrganizeHelpDocumentsTheContract(t *testing.T) {
	t.Parallel()
	organizer := successfulOrganizer()
	app, stdout, _ := organizeApp(t, organizer)

	if code := app.Run([]string{"organize", "--help"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	for _, expected := range []string{
		"VERIFIED_WITH_LIMITATIONS", "INCOMPLETE", "must not already exist", "not a\nrestore source",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("help is missing %q:\n%s", expected, stdout.String())
		}
	}
	if organizer.calls != 0 {
		t.Fatal("--help started an organization")
	}
}
