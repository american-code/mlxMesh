package protocol

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// NodeRegistration is sent from node agent → pod coordinator on join or refresh.
// PublicKey is required for the coordinator to verify the signature and derive node_id.
// UserID is optional: when set, earned credits from this node's inference work are
// attributed to that user account. If omitted, the node_id itself is used as the account key.
type NodeRegistration struct {
	Manifest  CapabilityManifest `json:"manifest"`
	PublicKey []byte             `json:"public_key"` // Ed25519 public key
	Signature []byte             `json:"signature"`  // Ed25519 over Manifest.Bytes()
	UserID    string             `json:"user_id,omitempty"`
}

// PodHealthDigest is sent pod coordinator → directory (and directory ↔ directory gossip).
// Deliberately aggregate-only — the directory must never see per-node detail (proposal §7.1).
// NodeCountApprox is intentionally imprecise; over-precision reads as a false trust signal.
//
// PublicKey/Signature (task #52, M7): a coordinator signs every digest with its
// own Ed25519 identity (distinct from any node's identity — see
// internal/identity.LoadOrCreateAt). This alone doesn't establish who's
// AUTHORIZED to use a given pod_id — that's the directory's TOFU pinning /
// allowlist job (internal/directory) — it only proves the digest wasn't
// tampered with in transit and was actually produced by whoever holds the
// claimed key.
type PodHealthDigest struct {
	PodID                string   `json:"pod_id"`
	RegionHint           string   `json:"region_hint"`
	CoordinatorEndpoint  string   `json:"coordinator_endpoint,omitempty"` // public URL clients use to reach this coordinator
	ServableModelIDs     []string `json:"servable_model_ids"`
	AggregateHealthScore float64  `json:"aggregate_health_score"` // 0.0–1.0
	NodeCountApprox      int      `json:"node_count_approx"`
	TotalMemoryGB        float64  `json:"total_memory_gb"`        // sum of declared committed memory across live nodes, ALL nodes (simulated + real)
	AggregateToksPerSec  float64  `json:"aggregate_toks_per_sec"` // sum of measured tok/s across live nodes, ALL nodes (simulated + real)

	// Real* fields (task #49, progressive decentralization) are the same three
	// aggregates restricted to non-simulated nodes — the "parity" metric the
	// README flagged as missing is derivable from these vs. the totals above
	// (e.g. RealTotalMemoryGB / TotalMemoryGB), without this struct baking in
	// any specific threshold for what counts as "at parity." Omitted (zero
	// value) is indistinguishable from "no real capacity yet," which is
	// accurate for a seed-only deployment.
	RealNodeCountApprox     int     `json:"real_node_count_approx,omitempty"`
	RealTotalMemoryGB       float64 `json:"real_total_memory_gb,omitempty"`
	RealAggregateToksPerSec float64 `json:"real_aggregate_toks_per_sec,omitempty"`

	PublicKey string `json:"public_key,omitempty"` // hex-encoded Ed25519, signer of this digest
	Signature string `json:"signature,omitempty"`  // hex-encoded Ed25519 over the digest with these two fields cleared
}

// signableBytes returns the canonical bytes to sign/verify: the digest with
// PublicKey/Signature cleared, so the signature never covers itself.
func (d PodHealthDigest) signableBytes() ([]byte, error) {
	clean := d
	clean.PublicKey = ""
	clean.Signature = ""
	return json.Marshal(clean)
}

// SignPodHealthDigest sets d.PublicKey and d.Signature from signerPrivateKey,
// signing over every other field. Call this last, right before sending —
// mutating any other field afterward invalidates the signature.
func SignPodHealthDigest(d PodHealthDigest, signerPrivateKey, signerPublicKey []byte) (PodHealthDigest, error) {
	d.PublicKey = ""
	d.Signature = ""
	payload, err := d.signableBytes()
	if err != nil {
		return d, fmt.Errorf("marshal digest for signing: %w", err)
	}
	sig, err := SignPayload(signerPrivateKey, payload)
	if err != nil {
		return d, fmt.Errorf("sign digest: %w", err)
	}
	d.PublicKey = hex.EncodeToString(signerPublicKey)
	d.Signature = hex.EncodeToString(sig)
	return d, nil
}

// VerifyPodHealthDigestSignature checks that d.Signature is a valid Ed25519
// signature by d.PublicKey over d's other fields, and that d.PublicKey itself
// decodes cleanly. It does NOT check that PublicKey is the key authorized to
// speak for d.PodID — callers (the directory's pin/allowlist store) own that
// decision; this only proves internal consistency of the digest as received.
func VerifyPodHealthDigestSignature(d PodHealthDigest) (publicKey []byte, ok bool) {
	if d.PublicKey == "" || d.Signature == "" {
		return nil, false
	}
	pub, err := hex.DecodeString(d.PublicKey)
	if err != nil {
		return nil, false
	}
	sig, err := hex.DecodeString(d.Signature)
	if err != nil {
		return nil, false
	}
	payload, err := d.signableBytes()
	if err != nil {
		return nil, false
	}
	if !VerifySignature(pub, payload, sig) {
		return nil, false
	}
	return pub, true
}

// DirectoryQuery is sent by a node or pod coordinator to the directory.
type DirectoryQuery struct {
	ModelID             string `json:"model_id"`
	Quantization        string `json:"quantization,omitempty"`
	RequesterRegionHint string `json:"requester_region_hint,omitempty"`
}

// DirectoryQueryResult is the directory's response.
type DirectoryQueryResult struct {
	MatchingPods []string `json:"matching_pods"` // pod_ids ranked nearest-first
	QueriedAt    string   `json:"queried_at"`    // ISO8601
}
