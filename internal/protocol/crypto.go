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

package protocol

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// GenerateNodeIdentity creates a new Ed25519 keypair.
// NodeID must always be derived via NodeIDFromPubKey — never operator-chosen,
// so identity cannot be re-rolled to escape a bad reputation history.
func GenerateNodeIdentity() (privateKey, publicKey []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return []byte(priv), []byte(pub), nil
}

// NodeIDFromPubKey derives a stable, collision-resistant node ID.
// Returns the first 32 hex characters of SHA-256(pubKey).
func NodeIDFromPubKey(pubKey []byte) string {
	sum := sha256.Sum256(pubKey)
	return hex.EncodeToString(sum[:])[:32]
}

// SignPayload signs payload with an Ed25519 private key.
// Used for NodeRegistration.Signature and settlement records.
func SignPayload(privateKey, payload []byte) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid private key: got %d bytes, want %d", len(privateKey), ed25519.PrivateKeySize)
	}
	return ed25519.Sign(ed25519.PrivateKey(privateKey), payload), nil
}

// VerifySignature returns true if signature is valid for payload under publicKey.
func VerifySignature(publicKey, payload, signature []byte) bool {
	if len(publicKey) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature)
}

// VerifyP256Signature verifies a DER-encoded ECDSA-P256 signature over
// SHA-256(payload). Used to check Secure Enclave attestation proofs — Apple's
// Security framework only exposes Secure Enclave-backed keys as P-256, never
// Ed25519, so this is a separate curve/verify path from VerifySignature.
// rawPubKey is the X9.63 uncompressed point (0x04 || X || Y, 65 bytes).
func VerifyP256Signature(rawPubKey, payload, derSignature []byte) bool {
	x, y := elliptic.Unmarshal(elliptic.P256(), rawPubKey)
	if x == nil {
		return false
	}
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
	digest := sha256.Sum256(payload)
	return ecdsa.VerifyASN1(pub, digest[:], derSignature)
}

// CheckSecureEnclaveAvailable returns true if this device has a Secure Enclave.
// This is a CAPABILITY CHECK, not a confidentiality guarantee — a true return
// means the node is eligible for SensitivityHighRequiresAttestation jobs,
// not that the operator cannot see plaintext during inference (proposal §8.1).
func CheckSecureEnclaveAvailable() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	out, err := exec.Command("sysctl", "-n", "hw.optional.arm64").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}
