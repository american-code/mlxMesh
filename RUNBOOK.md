# mlxMesh Operations Runbook

Operational procedures for the live seed. This is the "what do I actually type
when X happens" document. It is grounded in the real deployment, not an
idealized one. Pair it with [SLOS.md](SLOS.md) (targets + alerts) and
[RELEASING.md](RELEASING.md) (cutting a build).

## The deployment at a glance

- **Host:** one EC2 instance, ~1.9 GB RAM + 2 GB swap. **Memory-constrained** —
  this is the single most important operational fact (see Incidents → OOM).
- **Address:** Elastic IP `54.197.150.242`, domain `mlxmesh.net` (GoDaddy DNS,
  manual — no API access). The Elastic IP means a stop/start no longer changes
  the public IP; before it was attached, a restart broke all DNS.
- **TLS:** terminated at host `nginx`. Public names → containers:
  `us.mlxmesh.net`→coordinator-us:9000, `eu.mlxmesh.net`→coordinator-eu:9001,
  `directory.mlxmesh.net`→directory:9100, `app.mlxmesh.net`→dashboard,
  `mlxmesh.net`→landing.
- **Containers:** all run from one image tag `mlxmesh`, on the docker network
  `mlxmesh`. 119 total = 58 `mlxmesh-node-N` + 58 `mlxmesh-stub-exo-N` (each
  simulated node is a node+stub pair) + coordinator-us + coordinator-eu +
  directory.
