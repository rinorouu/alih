# Writing an Alih connector

A connector teaches Alih to read one SaaS product. Alih Core knows nothing
about any provider: it does not know what your objects are called, which hosts
you talk to, or what your errors mean. Everything provider-shaped lives in your
adapter, and this document is the boundary between the two.

ClickUp is the reference implementation and Notion is the second connector.
Both are in the repository under the same contracts described here, not under a
special arrangement. Where the two differ is instructive: ClickUp has a fixed
space/folder/list hierarchy and declares one credential host, while Notion has
databases holding data sources holding pages whose content is a block tree of
unbounded depth, and declares no credential hosts at all. Neither shape is
privileged by Core.

**Before you start:** open an issue. Which providers Alih ships is a product
decision, not only an engineering one, and the answer affects how much of this
you need.

---

## 1. Where connector code lives

```
internal/connector/            the source-neutral boundary: interfaces, capabilities, health
internal/connector/clickup/    the reference adapter
internal/conformance/          the conformance suite your connector runs against
cmd/alih/main.go               the composition root, the only place that wires an adapter
```

Your connector is a package under `internal/connector/<name>/`. Nothing else in
Alih may import it — a test enforces that only `cmd/alih` may — so the way your
adapter reaches Core is by being passed to it, never by Core reaching for you.

---

## 2. What you must implement, and what you may

Nothing here is a single giant `Connector` interface. Alih uses small
interfaces, and your adapter satisfies the ones it can.

### Required

| Contract | Interface | Why |
| --- | --- | --- |
| Machine identity | `connector.Connector` — `Name() string` | Keys your credential, your environment variable, and every archive you produce |
| Portable model | `exporter.Normalizer` | Turns your raw evidence into the archive; without it there is no archive |

### Conditional — required for the behaviour they enable

| Contract | Interface | Without it |
| --- | --- | --- |
| Authentication | `connector.Authenticator` | No credential can be verified and no workspace discovered |
| Inventory | `connector.Scanner` | `alih scan` cannot report what exists |
| Extraction | `connector.Extractor` | No raw evidence, so no backup |
| Capabilities | `connector.CapabilityProvider` | You make no capability claims, so nothing can be reconciled against them |

### Optional — safe to omit, with a stated consequence

| Contract | Interface | If you omit it |
| --- | --- | --- |
| Human name | `DisplayName() string` | Core falls back to your identifier |
| Field semantics | `verify.FieldSemantics` | Archived field values verify as *unproven* rather than being rejected |
| Credential hosts | `connector.CredentialHostProvider` | **Alih sends no credential to any attachment host** — see §6 |
| Typed errors | `connector.OperationalError` | Every failure you produce collapses to `UNKNOWN_FAILURE` |

Optional means optional. Do not implement a surface you cannot honestly
support; declaring a capability you do not have is worse than not having it.

---

## 3. Identity

Your connector has two names and they are not interchangeable.

**Machine identity** is `Name()`. It must match `^[a-z0-9_-]{1,64}$`. This one
string:

- selects your credential in the local credential file;
- derives your environment variable — `Name()` `"clickup"` becomes
  `ALIH_CLICKUP_TOKEN`, and `"my-source"` becomes `ALIH_MY_SOURCE_TOKEN`;
- is sealed into every archive you produce and is how Alih later finds the
  right normalizer and field interpreter for that archive;
- scopes operational state, events, and the cross-process run lock.

It must never change. An archive written under one name is looked up under it
years later.

**Display name** is `DisplayName()`, optional, and is what a person reads. It
is sealed into the archive so a recovery report can name your provider without
Core knowing any provider's name. Omit it and Core uses your identifier.

Your `Normalizer` and `FieldSemantics` both report a `Connector()` string.
**All three must be the same value.** Core selects them by the name an archive
records; if they disagree, your archive will be verified without an
interpreter and its field values will be left unproven.

---

## 4. Provider vocabulary is yours

Alih counts neutrally — containers, collections, records, nested records — and
records *your* words beside those totals as `container:<kind>` and
`record:<kind>`. Core never interprets a kind name.

