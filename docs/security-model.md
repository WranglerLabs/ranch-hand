# Security model

Ranch Hand is a privileged local deployment tool. Its security boundary is
separate from RepoWrangler, which remains read-only toward repository providers.

## Local control service

- The API binds only to a random IPv4 loopback port.
- Each process creates a cryptographically random one-time launch token.
- The token is delivered in the browser URL fragment, removed from browser
  history, and required for every API request.
- Mutations require same-origin requests. Responses are non-cacheable and the UI
  uses a restrictive content security policy.
- Ranch Hand does not install a service or accept remote connections.

Do not expose or forward the loopback port. Close the executable when the local
management session is finished.

## Release trust

Ranch Hand accepts an explicit semantic version and the official versioned
RepoWrangler release URL. It rejects floating tags, untrusted redirects, size or
SHA-256 mismatches, incompatible contracts, invalid SPDX metadata, and provenance
that does not identify the allowed RepoWrangler release workflow and subject.
Cached and staged files are rehashed before reuse.

The current Ranch Hand Public Preview itself is unsigned. Its GitHub provenance and SHA-256
can identify the build, but they do not provide Authenticode publisher identity or
SmartScreen reputation. See the [operator guide](operator-guide.md).

## Credentials and plans

Deployment plans are versioned and secret-free. Tokens, passwords, SSH private
keys, passphrases, client secrets, connection strings, and recovery material are
rejected from plans and omitted from catalogs, journals, diagnostics, and exports.

Credentials are accepted only for live preflight, apply, or recovery, bounded by
size limits, passed to the selected native adapter, and cleared after the request.
Use the least privilege documented for the target and a fresh short-lived token
where the provider supports it.

## Mutation and recovery

- Target adapters operate only inside their declared new/evaluation boundary.
- Cleanup and recovery require exact deployment IDs, immutable versions, file
  hashes, provider markers, and Ranch Hand ownership labels.
- Missing, changed, or ambiguous evidence stops deletion.
- Durable journals permit only one active operation per deployment and preserve
  interrupted state until ownership-checked recovery succeeds.
- Backup-first local operations write to a new owned volume and preserve the
  prior container/volume until the replacement passes exact-version health.

## Network and HTTPS

Ranch Hand does not install a universal proxy. Azure Container Apps and
Cloudflare use provider-managed HTTPS. Local Docker and remote Linux evaluation
profiles remain loopback-only. Making a Compose deployment public requires an
operator-owned trusted HTTPS and authentication design.

## Reporting

Use GitHub private vulnerability reporting for suspected security issues. Never
attach credentials, private keys, provider identifiers, or an unredacted plan.
See [SECURITY.md](../SECURITY.md).
