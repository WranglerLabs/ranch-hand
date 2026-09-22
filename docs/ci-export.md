# Deployment plans and external CI/CD

Ranch Hand can export the canonical, secret-free JSON plan created after it has
verified a RepoWrangler release artifact in the current session. The plan is an
auditable statement of deployment intent; it is not a credential bundle.

## Contract

The schema is [`contracts/deployment-plan.schema.json`](../contracts/deployment-plan.schema.json).
It binds:

- schema version `1.0`;
- an explicit RepoWrangler semantic version;
- the official manifest URL and exact manifest SHA-256;
- the exact target artifact SHA-256 and byte size;
- one allow-listed target; and
- target-specific non-secret configuration.

Unknown properties and credential-like values are rejected. Treat an exported
plan as configuration: review it, version it if desired, and never add secrets to
it.

## Current automation boundary

The RC does not publish a reusable GitHub Actions or Azure DevOps pipeline and
does not support unattended remote control of the loopback API. The random launch
token protects an interactive local session and must not be copied into CI.

A user-owned pipeline may consume the same plan contract only if it independently:

1. validates the plan schema and supported contract version;
2. downloads the exact versioned RepoWrangler manifest from the official release;
3. verifies manifest and artifact size/SHA-256 plus the published provenance and
   SBOM identities;
4. obtains runtime credentials from the CI system's approved secret store;
5. applies only the selected target's allow-listed operations;
6. records backup/recovery evidence before data-changing work; and
7. verifies readiness and exact immutable release identity before success.

The manual-equivalent recipes live in
[`WranglerLabs/repo-wrangler/deploy`](https://github.com/WranglerLabs/repo-wrangler/tree/main/deploy).
Future opt-in CI templates must consume this same contract rather than introduce a
second plan format.
