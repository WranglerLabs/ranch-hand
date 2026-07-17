# ADR-0008: Redacted Diagnostics Boundary

- Status: Accepted
- Date: 2026-07-17

## Context

An open-source lifecycle manager needs support evidence that an operator can inspect and share without accidentally publishing deployment credentials or private infrastructure identifiers. Ranch Hand's plans are secret-free, but they still contain account IDs, subscription IDs, resource names, hostnames, custom domains, and installation paths that are unnecessary for first-line diagnosis.

Backup locators can similarly reveal local filenames or target resource identifiers. Raw logs, environment variables, HTTP bodies, SSH material, and platform tokens must never be included by convenience.

## Decision

Ranch Hand exports one versioned JSON diagnostics snapshot through its authenticated loopback API and Windows interface. The collector uses an explicit allowlist and emits only:

- Ranch Hand version, operating system, and architecture.
- An export-scoped deployment pseudonym plus random operation/backup identifiers.
- Target family, lifecycle state and phase, immutable versions, timestamps, and operation kinds.
- Event-chain SHA-256 evidence, backup kind, archive size, and archive SHA-256.
- The selected input-backup ID for an active restore or rollback.

The collector never serializes a deployment plan or its deterministic digest, stable deployment ID, configuration key or value, backup locator, release/cache/staging path, URL, hostname, domain, account/subscription/resource identifier, credential, environment variable, request/response body, SSH key, token, password, or arbitrary log text. A fresh cryptographic salt makes the deployment pseudonym unlinkable across separate exports.

Collection fails closed if lifecycle inventory cannot be read consistently. It does not silently omit a corrupt deployment and produce a misleading partial report. The response is launch-token protected and downloaded as `ranch-hand-diagnostics.json`; the operator remains in control of sharing it.

## Consequences

- First-line integrity and lifecycle diagnosis is possible without exposing target topology.
- New diagnostic fields require an explicit review and redaction test before they can be added.
- Deep platform troubleshooting may still require an operator to gather target-native evidence separately.
- The export is deterministic in shape but includes a generation timestamp and current operation state.
