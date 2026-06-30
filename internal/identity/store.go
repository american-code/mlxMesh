// Package identity persists the node's Ed25519 keypair across sessions.
// The node_id is always derived from the public key — never operator-chosen.
package identity

import (
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
	PrivateKey string `json:"private_key"` // hex-encoded
	PublicKey  string `json:"public_key"`  // hex-encoded
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
