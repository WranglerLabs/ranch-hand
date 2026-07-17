# ADR-0006: Versioned Installation Records and Migration Policy

- Status: Accepted
- Date: 2026-07-17

## Context

Restore, rollback, repair, backup, update, and uninstall must act on the version Ranch Hand actually committed, not a version typed into the interface. The operation journals already provide immutable, hash-chained evidence, but scanning journals for every lifecycle decision would make current-state selection ambiguous and error-prone.

State writes can also be interrupted after a committed journal reaches disk but before the deployment lock is released. Ranch Hand needs a recoverable current-state projection without rewriting its historical evidence.

## Decision

Each stable deployment directory has one secret-free `installation.json` record. It contains the canonical validated plan snapshot, deployment and target identity, current immutable version, active/uninstalled state, original installation time, last update time, and the exact operation ID and final event hash that produced it.

- A non-install operation cannot begin without an active record whose version exactly matches `fromVersion`.
- A second install cannot begin while an active record exists.
- A committed install or lifecycle mutation advances the record before the operation lock is released.
- If the journal write succeeds but record finalization is interrupted, the lock remains. Repeating commit finalization or beginning the next operation deterministically rebuilds the record from that exact committed journal.
- Backup commits do not change installation state.
- The authenticated loopback API exposes a read-only installation inventory for the Windows interface and diagnostics.

The installation record is a derived current-state projection. Hash-chained operation journals and canonical plan snapshots remain immutable historical evidence and are never rewritten during reconciliation.

## Schema migration policy

Lifecycle files declare an exact schema version. Readers reject unknown versions, unknown fields, malformed identities, non-canonical plans, and mismatched hashes rather than guessing.

When a future schema is required:

1. The executable must retain a bounded reader for every explicitly supported prior schema.
2. Migration must validate the old file completely before transformation.
3. Journals and their event chains remain immutable; migration creates a new derived projection or index instead of changing historical evidence.
4. Mutable projections are written to a mode-`0600` temporary file, flushed, validated with the new reader, and atomically replaced.
5. Migration failure leaves the prior file and deployment lock state intact and blocks mutation with a repairable error.
6. Removing support for an old schema requires a documented breaking release and an export/upgrade path.

There is no implicit best-effort downgrade and no migration based on data received from a target or browser request.

## Consequences

- Lifecycle decisions have a single trustworthy current version and plan identity.
- Interrupted commit finalization is recoverable without accepting user-supplied state.
- Restore and rollback selection can be bound to a recorded deployment and backup inventory.
- Future schema changes require explicit compatibility code and tests.
- Deployments created before installation records exist cannot be mutated until a separately designed, ownership-verifying adoption flow is implemented.
