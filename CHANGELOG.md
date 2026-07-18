# Changelog

All notable Ranch Hand changes will be recorded here. The project uses semantic
versions. Public Preview releases are publicly downloadable but are not
generally available or production-supported releases.

## Unreleased

## [0.1.0-rc.16] - 2026-07-18

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Changed

- Remote Linux's normal server-identity path is now **Get server fingerprint**
  followed by **Trust this server key**. Manual fingerprint entry and
  out-of-band verification remain available under advanced options.
- Remote authentication defaults to a clear **Username and password** choice
  and shows only the password field. Selecting **SSH private key** shows only
  the key and optional key-passphrase fields.
- The live check identifies the exact `user@host:port` connection and uses the
  direct **Test SSH connection and target** action.
- A successful preflight credential is retained only in the running local
  session for installation, eliminating duplicate password or token entry for
  Remote Linux, Azure Container Apps, and Cloudflare. Failed preflight and
  installation credentials remain in the local form for correction instead of
  disappearing.
- Azure and Cloudflare installation panels now summarize the target that
  passed preflight and proceed with the already validated in-memory token.
- Docker Desktop was verified to require no deployment credential and retains
  its direct named-pipe preflight-to-install path.

### Fixed

- SSH host-key mismatch and Linux authentication rejection now produce
  distinct errors. Authentication errors name the selected Linux user and
  explain whether to use the password or private key accepted by SSH itself.

### Verification

- Full Go test suite passes.
- The React application passes TypeScript checking and production build.

## [0.1.0-rc.15] - 2026-07-18

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Added

- Remote Linux setup can inspect the SSH service's presented host-key
  algorithm and SHA-256 fingerprint without sending user credentials or
  requiring OpenSSH on Windows.
- The guided flow explains the separate roles of server identity and Linux
  username/password or private-key authentication, requires explicit
  out-of-band verification, and then fills the pinned fingerprint.

### Security

- Changing the remote host or SSH port clears the inspected identity and
  existing pin so a fingerprint cannot be reused for a different endpoint.
- Ranch Hand does not silently trust a first-seen key; operators are directed
  to compare it with the Azure/server console or server administrator.

### Verification

- Go plan, adapter, and server tests pass.
- The React application passes TypeScript checking and production build.

## [0.1.0-rc.14] - 2026-07-18

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Added

- Local WSL Compose now presents an explicit **Demo mode** toggle in the
  secret-free deployment plan. It defaults to off.
- With demo mode off, Ranch Hand starts RepoWrangler with `DEMO_MODE=false`,
  generates unique 256-bit session and credential-encryption secrets, protects
  them in the target-only `.env`, and opens RepoWrangler's real first-run
  provider setup flow.
- With demo mode on, the existing no-credential mock-data profile remains
  available as an intentional evaluation choice.

### Fixed

- Ranch Hand no longer silently forces mock data for a local WSL installation.
- The WSL confirmation and success text now identify the selected real or demo
  operating mode before installation.

### Verification

- Started the v1.0.10 image with the real-mode environment, verified readiness
  reported `demoMode: false`, verified authentication reported
  `setupMode: true`, and removed the smoke deployment.

## [0.1.0-rc.13] - 2026-07-18

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Fixed

- Local WSL Compose no longer pulls RepoWrangler v1.0.10 from GHCR. Ranch Hand
  downloads the exact public companion image archive from its immutable rc.13
  release, verifies its hard-coded size and SHA-256, caches it, and streams it
  into the selected WSL Docker Engine.
- The generated WSL Compose override selects the verified locally loaded image
  and enforces `pull_policy: never`, so installation requires no GitHub account,
  registry credentials, or registry request.
- The ownership marker separately records the immutable product image digest
  and the verified runtime tag, allowing recovery and verification to retain
  exact identity checks without pretending the local tag is a registry digest.

### Verification

- Loaded the 286,575,554-byte archive into a clean local tag, verified image ID
  `sha256:89d1b4091137eef57c91270d363fb6c76e6d60c94dcac92b129b2b8629f45093`,
  started RepoWrangler with `--pull never`, and received the exact v1.0.10 live
  health identity before ownership-safe cleanup.

## [0.1.0-rc.12] - 2026-07-18

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Fixed

- WSL and remote Linux Compose now pull the exact verified image before Ranch
  Hand creates the dedicated installation directory, marker, or Docker
  resources. Registry authentication and availability failures therefore leave
  no failed installation to recover.
