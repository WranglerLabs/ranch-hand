# Repo intent — ranch-hand

**The standalone, Windows-first lifecycle manager for RepoWrangler.**

## What this repo is

Ranch Hand lets operators install and manage RepoWrangler without cloning or
forking its source repository. Contributors and advanced operators can still use
RepoWrangler's documented deployment recipes directly instead.

**Status: Public Preview.** `v0.1.0-rc.30` is the primary recommended Windows
deployment path for RepoWrangler — publicly downloadable and functional, but
unsigned and not production-supported or GA. See `docs/ga-readiness.md` for the
gates blocking a signed GA release.

## Shape

- `cmd/`, `internal/` — the Go CLI/service
- `web/` — a React frontend (per `package.json`/`pnpm-workspace.yaml` alongside the
  Go module)
- `contracts/` — interfaces shared with the product it manages
- `docs/releases/` — versioned release notes, including the GA readiness gates

## How it relates to other repos

- **`repo-wrangler`** — the product this tool installs and manages; Ranch Hand
  consumes its releases, it doesn't build or fork its source

## Status

Public Preview, pre-GA. The signed stable installer is gated on Authenticode
signing and clean-Windows/real-target UAT.
