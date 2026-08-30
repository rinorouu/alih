# Alih

Alih is a local-first SaaS data portability tool. Its purpose is not to export
data, but to answer a harder question:

> What can actually be recovered outside the source SaaS, and can we prove that
> the exported representation is complete within the supported scope?

Alih connects to a SaaS through its official API, preserves the raw responses,
builds a portable local representation, independently verifies the resulting
archive, and reports what it can and cannot prove about it.

**Status: experimental (V0). Not production software.** V0 is an engineering
validation experiment with one connector, ClickUp. ClickUp is an implementation
target, not part of Alih's identity. See `PRD.md` for the full specification.

## The idea

Most SaaS platforms offer some form of export. But *exportable* is not the same
as *portable, reconstructable, or verifiably complete*. Data gets split across
mechanisms, reduced to links, stripped of relationships, or left impossible to
validate after the fact.

Alih's guarantee is narrower and stronger: **everything it claims to support is
either archived and verified, or explicitly reported as incomplete, unavailable,
or unsupported.** Uncertainty is never converted into success.

If Alih expects 12,491 records and archives 12,487, the result is not
`EXPORT COMPLETE`. It is `INCOMPLETE`, with the four unresolved records named.

## Guarantees

- **Read-only.** Every request to the source is a `GET`. Alih never creates,
  modifies, or deletes anything at the source.
- **Local-first.** No Alih backend, no cloud storage, no telemetry. Your data
  and your credential stay on your machine.
- **No silent loss.** Expected versus archived counts are reconciled at three
  independent layers. A shortfall fails the archive rather than passing quietly.
- **Evidence over assumption.** `SUPPORTED`, `PARTIAL`, `UNSUPPORTED`,
  `UNAVAILABLE`, `UNKNOWN` and `FAILED` are preserved exactly. Verification
  never promotes one into another.
- **Fail closed.** Corruption, missing data, broken references, count
  mismatches, and unproven claims all prevent a clean verified result.
- **Credentials stay out.** The token never reaches logs, archives, reports, or
  command output, and never appears as a command-line argument.

## Install

Requires Go 1.22+ and cgo (the archive is SQLite).

```
git clone https://github.com/rinorouu/alih
cd alih
go build -o alih ./cmd/alih
```

## Use

```
alih auth                                   # verify and store a ClickUp token
alih scan                                   # inventory a Workspace, no archive
alih extract --output ./snapshot            # raw API evidence only
alih export  --snapshot ./snapshot --output ./archive
alih verify  --archive ./archive
alih report  --archive ./archive [--format text|html|json]
```

Provide the token once via the environment; it is never accepted as an
argument:

```
ALIH_CLICKUP_TOKEN=... alih auth
```

The commands are deliberately separate. `scan` establishes an inventory and
makes no portability claim. `extract` preserves raw evidence and builds no
model. `export` builds an archive and marks it `CREATED_UNVERIFIED` — creating
an archive is not the same as verifying one. `verify` and `report` are strictly
read-only with respect to the archive, so neither can turn a damaged archive
into a clean result.

## The archive

```
archive/
├── alih.db          portable SQLite representation, queryable without ClickUp
├── manifest.json    what Alih believes the archive contains, with checksums
├── schema.json      description of alih.db, so the archive explains itself
├── raw/             the original API responses, with their checksums
└── attachments/     retrieved binaries
```

Every row keeps its original source identifiers and a path into `raw/`, so any
archived object can be traced back to the exact response it came from.

The recovery report is written *beside* the archive, not inside it: an archive
is sealed by manifest checksums, and adding a file would make `alih verify`
fail.

## Result states

| State | Meaning |
| --- | --- |
| `VERIFIED` | Everything expected within supported scope was archived and proven. |
| `VERIFIED_WITH_LIMITATIONS` | Proven within supported scope; source limitations remain that the archive cannot resolve. |
| `INCOMPLETE` | Alih expected supported data that the archive does not contain. |
| `FAILED` | Alih cannot establish a trustworthy archive. |

`INCOMPLETE` and `FAILED` exit non-zero and are never presented as verified. In
practice a real ClickUp archive lands on `VERIFIED_WITH_LIMITATIONS`, because
some limitations below can never be resolved.

## What Alih does not claim

These are properties of the source and its API, not bugs, and no archive
resolves them:

- **Not a point-in-time snapshot.** ClickUp exposes no atomic snapshot, so
  records may reflect different moments of the traversal. Alih records this as
  `atomic: false` and never claims consistency at any single instant.
- **One identity's view.** An archive contains what the authenticating account
  could reach. A narrower credential simply returns fewer objects, which then
  reconcile perfectly with each other, so no archive-internal evidence can tell
  a complete Workspace from a partial one. Alih records which account extracted
  the archive and reports this claim as permanently unproven.
- **No executable Custom Field semantics.** Formula, rollup and other computed
  fields are archived as observed values. Their behaviour is not reconstructed.
- **Not a ClickUp replica.** Only the portable representation described in
  `schema.json` is preserved. Permissions, automations, views and source-side
  rendering are not archived.
- **Not a statement about the source today.** Verification reads the archive
  only. It cannot prove the source still matches it.

Alih does not use scraping, undocumented endpoints, or browser automation to
turn `UNAVAILABLE` into `SUPPORTED`.

## Not in V0

No web or desktop UI, no cloud backend, no accounts, billing, or telemetry, no
scheduled backups or continuous sync, no AI features, no additional connectors,
no cross-SaaS migration, and no restore into ClickUp or anywhere else. V0
deliberately proves the portability model before anything is built on top of it.

## Development

```
go test ./...
go test -race ./...
go vet ./...
```

`AGENTS.md` describes how changes to this repository are expected to be made.
`PRD.md` is the product source of truth; code does not redefine it.

## License

Not yet licensed. All rights reserved pending a decision after V0.