ClickUp's containers are spaces and folders and its records are tasks and
subtasks. **Those are ClickUp's words, not Alih's.** If your source has
constellations and packets, use those. The conformance suite fails a connector
that borrows another provider's nouns, because that is nearly always someone
copying the reference adapter rather than modelling their own source.

There is no universal schema and Alih is not trying to build one. Your source's
shape survives into the archive so that data is still recognisable to somebody
who knows your product.

---

## 5. Credentials

One credential per connector, stored locally, and never anywhere else.

- The credential file holds **one secret per connector**, addressed by your
  `Name()`. Saving yours cannot read or overwrite another connector's.
- A file this build cannot parse is **refused, not overwritten** — overwriting
  would destroy a credential Alih does not understand.
- Your identifier is validated before it selects anything, so a name cannot
  carry a path separator or address a file.
- Credentials are read from `ALIH_<NAME>_TOKEN` or the local store. Nothing
  else.
- **A credential must never reach an archive, a manifest, a log, an event, a
  status document, a report, or a scheduler file.** The conformance suite
  searches every byte your run publishes for the credential it gave you.

Alih has no credential syncing, no remote storage, and no hosted anything. If
your provider needs an authentication flow Alih does not have, open an issue
before writing it.

---

## 6. Attachment credential hosts — read this one carefully

Alih is **fail-closed**: unless your connector declares which hosts may receive
its credential, Alih attaches the credential to *nothing* when downloading
attachments.

That is right for one kind of provider and wrong for another, and Core cannot
tell which you are:

1. **Attachments are anonymous.** The archived URL is pre-signed or public and
   carries its own authorisation. Declare nothing. Sending the credential as
   well would only widen where your secret travels.
2. **Attachments require authentication.** The provider expects Alih's
   credential on the request. **Implement `connector.CredentialHostProvider`**
   and return the exact hostnames. If you do not, every download is anonymous
   and your provider answers `401` or `403` with nothing explaining why — the
   failure looks like a broken credential rather than a missing declaration.
3. **Attachments are not supported.** Declare nothing.

The matching rules are deliberately narrow and are not negotiable:

- **exact hostname match**, case-insensitive, no port;
- **no wildcards**;
- **no subdomain inheritance** — declaring `example.com` does not cover
  `files.example.com`, and declaring `files.example.com` does not cover
  `example.com`;
- **no trust inferred from a URL** the source handed you;
- a **redirect** to a host outside your declared set has the `Authorization`
  header stripped before it is followed;
- an **empty declaration means nothing is trusted**.

Because Core cannot infer which case you are, you state it in your conformance
fixture through `AttachmentIntent`. **That is test metadata, not a runtime
interface** — it changes nothing about how your connector behaves in
production, and exists so the suite can hold you to your own intent. Case 2
with no declaration is a named conformance failure instead of a mystery.

---

## 7. Capabilities, health and errors

**Capabilities are declarations, not requirements.** Implementing
`connector.CapabilityProvider` is conditional: a connector that does not is
simply treated as having made no capability claims, and nothing is reconciled
against them.

If you do implement it, the declaration has to hold together. You declare what
your adapter implements, with each capability marked `REQUIRED` or `OPTIONAL`,
and separately what a given run actually observed. An unsupported capability is
not the same as one that failed, and Alih will not let the two be confused. The
declaration must be deterministic — it describes your adapter, not one run's
luck — and at least one of the capabilities you declare must be `REQUIRED`, or
no run could ever be judged incomplete.

**Health and authentication are separate on purpose.** A rejected credential
leaves the provider `HEALTHY` and reports through the authentication
observation instead. Blaming the provider for a bad token sends an operator to
the wrong place. Every failure must classify as *either* a credential problem
*or* a provider problem.

**Core never reads your error text.** It asks your error what it means. Give
your error type an `OperationalAssessment(time.Time)` method returning a stable
reason — `AUTHENTICATION_REJECTED`, `RATE_LIMITED`, `UPSTREAM_UNAVAILABLE`,
`NETWORK_FAILURE`, `UNSUPPORTED_RESPONSE` and the rest — and Alih's status,
events, and notifications become meaningful. Skip it and every failure you
produce is an indistinguishable `UNKNOWN_FAILURE`.

Mapping *your* provider's status codes and error bodies onto those reasons is
your adapter's job. Do not ask Core to do it.

