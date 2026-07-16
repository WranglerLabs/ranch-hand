## Summary

Describe what changed and why.

## Security and lifecycle impact

- [ ] Deployment plans remain secret-free.
- [ ] Release inputs remain immutable and verifiable.
- [ ] Logs and diagnostics contain no credentials.
- [ ] Rollback or recovery behavior is documented where applicable.

## Validation

- [ ] `pnpm typecheck`
- [ ] `pnpm build`
- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] Windows executable builds successfully
