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

	"alih/internal/buildinfo"
	"alih/internal/cli"
	"alih/internal/config"
	"alih/internal/connector/clickup"
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
	clickUpClient := clickup.NewClient(nil)
	// The composition root is the one place that knows which connectors this
	// build ships. Core coordinators receive the adapters rather than import
	// them, so a second connector is added here and nowhere else.
	archiveVerifier := verifier.New(clickup.FieldSemantics{})
	// One release identity reaches every artifact this process writes.
	version := buildinfo.Version
	app := cli.New(os.Stdout, os.Stderr, logger, cli.Options{
		Authenticator:       clickUpClient,
		Scanner:             clickUpClient,
		Extractor:           clickUpClient,
		Exporter:            exporter.NewWithVersion(nil, version, clickup.Normalizer{}),
		Verifier:            archiveVerifier,
		Reporter:            reporter.NewWithVersion(archiveVerifier, version, clickup.Normalizer{}),
		Organizer:           organize.New(archiveVerifier, version),
		CredentialStore:     credentials.NewFileStore(""),
		EnvironmentToken:    cfg.ClickUpToken,
		EnvironmentTokenSet: cfg.ClickUpTokenSet,
		// A credential injected per run stays where its owner put it when
		// ALIH_SAVE_CREDENTIAL is off.
		SaveEnvironmentCredential: cfg.SaveCredential,
		Version:                   version,
	})

	return app.Run(args)
}
