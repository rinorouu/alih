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
	"go/build"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The claims these tests defend are the ones a person has no practical way to
// check for themselves before trusting Alih with a credential: that it talks to
// nothing except the source they pointed it at and the webhook they configured,
// that it reports nothing about them anywhere, and that no capability is held
// back for a paid or private build.
//
// They are structural rather than behavioural on purpose. A behavioural test
// proves a path was not taken during that test; these prove the path does not
// exist in the source at all.

// networkCapablePackages are the only packages allowed to reach the network,
// and each is allowed for one stated reason.
var networkCapablePackages = map[string]string{
	"internal/connector":         "defines the source-neutral request and response types",
	"internal/connector/clickup": "is the source connector; talking to the source is its purpose",
	"internal/archive":           "downloads the attachments the archive is required to contain",
	"internal/exporter":          "passes an HTTP client to the archive writer",
	"internal/notify":            "delivers the webhook notifications a user explicitly configured",
	"internal/sqliteutil":        "builds file: URIs for SQLite and makes no request",
	"internal/organize":          "reads a sealed archive and makes no request",
}

func goPackages(t *testing.T) map[string]*build.Package {
	t.Helper()
	root := repositoryRoot(t)
	packages := make(map[string]*build.Package)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if name == ".git" || name == "testdata" || name == ".github" {
			return filepath.SkipDir
		}
		parsed, err := build.ImportDir(path, build.ImportComment)
		if err != nil {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		packages[filepath.ToSlash(relative)] = parsed
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) < 10 {
		t.Fatalf("found only %d packages; the walk is not seeing the repository", len(packages))
	}
	return packages
}

// TestOnlyStatedPackagesCanReachTheNetwork proves the set of packages able to
// make a request is small, enumerated, and justified. A new one appearing here
// is a deliberate decision somebody has to make, not a drift.
func TestOnlyStatedPackagesCanReachTheNetwork(t *testing.T) {
	t.Parallel()

	networking := map[string]bool{"net/http": true, "net": true, "net/url": true}
	for name, parsed := range goPackages(t) {
		if name == "." || strings.HasPrefix(name, "cmd/") {
			// The composition root wires everything together by definition.
			continue
		}
		var reaches bool
		for _, imported := range parsed.Imports {
			if networking[imported] {
				reaches = true
			}
		}
		if !reaches {
			continue
		}
		if _, allowed := networkCapablePackages[name]; !allowed {
			t.Errorf("package %s can reach the network but is not in the stated set; "+
				"either it should not, or the reason belongs in networkCapablePackages", name)
		}
	}
}

// TestTheOnlyExternalHostIsTheSource proves no endpoint of Alih's own is
// compiled in: no telemetry collector, no update check, no licence server.
func TestTheOnlyExternalHostIsTheSource(t *testing.T) {
	t.Parallel()

	// Hosts that appear as XML or plist schema identifiers are never fetched;
	// they are namespace names required by the scheduler file formats.
	schemaNamespaces := map[string]bool{
		"http://www.apple.com/DTDs/PropertyList-1.0.dtd":        true,
		"http://schemas.microsoft.com/windows/2004/02/mit/task": true,
		"http://www.apache.org/licenses/LICENSE-2.0":            true,
		"https://github.com/rinorouu/alih":                      true,
		"https://api.clickup.com/api/v2":                        true,
	}
	pattern := regexp.MustCompile(`https?://[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]+`)

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
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
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
		for _, found := range pattern.FindAllString(string(content), -1) {
			trimmed := strings.TrimRight(found, `"'.,)`)
			if schemaNamespaces[trimmed] {
				continue
			}
			t.Errorf("%s compiles in the URL %q; Alih must contact nothing but the source "+
				"and an explicitly configured webhook", filepath.ToSlash(relative), trimmed)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestNoCapabilityIsGatedBehindAnEdition proves there is no licence check, no
// entitlement lookup, and no build tag that could produce a more capable
// private binary from this source.
func TestNoCapabilityIsGatedBehindAnEdition(t *testing.T) {
	t.Parallel()

	gating := regexp.MustCompile(`(?i)\b(entitlement|licen[cs]e[ _]?key|premium|paid[ _]?tier|subscription|activation[ _]?code|feature[ _]?flag|telemetry|analytics|phone[ _]?home)\b`)

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
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
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
		if match := gating.FindString(string(content)); match != "" {
			t.Errorf("%s mentions %q; Alih ships one build with one set of capabilities",
				filepath.ToSlash(relative), match)
		}
		// A build tag other than the platform splits could hide a private path.
		for _, line := range strings.Split(string(content), "\n") {
			if !strings.HasPrefix(line, "//go:build") {
				continue
			}
			constraint := strings.TrimSpace(strings.TrimPrefix(line, "//go:build"))
			switch constraint {
			case "windows", "!windows", "linux", "darwin", "unix", "!windows && !plan9":
			default:
				t.Errorf("%s carries the build constraint %q; only platform splits are expected",
					filepath.ToSlash(relative), constraint)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestNoDependencyCanReportOnTheUser proves the module's own dependency list
// stays as small as it is claimed to be, so trusting Alih does not mean
// trusting a transitive analytics library nobody reviewed.
func TestNoDependencyCanReportOnTheUser(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	// Every non-standard dependency Alih has, with the reason it exists.
	expected := map[string]string{
		"github.com/mattn/go-sqlite3": "the portable archive is a SQLite database",
	}
	requirement := regexp.MustCompile(`(?m)^\s*([a-z0-9.-]+\.[a-z]{2,}/[^\s]+)\s+v`)
	for _, match := range requirement.FindAllStringSubmatch(string(content), -1) {
		module := match[1]
		if _, ok := expected[module]; !ok {
			t.Errorf("go.mod requires %s, which is not in the reviewed dependency set", module)
		}
	}
}
