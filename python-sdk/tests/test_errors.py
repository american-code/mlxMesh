import httpx
import pytest

from mlxmesh.errors import (
    InsufficientCreditsError,
    MeshError,
    NoCapacityError,
    RateLimitedError,
    ReservationExpiredError,
    raise_for,
)


def _response(status_code: int, json_body: dict | None = None, headers: dict | None = None) -> httpx.Response:
    request = httpx.Request("GET", "http://example.test/")
    return httpx.Response(status_code, json=json_body, headers=headers or {}, request=request)


def test_2xx_is_a_noop():
    raise_for(_response(200, {"ok": True}))  # must not raise


def test_402_raises_insufficient_credits_with_fields():
    resp = _response(402, {"error": "insufficient_credits", "balance": 0.5, "required": 2.0, "max_tokens": 2048})
    with pytest.raises(InsufficientCreditsError) as exc_info:
        raise_for(resp)
    err = exc_info.value
    assert err.balance == 0.5
    assert err.required == 2.0
    assert err.message == "insufficient_credits"


def test_409_raises_reservation_expired():
    resp = _response(409, {"error": "reservation_expired_or_unknown: re-reserve and re-encrypt"})
    with pytest.raises(ReservationExpiredError):
        raise_for(resp)


def test_429_raises_rate_limited_with_retry_after():
    resp = _response(429, {"error": "rate limit exceeded, retry shortly"}, headers={"Retry-After": "1"})
    with pytest.raises(RateLimitedError) as exc_info:
        raise_for(resp)
    assert exc_info.value.retry_after == 1.0


def test_429_without_retry_after_header_still_raises():
    resp = _response(429, {"error": "per-account quota exceeded, retry shortly"})
    with pytest.raises(RateLimitedError) as exc_info:
        raise_for(resp)
    assert exc_info.value.retry_after is None


def test_503_raises_no_capacity():
    resp = _response(503, {"error": "no eligible nodes available for job job-123 (tried 3)"})
    with pytest.raises(NoCapacityError):
        raise_for(resp)


def test_generic_4xx_raises_base_mesh_error():
    resp = _response(400, {"error": "parse request: unexpected EOF"})
    with pytest.raises(MeshError) as exc_info:
        raise_for(resp)
    assert exc_info.value.status_code == 400
    assert "parse request" in exc_info.value.message


def test_non_json_body_still_raises_with_raw_text():
    request = httpx.Request("GET", "http://example.test/")
    resp = httpx.Response(500, text="upstream proxy error", request=request)
    with pytest.raises(MeshError) as exc_info:
        raise_for(resp)
    assert "upstream proxy error" in exc_info.value.message
