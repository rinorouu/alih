# Project memory — Alih

Orientation for any language model picking this repository up cold. It is
written to save you from re-deriving what has already been decided, and to warn
you about the specific ways this codebase will mislead a careless change.

Read this first, then `README.md` for what Alih does for a user, then the
document listed under whichever area you are touching.

**Trust the code over this file.** Where they disagree, the code is right and
this file is stale — fix it. `internal/hardening` verifies the parts of this
document that can be checked mechanically, so some drift breaks the build, but
not all of it.

---

## What Alih is

A local-first, free, open-source CLI that makes a **verifiable, portable backup
of a ClickUp workspace**. Written in Go. One connector. One non-standard
dependency (`github.com/mattn/go-sqlite3`, because the portable archive is a
SQLite database).

The product promise is not "it backs things up". It is **"when it says the
backup is good, that is provable"**. Nearly every design decision below follows
from that.

## Non-negotiable principles

Violating any of these is a bug even if tests pass. Several have dedicated
tests that will fail loudly.

1. **Fail closed.** Partial, unknown, unavailable, or failed evidence never
   becomes success. A run that cannot prove something says so.
2. **No silent loss, no false success, no hidden ambiguity.** These three are
   the axis everything is measured on. See `HARDENING.md`.
3. **The source is read-only.** Alih never writes to ClickUp.
4. **Archives are sealed and immutable.** Reading, verifying, reporting, or
   organizing an archive must never modify it — not even to "upgrade" it.
5. **No telemetry, no cloud, no phone-home, no feature gate.** Exactly one
   external host is compiled in: `api.clickup.com`. One build, one set of
   capabilities.
6. **Credentials appear nowhere** — not in archives, state, events, reports,
   organized views, generated scheduler files, or logs.
7. **Local state is versioned, atomic, and never silently repaired.** A future
   schema is refused, not guessed at.
8. **Inject clocks, IDs, filesystem roots, transports, and process boundaries**
   so tests never need a network or a real service.

## Layering — the rule that matters most

```
cmd/alih                 composition root; the ONLY place that may import an adapter
  ├─ internal/cli        command surface, human + JSON output, exit codes
  ├─ internal/exporter   M3→M4 coordinator; selects a Normalizer by connector name
  ├─ internal/verifier   M5 coordinator; selects FieldSemantics by the archive's connector
  └─ internal/reporter   M6 coordinator; supplies connector display-name fallback

internal/connector       source-neutral contracts (Authenticator, Scanner,
                         Extractor, CapabilityProvider, capability + health)
internal/connector/clickup   the ONE adapter; all ClickUp knowledge lives here
internal/snapshot        M3 raw evidence (verbatim response bytes)
internal/archive         M4 sealed archive writer
internal/verify          M5 independent verification
internal/report          M6 recovery report document + renderers
internal/organize        derived browsable view from a verified archive
internal/state           local operational state
internal/event           local append-only event log
internal/notify          opt-in HTTPS webhook
internal/schedule        native OS scheduler artifacts
internal/oplock          cross-process operation lock
internal/compat          NO production code — frozen backward-compatibility corpus
internal/conformance     NO production code — fake-connector conformance suite
internal/hardening       NO production code — verifies the review documents

internal/buildinfo       release identity, injected via -ldflags, falls back to "dev"
internal/config          process environment (ALIH_* variables)
internal/logging         slog setup
internal/model           the portable model, source-neutral
internal/sqliteutil      file: URI construction for SQLite
```

**`TestOnlyTheCompositionRootImportsAnAdapter` enforces the top rule.** If you
find yourself importing `internal/connector/clickup` from anywhere except
`cmd/alih`, you are solving the problem the wrong way — pass the adapter in.

## Pipeline

`auth → scan → extract (M3) → export (M4) → verify (M5) → report (M6)`,
orchestrated by `alih backup`. Then optionally `alih organize`.

A backup is only reported successful after **independent verification** passes
and the bundle is atomically published. `operation.completed` is emitted only
after the archive and report exist on disk.

## Versioned contracts

Keep these in sync with the code; `internal/hardening` checks them.