---

## 8. Raw evidence and the archive

```
provider API  ->  raw evidence  ->  normalization  ->  portable archive
                  (yours,           (yours)            (Core's format)
                   verbatim)
```

**Raw evidence is the provider's bytes, unmodified.** It is what makes an
archive provable. Record responses exactly as received; do not tidy, reorder,
or merge them. Note that verbatim bytes may include short-lived signed URLs
your provider put there — that is expected, and it is why an archive should be
treated as being as sensitive as the workspace it came from.

**Normalization is derived and never destructive.** It produces Core's portable
model without discarding the evidence it came from; every portable row keeps its
original source identifier and a path back into the raw tree.

**The manifest format is Core's.** You do not write it and you must not need it
changed. Current compatibility:

| | |
| --- | --- |
| Written by this version | manifest schema 3 |
| Read and verified | schema 2 and 3 |

If implementing your connector appears to require a new manifest schema, stop
and open an issue. That is a Core change with its own migration, not connector
work.

---

## 9. The four invariants Core owns

These are rules Core enforces that no interface states. Each one is asserted by
the conformance suite, so you will meet them before you meet a failed archive.

### 9.1 Portable-ID namespace

Every portable identifier is `model.PortableID(provider, namespace, sourceID)`.
The namespace is your object's own source type — **except for identities, where
it is the fixed string `"identity"`**, whatever your provider calls a person.

*Why Core owns it:* identifiers must be reproducible from the archive alone, by
a later build, without your adapter present.

*What you do:* derive identity IDs with the literal `"identity"`; derive
everything else with its own source type, and make a container's `Kind` equal
its source type.

*Typical failure:* Core rejects the archive with a foreign-key violation deep
inside the database writer, which tells you nothing. The suite catches it first
and names the rule.

*Caught by:* `archive_pipeline`.

### 9.2 SemanticsState vocabulary

A field **definition** declares `SOURCE_DEFINITION_ONLY`, or
`OBSERVED_ONLY_NO_EXECUTION` for a computed field whose behaviour Alih does not
reproduce. An **observed value** is always `OBSERVED_ONLY`.

*Why Core owns it:* Alih records what it saw. It never claims to have executed
your provider's formula or rollup semantics, and the vocabulary is how that
promise is kept in the data itself.

*What you do:* mark computed fields honestly and never mark an observed value
as anything else.

*Typical failure:* verification fails `custom_field_evidence` with
`field "x" declares unknown semantics state "..."`, or `observed value for
field "x" declares semantics state "..."; schema.json states observed values
are always OBSERVED_ONLY`.

*Caught by:* `archive_pipeline`, and `field_semantics` for the interpreter.

### 9.3 Archived field-definition keys

The definition JSON you archive for a field must contain `id`, `name` and
`type`, and each must match the columns stored beside it.

*Why Core owns it:* verification proves the archived definition is the one the
value was actually observed against, rather than one swapped in later.

*What you do:* archive the provider's definition with those three keys intact.

*Typical failure:* verification fails `custom_field_evidence` with
`archived definition for field "x" contains no source identifier`, or
`field definition "x" does not match the source identifier inside its own
archived definition`.

*Caught by:* `archive_pipeline`.

### 9.4 Raw path rewriting

Portable rows retain a path into the raw evidence. You record the path your
sink gave you; **Core rewrites it onto the archive's sealed `raw/` tree.** Paths
must be relative and must resolve inside the archive.

*Why Core owns it:* an archive has to be movable and readable on any machine,
so no path may point outside it or depend on where extraction happened.

*What you do:* pass through what the sink recorded. Do not construct absolute
paths and do not invent your own prefix.

*Typical failure:* verification fails `raw_evidence_references`, because a path
a portable row retains does not resolve inside the archive.

*Caught by:* `archive_pipeline`.

---

## 10. Running the conformance suite

Alih's connector contracts are executable. Add one test to your package:

