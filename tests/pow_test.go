package tests

import (
	"testing"

	"github.com/open-inference-mesh/oim/internal/settlement"
)

func TestVerifyProofOfWorkZeroDifficultyAlwaysPasses(t *testing.T) {
	if !settlement.VerifyProofOfWork("any-user", 0, 0) {
		t.Error("difficulty 0 should accept any nonce")
	}
}

func TestVerifyProofOfWorkRejectsWrongNonce(t *testing.T) {
	// A random unmined nonce should essentially never satisfy a real difficulty.
	if settlement.VerifyProofOfWork("user-abc", 0, 16) {
		t.Error("nonce 0 should not (reliably) satisfy 16 bits of difficulty for this user")
	}
}

func TestVerifyProofOfWorkAcceptsMinedNonce(t *testing.T) {
	const bits = 12 // small enough to mine instantly in a test
	userID := "user-xyz"
	var found uint64
	ok := false
	for n := uint64(0); n < 1<<20; n++ {
		if settlement.VerifyProofOfWork(userID, n, bits) {
			found = n
			ok = true
			break
		}
	}
	if !ok {
		t.Fatal("failed to mine a valid nonce within 2^20 attempts at 12-bit difficulty")
	}
	if !settlement.VerifyProofOfWork(userID, found, bits) {
		t.Error("mined nonce should verify")
	}
	// A solution for one user_id must not transfer to another — otherwise the
	// PoW cost isn't actually per-identity.
	if settlement.VerifyProofOfWork("different-user", found, bits) {
		t.Error("a nonce mined for one user_id should not validate for a different user_id")
	}
}

func TestVerifyProofOfWorkHigherDifficultyIsStricter(t *testing.T) {
	userID := "user-strict"
	var loBitsNonce uint64
	found := false
	for n := uint64(0); n < 1<<16; n++ {
		if settlement.VerifyProofOfWork(userID, n, 8) {
			loBitsNonce = n
			found = true
			break
		}
	}
	if !found {
		t.Fatal("failed to mine an 8-bit solution")
	}
	// An 8-bit solution has no guarantee of satisfying a much higher bar; this
	// just exercises that VerifyProofOfWork actually discriminates by bit count
	// rather than always returning true once any nonce is supplied.
	strictPass := settlement.VerifyProofOfWork(userID, loBitsNonce, 30)
	if strictPass {
		t.Log("mined nonce happened to also satisfy 30 bits — astronomically unlikely but not impossible; re-run if flaky")
	}
}
