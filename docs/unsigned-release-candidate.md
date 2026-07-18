# Unsigned release-candidate policy

Ranch Hand `v0.1.0-rc.1` was the first **Public Preview**; `v0.1.0-rc.8` is the
current Preview. These are evaluation artifacts, not generally available or
production-supported releases.

## Build and distribution boundary

The `Unsigned release candidate` workflow runs only by explicit manual dispatch with a version matching `v0.1.0-rc.N`. It builds on GitHub-hosted Windows, runs the repository test suite, embeds the exact candidate version, and produces:

- `ranch-hand-v0.1.0-rc.N-windows-amd64.exe`
- a matching lowercase SHA-256 file
- an SPDX JSON SBOM generated from the executable
- GitHub build-provenance attestations for the executable and SBOM

The workflow has read-only repository-content permission, uploads a workflow
artifact retained for 30 days, and has no tag trigger or GitHub Release upload
step. It cannot publish anything automatically or be mistaken for the future
trusted distribution workflow.

After the recorded build, hash, provenance, launch, and independent verification
checks pass, a maintainer may explicitly promote those exact bytes as immutable
GitHub **prerelease** assets. The public Wrangler Labs documentation provides the
end-user download route and preserves the unsigned evaluation warning. Manual
promotion never changes the candidate into a signed or generally available
release.

## Operator expectations

The executable is not Authenticode-signed. Windows SmartScreen, reputation checks, or enterprise application-control policy can warn, quarantine, or block it. An evaluator should verify the SHA-256 and GitHub provenance attestation before running it and should use only disposable or explicitly authorized evaluation targets.

No paid support, response time, compatibility guarantee, or upgrade guarantee applies to the unsigned Public Preview. Diagnostics remain redacted, and security reports should use the repository's private vulnerability-reporting channel.

## GA promotion gate

A generally available Ranch Hand release requires the complete
[GA readiness contract](ga-readiness.md), including a separately reviewed
workflow that applies an approved Windows Authenticode signature, verifies that
signature on the final bytes, preserves the SBOM and provenance evidence, passes
clean-Windows and real-target UAT, and publishes immutable release assets.