- **Source + build:** `~/mlxmesh-src` (rsync'd from a dev checkout), built on
  the box with `docker build -t mlxmesh .`.
- **Secrets:** `/etc/mlxmesh/api-key`, `/etc/mlxmesh/federation-key` (root-owned,
  0600), bind-mounted to `/run/secrets/*`. Per-node TLS certs in
  `~/mlxmesh-node-certs/`.
- **Helper scripts on the box:** `redeploy-infra.sh` (recreate directory +
  coordinators), `refresh-nodes.py` (recreate all nodes from the current image),
  `spawn-nodes.sh` (create N new node pairs), `enable-node-tls.py`.

Access: `ssh -i <key>.pem ec2-user@54.197.150.242`.

## Golden signals — is it healthy right now?

```bash
# All five public endpoints should return 200:
for u in https://us.mlxmesh.net/health https://eu.mlxmesh.net/health \
        https://directory.mlxmesh.net/health https://app.mlxmesh.net https://mlxmesh.net; do
  printf "%s -> " "$u"; curl -s -o /dev/null -w "%{http_code}\n" --max-time 8 "$u"; done
docker ps -q | wc -l                       # expect 119
free -m | awk '/Mem:/{print "avail="$7} /Swap:/{print "swap_free="$4}'
curl -s https://directory.mlxmesh.net/topology | grep -o '"pod_count":[0-9]*'   # expect 2
```

Prometheus (per coordinator, `GET /metrics/prometheus`): watch
`oim_ledger_consistent` (must be `1`), `oim_ledger_anomalies` (must be `0`),
`oim_queue_depth`, `oim_nodes_registered`, `oim_http_requests_in_flight`.

## Deploy a new version

**Golden rule: never run a from-scratch `docker build` on the box without
freeing RAM first.** A cold Go build of all five binaries has OOM'd the box
(see Incidents → OOM). The swap now cushions it, but free RAM anyway.

```bash
# 1. Sync the release commit's source from a dev checkout:
rsync -az --include='go.mod' --include='go.sum' --include='Dockerfile' \
  --include='cmd/***' --include='internal/***' --include='tools/***' --exclude='*' \
  ./ ec2-user@54.197.150.242:/home/ec2-user/mlxmesh-src/

# 2. Free RAM: stop ~40 node+stub pairs (they get recreated in step 5):
NAMES=""; for i in $(seq 19 58); do NAMES="$NAMES mlxmesh-node-$i mlxmesh-stub-exo-$i"; done
docker stop $NAMES

# 3. Build (watch `free -m` in another shell):
cd ~/mlxmesh-src && docker build -t mlxmesh .

# 4. Recreate infra (data volumes persist ledger/identity/federation/pins):
bash ~/redeploy-infra.sh

# 5. Recreate all nodes from the new image, restarting the stopped ones:
python3 ~/refresh-nodes.py 1 58

# 6. Verify (Golden signals above). Confirm the version stamp:
docker logs mlxmesh-coordinator-us 2>&1 | grep 'oim-coordinator'
```

For a **single-component** change, recreate only that container. Only `oim` node
code needs all 58 refreshed; a coordinator/directory-only change is just
`redeploy-infra.sh`.

Alternative (lower box risk): build the image on a dev machine / CI, `docker
save | gzip`, `scp`, `docker load` on the box — no compile load on the box. See
RELEASING.md. Prefer this once traffic makes the fleet churn during a box-build
unacceptable.

## Rollback

Images are tagged. To roll back, rebuild the previous tag's source (or `docker
load` the previously-saved image) and re-run `redeploy-infra.sh` +
`refresh-nodes.py`. Data volumes are untouched by a rollback, so ledger/identity
survive. **The ledger schema is append-only and has not had a breaking
migration** — a rollback of code is safe against the existing DB.

## Scale the simulated fleet up/down

- **Down (free resources):** `docker stop`/`rm` the highest-numbered
  `mlxmesh-node-N` + `mlxmesh-stub-exo-N` pairs. The coordinator prunes the
  stale registrations on its node-TTL.
- **Up:** `bash ~/spawn-nodes.sh <start_index> <count>` then, if TLS is wanted,
  `python3 ~/enable-node-tls.py <start> <end>`.

## Restart / reboot

- **Single container:** `docker restart mlxmesh-<name>`. `--restart
  unless-stopped` brings everything back after a host reboot automatically.
- **Whole host (AWS console stop/start):** the Elastic IP persists, so DNS is
  unaffected. After boot, run Golden signals; containers auto-start.

## Secrets rotation

- **API/admin key:** write a new value to `/etc/mlxmesh/api-key` (0600,
  root:root) and recreate the coordinators (`redeploy-infra.sh`). Clients using
  the old admin key must update.
- **Federation key:** same, but rotate on BOTH pods together — witnessing pauses
  until both share the new key.
- **TLS certs (nginx / Let's Encrypt):** renew per your ACME setup; the servers
  log a warning 30 days before their own `--tls-cert` expires
  (`WarnIfExpiringSoon`).

## Incident response

Each entry: **symptom → likely cause → action**. Confirm the cause before
running a state-changing fix — a signal that pattern-matches a known failure can
have a different root.

### SSH + all HTTPS endpoints unreachable at once
**Cause:** host OOM — almost always a from-scratch `docker build` on the box
under memory pressure (the original incident: cold Go build, 119 containers, no
swap → total lockup). **Action:** you likely have no SSH path in; stop/start the
instance from the AWS console. After boot: verify swap is active (`free -m`),
run Golden signals. **Prevent:** always free RAM before building (Deploy step 2);
or build off-box and `docker load`.

### One endpoint 502/504, others fine
**Cause:** that container crashed or is unhealthy; nginx can't reach it.
**Action:** `docker ps -a | grep <name>`; `docker logs --tail 100 <name>`.
Restart it; if it crash-loops, check the logs for a config/secret error (e.g. a
bind-mount source that became a directory — see below) and recreate with the
correct `docker run` (configs in redeploy-infra.sh / refresh-nodes.py).

### `cat: /etc/mlxmesh/<x>: Is a directory` / coordinator won't start
**Cause:** Docker auto-creates a **directory** at a bind-mount *source* path that
doesn't exist yet. A secret file that was never written becomes a directory.
**Action:** `sudo rmdir /etc/mlxmesh/<x>`, write the real file
(`head -c 32 /dev/urandom | base64 | sudo tee /etc/mlxmesh/<x>`; `chmod 600`;
`chown root:root`), recreate the coordinator.

### Public IP changed / DNS broken after a restart
**Cause:** instance without an Elastic IP was stop/started. **Should not recur**
— the Elastic IP is attached. If it does: update GoDaddy DNS A-records manually
(no API access) to the new IP, then `dig us.mlxmesh.net` to confirm propagation
before declaring recovery.

### `oim_ledger_consistent = 0` / "LEDGER ANOMALY" in coordinator logs
**Cause:** a user's debits exceed their credits (overdraft) or a debit exists
against a never-funded account — a real integrity violation, since DebitAccount
refuses overdrafts atomically at spend time. **Action:** pull the detail:
`curl -H "Authorization: Bearer <admin-key>" https://<pod>/admin/reconcile`.
Note the offending `user_id`(s) and `kind`. Do NOT restart blindly — the anomaly
is in persisted data and will survive it. Preserve the SQLite DB (copy the data
volume) for analysis before any remediation; treat as a possible exploit or a
bug in a new credit/debit path.

### Node count on a pod lower than expected but containers are up
**Cause:** normal churn after recreating nodes — old ephemeral node identities
stop heartbeating and are pruned on the node-TTL while the fresh registrations
settle. **Action:** none; confirm the fresh count via each node's target
coordinator. Only investigate if it stays low for more than a few minutes.

### Directory returning 429 / clients rate-limited
**Cause:** per-IP rate limiter with no `--trusted-proxy` set collapses all
nginx-proxied traffic into one bucket (nginx's IP) at the default 20 rps.
**Action:** set `--trusted-proxy <docker-network-cidr>` on the directory (and
coordinators) so the limiter keys on the real client via X-Forwarded-For, and
ensure nginx sets that header. As a stopgap, raise `--rate-limit-rps`.

### `--availability-reward` (verified availability bootstrap incentive)
Opt-in, off by default (see README's "Verified availability reward" section
for the full rationale — throttled by queue backpressure, not a treasury
cap, since credits have no external monetary value in this system). When
enabled, watch for:
- `oim_availability_probes_total` / `oim_availability_rewards_total`
  (Prometheus, `/metrics/prometheus`) — probes attempted vs. actually
  credited. A large gap between the two means probed nodes are failing
  dispatch (dead/misconfigured Exo, model not actually downloaded) more
  often than they're succeeding — check node logs, not the coordinator.
- Log lines `[coordinator] availability-reward: skipping round — backpressure
  X% > 40% ceiling` are expected and healthy during real traffic spikes —
  the feature is deliberately standing down, not broken.
- If `oim_availability_rewards_total` never increments at all with the flag
  on: confirm at least one registered node is genuinely non-simulated
  (`OIM_SIMULATED_NODE` unset) and idle for the full threshold (30 min
  default) — a seed-only deployment or one under constant real traffic will
  correctly never see a probe.

### A registered node never earns anything
**First, which delivery mode?** `curl <node's own address>:8765/detect` and
check `port_mapping`:
- `"pull"` (the default): the node long-polls the coordinator for work — no
  inbound reachability involved, so reachability is NOT the cause. If a pull
  node isn't earning, look instead at: (a) is it **linked** to the right
  wallet (else earnings land on its raw node_id — see the "linked but earnings
  land on the wrong account" fix); (b) does it actually have a **downloaded
  model** (a node with zero models is never dispatched real jobs); (c) is
  there any **real traffic / availability-reward** to serve at all. Confirm
  the pull path is healthy: the coordinator's `/jobs/claim` should show the
  node long-polling, and dispatches to it go through the mailbox, not an
  outbound dial.
- `"manual"` (push mode, explicit `--reachability-endpoint`): this is the only
  mode where reachability can be the cause. Check the coordinator's logs for
  `dial tcp ... connect: connection refused` against that node's advertised
  `reachability_endpoint` — definitive proof the coordinator can't reach it.
  Fix the endpoint/port-forward, or drop `--reachability-endpoint` to switch
  the node to pull mode (which sidesteps reachability entirely).

Reachability and wallet-linking are independent — a node must be reachable
(or pull) AND correctly linked; don't assume fixing one fixes the other.

## On-call

- **Primary contact:** the operator (single-maintainer project today).
- **Alerting:** ServerCat on the operator's iPad monitors all containers off-box
  and pushes alerts (a container down/crash-looping is the primary trigger).
  Complement with an HTTP check on the five public endpoints — container-up does
  not guarantee endpoint-200 (nginx/TLS or a wedged handler can still 502).
- **Escalation:** none beyond the operator yet — a gap to close before a real
  public SLA (see SLOS.md → "Not yet in place").
- **First 5 minutes of any page:** run Golden signals, capture
  `docker ps -a` + the failing container's `docker logs`, check
  `free -m`, and check `oim_ledger_consistent`. Decide restart-vs-investigate
  from that, not from the alert text alone.
