import hashlib

from mlxmesh.pow import mine, verify_proof_of_work


def test_mine_produces_a_valid_solution_at_low_difficulty():
    # Low difficulty (8-12 bits) keeps the test fast — the algorithm is
    # identical at any difficulty, only the search space changes.
    for bits in (8, 10, 12):
        nonce = mine("test-user", bits)
        assert verify_proof_of_work("test-user", nonce, bits)


def _leading_zero_bits(user_id: str, nonce: int) -> int:
    digest = hashlib.sha256(user_id.encode() + nonce.to_bytes(8, "big")).digest()
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


def test_verify_is_exact_at_the_boundary():
    # Deterministic, non-probabilistic check that verify_proof_of_work
    # actually discriminates: a nonce with exactly N leading zero bits must
    # pass at difficulty N and fail at N+1, not just "usually pass."
    nonce = mine("boundary-user", 10)
    actual_bits = _leading_zero_bits("boundary-user", nonce)
    assert verify_proof_of_work("boundary-user", nonce, actual_bits)
    assert not verify_proof_of_work("boundary-user", nonce, actual_bits + 1)


def test_zero_difficulty_always_passes():
    assert verify_proof_of_work("anyone", 0, 0)
    assert verify_proof_of_work("anyone", 123456, 0)


def test_user_id_is_mixed_into_the_hash():
    # A solution for one user_id must not carry over to a different one at
    # the same difficulty — otherwise user_id wouldn't actually be
    # contributing to the hash input.
    nonce = mine("user-a", 14)
    assert verify_proof_of_work("user-a", nonce, 14)
    assert not verify_proof_of_work("user-b", nonce, 14)
