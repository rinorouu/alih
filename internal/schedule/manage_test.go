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
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

type recordingRunner struct {
	commands []Command
	failAt   int
}

func (runner *recordingRunner) Run(ctx context.Context, command Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	runner.commands = append(runner.commands, command)
	if runner.failAt > 0 && len(runner.commands) == runner.failAt {
		return errors.New("injected scheduler failure")
	}
	return nil
}

func testPlan(t *testing.T) Plan {
	t.Helper()
	root := t.TempDir()
	return Plan{
		SchemaVersion: SchemaVersion, ScheduleID: "daily-main", Platform: runtime.GOOS,
		Artifacts: []Artifact{
			{Path: filepath.Join(root, "one.service"), Content: "service\n", Mode: 0o600},
			{Path: filepath.Join(root, "two.timer"), Content: "timer\n", Mode: 0o600},
		},
		Install: []Command{{Executable: "scheduler", Arguments: []string{"install"}}},
		Inspect: []Command{{Executable: "scheduler", Arguments: []string{"inspect"}}},
		Remove: []Command{
			{Executable: "scheduler", Arguments: []string{"disable"}},
			{Executable: "scheduler", Arguments: []string{"reload"}},
		},
	}
}

func TestRegisteredUsesOnlyTheReadOnlyInspectionPlan(t *testing.T) {
	t.Parallel()
	plan := testPlan(t)
	runner := &recordingRunner{}
	if err := (Manager{Runner: runner}).Registered(context.Background(), plan); err != nil {
		t.Fatalf("registered: %v", err)
	}
	if !reflect.DeepEqual(runner.commands, plan.Inspect) {
		t.Fatalf("inspection commands = %#v", runner.commands)
	}
}

func TestManagerInstallsInspectsAndRemovesOnlyPlannedArtifacts(t *testing.T) {
	t.Parallel()
	plan := testPlan(t)
	runner := &recordingRunner{}
	manager := Manager{Runner: runner}
	if err := manager.Install(context.Background(), plan); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !reflect.DeepEqual(runner.commands, plan.Install) {
		t.Fatalf("install commands = %#v", runner.commands)
	}
	installed, changed, err := Inspect(plan)
	if err != nil || !installed || len(changed) != 0 {
		t.Fatalf("inspect = %t %#v %v", installed, changed, err)
	}
	if runtime.GOOS != "windows" {
		for _, artifact := range plan.Artifacts {
			info, err := os.Stat(artifact.Path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("artifact permissions = %v (%v)", info, err)
			}
		}
	}
	if err := os.WriteFile(plan.Artifacts[0].Path, []byte("operator edit\n"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	installed, changed, err = Inspect(plan)
	if err != nil || installed || len(changed) != 1 {
		t.Fatalf("changed inspect = %t %#v %v", installed, changed, err)
	}

	runner.commands = nil
	if err := manager.Remove(context.Background(), plan); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !reflect.DeepEqual(runner.commands, plan.Remove) {
		t.Fatalf("remove commands = %#v", runner.commands)
	}
	for _, artifact := range plan.Artifacts {
		if _, err := os.Stat(artifact.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("artifact still exists: %s (%v)", artifact.Path, err)
		}
	}
}

func TestRegistrationFailureIsExplicitAndPreservesPreviewedArtifacts(t *testing.T) {
	t.Parallel()
	plan := testPlan(t)
	manager := Manager{Runner: &recordingRunner{failAt: 1}}
	if err := manager.Install(context.Background(), plan); err == nil {
		t.Fatal("registration failure was presented as installed")
	}
	for _, artifact := range plan.Artifacts {
		if _, err := os.Stat(artifact.Path); err != nil {
			t.Fatalf("failed registration lost inspectable artifact: %v", err)
		}
	}
}

func TestManagerRefusesSymlinkArtifactsAndHonorsCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not available to an unprivileged Windows test")
	}
	t.Parallel()
	plan := testPlan(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("do not replace"), 0o600); err != nil {
		t.Fatalf("target: %v", err)
	}
	if err := os.Symlink(target, plan.Artifacts[0].Path); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := (Manager{Runner: &recordingRunner{}}).Install(context.Background(), plan); err == nil {
		t.Fatal("symlink artifact was replaced")
	}
	content, _ := os.ReadFile(target)
	if string(content) != "do not replace" {
		t.Fatal("symlink target was changed")
	}

	plan = testPlan(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (Manager{Runner: &recordingRunner{}}).Install(ctx, plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled install error = %v", err)
	}
}
