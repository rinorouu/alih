# Operating Alih on someone else's behalf

**Readiness decision: READY, with one prerequisite and three boundaries that
must be stated to a customer rather than engineered away.**

This document is a readiness report and a runbook. It is not a service, and
nothing here is a commercial decision. Its question is narrow: can a person
operating Alih for somebody else do the whole job with the same public
commands, stable JSON, and local files a self-managed user has — with no
private build, no hidden endpoint, and no capability held back?

The answer is yes. What follows is the evidence and the boundaries.

## The rule this is measured against

Alih Assistance, if it ever exists, may configure, schedule, monitor, maintain,
organize, troubleshoot, and operate Alih Core. It may not depend on a
deliberately more capable private build. Any responsibility below that could
only be met by a private feature would be an architecture failure, not a
product opportunity.

No such responsibility was found.

## Operator responsibility matrix

Every row is a job an operator would have to do, the public surface that does
it, and the test that proves the surface is enough.

| Responsibility | Public surface | Proof |
| --- | --- | --- |
| Set up authentication | `ALIH_CLICKUP_TOKEN` + `alih auth` | `TestAuthVerifiesEnvironmentTokenSavesAndListsWorkspaces` |
| Keep the credential in the customer's own environment | `ALIH_SAVE_CREDENTIAL=0` | `TestCustomerKeepsItsCredentialWhenPersistenceIsOff` |
| Choose where backups are written | `alih backup --destination` | `TestBackupDestinationFlagIsAbsoluteAndOverridesTheDefault` |
| Take a backup | `alih backup` | `TestBackupHappyPathUsesExistingPipelineAndPublishesVerifiedBundle` |
| Verify an archive independently | `alih verify --archive` | `TestVerifyReportsAProvenArchiveWithoutHidingItsLimitations` |
| Hand the customer a readable result | `alih report --archive --format text\|html\|json` | `TestReportWritesSelfContainedHTMLBesideTheArchive` |
| Browse recovered content | `alih organize --archive --output` | `TestOrganizeReportsThePublishedView` |
| Monitor an installation | `alih status [--json]` | `TestOperatorRunbookOnACleanMachine` |
| Monitor without touching the source | `alih status` (default) | `TestReadOnlyCommandsMakeNoSourceRequest` |
| Diagnose a failure | `alih status --json` + the event log | `TestOperatorDiagnosesAFailureFromMachineReadableEvidenceAlone` |
| Schedule unattended runs | `alih schedule check/preview/install/inspect/remove` | `TestScheduleInstallInspectAndRemoveRequireExplicitActions` |
| Configure alerting | `notifications.json` + `alih notify [--test]` | `TestNotifyConfigCheckMakesNoDeliveryAndRedactsTheURL` |
| Prevent overlapping runs | automatic, per connector + workspace + destination | `TestTwoProcessesCannotEnterOneScopeAndKillReleasesIt` |
| Recover state Alih has lost | `alih status --reconcile` | `TestReconcileRebuildsStateForABackupAlihForgot` |
| Keep customers isolated | separate config, state, and destination roots | `TestTwoCustomerInstallationsCannotAffectEachOther` |
| Upgrade | replace the binary; old artifacts still read | `TestAReleasedArchiveStillVerifies`, `TestEveryRecordedStateVersionStillLoads` |
| Offboard | revoke the token, delete Alih's local files | `TestOffboardingLeavesTheCustomerWithUsableArchives` |

Every entry is a documented command or a documented local file. None requires
reading SQLite, parsing prose, or inspecting raw evidence.

## What is guaranteed about egress

Three tests in `internal/hardening` make these structural rather than
promissory. They read the source, not a running process, so they prove the path
does not exist rather than that it was not taken.

- **`TestOnlyStatedPackagesCanReachTheNetwork`** — seven packages may import
  networking, each for a stated reason. A new one is a decision somebody has to
  make on purpose.
- **`TestTheOnlyExternalHostIsTheSource`** — exactly one external host is
  compiled in, `api.clickup.com`. There is no telemetry collector, no update
  check, and no licence server. The other URLs in the tree are XML and plist
  schema namespaces that are never fetched.
- **`TestNoCapabilityIsGatedBehindAnEdition`** — no entitlement check, no
  licence key, no feature flag, and no build constraint beyond the platform
  splits. One build, one set of capabilities.
- **`TestNoDependencyCanReportOnTheUser`** — one non-standard dependency,
  `github.com/mattn/go-sqlite3`, because the portable archive is a SQLite
  database.

Alih makes exactly three kinds of outbound request, all of them to somewhere
the user chose: the source API, attachment downloads from that source, and a
webhook the user configured by hand. `status`, `verify`, `report`, `organize`,
`notify` (without `--test`), and `schedule check` make none at all.

## Runbook

### Onboarding a customer

1. Install Alih on a machine the **customer** controls, writing to storage the
   **customer** controls. Nothing in Alih defaults to operator-owned storage,
   and nothing uploads anywhere.
2. Have the customer create a read-only ClickUp personal token. The operator
   does not need to see it.
