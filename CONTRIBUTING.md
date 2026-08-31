# Contributing to Alih

Thank you for considering a contribution. Alih is early Alpha software
maintained by a small team, so this document describes how to contribute in a
way that is likely to be accepted.

Alih is licensed under the [Apache License 2.0](LICENSE). By contributing, you
agree that your contribution is licensed under the same terms.

## Open an issue first

**Please open an issue before writing code for anything beyond a small fix.**

| Contribution | Process |
| --- | --- |
| Bug report | Open an issue. No prior discussion needed. |
| Typo, documentation fix, small clear bug fix | Open a pull request directly. |
| New feature, new command, new flag | Open an issue first and wait for a reply. |
| New connector, or a change to the archive format | Open an issue first. These are product-direction decisions. |
| Platform test results and reproductions | Open an issue. These are genuinely useful. |

This is not bureaucracy. Alih makes explicit claims about what it can and
cannot prove, and a change that is reasonable in isolation can quietly weaken
one of those claims. Discussing the intent first avoids work being rejected
after it is written.

## Principles a change must not break

The README states Alih's principles. In practice, a pull request will be
rejected if it breaks one of these, regardless of how well it is written:

- **Read-only at the source.** Every request Alih makes to a SaaS API is a
  `GET`. Alih never creates, modifies, or deletes anything at the source.
- **No silent loss.** Expected and archived counts are reconciled. A shortfall
  must fail the archive, never pass quietly.
- **Evidence over assumption.** `SUPPORTED`, `PARTIAL`, `UNSUPPORTED`,
  `UNAVAILABLE`, `UNKNOWN`, and `FAILED` are distinct states. Do not collapse
  them, and do not report an unproven claim as proven.
- **Fail closed.** Corruption, missing data, broken references, count
  mismatches, and unproven claims must prevent a clean verified result.
- **Credentials stay out.** The token must never reach logs, archives,
  reports, or command output, and is never accepted as a command-line
  argument.
- **Local-first.** The Alpha workflow requires no Alih backend, cloud storage,
  or telemetry. Do not add a network call that is not a read of the connector's
  documented API.
- **Documented APIs only.** Alih does not use scraping, undocumented
  endpoints, or browser automation to turn unavailable source data into
  supported data.

## Development setup

Alih requires **Go 1.22 or newer** and a **working C compiler**, because the
SQLite driver uses cgo.

```bash
git clone https://github.com/rinorouu/alih.git
cd alih
go build ./cmd/alih
```

On Windows, use the MSYS2 UCRT64 toolchain (`mingw-w64-ucrt-x86_64-gcc`); this
is the compiler the release workflow uses and the one the build is verified
against.

## Before you open a pull request

Run everything CI runs:

```bash
gofmt -l $(git ls-files '*.go')   # must print nothing
go vet ./...
go test ./...
go test -race ./...
```

If you changed `install.sh` or `scripts/test-install.sh`:

```bash
sh -n install.sh
sh scripts/test-install.sh
```

If you changed `install.ps1`, verify it parses in PowerShell. CI does this
automatically on the Windows runner.

CI runs the full test suite on Linux, Windows, and macOS. Filesystem paths,
SQLite behavior, and credential storage differ across these platforms, and past
releases have failed on Windows specifically — please keep tests portable.

## Code expectations

- Every Go file carries the Apache-2.0 header used by the existing files.
- Match the surrounding code: its naming, its comment density, its structure.
  Alih's code is deliberately plain.
- **Add tests.** A change to behavior needs a test that fails without it. A
  change to failure handling needs a test that proves it still fails closed.
- Do not add a dependency without raising it in an issue first. Alih currently
  has exactly one direct dependency, and that is intentional.
- Never commit real credentials, real Workspace data, or real tokens. Test
  fixtures use synthetic values such as `example.com`.

## Changelog

**Update `CHANGELOG.md` in the same pull request.** Add your entry under
`## [Unreleased]`, in the appropriate section (`Added`, `Changed`, `Fixed`,
`Security`), and write it for the person running Alih rather than for the
person reading the diff.

Internal refactors and test-only changes need an entry only when they affect
supported behavior, compatibility, security, or release confidence.

## Commit and pull request style

Commit subjects use a lowercase type prefix, in the imperative, describing the
user-visible effect:

```text
fix: support native Windows release tests
docs: update alpha release installation
test: accept Windows credential storage notice
ci: add cross-platform release workflow
```

Common types: `feat`, `fix`, `docs`, `test`, `ci`, `chore`, `release`.

In the pull request description, state what changed, why, and how you verified
it. If it relates to an issue, link the issue.

## Reporting a vulnerability

Do not report security vulnerabilities in a public issue or pull request. See
[SECURITY.md](SECURITY.md) for the private reporting process.

## Questions

If you are unsure whether something is in scope, open an issue and ask. That is
a legitimate use of the issue tracker, and it is cheaper than writing code that
has to be turned away.
