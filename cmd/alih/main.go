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

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"alih/internal/buildinfo"
	"alih/internal/cli"
	"alih/internal/config"
	"alih/internal/connector"
	"alih/internal/connector/clickup"
	"alih/internal/connector/notion"
	"alih/internal/credentials"
	"alih/internal/exporter"
	"alih/internal/logging"
	"alih/internal/organize"
	"alih/internal/reporter"
	"alih/internal/verifier"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "alih: configuration: %v\n", err)
		return 2
	}

	logger := logging.New(os.Stderr, cfg.LogLevel)
	// The composition root is the one place that knows which connectors this
	// build ships. Core coordinators receive the adapters rather than import
	// them, so a connector is added here and nowhere else.
	// Which connector this invocation drives. Selection lives here rather than
	// in Core: Core still receives exactly one source adapter and never learns
	// that more than one exists.
	selected, args, err := selectConnector(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "alih: %v\n", err)
		return 2
	}

	// Every connector's interpreter and normalizer is registered regardless of
	// which one is selected, so "alih verify" and "alih report" still read an
	// archive produced by the other one.
	archiveVerifier := verifier.New(clickup.FieldSemantics{}, notion.FieldSemantics{})
	normalizers := []exporter.Normalizer{clickup.Normalizer{}, notion.Normalizer{}}
	// One release identity reaches every artifact this process writes.
	version := buildinfo.Version
	// The credential is read from the variable the selected connector names, so
	// adding a connector brings its variable with it and Core never has to know
	// that ClickUp's is ALIH_CLICKUP_TOKEN.
	environmentToken, environmentTokenSet := config.CredentialFromEnvironment(selected.source.Name())
	app := cli.New(os.Stdout, os.Stderr, logger, cli.Options{
		Authenticator:   selected.source,
		Scanner:         selected.source,
		Extractor:       selected.source,
		Exporter:        exporter.NewWithVersion(nil, version, normalizers...),
		Verifier:        archiveVerifier,
		Reporter:        reporter.NewWithVersion(archiveVerifier, version, clickup.Normalizer{}, notion.Normalizer{}),
		Organizer:       organize.New(archiveVerifier, version),
		CredentialStore: credentials.NewFileStore(""),
		// The composition root tells Core which connectors this build ships;
		// Core never discovers them.
		AvailableConnectors: []string{"clickup", "notion"},
		EnvironmentToken:    environmentToken,
		EnvironmentTokenSet: environmentTokenSet,
		// A credential injected per run stays where its owner put it when
		// ALIH_SAVE_CREDENTIAL is off.
		SaveEnvironmentCredential: cfg.SaveCredential,
		Version:                   version,
	})

	return app.Run(args)
}

// sourceConnector is one adapter this build can drive.
type sourceConnector struct {
	source interface {
		connector.Authenticator
		connector.Scanner
		connector.Extractor
	}
}

// selectConnector reads --connector, or ALIH_CONNECTOR, and returns the adapter
// to drive along with the remaining arguments.
//
// The flag is parsed here rather than by any command, because the choice is
// which adapter Core is handed, not an option Core interprets. It defaults to
// ClickUp so every existing invocation keeps working unchanged.
func selectConnector(args []string) (sourceConnector, []string, error) {
	available := map[string]sourceConnector{
		"clickup": {source: clickup.NewClient(nil)},
		"notion":  {source: notion.NewClient(nil)},
	}
	names := make([]string, 0, len(available))
	for name := range available {
		names = append(names, name)
	}
	sort.Strings(names)

	requested := strings.TrimSpace(os.Getenv("ALIH_CONNECTOR"))
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--connector":
			if index+1 >= len(args) {
				return sourceConnector{}, nil, fmt.Errorf("--connector requires a name; available: %s", strings.Join(names, ", "))
			}
			requested = strings.TrimSpace(args[index+1])
			index++
		case strings.HasPrefix(argument, "--connector="):
			requested = strings.TrimSpace(strings.TrimPrefix(argument, "--connector="))
		default:
			remaining = append(remaining, argument)
		}
	}
	if requested == "" {
		requested = "clickup"
	}
	chosen, known := available[requested]
	if !known {
		return sourceConnector{}, nil, fmt.Errorf("unknown connector %q; available: %s", requested, strings.Join(names, ", "))
	}
	return chosen, remaining, nil
}
