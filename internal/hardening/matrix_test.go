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

package hardening

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/event"
	"alih/internal/notify"
	"alih/internal/oplock"
	"alih/internal/organize"
	"alih/internal/report"
	"alih/internal/schedule"
	"alih/internal/state"
)

var (
	citedName    = regexp.MustCompile("`((?:Test|Fuzz)[A-Za-z0-9_]+)`")
	definedName  = regexp.MustCompile(`(?m)^func ((?:Test|Fuzz)[A-Za-z0-9_]+)\(`)
	requiredHead = regexp.MustCompile(`(?m)^## (\d+)\. `)
)

// repositoryRoot walks up from this package to the module root.
func repositoryRoot(t *testing.T) string {
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

func matrix(t *testing.T) string {
	t.Helper()
	return readDocument(t, "HARDENING.md")
}

func readDocument(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}

// definedTests collects every test and fuzz target in the repository.
func definedTests(t *testing.T) map[string]string {
	t.Helper()
	defined := make(map[string]string)
	root := repositoryRoot(t)
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
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, match := range definedName.FindAllStringSubmatch(string(content), -1) {
			defined[match[1]] = filepath.ToSlash(relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(defined) < 200 {
		t.Fatalf("only found %d tests; the walk is not seeing the repository", len(defined))
	}
	return defined
}

// TestEveryScenarioNamesTestsThatExist is what makes the matrix a claim rather
// than a comforting document. A renamed or deleted test breaks this test, which
// forces the matrix to be updated deliberately instead of decaying.
func TestEveryScenarioNamesTestsThatExist(t *testing.T) {
	t.Parallel()

	defined := definedTests(t)
	cited := make(map[string]struct{})
	for _, match := range citedName.FindAllStringSubmatch(matrix(t), -1) {
		cited[match[1]] = struct{}{}
	}
	if len(cited) < 100 {
		t.Fatalf("the matrix cites only %d tests; it is not covering the foundation", len(cited))
	}

	var missing []string
	for name := range cited {
		if _, ok := defined[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("HARDENING.md cites %s, which no longer exists", name)
	}
}

// TestTheMatrixCoversEveryRequiredScenario checks the matrix still has a
// section for each scenario the hardening stage requires, so a section cannot
// be dropped along with the coverage it documented.
func TestTheMatrixCoversEveryRequiredScenario(t *testing.T) {
	t.Parallel()

	document := matrix(t)
	required := []string{
		"Expired, revoked, or rejected credentials",
		"Rate limiting",
		"Endpoint removal and changed API behaviour",
		"Malformed and oversized responses",
		"Provider outage and network failure",
		"Partial extraction",
		"Attachment failure",
		"Interruption",
		"Disk full and unwritable storage",
		"Corruption and tampering",
		"Verification failure",
		"Notification failure",
		"Overlapping execution",
		"Process kill",
		"Version upgrade",
	}
	for _, scenario := range required {
		if !strings.Contains(document, scenario) {
			t.Errorf("the matrix has no section for %q", scenario)
		}
	}
	if numbered := requiredHead.FindAllStringSubmatch(document, -1); len(numbered) != len(required) {
		t.Errorf("the matrix has %d numbered scenarios, want %d", len(numbered), len(required))
	}
}

// TestEveryScenarioSectionCitesAtLeastOneTest catches the failure mode where a
// section survives but its evidence quietly does not.
func TestEveryScenarioSectionCitesAtLeastOneTest(t *testing.T) {
	t.Parallel()

	sections := strings.Split(matrix(t), "\n## ")
	for _, section := range sections[1:] {
		title := strings.SplitN(section, "\n", 2)[0]
		if strings.HasPrefix(title, "Known limits") || strings.HasPrefix(title, "What the matrix") {
			continue
		}
		if len(citedName.FindAllString(section, -1)) == 0 {
			t.Errorf("section %q cites no test", title)
		}
	}
}

// TestTheOperatorMatrixNamesTestsThatExist holds OPERATOR.md to the same
// standard as the hardening matrix. Its responsibility table claims that every
// operator job is covered by a public command and a named test; a renamed or
// deleted test must break the build rather than quietly turn the table into a
// story.
func TestTheOperatorMatrixNamesTestsThatExist(t *testing.T) {
	t.Parallel()

	defined := definedTests(t)
	document := readDocument(t, "OPERATOR.md")

	cited := make(map[string]struct{})
	for _, match := range citedName.FindAllStringSubmatch(document, -1) {
		cited[match[1]] = struct{}{}
	}
	if len(cited) < 15 {
		t.Fatalf("the operator matrix cites only %d tests; it is not covering the responsibilities", len(cited))
	}
	var missing []string
	for name := range cited {
		if _, ok := defined[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("OPERATOR.md cites %s, which no longer exists", name)
	}
}

// TestTheOperatorMatrixCoversEveryResponsibility checks the runbook still
// answers each responsibility the readiness review is required to cover.
func TestTheOperatorMatrixCoversEveryResponsibility(t *testing.T) {
	t.Parallel()

	document := readDocument(t, "OPERATOR.md")
	for _, responsibility := range []string{
		"Set up authentication",
		"Choose where backups are written",
		"Take a backup",
		"Verify an archive independently",
		"Browse recovered content",
		"Monitor an installation",
		"Diagnose a failure",
		"Schedule unattended runs",
		"Configure alerting",
		"Keep customers isolated",
		"Upgrade",
		"Offboard",
	} {
		if !strings.Contains(document, responsibility) {
			t.Errorf("the operator matrix has no row for %q", responsibility)
		}
	}
	// The decision itself must be stated, not implied.
	if !strings.Contains(document, "Readiness decision:") {
		t.Error("OPERATOR.md states no readiness decision")
	}
}

// TestProjectMemoryStatesTheRealSchemaVersions checks the orientation document
// against the constants it describes.
//
// A stale MEMORY.md is worse than no MEMORY.md: it is read by someone — or
// something — that has no other context, and it will be believed. The schema
// versions are the part most likely to drift and most damaging to get wrong,
// so they are checked rather than trusted.
func TestProjectMemoryStatesTheRealSchemaVersions(t *testing.T) {
	t.Parallel()

	document := readDocument(t, "MEMORY.md")
	rows := map[string]int{
		"`connector.CapabilitySchemaVersion`": connector.CapabilitySchemaVersion,
		"`connector.HealthSchemaVersion`":     connector.HealthSchemaVersion,
		"`archive.ArchiveSchemaVersion`":      archive.ArchiveSchemaVersion,
		"`archive.MinReadableSchemaVersion`":  archive.MinReadableSchemaVersion,
		"`report.SchemaVersion`":              report.SchemaVersion,
		"`organize.SchemaVersion`":            organize.SchemaVersion,
		"`state.SchemaVersion`":               state.SchemaVersion,
		"`event.SchemaVersion`":               event.SchemaVersion,
		"`notify.SchemaVersion`":              notify.SchemaVersion,
		"`schedule.SchemaVersion`":            schedule.SchemaVersion,
		"`oplock.SchemaVersion`":              oplock.SchemaVersion,
	}
	for constant, version := range rows {
		row := tableRowFor(document, constant)
		if row == "" {
			t.Errorf("MEMORY.md has no schema row for %s", constant)
			continue
		}
		if !strings.Contains(row, fmt.Sprintf("| %d |", version)) {
			t.Errorf("MEMORY.md states %q but %s is %d", strings.TrimSpace(row), constant, version)
		}
	}
}

// tableRowFor returns the Markdown table row naming constant.
func tableRowFor(document, constant string) string {
	for _, line := range strings.Split(document, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "|") && strings.Contains(line, constant) {
			return line
		}
	}
	return ""
}

// TestProjectMemoryNamesEveryPackage proves the repository map has not silently
// fallen behind the packages that actually exist. A map that omits a package is
// how a reader concludes the package is unimportant.
func TestProjectMemoryNamesEveryPackage(t *testing.T) {
	t.Parallel()

	document := readDocument(t, "MEMORY.md")
	entries, err := os.ReadDir(filepath.Join(repositoryRoot(t), "internal"))
	if err != nil {
		t.Fatal(err)
	}
	var packages int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		packages++
		if !strings.Contains(document, "internal/"+entry.Name()) {
			t.Errorf("MEMORY.md does not mention internal/%s", entry.Name())
		}
	}
	if packages < 20 {
		t.Fatalf("found only %d packages; the walk is not seeing the repository", packages)
	}
}
