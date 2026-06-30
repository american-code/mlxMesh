# CLAUDE.md — Open Inference Mesh

## Project identity
- **Protocol name:** Open Inference Mesh
- **Brand:** MeshAI (for iOS/watchOS/visionOS client apps)
- **CLI binary:** `oim`
- **Go module:** `github.com/open-inference-mesh/oim`
- **Go version:** 1.22

## Design doc rule
Source docs live in `/Users/melton/meshAI/`. When multiple files have the same base name with version numbers (e.g. `Architecture 2.md`, `Architecture 3.md`), **always use the highest-numbered file** — it is the current iteration.

## Core architecture (non-negotiable constraints)
- **No WAN dense-model pipeline-sharding** — 20–150 ms inter-hop latency kills sequential token passing; MoE expert-sharding is the only WAN-viable strategy for large models
- **No native token** — off-protocol payment rails (stablecoin/fiat) to avoid Helium-style token trap; `earned_referral` credit type is explicitly banned
- **Division-order accounting** — measured resource lines, not declared; grant vs earned balances must NEVER collapse to one number
- **Ed25519 node identity** — node_id = `SHA-256(pubkey)[:32]` hex; never operator-chosen
- **Bootstrap grants** — per-pod, keyed to VERIFIED capacity, decay as earned capacity grows
- Sensitivity tiers: `low` | `moderate` | `high_requires_attestation` (Secure Enclave gate)

## Exo integration
- Exo (`exo-explore/exo`) is treated as a **black-box HTTP API** at `http://localhost:52415`
- `oim` sits one layer above Exo — it does NOT replace or fork Exo
- Single-cluster LAN inference is Exo's job; cross-cluster WAN coordination is `oim`'s job

## Three-layer hierarchy
```
Global Directory (librarian)       M4 (centralized) → M7 (federated)
        │
Pod Coordinators (1 per region)    M2
        │
Node Agents (wrapping Exo)         M1 ✓
```

## Dual-lane routing
- **Fast lane** — interactive, resolver-routed, low-latency
- **Background lane** — recurring/batch, scheduler-routed, sticky-session (requires `RecurrenceSpec`)
- Fast-lane jobs MUST NOT have `Recurrence`; background-lane jobs MUST have it

## Directory evolution (pluggable Resolver interface)
1. Centralized (M4) — single librarian
2. Federated (M7) — multi-librarian
3. DHT — deferred (Sybil/eclipse risk not yet mitigated)

## Key packages
| Package | Purpose |
|---------|---------|
| `internal/protocol` | Wire types, crypto, job specs |
| `internal/exoadapter` | Thin HTTP client for Exo |
| `internal/governor` | Memory caps, foreground detection (platform-split via build tags) |
| `internal/capability` | Live manifest assembly from Exo state |
| `internal/bench` | Tier benchmarking (short/medium/long reference prompts) |
| `internal/identity` | Ed25519 keypair persistence at `~/.config/oim/node_identity.json` |
| `internal/coordinator` | Pod coordinator — NodeRegistry, ScoreForFastLane, DispatchFastLane, AssignBackgroundJob, ResolveForCycle, MeasurementStore, VerifyTierClaim, SpotCheckFastLane, StatisticalBaselineCheck, PlanMoEExpertAssignment, RouteTokenToExpertNode, DetectExpertLoadImbalance |
| `internal/jobrunner` | Node-side job execution (ExecuteFastLane, ExecuteBackgroundLane, RefuseIfConstrained) |
| `internal/agent` | Node agent lifecycle (register → serve jobs → heartbeat loop, ReportJobOutcome, SubmitBenchmarkResult) |
| `internal/directory` | Resolver interface, PodStore (TTL-based), Gossip (single-pair), CentralizedResolver (cache fallback) |
| `internal/settlement` | Division-order accounting, settlement records (Ed25519-signed), credit ledger (grant/earned split), grant decay, payment pointer validation |

