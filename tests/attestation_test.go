package tests

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/coordinator"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// enclaveTestKey mints an in-test ECDSA-P256 keypair standing in for a real
// Secure Enclave key, and signs digests exactly the way
// internal/attestation.Signer does (DER-encoded ECDSA over a SHA-256 digest).
type enclaveTestKey struct {
	priv *ecdsa.PrivateKey
}

func newEnclaveTestKey(t *testing.T) enclaveTestKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P256 key: %v", err)
	}
	return enclaveTestKey{priv: priv}
}

func (k enclaveTestKey) publicKeyBytes() []byte {
	return elliptic.Marshal(elliptic.P256(), k.priv.X, k.priv.Y)
}

func (k enclaveTestKey) sign(t *testing.T, msg []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(msg)
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	if err != nil {
		t.Fatalf("sign digest: %v", err)
	}
	return sig
}

// registerTestNode registers a node with a fresh Ed25519 identity and returns
// its private key + node ID for building attestation requests.
func registerTestNode(t *testing.T, r *coordinator.NodeRegistry) (priv []byte, nodeID string) {
	t.Helper()
	priv, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	manifest := protocol.CapabilityManifest{
		NodeID:               protocol.NodeIDFromPubKey(pub),
		DeclaredMemoryGB:     16.0,
		DeclaredMemoryCapPct: 0.5,
		ReachabilityEndpoint: "http://127.0.0.1:0",
		Models:               []protocol.ModelCapability{{ModelID: "test-model"}},
	}
	payload, err := manifest.Bytes()
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	sig, err := protocol.SignPayload(priv, payload)
	if err != nil {
		t.Fatalf("sign manifest: %v", err)
	}
	ok, err := r.Register(protocol.NodeRegistration{Manifest: manifest, PublicKey: pub, Signature: sig})
	if err != nil || !ok {
		t.Fatalf("register: ok=%v err=%v", ok, err)
	}
	return priv, manifest.NodeID
}

func buildAttestationRequest(t *testing.T, nodePriv []byte, nodeID string, enclave enclaveTestKey, ts int64) protocol.EnclaveAttestationRequest {
	t.Helper()
	req := protocol.EnclaveAttestationRequest{
		NodeID:           nodeID,
		EnclavePublicKey: enclave.publicKeyBytes(),
		Timestamp:        ts,
	}
	signingBytes, err := req.SigningBytes()
	if err != nil {
		t.Fatalf("signing bytes: %v", err)
	}
	req.EnclaveSignature = enclave.sign(t, signingBytes)
	nodeSig, err := protocol.SignPayload(nodePriv, signingBytes)
	if err != nil {
		t.Fatalf("sign with node key: %v", err)
	}
	req.Signature = nodeSig
	return req
}

func TestVerifyEnclaveAttestationAcceptsValidProof(t *testing.T) {
	r := coordinator.NewNodeRegistry()
	nodePriv, nodeID := registerTestNode(t, r)
	enclave := newEnclaveTestKey(t)
	req := buildAttestationRequest(t, nodePriv, nodeID, enclave, time.Now().Unix())

	if err := coordinator.VerifyEnclaveAttestation(r, req); err != nil {
		t.Fatalf("expected valid attestation to verify, got: %v", err)
	}
}

func TestVerifyEnclaveAttestationRejectsUnregisteredNode(t *testing.T) {
	r := coordinator.NewNodeRegistry()
	enclave := newEnclaveTestKey(t)
	// Node identity generated but never registered.
	nodePriv, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	nodeID := protocol.NodeIDFromPubKey(pub)
	req := buildAttestationRequest(t, nodePriv, nodeID, enclave, time.Now().Unix())

	if err := coordinator.VerifyEnclaveAttestation(r, req); err == nil {
		t.Fatal("expected error for an unregistered node")
	}
}

func TestVerifyEnclaveAttestationRejectsForgedNodeSignature(t *testing.T) {
	// An attacker with their OWN Secure Enclave (valid EnclaveSignature) must
	// not be able to attest on behalf of a victim node_id — this is exactly
	// what the outer Ed25519 node-identity signature exists to prevent.
	r := coordinator.NewNodeRegistry()
	_, victimNodeID := registerTestNode(t, r)
	attackerPriv, _, _ := protocol.GenerateNodeIdentity() // attacker's own key, not the victim's
	enclave := newEnclaveTestKey(t)
	req := buildAttestationRequest(t, attackerPriv, victimNodeID, enclave, time.Now().Unix())

	if err := coordinator.VerifyEnclaveAttestation(r, req); err == nil {
		t.Fatal("expected forged node-identity signature to be rejected")
	}
}

