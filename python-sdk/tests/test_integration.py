"""End-to-end test against the REAL oim-coordinator + stub-exo + oim node
binaries, mirroring the pattern in ../../tests/integration_test.go: build the
Go binaries once, spin up a fresh mesh per test, talk to it only through the
public HTTP wire protocol — no mocking. Requires the `go` toolchain; skips
cleanly (not a failure) when unavailable, same as the crypto interop test.
"""

from __future__ import annotations

import shutil
import socket
import subprocess
import time
from pathlib import Path

import pytest

from mlxmesh import MeshClient, RecurrenceSpec, Wallet
from mlxmesh.errors import InsufficientCreditsError, NoCredentialError

REPO_ROOT = Path(__file__).resolve().parents[2]


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _wait_healthy(url: str, timeout: float = 15.0) -> None:
    import httpx

    deadline = time.time() + timeout
    last_err = None
    while time.time() < deadline:
        try:
            resp = httpx.get(url, timeout=2.0)
            if resp.status_code == 200:
                return
        except httpx.HTTPError as e:
            last_err = e
        time.sleep(0.15)
    raise TimeoutError(f"{url} never became healthy: {last_err}")


@pytest.fixture(scope="session")
def go_binaries(tmp_path_factory):
    if shutil.which("go") is None:
        pytest.skip("go toolchain not available — skipping live-mesh integration tests")
    out_dir = tmp_path_factory.mktemp("oim-bin")
    binaries = {}
    for name, pkg in (
        ("coordinator", "./cmd/coordinator"),
        ("stub-exo", "./cmd/stub-exo"),
        ("oim", "./cmd/oim"),
    ):
        out = out_dir / name
        result = subprocess.run(
            ["go", "build", "-o", str(out), pkg], cwd=REPO_ROOT, capture_output=True, text=True
        )
        if result.returncode != 0:
            pytest.fail(f"failed to build {pkg}: {result.stderr}")
        binaries[name] = str(out)
    return binaries


def _start_mesh(go_binaries, tmp_path, extra_coordinator_args=()):
    """Starts a fresh stub-exo + coordinator + node trio, yields the
    coordinator's base URL, and tears everything down afterward. Shared by
    the `mesh` and `mesh_with_auth` fixtures below — the only difference is
    whether the coordinator is given --api-key (and therefore actually
    enforces Bearer auth on billable endpoints)."""
    exo_port, coord_port, node_port = _free_port(), _free_port(), _free_port()
    exo_url = f"http://127.0.0.1:{exo_port}"
    coord_url = f"http://127.0.0.1:{coord_port}"

    procs = []
    coord_home = tmp_path / "coord-home"
    coord_home.mkdir()

    def start(args, env=None):
        import os

        full_env = {**os.environ, **(env or {})}
        # cwd=coord_home: the coordinator/directory/stub-exo write relative
        # files (ledger.db, pod pins, etc.) — keep test artifacts out of the
        # repo tree instead of wherever pytest happened to be invoked from.
        p = subprocess.Popen(
            args, env=full_env, cwd=coord_home, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL
        )
        procs.append(p)
        return p

    start(
        [go_binaries["stub-exo"]],
        env={"STUB_LISTEN": f":{exo_port}", "STUB_RESPONSE_FILLER_WORDS": "20"},
    )
    _wait_healthy(f"{exo_url}/state")

    start(
        [
            go_binaries["coordinator"],
            f"--listen=:{coord_port}",
            "--pod-id=pysdk-itest",
            "--region=us",
            f"--public-url={coord_url}",
            "--grant-pow-bits=8",  # low difficulty — fast tests, still exercises real PoW verification
            *extra_coordinator_args,
        ]
    )
    _wait_healthy(f"{coord_url}/health")

    node_home = tmp_path / "node-home"
    node_home.mkdir()
    start(
        [
            go_binaries["oim"],
            "node",
            "start",
            f"--coordinator={coord_url}",
            f"--listen=:{node_port}",
            f"--exo-url={exo_url}",
            f"--reachability-endpoint=http://127.0.0.1:{node_port}",
            "--region=us",
            "--user-id=pysdk-itest-miner",
        ],
        env={"HOME": str(node_home)},
    )

    # Wait for node registration.
    import httpx

    deadline = time.time() + 15
    while time.time() < deadline:
        nodes = httpx.get(f"{coord_url}/nodes", timeout=2.0).json()
        if len(nodes.get("nodes", [])) >= 1:
            break
        time.sleep(0.2)
    else:
        for p in procs:
            p.kill()
        pytest.fail("node never registered with coordinator")

    try:
        yield coord_url
    finally:
        for p in procs:
            p.kill()
            p.wait(timeout=5)


