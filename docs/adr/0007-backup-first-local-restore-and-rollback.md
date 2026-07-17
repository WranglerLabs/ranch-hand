# ADR-0007: Backup-First Local Restore and Rollback

- Status: Accepted
- Date: 2026-07-17

## Context

Ranch Hand already creates verified local archives and performs copy-on-write updates, but an archive inventory is not itself a safe restore contract. Restore and rollback must not accept arbitrary paths, must not overwrite the only current data volume, and must remain recoverable when the application version is unchanged.

The selected archive and the backup needed to recover the state being replaced are different roles. Treating them as one record would make a failed restore capable of destroying the operator's newest recoverable state.

## Decision

Local Docker restore and rollback are explicit coordinator operations with two independently validated backups:

- `selected` is an existing Ranch Hand backup record chosen from the deployment's inventory.
- `safety` is a fresh consistent backup created after the operation acquires the deployment lock and before any replacement mutation.

The selected backup ID is part of the journal header and therefore part of the first event hash. The fresh safety backup is bound by the later `backup-complete` event. A resumed operation can prove both roles without relying on browser state.

Restore requires the selected backup, current installation record, and verified target artifact to have the same immutable version. Rollback requires the selected backup and verified target artifact to have the same prior version, different from the currently recorded version. Both operations reject a backup from another deployment or target.

Apply pulls the exact digest-pinned target image, creates a new ownership-labeled volume keyed to the fresh safety-backup identity, restores the selected archive into that new volume, preserves the current stopped container and its untouched volume under a deterministic safety identity, and activates the replacement. Readiness and `/health/live` must report the exact target version before commit.

Recovery first looks for the preserved safety container. This makes same-version restore unambiguous: an active same-version container can still be the failed replacement. Ranch Hand removes a replacement only when its deployment labels, version, and data-volume identity all match the operation, removes that exact failed volume, renames the preserved container, restarts it if necessary, and verifies the original version. Missing or conflicting ownership evidence stops cleanup.

The Windows interface reads installation and backup inventories from the authenticated loopback API. It never accepts a backup path. It displays only backups whose recorded version matches the immutable release plan already verified in the current session.

## Consequences

- Restore and rollback are backup-first and copy-on-write.
- A failed same-version restore cannot be mistaken for the original container based only on version labels.
- The original container and volume remain available after a successful operation until explicit rollback-pool retention is implemented.
- Each operation creates additional archive and Docker volume storage; the interface must make this clear and later expose selective retention controls.
- Cross-version database compatibility is never inferred. Rollback is limited to a backup produced while the exact target release was installed.
