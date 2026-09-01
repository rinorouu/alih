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

// Package notify delivers selected operational events to a destination the
// user configured on purpose.
//
// Alih sends nothing unless a configuration file exists, is readable only by
// its owner, and names both a destination and the exact event types that
// destination should receive. There is no default endpoint, no hosted relay,
// and no configuration that arrives from anywhere but the local disk.
//
// A destination secret is never stored here: the configuration names the
// environment variable that holds it, so the file itself, the operational
// state, the event log, and any future scheduler definition contain a
// reference and never a value.
package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"alih/internal/event"
)

// SchemaVersion identifies the notification configuration contract.
const SchemaVersion = 1

// TypeWebhook is the one transport Alih implements. A local command hook was
// considered and rejected: it would add arbitrary code execution to a tool
// whose purpose is protecting data, and it behaves differently on every
// supported operating system.
const TypeWebhook = "webhook"

const (
	configDirectory = "alih"
	configFilename  = "notifications.json"

	maxConfigBytes   = 64 * 1024
	maxIDBytes       = 64
	maxURLBytes      = 2048
	maxDestinations  = 8
	maxEventsPerDest = 16

	// Delivery bounds. They exist so that a slow or hostile destination cannot
	// hold a backup open or make Alih retry forever.
	defaultTimeout  = 10 * time.Second
	minTimeout      = time.Second
	maxTimeout      = 60 * time.Second
	defaultAttempts = 3
	maxAttempts     = 5
)

// Configuration failures are separate identities because they demand different
// responses: nothing configured is the normal, silent state.
var (
	// ErrNotConfigured means no configuration exists. Alih stays silent.
	ErrNotConfigured = errors.New("no notification destination is configured")
	// ErrInsecureConfig means the file exists but is not private enough or is
	// not a regular file. Alih refuses to read it rather than lower the bar.
	ErrInsecureConfig = errors.New("notification configuration is not private")
)

