# Unsigned release-candidate policy

Ranch Hand `v0.1.0-rc.1` is an evaluation artifact, not a generally available release.

## Build and distribution boundary

The `Unsigned release candidate` workflow runs only by explicit manual dispatch with a version matching `v0.1.0-rc.N`. It builds on GitHub-hosted Windows, runs the repository test suite, embeds the exact candidate version, and produces:

- `ranch-hand-v0.1.0-rc.N-windows-amd64.exe`
- a matching lowercase SHA-256 file
- an SPDX JSON SBOM generated from the executable
- GitHub build-provenance attestations for the executable and SBOM

The workflow has read-only repository-content permission, uploads a workflow artifact retained for 30 days, and has no tag trigger or GitHub Release upload step. It cannot be mistaken for the future trusted distribution workflow.

## Operator expectations

The executable is not Authenticode-signed. Windows SmartScreen, reputation checks, or enterprise application-control policy can warn, quarantine, or block it. An evaluator should verify the SHA-256 and GitHub provenance attestation before running it and should use only disposable or explicitly authorized evaluation targets.

No paid support, response time, compatibility guarantee, or upgrade guarantee applies to an unsigned candidate. Diagnostics remain redacted, and security reports should use the repository's private vulnerability-reporting channel.

## Promotion gate

A generally available Ranch Hand release requires a separately reviewed workflow that applies an approved Windows Authenticode signature, verifies that signature on the final bytes, preserves the SBOM and provenance evidence, passes clean-Windows and target UAT, and publishes immutable GitHub Release assets. Creating or running that workflow requires explicit maintainer authorization after signing identity setup is complete.
