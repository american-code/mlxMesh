#!/usr/bin/env bash
#
# Builds a version-stamped darwin/arm64 `oim` binary into
# OIMMenuBar/Resources/oim, so `oim version` and the coordinator startup logs
# always match exactly what's running.
#
# MUST run BEFORE `xcodegen generate`: xcodegen auto-includes any non-code
# file under a declared `sources:` path (here, OIMMenuBar/Resources) as a
# Copy-Bundle-Resources reference by scanning the filesystem AT GENERATION
# TIME — so the binary has to already exist on disk for the generated Xcode
# project to know to embed it. Build order is always:
#
#     scripts/embed-oim.sh  →  xcodegen generate  →  xcodebuild
#
# Only darwin/arm64: Exo/MLX are Apple-Silicon-only, so an Intel slice would
# be dead weight (see MENUBAR_RELEASING.md for how to widen this later — a
# GOARCH change, not an architecture change).
#
# This is a narrow, fast `go build` for just the one binary this app embeds —
# not the full scripts/build-release.sh, which builds 4 platforms x 5
# binaries and would be wasteful here. Release packaging still uses the same
# internal/version ldflags convention so there is exactly one versioning
# scheme project-wide.

set -euo pipefail
cd "$(dirname "$0")/../.."  # repo root

VERSION="${VERSION:-$(git describe --tags --exact-match 2>/dev/null || echo dev-$(git rev-parse --short HEAD 2>/dev/null || echo none))}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"

VPKG="github.com/open-inference-mesh/oim/internal/version"
LDFLAGS="-s -w -buildid= -X ${VPKG}.Version=${VERSION} -X ${VPKG}.Commit=${COMMIT}"

OUT="OIMMenuBar/Resources/oim"
mkdir -p OIMMenuBar/Resources

echo "[embed-oim] building ${VERSION} (${COMMIT}) -> ${OUT}"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$LDFLAGS" -o "$OUT" ./cmd/oim
chmod +x "$OUT"
"$OUT" version
