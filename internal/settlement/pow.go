package settlement

import (
	"crypto/sha256"
	"encoding/binary"
)

// DefaultGrantPoWBits is the default proof-of-work difficulty for startup-grant
// claims: the caller must find a nonce such that sha256(userID || nonce) has at
// least this many leading zero bits. 18 bits averages ~262k SHA-256 hashes
// (sub-second on real hardware, a few hundred ms in a browser) — cheap enough
// not to annoy a real user claiming their one grant, expensive enough that
// minting thousands of disposable identities (the actual attack: userID is a
// free client-generated UUID — see AccountView.tsx getOrCreateUserId) costs
// real, non-parallelizable-for-free wall-clock time (Fable security review:
// Sybil-farmable grants).
const DefaultGrantPoWBits = 18

// VerifyProofOfWork reports whether nonce is a valid hashcash-style solution
// for userID at the given difficulty. Deterministic and side-effect-free so
// the coordinator can check it inline on every grant claim.
func VerifyProofOfWork(userID string, nonce uint64, difficultyBits int) bool {
	if difficultyBits <= 0 {
		return true
	}
	h := sha256.New()
	h.Write([]byte(userID))
	var nb [8]byte
	binary.BigEndian.PutUint64(nb[:], nonce)
	h.Write(nb[:])
	sum := h.Sum(nil)
	return leadingZeroBits(sum) >= difficultyBits
}

func leadingZeroBits(sum []byte) int {
	count := 0
	for _, b := range sum {
		if b == 0 {
			count += 8
			continue
		}
		for mask := byte(0x80); mask != 0; mask >>= 1 {
			if b&mask != 0 {
				return count
			}
			count++
		}
	}
	return count
}
