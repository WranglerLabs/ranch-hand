# ADR-0003: Dedicated resource-group boundary for the first Azure mutator

**Status:** Accepted

**Date:** 2026-07-17

**Deciders:** WranglerLabs maintainers

## Context

Ranch Hand must recover safely when a first Azure Container Apps deployment fails after creating some, but not all, resources. The verified RepoWrangler ARM template creates a managed identity, storage, Log Analytics workspace, Container Apps environment, environment storage, and Container App. Deleting a deployment record does not delete those resources, while deleting resources by inferred names inside a shared group risks affecting infrastructure Ranch Hand does not own.

The first Azure adapter is an evaluation profile: demo mode, SQLite on Azure Files, the public digest-pinned release image, and the platform-provided Container Apps HTTPS hostname. Existing resource groups, production secrets/PostgreSQL, and custom-domain certificates require broader ownership and recovery contracts.

## Decision

The first Azure mutation requires a new, dedicated resource group.

- Preflight proves the selected group name does not exist.
- Apply repeats that check immediately before mutation, creates the group through ARM, and attaches Ranch Hand managed/deployment/version tags.
- The compiled `main.json` from the verified immutable bundle is submitted directly to the resource-group deployment API in incremental mode.
- Verification checks ARM success, the exact digest-pinned container image, the Azure-managed HTTPS suffix, readiness, and exact release identity.
- Failed-install recovery may delete the resource group only after reading and matching the exact ownership and deployment tags.
- Recovery refuses existing, untagged, or differently tagged groups.

## Consequences

- Recovery has one unambiguous ownership boundary and cannot delete unrelated resources.
- Operators cannot initially target an existing/shared group or bind a custom domain through Ranch Hand.
- The evaluation deployment creates billable Azure resources and therefore requires explicit UI confirmation.
- Production and shared-group support require resource-level ownership tags, secret/identity preflight, backup policy, certificate binding, and selective rollback before those modes can be enabled.

## Alternatives considered

### Deploy into any selected resource group

This matches common manual ARM workflows but cannot guarantee safe cleanup after partial deployment without a complete resource ownership inventory.

### Delete resources by template-derived name

Names are configurable and can predate Ranch Hand. Name matching is not proof of ownership and is insufficient for destructive recovery.

### Dedicated resource group with exact tags (selected)

This is intentionally narrower but provides a deterministic, auditable recovery boundary suitable for the first mutation-capable Azure adapter.
