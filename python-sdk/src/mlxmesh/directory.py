"""Model discovery against the directory service.

The coordinator itself has no /topology endpoint — that's the separate
`oim-directory` binary (cmd/directory/main.go), which each coordinator pod
periodically reports its signed PodHealthDigest to. A third-party app
discovers which pods currently serve a model BEFORE submitting a job by
querying the directory, then talks to that pod's own coordinator_endpoint
for the actual /v1/chat/completions call.
"""

from __future__ import annotations

import httpx

from .errors import raise_for
from .models import PodHealthDigest


class MeshDirectory:
    def __init__(self, base_url: str, *, timeout: float = 10.0):
        self._base_url = base_url.rstrip("/")
        self._timeout = timeout

    def topology(self) -> list[PodHealthDigest]:
        """GET /topology — every pod the directory currently knows about."""
        resp = httpx.get(f"{self._base_url}/topology", timeout=self._timeout)
        raise_for(resp)
        data = resp.json()
        return [PodHealthDigest.from_dict(p) for p in data.get("pods", [])]

    def find_pods(self, model_id: str) -> list[str]:
        """GET /pods?model_id=... — pod IDs advertising this model. Aggregate-only
        (no quantization filter at this layer) — check the specific pod's own
        GET /nodes for per-node/per-quantization availability."""
        resp = httpx.get(f"{self._base_url}/pods", params={"model_id": model_id}, timeout=self._timeout)
        raise_for(resp)
        return resp.json().get("matching_pods", [])