// Destination is one place selected events are sent to.
type Destination struct {
	ID      string   `json:"id"`
	Enabled bool     `json:"enabled"`
	Type    string   `json:"type"`
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	// SecretEnv names the environment variable holding the bearer token, if the
	// destination needs one. It is a name, never a value.
	SecretEnv      string `json:"secret_env,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	MaxAttempts    int    `json:"max_attempts,omitempty"`
}

// Config is the complete local notification configuration.
type Config struct {
	SchemaVersion int           `json:"schema_version"`
	Destinations  []Destination `json:"destinations"`
}

// Timeout returns the bounded per-attempt timeout for this destination.
func (d Destination) Timeout() time.Duration {
	if d.TimeoutSeconds <= 0 {
		return defaultTimeout
	}
	timeout := time.Duration(d.TimeoutSeconds) * time.Second
	if timeout < minTimeout {
		return minTimeout
	}
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}

// Attempts returns the bounded number of delivery attempts.
func (d Destination) Attempts() int {
	if d.MaxAttempts <= 0 {
		return defaultAttempts
	}
	if d.MaxAttempts > maxAttempts {
		return maxAttempts
	}
	return d.MaxAttempts
}

// Wants reports whether this destination asked for that event type. Selection
// is an explicit allowlist of stable type names, not a filtering language.
func (d Destination) Wants(eventType event.Type) bool {
	if !d.Enabled {
		return false
	}
	for _, selected := range d.Events {
		if selected == string(eventType) {
			return true
		}
	}
	return false
}

// SafeURL renders a destination for humans without exposing what a URL can
// carry. Some webhook providers put the token in the path, and a query or
// fragment can carry anything, so only the scheme and host are ever shown.
func (d Destination) SafeURL() string {
	parsed, err := url.Parse(d.URL)
	if err != nil || parsed.Host == "" {
		return "(unreadable destination URL)"
	}
	return parsed.Scheme + "://" + parsed.Host + "/…"
}

// Enabled returns the destinations that are switched on, in a stable order.
func (c Config) EnabledDestinations() []Destination {
	enabled := make([]Destination, 0, len(c.Destinations))
	for _, destination := range c.Destinations {
		if destination.Enabled {
			enabled = append(enabled, destination)
		}
	}
	sort.Slice(enabled, func(i, j int) bool { return enabled[i].ID < enabled[j].ID })
	return enabled
}

// Path returns the configuration file location without creating anything.
func Path(root string) (string, error) {
	if trimmed := strings.TrimSpace(root); trimmed != "" {
		absolute, err := filepath.Abs(trimmed)
		if err != nil {
			return "", fmt.Errorf("resolve notification configuration directory: %w", err)
		}
		return filepath.Join(absolute, configFilename), nil
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user configuration directory: %w", err)
	}
	return filepath.Join(directory, configDirectory, configFilename), nil
}

// Load reads the configuration. A missing file is ErrNotConfigured, which is
// the ordinary state of an installation that never asked to be notified.
func Load(root string) (Config, error) {
	path, err := Path(root)
	if err != nil {
		return Config{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, ErrNotConfigured
	}
	if err != nil {
		return Config{}, fmt.Errorf("inspect notification configuration: %w", err)
	}
	directoryInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return Config{}, fmt.Errorf("inspect notification configuration directory: %w", err)
	}
	if !directoryInfo.IsDir() {
		return Config{}, fmt.Errorf("%w: configuration parent is not a directory", ErrInsecureConfig)
	}
	if runtime.GOOS != "windows" && directoryInfo.Mode().Perm()&0o077 != 0 {
		return Config{}, fmt.Errorf("%w: directory permissions %04o are too broad; require 0700",
			ErrInsecureConfig, directoryInfo.Mode().Perm())
	}
	if !info.Mode().IsRegular() {
		return Config{}, fmt.Errorf("%w: %s is not a regular file", ErrInsecureConfig, path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return Config{}, fmt.Errorf("%w: permissions %04o are too broad; require 0600",
			ErrInsecureConfig, info.Mode().Perm())
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open notification configuration: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("read notification configuration: %w", err)
	}
	if len(content) > maxConfigBytes {
		return Config{}, fmt.Errorf("notification configuration exceeds %d bytes", maxConfigBytes)
	}
	return Parse(content)
}

// Parse decodes and validates configuration content strictly. An unknown field
// is refused rather than ignored, so a typo cannot silently disable delivery.
func Parse(content []byte) (Config, error) {
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("notification configuration could not be decoded: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("notification configuration contains trailing content")
	}
	if err := Validate(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate rejects configuration that is ambiguous, unsafe, or that would send
// somewhere Alih cannot send safely.
func Validate(config Config) error {
	if config.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported notification schema version %d", config.SchemaVersion)
	}
	if len(config.Destinations) > maxDestinations {
		return fmt.Errorf("more than %d destinations are configured", maxDestinations)
	}
	seen := make(map[string]struct{}, len(config.Destinations))
	for index, destination := range config.Destinations {
		if err := validateDestination(destination); err != nil {
			return fmt.Errorf("destination %d: %w", index, err)
		}
		if _, duplicate := seen[destination.ID]; duplicate {
			return fmt.Errorf("duplicate destination id %q", destination.ID)
		}
		seen[destination.ID] = struct{}{}
	}
	return nil
}

func validateDestination(destination Destination) error {
	if err := validateIdentifier("id", destination.ID); err != nil {
		return err
	}
	if destination.Type != TypeWebhook {
		return fmt.Errorf("unsupported destination type %q", destination.Type)
	}
	if err := validateWebhookURL(destination.URL); err != nil {
		return err
	}
	if len(destination.Events) == 0 {
		return errors.New("no event types are selected, so nothing would ever be sent")
	}
	if len(destination.Events) > maxEventsPerDest {
		return fmt.Errorf("more than %d event types are selected", maxEventsPerDest)
	}
	selectedEvents := make(map[string]struct{}, len(destination.Events))
	for _, selected := range destination.Events {
		if !event.KnownType(event.Type(selected)) {
			return fmt.Errorf("unknown event type %q", selected)
		}
		if event.Type(selected) == event.TypeNotificationProblem {
			return errors.New("notification.problem cannot notify itself")
		}
		if _, duplicate := selectedEvents[selected]; duplicate {
			return fmt.Errorf("duplicate event type %q", selected)
		}
		selectedEvents[selected] = struct{}{}
	}
	if destination.SecretEnv != "" {
		if err := validateEnvironmentName(destination.SecretEnv); err != nil {
			return err
		}
	}
	if destination.TimeoutSeconds < 0 || destination.MaxAttempts < 0 {
		return errors.New("timeout and attempt counts cannot be negative")
	}
	return nil
}

// validateWebhookURL enforces what a destination URL may be. Plain HTTP is
// refused because a token would travel in clear text, and credentials embedded
// in the URL are refused because they would leak into every place a URL is
// handled.
func validateWebhookURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("destination url is empty")
	}
	if len(raw) > maxURLBytes {
		return fmt.Errorf("destination url exceeds %d bytes", maxURLBytes)
	}
	if !utf8.ValidString(raw) {
		return errors.New("destination url is not valid UTF-8")
	}
	for _, character := range raw {
		if character < 0x20 || character == 0x7f {
			return errors.New("destination url contains a control character")
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("destination url could not be parsed: %w", err)
	}
	if parsed.Scheme != "https" {
		return errors.New("destination url must use https")
	}
	if parsed.Host == "" {
		return errors.New("destination url has no host")
	}
	if parsed.User != nil {
		return errors.New("destination url must not embed credentials")
	}
	return nil
}

func validateIdentifier(label, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is empty", label)
	}
	if len(value) > maxIDBytes {
		return fmt.Errorf("%s exceeds %d bytes", label, maxIDBytes)
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '-', character == '_':
		default:
			return fmt.Errorf("%s may only contain letters, digits, hyphen, and underscore", label)
		}
	}
	return nil
}

func validateEnvironmentName(value string) error {
	if len(value) > maxIDBytes {
		return fmt.Errorf("secret_env exceeds %d bytes", maxIDBytes)
	}
	if strings.TrimSpace(value) == "" {
		return errors.New("secret_env is empty")
	}
	for _, character := range value {
		switch {
		case character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '_':
		default:
			return errors.New("secret_env must be an environment variable name, not a value")
		}
	}
	return nil
}
