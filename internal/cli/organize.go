// Copyright 2025 rinorouu
// Licensed under the Apache License, Version 2.0.

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

const organizeHelpText = `Build a deterministic browsing view from a verified archive.

Usage:
  alih organize --archive PATH --output PATH [--json]

The archive is independently verified before generation and again before the
new view is published. VERIFIED and VERIFIED_WITH_LIMITATIONS are accepted;
INCOMPLETE, FAILED, corrupt, and unverified archives are refused. The output
must not already exist and must not contain or be contained by the archive.

The view contains Markdown records, separately copied attachments, and a
machine-readable provenance index. It is disposable derived data, not a
restore source. Alih never changes the canonical archive or merges into an
existing view.
`

func (a *App) runOrganize(args []string) int {
	flags := flag.NewFlagSet("alih organize", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	flags.Usage = func() { fmt.Fprint(a.stdout, organizeHelpText) }
	archivePath := flags.String("archive", "", "verified M4 archive directory")
	outputPath := flags.String("output", "", "new organized-view directory")
	asJSON := flags.Bool("json", false, "print a machine-readable result")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*archivePath) == "" || strings.TrimSpace(*outputPath) == "" {
		fmt.Fprintln(a.stderr, "alih organize: --archive and --output are required; positional arguments are not accepted")
		return 2
	}
	if a.options.Organizer == nil {
		fmt.Fprintln(a.stderr, "alih organize: organized-view dependencies are unavailable")
		return 1
	}
	// An interrupted generation must remove its staging directory rather than
	// leave a partial view behind, so organization observes the same signals
	// the rest of the pipeline does.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := a.options.Organizer.Build(ctx, *archivePath, *outputPath)
	if err != nil {
		fmt.Fprintf(a.stderr, "alih organize: %s\n", safeError(err, ""))
		fmt.Fprintln(a.stderr, "alih organize: no organized view was published; the canonical archive was not modified")
		return 1
	}
	if *asJSON {
		encoder := json.NewEncoder(a.stdout)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(a.stderr, "alih organize: encode result: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(a.stdout, "Organized view: %s\n", displayValue(result.OutputPath))
	fmt.Fprintf(a.stdout, "Verification: %s\n", displayValue(result.Verification))
	fmt.Fprintf(a.stdout, "Manifest checksum: %s\n", displayValue(result.ManifestChecksum))
	fmt.Fprintf(a.stdout, "Files: %d (%d attachments)\n", result.Files, result.Attachments)
	fmt.Fprintln(a.stdout, "The canonical archive was not modified.")
	return 0
}
