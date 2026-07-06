// Copyright (C) 2024 Open Inference Mesh
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

// Package federation lets pod coordinators witness each other's credit
// issuance so a forked or compromised coordinator's claims are checkable
// against its own signed history instead of just trusted outright (task #52,
// M7). This is deliberately NOT full Byzantine-fault-tolerant consensus or
// staking/slashing — the project has no native token to stake (see README's
// no-native-token design philosophy) and a federation of a handful of
// operator-run pods doesn't need Nakamoto-style open consensus. What it does
// close: a pod can no longer report a balance that contradicts its own
// previously-signed credit history without every peer that's witnessed it
// being able to prove the contradiction.
package federation

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// EventType distinguishes the two ways a ledger entry comes into existence —
// mirrors settlement.CreditOrigin, kept as a separate type so this package
// doesn't need to import settlement just for two string constants.
type EventType string

const (
	EventStartupGrant  EventType = "startup_grant"
	EventEarnedContrib EventType = "earned"
)

// LedgerEvent is one signed, sequenced entry in a pod's append-only credit
// history. Sequence is per-pod and strictly increasing — a gap or a
// resigned-differently entry at a previously-seen sequence number is
// evidence of tampering or a fork, not something a witness should paper over.
type LedgerEvent struct {
	PodID     string    `json:"pod_id"`
	Sequence  uint64    `json:"sequence"`
	EventType EventType `json:"event_type"`
	UserID    string    `json:"user_id"`
	Amount    float64   `json:"amount"`
	IssuedAt  string    `json:"issued_at"` // RFC3339Nano, UTC
	PublicKey string    `json:"public_key"`
	Signature string    `json:"signature,omitempty"`
}

// signableBytes returns the canonical bytes to sign/verify: the event with
// Signature cleared, so the signature never covers itself.
func (e LedgerEvent) signableBytes() ([]byte, error) {
	clean := e
	clean.Signature = ""
	return json.Marshal(clean)
}

// Sign returns a copy of e with PublicKey/Signature set from the given
// coordinator identity keypair. Call last — mutating any other field
// afterward invalidates the signature.
func Sign(e LedgerEvent, privateKey, publicKey []byte) (LedgerEvent, error) {
	e.PublicKey = hex.EncodeToString(publicKey)
	e.Signature = ""
	payload, err := e.signableBytes()
	if err != nil {
		return e, fmt.Errorf("marshal event for signing: %w", err)
	}
	sig, err := protocol.SignPayload(privateKey, payload)
	if err != nil {
		return e, fmt.Errorf("sign event: %w", err)
	}
	e.Signature = hex.EncodeToString(sig)
	return e, nil
}

// VerifySelfConsistent checks that e.Signature is a valid signature by
// e.PublicKey over e's other fields. It does NOT check that PublicKey is the
// key authorized to speak for e.PodID — callers (Store.StoreWitnessedEvent)
// own that decision by checking against a pinned key, exactly like
// protocol.VerifyPodHealthDigestSignature/directory.PinStore.
func VerifySelfConsistent(e LedgerEvent) (publicKey []byte, ok bool) {
	if e.PublicKey == "" || e.Signature == "" {
		return nil, false
	}
	pub, err := hex.DecodeString(e.PublicKey)
	if err != nil {
		return nil, false
	}
	sig, err := hex.DecodeString(e.Signature)
	if err != nil {
		return nil, false
	}
	payload, err := e.signableBytes()
	if err != nil {
		return nil, false
	}
	if !protocol.VerifySignature(pub, payload, sig) {
		return nil, false
	}
	return pub, true
}
