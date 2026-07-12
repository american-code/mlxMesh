# mlxmesh

Python client SDK for [mlxMesh](https://github.com/open-inference-mesh/oim) — a
distributed AI inference network with dual-lane routing, coordinator-verified
earnings, and an optional end-to-end encrypted payload path.

```bash
pip install mlxmesh
```

## Quick start

```python
from mlxmesh import MeshClient, Wallet

client = MeshClient("https://<coordinator>", wallet=Wallet.load_or_create())

response = client.chat("llama-3.2-3b", [{"role": "user", "content": "Summarize this document: ..."}])
print(response.content)
```

A billable call (`chat`, `stream_chat`, `submit_background_job`, ...) refuses
outright — before making any request — unless a credential is configured:
either a `wallet=` (see below) or a pre-existing `api_key=`/`user_id=` pair.
There is no anonymous/unmetered fallback path in this SDK; if you want the
coordinator to actually debit an account, a wallet is the way to get one.

## Wallets

A `Wallet` is a local Ed25519 keypair that proves ownership of a ledger
balance — portable across machines/processes and recoverable, unlike a
random per-session ID. The coordinator only ever sees the derived address
and, once per session, the public key + a signature; the private key never
leaves this process.

```python
from mlxmesh import MeshClient, Wallet

# First run: generates a new keypair and saves it to ~/.config/mlxmesh/wallet.json
# (0600 permissions). Later runs load the same one, so the address — and the
# balance tied to it — is stable across restarts.
wallet = Wallet.load_or_create()
print(wallet.address)  # "oim<64 hex chars>" — this is what the ledger keys balances on

client = MeshClient("https://<coordinator>", wallet=wallet)
client.claim_startup_grant()

# The first billable call authenticates transparently: it signs a coordinator
# challenge with the wallet's key and mints a session api_key — you never
# have to call this yourself, but you can:
client.authenticate()
print(client.api_key)  # "oim_..."

response = client.chat("llama-3.2-3b", [{"role": "user", "content": "..."}])
```

If the coordinator ever rejects `client.api_key` (expired, coordinator
restarted with a fresh in-memory store, etc.), the client re-authenticates
once automatically and retries — you don't need to handle 401s yourself as
long as a wallet is configured.

`Wallet.create()` generates a keypair without touching disk (you decide
if/where to persist it via `.save(path)`); `Wallet.load(path)` loads an
existing one and raises `FileNotFoundError` if there isn't one yet.

You can still pass a pre-existing `api_key=`/`user_id=` pair instead of a
wallet (e.g. a key you minted some other way) — the wallet flow is the
recommended default for new integrations, not the only supported one.

## Streaming

```python
for chunk in client.stream_chat("llama-3.2-3b", [{"role": "user", "content": "..."}]):
    if chunk.delta_content:
        print(chunk.delta_content, end="", flush=True)
    if chunk.is_usage_frame:
        print(f"\n({chunk.usage['completion_tokens']} tokens)")
```

Async variants (`achat`, `astream_chat`) are available on the same client.

## Account

```python
client.claim_startup_grant()   # mines the required proof-of-work nonce for you
balance = client.balance()
print(balance.total)
```

## Background lane

Fast lane (`chat`/`stream_chat`) and background lane are genuinely different
endpoints — background jobs are assigned once (sticky-session node selection)
then executed per recurrence cycle:

```python
from mlxmesh import RecurrenceSpec

job = client.submit_background_job(
    "llama-3.2-3b", job_id="daily-report", recurrence=RecurrenceSpec(interval_seconds=86400)
)
result = client.run_background_cycle(job, [{"role": "user", "content": "..."}])
```

## Model discovery

The coordinator itself doesn't expose `/topology` — that's the separate
directory service, which tracks which pods currently serve which models:

```python
from mlxmesh import MeshDirectory

directory = MeshDirectory("https://<directory>")
pods = directory.find_pods("llama-3.2-3b")
```

## Privacy mode (encrypted-pointer)

For sensitive payloads, encrypt client-side to a specific reserved node's key
instead of sending plaintext:

```python
reservation = client.reserve_node("llama-3.2-3b")
# host encrypt_payload's output somewhere the assigned node can fetch it —
# the SDK does not solve hosting, that's application-specific
response = client.submit_encrypted(reservation, messages, fetch_url="https://your-host/blob")
```

Not compatible with `stream=True` — a reservation always returns buffered.

## Errors

```python
from mlxmesh.errors import InsufficientCreditsError, NoCapacityError, NoCredentialError

try:
    client.chat(...)
except InsufficientCreditsError as e:
    print(f"need {e.required}, have {e.balance}")
except NoCredentialError:
    print("pass wallet=... or api_key=...+user_id=... to MeshClient(...)")
```

## Development

```bash
pip install -e ".[dev]"
pytest
```

The crypto interop and live-mesh integration tests build and run the real Go
binaries from this repo — they skip cleanly (not a failure) if the `go`
toolchain isn't on `PATH`.
