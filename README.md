# Alih

Alih is a free and open-source, local-first SaaS data portability tool. It is
designed to answer a specific question:

> What can actually be recovered outside the source SaaS, and can we prove that
> the exported representation is complete within the supported scope?

Alih connects to a SaaS through its official API, preserves raw responses,
builds a portable local representation, independently verifies the resulting
archive, and reports what it can and cannot prove.

**Status: Alpha.** Alih is early software and should be used alongside your
existing backup and data-protection practices. ClickUp is the first and
reference connector, but it is not part of Alih's core identity.

## Principles

- **Read-only.** Every request to the source is a `GET`. Alih never creates,
  modifies, or deletes anything at the source.
- **Local-first.** The current Alpha workflow requires no Alih backend, cloud
  storage, or telemetry. Your data and credential stay on your machine.
- **No silent loss.** Expected and archived counts are reconciled. A shortfall
  fails the archive instead of passing quietly.
- **Evidence over assumption.** `SUPPORTED`, `PARTIAL`, `UNSUPPORTED`,
  `UNAVAILABLE`, `UNKNOWN`, and `FAILED` remain distinct.
- **Fail closed.** Corruption, missing data, broken references, count
  mismatches, and unproven claims prevent a clean verified result.
- **Credentials stay out.** The token never reaches logs, archives, reports,
  or command output, and is never accepted as a command-line argument.

If Alih expects 12,491 records and archives 12,487, the result is not
`VERIFIED`. It is `INCOMPLETE`, with the unresolved records reported.

## Install from source

Building Alih requires Go 1.22 or later and cgo because the archive uses
SQLite.

```bash
git clone https://github.com/rinorouu/alih
cd alih
go build -o alih ./cmd/alih
```

Release binaries may also be provided. Verify a release binary against its
published checksum before running it; see [ALPHA.md](ALPHA.md).

## Quick start

The primary Alpha workflow is:

```text
alih auth
alih backup
```

For initial authentication, provide a ClickUp personal API token through the
environment. Alih verifies and stores it locally for subsequent runs.

```bash
export ALIH_CLICKUP_TOKEN='YOUR_CLICKUP_TOKEN'
./alih auth
unset ALIH_CLICKUP_TOKEN
```

Create and independently verify a backup:

```bash
./alih backup
```

If more than one accessible ClickUp Workspace exists, select one explicitly:

```bash
./alih backup --workspace-id WORKSPACE_ID
```

A completed backup is stored under
`~/Alih/<workspace>/<UTC-start-time>/`. The directory contains a sealed
`archive/` and a `recovery-report.html` beside it. A backup is not reported as
successful until independent verification finishes.

## Advanced commands

The individual pipeline commands remain available for inspection,
troubleshooting, development, and advanced use:

```text
alih scan [--workspace-id ID]
alih extract --output PATH [--workspace-id ID]
alih export --snapshot PATH [--output PATH]
alih verify --archive PATH
alih report --archive PATH [--format text|html|json] [--output PATH]
```

`scan` establishes an inventory and makes no portability claim. `extract`
preserves raw evidence without building a portable model. `export` creates an
archive marked `CREATED_UNVERIFIED`; creating an archive is not the same as
verifying it. `verify` and `report` read the archive without contacting or
modifying the source.

## The archive

```text
archive/
├── alih.db          portable SQLite representation, queryable without ClickUp
├── manifest.json    archive inventory, capability states, and checksums
├── schema.json      description of alih.db
├── raw/             original API evidence and checksums
└── attachments/     retrieved binaries
```

Every portable row retains its original source identifiers and a path into the
raw evidence, allowing archived objects to be traced back to their source
response.

The Recovery Report is written beside the archive rather than inside it. The
archive is sealed by manifest checksums, so adding the report to it would make
verification fail.

## Result states

| State | Meaning |
| --- | --- |
| `VERIFIED` | Everything expected within supported scope was archived and proven. |
| `VERIFIED_WITH_LIMITATIONS` | Supported data was proven; disclosed source or capability limitations remain. |
| `INCOMPLETE` | Alih expected supported data that the archive does not contain. |
| `FAILED` | Alih could not establish a trustworthy archive. |

Only `VERIFIED` and `VERIFIED_WITH_LIMITATIONS` are successful backup results.
`INCOMPLETE` and `FAILED` exit non-zero.

## What Alih does not claim

- **Not a point-in-time snapshot.** ClickUp exposes no atomic snapshot across
  the endpoints Alih traverses. Archived records may reflect different moments
  during extraction.
- **Not proof of account-wide visibility.** An archive contains what the
  authenticating identity could access. Archive-internal evidence cannot prove
  that the identity could see every object in a Workspace.
- **No executable Custom Field semantics.** Formula, rollup, and other computed
  fields are archived as observed values; their behavior is not reconstructed.
- **Not a ClickUp replica.** Alih preserves its documented portable model, not
  every source-side permission, automation, view, or rendering behavior.
- **Not a statement about the source today.** Verification reads the archive
  only and cannot prove that the source still matches it.

Alih does not use scraping, undocumented endpoints, or browser automation to
turn unavailable source data into supported data.

## Current Alpha scope

The Alpha has no web or desktop UI, Alih cloud backend, billing, telemetry,
scheduled backups, continuous sync, restore workflow, cross-SaaS migration,
production OAuth onboarding, or additional connector.

Alih OSS is software that users may run themselves. The maintainers may choose
to build an optional managed service on top of the open-source core in the
future. This is a boundary, not a promise of availability, features, pricing,
or a change to the current Alpha scope.

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
```

`AGENTS.md` describes how changes to this repository are expected to be made.
`PRD.md` is the product source of truth; code does not redefine it.

## License

Alih is free and open-source software licensed under the
[Apache License 2.0](LICENSE). You may run, modify, and redistribute it,
commercially or otherwise, subject to that license.

Copyright 2025 rinorouu.
