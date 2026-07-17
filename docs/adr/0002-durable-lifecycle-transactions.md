# ADR-0002: Durable backup-first lifecycle transactions

**Status:** Accepted

**Date:** 2026-07-17

**Deciders:** WranglerLabs maintainers

## Context

Ranch Hand will install, update, repair, restore, roll back, and uninstall RepoWrangler across four target families. These operations can outlive an HTTP request and can fail after a target has partially changed. Updates must back up recoverable state before activation, and a second Ranch Hand process must not mutate the same deployment concurrently.

Deployment plans are portable and secret-free. Lifecycle state is machine-local operational evidence and must also remain secret-free. It must survive application or workstation interruption without depending on a cloned RepoWrangler repository, vendor CLI state, or an external database.

## Decision

Ranch Hand uses a durable, per-deployment transaction journal stored in its user-scoped application state directory.

- A stable deployment identifier is derived from the target kind and non-secret target configuration. Changing the release or display name does not change deployment identity.
- An exclusive `active` record permits one lifecycle operation per deployment. A completed, recovered, or failed operation releases it.
- Every operation embeds its canonical deployment-plan snapshot and plan digest.
- State transitions are atomic file replacements. The current phase is always either the prior complete state or the next complete state.
- Events form a SHA-256 chain over the immutable operation header and ordered phases. This detects corruption and unsophisticated edits; it is not an authentication boundary against a malicious process running as the same operating-system user.
- Updates cannot commit unless `backup-complete`, `staged`, `applied`, and `verified` all occurred in order.
- Failed activation or verification can enter `recovery-started` and must end as `recovered` or `failed`.
- Journals contain phase codes and release identity only. Target output, credentials, tokens, private keys, and secret values are excluded.

```text
prepared -> backup-complete -> staged -> applied -> verified -> committed
    |              |             |          |           |
    +------------ failed <-------+----------+-----------+
                                           |
                                           +-> recovery-started -> recovered
```

Operations that do not need every phase use the applicable subset, but their commit rules remain explicit. For example, install requires staged/applied/verified, backup requires backup-complete, and uninstall requires applied.

## Options Considered

### Direct, unjournaled adapter calls

| Dimension | Assessment |
|---|---|
| Complexity | Low initially |
| Recovery | Poor |
| Auditability | Poor |
| Cross-process safety | None |

**Pros:** Minimal implementation.

**Cons:** Cannot reliably identify partial completion, enforce backup-first update, or resume after interruption.

### In-memory transaction coordinator

| Dimension | Assessment |
|---|---|
| Complexity | Medium |
| Recovery | Only while the process lives |
| Auditability | Session-local |
| Cross-process safety | Partial |

**Pros:** Clear orchestration API without disk state.

**Cons:** Process or workstation interruption loses the authoritative phase and recovery intent.

### Durable local journal (selected)

| Dimension | Assessment |
|---|---|
| Complexity | Medium |
| Recovery | Survives process interruption |
| Auditability | Durable and secret-free |
| Cross-process safety | One active operation per deployment |

**Pros:** Enforces lifecycle policy and provides deterministic resume/recovery input without external infrastructure.

**Cons:** Requires state-schema compatibility, corruption handling, retention, and careful atomic filesystem behavior.

## Trade-off Analysis

A local journal adds code and durable-state compatibility requirements, but those costs are smaller than attempting to infer target state after an interrupted mutation. An embedded database was not selected because the state is small, append-like, and isolated per deployment; atomic JSON keeps recovery inspectable and avoids a new runtime component.

## Consequences

- Adapter mutation methods must execute through the lifecycle coordinator rather than changing targets directly.
- Update and rollback tests can assert phase ordering independently of any cloud account or Docker host.
- A later Ranch Hand version must preserve or explicitly migrate journal schema `1.1`.
- Recovery UX must surface active operations and never silently discard an incomplete journal.
- Retention and redacted diagnostics can operate on structured journals without collecting credentials.
- Local Docker backups are streamed through the Engine archive API while the owned container is stopped, stored beneath Ranch Hand's user-scoped root, hashed before inventory registration, and followed by restart/readiness verification.
- Local Docker update uses a new owned volume seeded from the exact recorded backup. The prior stopped container and volume remain untouched in the rollback pool until the new image passes readiness and exact release-identity verification.
- Local Docker restore and rollback keep selected input and fresh safety backups distinct; repair uses its fresh safety backup as reconstruction input. All use a new owned volume and recover the exact preserved pre-operation container when activation fails.

## Action Items

1. [x] Implement stable deployment identity, exclusive active-operation records, atomic transitions, plan snapshots, and event-chain validation.
2. [x] Enforce backup-first update and operation-specific commit requirements.
3. [x] Implement the lifecycle coordinator, exact-backup references, backup inventory, and automatic recovery sequencing.
4. [x] Implement bounded evaluation install methods for all four initial targets and expose them only through coordinator-driven loopback operations. Local Docker consistent backup, update, restore, rollback, and repair are also complete.
5. [x] Add active-operation recovery controls to the loopback API and Windows UI, with retryable recovery locks and fresh in-memory credentials.
6. [x] Add durable installation/current-version records and an explicit lifecycle schema migration policy.
7. [ ] Add rollback-pool retention and pruning controls.
