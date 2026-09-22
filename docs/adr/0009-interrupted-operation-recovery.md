# ADR-0009: Interrupted operations use explicit retryable recovery

**Status:** Accepted

**Date:** 2026-07-17

**Deciders:** WranglerLabs maintainers

## Context

A process, workstation, or browser can stop while a durable lifecycle operation is active. The plan and journal are secret-free, so credentials cannot be persisted for an automatic replay. Ranch Hand must distinguish work that certainly stopped before target mutation from work where apply may have started, and it must not release the exclusive deployment lock while target state is uncertain.

## Decision

The authenticated loopback API exposes a redacted inventory of active operations and an explicit recovery action. The inventory includes deployment and operation IDs, operation kind, target family, versions, phase, and last-update time; it does not expose the embedded plan or target configuration.

- `prepared` and `backup-complete` stopped before staging and apply. Ranch Hand may close them as failed without calling a target adapter.
- `staged`, `applied`, and `verified` are treated as potentially mutated. Ranch Hand first durably transitions the operation to `recovery-started`, then calls the target adapter's ownership-checked recovery.
- An operation already in `recovery-started` retries recovery directly.
- Target credentials are supplied again through the loopback request, validated, held in memory only, cleared after use, and never written to the journal.
- Successful recovery transitions to `recovered` and releases the active lock.
- Failed recovery remains in `recovery-started` with the lock intact. A later attempt can retry after credentials or target availability are corrected.
- Recovery runs in a bounded context detached from browser-request cancellation.

The Windows interface displays every active operation on launch. It enables a no-credential close only for pre-apply phases and requires the target's minimum credential material before enabling potentially destructive recovery.

## Consequences

- Interrupted work is visible and recoverable without cloning RepoWrangler or editing Ranch Hand state files.
- Ranch Hand never infers success from a failed cleanup and never permits a second mutation while target state is uncertain.
- Recovery remains constrained by each adapter's ownership evidence. Ambiguous or missing evidence fails closed and leaves the operation available for retry or operator investigation.
- The operator must re-enter ephemeral credentials after restarting Ranch Hand.

## Alternatives considered

Persisting credentials would permit unattended restart but would expand the secret-storage boundary and make portable lifecycle state unsafe. Automatically deleting the active lock would allow progress at the cost of losing target-state certainty. Both were rejected.
