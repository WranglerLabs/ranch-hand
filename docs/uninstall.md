# Remove a Ranch Hand evaluation deployment

Ranch Hand Public Preview does not yet provide an automated **Uninstall** action.
Use this runbook only when you intentionally need to remove an evaluation
deployment before that lifecycle is implemented. These steps act on the target;
they do not erase Ranch Hand's local evidence.

## Before removing anything

1. Close or finish any active Ranch Hand operation. Never remove resources while
   a lifecycle phase is running or awaiting recovery.
2. Export Ranch Hand's redacted diagnostics and retain the original secret-free
   deployment plan.
3. Decide whether to **retain data** or **permanently delete data**. Permanent
   deletion cannot be undone.
4. Match every target resource to the names in the plan and to the labels or
   marker created by Ranch Hand. Stop if ownership is missing or ambiguous.

The current RC keeps the installation record after manual target removal. That
record is intentional evidence, but Ranch Hand will continue to show the target
as installed and will not reinstall over it. Do not delete or edit files below
`%LOCALAPPDATA%\WranglerLabs\Ranch Hand`; catalog reconciliation and automated
uninstall remain follow-on lifecycle work.

## Local Docker Desktop

The managed container is `<projectName>-server`. The persistent volume is the
plan's `dataVolume` value. Ranch Hand labels both with
`wranglerlabs.ranch-hand.managed=true` and the deployment identity.

1. In Docker Desktop, inspect the container and volume and confirm those labels.
2. Stop and remove `<projectName>-server`.
3. To retain data, keep `dataVolume` and all Ranch Hand backup archives.
4. To permanently delete data, remove `dataVolume` only after confirming its
   Ranch Hand labels and making any required external backup.
5. Review stopped rollback-pool containers and volumes separately. Do not assume
   they are disposable merely because the active container was removed.

Equivalent Docker CLI commands, after independently confirming the labels:

```powershell
docker inspect <projectName>-server
docker volume inspect <dataVolume>
docker rm --force <projectName>-server
# Permanent data deletion only:
docker volume rm <dataVolume>
```

## Local WSL Docker Compose

The default WSL directory is
`$HOME/.<projectName>-ranch-hand`. Run the following inside the exact WSL
distribution recorded in the plan:

```sh
cd "$HOME/.<projectName>-ranch-hand"
cat .ranch-hand-installation.json
POSTGRES_PASSWORD=ranch-hand-postgres-profile-disabled docker compose \
  --project-name <projectName> \
  --env-file .env \
  --file compose.yaml \
  --file ranch-hand.override.yaml \
  down --remove-orphans
```

To retain data, stop there and keep the `<projectName>-data` Docker volume and
the installation directory. For permanent deletion, rerun `down` with
`--volumes`, confirm the owned volume is gone, and then remove the dedicated
installation directory. Never remove an unexpected file or symlink as part of
that directory cleanup.

## Remote Linux Docker Compose

SSH to the exact private server and account recorded in the plan, then use the
plan's `installDirectory` and `projectName`:

```sh
cd <installDirectory>
cat .ranch-hand-installation.json
POSTGRES_PASSWORD=ranch-hand-postgres-profile-disabled docker compose \
  --project-name <projectName> \
  --env-file .env \
  --file compose.yaml \
  --file ranch-hand.override.yaml \
  down --remove-orphans
```

To retain data, keep `<projectName>-data` and the installation directory. For
permanent deletion, use `down --volumes --remove-orphans`, verify that the
container and volume carried the marker's deployment identity, and remove only
the dedicated installation directory. Ranch Hand does not change the server's
Docker installation, Linux packages, firewall, SSH configuration, or unrelated
images during removal.

## Azure Container Apps

The evaluation adapter creates a dedicated resource group whose name is stored
in the plan.

1. Open that resource group in the Azure portal and confirm its Container App,
   Container Apps environment, storage resources, and Ranch Hand deployment
   identity match the plan.
2. To retain data, export or copy the Azure Files content to storage outside the
   dedicated resource group and verify the copy before continuing.
3. Delete the dedicated resource group from Azure Resource Manager.
4. Confirm the deletion completed and review subscription activity/cost views
   for any separately created or retained resources.

Never delete a shared or pre-existing resource group. The evaluation adapter is
designed for a new dedicated group; a target that does not match that boundary
must be investigated instead of removed with this procedure.

## Cloudflare Worker and D1

The plan records the dedicated Worker name and D1 database name.

1. In the Cloudflare dashboard, confirm both names and inspect the D1 ownership
   marker before deletion.
2. To retain data, export the D1 database through an approved Cloudflare API or
   Wrangler workflow and verify the exported file outside Cloudflare.
3. Delete the Worker first so it cannot continue writing to D1.
4. For permanent deletion, delete the exact D1 database after confirming the
   retained export or explicitly accepting data loss.
5. Remove only routes, schedules, and custom bindings owned by that Worker.
   Leave unrelated account resources unchanged.

Revoking or deleting the scoped installation token is separate from deleting
the Worker and D1 database. Ranch Hand never persisted that token.

## Remove Ranch Hand itself

Ranch Hand is a portable executable, not an installed Windows service. Close the
application and delete the downloaded executable when it is no longer needed.
Keep `%LOCALAPPDATA%\WranglerLabs\Ranch Hand` while it contains lifecycle
evidence or backups you may need. Deleting that entire directory permanently
removes the catalog, verified artifact cache, journals, and local backup
archives for every managed target on that Windows account.
