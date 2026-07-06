# mlxMesh Service Level Objectives

SLOs for the trusted-operator beta. They are deliberately modest — a
single-instance seed cannot honestly promise four-nines. Each objective names
the **metric that measures it** so it is observable, not aspirational, and an
**error budget** so "how much downtime is acceptable" is a number, not a vibe.

These are beta targets. Numbers tighten once coordinator HA and a managed
datastore land (both are post-beta — see the README release path).

## Objectives

| # | Objective | Target (28-day window) | Measured by |
|---|-----------|------------------------|-------------|
| 1 | **Coordinator availability** — `GET /health` returns 200 | 99.0% | external uptime check per pod; error budget ≈ 7h/28d |
| 2 | **Directory availability** — `GET /health` returns 200 | 99.0% | external uptime check |
| 3 | **Fast-lane dispatch success** — a credited request gets a node and a reply (not 503) | 99.0% of dispatch attempts | `oim_rejections_total{reason="..."}` vs `oim_http_requests_total` on `/v1/chat/completions` |
| 4 | **Fast-lane latency** — coordinator overhead (excludes node inference time) | p95 < 250 ms | `oim_latency_ms` tag on responses / request histogram |
| 5 | **Ledger integrity** — the books reconcile | 100%, no error budget | `oim_ledger_consistent == 1` and `oim_ledger_anomalies == 0` |
| 6 | **Credit-gate correctness** — no request is served that wasn't paid for, none double-charged | 100%, no error budget | integration suite (75/25 split, no double-credit) + item 5 reconciliation |

Objectives 5 and 6 are **hard invariants**, not budgeted SLOs: a single
violation is an incident, because they are the difference between a credit
system and a free-money bug. Availability/latency (1–4) get error budgets
because brief unavailability is recoverable; a minted credit is not.

## Alerting

Wire these to whatever notifies the on-call (see RUNBOOK.md → On-call). Minimum
set, in priority order:

1. **`oim_ledger_consistent == 0` for > 1 scrape** → page immediately. This is
   the money invariant; treat as a potential exploit until proven a bug.
   Response: RUNBOOK.md → "LEDGER ANOMALY".
2. **Any `/health` non-200 for > 2 min** → page. Response: RUNBOOK.md →
   endpoint-down / OOM.
3. **`oim_queue_depth` sustained near its cap, or 503 rate climbing** → warn;
   capacity/backpressure, not necessarily an outage.
4. **Host memory available < 150 MB sustained** → warn; the box is
   memory-constrained and this precedes the OOM failure mode. Never start a
   build in this state.
5. **TLS cert within 14 days of expiry** (the server already logs at 30) → warn.

## What is NOT yet in place (honest gaps)

These are why this is a *beta* SLA, not a production one — each is a known
release-path item, not a surprise:

- **No coordinator failover.** One coordinator per region; if it's down, that
  region is down for the duration. This caps objectives 1/3 structurally — HA is
  required to promise more than ~99%.
- **No managed datastore.** SQLite on one host; a disk/host loss is a restore
  from backup, not a seamless failover. **Backups:** the ledger/identity data
  volumes must be snapshotted regularly (define cadence before real users) —
  today this is manual.
- **Single-maintainer on-call.** No rotation or secondary escalation yet.
- **No external synthetic monitoring wired.** The Golden-signals checks in the
  runbook are manual; objectives 1–4 need an external uptime monitor
  (e.g. a hosted check) actually configured and pointed at the endpoints to be
  measured continuously rather than spot-checked.

Closing the first two is the coordinator-HA + Postgres work; closing the last
two is operational setup that should precede opening the beta to real users.