func TestVerifyEnclaveAttestationRejectsInvalidEnclaveSignature(t *testing.T) {
	r := coordinator.NewNodeRegistry()
	nodePriv, nodeID := registerTestNode(t, r)
	enclave := newEnclaveTestKey(t)
	req := buildAttestationRequest(t, nodePriv, nodeID, enclave, time.Now().Unix())
	req.EnclaveSignature = newEnclaveTestKey(t).sign(t, []byte("wrong signer")) // signed by a different key than EnclavePublicKey

	if err := coordinator.VerifyEnclaveAttestation(r, req); err == nil {
		t.Fatal("expected mismatched enclave signature/pubkey to be rejected")
	}
}

func TestVerifyEnclaveAttestationRejectsStaleTimestamp(t *testing.T) {
	r := coordinator.NewNodeRegistry()
	nodePriv, nodeID := registerTestNode(t, r)
	enclave := newEnclaveTestKey(t)
	req := buildAttestationRequest(t, nodePriv, nodeID, enclave, time.Now().Add(-time.Hour).Unix())

	if err := coordinator.VerifyEnclaveAttestation(r, req); err == nil {
		t.Fatal("expected a stale (replayed) timestamp to be rejected")
	}
}

// --- Registry + routing gate: self-declared HasSecureEnclave must never be
// sufficient on its own; only a coordinator-verified attestation counts. ---

func TestFastLaneGateIgnoresSelfDeclaredEnclaveClaim(t *testing.T) {
	job := protocol.JobSpec{
		JobID:       "j1",
		ModelID:     "m1",
		Sensitivity: protocol.SensitivityHighRequiresAttestation,
		Lane:        protocol.JobLaneFast,
	}
	node := protocol.CapabilityManifest{
		NodeID:           "n1",
		HasSecureEnclave: true, // self-declared — must be ignored for gating
		Models: []protocol.ModelCapability{
			{ModelID: "m1"},
		},
	}
	score := coordinator.ScoreForFastLane(node, job, 0, false /* not coordinator-attested */)
	if !isNegInf(score) {
		t.Errorf("self-declared HasSecureEnclave=true without coordinator attestation should still be ineligible for a high-sensitivity job, got score %v", score)
	}
}

func TestFastLaneGateAcceptsVerifiedAttestation(t *testing.T) {
	job := protocol.JobSpec{
		JobID:       "j1",
		ModelID:     "m1",
		Sensitivity: protocol.SensitivityHighRequiresAttestation,
		Lane:        protocol.JobLaneFast,
	}
	node := protocol.CapabilityManifest{
		NodeID:           "n1",
		HasSecureEnclave: false, // even if the self-declared field is false/absent
		Models: []protocol.ModelCapability{
			{ModelID: "m1"},
		},
	}
	score := coordinator.ScoreForFastLane(node, job, 0, true /* coordinator-attested */)
	if isNegInf(score) {
		t.Error("a coordinator-verified attestation should make the node eligible regardless of the self-declared field")
	}
}

func TestRegistryCandidatesWithLoadReflectsMarkEnclaveAttested(t *testing.T) {
	r := coordinator.NewNodeRegistry()
	_, nodeID := registerTestNode(t, r)

	before, err := r.CandidatesWithLoad("test-model", "")
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(before) != 1 || before[0].EnclaveAttested {
		t.Fatalf("expected exactly one non-attested candidate before attestation, got %+v", before)
	}

	if err := r.MarkEnclaveAttested(nodeID, []byte{0x04, 0x01}); err != nil {
		t.Fatalf("MarkEnclaveAttested: %v", err)
	}

	after, err := r.CandidatesWithLoad("test-model", "")
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(after) != 1 || !after[0].EnclaveAttested {
		t.Fatalf("expected the candidate to be EnclaveAttested after MarkEnclaveAttested, got %+v", after)
	}
}
