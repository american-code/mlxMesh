"""Portable, recoverable account identity — the client-side half of
`internal/wallet.Manager`'s challenge/response protocol.

This is NOT an on-chain wallet: credits live in the coordinator's ledger
(server-authoritative internal credits, no token/chain). What `Wallet`
provides is an Ed25519 keypair that PROVES ownership of a ledger balance, so
the same account works from any device/process and is recoverable — the
coordinator never generates or sees the private key, only the derived
address and, once a challenge is signed, the public key.

Wire format matches `internal/wallet/wallet.go` and `cmd/coordinator/main.go`'s
`POST /account/challenge` / `POST /account/auth` handlers exactly:

  - address       = "oim" + hex(sha256(raw 32-byte Ed25519 public key))
  - signed message = f"oim-account-auth:{address}:{nonce}" (UTF-8 bytes)
  - public_key/signature on the wire are standard (padded) base64 — NOT
    URL-safe base64.
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
from pathlib import Path

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

ADDRESS_PREFIX = "oim"

DEFAULT_WALLET_PATH = Path.home() / ".config" / "mlxmesh" / "wallet.json"


def address_from_public_key(public_key_raw: bytes) -> str:
    """Mirrors wallet.AddressFromPubKey exactly: AddressPrefix + hex(sha256(pubkey))."""
    return ADDRESS_PREFIX + hashlib.sha256(public_key_raw).hexdigest()


class Wallet:
    """An Ed25519 account keypair plus its derived address.

    The private key never leaves this object except to sign — it is never
    sent over the network. Only the address and (during authentication) the
    raw public key and a signature are ever transmitted.
    """

    def __init__(self, seed: bytes):
        if len(seed) != 32:
            raise ValueError(f"wallet seed must be 32 bytes, got {len(seed)}")
        self._private_key = Ed25519PrivateKey.from_private_bytes(seed)
        self._seed = seed
        self._public_key_raw = self._private_key.public_key().public_bytes_raw()
        self.address = address_from_public_key(self._public_key_raw)

    @classmethod
    def create(cls) -> "Wallet":
        """Generates a brand-new random keypair. Nothing is persisted until
        you call .save() — the caller decides whether/where to keep it."""
        seed = os.urandom(32)
        return cls(seed)

    @classmethod
    def load(cls, path: str | Path | None = None) -> "Wallet":
        """Loads a previously-saved wallet. Raises FileNotFoundError if the
        path doesn't exist — use load_or_create() if you want silent
        first-run creation instead."""
        p = Path(path) if path is not None else DEFAULT_WALLET_PATH
        with open(p, "r", encoding="utf-8") as f:
            data = json.load(f)
        seed = bytes.fromhex(data["seed"])
        return cls(seed)

    @classmethod
    def load_or_create(cls, path: str | Path | None = None) -> "Wallet":
        """The common case: use the wallet on disk if one exists, otherwise
        generate a fresh one and persist it immediately so the address is
        stable across process restarts from the very first run."""
        p = Path(path) if path is not None else DEFAULT_WALLET_PATH
        if p.exists():
            return cls.load(p)
        wallet = cls.create()
        wallet.save(p)
        return wallet

    def save(self, path: str | Path | None = None) -> None:
        """Persists the seed as {"seed": "<64 hex chars>"} with 0600
        permissions — mirrors nodeconfig.Save's convention on the Go side,
        scoped to this SDK's own config namespace."""
        p = Path(path) if path is not None else DEFAULT_WALLET_PATH
        p.parent.mkdir(parents=True, exist_ok=True)
        with open(p, "w", encoding="utf-8") as f:
            json.dump({"seed": self._seed.hex()}, f)
        os.chmod(p, 0o600)

    def sign(self, message: bytes) -> bytes:
        """Raw Ed25519 signature over message (64 bytes)."""
        return self._private_key.sign(message)

    @property
    def public_key_b64(self) -> str:
        """Standard (padded) base64 of the raw 32-byte public key — the exact
        encoding POST /account/auth expects in its public_key field."""
        return base64.b64encode(self._public_key_raw).decode("ascii")

    def __repr__(self) -> str:
        return f"Wallet(address={self.address!r})"
