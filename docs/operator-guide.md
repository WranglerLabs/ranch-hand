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

There is no signed GA Ranch Hand installer yet. `v0.1.0-rc.7` is an unsigned
Public Preview published as a stable prerelease download. It is intended for
evaluation and feedback, not production support.

Use it only on an explicitly authorized evaluation target. Windows SmartScreen or
organizational application-control policy may warn or block it. Do not bypass an
organizational security policy.

## Download and verify the Public Preview

1. Open the public [Ranch Hand for Windows guide](https://wranglerlabs.org/ranch-hand)
   and select **Download Ranch Hand v0.1.0-rc.7 for Windows (64-bit)**. A GitHub
   account is not required.
2. In PowerShell, verify the executable before running it:

   ```powershell
   Get-FileHash .\ranch-hand-v0.1.0-rc.7-windows-amd64.exe -Algorithm SHA256
   Get-AuthenticodeSignature .\ranch-hand-v0.1.0-rc.7-windows-amd64.exe
   ```

   Compare the result with the `.sha256` file published beside the executable
   and the checksum displayed on the public download page.

   Expected Authenticode status: `NotSigned`.

3. For optional GitHub provenance verification, install GitHub CLI and run:

   ```powershell
   gh attestation verify .\ranch-hand-v0.1.0-rc.7-windows-amd64.exe `
     --repo WranglerLabs/ranch-hand
   ```

GitHub provenance proves which workflow produced the file. It does not replace a
Windows code-signing certificate.

## Launch Ranch Hand

Double-click `ranch-hand-v0.1.0-rc.7-windows-amd64.exe`, or start it from
PowerShell. Ranch Hand binds a random port on `127.0.0.1`, opens the interface in
your default browser, and protects that browser session with a random one-time
launch token.

Keep the executable running while using the interface. Closing it stops the local
control service; it does not stop a RepoWrangler deployment. Relaunch the same
executable to reopen the local catalog and recover any interrupted operation.

## Choose a target

All five Preview adapters create **new evaluation deployments**. They do not adopt an
existing environment.

| Target | What you need | Current boundary |
|---|---|---|
| Local Docker Compose — WSL | WSL2 distribution with a running Linux Docker Engine and Docker Compose v2 | Ranch Hand detects the distribution, defaults to the collision-resistant `repo-wrangler-ranch-hand` project, and fixes the Docker volume and Windows listen address automatically. Docker Desktop, SSH, a WSL IP, and a filesystem path are not required. New evaluation install is available; lifecycle follow-ups remain open. |
| Local Docker Desktop | Docker Desktop running Linux containers | Windows Docker API, loopback-only demo/SQLite deployment. Full backup, update, restore, rollback, repair, recovery, and rollback-pool retention are available. |
| Azure Container Apps | An Azure subscription, permission to create a dedicated resource group and ACA resources, and a temporary ARM access token | New resource group, demo mode, SQLite on Azure Files, and Azure-managed HTTPS. Resources can incur Azure charges. Existing groups, PostgreSQL, production credentials, custom domains, and update are not enabled. |
| Cloudflare | Account ID, unused Worker and D1 names, a workers.dev subdomain, and a scoped API token with account read, Workers Scripts write, and D1 write access | New Worker and D1 database in demo mode with Cloudflare-managed workers.dev HTTPS. Existing resources, custom domains, production secrets, backup, and update are not enabled. |
| Remote Linux Docker Compose | Existing Linux host, Docker Engine, Docker Compose v2, an account allowed to use them, SSH credentials, and the pinned `SHA256:` host-key fingerprint | SSH port and project are prefilled; entering the SSH user fills that user's default installation directory. New dedicated project remains bound to remote loopback. Ranch Hand does not install public ingress, a proxy, Docker, or Linux. Backup and update are not enabled. |

The Azure RC currently accepts an operator-supplied ARM token; integrated Azure
interactive sign-in is still open work. Obtain and handle that token using your
organization's approved Azure process. The token exists only in memory and is
cleared after the operation.

## Guided deployment workflow

Use this sequence in the Ranch Hand interface:

1. Under **Verify a RepoWrangler bundle**, leave **Latest stable
   (recommended)** selected, choose the deployment target, and let Ranch Hand
   populate the newest compatible RepoWrangler version. Use **Latest
   prerelease** or **Specific version (advanced)** only intentionally. Choose
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
   a fresh credential if requested, and choose the displayed **Install …
   evaluation** action.
6. Treat the deployment as successful only after Ranch Hand reports a committed
   operation, ready health, and the exact immutable RepoWrangler release identity.

You can use **Export JSON plan** after verification to retain the secret-free
deployment intent for review or external automation.

## Target inputs

Ranch Hand asks only for these non-secret values in the plan:

- **Local Docker:** Compose project, persistent volume, and loopback listen
  address. Keep the address on `127.0.0.1` for the evaluation profile.
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
| Local or remote Docker Compose | [`deploy/docker`](https://github.com/WranglerLabs/repo-wrangler/tree/main/deploy/docker) and the v1.0.10 Compose release bundle |
| Azure Container Apps | [`deploy/azure-container-apps`](https://github.com/WranglerLabs/repo-wrangler/tree/main/deploy/azure-container-apps) and the v1.0.10 compiled ACA bundle |
| Cloudflare Worker + D1 | [`deploy/cloudflare`](https://github.com/WranglerLabs/repo-wrangler/tree/main/deploy/cloudflare) and the v1.0.10 built Cloudflare bundle |
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

Uninstall is not implemented. To avoid losing data, do not manually delete a
Ranch Hand-managed container, volume, backup directory, or catalog record.

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
- Remote Linux Compose remains loopback-only on the Linux host. Publishing it is
  outside the evaluation adapter and requires an operator-selected trusted HTTPS
  ingress and authentication design.

## Getting help

See [Support](../SUPPORT.md). Include the Ranch Hand version, RepoWrangler
version, target type, redacted diagnostics, and reproducible steps. Never post a
token, password, private key, tenant/account identifier, or unredacted plan.
