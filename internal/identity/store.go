// Package identity persists the node's Ed25519 keypair across sessions.
// The node_id is always derived from the public key — never operator-chosen.
package identity

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

const configDir = ".config/oim"
const identityFile = "node_identity.json"

type storedIdentity struct {
	PrivateKey string `json:"private_key"` // hex-encoded Ed25519
	PublicKey  string `json:"public_key"`  // hex-encoded Ed25519
	// ECDHPrivateKey is the node's P-256 key-agreement private key (hex-encoded
	// scalar) — used to decrypt encrypted-pointer payloads addressed to this
	// node (internal/payloadcrypto). A separate keypair from the Ed25519
	// identity above: Ed25519 signing keys cannot be used for ECDH. Empty on
	// identity files created before this field existed; LoadOrCreateECDH
	// backfills it in place on first use after upgrade.
	ECDHPrivateKey string `json:"ecdh_private_key,omitempty"`
}

// LoadOrCreate loads the keypair from disk, generating and persisting it on first run.
func LoadOrCreate() (privateKey, publicKey []byte, err error) {
	path := identityPath()
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return generate(path)
	}
	return load(path)
}

func identityPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, configDir, identityFile)
}

func generate(path string) (privateKey, publicKey []byte, err error) {
	priv, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		return nil, nil, fmt.Errorf("generate identity: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create config dir: %w", err)
	}
	stored := storedIdentity{
		PrivateKey: fmt.Sprintf("%x", priv),
		PublicKey:  fmt.Sprintf("%x", pub),
	}
	b, _ := json.MarshalIndent(stored, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, nil, fmt.Errorf("write identity: %w", err)
	}
	return priv, pub, nil
}

func load(path string) (privateKey, publicKey []byte, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read identity: %w", err)
	}
	var stored storedIdentity
	if err := json.Unmarshal(b, &stored); err != nil {
		return nil, nil, fmt.Errorf("parse identity: %w", err)
	}
	priv, err := hex.DecodeString(stored.PrivateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("decode private key: %w", err)
	}
	pub, err := hex.DecodeString(stored.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("decode public key: %w", err)
	}
	return priv, pub, nil
}

// LoadOrCreateECDH loads this node's P-256 key-agreement keypair, generating
// and persisting it — alongside the existing Ed25519 identity, in the same
// file — on first use. Ensures the identity file exists first (idempotent
// with LoadOrCreate), so callers may call this independently of load order.
func LoadOrCreateECDH() (*ecdh.PrivateKey, error) {
	path := identityPath()
	if _, _, err := LoadOrCreate(); err != nil {
		return nil, fmt.Errorf("ensure identity: %w", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read identity: %w", err)
	}
	var stored storedIdentity
	if err := json.Unmarshal(b, &stored); err != nil {
		return nil, fmt.Errorf("parse identity: %w", err)
	}
	if stored.ECDHPrivateKey != "" {
		raw, err := hex.DecodeString(stored.ECDHPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("decode ecdh private key: %w", err)
		}
		priv, err := ecdh.P256().NewPrivateKey(raw)
		if err != nil {
			return nil, fmt.Errorf("parse ecdh private key: %w", err)
		}
		return priv, nil
	}
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ecdh key: %w", err)
	}
	stored.ECDHPrivateKey = hex.EncodeToString(priv.Bytes())
	nb, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode identity: %w", err)
	}
	if err := os.WriteFile(path, nb, 0o600); err != nil {
		return nil, fmt.Errorf("persist ecdh key: %w", err)
	}
	return priv, nil
}
