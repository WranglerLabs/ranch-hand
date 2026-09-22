# Changelog

All notable Ranch Hand changes will be recorded here. The project uses semantic versions; release candidates are not generally available releases.

## [0.1.0-rc.1] - 2026-07-17

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
