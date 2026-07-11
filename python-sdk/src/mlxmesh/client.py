"""MeshClient — the coordinator-facing client (fast lane, background lane,
account/balance, and the privacy-mode encrypted-pointer flow).

Every call that carries a `user_id` sets `X-OIM-User-ID` so the consumer is
actually debited — the root README's own documented httpx examples omit
this header entirely, which silently runs them in the anonymous/unmetered
path. This client always sets it when configured, closing that gap by
construction rather than leaving it to the caller to remember.
"""

from __future__ import annotations

import json
from typing import Any, AsyncIterator, Iterator

import httpx

from . import pow as _pow
from .crypto import encrypt_payload
from .errors import raise_for
from .models import (
    Balance,
    BackgroundJob,
    ChatCompletion,
    ChatCompletionChunk,
    ChatMessage,
    CreditEntry,
    RecurrenceSpec,
    Reservation,
    Sensitivity,
)

DEFAULT_MAX_TOKENS = 2048
DEFAULT_TIMEOUT = 120.0


def _as_message_dicts(messages: list[ChatMessage | dict[str, str]]) -> list[dict[str, str]]:
    return [m.to_dict() if isinstance(m, ChatMessage) else dict(m) for m in messages]


def _iter_sse_frames(lines: Iterator[str] | AsyncIterator[str]):
    """Shared SSE parsing rule for both sync and async iterators, factored
    out so it's written and tested once: skip blank/comment lines, stop
    cleanly on `data: [DONE]` (not valid JSON — the naive README example
    would crash trying to json.loads it), yield the parsed frame otherwise."""
    for line in lines:
        if not line.startswith("data: "):
            continue
        payload = line[len("data: ") :]
        if payload == "[DONE]":
            return
        yield json.loads(payload)


async def _aiter_sse_frames(lines: AsyncIterator[str]):
    async for line in lines:
        if not line.startswith("data: "):
            continue
        payload = line[len("data: ") :]
        if payload == "[DONE]":
            return
        yield json.loads(payload)


