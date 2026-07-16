# Contributor and agent guide

Ranch Hand is a standalone deployment and lifecycle manager for RepoWrangler. Keep deployment privileges and provider credentials out of RepoWrangler itself.

## Boundaries

- Ranch Hand consumes immutable RepoWrangler releases; it does not clone product source for normal operations.
- Deployment plans are versioned and secret-free.
- Reject floating tags, unverifiable artifacts, untrusted SSH host identity, and public deployments without trusted HTTPS and authentication.
- Do not make Git, Node.js, Go, WSL, Azure CLI, Wrangler CLI, or OpenSSH an operator prerequisite.
- Do not introduce Caddy or another proxy as a universal dependency.

## Commands

```powershell
corepack pnpm install
corepack pnpm build
go test ./...
go vet ./...
go build ./cmd/ranch-hand
```

Use Conventional Commits. Never commit secrets or deployment-specific credentials.
