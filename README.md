# Ranch Hand

Ranch Hand is the standalone, Windows-first lifecycle manager for [RepoWrangler](https://github.com/WranglerLabs/repo-wrangler). It is for operators who want to install and manage RepoWrangler without cloning or forking its source repository. Contributors and advanced operators can still use RepoWrangler's documented deployment recipes directly.

> **Status:** early implementation. The secure local application shell, versioned contract validation, and immutable release bundle verification/cache are working. Deployment adapters do not apply infrastructure yet and this repository is not a production installer release.

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
