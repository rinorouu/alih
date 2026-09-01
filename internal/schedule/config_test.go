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
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func validDefinition() Definition {
	destination := filepath.Join(string(filepath.Separator), "srv", "Alih Backups")
	if runtime.GOOS == "windows" {
		destination = `C:\srv\Alih Backups`
	}
	return Definition{
		ID: "daily-main", Enabled: true, Operation: OperationBackup, Connector: "clickup",
		WorkspaceID: "100", Destination: destination,
		Cadence: Cadence{Frequency: FrequencyDaily, At: "02:30", Timezone: TimezoneLocal, MissedRunPolicy: MissedRunOnce},
	}
}

func validConfig() Config {
	return Config{SchemaVersion: SchemaVersion, Schedules: []Definition{validDefinition()}}
}

func TestScheduleConfigurationIsStrictCanonicalAndPrivate(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "alih")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := `{"schema_version":1,"schedules":[{"id":"daily-main","enabled":true,"operation":"backup","connector":"clickup","workspace_id":"100","destination":"` + filepath.ToSlash(validDefinition().Destination) + `","cadence":{"frequency":"daily","at":"02:30","timezone":"local","missed_run_policy":"run_once"}}]}`
	if err := os.WriteFile(filepath.Join(root, configName), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	config, err := Load(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(config.Enabled()) != 1 || config.Enabled()[0].ID != "daily-main" {
		t.Fatalf("config = %#v", config)
	}
	if _, err := Parse([]byte(strings.Replace(content, `"id":"daily-main"`, `"id":"daily-main","token":"secret"`, 1))); err == nil {
		t.Fatal("unknown credential-shaped field was accepted")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(root, configName), 0o644); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if _, err := Load(root); !errors.Is(err, ErrInsecure) {
			t.Fatalf("insecure config error = %v", err)
		}
	}
}

func TestScheduleValidationRejectsAmbiguousOrUnportableDefinitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"future schema", func(c *Config) { c.SchemaVersion = 2 }},
		{"duplicate id", func(c *Config) { c.Schedules = append(c.Schedules, c.Schedules[0]) }},
		{"unsafe id", func(c *Config) { c.Schedules[0].ID = "../daily" }},
		{"unknown operation", func(c *Config) { c.Schedules[0].Operation = "restore" }},
		{"missing workspace", func(c *Config) { c.Schedules[0].WorkspaceID = "" }},
		{"relative destination", func(c *Config) { c.Schedules[0].Destination = "backups" }},
		{"cron language", func(c *Config) { c.Schedules[0].Cadence.Frequency = "*/5 * * * *" }},
		{"bad civil time", func(c *Config) { c.Schedules[0].Cadence.At = "25:61" }},
		{"nonportable timezone", func(c *Config) { c.Schedules[0].Cadence.Timezone = "Asia/Jakarta" }},
		{"nonportable missed policy", func(c *Config) { c.Schedules[0].Cadence.MissedRunPolicy = "skip" }},
		{"unknown missed policy", func(c *Config) { c.Schedules[0].Cadence.MissedRunPolicy = "queue_all" }},
		{"control character", func(c *Config) { c.Schedules[0].WorkspaceID = "100\nmalicious" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := validConfig()
			test.mutate(&config)
			if err := Validate(config); err == nil {
				t.Fatal("invalid schedule configuration was accepted")
			}
		})
	}
}

func TestNextScheduleIncludesExactCivilTime(t *testing.T) {
	t.Parallel()
	definition := validDefinition()
	now := time.Date(2026, 9, 1, 2, 30, 0, 0, time.Local)
	if next := definition.Next(now); !next.Equal(now) {
		t.Fatalf("next = %s, want %s", next, now)
	}
}

func TestNextScheduleUsesLocalCivilTimeAndMovesForward(t *testing.T) {
	t.Parallel()
	definition := validDefinition()
	now := time.Date(2026, 9, 1, 3, 0, 0, 0, time.Local)
	next := definition.Next(now)
	if !next.After(now) || next.Hour() != 2 || next.Minute() != 30 {
		t.Fatalf("next = %s", next)
	}
}
