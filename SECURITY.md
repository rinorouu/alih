# Security Policy

Alih handles a credential that grants read access to a user's SaaS Workspace,
and it produces archives that people may rely on for recovery. Security reports
are taken seriously.

## Reporting a vulnerability

**Please do not report vulnerabilities in a public issue, pull request, or
discussion.**

Report privately through GitHub:

1. Go to the [Security tab](https://github.com/rinorouu/alih/security) of this
   repository.
2. Select **Report a vulnerability**.
3. Describe the issue.

This opens a private advisory visible only to you and the maintainers. If you
cannot use GitHub private vulnerability reporting, open a public issue that
says only that you have a security report and asks for a private channel —
without any detail about the issue itself.

### What to include

- What the vulnerability is, and which component it affects.
- The steps to reproduce it, ideally with a minimal case.
- The Alih version (`alih --version`), operating system, and architecture.
- What an attacker gains: token disclosure, a forged archive that verifies, a
  false `VERIFIED` result, code execution, or something else.
- Any suggested fix, if you have one.

**Never include a real API token, real Workspace data, or a real archive in a
report.** Redact them, or reproduce with synthetic data.

### What to expect

Alih is Alpha software maintained by a small team, so the following are
intentions rather than guarantees:

| Stage | Target |
| --- | --- |
| Acknowledgement of your report | within 7 days |
| Initial assessment | within 14 days |
| Fix or a stated plan for one | depends on severity and complexity |

You will be told what was decided and why, including if the report is not
treated as a vulnerability. Please give the maintainers a reasonable
opportunity to fix the issue before disclosing it publicly.

There is no bug bounty program. Alih is free and open-source software, and
there are no funds to pay for reports. Reporters are credited in the published
advisory and in `CHANGELOG.md` unless they ask not to be.

## Supported versions

Alih is in Alpha and has no long-term support branches. **Only the latest
published release receives security fixes.** Fixes ship in a new release rather
than as patches to older tags.

The current releases are listed on the
[Releases page](https://github.com/rinorouu/alih/releases).

## In scope

Reports about the following are in scope:

- **Credential exposure.** The token reaching logs, archives, reports, command
  output, error messages, crash output, or any file Alih writes. The token is
  never accepted as a command-line argument; a path that makes it possible is a
  vulnerability.
- **Credential storage.** Weaknesses in how the credential is stored on disk or
  in an operating-system credential store.
- **Verification bypass.** Any way to make a corrupt, incomplete, tampered, or
  forged archive produce a `VERIFIED` or `VERIFIED_WITH_LIMITATIONS` result.
  Verification integrity is Alih's core claim.
- **Silent data loss.** A path where expected data is missing and the archive
  still passes instead of reporting `INCOMPLETE`.
- **Malicious source responses.** Code execution, path traversal, archive
  extraction escapes, resource exhaustion, or SQL injection triggered by a
  crafted API response or attachment.
- **Installer integrity.** Any weakness in `install.sh` or `install.ps1` that
  allows an unverified binary to be installed, defeats the SHA-256 check,
  downgrades the transport from HTTPS, or writes outside the intended
  user-level destination.
- **Release integrity.** Weaknesses in the release workflow that could allow a
  published artifact not to match the tagged source.
- **Unexpected writes to the source.** Alih is read-only; any non-`GET` request
  to a source API is a vulnerability.

## Out of scope

- Vulnerabilities in ClickUp itself, or in any other SaaS provider. Report
  those to the provider.
- Vulnerabilities in Go, in `github.com/mattn/go-sqlite3`, or in another
  upstream dependency, unless Alih's use of it creates the exposure. Report
  those upstream, and feel free to open a public issue here so the dependency
  can be updated.
- Anything requiring an attacker who already has read access to the archive
  directory or to the user's credential store. Alih is local-first: it assumes
  the user's own machine and account are trusted.
- Limitations Alih already discloses in `README.md` under "What Alih does not
  claim" — for example, that an archive is not a point-in-time snapshot, and
  that archive-internal evidence cannot prove account-wide visibility. These
  are documented properties, not defects. If you believe a disclosure is
  materially misleading, that is worth an issue.
- Missing hardening with no demonstrated impact, and automated scanner output
  submitted without an explanation of exploitability.

## Security properties Alih aims to hold

These are the properties a report can usefully be measured against:

- Every request to a source API is a `GET`.
- The credential never appears in logs, archives, reports, or command output,
  and is never read from a command-line argument.
- An archive that fails any checksum, count, or reference check cannot be
  reported as verified.
- `INCOMPLETE` and `FAILED` exit non-zero.
- The installers require HTTPS, verify the downloaded binary against
  `SHA256SUMS`, confirm the binary's reported version, stage it before
  replacing an existing installation, install at user level, and never invoke
  privilege escalation.
- Verification reads the archive only. It does not contact or modify the
  source.
