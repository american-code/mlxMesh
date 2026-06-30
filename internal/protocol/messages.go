package protocol

// NodeRegistration is sent from node agent → pod coordinator on join or refresh.
type NodeRegistration struct {
	Manifest  CapabilityManifest `json:"manifest"`
	Signature []byte             `json:"signature"` // Ed25519 over Manifest.Bytes()
}

// PodHealthDigest is sent pod coordinator → directory (and directory ↔ directory gossip).
// Deliberately aggregate-only — the directory must never see per-node detail (proposal §7.1).
// NodeCountApprox is intentionally imprecise; over-precision reads as a false trust signal.
type PodHealthDigest struct {
	PodID                string   `json:"pod_id"`
	RegionHint           string   `json:"region_hint"`
	ServableModelIDs     []string `json:"servable_model_ids"`
	AggregateHealthScore float64  `json:"aggregate_health_score"` // 0.0–1.0
	NodeCountApprox      int      `json:"node_count_approx"`
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
