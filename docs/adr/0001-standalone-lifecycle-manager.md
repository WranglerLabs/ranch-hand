# ADR-0001: Ranch Hand is a separate lifecycle manager

- Status: Accepted
- Date: 2026-07-16

## Context

RepoWrangler supports several deployment methods, but requiring every operator to clone its source repository and install development tooling creates unnecessary friction. Deployment execution also has different privileges and lifecycle concerns from the read-only RepoWrangler product.

## Decision

Ranch Hand is a separate, Apache-2.0-licensed application and repository. It consumes immutable, versioned RepoWrangler artifacts and never requires the product source repository for normal installation or lifecycle operations.

The first client is a signed, portable Windows executable containing a React UI and a Go control service. The control API binds to a random IPv4 loopback port and is protected by a random per-launch token. Deployment plans are versioned JSON documents and must never contain secrets.

Initial target adapters are Azure Container Apps, Cloudflare, local Docker Compose, and remote Linux Docker Compose over SSH. Adapter implementation must use provider APIs or native Go libraries; local Git, Node.js, Go, WSL, Azure CLI, Wrangler CLI, and OpenSSH are not operator prerequisites.

Ranch Hand does not introduce a universal reverse proxy. Azure Container Apps and Cloudflare use platform-managed HTTPS, Kubernetes uses the operator's ingress, and a publicly exposed Compose deployment must use an operator-provided trusted HTTPS ingress. Caddy is neither required nor special-cased.

## Consequences

- Users may still clone or fork RepoWrangler and deploy it with their own tools.
- Ranch Hand releases independently from RepoWrangler.
- RepoWrangler must publish machine-readable manifests, checksums, SBOMs, and attestations.
- Secrets are requested at apply time and sent only to a target's supported secret mechanism.
- Release discovery rejects floating tags and unverified artifacts.