@pytest.fixture
def mesh(go_binaries, tmp_path):
    yield from _start_mesh(go_binaries, tmp_path)


@pytest.fixture
def mesh_with_auth(go_binaries, tmp_path):
    """Same mesh, but with --api-key set so authMiddleware actually engages
    on billable endpoints — the scenario that matters for proving wallet
    auth really enforces payment, not just against a wide-open dev
    coordinator (node registration is unaffected: /nodes/register is
    self-authenticating regardless of --api-key)."""
    yield from _start_mesh(go_binaries, tmp_path, extra_coordinator_args=("--api-key=pysdk-itest-static-key",))


def test_startup_grant_balance_and_chat_end_to_end(mesh):
    client = MeshClient(mesh, wallet=Wallet.create(), timeout=30)

    grant = client.claim_startup_grant(difficulty_bits=8)
    assert grant.amount > 0

    bal = client.balance()
    assert bal.total == grant.amount

    resp = client.chat("llama-3.2-3b", [{"role": "user", "content": "hi"}])
    assert "Simulated response" in resp.content
    assert resp.served_by_node_id
    assert resp.tokens_per_sec and resp.tokens_per_sec > 0
    assert client.api_key is not None  # chat() authenticated the wallet transparently

    bal_after = client.balance()
    assert bal_after.total < bal.total  # the fast-lane dispatch debited the consumer


def test_streaming_chat_parses_all_frames_including_trailing_usage(mesh):
    client = MeshClient(mesh, wallet=Wallet.create(), timeout=30)
    client.claim_startup_grant(difficulty_bits=8)

    chunks = list(client.stream_chat("llama-3.2-3b", [{"role": "user", "content": "hi"}]))
    assert chunks, "expected at least one SSE frame"

    content = "".join(c.delta_content or "" for c in chunks)
    assert "Simulated response" in content

    usage_frames = [c for c in chunks if c.is_usage_frame]
    assert len(usage_frames) == 1
    assert usage_frames[0].usage["completion_tokens"] > 0


@pytest.mark.asyncio
async def test_async_chat_and_streaming(mesh):
    client = MeshClient(mesh, wallet=Wallet.create(), timeout=30)
    client.claim_startup_grant(difficulty_bits=8)

    resp = await client.achat("llama-3.2-3b", [{"role": "user", "content": "hi"}])
    assert "Simulated response" in resp.content

    chunks = [c async for c in client.astream_chat("llama-3.2-3b", [{"role": "user", "content": "hi"}])]
    assert any(c.delta_content for c in chunks)
    assert any(c.is_usage_frame for c in chunks)


def test_background_lane_assign_and_execute(mesh):
    client = MeshClient(mesh, wallet=Wallet.create(), timeout=30)

    job = client.submit_background_job(
        "llama-3.2-3b", "itest-bg-job-1", recurrence=RecurrenceSpec(interval_seconds=60)
    )
    assert job.job_id == "itest-bg-job-1"
    assert job.primary

    result = client.run_background_cycle(job, [{"role": "user", "content": "hi"}])
    assert "Simulated response" in result.content


def test_insufficient_credits_raises_typed_error(mesh):
    client = MeshClient(mesh, wallet=Wallet.create(), timeout=30)
    with pytest.raises(InsufficientCreditsError) as exc_info:
        client.chat("llama-3.2-3b", [{"role": "user", "content": "hi"}])
    assert exc_info.value.required > 0
    assert exc_info.value.balance == 0


