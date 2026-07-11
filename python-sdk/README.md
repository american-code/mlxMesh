# mlxmesh

Python client SDK for [mlxMesh](https://github.com/open-inference-mesh/oim) — a
distributed AI inference network with dual-lane routing, coordinator-verified
earnings, and an optional end-to-end encrypted payload path.

```bash
pip install mlxmesh
```

## Quick start

```python
from mlxmesh import MeshClient

client = MeshClient("https://<coordinator>", api_key="<your-api-key>", user_id="<your-user-id>")

response = client.chat("llama-3.2-3b", [{"role": "user", "content": "Summarize this document: ..."}])
print(response.content)
```

Setting `user_id` isn't optional plumbing — it's what makes the coordinator
actually debit your account. Without it, requests run in the anonymous/
unmetered path.

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
from mlxmesh.errors import InsufficientCreditsError, NoCapacityError

try:
    client.chat(...)
except InsufficientCreditsError as e:
    print(f"need {e.required}, have {e.balance}")
```

## Development

```bash
pip install -e ".[dev]"
pytest
```

The crypto interop and live-mesh integration tests build and run the real Go
binaries from this repo — they skip cleanly (not a failure) if the `go`
toolchain isn't on `PATH`.