- Compose starts with `--pull never` after the successful pre-mutation pull, so
  apply cannot repeat a registry request after ownership state is written.
- Registry failures preserve bounded Docker output, including authorization
  errors, in the visible operation result.

## [0.1.0-rc.11] - 2026-07-18

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Fixed

- Replaced the terminal WSL directory-collision message with an explicit
  **Inspect and remove Ranch Hand remnants** action when no active lifecycle or
  installation record exists for the bound plan.
- Added a loopback-authenticated cleanup endpoint that rechecks the verified
  plan, release artifact, current target collision, and lifecycle inventory
  immediately before mutation.
- Limited journal-free cleanup to an exact matching Ranch Hand ownership marker,
  the known legacy empty-marker pattern, or a completely empty dedicated
  directory. Mismatched or invalid markers, unknown files, symlinks, changed
  deployment files, and unowned Docker resources remain hard stops.
- Refreshed live target preflight automatically after successful cleanup so the
  operator can proceed without recreating the deployment plan.

## [0.1.0-rc.10] - 2026-07-17

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Fixed

- Supplied a process-only interpolation sentinel for the disabled PostgreSQL
  profile when starting WSL and remote Linux SQLite evaluations. Ranch Hand does
  not create or persist a database password for a service it does not start.
- Explicitly passed Ranch Hand's generated `.env` to Docker Compose. Absolute
  Compose file paths no longer cause interpolation to search the launch user's
  working directory instead of the owned installation directory.
- Made ownership-checked recovery compatible with rc.9 and earlier failed
  installs whose environment lacks `POSTGRES_PASSWORD`. The process-only value
  is used solely to parse Compose; marker, file-hash, container,
  volume, and label checks remain required before deletion.
- Included bounded Compose output when owned failed-install cleanup fails.

## [0.1.0-rc.9] - 2026-07-17

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Fixed

- Corrected the shared WSL/SSH file-transfer shell wrapper. Payloads are now
  piped into the entire compound `umask; cat; chmod; mv` command, rather than
  into `umask` alone, which had created empty Compose, environment, and marker
  files and caused every remote-style Compose apply to fail.
- Added narrowly scoped recovery for the exact empty marker and empty deployment
  files produced by that defect. Docker resources, non-empty or invalid markers,
  unknown content, and symlinks remain hard cleanup stops.
- Retained the one-time launch token in browser `sessionStorage` after removing
  it from the address bar, allowing a same-tab refresh without losing the
  authenticated loopback session. Closing the tab still clears it.
- Preserved bounded, sanitized Docker Compose command output in apply failures
  instead of reporting only `exit status 1`.

### Added

- Distinguished a missing Docker command from a stopped/unauthorized Docker
  Engine and a missing Compose v2 plugin during WSL and remote Linux preflight.
- Added a Windows loopback port 8080 availability check before WSL mutation.

## [0.1.0-rc.8] - 2026-07-17

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Fixed

- Extended failed-install cleanup to the partial pre-marker transfer state. It
  removes only Ranch Hand's seven fixed final/temporary filenames, only when no
  Compose container or volume exists, and requires the private directory to be
  empty afterward. Unknown content, symlinks, invalid markers, and Docker
  resources remain hard stops.
- Invalidated the bound plan, staged bundle, live preflight, and confirmation
  whenever a deployment input changes. The UI can no longer display a new
  Compose project while submitting the previous project's plan and lock ID.
- Bound WSL progress and install summaries to the exact immutable plan being
  submitted rather than mutable form fields.

### Added

- Added a Ranch Hand **Managed deployments** inventory with target, project,
  version, state, last update, and the local WSL endpoint.
- Renamed local WSL recovery to **Remove failed installation and release lock**
  so its ownership-checked cleanup behavior is explicit.

## [0.1.0-rc.7] - 2026-07-17

**Classification: Public Preview.** Publicly downloadable and intended for
evaluation and feedback; unsigned, not production-supported, and not GA.

### Fixed

- Completed interrupted WSL recovery for the earliest apply crash boundary:
  Ranch Hand can remove its exact dedicated directory when it is still empty
  because the process stopped before writing the ownership marker.
- Preserved the refusal boundary for any markerless directory containing a file
  or subdirectory. Ranch Hand never recursively removes or adopts that path.

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
