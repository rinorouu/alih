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

See [CHANGELOG.md](CHANGELOG.md) for tagged, released, and pending changes.

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

## Install

The official installers select the latest stable release, download the matching
binary and `SHA256SUMS`, and refuse to install unless SHA-256 verification
succeeds. They install at user level and do not use `sudo` or request
Administrator privileges.

The commands below execute the official installer delivered over HTTPS. To
inspect it first, download the same `install.sh` or `install.ps1` URL to a file,
review it, and then run the local file.

### Linux

```bash
curl -fsSL https://github.com/rinorouu/alih/releases/latest/download/install.sh | sh
```

The default location is `~/.local/bin/alih`. If that directory is not already
in `PATH`, the installer prints the directory and the exact binary path to use.

### macOS

```bash
curl -fsSL https://github.com/rinorouu/alih/releases/latest/download/install.sh | sh
```

The default location is `~/.local/bin/alih`. Both Intel and Apple Silicon are
detected automatically.

### Windows

#### WSL — recommended if Smart App Control blocks the native executable

WSL uses Alih's supported Linux build. From a WSL shell, install Alih and
confirm the installation with:

```bash
curl -fsSL https://github.com/rinorouu/alih/releases/latest/download/install.sh | sh
~/.local/bin/alih --version
```

#### Native Windows — available, but currently unsigned

Run in PowerShell:

```powershell
irm https://github.com/rinorouu/alih/releases/latest/download/install.ps1 | iex
```

The default location is `%LOCALAPPDATA%\Alih\bin\alih.exe`. The installer adds
that directory to the current user's `PATH` when needed; open a new terminal
afterward. It never changes the machine-wide `PATH`.

The native Windows executable is currently unsigned. On Windows 11, Microsoft
Smart App Control may block `alih.exe` because its publisher cannot be verified.
If this happens, use Alih through WSL. Keep Smart App Control, Microsoft
Defender, and SmartScreen enabled, and do not change security policies to run
Alih.

Set `ALIH_INSTALL_DIR` before running an installer to choose another absolute,
user-writable directory.

### Manual installation

Prebuilt binaries remain available from
[GitHub Releases](https://github.com/rinorouu/alih/releases):

- `alih-linux-amd64`
- `alih-linux-arm64`
- `alih-windows-amd64.exe`
- `alih-darwin-amd64`
- `alih-darwin-arm64`

Download `SHA256SUMS` from the same release and verify the selected binary's
SHA-256 entry before running it. On Linux and macOS, then make it executable
and place it in a directory in `PATH` under the name `alih`:

```bash
mkdir -p ~/.local/bin
chmod +x alih-<platform>-<architecture>
mv alih-<platform>-<architecture> ~/.local/bin/alih
~/.local/bin/alih --version
```

On Windows, compare `Get-FileHash -Algorithm SHA256` with `SHA256SUMS`, then
install the verified file as `alih.exe` in a user-level directory.

### Build from source

Building Alih requires Go 1.22 or later and cgo because the archive uses
SQLite.

```bash
git clone https://github.com/rinorouu/alih.git
cd alih
go build -o alih ./cmd/alih
```

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
alih auth
unset ALIH_CLICKUP_TOKEN
```

In Windows PowerShell:

```powershell
$env:ALIH_CLICKUP_TOKEN = 'YOUR_CLICKUP_TOKEN'
alih auth
Remove-Item Env:ALIH_CLICKUP_TOKEN
```

After successful verification, Alih stores the token in `credentials.json`
under the operating system's user configuration directory. On Linux and
macOS, Alih requires user-only directory and file permissions. On Windows, the
file relies on the user profile's access controls. Alih does not encrypt this
file, put it in an archive, or print the token in normal command output.

Create and independently verify a backup:

```bash
alih backup
```

If more than one accessible ClickUp Workspace exists, select one explicitly:

```bash
alih backup --workspace-id WORKSPACE_ID
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
production OAuth onboarding, or additional connectors.

## Run Alih yourself

Alih is open-source software that users may build and run themselves. The
current Alpha workflow does not require an Alih-hosted service.

## Alih Assistance

Users who prefer not to operate Alih themselves may be able to use paid Alih
Assistance in the future, where the maintainers operate the same open-source
software on their behalf. Assistance would be optional and would not restrict
the self-run open-source core. No availability or pricing is promised here.

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
```

Building Alih requires Go 1.22 or newer and a working C compiler, because the
SQLite driver uses cgo.

## Contributing

Contributions are welcome. Bug reports, documentation fixes, and small clear
fixes can go straight to an issue or pull request; new features, new commands,
and new connectors should start with an issue so the direction can be agreed
before the code is written.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the process, the principles a change
must not break, and the checks CI runs.

## Security

Do not report security vulnerabilities in a public issue. Use GitHub's private
vulnerability reporting on the
[Security tab](https://github.com/rinorouu/alih/security).

See [SECURITY.md](SECURITY.md) for what is in scope, what to include in a
report, and what to expect after sending one.

## License

Alih is free and open-source software licensed under the
[Apache License 2.0](LICENSE). You may run, modify, and redistribute it,
commercially or otherwise, subject to that license.

Copyright 2025 rinorouu.
