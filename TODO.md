# Internal TODO List

This document tracks internal work items that are not exposed in the public README. These are for internal planning and tracking purposes only.

## Treasury & Admin Features

- ✅ **Treasury balance monitoring** — `oim_treasury_balance_x1000` gauge (`GET /metrics/prometheus`, refreshed live each scrape from `ledger.GetBalance(economics.TreasuryAccount)` — matches the existing `oim_ledger_*`/`oim_capacity_*` scrape-time-refresh pattern rather than a push-on-every-write model) and `oim_coordination_reward_skipped_total{reason="treasury_insufficient"}` (incremented in `creditPointerHost` exactly where the floor check used to silently skip the treasury debit — the participant is still credited, unchanged). Alert guidance in SLOS.md ties the threshold to your deployment's own trailing coordination-spend rate rather than a fixed number, since volume is deployment-specific. Covered by 3 new unit tests in `cmd/coordinator/treasury_test.go`.
- **Admin panel with Ed25519 BDFL key authentication** — Build admin panel in web dashboard that only shows admin menu item after Ed25519 admin key authentication (dedicated BDFL key, separate from personal wallet keys). Admin features include treasury balance view, reconciliation reports, node management, operational controls, and manual treasury credit injection (with audit logging, reason field, and rate limits). Manual treasury credits are acceptable since credits have no external monetary value and the real constraint is compute capacity, not credit supply. The BDFL key is generated once during initial deployment and stored securely, similar to coordinator identity keys.

## Scalability

- **Coordinator HA** — One coordinator per region with no failover or shared state. Not started.
- **Ledger beyond SQLite** — SQLite won't survive multi-coordinator write load; needs Postgres/managed store. Not started.

## Performance Optimizations

- **Speculative decoding on node side** — Directly attacks 30-50 t/s bottleneck; MLX-native speculative decoding achieves 2-3x speedups while maintaining exact output distribution equivalence (lossless). Gemma 4 MTP speculative decoding took M4 Max from ~58 to ~112 tok/s. Drop-in inference-engine upgrade, no protocol changes needed.
- **Prefix/KV-cache-aware routing** — Single biggest win for cache locality. Modern MLX inference gets massive speedups from prefix caching (21.7s → 0.78s on cached queries, 5.8x speedup on shared prompt prefixes). Current TPS-greedy router doesn't guarantee similar prompts land on same node. Fix: prefix-aware consistent hashing so similar prompts land on same/nearby workers, preserving cache locality.
- ✅ **Power-of-two-choices for fast-lane dispatch** — `internal/coordinator/router.go`'s `rankCandidates` now samples 2 of the top-`powerOfTwoWindow` (3) score-sorted candidates and promotes the better-scored to primary, instead of always the single global argmax — breaks the herding failure mode (concurrent requests evaluated against the same registry snapshot no longer all agree on one target before in-flight load can react) while bounding quality loss to "the window's weakest member," not a random node from the whole fleet. The deterministic best-first order is kept for the retry/fallback tail, since a dispatch *failure* (not load) is what fallback recovers from. Covered by 5 new unit tests in `router_test.go` (herding-fix variance, window bound, fallback-order stability).
- **Kademlia DHT for M7 directory federation** — `FederatedResolver`/`DHTResolver` are stubbed. Kademlia enables efficient node discovery and data lookup in decentralized P2P networks using XOR distance, O(log n) messages to locate any key/node, highly resilient to churn. Solves discovery, not ledger consensus (those stay separate concerns).

## Client SDKs

- ✅ **Python SDK** — `python-sdk/` (package `mlxmesh`). `MeshClient` covers fast-lane chat (buffered + streaming, sync + async), background-lane job submission (`submit_background_job`/`run_background_cycle` — a genuinely different endpoint set from fast lane), balance + startup-grant (PoW nonce mined automatically), and the privacy-mode encrypted-pointer flow (`reserve_node`/`submit_encrypted`, ECDH-P256→HKDF-SHA256→AES-256-GCM, verified byte-compatible with the real Go `internal/payloadcrypto` via a cross-language interop test). `MeshDirectory` covers model discovery. Unit + live end-to-end tests (real coordinator/stub-exo/node binaries) in `python-sdk/tests/`. **Remaining (operator):** reserve the `mlxmesh` name and actually publish to PyPI — needs an account/credentials, not engineering.
- ✅ **Swift SDK** — `swift-sdk/` (SwiftPM library `MeshKit`, iOS 16+/macOS 13+/tvOS 16+/watchOS 9+). Same surface as the Python SDK (`MeshClient` actor, `MeshDirectory`), plus `PayloadEncryption` adapted from `OIMDashboard/iOS/Crypto/PayloadEncryption.swift`. Its cross-language interop test caught a real, previously-unknown bug in that very file — CryptoKit's `.rawRepresentation` for the ephemeral P-256 key is the compact 64-byte form, not the 65-byte SEC1 form Go's `crypto/ecdh` requires — silently breaking every real encrypted-pointer job end to end; fixed in both the new SDK and the original dashboard file. `swift test` passes (21/21); confirmed `OIMDashboard`/`OIMMenuBar` still build clean. **Remaining (operator):** publish via Swift Package Manager (tag a release) — needs repo/release setup, not engineering.

## Website & Marketing

**Critical gaps:**
- **No visible repo** — "GitHub — coming soon" appears twice on a page whose pitch is built on "open source, AGPL-3.0, verify cryptographically." For engineers doing technical diligence, an open-source claim with no visible source is a credibility gap. Either soft-pedal open-source framing until repo is public, or ship a read-only mirror/tag now.
- **Live dashboard numbers are blank** — Nodes online, Regions, Committed memory, Aggregate tok/s all render as em-dashes. First-time visitor's impression of "a live compute fabric" is an empty dashboard. Seed with simulated fleet stats so it never looks dead.

**Highest-leverage additions (priority order):**
1. **Code snippet above the fold** — Working `curl .../v1/chat/completions` block converts far better than prose describing dual-lane routing
2. **Hosted-API benchmark comparison** — "Tokens/sec parity with Claude Sonnet and GPT-4.1, on a consumer Mac Studio, for the cost of electricity" is a compelling, concrete claim
3. **Topology dashboard screenshot/screen-recording** — Live map is most visually distinctive feature; screenshots convert better than descriptions
4. **Reconsider primary CTA** — "Launch Dashboard" as only CTA asks cold visitor to jump into live operational tool with zero context. Add secondary, softer CTA ("Read the whitepaper" or 90-second demo video)
5. **Follow-up capture** — With GitHub "coming soon" and pre-launch audience, no way to capture visitors who come back later. Even a plain email field solves this cheaply
