#!/usr/bin/env bash
#
# Run this ON a lab contributor machine (e.g. lab-01, lab-02) to rebuild the
# `oim` CLI from the latest source and restart the node agent from that fresh
# binary.
#
# Exists because there's no established distribution path for lab machines:
# they aren't SSH-key-reachable from the dev machine or CI, and OIMMenuBar.app
# only picks up new code via a full Xcode rebuild (embed-oim.sh -> xcodegen
# generate -> xcodebuild), which this script deliberately bypasses — it runs
# `oim node start` directly instead, trading OIMMenuBar's process supervision
# (auto-restart, Exo health checks) for something you can run from a
# terminal in under a minute. Rebuild the app properly in Xcode when you want
# that supervision back; this is the fast path to unblock routing/testing.
#
# Two ways to get the source, auto-detected in this order:
#   1. SRC_PATH — an already-reachable path to the repo (e.g. a network share
#      the dev machine exports — /Volumes/melton/open-inference-mesh — or a
#      local clone). If this script is itself run from inside such a path
#      (e.g. straight off the mounted share), it's auto-detected from $0 with
#      no env var needed at all — this is the common case for lab-01/lab-02,
#      which already have the dev machine's repo mounted over AFP/SMB.
#   2. DEV_HOST — falls back to rsync-over-ssh when there's no mount. Needs
#      Remote Login enabled (System Settings > General > Sharing) on the
#      target; no SSH key required, you'll get a password prompt. Do NOT
#      point this at the lab machine's own hostname — it must be the actual
#      dev machine (e.g. DEV_HOST=melton@Meltons-Mac.local), never
#      lab-01.local/lab-02.local from lab-01/lab-02 themselves — that
#      resolves to loopback and just fails to connect.
#
# Usage:
#   ./update-lab-node.sh                                    # run from the mounted share
#   SRC_PATH=/Volumes/melton/open-inference-mesh ./update-lab-node.sh
#   DEV_HOST=melton@Meltons-Mac.local ./update-lab-node.sh   # no mount available
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AUTO_SRC_PATH="$(cd "$SCRIPT_DIR/.." && pwd)"
COORDINATOR_URL="${COORDINATOR_URL:-https://us.mlxmesh.net}"
EXO_URL="${EXO_URL:-http://localhost:52415}"

if [ -n "${SRC_PATH:-}" ]; then
  BUILD_DIR="$SRC_PATH"
  echo "[update-lab-node] using explicit SRC_PATH=$BUILD_DIR"
elif [ -n "${DEV_HOST:-}" ]; then
  DEV_REPO_PATH="${DEV_REPO_PATH:-/Users/melton/open-inference-mesh}"
  BUILD_DIR="${SRC_DIR:-$HOME/oim-src}"
  echo "[update-lab-node] pulling source from $DEV_HOST:$DEV_REPO_PATH ..."
  mkdir -p "$BUILD_DIR"
  rsync -az --include='go.mod' --include='go.sum' --include='cmd/***' --include='internal/***' \
    --include='tools/***' --exclude='*' \
    "$DEV_HOST:$DEV_REPO_PATH/" "$BUILD_DIR/"
elif [ -f "$AUTO_SRC_PATH/go.mod" ]; then
  BUILD_DIR="$AUTO_SRC_PATH"
  echo "[update-lab-node] auto-detected source at $BUILD_DIR (running from a mounted/local checkout)"
else
  echo "[update-lab-node] no source found — set SRC_PATH=<mounted repo path> or DEV_HOST=<user@dev-machine>" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "[update-lab-node] 'go' not found on PATH. Install it first:" >&2
  echo "    which brew || /bin/bash -c \"\$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\"" >&2
  echo "    brew install go" >&2
  exit 1
fi

echo "[update-lab-node] building oim ..."
go build -C "$BUILD_DIR" -o "$HOME/oim" ./cmd/oim
"$HOME/oim" version

echo "[update-lab-node] stopping any running node agent ..."
pkill -f "oim node start" 2>/dev/null || true
sleep 1

echo "[update-lab-node] starting node against $COORDINATOR_URL ..."
echo "[update-lab-node] (reusing your usual flags — check ~/.config/oim/config.yaml if unsure)"
exec "$HOME/oim" node start --coordinator "$COORDINATOR_URL" --exo-url "$EXO_URL"
