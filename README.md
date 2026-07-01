# Open Inference Mesh (`oim`)

A distributed inference protocol that turns geographically-spread machines into a single, routable AI compute fabric — with strict privacy tiers, measured (not declared) performance accounting, and no native token.

**Brand:** MeshAI &nbsp;|&nbsp; **Protocol:** Open Inference Mesh &nbsp;|&nbsp; **CLI:** `oim`

---

## What it is

Most distributed inference tools (e.g. [Exo](https://github.com/exo-explore/exo)) work within a single LAN cluster. `oim` adds a coordination layer *above* that: it federates multiple clusters across the internet into a routable mesh, with:

- **Dual-lane routing** — fast lane for interactive jobs (resolver-routed, low-latency), background lane for recurring/batch jobs (scheduler-routed, sticky-session)
- **MoE expert sharding** — the only WAN-viable strategy for large models (sequential token passing can't survive 20-150 ms inter-hop latency)
- **Division-order accounting** — measured resource lines, not declared promises; credits from bootstrap grants decay as earned capacity grows
- **Sensitivity tiers** — LOW / MODERATE / HIGH_REQUIRES_ATTESTATION (Secure Enclave gate on Apple Silicon)
- **Ed25519 node identity** — derived from public key, never operator-chosen

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

**Streaming pattern** — useful for long-running summarization where you want partial results:

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

**Webhook / async pattern** (planned Milestone 5) — submit a job with a `callback_url`; the coordinator POSTs the completed response to your endpoint when done. This is the target pattern for fire-and-forget batch pipelines.

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

## CLI reference

```
oim node status     Report live capability manifest from local Exo instance
oim node start      Start node agent (Milestone 2)

oim bench run       Benchmark a model and save MeasuredSignature
oim bench compare   Compare claimed vs measured performance (Milestone 3)
```

---

## Architecture overview

```
Global Directory (librarian)      ← Milestone 4 (centralized) → 7 (federated)
        │
        ▼
Pod Coordinators (1 per region)   ← Milestone 2
        │
        ▼
Node Agents (wrapping Exo)        ← Milestone 1 ✓
```

### Milestones

| # | Status | Description |
|---|--------|-------------|
| M1 | **Done** | Node agent: manifest assembly, resource governor, bench, Ed25519 identity |
| M2 | Stub | Pod coordinator: registry, fast-lane router, background scheduler |
| M3 | Stub | Spot-check verification, tier-claim validation |
| M4 | Stub | Centralized global directory |
| M5 | Stub | Division-order settlement ledger |
| M6 | Stub | MoE expert-shard planner |
| M7 | Stub | Federated directory (multi-librarian) |

---

## Repository layout

```
cmd/
  oim/             CLI entry point
  coordinator/     Pod coordinator server (M2)
  directory/       Global directory server (M4)
internal/
  protocol/        Wire types, crypto, job specs
  exoadapter/      Thin HTTP client wrapping Exo
  governor/        Resource caps and foreground check
  capability/      Live manifest assembly
  bench/           Tier benchmarking
  identity/        Ed25519 keypair persistence
  coordinator/     Registry, router, verification stubs
  directory/       Resolver interface + implementations
  settlement/      Division-order ledger stubs
config/
  node.example.yaml
tests/             Protocol-level tests
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

TBD — Apache 2.0 intended.