class MeshClient:
    def __init__(
        self,
        base_url: str,
        *,
        api_key: str | None = None,
        user_id: str | None = None,
        timeout: float = DEFAULT_TIMEOUT,
    ):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.user_id = user_id
        self.timeout = timeout

    def _headers(self) -> dict[str, str]:
        headers: dict[str, str] = {}
        if self.api_key:
            headers["Authorization"] = f"Bearer {self.api_key}"
        if self.user_id:
            headers["X-OIM-User-ID"] = self.user_id
        return headers

    def _chat_body(
        self,
        model: str,
        messages: list[ChatMessage | dict[str, str]],
        max_tokens: int,
        sensitivity: Sensitivity,
        max_price_per_unit: float,
        stream: bool,
    ) -> dict[str, Any]:
        return {
            "model": model,
            "messages": _as_message_dicts(messages),
            "max_tokens": max_tokens,
            "stream": stream,
            "oim_sensitivity": sensitivity,
            "oim_max_price_per_unit": max_price_per_unit,
        }

    # --- Fast lane ---

    def chat(
        self,
        model: str,
        messages: list[ChatMessage | dict[str, str]],
        *,
        max_tokens: int = DEFAULT_MAX_TOKENS,
        sensitivity: Sensitivity = "moderate",
        max_price_per_unit: float = 0.0,
    ) -> ChatCompletion:
        body = self._chat_body(model, messages, max_tokens, sensitivity, max_price_per_unit, stream=False)
        resp = httpx.post(
            f"{self.base_url}/v1/chat/completions", json=body, headers=self._headers(), timeout=self.timeout
        )
        raise_for(resp)
        return ChatCompletion.from_dict(resp.json())

    def stream_chat(
        self,
        model: str,
        messages: list[ChatMessage | dict[str, str]],
        *,
        max_tokens: int = DEFAULT_MAX_TOKENS,
        sensitivity: Sensitivity = "moderate",
        max_price_per_unit: float = 0.0,
    ) -> Iterator[ChatCompletionChunk]:
        body = self._chat_body(model, messages, max_tokens, sensitivity, max_price_per_unit, stream=True)
        with httpx.Client(timeout=self.timeout) as client:
            with client.stream(
                "POST", f"{self.base_url}/v1/chat/completions", json=body, headers=self._headers()
            ) as resp:
                raise_for(resp)
                for frame in _iter_sse_frames(resp.iter_lines()):
                    yield ChatCompletionChunk.from_dict(frame)

    async def achat(
        self,
        model: str,
        messages: list[ChatMessage | dict[str, str]],
        *,
        max_tokens: int = DEFAULT_MAX_TOKENS,
        sensitivity: Sensitivity = "moderate",
        max_price_per_unit: float = 0.0,
    ) -> ChatCompletion:
        body = self._chat_body(model, messages, max_tokens, sensitivity, max_price_per_unit, stream=False)
        async with httpx.AsyncClient(timeout=self.timeout) as client:
            resp = await client.post(f"{self.base_url}/v1/chat/completions", json=body, headers=self._headers())
        raise_for(resp)
        return ChatCompletion.from_dict(resp.json())

    async def astream_chat(
        self,
        model: str,
        messages: list[ChatMessage | dict[str, str]],
        *,
        max_tokens: int = DEFAULT_MAX_TOKENS,
        sensitivity: Sensitivity = "moderate",
        max_price_per_unit: float = 0.0,
    ) -> AsyncIterator[ChatCompletionChunk]:
        body = self._chat_body(model, messages, max_tokens, sensitivity, max_price_per_unit, stream=True)
        async with httpx.AsyncClient(timeout=self.timeout) as client:
            async with client.stream(
                "POST", f"{self.base_url}/v1/chat/completions", json=body, headers=self._headers()
            ) as resp:
                raise_for(resp)
                async for frame in _aiter_sse_frames(resp.aiter_lines()):
                    yield ChatCompletionChunk.from_dict(frame)

    # --- Background lane ---
    # A genuinely different endpoint set from the fast lane, not a `lane=`
    # flag on /v1/chat/completions — the coordinator hardcodes JobLaneFast
    # there. Background jobs are assigned once (sticky-session primary/backup
    # selection persisted server-side) then executed per recurrence cycle.

    def submit_background_job(
        self,
        model: str,
        job_id: str,
        *,
        sensitivity: Sensitivity = "moderate",
        recurrence: RecurrenceSpec | None = None,
        allow_decomposition: bool = False,
        redundancy_depth: int = 0,
        max_price_per_unit: float = 0.0,
    ) -> BackgroundJob:
        job_spec: dict[str, Any] = {
            "job_id": job_id,
            "requester_id": self.user_id or "",
            "model_id": model,
            "lane": "background",
            "sensitivity": sensitivity,
            "max_price_per_unit": max_price_per_unit,
            "redundancy_depth": redundancy_depth,
            "allow_decomposition": allow_decomposition,
        }
        if recurrence is not None:
            job_spec["recurrence"] = recurrence.to_dict()
        resp = httpx.post(
            f"{self.base_url}/jobs/background/assign", json=job_spec, headers=self._headers(), timeout=self.timeout
        )
        raise_for(resp)
        return BackgroundJob.from_dict(resp.json())

    def run_background_cycle(
        self, job: BackgroundJob, messages: list[ChatMessage | dict[str, str]]
    ) -> ChatCompletion:
        body = {"job_id": job.job_id, "messages": _as_message_dicts(messages)}
        resp = httpx.post(
            f"{self.base_url}/jobs/background/execute", json=body, headers=self._headers(), timeout=self.timeout
        )
        raise_for(resp)
        return ChatCompletion.from_dict(resp.json())

    # --- Account ---

    def balance(self) -> Balance:
        if not self.user_id:
            raise ValueError("balance() requires user_id to be set on the client")
        resp = httpx.get(
            f"{self.base_url}/users/{self.user_id}/balance", headers=self._headers(), timeout=self.timeout
        )
        raise_for(resp)
        return Balance.from_dict(resp.json())

    def claim_startup_grant(self, difficulty_bits: int | None = None) -> CreditEntry:
        """Mines the proof-of-work nonce automatically — the root README's
        documented curl for this endpoint omits the nonce entirely and would
        400 against a default deployment (--grant-pow-bits defaults to 18).
        Idempotent: a second claim returns the existing grant, not an error."""
        if not self.user_id:
            raise ValueError("claim_startup_grant() requires user_id to be set on the client")
        bits = _pow.DEFAULT_GRANT_POW_BITS if difficulty_bits is None else difficulty_bits
        nonce = _pow.mine(self.user_id, bits)
        resp = httpx.post(
            f"{self.base_url}/users/{self.user_id}/startup-grant",
            json={"nonce": nonce},
            headers=self._headers(),
            timeout=self.timeout,
        )
        raise_for(resp)
        data = resp.json()
        if data.get("status") == "already_claimed":
            return CreditEntry(
                user_id=self.user_id, origin="grant", amount=data.get("amount", 0.0),
                granted_or_earned_at="", source_reference="startup-grant",
            )
        return CreditEntry.from_dict(data)

    # --- Privacy mode (encrypted-pointer) ---

    def reserve_node(self, model: str, sensitivity: Sensitivity = "moderate") -> Reservation:
        """Pins a node (30s TTL) whose ecdh_public_key you then encrypt to —
        required because the ciphertext can only be decrypted by that one
        node's private key. Not compatible with stream=True (see submit_encrypted)."""
        resp = httpx.post(
            f"{self.base_url}/v1/reserve-node",
            json={"model": model, "sensitivity": sensitivity},
            headers=self._headers(),
            timeout=self.timeout,
        )
        raise_for(resp)
        return Reservation.from_dict(resp.json())

    def submit_encrypted(
        self,
        reservation: Reservation,
        messages: list[ChatMessage | dict[str, str]],
        fetch_url: str,
        *,
        payload_hash: str | None = None,
        max_tokens: int = DEFAULT_MAX_TOKENS,
    ) -> ChatCompletion:
        """Encrypts `messages` to the reserved node's key and submits the
        pointer. The SDK does NOT host the ciphertext for you — `fetch_url`
        must already serve the bytes this call computes (`encrypt_payload`'s
        `.ciphertext`) over HTTP(S) reachable by the assigned node; hosting
        is inherently application-specific (a local dev server, object
        storage, whatever your app already uses). Streaming is not available
        on this path — a reservation always returns buffered."""
        plaintext = json.dumps(_as_message_dicts(messages)).encode("utf-8")
        encrypted = encrypt_payload(plaintext, reservation.ecdh_public_key)
        body = {
            "model": "",  # ignored — DispatchToResolvedNode uses the reserved target directly
            "messages": [],  # real content comes from the pointer, not this field
            "max_tokens": max_tokens,
            "oim_reservation_id": reservation.reservation_id,
            "oim_payload_hash": payload_hash or "",
            "oim_payload_fetch_url": fetch_url,
            "oim_ephemeral_public_key": encrypted.ephemeral_public_key_b64,
        }
        resp = httpx.post(
            f"{self.base_url}/v1/chat/completions", json=body, headers=self._headers(), timeout=self.timeout
        )
        raise_for(resp)
        return ChatCompletion.from_dict(resp.json())
