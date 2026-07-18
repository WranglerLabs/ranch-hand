# Changelog

All notable Ranch Hand changes will be recorded here. The project uses semantic
versions. Public Preview releases are publicly downloadable but are not
generally available or production-supported releases.

## Unreleased

## [0.1.0-rc.6] - 2026-07-17

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Fixed

- Made target preflight recognize Ranch Hand's own interrupted lifecycle
  journal instead of reporting its owned WSL installation directory as an
  unrelated collision.
- Added the ownership-checked recovery action directly to the blocked WSL
  preflight result, followed by a clean preflight retry.
- Distinguished an already committed Ranch Hand installation from both an
  interrupted install and an unknown directory. Unknown directories remain
  protected and are never adopted or removed automatically.

## [0.1.0-rc.5] - 2026-07-17

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Fixed

- Made an unchecked WSL install confirmation produce a visible instruction
  instead of leaving a silently disabled button.
- Added an immediate WSL installation status message and one-second lifecycle
  journal polling while Docker pulls, creates, starts, and verifies the target.
- Enabled credential-free recovery controls for interrupted local WSL operations.

## [0.1.0-rc.4] - 2026-07-17

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Fixed

- Changed the WSL default Compose project to `repo-wrangler-ranch-hand` so a
  normal existing `repo-wrangler` Compose deployment does not block a new Ranch
  Hand evaluation installation.
- Replaced leaked remote-Linux collision wording with local WSL-specific,
  non-destructive guidance that names the conflicting project.
- Prefilled remote Linux SSH port `22` and Compose project name, and derived the
  default installation directory from the entered SSH username while preserving
  an operator-customized path.

## [0.1.0-rc.3] - 2026-07-17

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Fixed

- Split the misleading local target into **Local Docker Compose — WSL** and
  **Local Docker Desktop**. The WSL target executes the verified Compose bundle
  inside a detected WSL2 distribution without Docker Desktop or SSH; the
  Docker Desktop target retains the Windows Docker Engine API lifecycle.
- Removed the WSL volume and listen-address questions. Ranch Hand selects the
  Docker-managed volume and Windows loopback address automatically and reports
  a stopped WSL Docker service explicitly during preflight.

## [0.1.0-rc.2] - 2026-07-17

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Fixed

- Made the normal deployment workflow discover and preselect the latest stable
  RepoWrangler release compatible with the selected target. Prerelease and
  specific-version deployment are now explicit operator choices instead of
  requiring every user to know and type a release tag.

### Documentation

- Classified `v0.1.0-rc.1` as the first Ranch Hand Public Preview and made it the
  primary recommended Windows deployment path for RepoWrangler.
- Published the complete GA promotion contract covering signed distribution,
  RepoWrangler compatibility, production configuration and lifecycle parity,
  uninstall/data retention, application upgrades, real-target UAT,
  accessibility, security, documentation, and best-effort support.
- Added a task-oriented Windows operator guide covering acquisition, verification,
  launch, target prerequisites, the guided workflow, lifecycle operations, local
  state, diagnostics, and the manual RepoWrangler deployment alternative.

## [0.1.0-rc.1] - 2026-07-17

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Added

- Windows-first loopback application shell with an embedded React interface and random one-time launch token.
- Immutable RepoWrangler release discovery, SHA-256/size verification, Sigstore provenance verification, SPDX SBOM verification, and versioned local caching.
- Versioned secret-free deployment plans, export, artifact preflight, non-mutating dry run, and verified bundle staging.
- Bounded evaluation installers for local Docker, Azure Container Apps, Cloudflare Workers/D1, and remote Linux Docker Compose over pinned-host SSH.
- Durable lifecycle journals, installation records, backup inventory, backup-first local update/restore/rollback/repair, retryable interrupted-operation recovery, and redacted diagnostics.
- Ownership-checked local rollback-pool inventory and explicit keep-newest pruning that retains backup archives.
- Manual unsigned Windows RC workflow producing an executable, checksum, SPDX SBOM, and GitHub provenance attestation without publishing a GitHub Release.

### Security

- No Git, Node.js, Go, WSL, Azure CLI, Wrangler CLI, Docker CLI, OpenSSH executable, or universal proxy is required on the Windows control workstation.
- Deployment credentials remain in memory and are excluded from plans, journals, diagnostics, and release artifacts.
- Target mutation is constrained by exact release identity and target-specific ownership evidence.

### Known RC boundaries

- The executable is unsigned; Windows SmartScreen or organizational application-control policy may warn or block it.
- Azure evaluation deployment currently accepts an externally obtained temporary ARM access token; integrated interactive Azure sign-in is not included in this candidate.
- Cloud and remote adapters provide bounded new evaluation installs; their production backup/update/restore/rollback/uninstall contracts remain future work.
- Local uninstall with retain-data/permanent-delete choices remains a follow-on lifecycle item.
- There is no commercial support SLA.