3. Decide how the credential lives:
   - **Customer-held (preferred).** Inject `ALIH_CLICKUP_TOKEN` per run from
     the customer's own secret store and set `ALIH_SAVE_CREDENTIAL=0`. Alih
     keeps no copy.
   - **Machine-held.** Run `alih auth` once. The token is written to
     `credentials.json` with `0600` permissions inside a `0700` directory,
     on the customer's machine.
4. `alih backup --destination /path/the/customer/owns` once, by hand, and read
   the result.
5. Install a schedule: `alih schedule preview <id>` to see exactly what will be
   written, then `alih schedule install <id>`.

### Routine monitoring

`alih status --json` is the whole interface. Its exit codes are the contract:

| Exit | Meaning | Operator action |
| --- | --- | --- |
| `0` | Healthy | none |
| `1` | Needs attention | read `attention` in the JSON |
| `2` | Usage error | fix the invocation |
| `3` | Nothing recorded that can be called healthy | expected on a new install; otherwise investigate |
| `4` | Local state is unreadable | investigate; Alih has not modified it |

Status reads local records only. It makes no source request unless `--refresh`
is given, which makes exactly one authentication request.

### Diagnosing a failure

1. `alih status --json` gives the stage that failed, a stable reason code, and
   the last safe message.
2. The event log beside the state gives the ordered history, correlated to
   status by `operation_id`.
3. Failed work is preserved at `<run>.failed` rather than deleted.

None of this contains the credential, a provider response body, or a signed
URL. It also contains none of the customer's content: an operator can diagnose
a failure without being able to read the data.

### Upgrading

Replace the binary. Archives and local records written by earlier versions
remain readable, and reading them does not rewrite them. A future schema is
refused rather than guessed at, so a downgrade fails loudly instead of
corrupting state.

Do not upgrade while a backup is running; the operation lock covers concurrent
runs, not a binary being swapped underneath one.

### Offboarding

1. The customer revokes the ClickUp token.
2. Delete Alih's local files: the credential store, the state directory, the
   event log, `notifications.json`, `schedules.json`.
3. `alih schedule remove <id>` on each installed schedule.
4. **Leave the destination alone.** The archives are the customer's. They stay
   verifiable with no state, no credential, and no operator involvement, and a
   fresh installation can recover its bearings from them with
   `alih status --reconcile`.

## Ownership and permissions

| Thing | Owner | Permissions |
| --- | --- | --- |
| Archives, reports, organized views | The customer | Inherited from the destination |
| `credentials.json` | The customer's machine account | `0600` in a `0700` directory |
| Operational state and event log | The customer's machine account | `0600` in a `0700` directory |
| `notifications.json`, `schedules.json` | The customer's machine account | `0600`, rejected if wider |
| Scheduler artifacts | The customer's user account | Per-user, never system-wide |

Alih never escalates privileges and never installs anything system-wide.
Retention is entirely the customer's decision: Alih writes a new timestamped
bundle per run and deletes nothing, ever.

## The prerequisite

**Multi-connector credential storage.** `internal/credentials` holds exactly
one token in one file, with the provider hard-coded. This does not block
operating ClickUp installations today, and every test above passes without it.
It becomes a prerequisite the moment a second connector exists, because two
connectors on one machine would overwrite each other's credential. It belongs
to the work that adds a second connector, not to this review, and it is the
only thing on that list still open.

## Boundaries to state plainly, not engineer away

These are not gaps to close. They are properties an operator must be honest
with a customer about.

1. **An operator with filesystem access to the destination can read the
   customer's data.** Archives are readable SQLite plus verbatim raw evidence.
   There is no operator-facing view that shows health without also showing
   content, because there is no server between them — which is the point.
   Diagnosis via `status` and events exposes no content; direct filesystem
   access exposes everything.
2. **Raw evidence contains short-lived provider-signed attachment URLs.** This
   is required by the exact-response-bytes guarantee. An archive is as
   sensitive as the workspace it came from, and should be handled that way.
3. **Some actions still need interactive access to the customer's machine.**
   Installing a schedule, enabling systemd lingering on Linux, and the
   logged-in-session requirement of the current macOS and Windows integrations
   all need someone at that machine. Alih has no remote control surface, and
   adding one would contradict everything above.

## What was changed during this review

Two findings, both about credentials, both fixed because the gap was concrete
and the fix is generally useful to self-managed users rather than to an
operator specifically.

- **A backup failed outright when it could not cache a credential it had
  already verified.** An installation on read-only or ephemeral storage lost
  its backup over a convenience it never asked for. Caching is now
  best-effort for `backup`, `scan`, and `extract`; the operator is told on
  stderr and the run continues. `alih auth` still fails loudly, because saving
  is the entire reason that command exists.
- **A credential supplied through the environment was always written to disk.**
  `ALIH_SAVE_CREDENTIAL=0` now keeps it where its owner put it.

## Verdict

Every operator responsibility maps to the same documented Core a self-managed
user has. Customer-controlled storage and customer-held credentials are the
default and the practical design, not a configuration somebody has to fight
for. No hidden telemetry, upload, premium gate, or private build is required,
and three structural tests keep it that way.

The remaining work is commercial, and it is explicitly separate.
