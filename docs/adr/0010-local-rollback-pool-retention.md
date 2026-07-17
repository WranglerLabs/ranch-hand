# ADR-0010: Local rollback-pool pruning is explicit and ownership-checked

**Status:** Accepted

**Date:** 2026-07-17

**Deciders:** WranglerLabs maintainers

## Context

Successful local update, restore, rollback, and repair operations preserve the replaced stopped container and its untouched data volume. This gives automatic recovery a deterministic boundary and gives the operator a short-term rollback environment, but repeated operations consume Docker storage. Name matching alone is not sufficient authority to delete either resource.

Verified backup archives are independent from these stopped Docker resources. Pruning a rollback environment must not silently delete its archive or durable backup record.

## Decision

Ranch Hand exposes authenticated local rollback-pool inventory and an explicit keep-newest pruning action.

- Inventory starts from the deployment's validated backup records. Each backup maps to the deterministic rollback-container name used by the replacement operation.
- A listed entry must be stopped, carry the exact Ranch Hand managed/deployment/version labels, mount one `/app/data` Docker volume, and reference a volume carrying the matching managed/deployment labels.
- Any ambiguous, running, missing-volume, unowned, or version-mismatched resource makes inventory fail closed.
- The operator chooses to keep the newest zero through ten entries and checks an explicit permanent-removal confirmation. There is no automatic background deletion.
- Pruning is rejected while a durable lifecycle operation is active.
- Ranch Hand re-reads and revalidates each candidate immediately before mutation. It removes the stopped container without force or implicit volume deletion, then removes only the exact revalidated volume.
- Pruning retains all backup archives, backup records, installation records, and lifecycle journals.
- A request processes oldest entries sequentially. If Docker fails after an earlier entry was removed, Ranch Hand returns the successfully pruned entries and stops; it does not continue across uncertain state.

The inventory is bounded to 1,000 backup records per deployment. This prevents a corrupt or adversarial local state directory from causing unbounded Docker API work.

## Consequences

- Operators can bound the large Docker resources created by copy-on-write lifecycle operations without losing verified archive restore points.
- Retaining zero physical rollback environments is allowed only through explicit confirmation; it does not remove archives.
- Docker container and volume deletion are not transactional. If the container deletion succeeds and the exact volume deletion then fails, the response identifies the volume and stops. Ranch Hand never broadens deletion or forces cleanup to hide that failure.
- This policy applies only to Ranch Hand-managed local Docker evaluation deployments. Other targets require target-native retention contracts.
