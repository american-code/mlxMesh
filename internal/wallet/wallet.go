// Package wallet provides portable, recoverable account identity for the mesh.
//
// This is NOT an on-chain wallet. Credits live in the coordinator's ledger
// (server-authoritative internal credits — the project has no token/chain).
// What a "wallet" provides here is a keypair that PROVES ownership of a ledger
// balance, so the same account can be used from many devices and recovered
// after a device dies:
//
//   - Account address = AddressFromPubKey(accountPubKey). This deterministic
//     address is what the ledger keys balances on (it replaces the old random
//     per-device UUID), so re-deriving the same account key anywhere yields the
//     same address and therefore the same balance. Nothing about the balance
//     lives on any device — only the key does.
//
//   - Cross-device consolidation: each contributing device (Mac Studio, iPad,
//     …) links its node key to the account once; earnings from any linked
//     device credit the one account. See LinkDevice / AccountForDevice.
//
//   - Recovery: whoever can sign a challenge with the account key controls the
//     balance. Import the account key (via iCloud Keychain or a seed phrase —
//     handled client-side) on a new machine, pass the challenge, done. The
//     coordinator never held the key, so it cannot and does not "reset" an
//     account — that is the deliberate wallet tradeoff.
package wallet

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// AddressPrefix visually distinguishes account addresses from raw node IDs.
const AddressPrefix = "oim"

// ChallengeTTL bounds how long an issued auth challenge stays valid. Short
// enough to limit replay windows, long enough to survive a signing round-trip.
const ChallengeTTL = 5 * time.Minute

var (
	ErrUnknownChallenge   = errors.New("wallet: no outstanding challenge for that nonce")
	ErrChallengeExpired   = errors.New("wallet: challenge expired")
	ErrKeyAddressMismatch = errors.New("wallet: public key does not derive to the claimed address")
	ErrBadSignature       = errors.New("wallet: signature verification failed")
)

// AddressFromPubKey derives a stable account address from an Ed25519 account
// public key: AddressPrefix + hex(SHA-256(pubKey)). Deterministic, so the same
// account key always maps to the same address (and thus the same ledger row) on
// every device and after any recovery.
func AddressFromPubKey(pubKey []byte) string {
	sum := sha256.Sum256(pubKey)
	return AddressPrefix + hex.EncodeToString(sum[:])
}

// Challenge is a one-time nonce a client must sign with its account key to prove
// ownership of an address.
type Challenge struct {
	Address   string    `json:"address"`
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SigningMessage is the exact bytes a client signs for this challenge. Namespaced
// so a signature captured here can never be replayed as a device-link or any
// other signature.
func (c Challenge) SigningMessage() []byte {
	return []byte("oim-account-auth:" + c.Address + ":" + c.Nonce)
}

// deviceLinkMessage is what an ACCOUNT key signs to authorize a device (node)
// to earn into the account. Namespaced distinctly from auth challenges.
func deviceLinkMessage(accountAddress, deviceNodeID string) []byte {
	return []byte("oim-account-link:" + accountAddress + ":" + deviceNodeID)
}

// Manager holds outstanding challenges and device→account links. Safe for
// concurrent use. Time is injected into Issue/Verify so behavior is
// deterministic and testable without sleeping.
type Manager struct {
	mu          sync.Mutex
	challenges  map[string]Challenge // keyed by nonce
	deviceLinks map[string]string    // deviceNodeID → accountAddress
}

func NewManager() *Manager {
	return &Manager{
		challenges:  make(map[string]Challenge),
		deviceLinks: make(map[string]string),
	}
}

// IssueChallenge creates and stores a fresh challenge for address, valid until
// now+ChallengeTTL. Also opportunistically evicts expired challenges.
func (m *Manager) IssueChallenge(address string, now time.Time) (Challenge, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Challenge{}, fmt.Errorf("wallet: generate nonce: %w", err)
	}
	c := Challenge{
		Address:   address,
		Nonce:     hex.EncodeToString(raw),
		ExpiresAt: now.Add(ChallengeTTL),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for nonce, existing := range m.challenges {
		if now.After(existing.ExpiresAt) {
			delete(m.challenges, nonce)
		}
	}
	m.challenges[c.Nonce] = c
	return c, nil
}

// VerifyChallenge authenticates a client as the owner of address. On success it
// consumes the nonce (one-time use) and returns nil; the caller then mints a
// session token bound to address. Checks, in order:
//  1. accountPubKey actually derives to the claimed address (binds key↔address)
//  2. an unexpired challenge exists for this nonce
//  3. the signature is valid over the challenge's namespaced message
func (m *Manager) VerifyChallenge(address, nonce string, accountPubKey, signature []byte, now time.Time) error {
	if AddressFromPubKey(accountPubKey) != address {
		return ErrKeyAddressMismatch
	}

	m.mu.Lock()
	c, ok := m.challenges[nonce]
	if ok {
		// Consume immediately (even on later failure) so a nonce is strictly
		// one-shot — a captured nonce can't be retried after a failed attempt.
		delete(m.challenges, nonce)
	}
	m.mu.Unlock()

	if !ok || c.Address != address {
		return ErrUnknownChallenge
	}
	if now.After(c.ExpiresAt) {
		return ErrChallengeExpired
	}
	if !protocol.VerifySignature(accountPubKey, c.SigningMessage(), signature) {
		return ErrBadSignature
	}
	return nil
}

// LinkDevice binds a contributing device's node ID to an account so the device's
// earnings credit that account. The binding must be authorized by a signature
// from the ACCOUNT key (proving the account owner consents), preventing anyone
// from attaching their device to someone else's balance.
func (m *Manager) LinkDevice(accountAddress, deviceNodeID string, accountPubKey, linkSig []byte, now time.Time) error {
	if AddressFromPubKey(accountPubKey) != accountAddress {
		return ErrKeyAddressMismatch
	}
	if !protocol.VerifySignature(accountPubKey, deviceLinkMessage(accountAddress, deviceNodeID), linkSig) {
		return ErrBadSignature
	}
	m.mu.Lock()
	m.deviceLinks[deviceNodeID] = accountAddress
	m.mu.Unlock()
	return nil
}

// AccountForDevice returns the account a device's earnings credit, if linked.
// A device with no link returns ("", false) — its earnings fall back to
// whatever user_id it registered with, exactly as before wallets existed.
func (m *Manager) AccountForDevice(deviceNodeID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	addr, ok := m.deviceLinks[deviceNodeID]
	return addr, ok
}

// UnlinkDevice revokes a device→account binding. Requires an account-key
// signature so only the account owner can revoke (e.g. a lost/stolen device).
func (m *Manager) UnlinkDevice(accountAddress, deviceNodeID string, accountPubKey, sig []byte) error {
	if AddressFromPubKey(accountPubKey) != accountAddress {
		return ErrKeyAddressMismatch
	}
	if !protocol.VerifySignature(accountPubKey, deviceLinkMessage(accountAddress, deviceNodeID), sig) {
		return ErrBadSignature
	}
	m.mu.Lock()
	if m.deviceLinks[deviceNodeID] == accountAddress {
		delete(m.deviceLinks, deviceNodeID)
	}
	m.mu.Unlock()
	return nil
}
