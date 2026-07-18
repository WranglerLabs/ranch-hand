# Ranch Hand GA readiness

Ranch Hand `v0.1.0-rc.19` is a **Public Preview**. It is the primary recommended
way for a Windows operator to begin a RepoWrangler deployment without cloning
source. The manual RepoWrangler recipes remain supported for contributors,
custom automation, production topologies not yet covered by Ranch Hand, and
operators who prefer to own every command.

Public Preview means the downloadable artifact and documented workflows are
available for evaluation and feedback. It does not mean production support,
compatibility guarantees, or a completed lifecycle on every target.

## GA promotion gates

Every required gate below must be complete for the exact release artifact before
Ranch Hand can be called generally available.

| Gate | Required GA outcome | Preview status |
|---|---|---|
| Trusted Windows distribution | Authenticode-signed and timestamped executable from the approved SignPath policy; publisher, signature, checksum, SBOM, provenance, revocation, and stable-channel checks pass | Open; preview executable is intentionally unsigned |
| RepoWrangler compatibility | Ranch Hand installs and manages the latest supported RepoWrangler patch through its immutable manifest and rejects unsupported, floating, downgraded, or tampered inputs | Partial; v1.0.10 discovery and verification pass |
| Production configuration | Guided production credentials, authentication, database, storage, domain/HTTPS, and target settings are supported without editing generated files | Partial; local WSL now defaults to real first-run provider setup with generated local secrets, while other production target settings remain open |
| Azure authentication and lifecycle | Integrated Azure sign-in replaces pasted ARM tokens; ACA supports PostgreSQL, supported existing/dedicated resource choices, managed domains, backup, update, restore, rollback, repair, and uninstall | Open |
| Cloudflare lifecycle | Production secrets and domains plus ownership-safe backup, update, restore, rollback, repair, and uninstall are implemented and tested | Open |
| Local and remote Compose lifecycle | Production credentials and data configuration, full backup/update/restore/rollback/repair/uninstall, and a documented trusted-ingress handoff for public remote deployments pass | Partial; local lifecycle is broad, remote is install-only |
| Removal and data retention | Every supported target offers explicit uninstall choices for retain data, export/backup first, or permanent deletion with ownership checks and confirmation | Open |
| Release and state upgrades | Ranch Hand application upgrades preserve or explicitly migrate its catalog, plans, journals, installation records, backups, and compatibility contracts; rollback and recovery are documented | Partial; schema compatibility exists, application upgrade channel is not complete |
| Clean-machine and real-target UAT | The exact signed artifact passes install, update, failed-health/migration recovery, rollback, and removal on clean supported Windows profiles and disposable real targets | Open |
| Accessibility and usability | Keyboard, screen reader, zoom, forced-colors, reduced-motion, high-DPI, understandable errors, and task-based operator tests pass | Open |
| Security release gate | Privileged-adapter review, least-privilege failures, secret redaction, SSH host-key rejection, tamper/downgrade tests, dependency review, CodeQL, and private vulnerability-reporting checks pass | Partial; automated and boundary tests pass, final signed-artifact review remains |
| Documentation and support | Public download, install, target, lifecycle, recovery, diagnostics, manual-equivalence, upgrade, compatibility, and removal guides are task-tested; latest-version best-effort support policy is published | Partial; guides exist, final task testing and GA support matrix remain |

## Target readiness rule

A target may be included in the GA support matrix only when its production
configuration, complete supported lifecycle, recovery behavior, removal policy,
and real-target tests all pass. A target that has not met those gates remains a
Preview target even if Ranch Hand itself later reaches GA.

## Support boundary during Public Preview

Public Preview support is community-style and best effort with no SLA, response
time, compatibility guarantee, or production commitment. Operators own their
accounts, credentials, costs, backups, availability, and disaster recovery.
Security reports use private vulnerability reporting.