| Contract | Constant | Version |
| --- | --- | --- |
| Capability | `connector.CapabilitySchemaVersion` | 1 |
| Health | `connector.HealthSchemaVersion` | 1 |
| Raw snapshot (M3) | `snapshot.schemaVersion` (unexported) | 2 |
| Archive manifest (M4) | `archive.ArchiveSchemaVersion` | 3 |
| Oldest readable manifest | `archive.MinReadableSchemaVersion` | 2 |
| Recovery report | `report.SchemaVersion` | 1 |
| Organized view | `organize.SchemaVersion` | 1 |
| Operational state | `state.SchemaVersion` | 3 |
| Event | `event.SchemaVersion` | 2 |
| Notification config | `notify.SchemaVersion` | 1 |
| Schedule config | `schedule.SchemaVersion` | 1 |
| Operation lock | `oplock.SchemaVersion` | 1 |
| `alih status --json` | `cli.statusSchemaVersion` | 2 |

Every reader accepts what it knows and **refuses a newer version rather than
guessing**. Bumping a version means writing a compatibility path, not deleting
the old one.

## Shared vocabulary — one meaning, one place

Cross-contract agreement is asserted in `internal/hardening/coherence_test.go`.
Do not introduce a second copy of any of these.

- **Scope identity** is always `connector + workspace_id + destination`. State,
  the operation lock, the event log, and schedule definitions all key on it.
- **Operations / stages** live in `state.Operations()` / `state.Stages()`. The
  event contract reads them from there. Do not re-list them.
- **Outcomes**: `STARTED`, `SUCCEEDED`, `FAILED`, `SKIPPED`.
- **Verification results**: `VERIFIED`, `VERIFIED_WITH_LIMITATIONS`,
  `INCOMPLETE`, `FAILED`. The first two pass; everything else fails.
  `state` deliberately keeps its own copy of these strings so it need not depend
  on the verifier — a test pins the two together.
- **Capability IDs** (8, fixed): `workspace_data`, `items`, `comments`,
  `attachment_metadata`, `attachment_content`, `custom_fields`,
  `relationships`, `raw_evidence`.
- **Portable model** is source-neutral: workspaces → containers → collections →
  records, plus comments, identities, fields, relationships, attachments. Each
  row keeps the connector's own word for what it is in `kind` and `Source.Type`.

## `alih status` exit codes

`0` healthy · `1` needs attention · `2` usage error · `3` nothing recorded that
can be called healthy · `4` local state unreadable. This is a public contract;
automation depends on it.

## Traps — read before changing anything below

These cost real debugging time. Each is a place where the obvious change is
wrong.

1. **The M3 logical digest is taken over the vocabulary the snapshot was
   recorded in, not the one the current build prefers.** Change
   `connector.Inventory`'s JSON shape without versioning the digest and *every
   existing archive stops verifying*. See `snapshot.logicalDigest`.

2. **A container's portable `kind` must equal its `Source.Type`.** Verification
   enforces it. Core counts kinds without interpreting them, so this is how a
   connector keeps its own vocabulary.

3. **`state.writeAtomically` chmods its own directory to `0700` before
   writing.** Making the state directory unwritable does *not* produce a write
   failure. To inject that fault, make the **parent** undeletable/uncreatable.

4. **Empty directories are meaningful in the archive corpus.** An archive with
   no retrieved attachments still needs an `attachments/` directory or it stops
   being well-formed. Any copy/archive/zip step must preserve them.

5. **Never regenerate `internal/compat/testdata`.** It was produced by the code
   at tag **v0.2.4** and is frozen on purpose. A fixture the current code
   generates follows the current writer and proves nothing.
   `TestTheCorpusIsTheReleasedShapeNotACurrentOne` guards it.

6. **Raw evidence contains provider-signed attachment URLs**, by design —
   `raw/` is verbatim response bytes. The tested boundary is that such a URL
   never escapes into `alih.db`, the manifest, verification, the report, or an
   organized view.

7. **Report prose uses a `{connector}` placeholder**, resolved once over the
   whole document by reflection. Never hard-code a provider name in
   `internal/report`.

8. **Adapter obligations that only surface as verification failures**: the
   identity portable-ID namespace is the fixed string `identity`; field
   `SemanticsState` is `SOURCE_DEFINITION_ONLY` or `OBSERVED_ONLY_NO_EXECUTION`
   for definitions and `OBSERVED_ONLY` for values; a field's archived definition
   JSON must carry `id`, `name`, `type`; raw paths in the portable model need
   the archive's `raw/` prefix; a capability *declaration* must carry
   `Availability: UNKNOWN`.

