# mlxMesh

A distributed inference protocol that turns geographically-spread machines into a single, routable AI compute fabric — with strict privacy tiers, measured (not declared) performance accounting, and no native token. (Internal packages, the Go module path, and the `oim` CLI/binary names are unchanged.)

**Brand:** MeshAI &nbsp;|&nbsp; **Protocol:** Open Inference Mesh &nbsp;|&nbsp; **CLI:** `oim`

---

## What it is

Most distributed inference tools (e.g. [Exo](https://github.com/exo-explore/exo)) work within a single LAN cluster. `oim` adds a coordination layer *above* that: it federates multiple clusters across the internet into a routable mesh, with:

- **Dual-lane routing** — fast lane for interactive jobs (resolver-routed, low-latency), background lane for recurring/batch jobs (scheduler-routed, sticky-session)
- **MoE expert sharding** *(planner only — not wired into live dispatch; see below)* — the only WAN-viable strategy for large models (sequential token passing can't survive 20-150 ms inter-hop latency)
- **Division-order accounting** — measured resource lines, not declared promises; credits from bootstrap grants decay as earned capacity grows
- **Sensitivity tiers** — LOW / MODERATE / HIGH_REQUIRES_ATTESTATION (Secure Enclave gate on Apple Silicon)
- **Ed25519 node identity** — derived from public key, never operator-chosen
- **iOS coordination / security layer** — iPhone/iPad devices classify on-device and host encrypted payload pointers, adding a privacy layer *without* becoming compute nodes. Additive: the mesh routes identically with zero coordination devices present.
- **Portable wallet identity** — an Ed25519 account key (iCloud-Keychain synced, seed-recoverable) that consolidates credits across a user's devices. Not on-chain — it *proves ownership* of a server-side ledger balance.
- **Native clients** — SwiftUI apps for iOS/iPadOS, tvOS, and watchOS render live topology, and drive contribution/coordination from Apple hardware.

---

## Submitting inference jobs

The mesh is designed for **background inference jobs** — workloads where you need a response but not within interactive latency (think batch summarization, nightly report generation, embedding pipelines, async RAG retrieval). For real-time chat you still want a local Exo cluster; for deferred, cost-sensitive, or burst workloads you route to the mesh.

### How a job enters the network

```
Your application
       │
       │  POST /v1/chat/completions   (OpenAI-compatible)
       ▼
Pod Coordinator (regional)
       │
       │  credit check → dispatch → node selection
       ▼
Node Agent (wrapping local Exo)
       │
       │  tokens stream back
       ▼
Your application
```

1. Your app checks the caller's credit balance (or the system does it automatically on submit).
2. The coordinator selects a node via the fast-lane router (measured TPS, model availability, sensitivity tier).
3. The node streams the response back through the coordinator.
4. Credits are debited on completion, proportional to tokens delivered.

### Submitting a job (OpenAI-compatible API)

The coordinator exposes an OpenAI-compatible endpoint. Any SDK that speaks the OpenAI API works without modification.

**cURL:**
```bash
curl https://<coordinator>/v1/chat/completions \
  -H "Authorization: Bearer <your-api-key>" \
  -H "X-OIM-User-ID: <user-uuid>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "llama-3.2-3b",
    "messages": [{"role": "user", "content": "Summarize this document: ..."}],
    "max_tokens": 2048,
    "stream": false
  }'
```

**Python (openai SDK):**
```python
from openai import OpenAI

client = OpenAI(
    base_url="https://<coordinator>/v1",
    api_key="<your-api-key>",
)

response = client.chat.completions.create(
    model="llama-3.2-3b",
    messages=[{"role": "user", "content": "Summarize this document: ..."}],
)
print(response.choices[0].message.content)
```

**JavaScript / TypeScript:**
```ts
import OpenAI from 'openai'

const client = new OpenAI({
  baseURL: 'https://<coordinator>/v1',
  apiKey: '<your-api-key>',
})

const response = await client.chat.completions.create({
  model: 'llama-3.2-3b',
  messages: [{ role: 'user', content: 'Summarize this document: ...' }],
})
console.log(response.choices[0].message.content)
```

### Model parameter method

Models are selected by `model` string in the request body. The coordinator resolves the model to available nodes, preferring nodes with a matching downloaded model and sufficient measured TPS.

| Parameter | Type | Description |
|-----------|------|-------------|
| `model` | string | Model ID as reported by Exo (e.g. `llama-3.2-3b`, `mlx-community/Llama-3.2-3B-Instruct-4bit`) |
| `messages` | array | OpenAI-format message array (`role` + `content`) |
| `stream` | boolean | Set `true` for streaming token output via SSE |
| `max_tokens` | integer | Cap on output tokens; defaults to model's `max_context_tokens` |
| `temperature` | float | Sampling temperature (0.0–2.0) |

To discover which models are currently available on the mesh, query the directory:

```bash
curl https://<directory>/topology
# Returns pod list with servable_model_ids per pod
```

### Credit / token validator — can this caller run a job?

Before dispatching, the coordinator checks the caller's credit balance. The flow:

```
1. GET /users/{user_id}/balance
   → { grant_balance, earned_balance, total }

2. Estimate job cost:  cost = ConsumerCost(lane, sensitivity, max_tokens)   (see matrix below)
   → 100-credit startup grant ≈ 100 typical fast/moderate calls (~1k tokens each)

3. If total >= cost → dispatch
   Else → 402 Payment Required  {"error": "insufficient_credits", "balance": ..., "required": ...}

4. On completion: debit the consumer the observed cost; credit the serving node its
   reward; book the spread to the treasury (see Cost / reward matrix).
```

### Cost / reward matrix (the house edge)

All pricing lives in [`internal/economics`](internal/economics/pricing.go) so the
debit and credit paths can never diverge. The model is a **spread, not a
transfer**: a provider is always paid *less* than the consumer is charged, and
the difference — the **house edge (25%)** — accrues to the network treasury
(`oim-treasury`, a reserved ledger account). The treasury funds iOS coordination
rewards; startup grants and availability rewards are minted directly (inflationary,
not treasury-funded). This is the "casino math": credits are a sink, not a closed
zero-sum loop, so the supply can't inflate to worthlessness or drain to zero.

```
consumer_cost   = 1.0 × lane × sensitivity   (credits per 1k output tokens)
provider_reward = consumer_cost × (1 − 0.25)      ← node earns 75%
network_margin  = consumer_cost − provider_reward ← treasury keeps 25%

lane:         fast ×1.0 (interactive premium) · background ×0.5 (batch discount)
sensitivity:  low ×0.5 · moderate ×1.0 · high_requires_attestation ×3.0
```

Per 1,000 output tokens:

| Lane | Sensitivity | Consumer pays | Provider earns (75%) | Treasury (25%) |
|------|-------------|--------------:|---------------------:|---------------:|
| Fast | low | 0.50 | 0.375 | 0.125 |
| Fast | moderate | 1.00 | 0.75 | 0.25 |
| Fast | high | 3.00 | 2.25 | 0.75 |
| Background | low | 0.25 | 0.1875 | 0.0625 |
| Background | moderate | 0.50 | 0.375 | 0.125 |
| Background | high | 1.50 | 1.125 | 0.375 |

**iOS coordination job** (hosting an encrypted pointer): a flat **0.02 credits per
pointer served**, paid to the device's linked wallet account out of the treasury —
coordination is a lightweight security service, not compute, so it earns a small
but nonzero reward. Fast-lane earnings are credited from the coordinator's *own
observed* token count (not the node's self-report — see [Security model](#security-model--threat-analysis)),
so a node can't inflate what it earns.

> **Setup note — linking is required to earn.** A coordination device (or a
> desktop node) only earns once its device/node ID is **linked** to a wallet
> account (`POST /account/{address}/link-device`, or the iOS Account tab's
> **"Link this iPad's participation"** button). An unlinked device announces and
> is visible on the map, but has no account to pay — credits silently go
> nowhere. Earlier builds regenerated the iOS device ID on every launch, which
> made linking impossible and left participation permanently stuck at 0 credits;
> the ID is now persisted per-install (see `DeviceIdentity.swift`).

*These multipliers/edge are tunable constants in `internal/economics`; future
work can layer reputation multipliers or streak bonuses on top of this base.*

**Bootstrapping-economics activity discount.** The table above is the
*undiscounted* matrix — what a provider always earns, in any network state.
`economics.ConsumerCostWithActivityDiscount` additionally discounts what the
**consumer** pays on the fast lane when the network is quiet (queue
backpressure below 40%), tapering the discount to nothing as backpressure
rises: at a fully idle network (0% backpressure), the consumer pays exactly
what the provider earns — the treasury's margin compresses to zero, never
negative, and **the provider's payout never changes because of this
discount**. This is the fix for the bootstrapping problem an early-network
user hits: burning an entire day's meager earnings on a single query. See
TODO.md's Economic Sustainability section for the full reasoning (why this,
not a coordination-reward bump or a new consumer tier).

### Verified availability reward (bootstrap incentive, opt-in)

A linked, running node earns nothing between real dispatched jobs — for a
brand-new deployment with little consumer traffic yet, that's a real
cold-start problem: nobody wants to leave a Mac on and registered if there's
no reason to believe it'll ever get paid.

`--availability-reward` (off by default) has the coordinator itself act as a
tiny, randomly-timed test consumer: every ~10 minutes (jittered so the timing
can't be predicted/gamed), it dispatches one small real inference request —
through the *exact same* dispatch path (`DispatchToResolvedNode`) and pricing
function (`economics.ProviderReward`, at the cheapest background/low tier)
real consumer traffic uses — to one of the longest-idle real (non-simulated)
nodes. A node can't fake this by merely staying registered: it has to
genuinely have a downloaded model and return a real completion. Rewards are
naturally tiny (fractions of a credit) since they scale with the small
number of tokens a short probe prompt produces.

The per-round probe budget (`coordinator.ScaledProbeBudget`) scales with how
quiet the network is: 3 nodes/round under any real load (the original fixed
value), growing up to 15 nodes/round at 0% backpressure — a fully idle
network has more otherwise-unpaid idle capacity to subsidize, and this
tapers back down automatically as real traffic returns. Bootstrapping-economics
fix, same reasoning as the activity discount above.

No debit, no treasury margin — nobody is being charged for this. It's a
self-funded subsidy minted directly into the node's account, the same way
the startup grant is minted from nothing. That's deliberate: **credits in
this system have no external monetary value** — it's a closed barter
network, not a currency, so minting a small bootstrap incentive isn't
deflationary the way it would be for a real currency. The actual constraint
at scale is compute capacity vs. demand, not credit supply — so instead of a
treasury-balance cap, the probe throttles against **queue backpressure**
(`JobQueue.BackpressurePct()`): above 40% saturation, a round is skipped
entirely, since real consumer traffic is already using the network and
doesn't need subsidized competition for the same idle capacity.

```
oim-coordinator --availability-reward ...
```

See `runAvailabilityProbe`/`probeIdleNodes` in `cmd/coordinator/main.go` and
`internal/coordinator/availability.go` for the implementation, and
`RUNBOOK.md` for the operational metrics this flag exposes.

To check balance programmatically before submitting:

```bash
curl https://<coordinator>/users/<user_id>/balance
# { "grant_balance": 100.00, "earned_balance": 0.00, "total": 100.00 }
```

To claim the one-time startup grant (100 credits — enough for ~100,000 output tokens at the default fast/moderate rate of 1 credit per 1k tokens):

```bash
curl -X POST https://<coordinator>/users/<user_id>/startup-grant
```

### Hooking an application or service

**Background job queue pattern** — enqueue a job, poll for completion:

```python
import httpx, time

def run_mesh_job(prompt: str, user_id: str) -> str:
    coordinator = "https://<coordinator>"
    headers = {"Authorization": "Bearer <api-key>"}

    # 1. Check credits
    bal = httpx.get(f"{coordinator}/users/{user_id}/balance").json()
    if bal["total"] < 0.01:
        raise ValueError("Insufficient credits")

    # 2. Submit job
    resp = httpx.post(
        f"{coordinator}/v1/chat/completions",
        headers=headers,
        json={"model": "llama-3.2-3b", "messages": [{"role": "user", "content": prompt}]},
        timeout=120,
    )
    resp.raise_for_status()
    return resp.json()["choices"][0]["message"]["content"]
```

**Streaming pattern** — useful for long-running summarization where you want partial results.
> **Status:** server-side token streaming (`stream: true`) **is implemented on the fast lane** — real SSE passthrough at every hop (Exo → node → coordinator → client), with billing read from the trailing usage frame. The background lane intentionally stays buffered/polling (recurring jobs don't need token-by-token delivery). Streaming is not available for jobs submitted with a `reservation_id` (encrypted-pointer reservations return buffered responses).

```python
with httpx.stream("POST", f"{coordinator}/v1/chat/completions",
    headers=headers,
    json={"model": "llama-3.2-3b", "messages": [...], "stream": True},
) as r:
    for line in r.iter_lines():
        if line.startswith("data: "):
            chunk = json.loads(line[6:])
            print(chunk["choices"][0]["delta"].get("content", ""), end="", flush=True)
```

**Webhook / async pattern** (planned — not yet implemented) — submit a job with a `callback_url`; the coordinator POSTs the completed response to your endpoint when done. This is the target pattern for fire-and-forget batch pipelines. Tracked under [Path to release](#path-to-release-safe-secure-scalable).

---

## Prerequisites

| Requirement | Version |
|-------------|---------|
| Go | 1.25+ (per `go.mod`) |
| [Exo](https://github.com/exo-explore/exo) | running locally at `http://localhost:52415` |

`oim` wraps Exo as a black-box HTTP API for Milestone 1. Apple Silicon (M-series) with unified memory is the reference platform.

---

## Quick start

```bash
# 1. Clone and build
git clone https://github.com/open-inference-mesh/oim.git
cd oim
make install          # installs oim binary to $GOPATH/bin

# 2. Make sure Exo is running, then check node status
oim node status

# 3. Optionally override defaults
oim node status --exo-url http://localhost:52415 --cap 0.5

# 4. Run a benchmark against a downloaded model
oim bench run --model mlx-community/Llama-3.2-3B-Instruct-4bit --prompt medium --samples 3
```

### Building manually

```bash
make build            # produces ./bin/oim
./bin/oim node status
```

---

## Configuration

Copy the example config and edit:

```bash
mkdir -p ~/.config/oim
cp config/node.example.yaml ~/.config/oim/config.yaml
$EDITOR ~/.config/oim/config.yaml
```

Key settings:

| Field | Default | Description |
|-------|---------|-------------|
| `exo_url` | `http://localhost:52415` | Local Exo endpoint |
| `memory_cap_pct` | `0.5` | Fraction of RAM to offer (actual cap = min(pct×total, available)) |
| `geographic_hint` | `us` | Coarse region for pod assignment (`us` / `eu` / `apac`) |
| `reachability_endpoint` | — | How the pod coordinator reaches this node |

### Reachability: outbound work-pull (default — no port forwarding, ever)

By default a node receives work the way an ASIC miner receives it from a
pool: it opens an **outbound** connection to the coordinator, long-polls for
jobs, runs them via Exo, and posts results back — all outbound. The
coordinator never dials into the node, so **NAT, home routers, port
forwarding, UPnP, and firewalls are all irrelevant**. Point the node at a
coordinator and it just works. This is "pull mode," and it's on automatically
whenever no `--reachability-endpoint` is set.

```
oim node start --coordinator https://us.mlxmesh.net   # pull mode, zero network config
```

`GET /detect` (or OIMMenuBar's popover) reports `port_mapping: "pull"` and the
node shows "Connected — receiving work ✓".

**Push mode (opt-in, for LAN / simulated / advanced setups):** set an explicit
`--reachability-endpoint http://<addr>:8765` and the coordinator dispatches
*into* the node over HTTP instead. This is what the simulated Docker fleet and
the integration tests use (they pass an explicit endpoint), and it's available
to a LAN operator who wants inbound dispatch. `port_mapping` then reports
`"manual"`.

> **v1 limitation:** SSE streaming (`stream: true`) is served buffered for
> pull-mode nodes — the pull mailbox is request/response, so token-by-token
> passthrough isn't available over it yet (a fast-follow). This doesn't affect
> earning, credit accounting, or the non-streaming "Try the mesh" path;
> streaming requests are simply routed to push-mode nodes when available.

---

## Running over TLS (HTTPS)

The coordinator and directory serve plain HTTP by default (convenient for a
single-box run and the docker sim). **Before exposing anything beyond localhost,
turn on TLS** — otherwise API keys and job payloads travel in plaintext. Both
servers log a loud warning while running without it.

**1. Generate a local dev CA + server cert** (covers `localhost` + your LAN IP so
a real iPhone/iPad connects cleanly):

```bash
scripts/gen-dev-certs.sh 192.168.1.135        # your Mac's LAN IP
# → certs/ca.crt, certs/server.crt, certs/server.key
```

**2. Serve HTTPS:**

```bash
oim-coordinator --listen :9000 --tls-cert certs/server.crt --tls-key certs/server.key ...
oim-directory   --listen :9100 --tls-cert certs/server.crt --tls-key certs/server.key ...
```

**3. Point Go nodes at the HTTPS coordinator and trust the CA:**

```bash
oim node start --coordinator https://192.168.1.135:9000 --tls-ca certs/ca.crt
# (or --tls-skip-verify for throwaway local testing — logged, never for prod)
```

**4. Trust the CA on your iPhone/iPad** so the SwiftUI app connects without a
warning: AirDrop `certs/ca.crt` to the device → install the profile (Settings →
General → VPN & Device Management) → enable full trust (Settings → General →
About → Certificate Trust Settings) → set the app's directory URL to
`https://192.168.1.135:9100`. The apps use a **local-networking-only** ATS
policy: plaintext http is allowed only to LAN hosts, so any public endpoint must
be https.

For a **public deploy**, use a real CA (Let's Encrypt / your cloud provider's
cert manager) instead of the dev script, or terminate TLS at a load balancer.
Automated cert issuance/renewal (ACME) is on the [release path](#path-to-release-safe-secure-scalable).

**5. Coordinator→node dispatch over TLS too** — a node can serve its own job
endpoint over HTTPS with `--tls-cert`/`--tls-key`. This uses a **different
trust model** than steps 1-4 above: nodes are independently operated and
self-signed (there's no shared CA to hand out), so instead of chain
verification the coordinator **pins the exact certificate fingerprint**
recorded at that node's registration — tamper-evident via the same Ed25519
signature covering the rest of the manifest. A self-signed cert is genuinely
fine here:

```bash
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -keyout node.key -out node.crt -days 825 -subj "/CN=node" \
  -addext "subjectAltName=IP:192.168.1.140"
oim node start --tls-cert node.crt --tls-key node.key --reachability-endpoint https://192.168.1.140:8765 ...
```

---

## CLI reference

```
oim node status     Report live capability manifest from local Exo instance
oim node start      Start node agent (registers with a coordinator, serves jobs)

oim bench run       Benchmark a model and save MeasuredSignature
oim bench compare   Compare claimed vs measured performance
```

To run the full local simulation mesh (2 regions, ~110 containers — every
simulated node is an agent + stub-exo pair — plus live traffic and coordination
participants) instead of a single node:

```bash
go run ./tools/gen-compose > docker-compose.yml   # regenerate if needed
docker compose up --build                          # heavy — needs several GB free
# Dashboards/apps point at the directory on :9100 and coordinators on :9000/:9001
```

### Node Setup (cluster topology view)

The web dashboard's **Node Setup** tab shows an Exo-style per-device cluster
diagram (each device's RAM / GPU load / temperature / power, wired in a ring),
plus contribution controls (memory cap, models, schedule). It reads the **local
node agent's** `/detect` endpoint, so point the "Local agent" bar at wherever
your agent listens:

- **Against the sim:** `http://localhost:8765` (node-us-1) or `http://localhost:8780`
  (node-eu-1) — both are seeded as 3-device clusters so the diagram is populated.
  Other sim nodes derive device count from memory (one device per ~48 GB).
- **Against your own hardware:** run `oim node start` with Exo running, then point
  the bar at that agent's `--listen` address. The diagram reflects Exo's live
  topology. (iOS has no Node Setup — iOS devices join as the coordination layer,
  not as compute nodes.)

---

## Architecture overview

```
Global Directory (librarian)         ← M4 (centralized) → M7 (ledger authority partial; directory federation stubbed)
        │
        ▼
Pod Coordinators (1 per region)      ← M2
   │        │
   │        └── Coordination Registry ← M8  (iOS pointer-hosts; never routed to)
   ▼
Node Agents (wrapping Exo)           ← M1 ✓
        ▲
        │  live topology + contribution/coordination + wallet
Native clients: iOS · tvOS · watchOS ← M8 / M9  (OIMDashboard/)
```

### Milestones

**Protocol core** (the original 7-milestone build plan in ARCHITECTURE):

| # | Status | Description |
|---|--------|-------------|
| M1 | **Done** | Node agent: manifest assembly, resource governor, bench, Ed25519 identity |
| M2 | **Done** | Pod coordinator: registry, fast-lane router, background scheduler, job queue, rate limiting |
| M3 | **Done** | Spot-check verification, tier-claim validation, measurement store |
| M4 | **Done** | Centralized global directory with gossip sync and cache fallback |
| M5 | **Done** | Division-order settlement ledger with SQLite persistence (single coordinator) or Postgres (`--ledger-db-url`, multi-coordinator safe via row locks + unique constraints), startup grants with PoW |
| M6 | **Done** | MoE expert-shard planner with proportional assignment and load imbalance detection |
| M7 | **Partial** | Federated *directory* (multi-librarian, `FederatedResolver`/`DHTResolver`) is still a stub. Federated ledger *authority* — the harder half of M7 — now has coordinator identity + signed, TOFU-pinned pod registration and cross-pod signed-ledger-event witnessing/audit (`internal/federation`); see [Security model](#security-model--threat-analysis) item 3 for exactly what's closed vs. still open |

**What remains of the original 7-milestone scope:** **M7** (federated
directory + ledger authority) — M1–M6 are complete and the core
inference/credit/routing loop is verified working end-to-end. M7 was always
entangled with the hardest open problem, federated ledger authority (see
[Security model](#security-model--threat-analysis), item 3): decentralizing
discovery is straightforward, but decentralizing *who is authoritative for
credits* is the real work. That work is now partially done (impersonation
prevention + witnessed audit trail); full open, permissionless federation
(the directory half) is still a stub.

**Extended scope** (built beyond the original plan — see [Beyond original scope](#beyond-original-scope)):

| # | Status | Description |
|---|--------|-------------|
| M8 | **Done** | On-device routing + iOS coordination/security layer: on-device CoreML classifier, client-side payload encryption (P256 ECDH → AES-256-GCM), coordinator hint validation (escalate-only), coordination registry + served-pointer accounting, native iOS/tvOS/watchOS apps, and now **full node-side pointer consumption**: a Go-native `internal/payloadcrypto` (ECDH-P256 → HKDF-SHA256 → AES-256-GCM, byte-compatible with the Swift client) lets the assigned node fetch and decrypt the ciphertext itself. A new `POST /v1/reserve-node` lets a client learn a specific node's public key (and reserve it) *before* encrypting, so privacy-mode jobs still get the coordinator's TPS-aware routing instead of picking a node blind. |
| M9 | **Done** | Portable wallet identity: Ed25519 account key, `oim…` address = ledger user_id, challenge-response auth, account-signed device linking for credit consolidation, iCloud-Keychain sync + Base32 recovery key. Cross-language crypto (CryptoKit ↔ Go) verified. |

---

### Beyond original scope

The original ARCHITECTURE spec defined a headless Go protocol (M1–M7). The following were added on top and are **not** in that spec — flagged here so the delta is explicit:

- **iOS as a coordination/security layer, not a compute node.** iOS/iPadOS cannot run Exo (Python+MLX, macOS/Linux only), so instead of forcing inference onto them, iOS devices classify on-device and host encrypted payload pointers. This is *additive* — with zero iOS devices the mesh routes exactly as M1–M7 defined. **iOS 27 update:** Apple now ships `MLXLanguageModel` and `CoreAILanguageModel` (public `LanguageModel` protocol) for running MLX-community models on Apple Silicon via the new Core AI framework. This provides a first-party inference binding for on-device MLX models on iPadOS 27+ hardware, lowering the barrier for iPad compute nodes. However, there's still no Apple-provided HTTP inference server — you'd still need to write the networking layer (NWListener) yourself to expose it as a mesh-servable endpoint.
- **Encrypted-pointer privacy path.** Client-side P256 ECDH + HKDF-SHA256 + AES-256-GCM; the coordinator sees only a metadata pointer (hash + fetch URL + ephemeral pubkey), never plaintext. Served-pointer counts are attributed per device.
- **Portable wallet (M9).** Credit consolidation + recovery across devices without a blockchain.
- **Native Apple apps** (`OIMDashboard/`) — live topology, "Try the mesh," network-load/backpressure, coordination layer, wallet, and a tvOS global-map view.
- **Simulation harness** — `tools/jobgen` (incl. `--pointer-host` mode) + `tools/gen-compose` produce a 100+ container multi-region mesh with continuous traffic and live coordination participants, for device-free testing.
- **Operational hardening already landed** — SQLite persistence, write-path signing, rate limiting, configurable CORS, security headers, config validation, dynamic queue capacity.
- **Cluster deduplication** — prevents double-counting capacity and double-paying for clustered nodes (nodes with the same `ClusterSignature`). Only one node from a physical Exo ring is dispatched to, avoiding duplicate capacity accounting.
- **Warm model support** — coordinator can instruct a node to load a model it has downloaded but not yet active via `POST /warm-model`, reducing cold-start latency for frequently-used models.
- **Observed throughput routing** — coordinator tracks actual node performance (tokens/sec) from completed jobs and uses it for routing decisions instead of relying solely on self-reported benchmarks.
- **Device unlinking** — `POST /account/{address}/unlink-device` allows removing a device/node ID from a wallet account, useful when decommissioning hardware.

---

### Known gaps at a glance

Concrete things a tester will hit today, so nothing reads as "silently broken":

- **Node Setup cluster topology is web-only and Exo-driven.** The dashboard's per-device diagram (RAM/GPU/temp/power ring) renders from the local agent's `/detect`, which parses Exo's `/state`. It now populates in the docker sim (stub-exo emits a topology; node-us-1 / node-eu-1 are 3-device clusters). Against a **real** Exo cluster it depends on Exo's `/state` field names (`topology.nodes`, `nodeMemory`, `nodeIdentities`, `nodeSystem`) — validate this against your Exo build (e.g. lab-02) since that schema hasn't been confirmed against a live instance. **iOS has no Node Setup** by design — iOS devices can't run Exo, so they contribute as the coordination/security layer, not as compute nodes. **iOS 27 opportunity:** Apple now ships `MLXLanguageModel` and `CoreAILanguageModel` (public `LanguageModel` protocol) for running MLX-community models on Apple Silicon via Core AI. This enables iPad compute nodes without hand-rolling MLX bindings, but requires implementing the HTTP networking layer (NWListener) to expose it as a mesh-servable endpoint — not yet implemented.
- **Webhook / async callback** submission is documented as a target but not implemented.
- **M7 federated directory is a stub; progressive decentralization is partially started.** A public seed IS now deployed (task #42) and clients/coordinators can be configured with multiple directory endpoints so no single instance is a hard dependency (task #49) — but there's still only one directory *instance* actually running, "parity" now has a real metric (real vs. simulated capacity, see below) but no defined threshold or automatic handoff logic, and `FederatedResolver`/`DHTResolver` remain stubs. (The ledger-authority half of M7 — coordinator identity, pod pinning, cross-pod signed-event witnessing — is now partially built; see [Security model](#security-model--threat-analysis) item 3.)
- **MoE expert sharding is a planner, not a live feature.** `internal/coordinator/moe_planner.go` (assign experts to nodes by memory, route tokens to the expert-holding node, detect load imbalance) is implemented and tested (M6) but **not wired into any dispatch path** — no request is MoE-sharded across mesh nodes today. See the note below on why it wouldn't speed up the fast lane anyway. What *is* wired is **query decomposition** (`RouteWithDecomposition`), and only for the **background lane** (it refuses fast-lane jobs).

Everything above is tracked in the release path below.

### A note on MoE sharding and speed

A common intuition is that sharding a model across nodes makes the **fast (interactive)
lane** faster. It's the opposite. MoE expert-sharding across the *mesh* (WAN) is a
**capacity** strategy, not a **latency** one: it lets you *run* a model too big for any
single node by splitting its experts across nodes — but every token that activates a
remote expert pays the 20–150 ms inter-hop latency, which is fatal for interactive use.
So sharding belongs to the **background lane** (throughput/big-model capacity), and the
fast lane stays **single-node** precisely to avoid those hops.

Where sharding *does* make inference fast is **inside a local Exo cluster** (LAN,
sub-millisecond links) — Exo itself tensor/expert-shards a big model across your cluster's
devices (e.g. your 2 Mac Studios + MacBook Pro). The mesh node *wraps* that cluster, so
local sharding already happens; the mesh's job is to route *between* clusters, where WAN
latency rules out per-token sharding. **Fast-lane speed comes from good single-node
routing (measured TPS, model present) + local Exo cluster sharding — not from mesh-level
sharding.** Wiring the MoE planner into a real background-lane big-model path (with Exo
expert-routing integration) is future work.

---

## Security model & threat analysis

Because this is (a) open source and (b) a **credit-based** compute network, the
first question is: *if anyone can read and fork the code, what stops them from
minting credits, spoofing the system, or knocking it over?* Here is the honest
answer, grounded in the current code.

### Can queries actually flow through? Yes — verified.

A node registers with a coordinator and a real `POST /v1/chat/completions` is
dispatched fast-lane to that node and proxied back with `oim_served_by_node_id`,
`oim_lane`, and measured `oim_tokens_per_sec`. The end-to-end path
(credit gate → dispatch → node/Exo → response → debit) works today. It is usable
**as a trusted-coordinator network right now**. It is *not yet* safe as an open,
decentralized credit network — see the open items below.

### What already holds

- **The ledger is server-authoritative.** Credits live in the coordinator's
  SQLite ledger. No client or node can write to it directly — every mutation
  goes through a coordinator endpoint. Forking the *client* or *node* code cannot
  fabricate a balance.
- **The write path is Ed25519-signed.** `register`, `refresh`, `job-outcome`
  (the credit-minting endpoint), `benchmark-result`, and settlement all require a
  signature from the registered node key. You **cannot report earnings for a node
  you don't control**, or impersonate another node's identity.
- **Grants are Sybil-resistant.** The one-time startup grant requires a
  proof-of-work nonce (18 bits) *and* a dedicated per-IP rate limit, so minting
  thousands of throwaway `user_id`s to farm grants costs real CPU time. Claims are
  idempotent against the ledger, surviving restarts.
- **Wallet auth is challenge-response**; the account key never leaves the device,
  and device-linking is account-signed (you can't attach your device to someone
  else's balance).
- **Per-IP rate limiting wraps every endpoint**; the job queue is bounded with
  backpressure; sensitivity tiers + Secure-Enclave attestation gate sensitive
  jobs; TLS 1.2+ is available; payloads can be client-encrypted.
- **Node→coordinator calls are resilient, not naive.** All outbound coordinator
  calls (register/refresh/reputation) retry transient failures (network errors,
  5xx, 429) with exponential backoff + jitter, and permanent failures (4xx) fail
  fast instead of hammering the server; a client-side token-bucket limiter caps
  how fast a single node can call out, so a bug can't turn one node into a
  self-inflicted flood. (`internal/httpx`, tasks #22/#24)
- **Quantum vulnerability is acknowledged.** Ed25519 (used for node/coordinator/wallet identity) would fall to a sufficiently large quantum computer running Shor's algorithm. This is true of all ECC-based systems in production today (TLS, SSH, Signal, Bitcoin). NIST finalized post-quantum signature standards in 2024 (ML-DSA/Dilithium, SLH-DSA), but migration is a multi-year industry-wide project. This is a known long-term consideration, not an immediate action item.

### Open vulnerabilities

1. ✅ **RESOLVED — Unverified self-reported earnings.** Both fast-lane and
   background-lane earnings are now credited from the coordinator's **own observed**
   token count (it dispatches/proxies every job, so it counts the tokens itself).
   `/job-outcome` is **reputation-only and never credits**, so a node running
   modified code cannot inflate its earnings. Verified end-to-end by the integration
   suite (75/25 split, no double-credit). *(task #51 — done)*
2. ✅ **RESOLVED — Earn/debit asymmetry.** Debit and credit are now derived from the
   same coordinator-observed token count, so a node can never earn more than the
   consumer was charged. *(task #51 — done)*
3. 🟡 **PARTIALLY ADDRESSED — Federated ledger authority (M7).** Each pod still
   runs its own independent ledger (no shared source of truth), but two of the
   three concrete gaps this used to describe are now closed:
   - **Coordinator impersonation** — `PodHealthDigest` registration used to be
     completely unauthenticated, so any coordinator could announce itself under
     any `pod_id`. Coordinators now have their own Ed25519 identity
     (`internal/identity.LoadOrCreateAt`, separate from node identity) and sign
     every digest (`protocol.SignPodHealthDigest`); the directory
     (`internal/directory.PinStore`) trust-on-first-use pins each `pod_id` to
     the key that first registered it — persisted across restarts — and
     rejects a different key later claiming the same `pod_id`. An
     operator-curated `--authorized-pods` allowlist is available for
     deployments that want to enumerate federation membership explicitly
     instead of trusting whoever registers first.
   - **No way to catch a rogue pod's claims** — every credit a pod issues
     (grant or earned) is now also appended to a signed, sequenced,
     per-pod event log (`internal/federation`). Peer pods pull and verify each
     other's history (`GET /federation/ledger-events`, opt-in via
     `--federation-key`) and can audit a peer's *live* balance claim against
     that peer's own signed history (`GET /federation/audit/{user_id}`): a
     claimed balance that exceeds everything the pod ever signed as credited
     is provable, not just suspected. Re-signing a previously-witnessed
     sequence number differently is detected outright
     (`federation.ErrForkedHistory`). Verified live against the production
     seed: a 100-credit grant issued on pod-us was witnessed by pod-eu and
     `GET /federation/audit/{user_id}?peer_endpoint=...` returned
     `claimed_balance: 100, witnessed_gross_credits: 100, consistent: true`.
   - **Still open**: this is PKI-based trust among a curated/TOFU-pinned set of
     pods, not Byzantine-fault-tolerant consensus or staking/slashing — a pod
     that is itself compromised (or run by a bad actor from day one, before any
     peer has witnessed it) can still misbehave undetected until a peer
     actually audits it, and there's no automatic quarantine/eviction of a pod
     caught misbehaving (an operator has to act on the log line). The project
     deliberately has no native token (see below), so a staking/slashing
     design would need to invent a stakeable asset from scratch — out of scope
     here. Full open, permissionless decentralization (`FederatedResolver`/
     `DHTResolver`) remains a stub. *(task #52 — narrowed and partially done;
     see [Progressive decentralization](#path-to-release-safe-secure-scalable)
     for what still blocks the "network takes over from the seed" vision)*
4. **DDoS — mostly hardened.** Per-IP rate limiting is now backed by a global
   in-flight **concurrency cap** (503 + Retry-After), an 8 MiB **request-body cap**,
   a `ReadHeaderTimeout` slow-loris guard, and an **SSRF allowlist** on the payload
   fetch URL (blocks loopback + link-local/cloud-metadata). The guard now stays
   attached past the front door via `httpmw.SafeFetchClient`: it **re-validates every
   redirect hop** (so a host can't pass with a public IP then 302 the fetching node
   to `169.254.169.254` or an internal port) **and validates the actual IP at
   connect time** through the dialer's `Control` hook (closing the DNS-rebinding gap
   where a host resolves to a good IP at check time and a blocked one at dial time).
   Remaining: a distributed
   volumetric flood still needs an **upstream WAF/scrubbing layer** (Cloudflare or
   equivalent), and read endpoints (`/topology`, `/nodes`, `/balance`) — plus the
   newer `POST /v1/reserve-node` — share the project's existing "auth is opt-in via
   `--api-key`, off by default" posture rather than being individually gated. A
   security review of the reservation endpoint specifically confirmed reservation
   churn has **no side effect on real dispatch** (it never touches a node's
   in-flight counter or routing score — verified against the code, not assumed),
   so spamming it can't starve a node the way a first pass at the review suggested.
   Read-endpoint auth is now available (opt-in): `--protect-user-reads` gates the
   per-user endpoints and `--user-quota-per-hour` caps per-account request volume;
   the directory gained per-IP rate limiting and both servers a `--trusted-proxy`
   option so those limits are effective behind a fronting reverse proxy (see the
   [release path](#path-to-release-safe-secure-scalable) security table).
5. ✅ **RESOLVED — Streaming billing edge case.** A streaming job whose node
   response ends with no (or a malformed) trailing SSE usage frame used to silently
   debit the consumer $0 and credit the node nothing, with no signal that anything
   was wrong. Now logged (`oim_rejections_total{reason="stream_missing_usage_frame"}`
   + a coordinator log line) so a backend that doesn't honor
   `stream_options.include_usage` shows up in monitoring instead of quietly giving
   away free inference. Billing itself is unchanged (never invents a cost it didn't
   observe) — this is an observability fix, not a new charge.
6. ✅ **RESOLVED — Self-minting + replay via `POST /settlement/records`.** This
   endpoint used to credit the ledger for any settlement record whose signature
   verified against the claimed node's own registered key. That check only proves
   *which node authored the record*, not that any work happened — a node holds its
   own private key, so it could self-sign a record naming itself with an arbitrary
   value and mint credit with no cross-check against a coordinator-dispatched job,
   and with no `record_id` dedup or freshness bound the same valid record replayed
   indefinitely. This was a live counterexample to item #1's "a modified node
   cannot inflate its earnings" (a *different* credit path than `/job-outcome`).
   The endpoint no longer credits at all — records are stored purely as inert
   dispute evidence ("publishing a record is not the same as moving money"), and
   the only authoritative earning path is the coordinator's own observed-token
   crediting (items #1/#2). No honest flow depended on it
   (`CreateSettlementRecord`/`PublishSettlementRecord` have no live callers).
7. ✅ **RESOLVED — Account takeover via unauthenticated API-key rotation.** The
   *first* `POST /users/{id}/api-key` mint for a new `user_id` is open by design
   (anonymous-account bootstrap), but nothing used to stop an unauthenticated
   caller from *replacing* an already-issued key — so anyone who learned a
   victim's `user_id` could overwrite their key and seize the account. Rotation
   now requires proof of control (the current `oim_` key or an admin credential);
   trust-on-first-use for the initial mint is unchanged.

### "How do I stop someone from modifying the code to mint credits?" — direct answer

- **Modifying the client or node** does **not** let you mint: the coordinator owns
  the ledger and credits *only* from its own observed token count for jobs it
  dispatched and verified (items #1/#2/#6), so a forked node can't self-report
  earnings on any path. The residual risk is *output fraud* — a node returning
  garbage tokens it never really computed — bounded by signatures (no
  impersonation) + rate limits + tier-verification (catches capacity lies), and
  fully closed only by spot-checks/redundancy that catch bad output (on the
  release path). Earned credits are no longer provisional against the
  self-reporting attack that used to make them so.
- **Modifying the coordinator** only matters in the *federated* model. In the
  current seed model, a rogue coordinator's credits aren't honored anywhere else.
  In the federated model this is item #3 — the open research problem, not a bug to
  patch.

**Bottom line:** run by a trusted operator (the seed), the credit system is now
sound against client/node tampering on every crediting path — the two remaining
credit-integrity holes (self-reported earnings #1/#2 and the settlement-record
self-mint #6) are closed, and account takeover via key rotation (#7) is gated.
The residual is *output-quality* fraud (mitigable with spot-checks/redundancy, on
the release path) and true open decentralization (#3), which needs a
consensus/staking/proof design that does not yet exist here. A third-party
security review of the credit, attestation, and settlement paths is on the
release path.

---

## Responsible use & content policy

mlxMesh is a **coordination and routing protocol** — it connects inference consumers with independently-operated compute nodes, but it does not host models, inspect prompts, or moderate generated content. This section clarifies the project's role and the tradeoffs inherent in its design.

### Infrastructure role, not content host

- The mesh provides routing, credit accounting, and node discovery — similar to how a network router or VPN provider transports data without examining it
- Actual inference execution happens on nodes running [Exo](https://github.com/exo-explore/exo), which pulls models from sources like Ollama or HuggingFace
- Those model sources have their own content policies and curation mechanisms; node operators choose which sources and models to host
- The coordinator cannot inspect job content due to the privacy design (encrypted payload pointers, HIGH sensitivity tier with Secure Enclave attestation)

### Privacy vs. moderation tradeoff

The system is designed for **privacy-first inference**:
- Encrypted payload pointers mean the coordinator never sees the actual prompt or response
- HIGH sensitivity jobs require Secure Enclave attestation, ensuring the payload only decrypts on hardware with verified security properties
- This privacy guarantee is incompatible with coordinator-side content moderation — you cannot have both end-to-end encryption and centralized inspection

### Operator responsibilities

Pod and node operators are responsible for their own deployment's content policies:

- **Model selection** — choose model sources and repositories aligned with your values (Ollama, HuggingFace, or private registries)
- **Node-side filtering** — implement content filtering at the node level if desired (outside the mesh protocol)
- **Identity requirements** — require wallet verification or other identity checks for certain sensitivity tiers
- **Acceptable use policies** — define and enforce your own terms for node participation

### Standard disclaimer

mlxMesh is open-source infrastructure software. The project maintainers:
- Do not control what content users generate through the network
- Do not endorse or take responsibility for third-party model sources
- Provide the coordination protocol as-is; operators deploy it at their own discretion
- Encourage responsible use and compliance with applicable laws in each jurisdiction

This position is consistent with other infrastructure projects (Tor, VPN providers, network routing software) — the project provides the plumbing, not the water that flows through it.

---

## Path to release (safe, secure, scalable)

Everything above is a working **testbed** — a full multi-region mesh you can run locally and drive from real Apple hardware. It is **not yet production-safe**. The work below is what stands between the current state and a public release, grouped by the property it protects. Ordered roughly by priority within each group.

### 🔒 Security — *blocks any public exposure*

| Item | Why it blocks release | Status |
|------|----------------------|--------|
| **TLS everywhere** (coordinator, directory, node reachability) | API keys and job payloads must not travel in plaintext | **Done** — coordinator + directory serve HTTPS via `--tls-cert`/`--tls-key` (TLS 1.2 floor); Go nodes trust it via `--tls-ca` (or `--tls-skip-verify` for dev); Apple clients use https + a local-networking-only ATS policy. **Coordinator→node dispatch is now TLS too**: a node opts in with its own `--tls-cert`/`--tls-key`, and the coordinator pins the exact certificate fingerprint recorded at that node's (Ed25519-signed) registration — TOFU pinning, not chain verification, since independently-operated nodes have no shared CA. Cert management is still manual (no ACME/auto-renew) |
| **Node-side pointer consumption** | The encrypted-pointer path was only half-built: the coordinator threaded the pointer but no node fetched/decrypted ciphertext | **Done** — `internal/payloadcrypto` (Go-native ECDH-P256 → HKDF-SHA256 → AES-256-GCM, byte-compatible with the Swift client) lets the assigned node decrypt the payload itself. A new `POST /v1/reserve-node` resolves the recipient node *before* encryption so privacy-mode jobs keep the coordinator's TPS-aware routing |
| **Secrets management** | API keys were stored and compared **in plaintext** in SQLite — a real, fixable vulnerability | **Done** — keys are now SHA-256-hashed at rest (only the hash is ever written or compared; the raw key exists only in the one-time `generate()` return value). TLS certs get a startup expiry warning (`WarnIfExpiringSoon`, 30-day window) across coordinator/directory/node. **Remaining:** node Ed25519 identity has no rotation by design (it's the node's permanent earnings anchor — rotating it would sever the trust chain, not improve it) |
| **Auth on read endpoints + abuse limits** | `/topology`, `/nodes`, `/balance` are unauthenticated; needs per-account quotas and read-endpoint auth | **Done (opt-in)** — `--protect-user-reads` gates the per-user endpoints (`GET /users/{id}/balance`, `GET /users/{id}/api-key`) so a caller needs the admin key or that user's own `oim_` key, closing balance-enumeration-by-user_id (aggregate reads `/topology`/`/nodes`/`/metrics` stay open by design for the public dashboard). `--user-quota-per-hour` adds a per-account request cap keyed on the *verified* user_id (not the spoofable `X-OIM-User-ID` header), so one account can't abuse the API from many IPs. The directory now has the coordinator's per-IP rate limiting too (previously it had none), and both servers gained `--trusted-proxy` so per-IP limits actually work behind the fronting nginx instead of collapsing every client into the proxy's single bucket. All opt-in like the rest of the hardening — a public deployment turns them on |
| **Input hardening / DoS** (task #53) | ~~Only per-IP rate limiting~~ | **Done** — 8 MiB request-body cap, global in-flight concurrency limit (503 + Retry-After), `ReadHeaderTimeout` slow-loris guard, and an SSRF allowlist on the payload fetch URL (blocks loopback + link-local/cloud-metadata). Remaining: upstream WAF/scrubbing for volumetric floods |
| **Outbound call resilience** (tasks #22, #24) | ~~A dropped connection silently failed a register/refresh; nothing bounded a node's own outbound call rate~~ | **Done** — `internal/httpx`: exponential backoff + jitter retry for transient node→coordinator failures (permanent 4xx fails fast), plus a client-side token-bucket limiter on outbound calls |
| **CORS granularity** (task #27) | ~~Origin allowlist only supported exact matches~~ | **Done** — `*.domain.tld` wildcard-subdomain matching, case-insensitive, with an explicit apex/lookalike-suffix test matrix |
| **Third-party security review** of the crypto + settlement paths | Wallet auth, attestation, and ledger debits are trust-critical | Not started — an internal multi-angle review of this session's diff (crypto/TLS trust boundary, secrets/auth, DoS/resource exhaustion) found the AES-GCM/HKDF/ECDH crypto and the TLS-pinning fail-closed logic sound, and fixed one real gap (identity-file permissions not tightened on rewrite — internal/identity/store.go). Several DoS claims from that pass were checked against the actual code and refuted (reservation creation never touches a node's in-flight counter, so it can't starve routing) rather than taken at face value. A later review pass found and fixed two more real issues: the node's payload-fetch SSRF guard only validated the initial URL, so a malicious pointer host could 302-redirect the fetch to the cloud-metadata endpoint or an internal port, or use DNS rebinding to resolve to a good IP at check time and a blocked one at dial time — both are now closed by `httpmw.SafeFetchClient` (per-redirect re-validation + a connect-time IP check in the dialer); and the federation-key bearer check used a non-constant-time `==` (a timing side channel on the secret), now `crypto/subtle.ConstantTimeCompare`. This is not a substitute for an outside reviewer, just cheaper due diligence before one. A reviewer-facing scope package now exists at [SECURITY.md](SECURITY.md) — assets, the five trust boundaries, the full cryptographic inventory, a threat→mitigation map, the internal-review baseline, documented design limits, and suggested reviewer priorities — plus a vulnerability-disclosure policy. **Remaining:** engaging and scheduling the actual external reviewer (lead time, not engineering) |

### 🛡️ Safety & correctness — *blocks trusting the numbers*

| Item | Why | Status |
|------|-----|--------|
| **Verified earnings — reconcile earn vs observed tokens** (task #51) | ~~Node self-reports and earns unverified~~ | **Done** — both fast-lane and background-lane earnings are now credited from the coordinator's OWN observed token count; `/job-outcome` is reputation-only and never credits, so a node cannot inflate earnings. Verified end-to-end (integration test) |
| **Coordination-device credit attribution** | ~~The iOS device ID regenerated on every launch, so it could never be linked to a wallet — participation announced and appeared on the map, but earnings had nowhere to land and stayed at 0 forever~~ | **Done** — the device ID now persists per-install (`DeviceIdentity.swift`); Account has a one-tap "Link this iPad's participation"; the coordinator stamps a server-assigned `region` on announce (a missing field previously threw and crashed the web map's shield panel); the "Pointers" stat now syncs from the coordinator's own served-pointer count instead of sitting dead at 0 |
| **Federated ledger authority** (task #52) | No consensus/staking/proof so a forked coordinator can't mint once the network federates (M7) | **Partially done, live in production** — coordinator identity + TOFU-pinned/allowlisted pod registration (`internal/directory.PinStore`) closes impersonation; signed, sequenced cross-pod ledger-event witnessing + a live-balance-vs-signed-history audit endpoint (`internal/federation`, `GET /federation/audit/{user_id}`) closes "no way to catch a rogue pod." Deployed to the live seed (pod-us + pod-eu each with distinct persisted identities, `--federation-key` witnessing enabled) and verified end-to-end: a 100-credit grant issued on pod-us was witnessed by pod-eu and audited (`claimed_balance: 100`, `witnessed_gross_credits: 100`, `consistent: true`). Still no BFT consensus or staking/slashing — see [Security model](#security-model--threat-analysis) item 3 |
| **Integration tests: coordinator ↔ node ↔ Exo** (task #18) | ~~Cross-process contract uncovered~~ | **Done** — subprocess integration suite (`go test -tags integration ./tests/`) spins up real coordinator+node+stub-exo and asserts the full money path (75/25 split, no double-credit), 402-gating, SSRF rejection, and metrics exposure |
| **Streaming (`stream: true`)** on `/v1/chat/completions` | Documented but unimplemented; interactive UX depends on it | **Done (fast lane)** — real SSE passthrough end-to-end (Exo → node → coordinator → client), each hop relaying chunks via `http.Flusher` as they arrive rather than buffering. Credit/debit accounting is unchanged — it reads the same observed-token count, now sourced from the trailing SSE usage frame instead of one JSON blob. Background lane intentionally stays buffered/polling (recurring jobs don't need it) |
| **Structured logging + metrics** (task #20) | ~~No observability~~ | **Done** — `GET /metrics/prometheus` exposes request/dispatch/credit/debit/rejection counters + live gauges (nodes, queue depth, coordination participants); `OIM_LOG_FORMAT=json` emits structured slog with typed money-event fields |
| **Maintainability & tooling** (tasks #21, #25, #26, #28) | ~~Large handler functions, magic numbers, no lint config, no perf baseline~~ | **Done** — coordination-reward logic extracted into a unit-testable `creditPointerHost()`; slow-loris timeout named; `.golangci.yml` added; allocation-bound perf regression tests on the metrics/pricing hot paths |
| **Code review of this session's diff** | An 8-angle review (correctness + reuse/simplification/efficiency/altitude/conventions) against v0.10→v0.11 | **Done** — found and fixed 2 real correctness bugs (streaming dispatch silently dropped the encrypted-pointer fields the buffered path forwards; a stream ending with no usage frame billed $0 with no signal), deduplicated the SSE relay loop shared by the node↔Exo and coordinator↔node hops into `internal/sse.Relay`, and untracked two compiled binaries (~19 MB) that had been accidentally committed at the repo root. One design note (TLS endpoint+fingerprint threaded as two loose strings instead of one struct) was logged as a follow-up and has since been fixed — dispatch now threads a single `NodeTarget{NodeID, Endpoint, TLSFingerprint}` (task #59) |
| **Ledger reconciliation & audit trail** | Debits log but there's no periodic balance-integrity check | **Done** — `Ledger.Reconcile()` audits the whole ledger for the credits≥debits invariant (per-user overdraft and orphan-debit detection, float-epsilon tolerant). A background loop runs it every 5 min (and once at boot, so a corrupt DB is caught on startup), logging loudly on any anomaly; `GET /admin/reconcile` (admin-key gated) returns the full report; and `oim_ledger_consistent`/`oim_ledger_anomalies`/`oim_ledger_{credits,debits,outstanding}` gauges expose it to Prometheus so an overdraft trips an alert, not just a log line |

### 📈 Scalability — *blocks growth past the seed*

**Scaling reality check:**

Fast lane is single-node per job by design (MoE sharding is a planner only, not wired into dispatch). Adding nodes doesn't make individual requests faster — it increases aggregate concurrent capacity. Per-job speed (~40 t/s) is a property of the selected node, not node count.

**Aggregate throughput scaling (assuming ~40 t/s avg/node, perfect load balancing):**

| Nodes | Aggregate t/s | Concurrent jobs/sec (at ~12.5s/job) |
|---|---|---|
| 100 | 4,000 | ~8 |
| 1,000 | 40,000 | ~80 |
| 10,000 | 400,000 | ~800 |
| 100,000 | 4,000,000 | ~8,000 |

**Real bottleneck: SQLite ledger write contention**

The original ceiling was a single coordinator's SQLite ledger (single-writer, correctness enforced by that one process's in-memory mutex — nothing a second coordinator process could safely share). Every job completion does 3+ writes (debit consumer, credit node, credit treasury) plus signature verification, so realistic sustained ceiling on SQLite alone is low hundreds to low thousands of job-completions/sec per coordinator.

**Done:** the ledger now also supports a Postgres backend (`--ledger-db-url`, `internal/settlement/ledger_postgres.go`) where correctness is enforced by the database itself — row locks (`SELECT ... FOR UPDATE`) on debits, a unique constraint on startup-grant claims — rather than by a process-local mutex, so multiple coordinator processes can safely share one Postgres instance without racing on the same user's balance. Verified directly: two real `oim-coordinator` processes racing to claim the same user's startup grant against one shared Postgres exactly-once-succeeded, and concurrent debit goroutines against a fixed balance never overdrew it. `--db-path`'s SQLite mode is unchanged and remains the default for a single coordinator. This is the prerequisite for "Coordinator HA," not HA itself — leader election and cross-coordinator request routing/failover are still not started.

Regional sharding (one coordinator per pod) also mitigates this independently of the ledger backend — 10,000 nodes across 20 coordinators is viable either way.

| Item | Why | Status |
|------|-----|--------|
| **M7 — federated directory** | Single centralized directory is a SPOF and a scale ceiling; `FederatedResolver`/`DHTResolver` are stubs | Stub (the ledger-authority half of M7 is now partially done — see the Security row above) |
| **Progressive decentralization** (task #49) | EC2 seed → network takes over "at parity"; needs the handoff logic + a parity metric | **Partially done, live in production** — coordinators (`--directory`), the web dashboard (`VITE_DIRECTORY_URL`), and the Apple clients now accept a comma-separated list of directory endpoints and fall back through them in order, so no single directory instance is a hard client-side dependency. `PodHealthDigest` now carries `real_node_count_approx`/`real_total_memory_gb`/`real_aggregate_toks_per_sec` alongside the existing totals (`internal/coordinator/registry.go` `HealthDigest`) — a real, live "parity" ratio (real capacity ÷ total capacity) instead of an undefined metric, currently reporting the honest 0% (the live seed's ~58 nodes are all simulated). **Still missing**: an actual second directory *instance* deployed, a defined parity threshold, and the automatic handoff logic itself (today this is observability, not automation) |

### 📊 Performance benchmarks — *honest positioning vs hosted APIs*

**Per-node throughput comparison:**

| System | Tokens/sec | Notes |
|--------|-----------|-------|
| mlxMesh (Mac Studio) | 30-50 | Consumer Apple Silicon, unmodified |
| Claude Sonnet | ~37 | Hosted API |
| Claude Haiku 3.5 | 65.2 | Hosted API |
| GPT-4.1 | ~55 | Hosted API |
| GPT-4o | 52-117 | Hosted API (varies by benchmark) |
| GPT-5 Pro (reasoning) | ~11 | Deliberately slow, reasoning-optimized |
| Gemini 3.5 Flash | 167-180 | Higher first-token latency |
| Groq (dedicated LPU) | 500+ | Purpose-built inference silicon |

**Key takeaways:**
- mlxMesh per-node throughput is competitive with hosted frontier APIs (Claude Sonnet, GPT-4.1/4o)
- The real gap is latency, not throughput: hosted APIs achieve 30-80ms first-token latency; mlxMesh has extra hop overhead
- Hosted providers win on aggregate scale via continuous batching across massive GPU fleets
- mlxMesh trades centralized-batching efficiency for zero per-token cost, no data leaving infrastructure, no third-party dependency

**Honest positioning:** mlxMesh doesn't beat OpenAI/Anthropic on scale or first-token latency, but achieves tokens-per-second parity with hosted frontier APIs using consumer hardware. Speculative decoding and prefix caching would push it past several hosted numbers on that specific axis.

### 🚀 Release engineering

| Item | Status |
|------|--------|
| **Public seed deploy** — EC2 coordinator + directory as the bootstrap (task #42) | **Done** — live at mlxmesh.net (pod-us + pod-eu + directory), running the M7-signed/pinned build with an Elastic IP |
| CI pipeline wiring | **Done** — `.github/workflows/ci.yml` runs on every push/PR: a `go` job (build, vet, `golangci-lint`, unit tests, and the `-tags integration` suite), a `dashboard` job (`npm ci && npm run build`, which typechecks via `tsc -b`), and a `swift` job (macOS runner, `xcodegen generate` + `xcodebuild` for the iOS/tvOS/watchOS schemes with `CODE_SIGNING_ALLOWED=NO` so it doesn't need the project's personal signing identity). `.golangci.yml` migrated to the v2 config format and the repo was brought to a clean `0 issues` baseline (misspellings, a few real slow-loris/file-permission gaps, dead unchecked-error patterns) rather than shipping lint wired to a repo that was never actually run through it |
| Signed release binaries + reproducible Docker images | **Mostly done** — `make release` (`scripts/build-release.sh`) produces byte-for-byte reproducible cross-platform binaries (darwin/linux × arm64/amd64) via `-trimpath -buildid=`, with a `SHA256SUMS` manifest (binary checksums verified stable across runs); `internal/version` stamps version/commit/date into every binary (`oim version`, coordinator/directory startup logs) and the Docker image (build args + OCI labels); see [RELEASING.md](RELEASING.md). **Remaining (operator):** provision a signing key and wire `cosign`/`minisign` over `SHA256SUMS` + the image — deliberately not automated since it needs a private key |
| App Store / TestFlight pipeline for the Apple apps | **Scaffolded** — the build side is already in CI (all three schemes compile via xcodegen). Added `fastlane/` (`Fastfile` with `preflight`/`beta` lanes reading all credentials from env, `Appfile`) and a manual-dispatch `.github/workflows/testflight.yml` that runs a no-upload preflight by default and gates the real `beta` upload on the operator having configured App Store Connect secrets. **Remaining (operator):** provision the ASC API key, signing certs/profiles, and App Store records — the parts that need an Apple Developer account and can't be committed |
| Runbook + incident/on-call docs; SLOs | **Done** — [RUNBOOK.md](RUNBOOK.md) (golden signals, deploy/rollback/scale/restart/secrets procedures grounded in the real EC2 topology, plus an incident-response playbook covering every failure this seed has actually hit — OOM, secret-as-directory, IP change, ledger anomaly, node churn, directory 429) and [SLOS.md](SLOS.md) (availability/latency/integrity objectives each tied to a real metric, alerting priorities, and an honest list of gaps that keep it a beta SLA) |

**Suggested sequencing for a first safe release:** auth on read endpoints + third-party security review (security floor) → ledger reconciliation/audit trail (trust the numbers) → EC2 seed (task #42) → M7 federation + progressive decentralization (scale past the seed).

---

## Repository layout

```
cmd/
  oim/             CLI + node agent entry point
  coordinator/     Pod coordinator server (M2) — routing, ledger, wallet, coordination + federation endpoints
  directory/       Global directory server (M4) — pod registration with TOFU pinning/allowlist
  stub-exo/        Fake Exo for simulation (incl. SSE streaming)
internal/
  protocol/        Wire types, crypto, job specs (+ payload-pointer fields, signed pod digests)
  exoadapter/      Thin HTTP client wrapping Exo (buffered + streaming)
  agent/           Node agent HTTP server (job endpoint, /detect, pointer fetch+decrypt)
  jobrunner/       Executes jobs against the local Exo (buffered + streaming fast lane)
  governor/        Resource caps and foreground check
  capability/      Live manifest assembly
  bench/           Tier benchmarking
  attestation/     Secure Enclave attestation verification
  identity/        Ed25519 (+ ECDH) keypair persistence — nodes and coordinators
  coordinator/     Registry, routers, queue, hint validation, coordination registry (M8)
  wallet/          Portable account identity: address derivation, challenge-response, device linking (M9)
  directory/       Resolver interface + implementations (M7 directory stubs) + PinStore pod pinning
  federation/      Signed cross-pod ledger-event witnessing + audit store (M7 ledger authority)
  settlement/      Division-order ledger, startup-grant PoW, credit hooks
  economics/       All pricing: cost/reward matrix, house edge, coordination reward
  payloadcrypto/   ECDH-P256 → HKDF-SHA256 → AES-256-GCM (byte-compatible with the Swift client)
  httptls/         TLS serving helpers, fingerprint-pinned clients, cert-expiry warning
  httpmw/          Shared HTTP middleware (rate limiting, body caps, SSRF guard, security headers)
  httpx/           Outbound HTTP resilience: retry/backoff + client-side rate limiting
  sse/             Shared SSE relay used by both streaming hops
  metrics/         Prometheus-format counters/gauges
  nodeconfig/      Node YAML config load + validation
config/
  node.example.yaml
tools/
  jobgen/          Simulated traffic generator (incl. --pointer-host mode)
  gen-compose/     Generates the multi-region docker-compose sim
  train-router/    Create ML pipeline for the on-device routing classifier
dashboard/         Web dashboard (React + Vite) — topology map, Node Setup, Try the Mesh
landing/           mlxmesh.net landing page (static)
scripts/           Dev CA + server cert generation (gen-dev-certs.sh)
.github/workflows/ CI: Go build/vet/lint/tests, dashboard build, Xcode builds
OIMDashboard/      SwiftUI apps — Shared / iOS / tvOS / watchOS (M8/M9 clients)
tests/             Protocol-, coordination-, and integration-level tests
```

---

## Privacy model

Three sensitivity tiers for jobs:

| Tier | Routing | Notes |
|------|---------|-------|
| `low` | Any reachable node | Embeddings, classification |
| `moderate` | Nodes with attestation consent | Default for chat |
| `high_requires_attestation` | Secure Enclave gate only | PII, confidential prompts |

Client telemetry is governed by [client-telemetry-schema-addendum.md](../meshAI/client-telemetry-schema-addendum.md). No prompt content, no raw embeddings, no biometric data ever leaves the device.

---

## Why no token?

Protocol credits are off-chain (stablecoin / fiat payment rails). Issuing a native token risks the Helium-style collapse pattern: token price speculation decouples from actual compute supply, then crashes when market sentiment shifts. Bootstrap grants are per-pod, keyed to *verified* capacity, and decay as earned revenue grows.

### Marketplace feasibility

The current ledger architecture does not support a credit marketplace. Building one would require:

1. **Credit transfer mechanism** - implement atomic transfers between accounts (debit A, credit B)
2. **External monetary value** - define pricing mechanism (credits ↔ fiat/crypto)
3. **Trust model** - either:
   - Keep centralized trust (operator acts as escrow), or
   - Move to a blockchain for trustless transfers (major architectural shift)
4. **Regulatory compliance** - KYC/AML if real money is involved

The ledger is intentionally centralized and append-only (SQLite), with no transfer capability. A marketplace would be a significant addition beyond the current scope.

---

## Future directions

### Non-Mac hardware support

**Exo on Linux/Windows/Android**

Exo itself is device-agnostic — Linux, Windows, Android, iPhone, and Raspberry Pi are all supported, using tinygrad as the backend when MLX isn't available (MLX and tinygrad interop for sharding). However, a practical caveat matters for homelab deployments: on Linux, Exo currently runs on CPU rather than GPU by default, with GPU acceleration still on the roadmap unless you build with explicit CUDA support (there's a CUDA/cuDNN path for NVIDIA boxes specifically, separate from the default). A Linux node with a beefy NVIDIA GPU sitting idle on CPU-only Exo is a real risk right now — check whether the CUDA build path is stable before counting on it for throughput.

Practically, M1/M2 Max Studios remain the fastest nodes by a wide margin (MLX + unified memory + GPU). A Windows/Linux box added today mostly helps with aggregate memory pool size, not speed, unless it has a strong discrete GPU and you get the CUDA path working.

**llama.cpp/Ollama integration**

Since mlxMesh is its own protocol (Ed25519 identity, dual-lane routing, credit accounting), it's not limited to Exo's node model. A potential path:

- Run llama.cpp or Ollama as a local inference server on the Windows/Linux box, exposing its OpenAI-compatible endpoint
- Write a thin adapter node in mlxMesh that wraps that endpoint — same node identity/attestation/credit-accounting semantics as MLX nodes, but the "inference backend" field points at the llama.cpp server instead of an MLX runtime

This is more work than plugging into Exo directly, but it fits the existing dispatch logic (power-of-two-choices, KV-cache-aware routing) without depending on Exo's Linux-CPU limitation — you get real GPU speed on non-Mac boxes via CUDA-accelerated llama.cpp instead of Exo's tinygrad/CPU path.

**Is it worth it?**

Worth it if the goal is raw memory pool for very large models (cheap way to add capacity) or heterogeneous demo value for mlxMesh (proving it's not Apple-only). Not worth it yet if the goal is throughput — until Linux GPU support in Exo matures, a non-Mac node bolted onto Exo directly is likely to be the slowest link and could even drag down tensor-parallel jobs. The adapter-node approach into mlxMesh's own protocol sidesteps this, since dispatch weighting is controlled directly.

---

## Contributing

Open Inference Mesh is open source. See [ARCHITECTURE 3.md](../meshAI/ARCHITECTURE%203.md) for the full design spec and milestone-by-milestone build plan.

Node identity is derived from an Ed25519 public key — your node ID is `SHA-256(pubkey)[:32]` in hex, never operator-chosen.

---

## License

This project is licensed under the **GNU Affero General Public License v3.0 (AGPL-3.0)**.

### Commercial Use

Commercial use of this software requires a separate commercial license. To obtain a commercial license, please contact us.

**What this means:**
- **Open source use**: Free to use, modify, and distribute under AGPL-3.0
- **Commercial use**: Requires a commercial license for:
  - Proprietary SaaS offerings using this software
  - Integration into commercial products without releasing source code
  - Use in enterprise environments without AGPL compliance

For commercial licensing inquiries, please contact: [licensing contact information]

### AGPL Summary

The AGPL-3.0 requires that:
- Source code modifications be made available to users of the software
- Network users (SaaS) have access to the source code
- Derivative works also be licensed under AGPL-3.0

See the [LICENSE](LICENSE) file for the full text.
