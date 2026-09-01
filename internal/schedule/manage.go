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
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type Runner interface {
	Run(context.Context, Command) error
}

type ExecRunner struct{}

// Run invokes the scheduler directly, never through a shell. Scheduler output
// is deliberately not copied into the returned error because it is external
// text and is not needed to identify which bounded command failed.
func (ExecRunner) Run(ctx context.Context, command Command) error {
	if command.Executable == "" {
		return errors.New("scheduler command has no executable")
	}
	process := exec.CommandContext(ctx, command.Executable, command.Arguments...)
	if err := process.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("scheduler command %s failed", command.Executable)
	}
	return nil
}

type Manager struct{ Runner Runner }

func (manager Manager) runner() Runner {
	if manager.Runner != nil {
		return manager.Runner
	}
	return ExecRunner{}
}

// Install writes the previewed artifacts first, then explicitly registers them
// with the native user scheduler. A registration failure preserves the files
// for inspection and retry; it never pretends the schedule is installed.
func (manager Manager) Install(ctx context.Context, plan Plan) error {
	if err := validatePlan(plan); err != nil {
		return err
	}
	for _, artifact := range plan.Artifacts {
		if err := writeArtifact(artifact); err != nil {
			return err
		}
	}
	for _, command := range plan.Install {
		if err := manager.runner().Run(ctx, command); err != nil {
			return err
		}
	}
	return nil
}

// Remove first asks the native scheduler to stop the task, then removes only
// the exact generated artifacts named by the plan. A final reload command, if
// the platform has one, runs after the files disappear.
func (manager Manager) Remove(ctx context.Context, plan Plan) error {
	if err := validatePlan(plan); err != nil {
		return err
	}
	commands := plan.Remove
	remaining := commands
	if len(commands) > 0 {
		if err := manager.runner().Run(ctx, commands[0]); err != nil {
			return err
		}
		remaining = commands[1:]
	}
	for _, artifact := range plan.Artifacts {
		info, err := os.Lstat(artifact.Path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect installed schedule artifact: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse to remove non-regular schedule artifact %s", artifact.Path)
		}
		if err := os.Remove(artifact.Path); err != nil {
			return fmt.Errorf("remove schedule artifact: %w", err)
		}
	}
	for _, command := range remaining {
		if err := manager.runner().Run(ctx, command); err != nil {
			return err
		}
	}
	return nil
}

// Inspect compares installed files to a newly generated plan. It does not ask
// the native scheduler to mutate or refresh anything.
func Inspect(plan Plan) (installed bool, changed []string, err error) {
	if err := validatePlan(plan); err != nil {
		return false, nil, err
	}
	installed = true
	for _, artifact := range plan.Artifacts {
		info, statErr := os.Lstat(artifact.Path)
		if errors.Is(statErr, fs.ErrNotExist) {
			installed = false
			changed = append(changed, artifact.Path)
			continue
		}
		if statErr != nil || !info.Mode().IsRegular() {
			return false, nil, fmt.Errorf("inspect schedule artifact %s", artifact.Path)
		}
		content, readErr := os.ReadFile(artifact.Path)
		if readErr != nil {
			return false, nil, fmt.Errorf("read schedule artifact: %w", readErr)
		}
		if string(content) != artifact.Content {
			installed = false
			changed = append(changed, artifact.Path)
		}
	}
	return installed, changed, nil
}

// Registered asks the native scheduler whether the planned task exists. It is
// read-only; output remains external and is not persisted or parsed.
func (manager Manager) Registered(ctx context.Context, plan Plan) error {
	if err := validatePlan(plan); err != nil {
		return err
	}
	for _, command := range plan.Inspect {
		if err := manager.runner().Run(ctx, command); err != nil {
			return err
		}
	}
	return nil
}

func validatePlan(plan Plan) error {
	if plan.SchemaVersion != SchemaVersion || plan.ScheduleID == "" {
		return errors.New("schedule plan is invalid")
	}
	if len(plan.Artifacts) == 0 || len(plan.Install) == 0 || len(plan.Inspect) == 0 || len(plan.Remove) == 0 {
		return errors.New("schedule plan is incomplete")
	}
	for _, artifact := range plan.Artifacts {
		if !filepath.IsAbs(artifact.Path) || artifact.Content == "" {
			return errors.New("schedule plan contains an invalid artifact")
		}
	}
	return nil
}

func writeArtifact(artifact Artifact) error {
	directory := filepath.Dir(artifact.Path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create schedule artifact directory: %w", err)
	}
	if info, err := os.Lstat(artifact.Path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("refuse to replace non-regular schedule artifact %s", artifact.Path)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect schedule artifact: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".alih-schedule-*")
	if err != nil {
		return fmt.Errorf("create temporary schedule artifact: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	mode := fs.FileMode(artifact.Mode)
	if mode == 0 {
		mode = 0o600
	}
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("secure schedule artifact: %w", err)
	}
	if _, err := temporary.WriteString(artifact.Content); err != nil {
		return fmt.Errorf("write schedule artifact: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync schedule artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close schedule artifact: %w", err)
	}
	if err := os.Rename(temporaryPath, artifact.Path); err != nil {
		return fmt.Errorf("commit schedule artifact: %w", err)
	}
	committed = true
	if runtime.GOOS != "windows" {
		_ = os.Chmod(artifact.Path, mode)
	}
	return nil
}
