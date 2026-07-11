"""Typed exceptions for the coordinator's error envelope.

Every coordinator error is `{"error": "<message>"}`, with a handful of
endpoints adding sibling fields (writeErr/writeJSON in cmd/coordinator/main.go
— there is no `code`/`type` nesting anywhere in the protocol). raise_for(...)
is the single place that maps a response to the right typed exception so
callers can `except InsufficientCreditsError` instead of checking status
codes and re-parsing the body themselves.
"""

from __future__ import annotations

import httpx


class MeshError(Exception):
    """Base error for any non-2xx coordinator/directory response."""

    def __init__(self, message: str, status_code: int, body: dict | None = None):
        super().__init__(message)
        self.message = message
        self.status_code = status_code
        self.body = body or {}


class InsufficientCreditsError(MeshError):
    """402 — the balance check failed before dispatch."""

    def __init__(self, message: str, status_code: int, body: dict):
        super().__init__(message, status_code, body)
        self.balance: float = body.get("balance", 0.0)
        self.required: float = body.get("required", 0.0)


class ReservationExpiredError(MeshError):
    """409 — the reservation_id from /v1/reserve-node is unknown or its
    30s TTL (coordinator.ReservationTTL) elapsed. Not recoverable by retrying
    the same reservation; the caller must reserve + re-encrypt."""


class RateLimitedError(MeshError):
    """429 — per-IP, per-account, or startup-grant-specific rate limit."""

    def __init__(self, message: str, status_code: int, body: dict, retry_after: float | None = None):
        super().__init__(message, status_code, body)
        self.retry_after = retry_after


class NoCapacityError(MeshError):
    """503 — no eligible node available for this job right now."""


def raise_for(response: httpx.Response) -> None:
    """Raises the appropriate MeshError subclass for a non-2xx response.
    No-op for 2xx. Every coordinator error body is {"error": str, ...} —
    a response that isn't valid JSON (rare, e.g. a proxy's own error page)
    still raises MeshError with the raw text as the message."""
    if response.status_code < 400:
        return
    try:
        body = response.json()
    except ValueError:
        body = {}
    message = body.get("error", response.text or f"HTTP {response.status_code}")

    if response.status_code == 402:
        raise InsufficientCreditsError(message, response.status_code, body)
    if response.status_code == 409:
        raise ReservationExpiredError(message, response.status_code, body)
    if response.status_code == 429:
        retry_after = response.headers.get("Retry-After")
        raise RateLimitedError(
            message, response.status_code, body, float(retry_after) if retry_after else None
        )
    if response.status_code == 503:
        raise NoCapacityError(message, response.status_code, body)
    raise MeshError(message, response.status_code, body)
