package protocol

import (
	"encoding/json"
	"fmt"
	"time"
)

// MaxRequestAge bounds how old a signed write-path request may be before the
// coordinator rejects it as a replay. A captured refresh/benchmark/outcome request
// must not be replayable indefinitely — this closes that window while staying
// generous enough for real network latency and modest clock skew.
const MaxRequestAge = 5 * time.Minute

// maxClockSkewAhead tolerates a signer's clock running slightly fast.
const maxClockSkewAhead = 30 * time.Second

// VerifyFreshness rejects a request timestamp that is too old (replay) or too far
// in the future (clock-skew abuse). now is passed explicitly so callers can test
// deterministically.
func VerifyFreshness(timestamp int64, now time.Time) error {
	ts := time.Unix(timestamp, 0)
	age := now.Sub(ts)
	if age > MaxRequestAge {
		return fmt.Errorf("request timestamp too old: %s ago (max %s)", age.Round(time.Second), MaxRequestAge)
	}
	if age < -maxClockSkewAhead {
		return fmt.Errorf("request timestamp is %s in the future", (-age).Round(time.Second))
	}
	return nil
}

// RefreshRequest is sent by a registered node to update its manifest.
// Signature must be Ed25519 over SigningBytes(), produced with the SAME keypair
// used at /nodes/register — the coordinator verifies against the public key it
// captured at registration, never a key supplied in this request. Accepting an
// unsigned or self-keyed refresh would let anyone hijack a victim node's
// ReachabilityEndpoint (traffic redirection) or inflate its MeasuredSignature
// (routing fraud).
type RefreshRequest struct {
	Manifest  CapabilityManifest `json:"manifest"`
	Timestamp int64              `json:"timestamp"`
	Signature []byte             `json:"signature"`
}

// SigningBytes returns the canonical bytes to sign — everything except Signature itself.
func (r *RefreshRequest) SigningBytes() ([]byte, error) {
	return json.Marshal(struct {
		Manifest  CapabilityManifest `json:"manifest"`
		Timestamp int64              `json:"timestamp"`
	}{r.Manifest, r.Timestamp})
}

// BenchmarkResultRequest is sent by a node submitting a freshly measured
// performance signature. Must be signed with the node's registration keypair —
// an unsigned benchmark submission lets an attacker inflate or deflate any
// node's measured throughput, defeating the tier-fraud check that reconciles
// against it (proposal §8.2/9.2).
type BenchmarkResultRequest struct {
	Measured  MeasuredSignature `json:"measured"`
	Timestamp int64             `json:"timestamp"`
	Signature []byte            `json:"signature"`
}

func (r *BenchmarkResultRequest) SigningBytes() ([]byte, error) {
	return json.Marshal(struct {
		Measured  MeasuredSignature `json:"measured"`
		Timestamp int64             `json:"timestamp"`
	}{r.Measured, r.Timestamp})
}

// JobOutcomeRequest is sent by a node reporting job completion. This is the
// earning side of the credit ledger — an unsigned outcome report is a free
// credit-minting oracle (POST success:true, tokens_delivered:999999999 for any
// node_id). Must be signed with the node's registration keypair.
type JobOutcomeRequest struct {
	JobID           string  `json:"job_id"`
	Success         bool    `json:"success"`
	LatencyMs       float64 `json:"latency_ms"`
	TokensDelivered int     `json:"tokens_delivered"`
	Timestamp       int64   `json:"timestamp"`
	Signature       []byte  `json:"signature"`
}

func (r *JobOutcomeRequest) SigningBytes() ([]byte, error) {
	return json.Marshal(struct {
		JobID           string  `json:"job_id"`
		Success         bool    `json:"success"`
		LatencyMs       float64 `json:"latency_ms"`
		TokensDelivered int     `json:"tokens_delivered"`
		Timestamp       int64   `json:"timestamp"`
	}{r.JobID, r.Success, r.LatencyMs, r.TokensDelivered, r.Timestamp})
}

