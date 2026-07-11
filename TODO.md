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
- **No visible repo** — "GitHub — coming soon" appears twice on a page whose pitch is built on "open source, AGPL-3.0, verify cryptographically." For engineers doing technical diligence, an open-source claim with no visible source is a credibility gap. Either soft-pedal open-source framing until repo is public, or ship a read-only mirror/tag now. **Not an engineering task** — publishing the repo is a business/timing decision for the operator, not something to implement.
- ✅ **"Live dashboard numbers are blank" — stale, not a current bug.** Verified live: `directory.mlxmesh.net/topology` returns real, non-zero pod data (2 pods, 60 nodes, ~2TB memory, ~4000 tok/s), CORS is open, and `landing/index.html`'s existing fetch script renders it correctly (confirmed in-browser). This note predates the seed going live. The script's "leave placeholders rather than invent a number" fallback (its own code comment) is a deliberate, correct design choice consistent with the page's "measured, not declared" pitch — seeding fake numbers on fetch failure, as this item originally suggested, would contradict that.

**Highest-leverage additions (priority order):** — attempted this session, mixed outcome:
1. ~~Code snippet above the fold~~ — added, then removed per design feedback ("does not need to be there, looks bad").
2. ~~Hosted-API benchmark comparison~~ — added, then removed per design feedback ("not pretty").
3. ~~Topology dashboard screenshot~~ — added (dashboard.png + iphone.png), then removed per design feedback ("looks horrible, those need to be meticulously described and designed").
4. ✅ **Reconsider primary CTA** — added a secondary, softer CTA ("Read why this exists ↓", scrolls to `#why`) next to "Launch Dashboard." This one landed fine.
5. **Follow-up capture** — not attempted. Needs a decision first (what actually receives the email — no backend/service exists today for this site), and given how #1–3 went, this is exactly the kind of visual/UX addition that needs the same dedicated design pass, not another inline attempt.

**Takeaway:** quick inline edits aren't meeting the bar for this page's visual content (screenshots, code blocks, tables) — three of five items got reverted. Content/data-driven fixes (the stats-strip verification) and simple structural additions (the secondary CTA) went fine. Future visual work here should get a real design pass, not ad hoc styling.