```go
package mysource_test

import (
	"testing"

	"alih/internal/conformance"
	"alih/internal/connector/mysource"
)

func TestMySourceConformsToTheConnectorContract(t *testing.T) {
	t.Parallel()

	conformance.Run(t, conformance.Subject{
		Connector:        mysource.New(),
		Normalizer:       mysource.Normalizer{},
		AttachmentIntent: conformance.AttachmentsAreAnonymous,
		FieldSemantics:   mysource.FieldSemantics{},
		SampleErrors: []error{
			// one representative failure per situation you can produce
		},
	})
}
```

`Run` executes seven contracts as subtests: `identity`, `capabilities`,
`credential_scoping`, `credential_hosts`, `field_semantics`,
`operational_errors` and `archive_pipeline`. Each is also callable on its own —
`conformance.AssertIdentity(t, subject)` — if you want to work on one at a time.

**Contracts you cannot support are skipped, and every skip states why.** That is
deliberate: a green run must never quietly mean nothing ran. Read the skips.

### Running the pipeline contract offline

`archive_pipeline` drives your connector through the real extraction, archive,
verification, report and organize implementations. To run it, the subject needs
a `Normalizer`, an `Authenticator`, an `Extractor`, a fake `Credential`, and an
`HTTPClient`. Without them the contract skips rather than reaching the network:
**the suite never makes a real request, and neither should your tests.**

There are **two** places HTTP has to be faked, and missing the first is the
usual reason a pipeline test tries to call a live API:

1. **Your adapter's own client**, used by `Authenticate`, `Scan` and `Extract`.
   The suite cannot inject this for you — it is your constructor's parameter.
   Take an `*http.Client` and fall back to a default when it is nil, as
   `clickup.NewClient` does, so a test can hand you a fake transport.
2. **`Subject.HTTPClient`**, which Alih uses **only to download attachments**
   while building the archive. It is not passed to your adapter.

A fake transport is a few lines and needs no library:

```go
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
    // answer r.URL.Path with the bytes your provider would return
})}
```

Alih has no recorded-cassette or replay transport, and does not need one to run
this contract. Use an obviously fake credential — the suite searches everything
your run publishes for it.

---

## 11. Learning from the ClickUp adapter

Copy the **boundary pattern**, not the provider implementation.

Two adapters exist, and reading both is more useful than reading either: what
they share is the boundary, what differs is the provider.

| Contract | ClickUp file | Notion file |
| --- | --- | --- |
| Identity, authentication, HTTP client, typed errors | `client.go` | `client.go` |
| Inventory and extraction | `scan.go` | `scan.go` |
| Capability contract | `capabilities.go` | `capabilities.go` |
| Health mapping | `health.go` | `health.go` |
| Normalization and display name | `portable.go` | `portable.go`, `rebuild.go` |
| Field semantics | `fields.go` | `fields.go` |
| Conformance test | `conformance_test.go` | `conformance_test.go` |

Everything in that adapter that mentions ClickUp *should* mention ClickUp: the
API base URL, the payload parsing, the words "space" and "task", the mapping
from ClickUp status codes to Alih reasons, which capabilities ClickUp supports,
what a ClickUp custom field means. None of it is a template for your provider's
semantics. What is worth copying is the shape: where each responsibility sits,
and what never crosses into Core.

---

## 12. Contribution checklist

Before opening a pull request:

- [ ] An issue exists and the connector has been agreed.
- [ ] The adapter lives in `internal/connector/<name>/` and nothing outside
      `cmd/alih` imports it.
- [ ] `Name()` is stable, lowercase, and matches your normalizer and field
      semantics.
- [ ] Provider vocabulary is your source's own words.
- [ ] `conformance.Run` passes, and you have read every skip it reports.
- [ ] `AttachmentIntent` states the truth, and if attachments need
      authentication you declare exact credential hosts.
- [ ] No credential appears in any archive, log, event, status, report, or
      scheduler file.
- [ ] Raw evidence is verbatim; normalization is derived and non-destructive.
- [ ] Errors carry an operational assessment with stable reasons.
- [ ] Your own tests cover the provider behaviour conformance cannot: pagination,
      retry, partial failure, and how your API actually responds.
