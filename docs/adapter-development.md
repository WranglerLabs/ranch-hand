# Adapter development guide

An adapter connects the common verified-plan/lifecycle coordinator to one target.
It must preserve the target's native HTTPS, secret, ownership, backup, and recovery
contracts; it must not shell out to a workstation CLI or install a universal
proxy.

## Required seams

1. Add the target and target-specific configuration allowlist to the deployment
   plan schema and validator.
2. Add a release-manifest target and safe bundle contract in RepoWrangler. The
   bundle must be prebuilt, digest-pinned, secret-free, and independently
   verifiable without a source checkout.
3. Implement `adapter.Preflighter` for live, non-mutating connectivity and boundary
   checks, and register it in `internal/adapter.Registry`.
4. Implement `operations.Mutator`: `Backup`, `Apply`, `Verify`, and `Recover`.
   Return an explicit unsupported error for lifecycle operations that have not
   been designed; never silently weaken the coordinator sequence.
5. Register the mutator with the operation coordinator and add only the minimum
   credential fields to the loopback request model. Credentials must be cleared
   after use and must never enter a plan or result.
6. Add the target fields, boundary text, confirmation, preflight, and result states
   to the embedded UI.
7. Document the ordinary manual equivalent in RepoWrangler and the precise
   evaluation/production support boundary in the operator guide.

## Safety requirements

- Reject adoption, overwrite, or deletion unless the adapter can prove exact
  Ranch Hand ownership immediately before mutation.
- Pin SSH/TLS/provider identity and use typed provider/native-library calls.
- Keep paths, names, archive expansion, response sizes, and operation durations
  bounded.
- Require a verified target-appropriate recovery point before a data-changing
  operation.
- Verify both readiness and exact release identity after apply.
- Make recovery deterministic and idempotent; ambiguous state remains locked for
  operator intervention.
- Keep public endpoints behind trusted target-native HTTPS and authentication.

## Tests and evidence

At minimum, test successful preflight/apply/verify, least-privilege failure,
ownership mismatch, name/path injection, partial apply, failed health, interrupted
recovery, cancellation boundaries, secret redaction, and idempotent retry. A
production lifecycle also needs clean-target install/update/backup/restore/rollback
evidence and migration-compatibility tests.

Run before review:

```powershell
corepack pnpm build
go test ./...
go vet ./...
go build ./cmd/ranch-hand
```

Record material trust-boundary decisions as an ADR and update the operator guide,
security model, changelog, and support matrix in the same change.
