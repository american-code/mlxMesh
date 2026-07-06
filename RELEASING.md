# Releasing mlxMesh

This document is the release process for the `oim` binaries and the `mlxmesh`
container image. It is intentionally boring and repeatable — a release should be
a checklist, not a judgment call.

## What a release produces

- **Reproducible binaries** for `darwin/{arm64,amd64}` and `linux/{amd64,arm64}`,
  one tarball per platform, under `dist/`.
- **`dist/SHA256SUMS`** — a checksum manifest over the raw binaries (the
  reproducibility anchor) and the tarballs (download integrity).
- **A detached signature** over `SHA256SUMS` (see Signing) so consumers verify
  both integrity *and* provenance.
- **A version-stamped container image** (`mlxmesh:<version>`), whose binaries
  report their build via `oim version` and the coordinator/directory startup log.

## Reproducibility

`make release` (→ `scripts/build-release.sh`) builds with `-trimpath` and
`-buildid=`, `CGO_ENABLED=0`, and a version/commit/date sourced from git rather
than the wall clock. Two builds of the same tagged commit with the same Go
toolchain produce **byte-for-byte identical binaries** — anyone can rebuild from
the tag and confirm the published checksums. (Verified: the 16 per-platform
binary checksums are stable across runs. Fully reproducible tarballs require GNU
tar, i.e. the Linux release runner — macOS `bsdtar` archives may vary, but the
binaries they contain do not.)

> Note: the prebuilt `darwin` binaries are `CGO_ENABLED=0`, so the opt-in Secure
> Enclave attestation path (which needs CGO) is stubbed in them. Mac operators
> who want hardware attestation build from source with `make build` (CGO on by
> default on darwin). This is documented, not accidental — attestation is opt-in.

## Cutting a release

1. **Pre-flight** — on a clean tree at the release commit:
   ```
   go build ./... && go vet ./... && golangci-lint run ./...
   go test ./... && go test -tags integration ./tests/ -run Integration
   ```
   All green, or stop.
2. **Tag** the commit: `git tag v0.16 && git push --tags` (annotated tags preferred).
3. **Build artifacts** (ideally on the Linux CI runner for reproducible tarballs):
   ```
   make release          # → dist/*.tar.gz + dist/SHA256SUMS
   ```
4. **Sign the manifest** (see below).
5. **Build + push the image**:
   ```
   make image            # mlxmesh:v0.16 + mlxmesh:latest, version-stamped
   ```
6. **Publish** `dist/*.tar.gz`, `dist/SHA256SUMS`, and the signature to the
   release page; push the image to the registry.
7. **Deploy** per [RUNBOOK.md](RUNBOOK.md) (build-on-box or pull-image path).

## Signing (operator-supplied key)

Signing is deliberately **not** automated in the build script — it needs a
private key only the release operator holds. Pick one:

- **cosign** (recommended, keyless or key-based):
  ```
  cosign sign-blob --yes dist/SHA256SUMS --output-signature dist/SHA256SUMS.sig
  # verify:
  cosign verify-blob --signature dist/SHA256SUMS.sig dist/SHA256SUMS
  ```
  For the image: `cosign sign mlxmesh:v0.16` and `cosign verify mlxmesh:v0.16`.
- **minisign** (simple, offline):
  ```
  minisign -Sm dist/SHA256SUMS        # → dist/SHA256SUMS.minisig
  minisign -Vm dist/SHA256SUMS -P <pubkey>
  ```

Publish the **public key** in the repo (e.g. `SECURITY.md`) so consumers can
verify without trusting the download page. **TODO (operator):** generate and
commit the signing public key; wire `cosign sign-blob`/`cosign sign` into the
release CI job once the key/OIDC identity is provisioned.

## Verifying a downloaded release

```
sha256sum -c SHA256SUMS               # integrity
cosign verify-blob --signature SHA256SUMS.sig SHA256SUMS   # provenance
oim version                           # confirm the stamped version/commit
```
