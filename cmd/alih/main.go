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
	archiveVerifier := verifier.New()
	app := cli.New(os.Stdout, os.Stderr, logger, cli.Options{
		Authenticator:       clickUpClient,
		Scanner:             clickUpClient,
		Extractor:           clickUpClient,
		Exporter:            exporter.New(nil),
		Verifier:            archiveVerifier,
		Reporter:            reporter.New(archiveVerifier),
		CredentialStore:     credentials.NewFileStore(""),
		EnvironmentToken:    cfg.ClickUpToken,
		EnvironmentTokenSet: cfg.ClickUpTokenSet,
		Version:             buildinfo.Version,
	})

	return app.Run(args)
}
