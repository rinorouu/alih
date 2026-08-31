# Changelog

This file records notable user-facing changes to Alih.

Add new work under **Unreleased**. When a release is published, move those
entries into a versioned section with the release date. Internal refactors and
test-only changes need an entry only when they affect supported behavior,
compatibility, security, or release confidence.

## [Unreleased]

## [0.2.4] - 2026-08-31

This is the first release published from the public repository.

### Added

- One-command installers for Linux, WSL, macOS, and Windows amd64.
- Automatic operating-system and architecture selection for the five existing
  release binaries.
- Installer checks for unsupported platforms, download failures, checksum
  mismatches, and preservation of an existing installation on failure.
- First-run workflow guidance in `alih` and `alih --help`.
- `CONTRIBUTING.md`, describing the issue-first process, the principles a
  change must not break, and the checks to run before opening a pull request.
- Continuous integration that runs formatting, `go vet`, the POSIX installer
  test, and the full test suite with the race detector on Linux, Windows, and
  macOS for every push and pull request to `main`.
- Dependabot configuration for Go modules and GitHub Actions, so dependency
  and workflow-action updates arrive as pull requests that CI verifies.

### Changed

- Missing-authentication errors now tell the user to set
  `ALIH_CLICKUP_TOKEN` and run `alih auth`.
- Release installation is now the primary README path; manual installation and
  source builds remain documented.
- The release workflow now validates the installers and publishes them with
  the existing five platform binaries.
- Updated `github.com/mattn/go-sqlite3` from 1.14.49 to 1.14.50.

### Security

- Installers require HTTPS, verify the selected binary against `SHA256SUMS`,
  confirm its reported release version, and stage it before replacing an
  existing installation.
- Installers use user-level destinations and do not silently invoke privilege
  escalation.
- `SECURITY.md`, documenting private vulnerability reporting through GitHub,
  the supported version, what is in and out of scope, and the security
  properties Alih aims to hold.

## [0.2.3] - 2026-08-31

This was the first successfully published five-platform GitHub Release.

### Fixed

- Accepted the expected Windows credential-storage notice in native release
  tests.

## [0.2.2] - 2026-08-31

This tag was a release-pipeline stabilization iteration. Its GitHub Actions
run failed, so no GitHub Release was published for this tag.

### Fixed

- Made SQLite paths, credentials, and filesystem tests portable to the native
  Windows release runner.

## [0.2.1] - 2026-08-31

This tag was a release-pipeline stabilization iteration. Its GitHub Actions
run failed, so no GitHub Release was published for this tag.

### Fixed

- Configured the Windows release build to use the working MSYS2 UCRT64 C
  compiler required by cgo and SQLite.

## [0.2.0] - 2026-08-31

This tag introduced the five-platform workflow. Its GitHub Actions run failed,
so no GitHub Release was published for this tag.

### Added

- Native release binaries for Linux amd64/arm64, Windows amd64, and macOS
  amd64/arm64.
- SHA-256 checksums and build-time version injection for release artifacts.

## [0.1.0-alpha.1] - 2026-08-30

### Added

- The primary `alih auth` and `alih backup` workflow.
- Backup orchestration through scan, extract, portable archive creation,
  independent verification, and Recovery Report generation.

[Unreleased]: https://github.com/rinorouu/alih/compare/v0.2.4...HEAD
[0.2.4]: https://github.com/rinorouu/alih/compare/v0.2.3...v0.2.4
[0.2.3]: https://github.com/rinorouu/alih/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/rinorouu/alih/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/rinorouu/alih/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/rinorouu/alih/compare/v0.1.0-alpha.1...v0.2.0
[0.1.0-alpha.1]: https://github.com/rinorouu/alih/releases/tag/v0.1.0-alpha.1
