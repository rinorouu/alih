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
