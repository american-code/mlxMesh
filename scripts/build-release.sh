#!/usr/bin/env bash
#
# Reproducible, versioned, checksummed release build for the oim binaries.
#
# Produces static binaries for every supported OS/arch under dist/, stamps each
# with the version/commit/date via -ldflags, and writes a SHA256SUMS manifest.
# Reproducibility levers: -trimpath (strip local paths), -buildid= (drop the
# non-deterministic build id), CGO disabled, and a version/commit/date sourced
# from git rather than the wall clock. Two builds of the same commit with the
# same Go toolchain produce byte-identical binaries and identical checksums.
#
# Signing is intentionally NOT done here: it requires a key only the release
# operator holds. See RELEASING.md — after this script, sign SHA256SUMS with
# cosign or minisign so consumers can verify both integrity AND provenance.
#
# Usage:
#   scripts/build-release.sh [version]
#   VERSION=v0.16 scripts/build-release.sh
# If no version is given, uses the current git tag, else "dev-<shortsha>".

set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-${VERSION:-}}"
if [ -z "$VERSION" ]; then
  VERSION="$(git describe --tags --exact-match 2>/dev/null || echo "dev-$(git rev-parse --short HEAD)")"
fi
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
# SOURCE_DATE_EPOCH makes the timestamp reproducible: prefer the caller's, else
# the commit's author date, else a fixed epoch. Never `date` (wall clock).
SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git log -1 --pretty=%ct 2>/dev/null || echo 0)}"
BUILD_DATE="$(TZ=UTC date -u -r "$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
             || TZ=UTC date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)"

PKG="github.com/open-inference-mesh/oim/internal/version"
LDFLAGS="-s -w -buildid= -X ${PKG}.Version=${VERSION} -X ${PKG}.Commit=${COMMIT} -X ${PKG}.Date=${BUILD_DATE}"

# cmd path -> output basename
BINARIES=(
  "./cmd/oim:oim"
  "./cmd/coordinator:oim-coordinator"
  "./cmd/directory:oim-directory"
  "./cmd/stub-exo:stub-exo"
)
PLATFORMS=("darwin/arm64" "darwin/amd64" "linux/amd64" "linux/arm64")

OUT="dist"
rm -rf "$OUT"
mkdir -p "$OUT"

echo "Building ${VERSION} (commit ${COMMIT}, date ${BUILD_DATE})"
# Portable touch to a fixed epoch so archives are deterministic (tar embeds
# file mtimes; without this two builds produce different tarball bytes).
touch_epoch() { TZ=UTC touch -t "$(TZ=UTC date -u -r "$SOURCE_DATE_EPOCH" +%Y%m%d%H%M.%S 2>/dev/null \
  || TZ=UTC date -u -d "@$SOURCE_DATE_EPOCH" +%Y%m%d%H%M.%S)" "$1"; }

for platform in "${PLATFORMS[@]}"; do
  os="${platform%/*}"; arch="${platform#*/}"
  outdir="${OUT}/${os}_${arch}"
  mkdir -p "$outdir"
  for spec in "${BINARIES[@]}"; do
    cmd="${spec%:*}"; name="${spec#*:}"
    echo "  ${os}/${arch}  ${name}"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
      go build -trimpath -ldflags "$LDFLAGS" -o "${outdir}/${name}" "$cmd"
    touch_epoch "${outdir}/${name}"
  done
  # Reproducible archive: GNU tar (Linux CI, where releases are cut) supports
  # deterministic ordering/owner/mtime; gzip -n drops the gz timestamp. On
  # macOS bsdtar these flags are absent, so dev-machine tarballs may vary — the
  # binaries themselves are always reproducible (verify against those).
  if tar --version 2>/dev/null | grep -qi gnu; then
    ( cd "$OUT" && tar --sort=name --owner=0 --group=0 --numeric-owner \
        --mtime="@${SOURCE_DATE_EPOCH}" -cf - "${os}_${arch}" \
        | gzip -n > "oim_${VERSION}_${os}_${arch}.tar.gz" )
  else
    ( cd "$OUT" && tar cf - "${os}_${arch}" | gzip -n > "oim_${VERSION}_${os}_${arch}.tar.gz" )
  fi
done

# Authoritative checksum manifest is over the RAW binaries — those are
# byte-for-byte reproducible (-trimpath -buildid=, no wall-clock inputs), so a
# verifier can rebuild from the tagged commit and confirm an exact match. The
# tarballs are also checksummed for download-integrity, but reproducibility is
# proven at the binary level.
sha_cmd() { command -v sha256sum >/dev/null && sha256sum "$@" || shasum -a 256 "$@"; }
( cd "$OUT" && sha_cmd $(find . -type f ! -name 'SHA256SUMS' ! -name '*.tar.gz' | sort) ./*.tar.gz | sort -k2 > SHA256SUMS )

echo
echo "Artifacts in ${OUT}/:"
ls -1 "$OUT"/*.tar.gz
echo
echo "SHA256SUMS:"
cat "$OUT/SHA256SUMS"
echo
echo "Next: sign dist/SHA256SUMS (cosign/minisign) — see RELEASING.md."
