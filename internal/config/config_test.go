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

package config

import (
	"log/slog"
	"testing"
)

func TestLoadDefaultsToInfoLogging(t *testing.T) {
	t.Parallel()

	cfg, err := load(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelInfo)
	}
}

func TestLoadReadsLogLevel(t *testing.T) {
	t.Parallel()

	cfg, err := load(func(key string) (string, bool) {
		if key == logLevelEnvironmentVariable {
			return "debug", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelDebug)
	}
}

// TestCredentialVariableIsDerivedFromTheConnector proves Core names no
// provider while still reading the variable ClickUp installations already use.
func TestCredentialVariableIsDerivedFromTheConnector(t *testing.T) {
	t.Parallel()

	for connectorName, want := range map[string]string{
		"clickup":  "ALIH_CLICKUP_TOKEN",
		"my-saas":  "ALIH_MY_SAAS_TOKEN",
		"a.b c":    "ALIH_A_B_C_TOKEN",
		"digits99": "ALIH_DIGITS99_TOKEN",
	} {
		if got := CredentialEnvironmentVariable(connectorName); got != want {
			t.Errorf("CredentialEnvironmentVariable(%q) = %q, want %q", connectorName, got, want)
		}
	}
}

func TestCredentialFromEnvironmentReadsTheTokenWithoutTransformingIt(t *testing.T) {
	t.Parallel()

	token, ok := credentialFromEnvironment(func(key string) (string, bool) {
		if key == "ALIH_CLICKUP_TOKEN" {
			return "pk_exact_secret", true
		}
		return "", false
	}, "clickup")
	if !ok || token != "pk_exact_secret" {
		t.Fatalf("credential = %q set=%v; the token was not read exactly", token, ok)
	}

	// An unset variable and an explicitly empty one are different answers.
	if _, ok := credentialFromEnvironment(func(string) (string, bool) { return "", false }, "clickup"); ok {
		t.Error("an absent variable reported as set")
	}
	if value, ok := credentialFromEnvironment(func(string) (string, bool) { return "", true }, "clickup"); !ok || value != "" {
		t.Error("an empty variable must stay distinguishable from an absent one")
	}
	if _, ok := credentialFromEnvironment(func(string) (string, bool) { return "x", true }, "  "); ok {
		t.Error("a blank connector name must read no variable at all")
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	t.Parallel()

	_, err := load(func(string) (string, bool) { return "verbose", true })
	if err == nil {
		t.Fatal("load() error = nil, want an invalid log level error")
	}
}
