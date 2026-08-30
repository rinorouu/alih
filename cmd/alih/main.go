package main

import (
	"fmt"
	"os"

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
	})

	return app.Run(args)
}
