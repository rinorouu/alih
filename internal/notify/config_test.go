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

package notify

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"alih/internal/event"
)

const validConfig = `{
  "schema_version": 1,
  "destinations": [
    {
      "id": "ops",
      "enabled": true,
      "type": "webhook",
      "url": "https://hooks.example.com/services/abc123",
      "events": ["operation.failed", "verification.recorded"],
      "secret_env": "ALIH_NOTIFY_OPS_TOKEN"
    }
  ]
}`

func writeConfig(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "alih")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create directory: %v", err)
	}
	path := filepath.Join(directory, configFilename)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	return directory
}

func TestNothingConfiguredMeansAlihStaysSilent(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join(t.TempDir(), "never-created"))
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("error = %v, want ErrNotConfigured", err)
	}
}

func TestAConfiguredDestinationIsReadWithItsBounds(t *testing.T) {
	t.Parallel()
	config, err := Load(writeConfig(t, validConfig, 0o600))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(config.Destinations) != 1 {
		t.Fatalf("destinations = %d", len(config.Destinations))
	}
	destination := config.Destinations[0]
	if destination.Timeout() != defaultTimeout || destination.Attempts() != defaultAttempts {
		t.Fatalf("bounds = %s / %d", destination.Timeout(), destination.Attempts())
	}
	if !destination.Wants(event.TypeOperationFailed) {
		t.Fatal("a selected event type was not wanted")
	}
	if destination.Wants(event.TypeOperationCompleted) {
		t.Fatal("an unselected event type was wanted")
	}
	disabled := destination
	disabled.Enabled = false
	if disabled.Wants(event.TypeOperationFailed) {
		t.Fatal("a disabled destination still wanted events")
	}
}

func TestDeliveryBoundsAreClampedNotTrusted(t *testing.T) {
	t.Parallel()
	huge := Destination{TimeoutSeconds: 3600, MaxAttempts: 100}
	if huge.Timeout() != maxTimeout {
		t.Fatalf("timeout = %s, want it clamped to %s", huge.Timeout(), maxTimeout)
	}
	if huge.Attempts() != maxAttempts {
		t.Fatalf("attempts = %d, want it clamped to %d", huge.Attempts(), maxAttempts)
	}
	tiny := Destination{TimeoutSeconds: 0, MaxAttempts: 0}
	if tiny.Timeout() != defaultTimeout || tiny.Attempts() != defaultAttempts {
		t.Fatalf("defaults = %s / %d", tiny.Timeout(), tiny.Attempts())
	}
	if (Destination{TimeoutSeconds: -0}).Timeout() < minTimeout {
		t.Fatal("timeout fell below its lower bound")
	}
}

func TestAnUnsafeOrUnreadableConfigurationIsRefused(t *testing.T) {
	t.Parallel()

	t.Run("too permissive", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX permission semantics are not enforced on Windows")
		}
		t.Parallel()
		_, err := Load(writeConfig(t, validConfig, 0o644))
		if !errors.Is(err, ErrInsecureConfig) {
			t.Fatalf("error = %v, want ErrInsecureConfig", err)
		}
	})

	t.Run("too permissive directory", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX permission semantics are not enforced on Windows")
		}
		t.Parallel()
		directory := writeConfig(t, validConfig, 0o600)
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatalf("chmod directory: %v", err)
		}
		_, err := Load(directory)
		if !errors.Is(err, ErrInsecureConfig) {
			t.Fatalf("error = %v, want ErrInsecureConfig", err)
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		t.Parallel()
		malformed := strings.Replace(validConfig, `"id": "ops",`, `"id": "ops", "retries": 9,`, 1)
		if _, err := Load(writeConfig(t, malformed, 0o600)); err == nil {
			t.Fatal("an unknown field was accepted")
		}
	})

	t.Run("trailing content", func(t *testing.T) {
		t.Parallel()
		if _, err := Load(writeConfig(t, validConfig+"\n{}", 0o600)); err == nil {
			t.Fatal("trailing content was accepted")
		}
	})
}

