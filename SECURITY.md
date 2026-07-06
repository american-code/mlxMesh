# Security Policy & Review Package

This document scopes mlxMesh for an external security review and tells anyone
who finds a vulnerability how to report it. It is the reviewer's map: assets,
trust boundaries, the cryptographic inventory, and where the trust-critical code
lives. The [README security model](README.md#security-model--threat-analysis)
has the narrative; this is the structured scope.

## Reporting a vulnerability

Email **jacob@paydirt.solutions** with details and a reproduction. Please do
**not** open a public issue for a security bug. Expect an acknowledgement within
a few business days. There is no bug-bounty program yet; credit is given in the
release notes unless you prefer otherwise.

Signed releases: `dist/SHA256SUMS` is signed at release time (see
[RELEASING.md](RELEASING.md)). **TODO (operator):** publish the signing public
key here so downloads can be verified against it.

## What this system is

A credit-based, no-native-token distributed inference network. The security
question that dominates everything: **if the client and node code are open and
forkable, what stops someone minting credits, spoofing the system, or knocking
it over?** The review should judge the answer against the code, not the prose.

## Assets (what an attacker wants)

| Asset | Where it lives | Why it matters |
|-------|----------------|----------------|
| **Credit ledger** | coordinator SQLite (`internal/settlement`) | The money. Minting or double-spending here is the top risk. |
| **Node earnings attribution** | coordinator-observed token counts → credits | A node lying about work done to inflate earnings. |
| **Coordinator identity key** (Ed25519) | `internal/identity`, per-pod file | Signs pod digests + federation events; impersonation risk. |
| **Node identity key** (Ed25519) | `internal/identity`, per-node file | The node's permanent earnings/reputation anchor. |
| **Wallet account key** (Ed25519) | client device only (never sent) | Proves ownership of a ledger balance across devices. |
| **Admin / federation bearer keys** | `/etc/mlxmesh/*`, coordinator flags | Full write access / cross-pod ledger pull. |
| **Encrypted job payloads** | client → node, pointer-fetched | User prompt confidentiality (privacy tiers). |

## Trust boundaries (where to focus)

1. **Client ↔ Coordinator.** Untrusted clients. Credit gate, write-path Ed25519
   signatures, API-key auth, rate limits, per-account quotas, read-endpoint
   protection. Entry: `cmd/coordinator/main.go` (handlers + `authMiddleware`).
2. **Node ↔ Coordinator.** Independently-operated, semi-trusted nodes. Node
   identity signing, coordinator-observed earnings (not node self-report),
   TOFU TLS-fingerprint pinning, tier spot-checks. Entry:
   `internal/coordinator/{router,verification,registry}.go`.
3. **Coordinator ↔ Coordinator (federation).** Peer pods witness each other's
   signed ledger events; a rogue pod's balance claim is auditable against its
   own signed history. **Not** BFT consensus — see Known limitations. Entry:
   `internal/federation`.
4. **Coordinator ↔ Directory.** Directory is a transport, not an authority;
   TOFU-pinned/allowlisted signed pod registration. Entry:
   `internal/directory/pinning.go`, `cmd/directory/main.go`.
5. **Node ↔ arbitrary payload host (SSRF surface).** A node fetches a
   client-supplied pointer URL. Guarded by an allowlist re-checked on every
   redirect hop + a connect-time IP check. Entry: `internal/httpmw/ssrf.go`,
   `internal/agent/agent.go` (`fetchAndDecryptPayload`).

## Cryptographic inventory (audit these carefully)

| Primitive | Use | Location |
|-----------|-----|----------|
| Ed25519 | node/coordinator/wallet identity + all write-path signatures | `internal/protocol/crypto.go`, `internal/identity` |
| ECDH P-256 → HKDF-SHA256 → AES-256-GCM | encrypted payload pointers (client ↔ node) | `internal/payloadcrypto` (Go) ↔ `OIMDashboard/.../PayloadEncryption.swift` |
| SHA-256 | node ID derivation, API-key hashing at rest, PoW | `internal/protocol/crypto.go`, `cmd/coordinator` `hashAPIKey`, `internal/settlement/pow.go` |
| SHA-256 cert-fingerprint pinning (TOFU) | coordinator→node TLS | `internal/httptls`, `internal/coordinator/router.go` |
| Secure Enclave P-256 (CGO, opt-in) | hardware attestation for HIGH-sensitivity jobs | `internal/attestation/enclave_darwin.go` |
| `crypto/subtle.ConstantTimeCompare` | admin + federation bearer comparison | `cmd/coordinator/main.go` |

Cross-language crypto compatibility (Go ↔ Swift CryptoKit) is a specific
audit target: the AES-GCM/HKDF payload path and the wallet challenge-response
must interoperate exactly, and any drift is a correctness *and* security bug.

## Threat model → mitigation map

| Adversary / threat | Mitigation | Verify against |
|--------------------|-----------|----------------|
| Forked client/node mints credits | Ledger is coordinator-authoritative; no client/node writes it directly | `internal/settlement` |
| Node inflates its earnings | Earnings credited from coordinator-*observed* tokens; `/job-outcome` is reputation-only | `router.go`, `verification.go` |
| Sybil grant farming | Startup grant requires PoW (18 bits) + per-IP hourly limit; idempotent | `internal/settlement/pow.go` |
| Coordinator impersonation (federation) | Signed pod digests + TOFU/allowlist pinning at the directory | `internal/directory/pinning.go` |
| Rogue pod claims unbacked balance | Cross-pod signed-event witnessing + `/federation/audit` invariant | `internal/federation` |
| Balance enumeration by user_id | `--protect-user-reads` (opt-in ownership auth on per-user reads) | `authorizeUserRead` |
| API abuse from many IPs | Per-account quota keyed on verified user_id | `authMiddleware` |
| SSRF via payload fetch (redirect / DNS rebind) | Per-redirect re-validation + connect-time IP check | `internal/httpmw/ssrf.go` |
| Volumetric DoS | Body cap, global concurrency cap, per-IP rate limit, slow-loris timeout | `internal/httpmw` |
| Credential timing side-channel | Constant-time bearer/admin compare | `cmd/coordinator/main.go` |
| Ledger corruption / overdraft bug | Periodic reconciliation + `oim_ledger_consistent` gauge | `internal/settlement/reconcile.go` |
| Rate-limit evasion via forged `X-Forwarded-For` | XFF honored ONLY when the direct peer is a configured `--trusted-proxy`; otherwise the direct peer IP is used. Misconfiguration risk: a too-broad trusted-proxy range makes XFF forgeable; none behind nginx collapses all clients into the proxy's single bucket | `internal/httpmw/clientip.go` |
| Man-in-the-middle | TLS 1.2 floor when TLS is enabled. Client-facing TLS terminates at nginx on the seed; coordinator→node TLS is opt-in (`--tls-cert`) and enabled on the live fleet, not structural | `internal/httptls` |

## Internal review already performed (not a substitute for external)

Multiple internal multi-angle passes have run against recent diffs and found +
fixed real issues, documented so the external reviewer can start from a known
baseline rather than re-deriving it:

- **SSRF redirect + DNS-rebinding bypass** in the node payload fetch — fixed
  (`httpmw.SafeFetchClient`: per-redirect re-validation + dialer `Control` IP
  check). Tests: `internal/httpmw/ssrf_test.go`, `clientip_test.go`.
- **Non-constant-time bearer comparison** (federation + admin keys) — fixed with
  `crypto/subtle`.
- **Directory missing the coordinator's DoS floor** — fixed (body cap +
  concurrency + per-IP limit added).
