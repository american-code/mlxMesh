package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/open-inference-mesh/oim/internal/attestation"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// ReportJobOutcome posts a signed job completion record to the pod coordinator.
// tokensDelivered is read from the Exo response usage field when available;
// pass 0 when the node could not determine the count.
// Signed with priv (this node's registration keypair) — this is the earning side
// of the credit ledger, so an unsigned report would be a free credit-minting oracle.
// Non-fatal on error — reporting failure must not stop the agent.
func ReportJobOutcome(ctx context.Context, coordinatorURL, nodeID string, priv []byte, jobID string, success bool, latencyMs float64, tokensDelivered int) error {
	req := protocol.JobOutcomeRequest{
		JobID:           jobID,
		Success:         success,
		LatencyMs:       latencyMs,
		TokensDelivered: tokensDelivered,
		Timestamp:       time.Now().Unix(),
	}
	signingBytes, err := req.SigningBytes()
	if err != nil {
		return fmt.Errorf("build signing bytes: %w", err)
	}
	sig, err := protocol.SignPayload(priv, signingBytes)
	if err != nil {
		return fmt.Errorf("sign job outcome: %w", err)
	}
	req.Signature = sig
	return postJSON(ctx, coordinatorURL+"/nodes/"+nodeID+"/job-outcome", req)
}

// SubmitBenchmarkResult posts a signed, freshly measured MeasuredSignature to the
// pod coordinator. This closes the tier-claim fraud gap (proposal §8.2/9.2): nodes
// must prove their claimed performance on a recurring schedule, not just at initial
// registration — and must sign each submission so it can't be forged for another node.
func SubmitBenchmarkResult(ctx context.Context, coordinatorURL, nodeID string, priv []byte, sig *protocol.MeasuredSignature) error {
	req := protocol.BenchmarkResultRequest{
		Measured:  *sig,
		Timestamp: time.Now().Unix(),
	}
	signingBytes, err := req.SigningBytes()
	if err != nil {
		return fmt.Errorf("build signing bytes: %w", err)
	}
	nodeSig, err := protocol.SignPayload(priv, signingBytes)
	if err != nil {
		return fmt.Errorf("sign benchmark result: %w", err)
	}
	req.Signature = nodeSig
	return postJSON(ctx, coordinatorURL+"/nodes/"+nodeID+"/benchmark-result", req)
}

// AttestSecureEnclave proves, cryptographically, that this node possesses a
// real Secure Enclave — replacing the plain self-declared
// CapabilityManifest.HasSecureEnclave boolean for routing decisions (Fable
// security review: self-declared attestation, unenforced privacy claims).
// enclave signs the request with a Secure Enclave-backed P-256 key (real on
// darwin via internal/attestation's cgo Security.framework binding; a stub
// returning attestation.ErrUnsupported everywhere else, including any darwin
// process that isn't code-signed with the entitlements Secure Enclave key
// access requires — see internal/attestation/enclave_darwin.go). Returning
// ErrUnsupported here is expected and non-fatal: the node just doesn't attest
// and remains ineligible for SensitivityHighRequiresAttestation jobs, exactly
// as if this feature didn't exist.
func AttestSecureEnclave(ctx context.Context, coordinatorURL, nodeID string, priv []byte, enclave *attestation.Signer) error {
	enclavePub, err := enclave.PublicKey()
	if err != nil {
		return fmt.Errorf("secure enclave public key: %w", err)
	}

	req := protocol.EnclaveAttestationRequest{
		NodeID:           nodeID,
		EnclavePublicKey: enclavePub,
		Timestamp:        time.Now().Unix(),
	}
	signingBytes, err := req.SigningBytes()
	if err != nil {
		return fmt.Errorf("build signing bytes: %w", err)
	}
	enclaveSig, err := enclave.Sign(signingBytes)
	if err != nil {
		return fmt.Errorf("sign with secure enclave: %w", err)
	}
	nodeSig, err := protocol.SignPayload(priv, signingBytes)
	if err != nil {
		return fmt.Errorf("sign attestation with node identity key: %w", err)
	}
	req.EnclaveSignature = enclaveSig
	req.Signature = nodeSig
	return postJSON(ctx, coordinatorURL+"/nodes/"+nodeID+"/attest-enclave", req)
}
