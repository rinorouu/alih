ALIH

Product Requirements Document — V0

Status: Experimental
Version: 0.1
Initial Connector: ClickUp
Primary Interface: CLI
Architecture: Local-first
Primary Goal: Prove portable, verifiable SaaS data extraction
Production Readiness: Not intended

---

1. Product Definition

Alih is a local-first SaaS data portability tool.

Its purpose is not merely to export data, but to determine:

«What can actually be recovered outside the source SaaS, and can we prove that the exported representation is complete within the supported scope?»

Alih connects to a supported SaaS using its official API, discovers the available source data, creates a portable local representation, and independently verifies the resulting archive.

V0 uses ClickUp as the first connector.

ClickUp is an implementation target, not part of Alih's core identity.

---

2. Product Thesis

Most SaaS platforms provide some form of export.

However:

«Exportable does not necessarily mean portable, reconstructable, or verifiably complete.»

Data may be distributed across different export mechanisms, represented only as links, lose relationships, omit platform-specific structures, or become difficult to validate after export.

Alih attempts to provide a stronger guarantee:

«Everything Alih claims to support must either be successfully archived and verified, or explicitly reported as incomplete, unavailable, or unsupported.»

Alih must never silently convert uncertainty into success.

---

3. Core Principles

3.1 Local First

User data should remain on the user's machine whenever technically possible.

V0 must not require an Alih cloud backend.

Expected flow:

Source SaaS
    │
    ▼
Official API
    │
    ▼
Alih
    │
    ├── Raw Snapshot
    ├── Portable Model
    ├── Attachments
    └── Verification
    │
    ▼
Local Archive

---

3.2 Read First, Write Never

V0 is read-only.

Alih must not:

- modify source records;
- create source records;
- delete source records;
- modify permissions;
- trigger automations;
- perform migrations into another SaaS.

If an operation would modify source state, it is outside V0 scope.

---

3.3 No Silent Loss

This is the primary correctness invariant.

If Alih expects:

12,491 records

but archives:

12,487 records

the result must NOT be:

EXPORT COMPLETE

It must be:

ARCHIVE INCOMPLETE

Expected: 12,491
Archived: 12,487
Unresolved: 4

A visible failure is preferable to invisible data loss.

---

3.4 Evidence Over Assumption

Alih must distinguish between:

VERIFIED
PARTIAL
UNSUPPORTED
UNAVAILABLE
UNKNOWN
FAILED

Unsupported data must not be treated as absent.

Unavailable data must not be treated as zero.

Unknown data must not be guessed.

---

3.5 Connector Isolation

Source-specific behavior belongs inside connectors.

Core Alih components must not contain ClickUp-specific assumptions.

Conceptually:

ClickUp Connector ──┐
                    │
Future Connector ───┼──► Portable Model
                    │
Future Connector ───┘

The ClickUp connector exists to prove the architecture.

---

4. V0 Objective

V0 answers one question:

«Can Alih inspect a non-trivial ClickUp Workspace, create a portable local representation of the supported data, and prove that representation is complete within its declared scope?»

If the answer is no, development should stop or the architecture should be revised before product features are added.

---

5. V0 Success Criteria

Alih V0 succeeds when it can:

1. authenticate against a test ClickUp account;
2. discover accessible Workspaces;
3. inventory supported workspace structures;
4. collect supported records through the official API;
5. preserve raw source responses;
6. transform supported data into Alih's portable model;
7. create a local SQLite representation;
8. retrieve supported attachments where possible;
9. generate a manifest describing the archive;
10. independently verify source inventory against archive inventory;
11. explicitly disclose unsupported/unavailable structures;
12. produce a human-readable recovery report;
13. detect incomplete exports rather than silently succeeding.

A successful CLI run should require no cloud infrastructure.

---

6. V0 Non-Goals

The following must NOT be implemented during V0 unless required to prove a core invariant:

- web application;
- desktop GUI;
- browser extension;
- user accounts;
- Alih cloud storage;
- billing;
- subscriptions;
- payment processing;
- AI/LLM functionality;
- scheduled backups;
- continuous sync;
- multiple SaaS connectors;
- cross-SaaS migration;
- restore into ClickUp;
- restore into another SaaS;
- team collaboration;
- analytics;
- marketing website;
- mobile application;
- production OAuth onboarding;
- automatic remediation;
- perfect ClickUp reconstruction.