## Build / test
```bash
/usr/local/go/bin/go build ./...        # clean build
/usr/local/go/bin/go test ./...         # all tests pass (75 tests)
/usr/local/go/bin/go build -o bin/oim ./cmd/oim
/usr/local/go/bin/go build -o bin/oim-coordinator ./cmd/coordinator
```

## M2 runtime: two-node quick start
```bash
# Terminal 1 — pod coordinator
./bin/oim-coordinator --pod-id pod-local --region us

# Terminal 2 — node 1 (must have Exo running)
./bin/oim node start --coordinator http://localhost:9000 --listen :8765 --cap 0.5

# Terminal 3 — node 2
./bin/oim node start --coordinator http://localhost:9000 --listen :8766 --cap 0.5

# Dispatch a fast-lane job (any OpenAI client works)
curl -X POST http://localhost:9000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"llama-3.2-3b","messages":[{"role":"user","content":"hello"}]}'

# Pod health
curl http://localhost:9000/health
```

## Platform notes
- `internal/governor/foreground_unix.go` — `//go:build darwin || linux` (uses `syscall.Syscall` + `TIOCGPGRP`)
- `internal/governor/foreground_other.go` — `//go:build !darwin && !linux` (returns true)
- Apple Silicon (M-series, unified memory) is the reference platform; `gopsutil` used for accurate macOS memory readings
- `crypto/go:Ed25519` from stdlib — no external crypto dep

## Milestone status
- **M1 DONE** — node agent: manifest, governor, bench, identity, CLI (`oim node status`, `oim bench run`)
- **M2 DONE** — pod coordinator: NodeRegistry (signature-verified, TTL decay), fast-lane routing (measured-TPS scoring, Secure Enclave gate, failover), background-lane sticky assignment (primary + N backups, cycle resolution with failover), `oim node start`, `oim-coordinator` server
- **M3 DONE** — verification layer: `SpotCheckFastLane` (probabilistic re-dispatch, content-length consistency), `StatisticalBaselineCheck` (3σ baseline drift detection), `VerifyTierClaim` (compares submitted benchmark vs claimed signature, detects tier fraud), `MeasurementStore` (per-node submitted benchmark store), `ReportJobOutcome` + `SubmitBenchmarkResult` (reputation client), coordinator endpoints `/nodes/{id}/benchmark-result`, `/nodes/{id}/job-outcome`, `/nodes/{id}/verify-tier`, agent re-bench loop (BenchInterval), 28 tests passing
- **M4 DONE** — centralized directory: `PodStore` (TTL-based pod digest store), `Gossip` (one-hop bootstrap-pair sync, no loop amplification), `CentralizedResolver` (tries all endpoints → falls back to cache on total outage), `oim-directory` server (`POST /pods/register`, `GET /pods`, `POST /gossip/digest`, `GET /health`), coordinator `--directory` flag with periodic reporting goroutine, 46 tests passing
- **M5 DONE** — settlement layer: `BuildDivisionOrder` (multi-line resource accounting, shrinkage), `CreateSettlementRecord` (Ed25519-signed, failed-verification records kept as evidence), `ValidatePaymentPointer` (format only, no fund custody), `Ledger` (append-only, grant-before-earned debit, `TotalOutstandingGrantLiability`), `VerifiedCapacityScore` (registry method, verified nodes only), `CurrentGrantMultiplier` + `IssueStartupGrant` (stepped decay by pod), coordinator endpoints `/settlement/records`, `/users/{id}/startup-grant`, `/users/{id}/balance`, 61 tests passing
- **M6 DONE** — MoE expert-shard planner: `PlanMoEExpertAssignment` (largest-remainder proportional assignment, memory-cap weighted, nodeID-deterministic), `RouteTokenToExpertNode` (top-K and single-expert routing decision formats, JSON float64 safe), `DetectExpertLoadImbalance` (2× per-expert average threshold, detect-only — rebalancing is explicitly out of scope per §6.3), 75 tests passing
- M7 stub — federated directory
