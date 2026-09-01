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

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"alih/internal/schedule"
)

const scheduleHelpText = `Preview and manage user-level native backup schedules.

Usage:
  alih schedule check [--json]
  alih schedule preview ID [--platform linux|darwin|windows] [--json]
  alih schedule inspect ID [--json]
  alih schedule install ID
  alih schedule remove ID

Schedules are explicit local definitions in schedules.json under Alih's user
configuration directory. Check and preview are read-only. Install and remove
are the only commands that mutate native scheduler state, and must be invoked
explicitly. Linux uses a systemd user timer, macOS uses a launchd LaunchAgent,
and Windows uses a per-user Task Scheduler task. Alih has no resident scheduler.

Every generated task invokes the same "alih backup" pipeline with an absolute
executable, workspace ID, and destination. It contains no credential value;
the scheduled user must already have a verified credential in Alih's local
credential store. Core's cross-process lock rejects overlap immediately.
`

type scheduleCheckDocument struct {
	SchemaVersion int                   `json:"schema_version"`
	Kind          string                `json:"kind"`
	Configured    bool                  `json:"configured"`
	Configuration string                `json:"configuration"`
	Schedules     []schedule.Definition `json:"schedules"`
	Plan          *schedule.Plan        `json:"plan,omitempty"`
	Installed     *bool                 `json:"installed,omitempty"`
	Changed       []string              `json:"changed_artifacts,omitempty"`
	Registered    *bool                 `json:"registered,omitempty"`
}

func (a *App) runSchedule(args []string) int {
	if len(args) == 0 {
		args = []string{"check"}
	}
	action := args[0]
	if action == "--help" || action == "-h" {
		fmt.Fprint(a.stdout, scheduleHelpText)
		return 0
	}
	if action != "check" && action != "preview" && action != "inspect" &&
		action != "install" && action != "remove" {
		fmt.Fprintf(a.stderr, "alih schedule: unknown action %q\n", displayValue(action))
		return 2
	}
	flags := flag.NewFlagSet("alih schedule "+action, flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() { fmt.Fprint(a.stdout, scheduleHelpText) }
	asJSON := flags.Bool("json", false, "print a stable machine-readable document")
	platform := flags.String("platform", "", "preview platform: linux, darwin, or windows")
	if err := flags.Parse(scheduleFlagOrder(args[1:])); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if action == "check" {
		if flags.NArg() != 0 || strings.TrimSpace(*platform) != "" {
			fmt.Fprintln(a.stderr, "alih schedule check: arguments and --platform are not accepted")
			return 2
		}
		return a.scheduleCheck(*asJSON)
	}
	if flags.NArg() != 1 {
		fmt.Fprintf(a.stderr, "alih schedule %s: exactly one schedule ID is required\n", action)
		return 2
	}
	if action != "preview" && strings.TrimSpace(*platform) != "" {
		fmt.Fprintf(a.stderr, "alih schedule %s: --platform is available only for preview\n", action)
		return 2
	}
	if (action == "install" || action == "remove") && *asJSON {
		fmt.Fprintf(a.stderr, "alih schedule %s: --json is not available for a mutating action\n", action)
		return 2
	}
	config, err := schedule.Load(a.options.ScheduleRoot)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih schedule %s: %s\n", action, safeError(err, ""))
		return 1
	}
	definition, found := config.Find(flags.Arg(0))
	if !found {
		fmt.Fprintf(a.stderr, "alih schedule %s: schedule %q was not found\n", action, displayValue(flags.Arg(0)))
		return 1
	}
	if action == "install" && !definition.Enabled {
		fmt.Fprintf(a.stderr, "alih schedule install: schedule %q is disabled\n", definition.ID)
		return 1
	}
	targetPlatform := a.schedulePlatform()
	if action == "preview" && strings.TrimSpace(*platform) != "" {
		targetPlatform = strings.TrimSpace(*platform)
	}
	plan, err := a.schedulePlan(definition, targetPlatform)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih schedule %s: %s\n", action, safeError(err, ""))
		return 1
	}
	document := scheduleCheckDocument{
		SchemaVersion: schedule.SchemaVersion, Kind: "schedule_plan", Configured: true,
		Schedules: []schedule.Definition{definition}, Plan: &plan,
	}
	switch action {
	case "preview":
		return a.writeScheduleDocument(document, *asJSON)
	case "inspect":
		installed, changed, err := schedule.Inspect(plan)
		if err != nil {
			fmt.Fprintf(a.stderr, "alih schedule inspect: %s\n", safeError(err, ""))
			return 1
		}
		registered := (schedule.Manager{Runner: a.options.ScheduleRunner}).Registered(context.Background(), plan) == nil
		document.Installed, document.Changed, document.Registered = &installed, changed, &registered
		code := a.writeScheduleDocument(document, *asJSON)
		if code != 0 {
			return code
		}
		if !installed || !registered {
			return 1
		}
		return 0
	case "install", "remove":
		if targetPlatform != runtime.GOOS {
			fmt.Fprintln(a.stderr, "alih schedule: native mutation is allowed only for the current operating system")
			return 1
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		manager := schedule.Manager{Runner: a.options.ScheduleRunner}
		if action == "install" {
			err = manager.Install(ctx, plan)
		} else {
			err = manager.Remove(ctx, plan)
		}
		if err != nil {
			fmt.Fprintf(a.stderr, "alih schedule %s: %s\n", action, safeError(err, ""))
			return 1
		}
		verb := "installed"
		if action == "remove" {
			verb = "removed"
		}
		fmt.Fprintf(a.stdout, "Schedule %s %s for the current user.\n", definition.ID, verb)
		return 0
	}
	return 2
}

