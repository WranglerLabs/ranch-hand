# ADR-0004: Dedicated Cloudflare evaluation boundary

- Status: Accepted
- Date: 2026-07-17

## Context

Ranch Hand must deploy RepoWrangler's Cloudflare profile without cloning source or requiring Node.js, Wrangler, or a shell on the operator workstation. The profile combines a Worker module, static assets, D1 migrations, bindings, variables, schedules, and Cloudflare-managed HTTPS. A failed install must not delete a Worker or database that Ranch Hand does not own.

Cloudflare Workers and D1 databases do not provide the same general resource tags used by the Azure adapter. Name matching alone is not durable ownership evidence, especially after an interrupted process.

## Decision

The initial Cloudflare mutator is evaluation-only and requires unused Worker and D1 names. It consumes deployment metadata exclusively from the verified immutable bundle identity and uses Cloudflare's documented HTTPS APIs directly.

The operation performs these steps:

1. Recheck that the requested Worker and D1 names are unused.
2. Create the dedicated D1 database.
3. Create `_ranch_hand_installation` in D1 and record the stable deployment ID and exact release version.
4. Apply every ordered SQL migration from the verified release bundle.
5. Register the static-asset manifest, upload the hash buckets Cloudflare requests, and retain the returned completion token.
6. Upload the Worker module and signed configuration as multipart form data, binding the owned D1 database, assets, evaluation variables, compatibility date, routing rules, and observability setting.
7. Configure the release-declared cron triggers and enable the Worker on workers.dev.
8. Re-read the marker and Worker settings, then verify readiness and exact release identity through Cloudflare-managed HTTPS.

The D1 marker is product-isolated operational metadata. Ranch Hand never stores the API token in the deployment plan, journal, D1 marker, response, or diagnostic output.

## Recovery rule

Recovery first finds the exact named D1 database and requires one unambiguous result. It reads the marker and requires both the stable Ranch Hand deployment ID and immutable version to match the failed operation.

If a Worker exists, recovery also requires its `DB` binding to reference that exact D1 UUID and its `APP_VERSION` binding to match. Only then may Ranch Hand delete the Worker and database. Missing, ambiguous, or mismatched evidence stops recovery without deletion. If failure occurs before the ownership marker is durable, Ranch Hand deliberately leaves the database for operator review.

## Boundaries

This adapter enables only a new demo-mode installation on workers.dev. It does not adopt or replace existing resources, bind a custom domain, write production authentication secrets, back up D1, update, restore, roll back, repair, or uninstall. Those operations require their own backup and compatibility contracts.

Cloudflare terminates trusted HTTPS. Ranch Hand does not install Caddy or any other reverse proxy.

## Consequences

- Normal installation requires no repository clone, Git, Node.js, Wrangler, or shell.
- The immutable release bundle is the sole source of Worker deployment configuration.
- Recovery has durable target-side ownership proof instead of relying on a process-local flag.
- A database created but not yet marked is retained rather than risk deleting an unowned resource.
- Production Cloudflare lifecycle work remains explicit future scope.
