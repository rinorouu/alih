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

// Package schedule defines recurring Alih operations and renders them into the
// operating system's user-level scheduler. Alih has no resident scheduler,
// background daemon, or second backup pipeline.
package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SchemaVersion = 1
	configName    = "schedules.json"
	maxConfig     = 64 << 10
	maxSchedules  = 8
)

const (
	OperationBackup = "backup"
	FrequencyDaily  = "daily"
	TimezoneLocal   = "local"
	MissedRunOnce   = "run_once"
)

var (
	ErrNotConfigured = errors.New("no schedule configuration exists")
	ErrInsecure      = errors.New("schedule configuration is not private")
)

// Cadence is deliberately smaller than cron. Daily local civil time is the
// one recurring behavior all three native schedulers can represent without a
// resident process or incompatible timezone claims.
type Cadence struct {
	Frequency       string `json:"frequency"`
	At              string `json:"at"`
	Timezone        string `json:"timezone"`
	MissedRunPolicy string `json:"missed_run_policy"`
}

// Definition is one provider-neutral recurring operation. The current CLI can
// execute the ClickUp adapter, but the schedule identity itself keeps connector
// explicit for the Stage 9 adapter review.
type Definition struct {
	ID          string  `json:"id"`
	Enabled     bool    `json:"enabled"`
	Operation   string  `json:"operation"`
	Connector   string  `json:"connector"`
	WorkspaceID string  `json:"workspace_id"`
	Destination string  `json:"destination"`
	Cadence     Cadence `json:"cadence"`
}

type Config struct {
	SchemaVersion int          `json:"schema_version"`
	Schedules     []Definition `json:"schedules"`
}

func Path(root string) (string, error) {
	if strings.TrimSpace(root) != "" {
		absolute, err := filepath.Abs(strings.TrimSpace(root))
		if err != nil {
			return "", fmt.Errorf("resolve schedule configuration directory: %w", err)
		}
		return filepath.Join(absolute, configName), nil
	}
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user configuration directory: %w", err)
	}
	return filepath.Join(configRoot, "alih", configName), nil
}

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
		return Config{}, fmt.Errorf("inspect schedule configuration: %w", err)
	}
	directory, err := os.Lstat(filepath.Dir(path))
	if err != nil || !directory.IsDir() {
		return Config{}, fmt.Errorf("%w: configuration parent is not a real directory", ErrInsecure)
	}
	if !info.Mode().IsRegular() {
		return Config{}, fmt.Errorf("%w: configuration is not a regular file", ErrInsecure)
	}
	if runtime.GOOS != "windows" && (directory.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o077 != 0) {
		return Config{}, fmt.Errorf("%w: require directory 0700 and file 0600", ErrInsecure)
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open schedule configuration: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxConfig+1))
	if err != nil {
		return Config{}, fmt.Errorf("read schedule configuration: %w", err)
	}
	if len(content) > maxConfig {
		return Config{}, fmt.Errorf("schedule configuration exceeds %d bytes", maxConfig)
	}
	return Parse(content)
}

func Parse(content []byte) (Config, error) {
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("schedule configuration could not be decoded: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("schedule configuration contains trailing content")
	}
	if err := Validate(config); err != nil {
		return Config{}, err
	}
	Canonicalize(&config)
	return config, nil
}

func Canonicalize(config *Config) {
	if config == nil {
		return
	}
	for index := range config.Schedules {
		config.Schedules[index].ID = strings.TrimSpace(config.Schedules[index].ID)
		config.Schedules[index].Connector = strings.TrimSpace(config.Schedules[index].Connector)
		config.Schedules[index].WorkspaceID = strings.TrimSpace(config.Schedules[index].WorkspaceID)
		config.Schedules[index].Destination = filepath.Clean(strings.TrimSpace(config.Schedules[index].Destination))
	}
	sort.Slice(config.Schedules, func(i, j int) bool { return config.Schedules[i].ID < config.Schedules[j].ID })
}

func Validate(config Config) error {
	if config.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schedule schema version %d", config.SchemaVersion)
	}
	if len(config.Schedules) > maxSchedules {
		return fmt.Errorf("more than %d schedules are configured", maxSchedules)
	}
	seen := make(map[string]struct{}, len(config.Schedules))
	for index, definition := range config.Schedules {
		if err := validateDefinition(definition); err != nil {
			return fmt.Errorf("schedule %d: %w", index, err)
		}
		if _, duplicate := seen[definition.ID]; duplicate {
			return fmt.Errorf("duplicate schedule id %q", definition.ID)
		}
		seen[definition.ID] = struct{}{}
	}
	return nil
}

func validateDefinition(definition Definition) error {
	if err := validateID("id", definition.ID); err != nil {
		return err
	}
	if definition.Operation != OperationBackup {
		return fmt.Errorf("unsupported operation %q", definition.Operation)
	}
	if err := validateID("connector", definition.Connector); err != nil {
		return err
	}
	if err := validateText("workspace_id", definition.WorkspaceID, 512); err != nil {
		return err
	}
	if err := validateText("destination", definition.Destination, 4096); err != nil {
		return err
	}
	if !filepath.IsAbs(definition.Destination) {
		return errors.New("destination must be an absolute path")
	}
	if definition.Cadence.Frequency != FrequencyDaily {
		return fmt.Errorf("unsupported cadence frequency %q", definition.Cadence.Frequency)
	}
	if _, _, err := parseCivilTime(definition.Cadence.At); err != nil {
		return err
	}
	if definition.Cadence.Timezone != TimezoneLocal {
		return errors.New("timezone must be local for equivalent native scheduling on Linux, macOS, and Windows")
	}
	if definition.Cadence.MissedRunPolicy != MissedRunOnce {
		return fmt.Errorf("unsupported missed_run_policy %q", definition.Cadence.MissedRunPolicy)
	}
	return nil
}

func (config Config) Enabled() []Definition {
	enabled := make([]Definition, 0, len(config.Schedules))
	for _, definition := range config.Schedules {
		if definition.Enabled {
			enabled = append(enabled, definition)
		}
	}
	sort.Slice(enabled, func(i, j int) bool { return enabled[i].ID < enabled[j].ID })
	return enabled
}

func (config Config) Find(id string) (Definition, bool) {
	for _, definition := range config.Schedules {
		if definition.ID == strings.TrimSpace(id) {
			return definition, true
		}
	}
	return Definition{}, false
}

func parseCivilTime(value string) (int, int, error) {
	if len(value) != 5 || value[2] != ':' {
		return 0, 0, errors.New("cadence at must use 24-hour HH:MM")
	}
	hour, hourErr := strconv.Atoi(value[:2])
	minute, minuteErr := strconv.Atoi(value[3:])
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, errors.New("cadence at is not a valid local civil time")
	}
	return hour, minute, nil
}

func validateID(label, value string) error {
	if err := validateText(label, value, 64); err != nil {
		return err
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9', character == '-', character == '_':
		default:
			return fmt.Errorf("%s may only contain letters, digits, hyphen, and underscore", label)
		}
	}
	return nil
}

func validateText(label, value string, limit int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is empty", label)
	}
	if len(value) > limit {
		return fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", label)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

// Next returns the next local civil trigger at or after now. Native schedulers
// remain authoritative; this helper exists for deterministic previews only.
func (definition Definition) Next(now time.Time) time.Time {
	hour, minute, _ := parseCivilTime(definition.Cadence.At)
	local := now.In(time.Local)
	next := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, time.Local)
	if next.Before(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}
