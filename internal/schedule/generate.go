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
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	PlatformLinux   = "linux"
	PlatformDarwin  = "darwin"
	PlatformWindows = "windows"
)

type Artifact struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    uint32 `json:"mode"`
}

type Command struct {
	Executable string   `json:"executable"`
	Arguments  []string `json:"arguments"`
}

type Plan struct {
	SchemaVersion int        `json:"schema_version"`
	ScheduleID    string     `json:"schedule_id"`
	Platform      string     `json:"platform"`
	Artifacts     []Artifact `json:"artifacts"`
	Install       []Command  `json:"install_commands"`
	Inspect       []Command  `json:"inspect_commands"`
	Remove        []Command  `json:"remove_commands"`
}

// Generate renders native user-level scheduler artifacts. It never writes or
// invokes them; preview and tests therefore have no external side effects.
func Generate(definition Definition, platform, executable, home, configRoot, userID string) (Plan, error) {
	if err := validateDefinition(definition); err != nil {
		return Plan{}, err
	}
	if !filepath.IsAbs(executable) {
		return Plan{}, errors.New("scheduled executable path must be absolute")
	}
	if !filepath.IsAbs(home) || !filepath.IsAbs(configRoot) {
		return Plan{}, errors.New("schedule generation requires absolute home and configuration roots")
	}
	plan := Plan{SchemaVersion: SchemaVersion, ScheduleID: definition.ID, Platform: platform}
	switch platform {
	case PlatformLinux:
		return generateSystemd(plan, definition, executable, home)
	case PlatformDarwin:
		return generateLaunchd(plan, definition, executable, home, userID)
	case PlatformWindows:
		return generateTaskScheduler(plan, definition, executable, configRoot)
	default:
		return Plan{}, fmt.Errorf("unsupported scheduling platform %q", platform)
	}
}

func backupArguments(definition Definition) []string {
	return []string{"backup", "--workspace-id", definition.WorkspaceID, "--destination", definition.Destination,
		"--schedule-id", definition.ID}
}

func generateSystemd(plan Plan, definition Definition, executable, home string) (Plan, error) {
	unitName := "alih-" + definition.ID
	directory := filepath.Join(home, ".config", "systemd", "user")
	servicePath := filepath.Join(directory, unitName+".service")
	timerPath := filepath.Join(directory, unitName+".timer")
	command, err := systemdCommand(append([]string{executable}, backupArguments(definition)...))
	if err != nil {
		return Plan{}, err
	}
	workingDirectory, err := systemdQuote(filepath.Dir(executable))
	if err != nil {
		return Plan{}, err
	}
	persistent := "false"
	if definition.Cadence.MissedRunPolicy == MissedRunOnce {
		persistent = "true"
	}
	service := "[Unit]\nDescription=Alih scheduled backup " + definition.ID +
		"\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=oneshot\nExecStart=" + command +
		"\nWorkingDirectory=" + workingDirectory + "\nNoNewPrivileges=true\nPrivateTmp=true\n"
	timer := "[Unit]\nDescription=Alih scheduled backup timer " + definition.ID +
		"\n\n[Timer]\nOnCalendar=*-*-* " + definition.Cadence.At + ":00\nPersistent=" + persistent +
		"\nAccuracySec=1min\nRandomizedDelaySec=0\nUnit=" + unitName + ".service\n\n[Install]\nWantedBy=timers.target\n"
	plan.Artifacts = []Artifact{{Path: servicePath, Content: service, Mode: 0o600}, {Path: timerPath, Content: timer, Mode: 0o600}}
	plan.Install = []Command{
		{Executable: "systemctl", Arguments: []string{"--user", "daemon-reload"}},
		{Executable: "systemctl", Arguments: []string{"--user", "enable", "--now", unitName + ".timer"}},
	}
	plan.Inspect = []Command{{Executable: "systemctl", Arguments: []string{"--user", "is-enabled", unitName + ".timer"}}}
	plan.Remove = []Command{
		{Executable: "systemctl", Arguments: []string{"--user", "disable", "--now", unitName + ".timer"}},
		{Executable: "systemctl", Arguments: []string{"--user", "daemon-reload"}},
	}
	return plan, nil
}

