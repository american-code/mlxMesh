package wallet

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestCrossLanguageVectors verifies that signatures produced by the iOS
// WalletStore (CryptoKit Curve25519 / Ed25519) are accepted by this Go
// coordinator: same address derivation, same signing messages, same base64
// encoding. Vectors are emitted by scratchpad/walletsign.swift.
//
// Skips cleanly when the vectors file isn't present (CI without the iOS toolchain).
func TestCrossLanguageVectors(t *testing.T) {
	path := os.Getenv("WALLET_VECTORS")
	if path == "" {
		t.Skip("set WALLET_VECTORS to a walletsign.swift output file to run")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no vectors file: %v", err)
	}
	var v struct {
		Address       string `json:"address"`
		Nonce         string `json:"nonce"`
		PublicKey     string `json:"public_key"`
		AuthSignature string `json:"auth_signature"`
		DeviceNodeID  string `json:"device_node_id"`
		LinkSignature string `json:"link_signature"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	pub, _ := base64.StdEncoding.DecodeString(v.PublicKey)
	authSig, _ := base64.StdEncoding.DecodeString(v.AuthSignature)
	linkSig, _ := base64.StdEncoding.DecodeString(v.LinkSignature)

	// 1. Address derived from the Swift public key must match Swift's address.
	if got := AddressFromPubKey(pub); got != v.Address {
		t.Fatalf("address mismatch:\n go:    %s\n swift: %s", got, v.Address)
	}

	m := NewManager()
	now := time.Now()
	// Seed the exact challenge the client signed (the endpoint would have issued
	// this nonce; we inject it directly to isolate the signature check).
	m.challenges[v.Nonce] = Challenge{Address: v.Address, Nonce: v.Nonce, ExpiresAt: now.Add(time.Minute)}

	// 2. Auth signature over "oim-account-auth:<addr>:<nonce>" must verify.
	if err := m.VerifyChallenge(v.Address, v.Nonce, pub, authSig, now); err != nil {
		t.Fatalf("auth signature rejected: %v", err)
	}

	// 3. Link signature over "oim-account-link:<addr>:<device>" must verify.
	if err := m.LinkDevice(v.Address, v.DeviceNodeID, pub, linkSig, now); err != nil {
		t.Fatalf("link signature rejected: %v", err)
	}
	if acct, ok := m.AccountForDevice(v.DeviceNodeID); !ok || acct != v.Address {
		t.Fatalf("device not linked to account: %q %v", acct, ok)
	}
}
