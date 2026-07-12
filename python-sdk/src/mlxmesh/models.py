"""Wire types for the coordinator/directory JSON protocol.

These mirror internal/protocol, internal/settlement, and internal/coordinator
Go structs field-for-field (snake_case JSON, matching the wire exactly) —
see the root README.md and cmd/coordinator/main.go for the authoritative
protocol this SDK talks to.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Literal

Sensitivity = Literal["low", "moderate", "high_requires_attestation"]


@dataclass
class ChatMessage:
    role: str
    content: str

    def to_dict(self) -> dict[str, str]:
        return {"role": self.role, "content": self.content}


@dataclass
class ChatCompletion:
    """A buffered /v1/chat/completions response."""

    id: str
    object: str
    model: str
    created: int
    choices: list[dict[str, Any]]
    usage: dict[str, Any]
    served_by_node_id: str | None = None
    lane: str | None = None
    latency_ms: int | None = None
    tokens_per_sec: float | None = None
    raw: dict[str, Any] = field(default_factory=dict)

    @property
    def content(self) -> str:
        """Convenience accessor for choices[0].message.content, the common case."""
        return self.choices[0]["message"]["content"]

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "ChatCompletion":
        return cls(
            id=data.get("id", ""),
            object=data.get("object", ""),
            model=data.get("model", ""),
            created=data.get("created", 0),
            choices=data.get("choices", []),
            usage=data.get("usage", {}),
            served_by_node_id=data.get("oim_served_by_node_id"),
            lane=data.get("oim_lane"),
            latency_ms=data.get("oim_latency_ms"),
            tokens_per_sec=data.get("oim_tokens_per_sec"),
            raw=data,
        )


@dataclass
class ChatCompletionChunk:
    """One SSE frame. `usage` is only populated on the trailing frame, which
    has an empty `choices` list — callers must check `usage` before assuming
    a `delta` is present, exactly the crash the naive README example hits."""

    id: str
    object: str
    model: str
    created: int
    choices: list[dict[str, Any]]
    usage: dict[str, Any] | None = None

    @property
    def delta_content(self) -> str | None:
        if not self.choices:
            return None
        return self.choices[0].get("delta", {}).get("content")

    @property
    def is_usage_frame(self) -> bool:
        return not self.choices and self.usage is not None

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "ChatCompletionChunk":
        return cls(
            id=data.get("id", ""),
            object=data.get("object", ""),
            model=data.get("model", ""),
            created=data.get("created", 0),
            choices=data.get("choices", []),
            usage=data.get("usage"),
        )


@dataclass
class Balance:
    grant_balance: float
    earned_balance: float
    total: float

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Balance":
        return cls(
            grant_balance=data.get("grant_balance", 0.0),
            earned_balance=data.get("earned_balance", 0.0),
            total=data.get("total", 0.0),
        )


@dataclass
class CreditEntry:
    user_id: str
    origin: str
    amount: float
    granted_or_earned_at: str
    source_reference: str

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "CreditEntry":
        return cls(
            user_id=data.get("user_id", ""),
            origin=data.get("origin", ""),
            amount=data.get("amount", 0.0),
            granted_or_earned_at=data.get("granted_or_earned_at", ""),
            source_reference=data.get("source_reference", ""),
        )


@dataclass
class PodHealthDigest:
    pod_id: str
    region_hint: str
    coordinator_endpoint: str
    servable_model_ids: list[str]
    aggregate_health_score: float
    node_count_approx: int
    total_memory_gb: float
    aggregate_toks_per_sec: float
    real_node_count_approx: int = 0
    real_total_memory_gb: float = 0.0
    real_aggregate_toks_per_sec: float = 0.0

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "PodHealthDigest":
        return cls(
            pod_id=data.get("pod_id", ""),
            region_hint=data.get("region_hint", ""),
            coordinator_endpoint=data.get("coordinator_endpoint", ""),
            servable_model_ids=data.get("servable_model_ids", []),
            aggregate_health_score=data.get("aggregate_health_score", 0.0),
            node_count_approx=data.get("node_count_approx", 0),
            total_memory_gb=data.get("total_memory_gb", 0.0),
            aggregate_toks_per_sec=data.get("aggregate_toks_per_sec", 0.0),
            real_node_count_approx=data.get("real_node_count_approx", 0),
            real_total_memory_gb=data.get("real_total_memory_gb", 0.0),
            real_aggregate_toks_per_sec=data.get("real_aggregate_toks_per_sec", 0.0),
        )


@dataclass
class Reservation:
    """Response from POST /v1/reserve-node — pins a node for an
    encrypted-pointer job for coordinator.ReservationTTL (30s)."""

    reservation_id: str
    node_id: str
    ecdh_public_key: str  # base64, raw uncompressed P-256 point
    expires_at: str

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Reservation":
        return cls(
            reservation_id=data.get("reservation_id", ""),
            node_id=data.get("node_id", ""),
            ecdh_public_key=data.get("ecdh_public_key", ""),
            expires_at=data.get("expires_at", ""),
        )


@dataclass
class RecurrenceSpec:
    interval_seconds: int
    max_jitter_seconds: int = 0

    def to_dict(self) -> dict[str, int]:
        return {"interval_seconds": self.interval_seconds, "max_jitter_seconds": self.max_jitter_seconds}


@dataclass
class BackgroundJob:
    """Response from POST /jobs/background/assign — a persisted sticky-session
    assignment. Only job_id is needed to call run_background_cycle; primary/
    backups/job_spec are kept for inspection, not required by the SDK."""

    job_id: str
    primary: str
    backups: list[str]
    job_spec: dict[str, Any]

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "BackgroundJob":
        return cls(
            job_id=data.get("job_id", ""),
            primary=data.get("primary", ""),
            backups=data.get("backups", []),
            job_spec=data.get("job_spec", {}),
        )
