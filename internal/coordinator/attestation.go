package coordinator

import (
	"fmt"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// VerifyEnclaveAttestation checks both signatures on an EnclaveAttestationRequest:
// the outer Ed25519 signature proves the request came from the node that
// already registered NodeID (not an impersonator with their own Secure
// Enclave), and the inner P-256 signature proves EnclavePublicKey is a real
// Secure Enclave key that just signed a fresh, coordinator-bound challenge —
// evidence a self-reported boolean can never provide (Fable security review:
// self-declared attestation, unenforced privacy claims).
func VerifyEnclaveAttestation(registry *NodeRegistry, req protocol.EnclaveAttestationRequest) error {
	if err := protocol.VerifyFreshness(req.Timestamp, time.Now()); err != nil {
		return err
	}
	signingBytes, err := req.SigningBytes()
	if err != nil {
		return fmt.Errorf("build signing bytes: %w", err)
	}
	nodePubKey, ok := registry.PublicKey(req.NodeID)
	if !ok {
		return fmt.Errorf("node %s not registered; cannot verify attestation", req.NodeID)
	}
	if !protocol.VerifySignature(nodePubKey, signingBytes, req.Signature) {
		return fmt.Errorf("node identity signature invalid for %s", req.NodeID)
	}
	if !protocol.VerifyP256Signature(req.EnclavePublicKey, signingBytes, req.EnclaveSignature) {
		return fmt.Errorf("secure enclave signature invalid for %s", req.NodeID)
	}
	return nil
}
