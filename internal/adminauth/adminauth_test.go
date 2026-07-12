package adminauth

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// newBDFLKeypair generates a real Ed25519 keypair, mirroring what
// `oim admin keygen` produces, and returns the raw private key plus the
// hex-encoded public key an Authenticator is configured with.
func newBDFLKeypair(t *testing.T) (priv []byte, pubHex string) {
	t.Helper()
	priv, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	return priv, hex.EncodeToString(pub)
}

func sign(t *testing.T, priv, msg []byte) []byte {
	t.Helper()
	sig, err := protocol.SignPayload(priv, msg)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return sig
}

func TestNewAuthenticator_EmptyKeyIsUnconfiguredNotError(t *testing.T) {
	a, err := NewAuthenticator("")
	if err != nil {
		t.Fatalf("expected no error for empty (unconfigured) key, got %v", err)
	}
	if a.Configured() {
		t.Fatal("expected Configured() == false")
	}
}

func TestNewAuthenticator_RejectsMalformedKey(t *testing.T) {
	if _, err := NewAuthenticator("not-hex!!"); err == nil {
		t.Fatal("expected an error for invalid hex")
	}
	if _, err := NewAuthenticator("aabb"); err == nil {
		t.Fatal("expected an error for a key of the wrong length")
	}
}

func TestVerifyAndIssueSession_FailsClosedWhenUnconfigured(t *testing.T) {
	a, err := NewAuthenticator("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	nonce, _, err := a.IssueChallenge(now)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = a.VerifyAndIssueSession(nonce, []byte("anything"), now)
	if err != ErrNotConfigured {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestChallengeResponseHappyPath(t *testing.T) {
	priv, pubHex := newBDFLKeypair(t)
	a, err := NewAuthenticator(pubHex)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)

	nonce, expiresAt, err := a.IssueChallenge(now)
	if err != nil {
		t.Fatal(err)
	}
	if !expiresAt.Equal(now.Add(ChallengeTTL)) {
		t.Fatalf("expected expiry now+%s, got %s", ChallengeTTL, expiresAt)
	}

	sig := sign(t, priv, signingMessage(nonce))
	token, sessionExpiry, err := a.VerifyAndIssueSession(nonce, sig, now.Add(time.Second))
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty session token")
	}
	if len(token) <= len(SessionTokenPrefix) || token[:len(SessionTokenPrefix)] != SessionTokenPrefix {
		t.Fatalf("expected token prefixed with %q, got %q", SessionTokenPrefix, token)
	}
	if !sessionExpiry.Equal(now.Add(time.Second).Add(SessionTTL)) {
		t.Fatalf("expected session expiry now+%s, got %s", SessionTTL, sessionExpiry)
	}
	if !a.ValidSession(token, now.Add(2*time.Second)) {
		t.Fatal("expected the freshly-issued session to be valid")
	}
}

func TestVerifyAndIssueSession_NonceIsOneShot(t *testing.T) {
	priv, pubHex := newBDFLKeypair(t)
	a, _ := NewAuthenticator(pubHex)
	now := time.Unix(1_800_000_000, 0)

	nonce, _, _ := a.IssueChallenge(now)
	sig := sign(t, priv, signingMessage(nonce))

	if _, _, err := a.VerifyAndIssueSession(nonce, sig, now); err != nil {
		t.Fatalf("first use should succeed, got %v", err)
	}
	if _, _, err := a.VerifyAndIssueSession(nonce, sig, now); err != ErrUnknownChallenge {
		t.Fatalf("expected ErrUnknownChallenge on nonce replay, got %v", err)
	}
}

func TestVerifyAndIssueSession_ConsumesNonceEvenOnBadSignature(t *testing.T) {
	// A captured nonce must not be retryable after a failed attempt either —
	// same "consume regardless of outcome" guarantee as wallet.Manager.
	priv, pubHex := newBDFLKeypair(t)
	a, _ := NewAuthenticator(pubHex)
	now := time.Unix(1_800_000_000, 0)

	nonce, _, _ := a.IssueChallenge(now)
	badSig := sign(t, priv, []byte("wrong message entirely"))
	if _, _, err := a.VerifyAndIssueSession(nonce, badSig, now); err != ErrBadSignature {
		t.Fatalf("expected ErrBadSignature, got %v", err)
	}
	goodSig := sign(t, priv, signingMessage(nonce))
	if _, _, err := a.VerifyAndIssueSession(nonce, goodSig, now); err != ErrUnknownChallenge {
		t.Fatalf("expected the nonce to already be consumed (ErrUnknownChallenge), got %v", err)
	}
}

func TestVerifyAndIssueSession_ExpiredChallengeRejected(t *testing.T) {
	priv, pubHex := newBDFLKeypair(t)
	a, _ := NewAuthenticator(pubHex)
	now := time.Unix(1_800_000_000, 0)

	nonce, _, _ := a.IssueChallenge(now)
	sig := sign(t, priv, signingMessage(nonce))
	later := now.Add(ChallengeTTL).Add(time.Second)
	if _, _, err := a.VerifyAndIssueSession(nonce, sig, later); err != ErrChallengeExpired {
		t.Fatalf("expected ErrChallengeExpired, got %v", err)
	}
}

func TestVerifyAndIssueSession_WrongKeySignatureRejected(t *testing.T) {
	_, pubHex := newBDFLKeypair(t) // the "real" configured key
	wrongPriv, _ := newBDFLKeypair(t)
	a, _ := NewAuthenticator(pubHex)
	now := time.Unix(1_800_000_000, 0)

	nonce, _, _ := a.IssueChallenge(now)
	sig := sign(t, wrongPriv, signingMessage(nonce))
	if _, _, err := a.VerifyAndIssueSession(nonce, sig, now); err != ErrBadSignature {
		t.Fatalf("expected ErrBadSignature for a signature from a different keypair, got %v", err)
	}
}

func TestValidSession_UnknownOrExpiredTokenRejected(t *testing.T) {
	priv, pubHex := newBDFLKeypair(t)
	a, _ := NewAuthenticator(pubHex)
	now := time.Unix(1_800_000_000, 0)

	if a.ValidSession("oimadmin_nonexistent", now) {
		t.Fatal("expected an unknown token to be invalid")
	}

	nonce, _, _ := a.IssueChallenge(now)
	sig := sign(t, priv, signingMessage(nonce))
	token, _, err := a.VerifyAndIssueSession(nonce, sig, now)
	if err != nil {
		t.Fatal(err)
	}
	if !a.ValidSession(token, now.Add(SessionTTL-time.Second)) {
		t.Fatal("expected the session to still be valid just under its TTL")
	}
	if a.ValidSession(token, now.Add(SessionTTL+time.Second)) {
		t.Fatal("expected the session to be invalid once past its TTL")
	}
}
