"""Proof-of-work nonce mining for POST /users/{id}/startup-grant.

Matches internal/settlement/pow.go's VerifyProofOfWork exactly: the
coordinator requires a uint64 nonce such that sha256(user_id_bytes ++
big_endian_uint64(nonce)) has >= difficulty_bits leading zero BITS (not
bytes). DefaultGrantPoWBits = 18 averages ~262k hashes — sub-second on real
hardware — cheap enough not to annoy a real claim, expensive enough to make
Sybil-farming disposable user_ids cost real wall-clock time.
"""

from __future__ import annotations

import hashlib

DEFAULT_GRANT_POW_BITS = 18


def _leading_zero_bits(digest: bytes) -> int:
    count = 0
    for byte in digest:
        if byte == 0:
            count += 8
            continue
        mask = 0x80
        while mask:
            if byte & mask:
                return count
            count += 1
            mask >>= 1
    return count


def verify_proof_of_work(user_id: str, nonce: int, difficulty_bits: int) -> bool:
    """Pure check, mirroring the Go side — used by mine() and by tests to
    confirm interop without needing a live coordinator."""
    if difficulty_bits <= 0:
        return True
    digest = hashlib.sha256(user_id.encode("utf-8") + nonce.to_bytes(8, "big")).digest()
    return _leading_zero_bits(digest) >= difficulty_bits


def mine(user_id: str, difficulty_bits: int = DEFAULT_GRANT_POW_BITS) -> int:
    """Brute-forces the smallest nonce satisfying verify_proof_of_work.
    Deterministic starting point (0) — anyone re-mining the same user_id at
    the same difficulty gets the same nonce, which is fine: the coordinator
    doesn't care which valid nonce you submit, only that one is valid, and a
    claim is idempotent (a second claim is a no-op, not an error)."""
    nonce = 0
    while not verify_proof_of_work(user_id, nonce, difficulty_bits):
        nonce += 1
    return nonce