def test_reserve_node_returns_a_usable_ecdh_key(mesh):
    client = MeshClient(mesh, wallet=Wallet.create(), timeout=30)
    reservation = client.reserve_node("llama-3.2-3b")
    assert reservation.reservation_id
    assert reservation.node_id
    assert reservation.ecdh_public_key


def test_directory_topology_and_find_pods(go_binaries, tmp_path):
    """The directory is a separate binary from the coordinator — build it
    directly here since the shared `mesh` fixture doesn't start one."""
    from mlxmesh import MeshDirectory

    directory_bin_dir = Path(go_binaries["coordinator"]).parent
    directory_bin = directory_bin_dir / "directory"
    result = subprocess.run(
        ["go", "build", "-o", str(directory_bin), "./cmd/directory"], cwd=REPO_ROOT, capture_output=True, text=True
    )
    if result.returncode != 0:
        pytest.fail(f"failed to build directory: {result.stderr}")

    port = _free_port()
    # cwd=tmp_path: the directory writes directory_pod_pins.json relative to
    # its working directory — keep that out of the repo tree.
    proc = subprocess.Popen(
        [str(directory_bin), f"--listen=:{port}"],
        cwd=tmp_path,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    try:
        _wait_healthy(f"http://127.0.0.1:{port}/health")
        d = MeshDirectory(f"http://127.0.0.1:{port}")
        topo = d.topology()
        assert isinstance(topo, list)  # empty is fine — no pod has reported in yet
        pods = d.find_pods("llama-3.2-3b")
        assert isinstance(pods, list)
    finally:
        proc.kill()
        proc.wait(timeout=5)


# --- Wallet: challenge/response auth actually enforced (--api-key set) ---
# Uses mesh_with_auth (not mesh) specifically so authMiddleware really
# engages — this is the strongest possible proof the wallet flow works,
# since a bug in the address/signature encoding would show up as a real 401
# from the real coordinator, not a mock accepting anything.


def test_wallet_authenticates_and_pays_for_a_real_job(mesh_with_auth):
    wallet = Wallet.create()
    client = MeshClient(mesh_with_auth, wallet=wallet, timeout=30)
    assert client.user_id == wallet.address

    grant = client.claim_startup_grant(difficulty_bits=8)
    assert grant.amount > 0

    assert client.api_key is None  # nothing minted yet — chat() must do it
    resp = client.chat("llama-3.2-3b", [{"role": "user", "content": "hi"}])
    assert client.api_key is not None and client.api_key.startswith("oim_")
    assert "Simulated response" in resp.content

    bal = client.balance()
    assert bal.total < grant.amount  # the dispatch really debited the wallet's own address


def test_static_api_key_still_works_alongside_wallet_auth(mesh_with_auth):
    """The RUNBOOK-documented static-key workflow must keep working
    unchanged now that wallet auth exists — additive, not a replacement."""
    client = MeshClient(mesh_with_auth, api_key="pysdk-itest-static-key", user_id="pysdk-itest-static-user")
    client.claim_startup_grant(difficulty_bits=8)
    resp = client.chat("llama-3.2-3b", [{"role": "user", "content": "hi"}])
    assert "Simulated response" in resp.content


def test_expired_api_key_self_heals_via_wallet_reauth(mesh_with_auth):
    wallet = Wallet.create()
    client = MeshClient(mesh_with_auth, wallet=wallet, timeout=30)
    client.claim_startup_grant(difficulty_bits=8)
    client.authenticate()
    stale_key = client.api_key
    client.api_key = "oim_deliberately-invalid-and-unknown-key"

    resp = client.chat("llama-3.2-3b", [{"role": "user", "content": "hi"}])
    assert "Simulated response" in resp.content
    assert client.api_key not in (stale_key, "oim_deliberately-invalid-and-unknown-key")


def test_no_credential_client_never_reaches_the_wire_even_with_auth_enforced(mesh_with_auth):
    client = MeshClient(mesh_with_auth, user_id="pysdk-itest-nocred", timeout=5)
    with pytest.raises(NoCredentialError):
        client.chat("llama-3.2-3b", [{"role": "user", "content": "hi"}])
