#!/usr/bin/env bash
#
# Pull the mlxMesh stateful Docker volumes from the EC2 seed to THIS machine,
# date-stamped, then delete the server-side temp copies. No server space kept,
# no AWS cost (no AMI/EBS snapshot, no S3). Runs from cron/launchd on the Mac —
# the Mac initiates and pulls, so the server needs no credentials back to it.
#
# What it backs up (the only stateful volumes):
#   mlxmesh-coordinator-data  → ledger.db + both coordinator identities +
#                               federation-us/eu.db  (the money + signing keys)
#   mlxmesh-directory-data    → directory_pod_pins.json (TOFU pins)
# Simulated node identities are ephemeral (no volume) — nothing to back up there.
#
# SQLite note: this tars the whole volume (db + any -wal/-shm together), which is
# consistent enough for recovery given the low write rate. For a stricter
# snapshot under heavy write load, switch to `sqlite3 .backup` — overkill here.
#
# Config via env (defaults target the current seed):
set -euo pipefail

SEED_HOST="${SEED_HOST:-ec2-user@54.197.150.242}"
SSH_KEY="${SSH_KEY:-$HOME/Downloads/mlxMesh_seed_v1.pem}"
BACKUP_ROOT="${BACKUP_ROOT:-$HOME/meshAI_backups_folder}"
VOLUMES="${VOLUMES:-mlxmesh-coordinator-data mlxmesh-directory-data}"
RETENTION_DAYS="${RETENTION_DAYS:-30}"

DATE="$(date -u +%Y-%m-%d)"
DEST="$BACKUP_ROOT/$DATE"
REMOTE_TMP="/tmp/mlxmesh-backup-$DATE"
SSH_OPTS=(-i "$SSH_KEY" -o ConnectTimeout=15 -o BatchMode=yes)

mkdir -p "$DEST"

echo "[backup] $(date -u +%FT%TZ) tarring volumes on $SEED_HOST ..."
ssh "${SSH_OPTS[@]}" "$SEED_HOST" "
  set -e
  rm -rf $REMOTE_TMP && mkdir -p $REMOTE_TMP
  for v in $VOLUMES; do
    sudo docker run --rm -v \$v:/data:ro -v $REMOTE_TMP:/backup busybox \
      tar czf /backup/\$v.tar.gz -C /data .
  done
  sudo chown -R \$(id -u):\$(id -g) $REMOTE_TMP
  echo '[backup] server-side tarballs:' && ls -la $REMOTE_TMP
"

echo "[backup] pulling to $DEST ..."
rsync -az -e "ssh ${SSH_OPTS[*]}" "$SEED_HOST:$REMOTE_TMP/" "$DEST/"

echo "[backup] cleaning server temp ..."
ssh "${SSH_OPTS[@]}" "$SEED_HOST" "rm -rf $REMOTE_TMP"

# Retention: drop local dated folders older than RETENTION_DAYS so the Mac
# doesn't accumulate forever.
find "$BACKUP_ROOT" -maxdepth 1 -type d -name '20*' -mtime "+$RETENTION_DAYS" -exec rm -rf {} + 2>/dev/null || true

echo "[backup] done: $DEST"
ls -la "$DEST"