V0 is an engineering experiment.

---

7. Initial CLI

The initial CLI consists of four conceptual commands.

"alih auth"

Configure local authentication.

Development V0 may use a personal API token.

Credentials must never be:

- committed to Git;
- written into archives;
- printed in logs;
- included in reports;
- stored in plaintext unless explicitly required and clearly disclosed.

---

"alih scan"

Read the source and produce an inventory.

Example:

$ alih scan

ALI H — CLICKUP SCAN

Workspace: Example Workspace

Hierarchy

Spaces                 8
Folders                31
Lists                  94

Content

Tasks              18,491
Subtasks             4,291
Comments            42,183
Attachments          6,281
Custom fields           37
Relationships         8,921

Capability

Tasks              SUPPORTED
Comments           SUPPORTED
Attachments        SUPPORTED
Docs               PARTIAL
Whiteboards        UNSUPPORTED
Automations        UNAVAILABLE

Scan complete.
No source data modified.

"scan" must not create a successful portability claim.

It only establishes source inventory and capability.

---

"alih export"

Create the portable archive.

Example:

$ alih export

Scanning source...
Collecting records...
Downloading attachments...
Building portable model...
Creating SQLite database...
Writing manifest...

Archive created:

./alih-example-2026-08-30/

Expected structure:

alih-example-2026-08-30/
│
├── alih.db
├── manifest.json
├── schema.json
│
├── raw/
│   └── ...
│
├── attachments/
│   └── ...
│
└── report.html

---

"alih verify"

Verify an existing archive.

Example:

$ alih verify ./alih-example-2026-08-30

ALI H — VERIFICATION

Hierarchy
Spaces          8 / 8        PASS
Folders        31 / 31       PASS
Lists          94 / 94       PASS

Records
Tasks      18,491 / 18,491   PASS
Comments   42,183 / 42,183   PASS

Attachments
Expected        6,281
Retrieved       6,279
Unresolved          2        WARN

SQLite integrity             PASS
Manifest integrity           PASS

RESULT

INCOMPLETE

2 supported attachments could not be archived.

No silent omissions detected.

---

8. Proposed Architecture

cmd/alih
   │
   ▼
Application Layer
   │
   ├── scan
   ├── export
   ├── verify
   └── report
   │
   ▼
Connector Interface
   │
   ▼
ClickUp Connector
   │
   ▼
Raw Snapshot
   │
   ▼
Portable Model
   │
   ├── SQLite
   ├── Attachments
   ├── Schema
   └── Manifest
   │
   ▼
Verifier
   │
   ▼
Recovery Report

Suggested repository structure:

alih/
├── cmd/
│   └── alih/
│
├── internal/
│   ├── connector/
│   │   ├── connector.go
│   │   └── clickup/
│   │
│   ├── model/
│   ├── snapshot/
│   ├── archive/
│   ├── verify/
│   └── report/
│
├── fixtures/
├── tests/
├── docs/
├── go.mod
├── README.md
└── PRD.md

---

9. Connector Contract

Connectors should expose source capabilities rather than pretending every SaaS has equivalent structures.

Conceptual interface:

Connector

Authenticate()
Capabilities()
Discover()
Inventory()
Extract()

The core engine should consume normalized results rather than source-specific API objects wherever possible.

Example capability declaration:

tasks           SUPPORTED
comments        SUPPORTED
attachments     SUPPORTED
docs            PARTIAL
whiteboards     UNSUPPORTED
automations     UNAVAILABLE

Capability state must be preserved in the final manifest.

---

10. Raw Snapshot

Before normalization, Alih should preserve enough raw source material to:

- debug transformations;
- reproduce parsing bugs;
- compare normalized output with source responses;
- support future schema migrations;
- provide evidence for verification.

Raw data must remain local.

Raw snapshots must not contain authentication credentials.

Alih should avoid unnecessary duplication of large binary payloads.

---

11. Portable Model

