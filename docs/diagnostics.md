# Redacted diagnostics

Choose **Export redacted diagnostics** in the Ranch Hand header to download
`ranch-hand-diagnostics.json`. The export is intended for troubleshooting without
copying plans, credentials, provider responses, or arbitrary logs.

## Included

- Ranch Hand version and diagnostics schema version;
- lifecycle operation kind, phase, immutable from/to versions, and timestamps;
- target family and random operation/backup identifiers;
- an export-scoped deployment pseudonym; and
- safe record-integrity hashes and counts.

## Excluded

- passwords, tokens, private keys, passphrases, environment variables, and request
  or response bodies;
- deployment plans, configuration keys/values, and deterministic plan/deployment
  identifiers;
- URLs, hostnames, domains, subscription/account/resource identifiers, and SSH
  target details;
- release-cache, staging, backup, and filesystem paths; and
- arbitrary application or provider log text.

A new cryptographic salt is used for every export, so deployment pseudonyms cannot
be correlated across two diagnostic files. Collection fails closed if the local
lifecycle inventory is inconsistent; Ranch Hand does not silently publish a
misleading partial snapshot.

## Before sharing

Review the JSON yourself, include only the minimum reproduction steps, and use the
support or private security channel appropriate to the issue. Do not supplement
the file with screenshots containing credentials or provider/account identifiers.
