# mlxMesh

A distributed inference protocol that turns geographically-spread machines into a single, routable AI compute fabric — with strict privacy tiers, measured (not declared) performance accounting, and no native token. (Internal packages, the Go module path, and the `oim` CLI/binary names are unchanged.)

**Brand:** MeshAI &nbsp;|&nbsp; **Protocol:** Open Inference Mesh &nbsp;|&nbsp; **CLI:** `oim`

---

## What it is

Most distributed inference tools (e.g. [Exo](https://github.com/exo-explore/exo)) work within a single LAN cluster. `oim` adds a coordination layer *above* that: it federates multiple clusters across the internet into a routable mesh, with:

- **Dual-lane routing** — fast lane for interactive jobs (resolver-routed, low-latency), background lane for recurring/batch jobs (scheduler-routed, sticky-session)
- **MoE expert sharding** — the only WAN-viable strategy for large models (sequential token passing can't survive 20-150 ms inter-hop latency)
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

2. Estimate job cost:
   cost_estimate = (max_tokens / 1000) * tier_rate
   tier_rate: low=0.5, moderate=1.0, high_requires_attestation=5.0  (credits per 1k tokens)
   → 100-credit startup grant ≈ 100 typical calls (2k tokens each, moderate tier)

3. If total >= cost_estimate → dispatch
   Else → 402 Payment Required  {"error": "insufficient_credits", "balance": ..., "required": ...}

4. On completion: debit actual completion_tokens delivered (falls back to max_tokens if not reported)
```

To check balance programmatically before submitting:

```bash
curl https://<coordinator>/users/<user_id>/balance
# { "grant_balance": 100.00, "earned_balance": 0.00, "total": 100.00 }
```

To claim the one-time startup grant (100 credits — enough for ~50,000 tokens at the default rate):

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
> **Status:** server-side token streaming (`stream: true`) is **not yet implemented** on `/v1/chat/completions` — the coordinator currently buffers and returns the full completion. The example below is the target API shape; see [Path to release](#path-to-release-safe-secure-scalable). (The `/nodes/stream` SSE endpoint that powers the live dashboards *is* implemented.)

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
| Go | 1.22+ |
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

---

## CLI reference

```
oim node status     Report live capability manifest from local Exo instance
oim node start      Start node agent (registers with a coordinator, serves jobs)

oim bench run       Benchmark a model and save MeasuredSignature
oim bench compare   Compare claimed vs measured performance
```

To run the full local simulation mesh (2 regions, ~60 containers, live traffic + a
coordination participant) instead of a single node:

```bash
go run ./tools/gen-compose > docker-compose.yml   # regenerate if needed
docker compose up --build                          # heavy — needs several GB free
# Dashboards/apps point at the directory on :9100 and coordinators on :9000/:9001
```

---

## Architecture overview

```
Global Directory (librarian)         ← M4 (centralized) → M7 (federated, stubbed)
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
| M5 | **Done** | Division-order settlement ledger with SQLite persistence, startup grants with PoW |
| M6 | **Done** | MoE expert-shard planner with proportional assignment and load imbalance detection |
| M7 | **Stub** | Federated directory (multi-librarian) — `FederatedResolver`/`DHTResolver` are stubs; see [Path to release](#path-to-release-safe-secure-scalable) |

**Extended scope** (built beyond the original plan — see [Beyond original scope](#beyond-original-scope)):

| # | Status | Description |
|---|--------|-------------|
| M8 | **Done (client + coordinator plumbing)** | On-device routing + iOS coordination/security layer: on-device CoreML classifier, client-side payload encryption (P256 ECDH → AES-256-GCM), coordinator hint validation (escalate-only), coordination registry + served-pointer accounting, native iOS/tvOS/watchOS apps. **Gap:** node-side pointer *consumption* (fetch ciphertext + decrypt) is not yet built — the coordinator threads the pointer but no node fetches it. |
| M9 | **Done** | Portable wallet identity: Ed25519 account key, `oim…` address = ledger user_id, challenge-response auth, account-signed device linking for credit consolidation, iCloud-Keychain sync + Base32 recovery key. Cross-language crypto (CryptoKit ↔ Go) verified. |

---

### Beyond original scope

The original ARCHITECTURE spec defined a headless Go protocol (M1–M7). The following were added on top and are **not** in that spec — flagged here so the delta is explicit:

- **iOS as a coordination/security layer, not a compute node.** iOS/iPadOS cannot run Exo (Python+MLX, macOS/Linux only), so instead of forcing inference onto them, iOS devices classify on-device and host encrypted payload pointers. This is *additive* — with zero iOS devices the mesh routes exactly as M1–M7 defined.
- **Encrypted-pointer privacy path.** Client-side P256 ECDH + HKDF-SHA256 + AES-256-GCM; the coordinator sees only a metadata pointer (hash + fetch URL + ephemeral pubkey), never plaintext. Served-pointer counts are attributed per device.
- **Portable wallet (M9).** Credit consolidation + recovery across devices without a blockchain.
- **Native Apple apps** (`OIMDashboard/`) — live topology, "Try the mesh," network-load/backpressure, coordination layer, wallet, and a tvOS global-map view.
- **Simulation harness** — `tools/jobgen` (incl. `--pointer-host` mode) + `tools/gen-compose` produce a 60+ container multi-region mesh with continuous traffic and a live coordination participant, for device-free testing.
- **Operational hardening already landed** — SQLite persistence, write-path signing, rate limiting, configurable CORS, security headers, config validation, dynamic queue capacity.

---

## Path to release (safe, secure, scalable)

Everything above is a working **testbed** — a full multi-region mesh you can run locally and drive from real Apple hardware. It is **not yet production-safe**. The work below is what stands between the current state and a public release, grouped by the property it protects. Ordered roughly by priority within each group.

### 🔒 Security — *blocks any public exposure*

| Item | Why it blocks release | Status |
|------|----------------------|--------|
| **TLS everywhere** (coordinator, directory, node reachability) | API keys and job payloads must not travel in plaintext | **Partial** — coordinator + directory serve HTTPS via `--tls-cert`/`--tls-key` (TLS 1.2 floor); Go nodes trust it via `--tls-ca` (or `--tls-skip-verify` for dev); Apple clients use https + a local-networking-only ATS policy. **Remaining:** coordinator→node dispatch is still plaintext, and cert management is manual (no ACME/auto-renew yet) |
| **Node-side pointer consumption** | The encrypted-pointer path is only half-built: the coordinator threads the pointer but no node fetches/decrypts ciphertext, so "privacy mode" isn't end-to-end yet | Not started (M8 gap) |
| **Secrets management** | API keys / signing keys live in SQLite + local files; needs a real secret store and key rotation | Partial |
| **Auth on read endpoints + abuse limits** | `/topology`, `/nodes`, `/balance` are unauthenticated; add per-account quotas and external-facing rate limits (task #24) | Partial |
| **Input hardening / DoS** | Request size caps, timeouts, and payload-pointer SSRF guards (a fetch URL is attacker-controlled) | Not started |
| **Third-party security review** of the crypto + settlement paths | Wallet auth, attestation, and ledger debits are trust-critical | Not started |

### 🛡️ Safety & correctness — *blocks trusting the numbers*

| Item | Why | Status |
|------|-----|--------|
| **Integration tests: coordinator ↔ node ↔ Exo** (task #18) | Unit tests are green, but the cross-process contract isn't covered end-to-end | Not started |
| **Streaming (`stream: true`)** on `/v1/chat/completions` | Documented but unimplemented; interactive UX depends on it | Not started |
| **Retry / backoff on inter-service HTTP** (task #22) | A single transient failure currently drops a job | Not started |
| **Structured logging + metrics** (task #20) | No observability into routing decisions, debit races, or queue behavior in prod | Not started |
| **Ledger reconciliation & audit trail** | Debits log but there's no periodic balance-integrity check | Not started |

### 📈 Scalability — *blocks growth past the seed*

| Item | Why | Status |
|------|-----|--------|
| **M7 — federated directory** | Single centralized directory is a SPOF and a scale ceiling; `FederatedResolver`/`DHTResolver` are stubs | Stub |
| **Progressive decentralization** (task #49) | EC2 seed → network takes over "at parity"; needs the handoff logic + a parity metric | Not started |
| **Coordinator HA** | One coordinator per region with no failover or shared state | Not started |
| **Ledger beyond SQLite** | SQLite won't survive multi-coordinator write load; needs Postgres/managed store | Not started |
| **Load & perf regression tests** (task #28) | No baseline to catch throughput regressions | Not started |

### 🚀 Release engineering

| Item | Status |
|------|--------|
| **Public seed deploy** — EC2 coordinator + directory as the bootstrap (task #42) | Not started |
| CI: `golangci-lint` (task #26) + the Swift build/typecheck in a pipeline | Not started |
| Signed release binaries + reproducible Docker images | Not started |
| App Store / TestFlight pipeline for the Apple apps | Not started |
| Runbook + incident/on-call docs; SLOs | Not started |

**Suggested sequencing for a first safe release:** TLS + node-side pointer consumption + input hardening (security floor) → integration tests + observability (trust the numbers) → EC2 seed (task #42) → M7 federation + progressive decentralization (scale past the seed).

---

## Repository layout

```
cmd/
  oim/             CLI + node agent entry point
  coordinator/     Pod coordinator server (M2) — routing, ledger, wallet + coordination endpoints
  directory/       Global directory server (M4)
  stub-exo/        Fake Exo for simulation
internal/
  protocol/        Wire types, crypto, job specs (+ payload-pointer fields)
  exoadapter/      Thin HTTP client wrapping Exo
  governor/        Resource caps and foreground check
  capability/      Live manifest assembly
  bench/           Tier benchmarking
  identity/        Ed25519 keypair persistence
  coordinator/     Registry, routers, queue, hint validation, coordination registry (M8)
  wallet/          Portable account identity: address derivation, challenge-response, device linking (M9)
  directory/       Resolver interface + implementations (M7 stubs)
  settlement/      Division-order ledger
config/
  node.example.yaml
tools/
  jobgen/          Simulated traffic generator (incl. --pointer-host mode)
  gen-compose/     Generates the multi-region docker-compose sim
  train-router/    Create ML pipeline for the on-device routing classifier
OIMDashboard/      SwiftUI apps — Shared / iOS / tvOS / watchOS (M8/M9 clients)
tests/             Protocol- and coordination-level tests
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