The portable model must be independent from ClickUp naming where practical.

Examples of conceptual entities:

Workspace
Container
Collection
Record
Comment
Attachment
Field
Relationship
Identity

Source-specific metadata may be retained separately:

source:
  provider: clickup
  source_id: "abc123"
  source_type: "task"

Original IDs must be retained.

This enables deterministic mapping between:

source object
↕
portable object

---

12. SQLite Archive

SQLite is the primary structured V0 output.

Reasons:

- portable;
- local;
- widely supported;
- queryable;
- single-file;
- deterministic;
- easy to integrity-check.

The schema does not need to recreate ClickUp internally.

It needs to preserve Alih's portable representation.

The database must retain original source identifiers.

---

13. Attachments

Attachments must be treated separately from ordinary structured records.

For each supported attachment Alih should record:

source identifier
source record
original filename
media type if available
expected location
download status
local path
size
checksum
error if unresolved

Successful retrieval should produce a checksum.

A failed download must remain represented in the manifest.

Alih must never silently omit failed attachments.

---

14. Manifest

Every archive must contain "manifest.json".

The manifest is the machine-readable statement of what Alih believes exists in the archive.

Example conceptual structure:

{
  "alih_version": "0.0.1",
  "connector": "clickup",
  "created_at": "...",
  "source": {
    "workspace_id": "...",
    "workspace_name": "..."
  },
  "inventory": {
    "tasks": {
      "expected": 18491,
      "archived": 18491
    },
    "comments": {
      "expected": 42183,
      "archived": 42183
    },
    "attachments": {
      "expected": 6281,
      "archived": 6279,
      "unresolved": 2
    }
  },
  "capabilities": {},
  "verification": {}
}

The manifest must be deterministic enough to support future independent verification.

---

15. Verification Model

Verification is a first-class subsystem, not a final UI feature.

It should answer:

Completeness

Did the archive contain everything Alih expected within supported scope?

Referential Integrity

Do portable relationships reference valid archived entities?

Attachment Integrity

Do downloaded files exist and match their recorded checksums?

Database Integrity

Does SQLite pass its own integrity checks?

Manifest Integrity

Does the manifest agree with the actual archive?

---

16. Result States

Alih must not reduce all outcomes to success/failure.

Suggested archive states:

VERIFIED

Everything expected within supported scope was archived and verified.

VERIFIED_WITH_LIMITATIONS

Supported data passed verification, but explicitly unsupported or unavailable source capabilities exist.

INCOMPLETE

Alih expected supported data that it failed to archive.

FAILED

Alih could not establish a trustworthy archive.

"INCOMPLETE" must never be presented as "VERIFIED".

---

17. Unsupported Data Policy

Alih must distinguish:

Unsupported

Alih has access to the source concept but has not implemented portability for it.

Unavailable

The official API does not expose sufficient information.

Unknown

Alih cannot determine whether the data exists or whether it can be recovered.

These states must appear in the report.

Alih must not use undocumented scraping or reverse engineering to convert "UNAVAILABLE" into "SUPPORTED".

---

18. API Reliability

The connector must account for:

- pagination;
- rate limits;
- transient network failures;
- HTTP 429;
- retries;
- partial responses;
- duplicate retrieval;
- interrupted exports.

Retries must be bounded.

Rate-limit handling must respect provider limits.

A partially completed API traversal must not produce a verified archive.

---

19. Resume Strategy

Full resumability is not required for the first implementation.

However, architecture should not make resumability impossible.

Stable source IDs and deterministic archive paths should be used from the beginning.

Future:

alih export
   ↓
interrupted at 71%
   ↓
alih export --resume
   ↓
continue safely

Resume must never cause duplicate logical records.

---

20. Security

V0 must:

- use the official source API;
- use the minimum access necessary;
- remain read-only;
- avoid logging secrets;
- avoid embedding credentials in archives;
- keep user data local;
- clearly identify every external network request;
- avoid telemetry by default.

No user content should be sent to an AI model.

No user content should be sent to Alih infrastructure because V0 has no Alih infrastructure.

---

21. Testing Strategy

Fixtures should exist independently from live API tests.

Required test categories:

