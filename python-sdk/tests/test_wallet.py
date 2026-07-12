"""Unit tests for Wallet — address derivation, persistence, and the
client-side "refuse before any network call" enforcement. No live coordinator
needed here; see test_integration.py for the real challenge/response
round-trip against the actual Go binary."""

from __future__ import annotations

import hashlib
import stat

import pytest

from mlxmesh import MeshClient, NoCredentialError, Wallet
from mlxmesh.wallet import address_from_public_key


def test_create_produces_a_32_byte_seed_and_matching_address():
    w = Wallet.create()
    expected = "oim" + hashlib.sha256(w._public_key_raw).hexdigest()
    assert w.address == expected
    assert w.address.startswith("oim")
    assert len(w.address) == len("oim") + 64  # sha256 hex digest is 64 chars, no truncation


def test_address_is_deterministic_for_the_same_seed():
    seed = bytes(range(32))
    assert Wallet(seed).address == Wallet(seed).address


def test_different_seeds_give_different_addresses():
    assert Wallet.create().address != Wallet.create().address


def test_rejects_wrong_length_seed():
    with pytest.raises(ValueError):
        Wallet(b"too short")


def test_address_from_public_key_matches_wallet_address():
    w = Wallet.create()
    assert address_from_public_key(w._public_key_raw) == w.address


def test_sign_produces_a_self_verifiable_signature():
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

    w = Wallet.create()
    message = b"oim-account-auth:some-address:some-nonce"
    sig = w.sign(message)
    assert len(sig) == 64
    pub = Ed25519PublicKey.from_public_bytes(w._public_key_raw)
    pub.verify(sig, message)  # raises if invalid — no exception is the assertion


def test_save_and_load_round_trip(tmp_path):
    path = tmp_path / "wallet.json"
    w1 = Wallet.create()
    w1.save(path)
    w2 = Wallet.load(path)
    assert w2.address == w1.address


def test_save_sets_owner_only_permissions(tmp_path):
    path = tmp_path / "wallet.json"
    Wallet.create().save(path)
    mode = stat.S_IMODE(path.stat().st_mode)
    assert mode == 0o600


def test_load_missing_file_raises():
    with pytest.raises(FileNotFoundError):
        Wallet.load("/nonexistent/path/wallet.json")


def test_load_or_create_persists_on_first_call_and_reuses_on_second(tmp_path):
    path = tmp_path / "wallet.json"
    assert not path.exists()
    w1 = Wallet.load_or_create(path)
    assert path.exists()
    w2 = Wallet.load_or_create(path)
    assert w2.address == w1.address


def test_client_auto_populates_user_id_from_wallet():
    wallet = Wallet.create()
    client = MeshClient("http://example.invalid", wallet=wallet)
    assert client.user_id == wallet.address


def test_client_rejects_conflicting_user_id_and_wallet():
    wallet = Wallet.create()
    with pytest.raises(ValueError):
        MeshClient("http://example.invalid", wallet=wallet, user_id="not-the-wallet-address")


def test_client_with_no_credential_refuses_before_any_network_call():
    # A deliberately unroutable address — if the SDK tried to make an HTTP
    # call at all here, this would raise a connection error, not
    # NoCredentialError. user_id alone (no api_key, no wallet) is not enough.
    client = MeshClient("http://127.0.0.1:1", user_id="someone", timeout=1)
    with pytest.raises(NoCredentialError):
        client.chat("llama-3.2-3b", [{"role": "user", "content": "hi"}])
    with pytest.raises(NoCredentialError):
        client.submit_background_job("llama-3.2-3b", "job-1")


@pytest.mark.asyncio
async def test_async_client_with_no_credential_refuses_before_any_network_call():
    client = MeshClient("http://127.0.0.1:1", user_id="someone", timeout=1)
    with pytest.raises(NoCredentialError):
        await client.achat("llama-3.2-3b", [{"role": "user", "content": "hi"}])