9. **`alih auth` always saves the credential** — that is the command's purpose.
   `ALIH_SAVE_CREDENTIAL=0` only suppresses the incidental saving that
   `backup`, `scan`, and `extract` do. Failing to cache a credential is never
   fatal for those three.

## Self-verifying documents

Three documents are checked by `internal/hardening`. Editing them carelessly
breaks the build, which is intended.

- **`HARDENING.md`** — traceability matrix mapping 15 operational failure
  scenarios to ~161 named tests. Renaming a cited test fails the build.
- **`OPERATOR.md`** — operator responsibility matrix and runbook; same
  treatment.
- **`MEMORY.md`** (this file) — its schema-version table is checked against the
  constants.

Milestone review notes live outside the repository as local developer state.
Anything from them that is durable has been folded into the three documents
above, into `CHANGELOG.md`, or into a test.

## Working agreements

- **Every program change gets a `CHANGELOG.md` entry in the same change**, not
  at release time. Entries go under `[Unreleased]`.
- **Do not commit, push, or tag unless explicitly asked.** Releases are cut
  deliberately, never as a side effect of finishing a piece of work.
- **Anything a maintainer keeps outside the repository stays outside it.**
  Local planning and review notes are excluded via `.git/info/exclude` rather
  than `.gitignore`, and must never be committed or copied into release assets.
- **Tests are the deliverable, not an afterthought.** A test here states a
  claim in a sentence and would fail if the claim stopped being true. Prefer
  proving a property over exercising a line.
- Comments explain **why**, not what. The codebase is deliberately dense with
  reasoning at decision points and silent elsewhere.

## Current state

The Operational Foundation is complete: capabilities, health, operational
state, `alih status`, events, notifications, scheduling, overlap protection,
and the organized view, on top of the existing extract/archive/verify/report
pipeline. **429 tests and fuzz targets**, passing under `go test` and
`go test -race`. Builds and vets clean for linux/amd64, linux/arm64,
windows/amd64, darwin/amd64, darwin/arm64.

**Open work:**

- **Connector #2 itself.** The boundary is ready for one; none is written. The
  credential store, the environment variable, and the archive writer's
  credential policy are all connector-scoped now, so adding a connector is
  adapter work plus one line in the composition root.
- **Archives are written at manifest schema 3, which an Alih older than 0.2.5
  refuses.** That refusal is explicit and correct, not a bug — see the archive
  compatibility section of `README.md`. Schema 2 archives stay readable, and
  `internal/compat` pins both ends of that range against the frozen v0.2.4
  corpus.

## Connector boundary

What an adapter owns, and what Core refuses to know.

- **Identity** is the adapter's `Name()`; `DisplayName()` is optional and falls
  back to the identifier. That one identifier scopes the credential, derives the
  environment variable, and is sealed into archives, state, events, and locks.
  There is no second place a connector is named.
- **Credentials** are addressed by connector. `internal/credentials` holds one
  secret per connector; `internal/config` derives
  `ALIH_<CONNECTOR>_TOKEN`, so ClickUp resolves to the documented
  `ALIH_CLICKUP_TOKEN` without Core naming a provider.
- **Which hosts may receive the credential** is declared by the adapter through
  `connector.CredentialHostProvider`. Core defaults to sending it nowhere. The
  archive writer previously carried `api.clickup.com` and attached the
  credential only there, which meant every other connector silently got none.
- **Errors** cross the boundary as typed `connector.OperationalError` values
  carrying stable reason codes. Core never parses a provider's error text; do
  not add a code path that does.
- **Vocabulary** stays with the connector: `Inventory` counts neutrally and
  carries the connector's own kind names beside the totals.

## Verification commands

```bash
gofmt -l . && go vet ./... && go build ./... && go test ./... && go test -race ./...
bash scripts/test-install.sh
GOOS=windows GOARCH=amd64 go build ./... && GOOS=darwin GOARCH=arm64 go build ./...
```

Run the full set before claiming anything works. This project's tests catch
real defects regularly; several were found by them during the work above.
