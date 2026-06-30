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
