"""mlxmesh — Python client SDK for the Open Inference Mesh (mlxMesh) network."""

from .client import MeshClient
from .directory import MeshDirectory
from .errors import (
    InsufficientCreditsError,
    MeshError,
    NoCapacityError,
    RateLimitedError,
    ReservationExpiredError,
)
from .models import (
    Balance,
    BackgroundJob,
    ChatCompletion,
    ChatCompletionChunk,
    ChatMessage,
    CreditEntry,
    PodHealthDigest,
    RecurrenceSpec,
    Reservation,
    Sensitivity,
)

__version__ = "0.1.0"

__all__ = [
    "MeshClient",
    "MeshDirectory",
    "MeshError",
    "InsufficientCreditsError",
    "ReservationExpiredError",
    "RateLimitedError",
    "NoCapacityError",
    "ChatMessage",
    "ChatCompletion",
    "ChatCompletionChunk",
    "Balance",
    "BackgroundJob",
    "CreditEntry",
    "PodHealthDigest",
    "Reservation",
    "RecurrenceSpec",
    "Sensitivity",
]
