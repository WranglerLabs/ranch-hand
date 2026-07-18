# Ranch Hand

Ranch Hand is the standalone, Windows-first lifecycle manager for [RepoWrangler](https://github.com/WranglerLabs/repo-wrangler). It is for operators who want to install and manage RepoWrangler without cloning or forking its source repository. Contributors and advanced operators can still use RepoWrangler's documented deployment recipes directly.

> **Status: Public Preview.** [`v0.1.0-rc.9`](docs/releases/v0.1.0-rc.9.md)
> is the primary recommended Windows deployment path for RepoWrangler. It is
> publicly downloadable and functional, but it is unsigned and not production
> supported or generally available. See the complete [GA readiness
> gates](docs/ga-readiness.md).

## Start here

- **Recommended — use the Public Preview:** use the public
  [Ranch Hand for Windows guide](https://wranglerlabs.org/ranch-hand) to download
  stable unsigned preview asset, verify it, launch it, and complete a
  supported evaluation deployment. A GitHub account is not required.
- **Wait for the signed GA installer:** the first signed stable release remains
  gated on Authenticode signing and clean-Windows/real-target UAT.
- **Manual alternative:** clone or fork
  [RepoWrangler](https://github.com/WranglerLabs/repo-wrangler) and use its
  documented Docker, Cloudflare, Azure Container Apps, or Kubernetes recipes.

Ranch Hand is a portable executable, not an MSI. Running it opens the local
interface in the default browser; it does not install a Windows service or add a
public listener.

Additional guides: [security model](docs/security-model.md) ·
[diagnostics](docs/diagnostics.md) ·
[deployment-plan/CI export](docs/ci-export.md) ·
[adapter development](docs/adapter-development.md).

## First release scope

- Discover the latest compatible stable RepoWrangler release by default, with
  explicit prerelease and version-pinning choices, then verify the immutable
  release.
- Validate its version, SHA-256 digest, size, compatibility, SBOM, and attestation.
- Create a versioned, secret-free deployment plan.
- Preflight, dry run, install, backup-first update/restore/rollback/repair, verify, export, and produce redacted diagnostics.
- Target local Docker Compose inside WSL, local Docker Desktop, remote Linux
  Docker Compose over SSH, Cloudflare, or Azure Container Apps.

Ranch Hand is optional. It is not a RepoWrangler feature screen, does not change RepoWrangler's read-only provider model, and does not require a Ranch Hand-managed deployment.

## Security boundary

The embedded UI talks to a Go control service on a random `127.0.0.1` port. A cryptographically random per-launch bearer token is passed in the URL fragment and removed from browser history immediately. API responses are non-cacheable, browser mutations are same-origin checked, and the UI uses a restrictive content security policy.

Plans must never contain passwords, tokens, private keys, client secrets, or provider credentials. Runtime secrets will be held only as long as required and written solely to the target platform's supported secret store.

## Release verification

The local interface discovers and preselects the latest published stable RepoWrangler release that contains an artifact for the chosen target. Operators can intentionally select the latest prerelease or enter a specific immutable version under the advanced choice. Ranch Hand retrieves the official versioned manifest and target bundle over HTTPS, restricts redirects to the trusted GitHub release infrastructure, enforces response-size limits, verifies the declared byte count and SHA-256, and atomically stores the verified bundle in the user's versioned application cache. A matching cached file is hashed again before reuse; partial or mismatched downloads are removed.

Ranch Hand also downloads the release's SPDX SBOM and Sigstore provenance bundle. It verifies the Sigstore trust root through TUF, the certificate and transparency-log evidence, the exact RepoWrangler release-workflow identity, the SLSA provenance predicate, and both the deployment bundle and SBOM digests before classifying the release as verified. This verification is built into Ranch Hand and does not require a GitHub account, GitHub CLI, or Cosign installation.

## Deployment plans and dry run

After a release is verified, the local interface creates a canonical JSON deployment plan for that exact release and target. The plan records the manifest digest, deployment-artifact digest, artifact size, target kind, and a target-specific allowlist of non-secret configuration. Unknown fields and credential-like keys are rejected. Export is permitted only when the plan still matches an artifact verified during the current Ranch Hand session.

Preflight revalidates the plan and rehashes the cached artifact before reporting it ready. Dry run describes the native target operations in order and reports `mutated: false`; it does not authenticate, contact the target control plane, or change infrastructure. Live control-plane checks and apply operations belong to the deployment-adapter implementation.

## Local WSL Docker Compose evaluation install

The WSL target detects ordinary installed WSL2 distributions and runs the
verified published Compose bundle inside the selected distribution. It requires
Docker Engine and Docker Compose v2 inside WSL, but does not require Docker
Desktop, SSH, a WSL IP address, or an operator-supplied filesystem path. Ranch
Hand uses the WSL user's home directory for its ownership-marked deployment
files, defaults to the collision-resistant `repo-wrangler-ranch-hand` project,
creates its Docker-managed data volume, binds
`127.0.0.1:8080`, and verifies the exact release from Windows.

This Preview supports a new WSL evaluation install. WSL backup, update, restore,
rollback, repair, and uninstall remain open lifecycle work.

If an install is interrupted after Ranch Hand creates its dedicated directory,
the next preflight recognizes the matching durable journal and offers
**Recover interrupted WSL installation** beside the result. Recovery removes
only resources whose marker, transferred-file hashes, and Docker labels prove
exact ownership. A committed installation is reported as already installed;
an unknown directory remains blocked and untouched.

## Local Docker Desktop evaluation install

After the exact plan passes live Docker preflight and its verified bundle is safely staged, Ranch Hand can install the Docker Desktop profile as a single loopback-only evaluation container. It talks directly to Docker Desktop's Windows-exposed Docker Engine API, pulls the manifest's digest-pinned RepoWrangler image, creates or verifies an ownership-labeled persistent Docker volume, labels the container with its Ranch Hand deployment identity, starts it, and verifies `/health/ready` through a fixed loopback-only client. No host filesystem path, repository clone, Docker CLI, shell, proxy, or public ingress is involved.

The interface requires an explicit confirmation and describes the current boundary before mutation. This path enables demo mode, SQLite, and GitHub authentication; it is not a production configuration. A partially failed install can remove only the exact container carrying Ranch Hand's matching ownership labels. Ranch Hand refuses to replace or recover an unowned container with the selected name.

The same coordinator can create a consistent local backup. Ranch Hand verifies the container and volume ownership labels, briefly stops the running container, streams `/app/data` through Docker's native archive API into its user-scoped backup directory, syncs and hashes the archive, restarts the container, and waits for readiness. The secret-free inventory records the relative locator, byte count, SHA-256, deployment, operation, and release. Local archives have a 64 GiB safety limit; a stopped container remains stopped.

Local updates are backup-first and copy-on-write. Ranch Hand verifies the current release identity and backup, pulls the new digest-pinned image, seeds a new owned volume from the archive, stops and preserves the prior container and volume under a deterministic rollback identity, then activates the new container. Readiness includes both `/health/ready` and an exact immutable version match from `/health/live`. Failed activation removes only the new owned container and restores the preserved prior container; migrations never touch the rollback volume.

Explicit local restore and rollback use the authenticated installation/backup inventory rather than accepting a path. Restore is same-version; rollback selects a backup created by the exact prior release. Both first create a fresh safety backup, restore into a new owned volume, preserve the current container and untouched volume, and require exact target-version health before commit. Recovery identifies the preserved safety container before examining the replacement, so a failed same-version restore cannot be confused with the original instance. See [ADR-0007](docs/adr/0007-backup-first-local-restore-and-rollback.md).

Local repair is also backup-first and same-version. It creates a new consistent archive of the recorded current release, rebuilds that exact verified release and data in a new owned volume, and preserves the original container and volume until the replacement passes health.

The Windows interface inventories stopped local rollback environments and can explicitly retain the newest zero through ten entries. Pruning is blocked during an active lifecycle operation and re-verifies the backup identity, stopped state, deployment/version labels, and data-volume ownership immediately before deleting each container and volume without force. Backup archives and inventory records are retained. See [ADR-0010](docs/adr/0010-local-rollback-pool-retention.md). Production credential configuration for this target is not enabled yet. Manual clone/fork and custom automation remain supported RepoWrangler deployment options.

## Azure Container Apps evaluation install

The first Azure mutator installs only into a brand-new dedicated resource group. Live preflight confirms subscription access, `Microsoft.App` registration, that the resource-group name is unused, and that no custom domain was requested. Apply creates the group with exact Ranch Hand ownership/deployment/version tags and submits the verified bundle's compiled ARM template directly to Azure Resource Manager with the digest-pinned public image.

This profile enables demo mode and SQLite on Azure Files. It does not configure production provider secrets, PostgreSQL, an existing/shared resource group, a custom domain, backup, or update. Azure resources are billable. The interface requires a fresh in-memory ARM token and explicit cost/dedicated-boundary confirmation before apply.

Ranch Hand polls the ARM deployment, verifies the resulting Container App uses the expected immutable image, requires an Azure-managed `*.azurecontainerapps.io` HTTPS endpoint, and validates both readiness and the exact release identity. Failed-install recovery deletes the resource group only when its tags prove it belongs to the exact Ranch Hand deployment; an unowned or differently owned group is never deleted. See [ADR-0003](docs/adr/0003-dedicated-azure-evaluation-boundary.md).

## Cloudflare evaluation install

The first Cloudflare mutator creates only a brand-new Worker and D1 database with unused names. Live preflight verifies the scoped API token, selected account, workers.dev subdomain, and dedicated resource names. Apply talks directly to Cloudflare's HTTPS APIs: it creates D1, writes a Ranch Hand ownership marker, applies the ordered SQL migrations from the verified bundle, performs the documented static-assets upload-session exchange, uploads the immutable Worker module with the release-declared bindings and routing settings, configures the release-declared cron triggers, and enables workers.dev.

The API token is held only in memory and must permit account reads, Workers Scripts writes, and D1 writes. Ranch Hand requests a fresh token for apply after live preflight and clears it after the operation. The evaluation profile publishes `DEMO_MODE=true`, no production secrets, no custom domain, and no external proxy. Verification re-reads the D1 marker and Worker bindings, requires the exact `APP_VERSION`, and checks `/health/ready` and `/health/live` through `https://<worker>.<account-subdomain>.workers.dev`.

Failed-install recovery deletes the Worker only when its D1 binding and version match the marker-owned database, then deletes that exact database. Missing, ambiguous, or mismatched ownership evidence stops recovery without deleting anything. Existing Workers and databases, custom domains, production authentication secrets, backup, update, and rollback are not enabled in this adapter. See [ADR-0004](docs/adr/0004-dedicated-cloudflare-evaluation-boundary.md).

## Remote Linux Compose evaluation install

The first remote Linux mutator uses Ranch Hand's native SSH client; Windows does not need OpenSSH, WSL, Docker, Compose, or a Linux shell. The target Linux account must already be able to run Docker and Docker Compose v2. Live preflight pins the exact SSH host-key fingerprint, verifies the Linux Docker Engine and Compose, requires an unused project, and requires that the dedicated installation directory not exist while its parent is writable.

Apply transfers the verified digest-pinned Compose file plus a generated evaluation-only override and `.env`. The override gives the server container and data volume stable names and exact Ranch Hand ownership/deployment/version labels. A secret-free marker records the release, artifact, immutable image, resource names, and SHA-256 of every transferred deployment file. RepoWrangler binds only to `127.0.0.1:8080` on the Linux host. Ranch Hand verifies the marker, file hashes, labels, image, running state, readiness, and release identity through an SSH-forwarded loopback connection.

Failed-install recovery repeats every ownership and hash check, runs `docker compose down --volumes` only from the verified files, and removes only the four known files plus the empty dedicated directory. A missing or changed marker, file, container label, or volume label stops cleanup. The adapter does not expose a public port, install a proxy, adopt existing resources, write production secrets, back up, update, restore, or roll back. Operators can still deploy the published Compose bundle themselves and supply their own trusted ingress. See [ADR-0005](docs/adr/0005-dedicated-remote-linux-evaluation-boundary.md).

## Live target preflight

The interface can run a separate live connectivity preflight after the offline checks succeed:

| Target | Native connection | Checks performed |
|---|---|---|
| Azure Container Apps | Azure Resource Manager HTTPS API | Subscription access, `Microsoft.App` registration, and Azure-managed HTTPS contract |
| Cloudflare | Cloudflare HTTPS API | Token and account access, workers.dev, and unused dedicated Worker/D1 names |
| Local Docker Compose — WSL | `wsl.exe` into the selected distribution | Installed distribution, running Linux Docker Engine, Compose v2, unused project/directory, and Windows-loopback contract |
| Local Docker Desktop | Docker Engine API over the Windows named pipe | Engine health, API version, Linux-container mode, and loopback bundle contract |
| Remote Linux Compose | Embedded Go SSH client | Pinned host identity, Linux Docker/Compose, unused project, and dedicated directory |

These checks do not shell out to Azure CLI, Wrangler CLI, a local Docker CLI, or OpenSSH. Azure and Cloudflare bearer tokens and SSH key/password material are submitted only to the loopback API, are never added to the plan or response, and are cleared from the form after each attempt. The current Azure preflight accepts a temporary ARM access token; integrated interactive Azure authentication remains part of the adapter work before GA.

## Verified bundle staging

Before an adapter can consume a release, Ranch Hand rehashes the cached archive and extracts it into a digest-addressed application cache. Extraction rejects absolute paths, traversal, links, special files, duplicate portable paths, oversized files, excessive entries, and excessive expanded size. Ranch Hand then validates `bundle.json` against the selected version and target, including the digest-pinned image and target-native HTTPS contract.

The staging record contains the size and SHA-256 of every extracted file. Ranch Hand rehashes all staged files before reuse and rebuilds a staging directory from the verified archive if any file is missing, added, or changed. A staged directory is local execution material only; its path is never written into the portable deployment plan.

## Lifecycle transaction policy

Lifecycle mutations use a durable, secret-free journal keyed to the stable target environment. The journal permits one active operation per deployment, embeds the canonical plan snapshot, replaces every phase atomically, and detects corrupted phase history. An update cannot commit until backup, staging, apply, and health verification have all completed. If activation or verification fails, recovery is an explicit journaled path rather than an undocumented retry.

On startup, the Windows interface lists interrupted operations from the authenticated loopback API. A journal that stopped before staging can be safely closed without touching the target. Once apply may have started, Ranch Hand requires fresh target credentials, moves the journal into `recovery-started`, and reruns the adapter's ownership-checked recovery. A failed recovery keeps the exclusive lock in that phase so the operator can correct credentials or target availability and retry; it is never mislabeled as complete. See [ADR-0009](docs/adr/0009-interrupted-operation-recovery.md).

Every committed install and version-changing operation also advances a validated `installation.json` current-state record before releasing the deployment lock. Later operations must match its exact recorded version; an arbitrary `fromVersion` cannot begin a mutation. If finalization is interrupted after the committed journal is durable, Ranch Hand rebuilds the projection from that exact journal. The authenticated loopback `GET /api/v1/installations` endpoint exposes this secret-free inventory to the Windows interface. Historical journals remain immutable. See [ADR-0006](docs/adr/0006-versioned-installation-records.md) for the record and schema migration policy.

The Windows interface can export a versioned redacted diagnostics JSON snapshot. It includes lifecycle phases, immutable versions, timestamps, target families, an export-scoped deployment pseudonym, random operation/backup IDs, and safe integrity hashes. It explicitly excludes stable deployment IDs, plans and their deterministic digests, configuration values, backup locators, URLs, hostnames, domains, account/resource identifiers, credentials, environment variables, request bodies, and arbitrary logs. Collection fails closed rather than silently skipping corrupt lifecycle state. See [ADR-0008](docs/adr/0008-redacted-diagnostics-boundary.md).

The coordinator implements install, backup, and backup-first update/restore/rollback/repair sequencing. It binds `backup-complete` to an exact validated safety-backup record, binds historical restore input to a separate inventory record, stages only the verified plan artifact, and automatically enters recovery if apply, health verification, or the post-apply journal write fails. Recovery receives a cancellation-independent bounded context so a closed browser request cannot abandon a partially mutated target. All five initial targets are wired for the bounded evaluation installs above; local Docker Desktop also supports consistent backup and copy-on-write update, restore, rollback, and repair. Uninstall and the remaining target lifecycle mutations remain disabled. See [ADR-0002](docs/adr/0002-durable-lifecycle-transactions.md) for phase rules, recovery semantics, and trade-offs.

## Build from source

Building is for contributors. The current release-candidate workflow produces a
versioned unsigned Windows executable, SHA-256 file, SPDX SBOM, and GitHub
build-provenance attestation as a short-lived workflow artifact. It cannot
publish automatically. After verification, a maintainer can promote the exact
bytes to an immutable unsigned prerelease; a later signing workflow will produce
the first trusted GA installer.

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
