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

func TestLoadReadsClickUpTokenWithoutTransformingIt(t *testing.T) {
	t.Parallel()

	cfg, err := load(func(key string) (string, bool) {
		if key == clickUpTokenEnvironmentVariable {
			return "pk_exact_secret", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if !cfg.ClickUpTokenSet || cfg.ClickUpToken != "pk_exact_secret" {
		t.Fatal("ClickUp token was not loaded exactly")
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	t.Parallel()

	_, err := load(func(string) (string, bool) { return "verbose", true })
	if err == nil {
		t.Fatal("load() error = nil, want an invalid log level error")
	}
}
