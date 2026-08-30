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
	logLevelEnvironmentVariable     = "ALIH_LOG_LEVEL"
	clickUpTokenEnvironmentVariable = "ALIH_CLICKUP_TOKEN"
)

// Config contains process-level settings that are safe to pass between Alih
// packages. ClickUpToken is never logged or rendered by this package.
type Config struct {
	LogLevel        slog.Level
	ClickUpToken    string
	ClickUpTokenSet bool
}

// Load reads configuration from the local process environment.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookupEnv func(string) (string, bool)) (Config, error) {
	cfg := Config{LogLevel: slog.LevelInfo}

	value, ok := lookupEnv(logLevelEnvironmentVariable)
	if ok && strings.TrimSpace(value) != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(strings.TrimSpace(value))); err != nil {
			return Config{}, fmt.Errorf("%s: %w", logLevelEnvironmentVariable, err)
		}
	}

	token, tokenSet := lookupEnv(clickUpTokenEnvironmentVariable)
	cfg.ClickUpToken = token
	cfg.ClickUpTokenSet = tokenSet

	return cfg, nil
}
