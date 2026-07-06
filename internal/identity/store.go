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
	return LoadOrCreateAt(identityPath())
}

// LoadOrCreateAt is LoadOrCreate against an explicit path instead of the fixed
// per-user node identity file. Coordinators use this (a coordinator's identity
// file is named/located independently of a node's, and a single host can run
// more than one coordinator — e.g. pod-us and pod-eu — each needing its own
// keypair) but the load/generate logic is identical, so it's shared rather
// than duplicated.
func LoadOrCreateAt(path string) (privateKey, publicKey []byte, err error) {
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
// file — on first use. Callers may call this independently of load order
// (LoadOrCreate isn't a prerequisite): a single read is enough on every
// startup after the first, since the identity file already exists; only the
// very first-ever startup pays a second read, to let LoadOrCreate generate
// the Ed25519 identity before this backfills the ECDH key onto it.
func LoadOrCreateECDH() (*ecdh.PrivateKey, error) {
	path := identityPath()
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read identity: %w", err)
		}
		if _, _, err := LoadOrCreate(); err != nil {
			return nil, fmt.Errorf("ensure identity: %w", err)
		}
		if b, err = os.ReadFile(path); err != nil {
			return nil, fmt.Errorf("read identity: %w", err)
		}
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
	// os.WriteFile's mode argument only applies when it CREATES the file; on an
	// already-existing file (the normal case here — LoadOrCreate() above just
	// ensured it exists) it truncates content without touching permissions. If
	// this file ever existed with looser permissions (e.g. restored from a
	// backup, or manually chmod'd), the private key rewrite here would
	// silently leave it that way. Chmod explicitly so this rewrite always
	// tightens to 0600 regardless of the file's prior mode.
	if err := os.WriteFile(path, nb, 0o600); err != nil {
		return nil, fmt.Errorf("persist ecdh key: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("tighten identity file permissions: %w", err)
	}
	return priv, nil
}