func generateLaunchd(plan Plan, definition Definition, executable, home, userID string) (Plan, error) {
	if strings.TrimSpace(userID) == "" {
		return Plan{}, errors.New("launchd generation requires the numeric user id")
	}
	hour, minute, _ := parseCivilTime(definition.Cadence.At)
	label := "io.alih.schedule." + definition.ID
	path := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	arguments := append([]string{executable}, backupArguments(definition)...)
	var argumentXML strings.Builder
	for _, argument := range arguments {
		argumentXML.WriteString("    <string>")
		argumentXML.WriteString(xmlText(argument))
		argumentXML.WriteString("</string>\n")
	}
	content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + xmlText(label) + `</string>
  <key>ProgramArguments</key>
  <array>
` + argumentXML.String() + `  </array>
  <key>WorkingDirectory</key><string>` + xmlText(filepath.Dir(executable)) + `</string>
  <key>StartCalendarInterval</key>
  <dict><key>Hour</key><integer>` + strconv.Itoa(hour) + `</integer><key>Minute</key><integer>` + strconv.Itoa(minute) + `</integer></dict>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`
	plan.Artifacts = []Artifact{{Path: path, Content: content, Mode: 0o600}}
	domain := "gui/" + userID
	plan.Install = []Command{{Executable: "launchctl", Arguments: []string{"bootstrap", domain, path}}}
	plan.Inspect = []Command{{Executable: "launchctl", Arguments: []string{"print", domain + "/" + label}}}
	plan.Remove = []Command{{Executable: "launchctl", Arguments: []string{"bootout", domain + "/" + label}}}
	return plan, nil
}

func generateTaskScheduler(plan Plan, definition Definition, executable, configRoot string) (Plan, error) {
	hour, minute, _ := parseCivilTime(definition.Cadence.At)
	path := filepath.Join(configRoot, "generated", "schedules", "alih-"+definition.ID+".xml")
	arguments := windowsCommandLine(backupArguments(definition))
	startWhenAvailable := "false"
	if definition.Cadence.MissedRunPolicy == MissedRunOnce {
		startWhenAvailable = "true"
	}
	content := `<?xml version="1.0" encoding="UTF-8"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>Alih scheduled backup ` + xmlText(definition.ID) + `</Description></RegistrationInfo>
  <Principals><Principal id="Author"><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Triggers><CalendarTrigger><StartBoundary>2000-01-01T` + fmt.Sprintf("%02d:%02d:00", hour, minute) + `</StartBoundary><Enabled>true</Enabled><ScheduleByDay><DaysInterval>1</DaysInterval></ScheduleByDay></CalendarTrigger></Triggers>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><StartWhenAvailable>` + startWhenAvailable + `</StartWhenAvailable><ExecutionTimeLimit>PT6H</ExecutionTimeLimit><Enabled>true</Enabled></Settings>
  <Actions Context="Author"><Exec><Command>` + xmlText(executable) + `</Command><Arguments>` + xmlText(arguments) + `</Arguments><WorkingDirectory>` + xmlText(filepath.Dir(executable)) + `</WorkingDirectory></Exec></Actions>
</Task>
`
	plan.Artifacts = []Artifact{{Path: path, Content: content, Mode: 0o600}}
	taskName := `\Alih\` + definition.ID
	plan.Install = []Command{{Executable: "schtasks.exe", Arguments: []string{"/Create", "/TN", taskName, "/XML", path, "/F"}}}
	plan.Inspect = []Command{{Executable: "schtasks.exe", Arguments: []string{"/Query", "/TN", taskName}}}
	plan.Remove = []Command{{Executable: "schtasks.exe", Arguments: []string{"/Delete", "/TN", taskName, "/F"}}}
	return plan, nil
}

func systemdCommand(arguments []string) (string, error) {
	quoted := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		value, err := systemdQuote(argument)
		if err != nil {
			return "", err
		}
		quoted = append(quoted, value)
	}
	return strings.Join(quoted, " "), nil
}

func systemdQuote(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("scheduled argument contains a control character")
	}
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`, nil
}

func windowsCommandLine(arguments []string) string {
	quoted := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		quoted = append(quoted, windowsQuote(argument))
	}
	return strings.Join(quoted, " ")
}

// windowsQuote follows CommandLineToArgvW rules: backslashes before a quote are
// doubled, and trailing backslashes are doubled before the closing quote.
func windowsQuote(argument string) string {
	if argument != "" && !strings.ContainsAny(argument, " \t\n\v\"") {
		return argument
	}
	var output strings.Builder
	output.WriteByte('"')
	backslashes := 0
	for _, character := range argument {
		if character == '\\' {
			backslashes++
			continue
		}
		if character == '"' {
			output.WriteString(strings.Repeat("\\", backslashes*2+1))
			output.WriteRune(character)
			backslashes = 0
			continue
		}
		output.WriteString(strings.Repeat("\\", backslashes))
		backslashes = 0
		output.WriteRune(character)
	}
	output.WriteString(strings.Repeat("\\", backslashes*2))
	output.WriteByte('"')
	return output.String()
}

func xmlText(value string) string {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(value))
	return output.String()
}
