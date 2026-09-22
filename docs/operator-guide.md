# Ranch Hand Windows operator guide

Ranch Hand Public Preview is the primary, clone-free Windows path to deploy and manage verified
RepoWrangler releases from Windows. You do not need the RepoWrangler source
repository, Git, Node.js, Go, Azure CLI, Wrangler CLI, Docker CLI, or a local
OpenSSH executable for Ranch Hand's normal workflow. WSL is required only when
you select the local WSL Docker Compose target.

If you prefer to own the source and commands, Ranch Hand is not required. Clone
or fork [RepoWrangler](https://github.com/WranglerLabs/repo-wrangler) and follow
its [manual deployment recipes](https://github.com/WranglerLabs/repo-wrangler/tree/main/deploy).

## Current availability

There is no signed GA Ranch Hand installer yet. `v0.1.0-rc.30` is an unsigned
Public Preview published as a stable prerelease download. It is intended for
evaluation and feedback, not production support.

Use it only on an explicitly authorized target. Windows SmartScreen or
organizational application-control policy may warn or block it. Do not bypass an
organizational security policy.

## Download and verify the Public Preview

1. Open the public [Ranch Hand for Windows guide](https://wranglerlabs.org/ranch-hand)
   and select **Download Ranch Hand v0.1.0-rc.30 for Windows (64-bit)**. A GitHub
   account is not required.
2. In PowerShell, verify the executable before running it:

   ```powershell
   Get-FileHash .\ranch-hand-v0.1.0-rc.30-windows-amd64.exe -Algorithm SHA256
   Get-AuthenticodeSignature .\ranch-hand-v0.1.0-rc.30-windows-amd64.exe
   ```

   Compare the result with the `.sha256` file published beside the executable
   and the checksum displayed on the public download page.

   Expected Authenticode status: `NotSigned`.

3. For optional GitHub provenance verification, install GitHub CLI and run:

   ```powershell
   gh attestation verify .\ranch-hand-v0.1.0-rc.30-windows-amd64.exe `
     --repo WranglerLabs/ranch-hand
   ```

GitHub provenance proves which workflow produced the file. It does not replace a
Windows code-signing certificate.

## Launch Ranch Hand

Double-click `ranch-hand-v0.1.0-rc.30-windows-amd64.exe`, or start it from
PowerShell. Ranch Hand binds a random port on `127.0.0.1`, opens the interface in
your default browser, and protects that browser session with a random one-time
launch token.

Keep the executable running while using the interface. Closing it stops the local
control service; it does not stop a RepoWrangler deployment. Relaunch the same
executable to reopen the local catalog and recover any interrupted operation.
Refreshing the same browser tab is supported; the launch token remains only in
that tab's session storage after Ranch Hand removes it from the address bar.

## Choose a target

All five Preview adapters create **new production-data deployments by default**.
Demo mode is an explicit opt-in. They do not adopt an existing environment.

| Target | What you need | Current boundary |
|---|---|---|
| Local Docker Compose — WSL | An installed WSL2 Ubuntu/Debian distribution | Ranch Hand configures the Windows user-scoped WSL instance and VM idle timeouts for persistent service hosting, preserving other `.wslconfig` settings and restarting WSL once. If Engine or Compose is missing, Ranch Hand installs it inside WSL, starts Docker, grants the WSL user access, and reruns preflight. **Demo mode** is an explicit toggle and defaults off. Off generates protected local secrets and opens real first-run provider setup at `http://127.0.0.1:8080/onboarding`; on uses mock data. Ranch Hand verifies and loads the selected immutable release's public image archive with pulls disabled. Docker Desktop, SSH, an open WSL terminal, a WSL IP, filesystem path, GitHub account, token, and registry login are not required. |
| Local Docker Desktop | Windows Package Manager, or an already installed Docker Desktop running Linux containers | If unavailable, Ranch Hand offers to install Docker Desktop through `winget`; administrator approval, first-run terms, and startup remain visible when required. Ranch Hand verifies and loads the selected immutable image archive through the Windows Docker API. Production data mode defaults on, generates and preserves protected secrets, uses persistent SQLite, and opens loopback onboarding. Demo mode is opt-in. Full backup, update, restore, rollback, repair, recovery, and rollback-pool retention are available. |
| Azure Container Apps | An Azure subscription, permission to create a dedicated resource group, Container Apps resources, and Azure Database for PostgreSQL, plus a temporary ARM access token | Production data mode requires RepoWrangler v1.0.18 or newer and provisions a dedicated PostgreSQL flexible server, generated secure Container App secrets, a one-time setup token, and Azure-managed HTTPS. Ranch Hand verifies the digest-pinned public image and exact reported mode. Demo mode explicitly selects SQLite on Azure Files and mock data. Resources are billable; existing groups, custom domains, backup, and update are not enabled. |
| Cloudflare | Account ID, unused Worker and D1 names, a workers.dev subdomain, and a scoped API token with account read, Workers Scripts write, and D1 write access | Production data mode defaults on and creates a dedicated Worker and D1 database, generated secret bindings, a one-time setup token, and Cloudflare-managed HTTPS. Ranch Hand verifies the exact Worker variables, secret-binding names, persistent D1 identity, release, and reported mode. Demo mode is opt-in. Existing resources, custom domains, backup, and update are not enabled. |
| Remote Linux Docker Compose | Existing Ubuntu/Debian host at an explicit private IPv4 address and an SSH password or private key; sudo is needed only when Docker prerequisites are missing | If Engine or Compose is missing, Ranch Hand offers a bounded sudo-backed install, starts Docker, grants the user access, and reruns preflight. Ranch Hand verifies the public image archive on Windows, streams it to Docker over the pinned SSH connection, verifies the loaded image ID, and disables registry pulls. The target needs no GitHub account, GHCR login, token, or registry access. SSH port and project are prefilled; entering the user fills its default installation directory. The successful credential is reused for installation only in memory. The project publishes port 8080 on the selected private address, and Ranch Hand verifies `http://<private-ip>:8080` from Windows before reporting success. Real mode also generates a one-time setup token, writes it only to the protected remote environment, and shows it in the committed result. Ranch Hand does not install public ingress, a proxy, or Linux. Backup and update are not enabled. |

Azure, Cloudflare, and Remote Linux credentials are entered once for live
preflight. After a successful check, Ranch Hand retains the credential only in
the running loopback session through installation. Failed credentials remain in
the form for correction. Docker Desktop requires no deployment credential.

The Azure RC currently accepts an operator-supplied ARM token; integrated Azure
interactive sign-in is still open work. Obtain and handle that token using your
organization's approved Azure process. The token exists only in memory and is
cleared after the operation.

## Guided deployment workflow

Use this sequence in the Ranch Hand interface:

1. Under **Verify a RepoWrangler bundle**, leave **Latest stable
   (recommended)** selected, choose the deployment target, and use **Fetch
   available releases** whenever you want to reload the live catalog. Ranch
   Hand lists every compatible stable release for that target; **Latest
   prerelease** lists the preview channel, and **Specific version (advanced)**
   accepts an exact immutable tag. Choose
   **Verify and cache release** and continue only when provenance, SBOM, size,
   and SHA-256 all report verified.
2. Under **Describe the target environment**, enter the requested non-secret
   target values and choose **Create bound plan**. Credentials do not enter the
   exported plan.
3. Choose **Preflight and dry run**. Review every check and proposed action. Dry
   run reports `mutated: false` and makes no infrastructure change.
4. Enter the requested credential only for **Run live target preflight**. Ranch
   Hand verifies connectivity, ownership boundaries, target prerequisites, and
   unused names. It clears credential fields after each attempt.
5. Read the target-specific boundary, select its confirmation checkbox, provide
   a fresh credential if requested, and choose the displayed install action.
6. Treat the deployment as successful only after Ranch Hand reports a committed
   operation, ready health, and the exact immutable RepoWrangler release identity.

## Complete real-mode first run

Real mode opens RepoWrangler at `/onboarding`, not at Command Center. Complete
the stages in this order:

1. For Remote Linux, enter the initial setup token shown by Ranch Hand. WSL is
   loopback-only and does not require this extra LAN handoff.
2. Choose the administrator identity provider. GitHub requires at least one
   administrator username; Microsoft Entra requires tenant, client, secret, and
   at least one administrator email. The first listed identity becomes owner.
3. Connect GitHub or GitLab estate access and choose monitored resources.
4. Finish and complete the first successful administrator sign-in. Setup access
   closes permanently only after that verified sign-in, so incorrect identity
   settings remain recoverable through **Change administrator identity**.

Local/private GitHub Apps are created without a webhook because GitHub cannot
deliver to loopback or private addresses; scheduled and manual synchronization
remain available. Microsoft Entra is offered on loopback HTTP or trusted HTTPS,
but not on private-LAN HTTP, because Entra does not accept that redirect URI.

You can use **Export JSON plan** after verification to retain the secret-free
deployment intent for review or external automation.

## Target inputs

Ranch Hand asks only for these non-secret values in the plan:

- **Local Docker:** Compose project, persistent volume, and loopback listen
  address. Keep the address on `127.0.0.1` for the evaluation profile.
- **Local WSL Compose:** installed distribution, Compose project, and explicit
  demo-mode toggle. Demo mode defaults off; the choice contains no secret.
- **Azure Container Apps:** subscription ID, new resource-group name, Azure
  region, Container Apps environment name, and Container App name.
- **Cloudflare:** account ID, new Worker name, and new D1 database name. Custom
  domains are not enabled in the RC evaluation adapter.
- **Remote Linux:** host, SSH port, user, unused installation directory, unused
  Compose project, and pinned SSH host-key fingerprint.

Secrets and credentials are submitted separately for live preflight/apply and are
never stored in the plan, catalog, journal, diagnostics, or exported JSON.

## Manual equivalents

Every guided target has a source-controlled path that works without Ranch Hand:

| Ranch Hand target | Manual source recipe |
|---|---|
| Local or remote Docker Compose | [`deploy/docker`](https://github.com/WranglerLabs/repo-wrangler/tree/main/deploy/docker) and the selected immutable Compose release bundle |
| Azure Container Apps | [`deploy/azure-container-apps`](https://github.com/WranglerLabs/repo-wrangler/tree/main/deploy/azure-container-apps) and the selected compiled ACA bundle |
| Cloudflare Worker + D1 | [`deploy/cloudflare`](https://github.com/WranglerLabs/repo-wrangler/tree/main/deploy/cloudflare) and the selected built Cloudflare bundle |
| Kubernetes or another topology not in Ranch Hand | The remaining [`deploy`](https://github.com/WranglerLabs/repo-wrangler/tree/main/deploy) recipes and user-owned CI/CD |

The manual path may build from a checked-out immutable tag or consume published
artifacts directly. Ranch Hand does not become a prerequisite for support,
upgrades, custom automation, or contribution.

## Local Docker lifecycle operations

After a local installation is recorded, Ranch Hand can:

- create a consistent backup;
- perform a backup-first update to another verified RepoWrangler release;
- restore a same-version backup;
- roll back using a backup from the exact prior release;
- repair the current release after creating a new safety backup;
- recover an operation interrupted after target mutation may have started; and
- prune older stopped rollback containers and volumes while retaining verified
  backup archives and records.

For update or rollback, first verify and create a plan for the desired target
release. Ranch Hand will not accept an arbitrary version or filesystem backup
path. Every destructive cleanup rechecks Ranch Hand ownership labels and stops on
missing or ambiguous evidence.

Managed permanent uninstall is available for every active deployment from
**Managed deployments**. Cloud targets and remote Linux request fresh in-memory
credentials. Ranch Hand permanently removes data only after target-specific
ownership checks and then closes the installation inventory record. Retain-data
removal uses the target-specific [manual removal runbook](uninstall.md).
Never delete Ranch Hand's local catalog merely to bypass an active installation
record.

## Local state and diagnostics

Secret-free state is stored below:

```text
%LOCALAPPDATA%\WranglerLabs\Ranch Hand
```

It includes verified release artifacts, staged bundles, deployment plans,
installation and backup inventory, lifecycle journals, and local backup archives.
Do not edit these records while an operation is active.

Choose **Export redacted diagnostics** to create a support snapshot. Review the
file before sharing it. Ranch Hand excludes credentials, plan values, URLs,
hostnames, account/resource identifiers, filesystem locators, and arbitrary log
text by design.

## HTTPS and public access

Ranch Hand never installs Caddy or another universal proxy.

- Azure Container Apps and Cloudflare evaluation deployments use their native
  managed HTTPS endpoints.
- Local Docker remains loopback-only.
- Remote Linux private-LAN evaluation publishes port 8080 only when the plan
  contains an explicit private IPv4 address. Public exposure remains outside the
  evaluation adapter and requires an operator-selected trusted HTTPS ingress and
  authentication design.

## Getting help

See [Support](../SUPPORT.md). Include the Ranch Hand version, RepoWrangler
version, target type, redacted diagnostics, and reproducible steps. Never post a
token, password, private key, tenant/account identifier, or unredacted plan.