func TestValidationRefusesDestinationsAlihCannotSendToSafely(t *testing.T) {
	t.Parallel()
	base := func() Config {
		return Config{SchemaVersion: SchemaVersion, Destinations: []Destination{{
			ID: "ops", Enabled: true, Type: TypeWebhook,
			URL: "https://hooks.example.com/x", Events: []string{"operation.failed"},
		}}}
	}
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"wrong schema", func(c *Config) { c.SchemaVersion = 2 }, "unsupported notification schema version"},
		{"empty id", func(c *Config) { c.Destinations[0].ID = " " }, "id is empty"},
		{"unsafe id", func(c *Config) { c.Destinations[0].ID = "ops/../etc" }, "may only contain"},
		{"unknown transport", func(c *Config) { c.Destinations[0].Type = "email" }, "unsupported destination type"},
		{"plain http", func(c *Config) { c.Destinations[0].URL = "http://hooks.example.com/x" }, "must use https"},
		{"credentials in url", func(c *Config) {
			c.Destinations[0].URL = "https://user:pass@hooks.example.com/x"
		}, "must not embed credentials"},
		{"no host", func(c *Config) { c.Destinations[0].URL = "https:///x" }, "no host"},
		{"control character in url", func(c *Config) {
			c.Destinations[0].URL = "https://hooks.example.com/\x07"
		}, "control character"},
		{"no events selected", func(c *Config) { c.Destinations[0].Events = nil }, "nothing would ever be sent"},
		{"unknown event", func(c *Config) {
			c.Destinations[0].Events = []string{"backup.almost"}
		}, "unknown event type"},
		{"recursive notification event", func(c *Config) {
			c.Destinations[0].Events = []string{string(event.TypeNotificationProblem)}
		}, "cannot notify itself"},
		{"duplicate event", func(c *Config) {
			c.Destinations[0].Events = []string{"operation.failed", "operation.failed"}
		}, "duplicate event type"},
		{"secret value instead of name", func(c *Config) {
			c.Destinations[0].SecretEnv = "xoxb-1234-secret"
		}, "not a value"},
		{"duplicate destination", func(c *Config) {
			c.Destinations = append(c.Destinations, c.Destinations[0])
		}, "duplicate destination id"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := base()
			test.mutate(&config)
			err := Validate(config)
			if err == nil {
				t.Fatalf("configuration accepted; want %q", test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to mention %q", err, test.want)
			}
		})
	}
}

func TestARenderedDestinationNeverExposesWhatItsURLCarries(t *testing.T) {
	t.Parallel()
	destination := Destination{
		ID: "ops", Type: TypeWebhook,
		URL: "https://hooks.example.com/services/T000/B000/XXXsecretXXX?token=abc#frag",
	}
	rendered := destination.SafeURL()
	for _, forbidden := range []string{"XXXsecretXXX", "token=abc", "frag", "services"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("rendered destination %q exposes %q", rendered, forbidden)
		}
	}
	if !strings.Contains(rendered, "hooks.example.com") {
		t.Fatalf("rendered destination %q does not identify the host", rendered)
	}
	if unreadable := (Destination{URL: "://"}).SafeURL(); !strings.Contains(unreadable, "unreadable") {
		t.Fatalf("an unparsable URL rendered as %q", unreadable)
	}
}

func TestEnabledDestinationsAreStableAndExcludeDisabledOnes(t *testing.T) {
	t.Parallel()
	config := Config{SchemaVersion: SchemaVersion, Destinations: []Destination{
		{ID: "second", Enabled: true}, {ID: "off", Enabled: false}, {ID: "first", Enabled: true},
	}}
	enabled := config.EnabledDestinations()
	if len(enabled) != 2 || enabled[0].ID != "first" || enabled[1].ID != "second" {
		t.Fatalf("enabled destinations = %#v", enabled)
	}
}

func TestConfigurationPathLivesBesideTheOtherPrivateFiles(t *testing.T) {
	t.Parallel()
	path, err := Path(filepath.Join("relative", "root"))
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if !filepath.IsAbs(path) || filepath.Base(path) != configFilename {
		t.Fatalf("path = %q", path)
	}
	if _, err := Path(""); err != nil {
		t.Skipf("this environment has no user configuration directory: %v", err)
	}
	if time.Second < minTimeout {
		t.Fatal("timeout bounds are inconsistent")
	}
}
