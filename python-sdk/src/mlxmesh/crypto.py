"""Client-side half of the encrypted-pointer payload scheme.

Byte-compatible with internal/payloadcrypto/payloadcrypto.go and
OIMDashboard/iOS/Crypto/PayloadEncryption.swift: ECDH(P-256) -> HKDF-SHA256
(salt=None, info="oim-payload-v1") -> AES-256-GCM. The wire blob is
`nonce (12 bytes) || ciphertext || tag`, matching Go's `append(nonce,
sealed...)` where `sealed` already carries the GCM tag, and Swift's
`AES.GCM.SealedBox.combined`. The ephemeral public key travels separately
(base64, raw uncompressed SEC1 point) — it is NOT part of this blob.
"""

from __future__ import annotations

import base64
import os
from dataclasses import dataclass

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.kdf.hkdf import HKDF

HKDF_INFO = b"oim-payload-v1"
GCM_NONCE_LEN = 12
AES_KEY_LEN = 32


@dataclass
class EncryptedPayload:
    ciphertext: bytes  # nonce || ciphertext || tag
    ephemeral_public_key_b64: str  # base64, raw uncompressed P-256 point


def _derive_key(shared_secret: bytes) -> bytes:
    return HKDF(
        algorithm=hashes.SHA256(),
        length=AES_KEY_LEN,
        salt=None,
        info=HKDF_INFO,
    ).derive(shared_secret)


def encrypt_payload(plaintext: bytes, recipient_ecdh_public_key_b64: str) -> EncryptedPayload:
    """Encrypts `plaintext` (conventionally `json.dumps(messages).encode()`,
    matching the wire contract documented in payloadcrypto.go) to a node's
    ECDH public key, as returned by POST /v1/reserve-node's `ecdh_public_key`
    field. Generates a fresh ephemeral keypair per call — never reuse one
    across jobs."""
    recipient_raw = base64.b64decode(recipient_ecdh_public_key_b64)
    recipient_pub = ec.EllipticCurvePublicKey.from_encoded_point(ec.SECP256R1(), recipient_raw)

    ephemeral_priv = ec.generate_private_key(ec.SECP256R1())
    shared_secret = ephemeral_priv.exchange(ec.ECDH(), recipient_pub)
    key = _derive_key(shared_secret)

    nonce = os.urandom(GCM_NONCE_LEN)
    sealed = AESGCM(key).encrypt(nonce, plaintext, None)  # ciphertext||tag

    ephemeral_pub_raw = ephemeral_priv.public_key().public_bytes(
        encoding=serialization.Encoding.X962,
        format=serialization.PublicFormat.UncompressedPoint,
    )
    return EncryptedPayload(
        ciphertext=nonce + sealed,
        ephemeral_public_key_b64=base64.b64encode(ephemeral_pub_raw).decode("ascii"),
    )


def decrypt_payload(combined: bytes, ephemeral_public_key_b64: str, recipient_private_key: ec.EllipticCurvePrivateKey) -> bytes:
    """The inverse of encrypt_payload — provided for round-trip testing and
    for any Python-side node/tooling that needs to decrypt, mirroring Go's
    payloadcrypto.Decrypt. Not needed for a typical SDK consumer (only the
    assigned node ever decrypts in production)."""
    if len(combined) < GCM_NONCE_LEN:
        raise ValueError("ciphertext too short")
    nonce, ct = combined[:GCM_NONCE_LEN], combined[GCM_NONCE_LEN:]

    ephemeral_raw = base64.b64decode(ephemeral_public_key_b64)
    ephemeral_pub = ec.EllipticCurvePublicKey.from_encoded_point(ec.SECP256R1(), ephemeral_raw)
    shared_secret = recipient_private_key.exchange(ec.ECDH(), ephemeral_pub)
    key = _derive_key(shared_secret)

    return AESGCM(key).decrypt(nonce, ct, None)