- [ ] No new dependency without discussion; Alih has one.
- [ ] `gofmt`, `go vet ./...`, `go test ./...` and `go test -race ./...` pass.
- [ ] A `CHANGELOG.md` entry under `[Unreleased]`.
- [ ] You have said in the pull request who will be the connector's primary
      maintainer, so its classification can be agreed during review — see
      [Support and maintenance](#13-support-and-maintenance).

---

## 13. Support and maintenance

Being merged into this repository does not mean the Alih maintainers have
promised to keep a connector working forever. This section says who is
responsible for what, so nobody has to guess.

### Three things people confuse

They change independently, and none of them predicts the others.

| | Question it answers | Where you find it | How fast it changes |
| --- | --- | --- | --- |
| **Conformance** | Does this connector satisfy Alih's contracts? | CI, every commit | per commit |
| **Maintenance** | Who fixes it when the provider changes? | the table below | rarely |
| **Runtime health** | Is it working right now, for you? | `alih status` | hour to hour |

A connector maintained by the Alih maintainers can be `UNAVAILABLE` this
morning because the provider is down. A community-maintained connector can be
fully conformant and working perfectly. **Maintenance is not a quality score,
and it is not a health signal.**

Conformance is deliberately *not* recorded as a label. It is proven by tests
that run on every commit; a stored "this connector is conformant" could only
ever be right by accident, and would be wrong in the dangerous direction the
moment a contract started failing.

### Maintenance — who fixes it

| State | Meaning |
| --- | --- |
| `alih-maintainers` | The Alih maintainers own this connector. If the provider changes its API and the connector breaks, fixing it is our problem. |
| `community` | A named contributor is the primary maintainer. We review changes and keep it compiling and conformant in CI, but we do not promise to fix provider API changes ourselves. |

Neither state is a promise of indefinite support. If a `community` connector
loses its maintainer and starts failing, we will say so, look for a new
maintainer, and — if none appears — mark it unmaintained or remove it in a
release, with the removal in the changelog. A connector is never silently left
broken.

### Maturity — what changing it means

| State | Meaning |
| --- | --- |
| `experimental` | Recently added. It may change shape or be removed. Breaking changes to its output are not treated as regressions. |
| `established` | Shipped in releases and covered by the frozen compatibility corpus. Changes to what it produces are treated as regressions and need a migration path. |

Maturity is about the commitment attached to a connector's output, not about
popularity, and there is no score. Note that Alih itself is Alpha: an
`established` connector is stable relative to this project's own maturity, not
a claim of production hardening.

### Connectors in this repository

| Connector | Maintenance | Maturity | Notes |
| --- | --- | --- | --- |
| `clickup` | `alih-maintainers` | `established` | The reference implementation. Ships in every release, passes the conformance suite in CI, and its archives are pinned by the frozen v0.2.4 compatibility corpus. |
| `notion` | `alih-maintainers` | `experimental` | Read-only, and new. It covers databases, data sources, rows and the block tree; comments, files and page history are not implemented yet. Its archive shape may still change, so its output is not something Alih promises to keep stable. |

### What happens to a connector you contribute

```
implementation -> your tests -> conformance passes -> review -> classification -> merge
```

Classification is agreed during review, not after. In practice a new connector
from an external contributor is merged as `community` + `experimental`: you are
the primary maintainer, and its output is not yet something Alih promises to
keep stable.

Either dimension can change later. A community connector can become
`alih-maintainers` if the project decides to take it on. An `experimental`
connector becomes `established` once it has shipped, its behaviour has settled,
and its archives are covered by the compatibility corpus.

**What merging does not mean:** that Alih maintainers become the primary
maintainer, that the provider's API will keep working, that the connector will
exist in future major versions, or that anyone is on call for it.

**What it does mean:** the connector met the contracts in this document at
review time, and CI keeps checking that it still does.

Being the primary maintainer means being reachable on issues about your
connector and reviewing changes to it. There is no rota, no SLA, and no
paperwork. Classification never affects what a connector is allowed to do —
Alih Core treats every connector identically, and there is no capability, flag,
or limit tied to any of this.

---

## 14. What must never leak into Core

If your work requires any of the following, the design is wrong and the answer
is an issue rather than a patch:

- a provider name, hostname, or URL in any package outside your adapter;
- Core parsing your error strings;
- Core learning your object names;
- a manifest schema change to fit your provider;
- a credential reaching a host you did not declare;
- anything that makes an unknown look like a success.
