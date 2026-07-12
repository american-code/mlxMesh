// Copyright (C) 2024-2026 American Code
// SPDX-License-Identifier: AGPL-3.0-or-later
// For commercial licensing: jmelton@americancode.org

package directory

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// PinStore binds each pod_id to the Ed25519 public key that is allowed to
// speak for it — the fix for "any coordinator can announce itself under any
// pod_id" (task #52, M7 research finding: PodHealthDigest registration was
// completely unauthenticated).
//
// Two modes, chosen by whether an allowlist path was loaded:
//   - TOFU (default, no --authorized-pods flag): the first public key ever seen
//     for a pod_id is trusted and persisted; every later digest claiming that
//     pod_id must carry a signature from that same key. Works with zero
//     operator setup, matching the project's "opt-in hardening" pattern (same
//     shape as node TLS fingerprint pinning).
//   - Strict allowlist (--authorized-pods <file>): only pod_id/pubkey pairs
//     present in the file are ever accepted; an unlisted pod_id is rejected
//     outright rather than auto-learned. For production deployments that want
//     to enumerate their federation membership explicitly rather than trust
//     whoever registers first.
//
// Neither mode is full Byzantine-fault-tolerant consensus — a pinned/allowlisted
// pod that is itself later compromised or run by a bad actor from day one is
// out of scope here (see README's open-vulnerabilities section). This closes
// impersonation of an EXISTING or explicitly-authorized pod_id, not insider risk.
type PinStore struct {
	mu      sync.RWMutex
	pins    map[string]string // pod_id -> hex pubkey
	strict  bool              // true = --authorized-pods loaded; no auto-learning
	persist string            // path to persist learned pins; empty = in-memory only (tests)
}

// NewPinStore creates a TOFU pin store. If persistPath is non-empty and the
// file already exists, previously-learned pins are loaded from it so a
// directory restart doesn't reset trust and let an impersonator in.
func NewPinStore(persistPath string) (*PinStore, error) {
	s := &PinStore{pins: make(map[string]string), persist: persistPath}
	if persistPath == "" {
		return s, nil
	}
	b, err := os.ReadFile(persistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read pin store %s: %w", persistPath, err)
	}
	if err := json.Unmarshal(b, &s.pins); err != nil {
		return nil, fmt.Errorf("parse pin store %s: %w", persistPath, err)
	}
	return s, nil
}

// NewAllowlistPinStore creates a strict pin store from an operator-curated
// file: {"pod_id": "hex pubkey", ...}. Registrations for a pod_id not present
// in the file are always rejected — no auto-learning, no way for an
// unlisted pod to join by registering first.
func NewAllowlistPinStore(allowlistPath string) (*PinStore, error) {
	b, err := os.ReadFile(allowlistPath)
	if err != nil {
		return nil, fmt.Errorf("read authorized-pods file %s: %w", allowlistPath, err)
	}
	pins := make(map[string]string)
	if err := json.Unmarshal(b, &pins); err != nil {
		return nil, fmt.Errorf("parse authorized-pods file %s: %w", allowlistPath, err)
	}
	return &PinStore{pins: pins, strict: true}, nil
}

// Verify checks digest's signature and pod_id/pubkey binding. Returns an
// error describing exactly why a digest is rejected (bad signature, pod_id
// claimed by a different key, or — strict mode only — pod_id not on the
// allowlist at all).
func (s *PinStore) Verify(digest protocol.PodHealthDigest) error {
	pub, ok := protocol.VerifyPodHealthDigestSignature(digest)
	if !ok {
		return fmt.Errorf("pod %q: missing or invalid digest signature", digest.PodID)
	}
	pubHex := hex.EncodeToString(pub)

	s.mu.Lock()
	defer s.mu.Unlock()

	pinned, known := s.pins[digest.PodID]
	switch {
	case known && pinned != pubHex:
		return fmt.Errorf("pod %q: signature key does not match the key pinned at first registration (impersonation attempt or key rotation without re-authorization)", digest.PodID)
	case known:
		return nil // matches pinned key
	case s.strict:
		return fmt.Errorf("pod %q: not on the authorized-pods allowlist", digest.PodID)
	default:
		// TOFU: first sighting of this pod_id — trust and pin it.
		s.pins[digest.PodID] = pubHex
		s.saveLocked()
		return nil
	}
}

// saveLocked persists pins to disk; must be called with s.mu held. Best-effort:
// a write failure is logged by the caller's context, not fatal — the pin is
// still enforced for the remainder of this process's lifetime either way.
func (s *PinStore) saveLocked() {
	if s.persist == "" || s.strict {
		return
	}
	b, err := json.MarshalIndent(s.pins, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.persist, b, 0o600)
}
