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

package schedule

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestNativePlansAreDeterministicBoundedAndContainNoCredential(t *testing.T) {
	t.Parallel()
	definition := validDefinition()
	definition.WorkspaceID = `100 & "quoted"`
	definition.Destination = filepath.Join(definition.Destination, `%daily&<safe>`)
	home := filepath.Join(t.TempDir(), "home")
	executable := filepath.Join(home, "Alih Bin", "alih")
	configRoot := filepath.Join(home, ".config", "alih")
	for _, platform := range []string{PlatformLinux, PlatformDarwin, PlatformWindows} {
		platform := platform
		t.Run(platform, func(t *testing.T) {
			t.Parallel()
			first, err := Generate(definition, platform, executable, home, configRoot, "1000")
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			second, err := Generate(definition, platform, executable, home, configRoot, "1000")
			if err != nil || !reflect.DeepEqual(first, second) {
				t.Fatalf("plan is nondeterministic: %v\n%#v\n%#v", err, first, second)
			}
			if len(first.Artifacts) == 0 || len(first.Install) == 0 || len(first.Remove) == 0 {
				t.Fatalf("incomplete plan = %#v", first)
			}
			encoded, _ := json.Marshal(first)
			lower := strings.ToLower(string(encoded))
			for _, forbidden := range []string{"alih_clickup_token", "authorization", "bearer ", "credential.json", "pk_"} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("plan contains credential surface %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestSystemdPlanUsesNoShellAndEscapesSpecifiers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("systemd paths use POSIX semantics")
	}
	t.Parallel()
	definition := validDefinition()
	definition.Destination = "/srv/Alih % Backups/with spaces"
	plan, err := Generate(definition, PlatformLinux, "/opt/Alih Bin/alih", "/home/tester", "/home/tester/.config/alih", "1000")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	service := plan.Artifacts[0].Content
	for _, expected := range []string{`ExecStart="/opt/Alih Bin/alih" "backup"`, `"--workspace-id" "100"`, `Alih %% Backups`} {
		if !strings.Contains(service, expected) {
			t.Fatalf("service missing %q:\n%s", expected, service)
		}
	}
	for _, forbidden := range []string{"/bin/sh", "bash -c", "Environment=ALIH_CLICKUP_TOKEN"} {
		if strings.Contains(service, forbidden) {
			t.Fatalf("service contains %q:\n%s", forbidden, service)
		}
	}
}

func TestXMLPlansEscapeSourceAndPathText(t *testing.T) {
	t.Parallel()
	definition := validDefinition()
	definition.WorkspaceID = `100&<danger>`
	definition.Destination = filepath.Join(definition.Destination, `Alih & <Backups>`)
	home := filepath.Join(t.TempDir(), "home")
	executable := filepath.Join(home, "Alih&Tools", "alih")
	for _, platform := range []string{PlatformDarwin, PlatformWindows} {
		plan, err := Generate(definition, platform, executable, home, filepath.Join(home, ".config", "alih"), "1000")
		if err != nil {
			t.Fatalf("%s generate: %v", platform, err)
		}
		content := plan.Artifacts[0].Content
		if strings.Contains(content, `100&<danger>`) || strings.Contains(content, `Alih & <Backups>`) {
			t.Fatalf("%s XML contains unescaped text:\n%s", platform, content)
		}
		if !strings.Contains(content, `&amp;`) || !strings.Contains(content, `&lt;`) {
			t.Fatalf("%s XML did not escape text:\n%s", platform, content)
		}
	}
}

func TestWindowsQuotingHandlesSpacesQuotesAndTrailingBackslashes(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"plain":               "plain",
		"two words":           `"two words"`,
		`quoted"value`:        `"quoted\"value"`,
		`trailing backslash\`: `"trailing backslash\\"`,
	}
	for input, expected := range tests {
		if actual := windowsQuote(input); actual != expected {
			t.Fatalf("windowsQuote(%q) = %q, want %q", input, actual, expected)
		}
	}
}
