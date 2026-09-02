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
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"alih/internal/config"
	"alih/internal/usage"
)

const setupHelpText = `Choose how Alih is operated, and set up a connector.

Usage:
  alih setup [--mode self-managed|assistance] [--show]

Alih is free and open source, and every capability works the same way however
you answer. The choice records who operates this installation, not what this
program is allowed to do.

  Self-managed      You configure and run Alih yourself. Nothing to pay, and
                    nothing to activate.
  Alih Assistance   An optional service where setup and operation are handled
                    for you. It is not available yet, so choosing it records
                    your intent locally and contacts nobody.

Setup is safe to run again at any time. It never deletes archives, credentials,
schedules or notification settings, and you can move between modes freely.

Running Alih without ever running setup is fine: an installation with no
recorded choice is self-managed.

  --mode NAME   Record a mode without prompting, for unattended installs.
  --show        Print the current mode and exit without changing anything.

Exit codes:
  0  a mode is recorded, or --show printed one
  2  usage error, or no mode could be chosen without a terminal
  4  local usage state exists but cannot be read; ALIH never rewrites it
`

// runSetup is the lifecycle surface for how this installation is operated.
//
// It is the only interactive command Alih has. Every other command must keep
// working unattended, so this one refuses to prompt when there is no terminal
// and points at --mode instead.
func (a *App) runSetup(args []string) int {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() { fmt.Fprint(a.stderr, setupHelpText) }
	mode := flags.String("mode", "", "record a usage mode without prompting")
	show := flags.Bool("show", false, "print the current mode and exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "alih setup: unexpected argument %q\n", flags.Arg(0))
		return 2
	}

	store := a.usageStore()
	current, err := store.Load()
	switch {
	case errors.Is(err, usage.ErrNotChosen):
		current = ""
	case err != nil:
		fmt.Fprintf(a.stderr, "alih setup: %v\n", err)
		fmt.Fprintln(a.stderr, "ALIH does not rewrite local state it cannot read. Inspect or remove the file, then run setup again.")
		return 4
	}

	if *show {
		a.printCurrentMode(current)
		return 0
	}

	if strings.TrimSpace(*mode) != "" {
		chosen, err := usage.ParseMode(*mode)
		if err != nil {
			fmt.Fprintf(a.stderr, "alih setup: %v\n", err)
			return 2
		}
		return a.recordMode(store, chosen, current)
	}

	if !a.interactive() {
		fmt.Fprintln(a.stderr, "alih setup: a choice is required in non-interactive mode; use --mode self-managed or --mode assistance")
		return 2
	}
	return a.promptForMode(store, current)
}

func (a *App) printCurrentMode(current usage.Mode) {
	if current == "" {
		fmt.Fprintln(a.stdout, "Mode: Self-managed (default; no choice recorded yet)")
		return
	}
	fmt.Fprintf(a.stdout, "Mode: %s\n", current.Display())
	if current == usage.Assistance {
		// Never imply a subscription exists. Only a future Assistance system
		// could know that, and this file is not it.
		fmt.Fprintln(a.stdout, "Assistance activation: not connected")
	}
}

// promptForMode asks the question and records the answer.
func (a *App) promptForMode(store *usage.Store, current usage.Mode) int {
	if current != "" {
		a.printCurrentMode(current)
		fmt.Fprintln(a.stdout)
	}
	fmt.Fprintln(a.stdout, "How would you like to use Alih?")
	fmt.Fprintln(a.stdout)
	fmt.Fprintln(a.stdout, "  1) Self-managed — 100% free")
	fmt.Fprintln(a.stdout, "     You configure and operate Alih yourself.")
	fmt.Fprintln(a.stdout, "  2) Alih Assistance")
	fmt.Fprintln(a.stdout, "     Setup and operation handled for you. Not available yet.")
	fmt.Fprintln(a.stdout)

	reader := bufio.NewReader(a.stdin())
	for attempt := 0; attempt < 3; attempt++ {
		fmt.Fprint(a.stdout, "Select [1-2] (or press Enter to keep things as they are): ")
		line, err := reader.ReadString('\n')
		answer := strings.TrimSpace(line)
		if errors.Is(err, io.EOF) && answer == "" {
			// Ctrl+D, a closed pipe, or a cancelled prompt. Nothing was
			// chosen, so nothing is written.
			fmt.Fprintln(a.stdout)
			fmt.Fprintln(a.stdout, "Nothing was changed.")
			return 0
		}
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintf(a.stderr, "alih setup: read selection: %v\n", err)
			return 2
		}
		switch answer {
		case "":
			fmt.Fprintln(a.stdout, "Nothing was changed.")
			return 0
		case "1":
			return a.recordMode(store, usage.SelfManaged, current)
		case "2":
			return a.recordMode(store, usage.Assistance, current)
		default:
			fmt.Fprintf(a.stderr, "Please answer 1 or 2. %q is not one of the choices.\n", answer)
		}
	}
	fmt.Fprintln(a.stderr, "alih setup: no valid selection was made; nothing was changed.")
	return 2
}

