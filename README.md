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

If you would rather Alih kept no copy — because the token is injected per run
from a secret store, or the machine is ephemeral — set
`ALIH_SAVE_CREDENTIAL=0`. `backup`, `scan`, and `extract` then use the token in
the environment and save nothing. `alih auth` still saves, because saving is
what that command is for. Caching is never a precondition for the work either:
if the credential has been verified but cannot be written to disk, Alih says so
and carries on rather than losing a backup over it.

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
alih status [--json] [--refresh] [--reconcile]
alih notify [--json]
alih notify --test DESTINATION_ID [--json]
alih schedule check [--json]
alih schedule preview ID [--platform linux|darwin|windows] [--json]
alih schedule inspect ID [--json]
alih schedule install ID
alih schedule remove ID
alih scan [--workspace-id ID] [--json]
alih extract --output PATH [--workspace-id ID]
alih export --snapshot PATH [--output PATH]
alih verify --archive PATH
alih report --archive PATH [--format text|html|json] [--output PATH]
alih organize --archive PATH --output PATH [--json]
```

`status` reports what Alih has recorded about its own runs: the last attempt,
the last successful backup, the verification result and whether the archive it
refers to is still unchanged, and the connector health observed at the time,
each with its own age. It also summarises the recorded event history for each
scope, as context that never decides the status. It reads local records only
and makes no source request unless `--refresh` is given, which makes exactly
one authentication request.
`--reconcile` reads the backup destination, verifies every archive found there,
and records what that proves, so backups Alih has no record of become visible
again; failed and abandoned runs are reported but never counted as backups.
Its exit code is `0` healthy, `1` needs attention, `3` nothing recorded that can
be called healthy, and `4` local state that cannot be read.

Notifications are disabled by default. `alih notify` validates the local
configuration and makes no network request; `alih notify --test ID` is the
explicit live-test path and replays the newest real recorded event selected by
that destination. Normal operations deliver only the stable event types each
destination explicitly allowlists. Delivery is synchronous, has bounded
timeouts and retries, refuses redirects, and carries a stable idempotency key.
A failed notification is visible in `alih status` but never changes a verified
archive or turns a successful backup into a failed one.

The one supported transport is an HTTPS webhook. Create
`notifications.json` in Alih's user configuration directory: normally
`~/.config/alih/` on Linux, `~/Library/Application Support/alih/` on macOS, or
`%AppData%\alih\` on Windows. On Linux and macOS, keep the directory `0700` and
the file `0600`.

```json
{
  "schema_version": 1,
  "destinations": [
    {
      "id": "ops",
      "enabled": true,
      "type": "webhook",
      "url": "https://hooks.example.com/alih",
      "events": [
        "operation.failed",
        "connector.unhealthy",
        "authentication.problem"
      ],
      "secret_env": "ALIH_NOTIFY_OPS_TOKEN",
      "timeout_seconds": 10,
      "max_attempts": 3
    }
  ]
}
```

`secret_env` is optional. When present, Alih reads that environment variable
at delivery time and sends its value as a bearer token. Only the variable name
is stored; its value must not be put in the JSON file. Status and command
output show only the webhook scheme and host, never its path, query, fragment,
bearer value, response body, or other destination-controlled text. Retryable
delivery is attempted only during the current invocation; there is no hidden
background worker or durable outbox. A receiving webhook should enforce the
provided idempotency key if duplicate processing matters.

Recurring backups use the operating system's user-level scheduler, not an Alih
daemon: systemd user timers on Linux and WSL, launchd LaunchAgents on macOS,
and per-user Task Scheduler tasks on Windows. Configuration is disabled until
the user creates `schedules.json` beside `notifications.json`. The initial
portable cadence is deliberately daily local civil time; the native scheduler
owns daylight-saving transitions. `run_once` asks it to coalesce a missed
trigger after sleep/offline time rather than queue every missed run.

```json
{
  "schema_version": 1,
  "schedules": [
    {
      "id": "daily-main",
      "enabled": true,
      "operation": "backup",
      "connector": "clickup",
      "workspace_id": "WORKSPACE_ID",
      "destination": "/absolute/path/to/Alih",
      "cadence": {
        "frequency": "daily",
        "at": "02:30",
        "timezone": "local",
        "missed_run_policy": "run_once"
      }
    }
  ]
}
```

On Windows, use an absolute drive path such as
`C:\\Users\\YOUR_NAME\\Alih`. `alih schedule check` validates only the local
file. `preview` prints deterministic artifacts and exact argument arrays
without changing the machine. `install` and `remove` are the only mutating
actions and always target the current user; `inspect` compares installed files
and asks the native scheduler whether the task is registered.

Generated scheduler files contain the absolute Alih executable, Workspace ID,
and destination, but no token, bearer value, or environment secret. The
scheduled account must already be able to read Alih's saved credential and
write the destination. The executable's directory does not need to be in
`PATH`; its absolute path and working directory are recorded. Locale is not
used for cadence parsing, and timezone is the machine's local timezone. WSL
requires a working systemd user session. Linux execution after logout may
require an administrator to enable user lingering; Alih never escalates
privileges automatically. The current macOS LaunchAgent and Windows
InteractiveToken task run in the user's logged-in session.

Every scheduled trigger invokes the ordinary `alih backup` pipeline, including
independent verification, status, events, and optional notifications. A
portable OS-handle lock covers connector + Workspace + destination. If another
backup owns that scope, the new trigger exits non-zero as `SKIPPED`, records
`OPERATION_OVERLAP`, and queues nothing. A crash or uncatchable termination
releases the OS handle automatically; the retained lock file is inspection
metadata and never acts as a stale lock.

`organize` builds an optional, disposable browsing view beside a canonical
archive: one directory per Workspace, container, and collection, a Markdown
page per record, separately copied attachments, and a `provenance.json` index
mapping every generated file back to its portable identifier, original source
identifier, and raw evidence path. It is derived data for reading, not a
restore source and not a ClickUp replica.

The archive is independently verified before generation and again immediately
before publication; only `VERIFIED` and `VERIFIED_WITH_LIMITATIONS` are
accepted, and the disclosed limitations are repeated in the view itself. The
canonical archive is opened read-only and is never modified. The view is built
in a private staging directory and published with a single rename, so an
interrupted run publishes nothing. The output must not already exist: Alih
never merges into or edits a previous view, because regenerating one is
cheaper than reconciling one. Two runs over the same archive produce identical
content, so a view can be deleted and rebuilt at any time.

Generated names keep the original Unicode text, without normalisation, and add
a short prefix of the portable identifier so that two records with the same
name — or names differing only in case — never collide. Characters Windows
reserves are replaced, reserved basenames are escaped, and each component is
length-bounded so the deepest generated path stays usable on all three
supported platforms.

`scan` establishes an inventory and makes no portability claim; `--json`
prints that inventory together with the connector capability contract and
the operational health assessment of the same run. `extract` preserves raw
evidence without building a portable model. `export` creates an
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
response. The portable model is source-neutral: containers, collections, and
records, each keeping the connector's own name for what it is. `manifest.json`
records neutral totals and the connector's own vocabulary beside them, so an
archive describes its source in that source's words without Alih having to know
them.

The Recovery Report is written beside the archive rather than inside it. The
archive is sealed by manifest checksums, so adding the report to it would make
verification fail.

`raw/` holds the provider's responses byte for byte, because that is what makes
the archive provable. Those exact bytes include whatever ClickUp put in them,
which for attachments is a short-lived signed download URL. Alih never writes
its own token into an archive, and a signed provider URL never reaches
`alih.db`, `manifest.json`, the verification result, the Recovery Report, or an
organized view. Treat an archive as being as sensitive as the Workspace it came
from, and share it accordingly.

### Archive format compatibility

`manifest.json` carries a schema version, and Alih states plainly which
versions it reads.

| | |
| --- | --- |
| Written by this version | schema 3 |
| Read and verified by this version | schema 2 and 3 |

An archive written by an older Alih keeps the format it was sealed with. Alih
verifies, reports, and organizes it as it is, and never rewrites or upgrades it
in place.

Going the other way does not work, by design: **an Alih older than 0.2.5 will
refuse a schema 3 archive.** It reports a failed `manifest_integrity` check,
names the schema version it found and the one it supports, and exits non-zero.
The archive is not damaged — the older binary cannot make claims about a format
it does not know, so it declines to make any. Verify with 0.2.5 or later
instead of downgrading.

An archive whose schema is outside the readable range is refused explicitly and
is never interpreted under whichever schema the running build happens to
implement.

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
resident scheduler, continuous sync, restore workflow, cross-SaaS migration,
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