// ClaimRequest is a node's outbound long-poll asking the coordinator for work
// (the "mining-pool" pull model). Signed with the node's registration keypair
// for the same reason every other node→coordinator write is: an unsigned claim
// would let anyone drain a victim node's queued jobs (stealing its work and
// its earnings) just by knowing the victim's node_id. NodeID rides inside the
// signed payload so it can't be swapped after signing.
type ClaimRequest struct {
	NodeID    string `json:"node_id"`
	Timestamp int64  `json:"timestamp"`
	Signature []byte `json:"signature"`
}

func (r *ClaimRequest) SigningBytes() ([]byte, error) {
	return json.Marshal(struct {
		NodeID    string `json:"node_id"`
		Timestamp int64  `json:"timestamp"`
	}{r.NodeID, r.Timestamp})
}

// JobResultRequest is a node returning a completed job's output over its own
// outbound connection (pull model). Signed with the node's registration
// keypair — this is the earning side, exactly like JobOutcomeRequest: an
// unsigned result submission would let anyone inject a fabricated completion
// for a job_id and get it credited to a node they don't control. The
// coordinator matches JobID to an outstanding Dispatch waiter; a result for an
// unknown/expired job is safely ignored.
type JobResultRequest struct {
	NodeID    string         `json:"node_id"`
	JobID     string         `json:"job_id"`
	Result    map[string]any `json:"result"`
	Error     string         `json:"error"` // node-side execution error, empty on success
	Timestamp int64          `json:"timestamp"`
	Signature []byte         `json:"signature"`
}

func (r *JobResultRequest) SigningBytes() ([]byte, error) {
	return json.Marshal(struct {
		NodeID    string         `json:"node_id"`
		JobID     string         `json:"job_id"`
		Result    map[string]any `json:"result"`
		Error     string         `json:"error"`
		Timestamp int64          `json:"timestamp"`
	}{r.NodeID, r.JobID, r.Result, r.Error, r.Timestamp})
}

// EnclaveAttestationRequest proves a node possesses a Secure Enclave-backed
// signing key, replacing trust in the plain self-reported
// CapabilityManifest.HasSecureEnclave boolean (Fable security review:
// self-declared attestation, unenforced privacy claims). A boolean a node
// sets on itself proves nothing; a private key that never leaves the Secure
// Enclave and can be shown to sign a fresh, coordinator-bound challenge is
// real evidence of hardware possession.
//
// Two independent signatures gate this request:
//   - EnclaveSignature: ECDSA-P256 over SigningBytes(), by EnclavePublicKey —
//     proves EnclavePublicKey is a real, usable Secure Enclave key right now.
//   - Signature: Ed25519 over SigningBytes(), by the node's ALREADY-REGISTERED
//     identity key — proves this attestation is being submitted by the node
//     that owns NodeID, not by an attacker who merely has some Secure Enclave
//     of their own (e.g. their own Mac) trying to attest on a victim's behalf.
type EnclaveAttestationRequest struct {
	NodeID           string `json:"node_id"`
	EnclavePublicKey []byte `json:"enclave_public_key"` // raw X9.63 uncompressed P-256 point
	Timestamp        int64  `json:"timestamp"`
	EnclaveSignature []byte `json:"enclave_signature"` // ECDSA-P256 DER signature, by EnclavePublicKey
	Signature        []byte `json:"signature"`         // Ed25519 signature, by the node's registered identity key
}

// SigningBytes returns the canonical bytes both signatures are computed over.
func (r *EnclaveAttestationRequest) SigningBytes() ([]byte, error) {
	return json.Marshal(struct {
		NodeID           string `json:"node_id"`
		EnclavePublicKey []byte `json:"enclave_public_key"`
		Timestamp        int64  `json:"timestamp"`
	}{r.NodeID, r.EnclavePublicKey, r.Timestamp})
}
