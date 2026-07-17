# Ranch Hand

Ranch Hand is the standalone, Windows-first lifecycle manager for [RepoWrangler](https://github.com/WranglerLabs/repo-wrangler). It is for operators who want to install and manage RepoWrangler without cloning or forking its source repository. Contributors and advanced operators can still use RepoWrangler's documented deployment recipes directly.

> **Status:** active implementation. The secure local application shell, immutable release verification/cache, secret-free plan creation/export, artifact preflight, non-mutating dry run, and live target-native connectivity preflight are working. Deployment adapters do not apply infrastructure yet and this repository is not a production installer release.

## First release scope

- Discover and verify an explicit immutable RepoWrangler release.
- Validate its version, SHA-256 digest, size, compatibility, SBOM, and attestation.
- Create a versioned, secret-free deployment plan.
- Preflight, dry run, install, backup-first update, verify, rollback, export, and produce redacted diagnostics.
- Target Azure Container Apps, Cloudflare, local Docker Compose, or remote Linux Docker Compose over SSH.

Ranch Hand is optional. It is not a RepoWrangler feature screen, does not change RepoWrangler's read-only provider model, and does not require a Ranch Hand-managed deployment.

## Security boundary

The embedded UI talks to a Go control service on a random `127.0.0.1` port. A cryptographically random per-launch bearer token is passed in the URL fragment and removed from browser history immediately. API responses are non-cacheable, browser mutations are same-origin checked, and the UI uses a restrictive content security policy.

Plans must never contain passwords, tokens, private keys, client secrets, or provider credentials. Runtime secrets will be held only as long as required and written solely to the target platform's supported secret store.

## Release verification

The local interface accepts an explicit RepoWrangler version and deployment target. Ranch Hand retrieves the official versioned manifest and target bundle over HTTPS, restricts redirects to the trusted GitHub release infrastructure, enforces response-size limits, verifies the declared byte count and SHA-256, and atomically stores the verified bundle in the user's versioned application cache. A matching cached file is hashed again before reuse; partial or mismatched downloads are removed.

Ranch Hand also downloads the release's SPDX SBOM and Sigstore provenance bundle. It verifies the Sigstore trust root through TUF, the certificate and transparency-log evidence, the exact RepoWrangler release-workflow identity, the SLSA provenance predicate, and both the deployment bundle and SBOM digests before classifying the release as verified. This verification is built into Ranch Hand and does not require a GitHub account, GitHub CLI, or Cosign installation.

## Deployment plans and dry run

After a release is verified, the local interface creates a canonical JSON deployment plan for that exact release and target. The plan records the manifest digest, deployment-artifact digest, artifact size, target kind, and a target-specific allowlist of non-secret configuration. Unknown fields and credential-like keys are rejected. Export is permitted only when the plan still matches an artifact verified during the current Ranch Hand session.

Preflight revalidates the plan and rehashes the cached artifact before reporting it ready. Dry run describes the native target operations in order and reports `mutated: false`; it does not authenticate, contact the target control plane, or change infrastructure. Live control-plane checks and apply operations belong to the deployment-adapter implementation.

## Live target preflight

The interface can run a separate live connectivity preflight after the offline checks succeed:

| Target | Native connection | Checks performed |
|---|---|---|
| Azure Container Apps | Azure Resource Manager HTTPS API | Subscription access, `Microsoft.App` registration, and Azure-managed HTTPS contract |
| Cloudflare | Cloudflare HTTPS API | Token status, selected-account access, and Cloudflare-managed HTTPS contract |
| Local Docker Compose | Docker Engine API over the Windows named pipe or Unix socket | Engine health, API version, Linux-container mode, and loopback bundle contract |
| Remote Linux Compose | Embedded Go SSH client | Pinned host identity, authentication, Linux Docker Engine, Compose v2, and operator-managed HTTPS boundary |

These checks do not shell out to Azure CLI, Wrangler CLI, a local Docker CLI, or OpenSSH. Azure and Cloudflare bearer tokens and SSH key/password material are submitted only to the loopback API, are never added to the plan or response, and are cleared from the form after each attempt. The current Azure preflight accepts a temporary ARM access token; integrated interactive Azure authentication remains part of the adapter work before RC.

## Build from source

Building is for contributors; ordinary operators will download a signed executable from a Ranch Hand release.

Requirements: Go 1.26+, Node.js 20+, and Corepack.

```powershell
corepack pnpm install
corepack pnpm build
go test ./...
go build -o dist/ranch-hand.exe ./cmd/ranch-hand
```

Run `dist/ranch-hand.exe`. It opens the embedded interface in the default browser and stops cleanly when the process exits.

## HTTPS and proxies

Ranch Hand does not install Caddy or any other universal proxy. Azure Container Apps and Cloudflare provide trusted HTTPS at their managed ingress. Kubernetes deployments use the cluster's selected ingress. Public Docker Compose deployments require an existing trusted HTTPS ingress selected and managed by the operator; private or loopback-only deployments do not.

See [ADR-0001](docs/adr/0001-standalone-lifecycle-manager.md) for the product boundary.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