// recordMode persists the choice and explains what it does and does not mean.
func (a *App) recordMode(store *usage.Store, chosen, previous usage.Mode) int {
	if err := store.Save(chosen, a.observedAt()); err != nil {
		fmt.Fprintf(a.stderr, "alih setup: %v\n", err)
		return 2
	}
	fmt.Fprintln(a.stdout)
	switch chosen {
	case usage.SelfManaged:
		fmt.Fprintln(a.stdout, "Mode: Self-managed")
		if previous == usage.Assistance {
			// Leaving Assistance must never look like a downgrade or a loss.
			fmt.Fprintln(a.stdout, "Nothing was removed. Your archives, credentials, schedules and")
			fmt.Fprintln(a.stdout, "notification settings are exactly as you left them.")
		}
		fmt.Fprintln(a.stdout, "Alih is free and open source, and you operate it yourself.")
		a.printConnectorGuidance()
	case usage.Assistance:
		fmt.Fprintln(a.stdout, "Mode: Alih Assistance (intent recorded locally)")
		fmt.Fprintln(a.stdout, "Assistance activation: not connected")
		fmt.Fprintln(a.stdout)
		fmt.Fprintln(a.stdout, "Alih itself stays free and open source. Assistance is an optional")
		fmt.Fprintln(a.stdout, "service where setup and operation are handled for you; it does not")
		fmt.Fprintln(a.stdout, "unlock anything, because nothing here is locked.")
		fmt.Fprintln(a.stdout)
		fmt.Fprintln(a.stdout, "It is not available yet. Nothing was sent anywhere, no account was")
		fmt.Fprintln(a.stdout, "created, and this installation keeps working exactly as before.")
		fmt.Fprintln(a.stdout, "Watch https://github.com/rinorouu/alih for when it opens.")
		fmt.Fprintln(a.stdout)
		fmt.Fprintln(a.stdout, "Until then you can keep using Alih yourself, and return to")
		fmt.Fprintln(a.stdout, "self-managed whenever you like with \"alih setup\".")
		a.printConnectorGuidance()
	}
	return 0
}

// printConnectorGuidance points at the existing credential path rather than
// reimplementing authentication inside setup.
func (a *App) printConnectorGuidance() {
	name := a.connectorName()
	if name == "" {
		return
	}
	fmt.Fprintln(a.stdout)
	fmt.Fprintf(a.stdout, "Next, connect a source. This build ships %s.\n", strings.Join(a.availableConnectors(), " and "))
	fmt.Fprintf(a.stdout, "  1. Set %s in your environment.\n", config.CredentialEnvironmentVariable(name))
	fmt.Fprintln(a.stdout, "  2. Run \"alih auth\" to verify and save it.")
	fmt.Fprintln(a.stdout, "  3. Run \"alih backup\" to create and verify a backup.")
	if others := a.otherConnectors(); len(others) > 0 {
		fmt.Fprintf(a.stdout, "Use \"alih --connector %s setup\" to set one of the others up instead.\n", others[0])
	}
}

// interactive reports whether a person is present to answer a question.
//
// Whether a stream is a terminal is a platform question, answered by
// isTerminal in terminal_windows.go and terminal_other.go. Setup only decides
// what to do with the answer.
func (a *App) interactive() bool {
	if a.options.Interactive != nil {
		return *a.options.Interactive
	}
	file, ok := a.stdin().(*os.File)
	if !ok {
		return false
	}
	return isTerminal(file)
}

func (a *App) stdin() io.Reader {
	if a.options.Stdin != nil {
		return a.options.Stdin
	}
	return os.Stdin
}

func (a *App) usageStore() *usage.Store { return usage.NewStore(a.options.UsageStatePath) }
