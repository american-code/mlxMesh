#!/usr/bin/env bash
# gen-dev-certs.sh — generate a local development CA and a server certificate for
# running the coordinator/directory over HTTPS on your LAN (so a real iPhone/iPad
# will connect to https:// without a scary cert warning, once the CA is trusted).
#
# This is for DEV / LAN testing only. For a public deploy use a real CA
# (Let's Encrypt / your cloud provider's cert manager), never these files.
#
# Usage:
#   scripts/gen-dev-certs.sh 192.168.1.135            # your Mac's LAN IP
#   scripts/gen-dev-certs.sh 192.168.1.135 mesh.local # extra SAN hostname(s)
#
# Outputs into ./certs/:
#   ca.crt         → install this on the iPad (see steps printed at the end)
#   ca.key         → keep secret; signs server certs
#   server.crt     → pass to --tls-cert
#   server.key     → pass to --tls-key
#
# Then run, e.g.:
#   oim-coordinator --listen :9000 --tls-cert certs/server.crt --tls-key certs/server.key ...
#   oim-directory   --listen :9100 --tls-cert certs/server.crt --tls-key certs/server.key ...
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <lan-ip-or-host> [extra-san ...]" >&2
  echo "example: $0 192.168.1.135" >&2
  exit 1
fi

OUT_DIR="${CERT_DIR:-certs}"
DAYS="${CERT_DAYS:-825}"   # Apple caps leaf cert lifetime at 825 days
mkdir -p "$OUT_DIR"

# Build the SAN list: always cover localhost + loopback, plus every arg. IPs are
# detected by a simple dotted-quad check and emitted as IP:, others as DNS:.
SAN="DNS:localhost,IP:127.0.0.1,IP:0:0:0:0:0:0:0:1"
for host in "$@"; do
  if [[ "$host" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    SAN="$SAN,IP:$host"
  else
    SAN="$SAN,DNS:$host"
  fi
done

echo "▶ SANs: $SAN"

# ── 1. Local CA ───────────────────────────────────────────────────────────────
if [[ -f "$OUT_DIR/ca.crt" && -f "$OUT_DIR/ca.key" ]]; then
  echo "▶ Reusing existing CA in $OUT_DIR (delete ca.crt/ca.key to regenerate)"
else
  echo "▶ Generating local development CA"
  openssl req -x509 -newkey rsa:4096 -sha256 -days 3650 -nodes \
    -keyout "$OUT_DIR/ca.key" -out "$OUT_DIR/ca.crt" \
    -subj "/CN=mlxMesh Dev CA/O=mlxMesh"
fi

# ── 2. Server key + CSR ───────────────────────────────────────────────────────
echo "▶ Generating server key + CSR"
openssl req -newkey rsa:2048 -sha256 -nodes \
  -keyout "$OUT_DIR/server.key" -out "$OUT_DIR/server.csr" \
  -subj "/CN=${1}/O=mlxMesh"

# ── 3. Sign the server cert with the CA, embedding the SANs ────────────────────
echo "▶ Signing server certificate ($DAYS days)"
openssl x509 -req -in "$OUT_DIR/server.csr" -sha256 -days "$DAYS" \
  -CA "$OUT_DIR/ca.crt" -CAkey "$OUT_DIR/ca.key" -CAcreateserial \
  -out "$OUT_DIR/server.crt" \
  -extfile <(printf "subjectAltName=%s\nextendedKeyUsage=serverAuth\n" "$SAN")

rm -f "$OUT_DIR/server.csr" "$OUT_DIR/ca.srl"
chmod 600 "$OUT_DIR"/*.key

cat <<EOF

✅ Done. Files in ./$OUT_DIR/

Run the services with TLS:
  oim-coordinator --tls-cert $OUT_DIR/server.crt --tls-key $OUT_DIR/server.key ...
  oim-directory   --tls-cert $OUT_DIR/server.crt --tls-key $OUT_DIR/server.key ...

Point Go nodes at the HTTPS coordinator and trust the CA:
  oim node start --coordinator https://${1}:9000 --tls-ca $OUT_DIR/ca.crt

Trust the CA on your iPhone/iPad (so the SwiftUI app connects cleanly):
  1. AirDrop / email $OUT_DIR/ca.crt to the device and open it.
  2. Settings → General → VPN & Device Management → install the profile.
  3. Settings → General → About → Certificate Trust Settings →
     enable full trust for "mlxMesh Dev CA".
  4. In the app's Settings, set the directory URL to  https://${1}:9100

EOF
