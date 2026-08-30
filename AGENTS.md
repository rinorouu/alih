AGENTS.md — Alih

Purpose

This file defines how coding agents should work inside the Alih repository.

"PRD.md" is the product source of truth.

Read "PRD.md" before making product or architectural changes, but do not treat the entire PRD as an instruction to implement everything it contains.

---

1. Work Only on the Requested Scope

Implement only the milestone, task, or change explicitly requested by the user.

Do not implement future milestones preemptively.

Do not add features merely because they appear useful.

If the user requests:

«Implement M0.»

Then implement M0 only.

Do not continue into M1 or later milestones unless explicitly requested.

---

2. Preserve Product Invariants

Changes must preserve the following Alih principles unless the user explicitly decides to revise the product specification:

- local-first;
- read-only access to source SaaS during V0;
- official APIs only;
- no silent data loss;
- evidence over assumption;
- explicit handling of unsupported, unavailable, unknown, partial, and failed states;
- connector-specific behavior remains isolated from the core portable model;
- inability to prove completeness must not be presented as success.

Do not weaken these invariants merely to make an implementation easier.

---

3. Fail Closed

Alih deals with data portability and verification.

A false success is worse than a visible failure.

If completeness cannot be established, report that uncertainty explicitly.

Never convert:

- missing records;
- incomplete pagination;
- unresolved supported data;
- broken relationships;
- failed supported attachments;
- corrupt archives;
- unknown extraction termination;

into a clean verified result.

Prefer:

«Alih cannot prove this archive is complete.»

over:

«Probably complete.»

---

4. Do Not Guess Source Capabilities

Do not assume that the source API exposes data merely because the source product contains that data.

Distinguish between:

- "SUPPORTED"
- "PARTIAL"
- "UNSUPPORTED"
- "UNAVAILABLE"
- "UNKNOWN"
- "FAILED"

If the official API cannot provide sufficient evidence, preserve that limitation.

Do not silently work around official API limitations using scraping, undocumented endpoints, reverse engineering, browser automation, or other unofficial access methods.

---

5. Keep V0 Small

V0 is an engineering validation experiment.

Unless explicitly requested, do not add:

- web UI;
- desktop GUI;
- browser extension;
- cloud backend;
- telemetry;
- analytics;
- billing;
- subscriptions;
- AI or LLM functionality;
- scheduled backups;
- continuous sync;
- additional SaaS connectors;
- restore functionality;
- cross-SaaS migration;
- production OAuth onboarding;
- marketing infrastructure.

Do not build production infrastructure before the underlying portability model is proven.

---

6. Respect Milestone Gates

Follow the milestone sequence defined in "PRD.md".

In particular, M2 ("alih scan") is a major go/no-go gate.

Do not treat later implementation as evidence that an earlier milestone is correct.

If source inventory cannot be trusted, stop and report the problem rather than compensating for it downstream.

---

7. Prefer Simple, Auditable Implementations

Favor:

- explicit code over unnecessary abstraction;
- deterministic behavior over clever behavior;
- small interfaces over speculative frameworks;
- standard library functionality where appropriate;
- testable transformations;
- stable source identifiers;
- reproducible output.

Avoid abstractions created solely for hypothetical future requirements.

Multi-connector architecture should remain possible, but V0 must not implement infrastructure that has no current use.

---

8. Preserve Raw Evidence

When transforming source data, retain enough raw source evidence to debug and verify the transformation where required by the PRD.

Never mutate raw evidence to make it conform to the portable model.

Transformation should conceptually remain:

Source API
    ↓
Raw Source Evidence
    ↓
Normalization
    ↓
Portable Model
    ↓
Archive
    ↓
Verification

---

9. Security and Privacy

Never:

- commit credentials;
- print API tokens;
- place credentials inside archives;
- expose secrets in logs;
- send user content to external AI services;
- introduce telemetry without explicit approval.

Use the minimum source permissions required for the requested milestone.

V0 should remain local-first and read-only.

---

10. Testing Is Part of Correctness

Changes affecting extraction, normalization, archive generation, or verification should include appropriate tests.

Prioritize tests for:

- pagination;
- duplicate prevention;
- partial extraction;
- count reconciliation;
- relationship integrity;
- attachment failures;
- manifest consistency;
- archive corruption;
- unexpected API responses.

Do not modify tests merely to make an incorrect implementation pass.

If an existing test represents an outdated requirement, explain why before changing it.

---

11. Do Not Hide Problems

When implementation reveals a limitation, inconsistency, or conflict with the PRD:

1. identify it clearly;
2. determine whether it is an implementation bug, source limitation, or specification issue;
3. fix it only when the correct behavior is clear and within the requested scope;
4. otherwise report it and stop at the appropriate boundary.

Do not invent behavior just to complete a task.

---

12. Changes to the PRD

Do not silently modify "PRD.md" to match the implementation.

If implementation evidence suggests that the PRD is wrong or impractical, explain the conflict.

The user decides whether product requirements should change.

Code should not redefine product requirements by accident.

---

13. Dependency Discipline

Add dependencies only when they provide clear value for the requested implementation.

Before adding a dependency, consider whether the Go standard library or an existing project dependency is sufficient.

Avoid large frameworks for small problems.

Do not add infrastructure dependencies for hypothetical future features.

---

14. Completion Behavior

After completing a requested task:

1. stop at the requested scope;
2. run relevant tests and checks;
3. summarize what changed;
4. report what was verified;
5. disclose limitations, failures, or unresolved questions;
6. identify the next PRD milestone only as context.

Do not automatically begin the next milestone.

---

15. Audit Behavior

When asked to review or audit Alih, do not assume every difference, missing feature, or unusual implementation is a bug.

For each finding, determine whether it is:

- a real defect;
- a correctness risk;
- a security/privacy risk;
- intentional behavior;
- deferred work from a future milestone;
- unsupported source behavior;
- or merely a possible improvement.

Only recommend changes when there is evidence that the current behavior should change.

If the implementation is correct for the current milestone, leave it unchanged.

If a finding is valid and critical within the explicitly requested scope, fix it when the user has requested implementation/fixes.

If it is intentional, deferred, ambiguous, or outside scope, explain it instead of modifying the code.

---

Core Rule

When uncertain, preserve evidence and surface uncertainty.

Never manufacture confidence.
