package coordinator

import (
	"fmt"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// VerifyNodeSignature is the shared authentication gate for every node write-path
// request that touches credits or routing state (refresh, benchmark-result,
// job-outcome). It checks that signature is a valid Ed25519 signature over
// signingBytes produced by the SAME keypair nodeID registered with, and that
// timestamp is fresh. Without this gate, anyone can forge requests for any
// node_id — minting credits via job-outcome, hijacking routing via refresh, or
// defeating tier-fraud detection via benchmark-result (Fable security review,
// findings #1/#2).
func VerifyNodeSignature(registry *NodeRegistry, nodeID string, timestamp int64, signingBytes, signature []byte) error {
	if err := protocol.VerifyFreshness(timestamp, time.Now()); err != nil {
		return err
	}
	pubKey, ok := registry.PublicKey(nodeID)
	if !ok {
		return fmt.Errorf("node %s not registered; cannot verify signature", nodeID)
	}
	if !protocol.VerifySignature(pubKey, signingBytes, signature) {
		return fmt.Errorf("signature verification failed for node %s", nodeID)
	}
	return nil
}
