import base64
import os
import shutil
import subprocess

import pytest
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ec

from mlxmesh.crypto import decrypt_payload, encrypt_payload

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
GO_HELPER_SRC = os.path.join(os.path.dirname(__file__), "go_interop_helper")


def _generate_recipient_keypair():
    priv = ec.generate_private_key(ec.SECP256R1())
    pub_raw = priv.public_key().public_bytes(
        serialization.Encoding.X962, serialization.PublicFormat.UncompressedPoint
    )
    return priv, base64.b64encode(pub_raw).decode("ascii")


def test_encrypt_then_decrypt_round_trip():
    priv, pub_b64 = _generate_recipient_keypair()
    plaintext = b'[{"role":"user","content":"round trip"}]'

    encrypted = encrypt_payload(plaintext, pub_b64)
    assert len(base64.b64decode(encrypted.ephemeral_public_key_b64)) == 65  # uncompressed P-256 point

    recovered = decrypt_payload(encrypted.ciphertext, encrypted.ephemeral_public_key_b64, priv)
    assert recovered == plaintext


def test_ciphertext_blob_layout_is_nonce_then_ciphertext_plus_tag():
    priv, pub_b64 = _generate_recipient_keypair()
    encrypted = encrypt_payload(b"x", pub_b64)
    # 12-byte nonce + at least a 16-byte GCM tag, even for 1 byte of plaintext.
    assert len(encrypted.ciphertext) >= 12 + 16


def test_wrong_recipient_key_fails_to_decrypt():
    _, pub_b64 = _generate_recipient_keypair()
    wrong_priv, _ = _generate_recipient_keypair()
    encrypted = encrypt_payload(b"secret", pub_b64)
    with pytest.raises(Exception):
        decrypt_payload(encrypted.ciphertext, encrypted.ephemeral_public_key_b64, wrong_priv)


@pytest.fixture(scope="module")
def go_helper_binary(tmp_path_factory):
    """Builds the Go interop helper once per test session. Skips (not fails)
    the interop test when the `go` toolchain isn't available — this repo's
    Go module is a prerequisite for the interop proof, not for the SDK
    itself, so a plain `pip install -e .[dev] && pytest` on a machine
    without Go still runs every other test."""
    if shutil.which("go") is None:
        pytest.skip("go toolchain not available — skipping cross-language interop test")
    out = tmp_path_factory.mktemp("go-interop") / "go_interop_helper"
    result = subprocess.run(
        ["go", "build", "-o", str(out), "./python-sdk/tests/go_interop_helper"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        pytest.fail(f"failed to build go_interop_helper: {result.stderr}")
    return str(out)


def test_python_encrypted_payload_decrypts_with_real_go_payloadcrypto(go_helper_binary):
    """Proves encrypt_payload is byte-compatible with
    internal/payloadcrypto.Decrypt — the actual production node-side
    decryptor — not just internally self-consistent."""
    priv = ec.generate_private_key(ec.SECP256R1())
    priv_scalar_b64 = base64.b64encode(priv.private_numbers().private_value.to_bytes(32, "big")).decode("ascii")
    pub_raw = priv.public_key().public_bytes(
        serialization.Encoding.X962, serialization.PublicFormat.UncompressedPoint
    )
    pub_b64 = base64.b64encode(pub_raw).decode("ascii")

    plaintext = b'[{"role":"user","content":"hello from python interop test"}]'
    encrypted = encrypt_payload(plaintext, pub_b64)
    combined_b64 = base64.b64encode(encrypted.ciphertext).decode("ascii")

    result = subprocess.run(
        [go_helper_binary, priv_scalar_b64, encrypted.ephemeral_public_key_b64, combined_b64],
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0, f"go helper failed: {result.stderr}"
    assert result.stdout.encode("utf-8") == plaintext
