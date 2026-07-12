// Package adminauth implements challenge-response login for the single,
// fixed "BDFL" administrator identity — separate from the per-account
// wallet identities in internal/wallet and the per-node identities in
// internal/coordinator's NodeRegistry, but built on the exact same
// verify-only idiom already established by both: the coordinator is
// configured with the administrator's Ed25519 PUBLIC key only (an
// operator-supplied flag, generated entirely offline — see
// `oim admin keygen` in cmd/oim) and never generates or holds the matching
// private key. Losing coordinator disk access does not hand over admin
// authority, unlike a static shared bearer secret would.
package adminauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// ChallengeTTL matches internal/wallet's ChallengeTTL — short enough to
// bound a captured nonce's replay window, long enough to survive a real
// signing round-trip (including the "paste the private key, sign, submit"
// flow the dashboard uses).
const ChallengeTTL = 5 * time.Minute

// SessionTTL is deliberately much shorter than a typical session token
// elsewhere in this codebase (personal API keys have no expiry at all) —
// an admin session grants treasury-adjustment and node-deregistration
// authority, so it stays high-privilege-short-lived rather than
// indefinite.
const SessionTTL = 1 * time.Hour

// SessionTokenPrefix visually distinguishes admin sessions from per-user
// oim_ API keys and node/account identifiers at a glance in logs.
const SessionTokenPrefix = "oimadmin_"

var (
	ErrUnknownChallenge = errors.New("adminauth: no outstanding challenge for that nonce")
	ErrChallengeExpired = errors.New("adminauth: challenge expired")
	ErrBadSignature     = errors.New("adminauth: signature verification failed")
	ErrNotConfigured    = errors.New("adminauth: no BDFL public key configured")
)

// signingMessage is the exact bytes the BDFL private key must sign.
// Namespaced so a captured signature can never be replayed as some other
// action's signature (the same reasoning as wallet.Challenge.SigningMessage
// and deviceLinkMessage).
func signingMessage(nonce string) []byte {
	return []byte("oim-admin-auth:" + nonce)
}

// Authenticator verifies challenge-response logins against a single fixed
// Ed25519 public key. A zero-value/unconfigured Authenticator (publicKey
// nil) fails every check closed — mirrors adminAuthorized's existing "no
// key configured -> always false" precedent in cmd/coordinator/main.go, so
// a deployment that hasn't set up a BDFL key simply has no working admin
// login rather than an insecure default.
type Authenticator struct {
	publicKey ed25519.PublicKey

	mu         sync.Mutex
	challenges map[string]time.Time // nonce -> expiresAt, one-shot
	sessions   map[string]time.Time // token -> expiresAt
}

// NewAuthenticator parses bdflPublicKeyHex (hex-encoded Ed25519 public key,
// as printed by `oim admin keygen`). An empty string is valid and means
// "unconfigured" (see ErrNotConfigured on every subsequent call) rather
// than an error, so the coordinator can start up fine without a BDFL key
// configured — the admin panel is just unreachable until one is set.
func NewAuthenticator(bdflPublicKeyHex string) (*Authenticator, error) {
	a := &Authenticator{
		challenges: make(map[string]time.Time),
		sessions:   make(map[string]time.Time),
	}
	if bdflPublicKeyHex == "" {
		return a, nil
	}
	raw, err := hex.DecodeString(bdflPublicKeyHex)
	if err != nil {
		return nil, fmt.Errorf("adminauth: decode BDFL public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("adminauth: BDFL public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	a.publicKey = ed25519.PublicKey(raw)
	return a, nil
}

// Configured reports whether a BDFL public key was supplied — lets callers
// (e.g. a health/status endpoint) distinguish "feature not set up" from
// "feature set up but this login attempt failed."
func (a *Authenticator) Configured() bool {
	return len(a.publicKey) == ed25519.PublicKeySize
}

// IssueChallenge creates and stores a fresh nonce, valid until
// now+ChallengeTTL. Also opportunistically evicts expired challenges and
// sessions — same pattern as wallet.Manager.IssueChallenge, no separate GC
// goroutine needed at this scale.
func (a *Authenticator) IssueChallenge(now time.Time) (nonce string, expiresAt time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("adminauth: generate nonce: %w", err)
	}
	nonce = hex.EncodeToString(raw)
	expiresAt = now.Add(ChallengeTTL)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.evictExpiredLocked(now)
	a.challenges[nonce] = expiresAt
	return nonce, expiresAt, nil
}

// VerifyAndIssueSession authenticates nonce+signature against the
// configured BDFL public key. On success it consumes the nonce (one-time
// use, exactly like wallet.Manager.VerifyChallenge — deleted immediately
// even if a later check in this same call were to fail, so a captured
// nonce can never be retried) and mints a fresh session token.
func (a *Authenticator) VerifyAndIssueSession(nonce string, signature []byte, now time.Time) (token string, expiresAt time.Time, err error) {
	if !a.Configured() {
		return "", time.Time{}, ErrNotConfigured
	}

	a.mu.Lock()
	challengeExpiry, ok := a.challenges[nonce]
	if ok {
		delete(a.challenges, nonce) // one-shot, consumed regardless of outcome below
	}
	a.mu.Unlock()

	if !ok {
		return "", time.Time{}, ErrUnknownChallenge
	}
	if now.After(challengeExpiry) {
		return "", time.Time{}, ErrChallengeExpired
	}
	if !protocol.VerifySignature(a.publicKey, signingMessage(nonce), signature) {
		return "", time.Time{}, ErrBadSignature
	}

	raw := make([]byte, 20) // same byte length as per-user oim_ API keys
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("adminauth: generate session token: %w", err)
	}
	token = SessionTokenPrefix + hex.EncodeToString(raw)
	expiresAt = now.Add(SessionTTL)

	a.mu.Lock()
	a.evictExpiredLocked(now)
	a.sessions[token] = expiresAt
	a.mu.Unlock()
	return token, expiresAt, nil
}

// ValidSession reports whether token is a currently-valid admin session.
func (a *Authenticator) ValidSession(token string, now time.Time) bool {
	if token == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	expiresAt, ok := a.sessions[token]
	if !ok {
		return false
	}
	if now.After(expiresAt) {
		delete(a.sessions, token)
		return false
	}
	return true
}

// evictExpiredLocked removes expired challenges/sessions. Caller must hold a.mu.
func (a *Authenticator) evictExpiredLocked(now time.Time) {
	for nonce, expiresAt := range a.challenges {
		if now.After(expiresAt) {
			delete(a.challenges, nonce)
		}
	}
	for token, expiresAt := range a.sessions {
		if now.After(expiresAt) {
			delete(a.sessions, token)
		}
	}
}
