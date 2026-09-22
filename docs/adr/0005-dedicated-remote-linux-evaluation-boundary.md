# ADR-0005: Dedicated remote Linux Compose evaluation boundary

- Status: Accepted
- Date: 2026-07-17

## Context

Ranch Hand must install RepoWrangler on a Linux server without requiring Git, OpenSSH, WSL, Docker, Compose, Node.js, Go, or a shell on the Windows control workstation. Docker Compose is a client-side orchestrator, so the target host must already provide a Linux Docker Engine and Docker Compose v2.

A remote operation crosses a privileged SSH boundary. Ranch Hand must pin host identity, prevent command injection from portable plan fields, avoid replacing an existing project, and prove ownership before failed-install cleanup.

## Decision

The initial remote Linux mutator is a loopback-only evaluation install. Deployment plans permit only validated host, port, Linux user, normalized absolute installation path, Docker project name, and SHA-256 SSH host-key fingerprint. Runtime private keys, passphrases, and passwords remain in memory and are never written to the target or plan.

Ranch Hand connects with its embedded Go SSH client and performs these steps:

1. Verify the pinned SSH host key and authenticate with the in-memory key or password.
2. Verify a Linux Docker Engine, Docker Compose v2, an unused Compose project, and an absent dedicated directory whose parent is writable.
3. Create the directory with owner-only permissions.
4. Transfer the verified digest-pinned `compose.yaml`, a generated evaluation override, a secret-free `.env`, and a target-side ownership marker.
5. Use Docker Compose v2 on the target to activate only the server service.
6. Re-read the marker, rehash every transferred file, inspect the exact container and volume labels, verify the immutable image and running state, and check readiness and version through an SSH-forwarded connection to remote loopback.

The generated configuration binds `127.0.0.1:8080`, enables demo mode and SQLite, creates no proxy, and exposes no public ingress. The container and data volume carry exact managed, deployment, and release labels.

## Recovery rule

Recovery requires the exact marker and matching stable deployment ID, release, artifact, project, resource names, image, and file hashes. If a same-named container or volume exists, its ownership labels must match before cleanup proceeds.

Only after those checks may Ranch Hand run `docker compose down --volumes --remove-orphans` using the rehashed files. It then removes only `compose.yaml`, `ranch-hand.override.yaml`, `.env`, the ownership marker, and the resulting empty directory. It never recursively deletes a remote path.

Missing or changed evidence stops recovery without mutation. Files transferred before the marker is committed are deliberately retained for operator inspection.

## Consequences

- Windows needs no external deployment tooling.
- The Linux target still needs SSH, Docker Engine, and Docker Compose v2.
- The first remote install is safely reachable only from the host or an operator-created SSH tunnel.
- Public ingress, trusted HTTPS, production secrets, backup, update, restore, rollback, repair, and uninstall remain separate future contracts.
- Manual clone/fork and direct Compose deployment remain fully supported outside Ranch Hand.
