# Alih Alpha

- Version: `0.2.3`
- Status: Alpha
- Reference connector: ClickUp

Alih creates a local, portable, independently verified archive of your ClickUp
data.

This is an early Alpha release intended for testing with real ClickUp
Workspaces.

Alih is read-only. It does not modify your ClickUp Workspace.

## What Alih Does

Alih reads data available through the ClickUp API and creates a local archive
containing:

- Workspace structure;
- tasks and subtasks;
- comments;
- supported task relationships;
- Custom Field definitions and observed values;
- attachment metadata and retrieved attachment files;
- raw source evidence;
- a portable SQLite database;
- archive metadata and checksums;
- an independent verification result;
- a human-readable Recovery Report.

Some ClickUp capabilities are not yet fully supported. Alih reports these
limitations instead of claiming they were preserved.

## Installation

### Release binary

A release publishes platform binaries together with one SHA-256 checksum file.
Example Linux files:

```text
alih-linux-amd64
SHA256SUMS
```

Make the binary executable:

```bash
chmod +x alih-linux-amd64
```

Optional: rename it for convenience:

```bash
mv alih-linux-amd64 alih
```

Check the version:

```bash
./alih --version
```

Expected for this release:

```text
alih 0.2.3
```

### Build from source

Alih is free and open-source software. Building it requires Go 1.22 or later
and cgo.

```bash
git clone https://github.com/rinorouu/alih.git
cd alih
go build -o alih ./cmd/alih
```

## Verify the Download

Before running a release binary, verify it against the matching entry in
`SHA256SUMS`:

```bash
grep 'alih-linux-amd64$' SHA256SUMS | sha256sum --check
```

Expected:

```text
alih-linux-amd64: OK
```

Do not use the binary if the checksum does not match the checksum provided with
the release.

## Authenticate with ClickUp

Alih Alpha currently uses a ClickUp personal API token.

Set the token temporarily in your shell:

```bash
export ALIH_CLICKUP_TOKEN='YOUR_CLICKUP_TOKEN'
```

Then run:

```bash
./alih auth
```

Alih verifies the credential with ClickUp and stores it locally for future
runs.

After successful authentication, you may remove the token from the current
shell environment:

```bash
unset ALIH_CLICKUP_TOKEN
```

Alih does not store your ClickUp credential inside backup archives or Recovery
Reports.

Do not send your ClickUp token when reporting a bug.

## Create a Backup

Run:

```bash
./alih backup
```

If exactly one accessible ClickUp Workspace exists, Alih selects it
automatically.

If your account has multiple accessible Workspaces, specify one explicitly:

```bash
./alih backup --workspace-id WORKSPACE_ID
```

Alih will run the backup pipeline:

```text
Scan
  ↓
Extract
  ↓
Build portable archive
  ↓
Verify
  ↓
Generate Recovery Report
```

A backup is not considered successful until verification completes.

## Successful Backup

A successful run looks similar to:

```text
ALIH — BACKUP COMPLETE

Workspace: Example Workspace
Status: VERIFIED_WITH_LIMITATIONS

Archive:
/home/user/Alih/Example-Workspace/2026-08-30T075208Z/archive

Recovery report:
/home/user/Alih/Example-Workspace/2026-08-30T075208Z/recovery-report.html

Your ClickUp data was not modified.
```

Alih only considers these verification results successful:

```text
VERIFIED
VERIFIED_WITH_LIMITATIONS
```

## What `VERIFIED_WITH_LIMITATIONS` Means

`VERIFIED_WITH_LIMITATIONS` does not mean the archive is corrupt.

It means Alih successfully proved the integrity of the data within its
supported scope, while one or more known source or capability limitations
remain.

For example, a source API may not provide everything required to reconstruct
every ClickUp feature or guarantee a single atomic point-in-time snapshot.

The Recovery Report explains the specific limitations for your archive.

Alih deliberately preserves these limitations instead of presenting a stronger
recovery claim than the evidence supports.

## Failed Backups

A failed backup is never presented as complete.

Possible failure output may include:

```text
Backup was not completed.
```

The command exits with a non-zero exit code.

If Alih preserves a failed working directory for diagnosis, it is not a
verified backup and should not be treated as a recovery source.

Do not manually rename a failed or partial directory to make it look complete.

## Backup Location

By default, backups are stored under:

```text
~/Alih/<workspace>/<timestamp>/
```

A completed backup contains the sealed portable archive and its Recovery
Report.

Conceptually:

```text
~/Alih/
└── Example-Workspace/
    └── <timestamp>/
        ├── archive/
        │   ├── alih.db
        │   ├── attachments/
        │   ├── raw/
        │   ├── manifest.json
        │   └── schema.json
        └── recovery-report.html
```

Do not modify files inside `archive/` if you want the archive to continue
passing verification.

## Recovery Report

Open `recovery-report.html` in a web browser.

The report explains:

- what Alih archived;
- what Alih verified;
- archive integrity;
- entity coverage;
- attachment integrity;
- capability coverage;
- known limitations;
- discrepancies or unresolved items;
- the final recovery conclusion.

The report describes archived evidence. It is not a statement about the current
state of your ClickUp Workspace after the backup was created.

## Privacy

Alih Alpha operates locally.

Your portable archive remains on your machine.

Alih does not require an Alih cloud service to create or verify the archive.

Your archive may contain sensitive Workspace data, including task content and
attachments.

Treat the backup directory as sensitive data and protect it accordingly.

## Current Alpha Limitations

Alih Alpha does not claim to reproduce every ClickUp feature.

The Recovery Report is the authoritative explanation of what was and was not
proven for a specific archive.

Current Alpha does not provide:

- restore to ClickUp;
- migration to another SaaS;
- scheduled backups;
- background backups;
- cloud storage;
- desktop GUI;
- production OAuth;
- additional SaaS connectors.

These are intentionally outside the scope of this Alpha release.

## Reporting a Problem

When reporting an issue, please include:

```text
Alih version:
Operating system:
Command:
Approximate Workspace size:
Final verification status:
Failed stage:
Error message:
```

You can get the Alih version with:

```bash
./alih --version
```

Please do not send:

- your ClickUp API token;
- credential files;
- private task contents;
- attachments;
- the entire backup archive;

unless you intentionally choose to share specific data for debugging and
understand what it contains.

## Useful Diagnostic Commands

Show help:

```bash
./alih --help
```

Show version:

```bash
./alih --version
```

Verify an existing archive:

```bash
./alih verify --archive /home/user/path/to/archive
```

Generate a text Recovery Report:

```bash
./alih report --archive /home/user/path/to/archive
```

These commands do not modify the ClickUp source.

## Alpha Feedback

This Alpha exists to test Alih against real-world ClickUp Workspaces.

Useful feedback includes:

- Was setup understandable?
- Was authentication confusing?
- Did the backup complete?
- How long did it take?
- Was the Recovery Report understandable?
- Did `VERIFIED_WITH_LIMITATIONS` make sense?
- Did Alih behave as you expected?
- Would you run the backup again?
- What would prevent you from using Alih regularly?

Bug reports and confusing behavior are valuable during this phase.

## Important

Alih is currently Alpha software.

Keep your existing backup and data-protection practices in place while testing
it.

Alih makes recovery claims only for evidence it can independently verify.

Take control of your SaaS data.