// The standard flag package stops at the first positional argument. Schedule
// actions are naturally written as `preview ID --json`, so move only the known
// flags ahead of the ID while leaving unknown input for the flag parser to
// reject.
func scheduleFlagOrder(args []string) []string {
	var flags, positional []string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--json" || argument == "-h" || argument == "--help" || strings.HasPrefix(argument, "--platform="):
			flags = append(flags, argument)
		case argument == "--platform" && index+1 < len(args):
			flags = append(flags, argument, args[index+1])
			index++
		default:
			positional = append(positional, argument)
		}
	}
	return append(flags, positional...)
}

func (a *App) scheduleCheck(asJSON bool) int {
	path, err := schedule.Path(a.options.ScheduleRoot)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih schedule check: %s\n", safeError(err, ""))
		return 1
	}
	document := scheduleCheckDocument{
		SchemaVersion: schedule.SchemaVersion, Kind: "schedule_configuration",
		Configuration: path, Schedules: []schedule.Definition{},
	}
	config, err := schedule.Load(a.options.ScheduleRoot)
	if errors.Is(err, schedule.ErrNotConfigured) {
		return a.writeScheduleDocument(document, asJSON)
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "alih schedule check: %s\n", safeError(err, ""))
		return 1
	}
	document.Configured = true
	document.Schedules = config.Schedules
	return a.writeScheduleDocument(document, asJSON)
}

func (a *App) writeScheduleDocument(document scheduleCheckDocument, asJSON bool) int {
	if asJSON {
		encoder := json.NewEncoder(a.stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(document); err != nil {
			fmt.Fprintf(a.stderr, "alih schedule: encode result: %v\n", err)
			return 1
		}
		return 0
	}
	if document.Plan != nil {
		fmt.Fprintf(a.stdout, "Schedule: %s (%s)\n", document.Plan.ScheduleID, document.Plan.Platform)
		for _, artifact := range document.Plan.Artifacts {
			fmt.Fprintf(a.stdout, "\nArtifact: %s\n%s", artifact.Path, artifact.Content)
		}
		fmt.Fprintln(a.stdout, "\nInstall argv:")
		for _, command := range document.Plan.Install {
			content, _ := json.Marshal(append([]string{command.Executable}, command.Arguments...))
			fmt.Fprintf(a.stdout, "  %s\n", content)
		}
		if document.Installed != nil {
			fmt.Fprintf(a.stdout, "Artifacts match: %t\nNative registration present: %t\n", *document.Installed, *document.Registered)
		}
		return 0
	}
	if !document.Configured {
		fmt.Fprintln(a.stdout, "Schedules: not configured (no recurring execution).")
		fmt.Fprintf(a.stdout, "Configuration: %s\n", document.Configuration)
		return 0
	}
	fmt.Fprintf(a.stdout, "Schedules: %d configured. This check made no scheduler changes.\n", len(document.Schedules))
	for _, definition := range document.Schedules {
		status := "disabled"
		if definition.Enabled {
			status = "enabled"
		}
		fmt.Fprintf(a.stdout, "- %s: %s %s at %s %s, workspace %s, destination %s\n",
			definition.ID, status, definition.Cadence.Frequency, definition.Cadence.At,
			definition.Cadence.Timezone, definition.WorkspaceID, definition.Destination)
	}
	return 0
}

func (a *App) schedulePlatform() string {
	if platform := strings.TrimSpace(a.options.SchedulePlatform); platform != "" {
		return platform
	}
	return runtime.GOOS
}

func (a *App) schedulePlan(definition schedule.Definition, platform string) (schedule.Plan, error) {
	executable := strings.TrimSpace(a.options.ExecutablePath)
	if executable == "" {
		resolved, err := os.Executable()
		if err != nil {
			return schedule.Plan{}, fmt.Errorf("resolve Alih executable: %w", err)
		}
		executable, err = filepath.Abs(resolved)
		if err != nil {
			return schedule.Plan{}, fmt.Errorf("resolve Alih executable: %w", err)
		}
	}
	home := strings.TrimSpace(a.options.UserHome)
	if home == "" {
		resolved, err := os.UserHomeDir()
		if err != nil {
			return schedule.Plan{}, fmt.Errorf("resolve user home: %w", err)
		}
		home = resolved
	}
	configPath, err := schedule.Path(a.options.ScheduleRoot)
	if err != nil {
		return schedule.Plan{}, err
	}
	uid := strings.TrimSpace(a.options.UserID)
	if uid == "" && platform == schedule.PlatformDarwin {
		current, err := user.Current()
		if err != nil {
			return schedule.Plan{}, fmt.Errorf("resolve current user id: %w", err)
		}
		uid = current.Uid
	}
	// Which connectors this build can actually run is knowledge of the wiring,
	// not of the scheduler. Checking it here keeps schedule generation
	// provider-neutral while still refusing to install a timer for a connector
	// this executable could never execute.
	if err := a.ensureConnectorIsWired(definition.Connector); err != nil {
		return schedule.Plan{}, err
	}
	return schedule.Generate(definition, platform, executable, home, filepath.Dir(configPath), uid)
}

// ensureConnectorIsWired refuses a schedule naming a connector this executable
// cannot run. An App with no connector wired at all cannot answer the question
// and does not pretend to: read-only status still describes such a schedule
// rather than refusing to talk about it.
func (a *App) ensureConnectorIsWired(name string) error {
	if a.options.Authenticator == nil {
		return nil
	}
	if a.options.Authenticator.Name() != name {
		return fmt.Errorf("connector %q is not executable by the current CLI", name)
	}
	return nil
}