Connector parsing

Known API response → expected source model.

Pagination

Multiple pages must produce exactly the expected inventory.

Duplicate protection

Repeated source records must not create duplicate portable records.

Relationship integrity

Broken references must be detected.

Attachment failure

Failed attachment download must produce "INCOMPLETE" or an explicit supported limitation, never silent success.

Manifest consistency

Manifest counts must match actual archive counts.

SQLite integrity

Generated databases must pass integrity checks.

Interrupted extraction

Partial traversal must not produce "VERIFIED".

Unknown fields

Unexpected provider fields must not crash the entire export unless correctness requires it.

---

22. Fail-Closed Invariants

The following conditions must prevent a clean VERIFIED result:

pagination incomplete
expected != archived
unresolved supported record
corrupt SQLite
manifest mismatch
broken required relationship
attachment expected but silently absent
authentication scope insufficient and undetected
unknown extraction termination

Alih should prefer:

I cannot prove this archive is complete.

over:

Probably fine.

---

23. Observability

V0 logs should describe operations without exposing customer content unnecessarily.

Example:

INFO connector=clickup operation=list_tasks page=4 records=100
INFO connector=clickup operation=list_tasks page=5 records=73
INFO archive entity=task archived=473
WARN attachment id=... status=unresolved
INFO verify entity=task expected=473 actual=473 result=pass

Never log:

API token
OAuth secret
full private comments
attachment contents

---

24. V0 Milestones

M0 — Repository Foundation

- Go module
- CLI skeleton
- configuration
- logging
- tests
- connector interface

Exit criterion:

alih --help

works.

---

M1 — Authentication

- ClickUp personal token
- authenticated identity/workspace discovery
- safe credential handling

Exit criterion:

Alih can list authorized test Workspaces.

---

M2 — Scan

- hierarchy discovery
- supported content inventory
- pagination
- capability report

Exit criterion:

"alih scan" inventory can be manually reconciled with a test Workspace.

This is the first major go/no-go gate.

If inventory cannot be trusted, stop before export.

---

M3 — Raw Extraction

- deterministic API traversal
- raw snapshot
- stable source IDs
- failure accounting

Exit criterion:

Repeated scans of unchanged source produce equivalent logical inventories.

---

M4 — Portable Archive

- normalized model
- SQLite
- schema
- manifest
- attachments

Exit criterion:

A non-trivial Workspace can be represented locally without relying on ClickUp to inspect core supported data.

---

M5 — Verification

- source/archive count reconciliation
- referential checks
- checksums
- SQLite integrity
- manifest integrity

Exit criterion:

Alih can deliberately detect an intentionally corrupted archive.

---

M6 — Recovery Report

Generate human-readable "report.html".

Exit criterion:

A user can understand:

- what was captured;
- what was not captured;
- what failed;
- what is unsupported;
- whether the archive is verified.

---

25. V0 Final Gate

Alih V0 is considered technically validated only if a non-trivial test Workspace completes:

SCAN
  ↓
EXTRACT
  ↓
ARCHIVE
  ↓
VERIFY
  ↓
REPORT

with no unexplained discrepancy.

Passing V0 means:

«Alih can make a defensible portability claim for its supported scope.»

It does NOT mean:

«Alih is commercially validated.»

---

26. Post-V0 Decision

Only after V0 passes should the project evaluate:

- real-user demand;
- OAuth;
- GUI;
- scheduled snapshots;
- second connector;
- migration targets;
- monetization;
- commercial distribution.

The second connector should not be added merely to demonstrate extensibility.

It should be selected based on evidence of user demand.

---

27. Product Guardrail

When evaluating future changes, ask:

«Does this improve our ability to determine, preserve, or prove the portability of user-owned data?»

If not, it probably does not belong in the core product.

---

28. V0 Definition of Done

V0 is done when:

$ alih scan

produces a trustworthy inventory,

$ alih export

creates a portable local archive,

and:

$ alih verify

can independently determine whether that archive matches Alih's declared source inventory.

The most important output is not the SQLite database.

It is the ability to say, with evidence:

«“This is what we could recover, this is what we could not recover, and nothing we know about was silently omitted.”»
