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

// Package config loads Alih's local process configuration.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

const (
	logLevelEnvironmentVariable  = "ALIH_LOG_LEVEL"
	saveCredentialEnvironmentVar = "ALIH_SAVE_CREDENTIAL"
)

// Config contains process-level settings that are safe to pass between Alih
// packages. No credential is stored here: a credential belongs to whichever
// connector is wired, so it is read by name through CredentialFromEnvironment
// rather than carried as a field Core would have to name after one provider.
type Config struct {
	LogLevel slog.Level
	// SaveCredential reports whether a credential supplied through the
	// environment may also be written to the local credential store. It
	// defaults to true, which is what a person setting Alih up once expects.
	// An unattended installation whose credential is injected per run — from a
	// secrets manager, a CI secret, or an ephemeral container — can set
	// ALIH_SAVE_CREDENTIAL=0 so the credential stays where its owner put it.
	SaveCredential bool
}

// Load reads configuration from the local process environment.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookupEnv func(string) (string, bool)) (Config, error) {
	cfg := Config{LogLevel: slog.LevelInfo, SaveCredential: true}

	value, ok := lookupEnv(logLevelEnvironmentVariable)
	if ok && strings.TrimSpace(value) != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(strings.TrimSpace(value))); err != nil {
			return Config{}, fmt.Errorf("%s: %w", logLevelEnvironmentVariable, err)
		}
	}

	if value, ok := lookupEnv(saveCredentialEnvironmentVar); ok {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "0", "false", "no", "off", "never":
			cfg.SaveCredential = false
		case "", "1", "true", "yes", "on", "always":
			cfg.SaveCredential = true
		default:
			return Config{}, fmt.Errorf("%s: %q is not a boolean value", saveCredentialEnvironmentVar, value)
		}
	}

	return cfg, nil
}

// CredentialEnvironmentVariable is the process environment variable that
// supplies one connector's credential.
//
// The name is derived from the connector rather than listed in Core, so adding
// a connector adds its variable automatically and Core never has to name a
// provider. ClickUp resolves to ALIH_CLICKUP_TOKEN, which is the variable Alih
// has always documented and continues to read unchanged.
func CredentialEnvironmentVariable(connectorName string) string {
	var name strings.Builder
	name.WriteString("ALIH_")
	for _, character := range strings.ToUpper(strings.TrimSpace(connectorName)) {
		switch {
		case character >= 'A' && character <= 'Z', character >= '0' && character <= '9':
			name.WriteRune(character)
		default:
			name.WriteByte('_')
		}
	}
	name.WriteString("_TOKEN")
	return name.String()
}

// CredentialFromEnvironment reads the credential a connector's variable holds.
// The second result reports whether the variable was set at all, which is how
// an explicitly empty value stays distinguishable from an absent one.
func CredentialFromEnvironment(connectorName string) (string, bool) {
	return credentialFromEnvironment(os.LookupEnv, connectorName)
}

func credentialFromEnvironment(lookupEnv func(string) (string, bool), connectorName string) (string, bool) {
	if strings.TrimSpace(connectorName) == "" {
		return "", false
	}
	return lookupEnv(CredentialEnvironmentVariable(connectorName))
}