- **Identity-file permissions not tightened on rewrite** — fixed
  (`internal/identity/store.go`).
- Several **speculative DoS claims refuted against the code** (e.g. reservation
  churn never touches a node's in-flight counter, so it can't starve routing).

## Known limitations (in scope to confirm, not to "find")

These are deliberate, documented design boundaries — a reviewer should confirm
they are what they claim, not report them as surprises:

- **No BFT consensus / staking.** Federation is PKI-based witnessing among a
  curated/TOFU-pinned set of pods. A *compromised* pod (not merely impersonating)
  can misbehave until a peer audits it; there's no automatic quarantine. The
  no-native-token design means there's no stakeable asset for slashing.
- **Attestation is opt-in and self-declared unless Secure-Enclave-backed.** The
  `HasSecureEnclave` bool is never trusted for routing; only coordinator-verified
  enclave attestation gates HIGH-sensitivity jobs.
- **Read-endpoint protection and per-account quotas are opt-in flags.** A public
  deployment must enable them; the sim/dev default is open.
- **Single coordinator per region, SQLite ledger.** No HA/failover yet; a
  managed datastore + coordinator HA are on the release path.

## Suggested reviewer priorities

1. The settlement ledger + credit/debit paths (money integrity, double-spend,
   overdraft, reconciliation soundness).
2. The cross-language payload crypto (Go ↔ Swift AES-GCM/HKDF/ECDH).
3. The wallet challenge-response + device-linking auth.
4. `authMiddleware` and the self-authenticating-write allowlist (a too-broad
   entry there defeats the write gate).
5. The federation witnessing/audit invariant under adversarial event histories.
