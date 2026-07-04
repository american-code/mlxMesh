package protocol

import "encoding/json"

// RuntimeType identifies the inference runtime a node serves through.
type RuntimeType string

const (
	RuntimeExoMLX            RuntimeType = "exo_mlx"
	RuntimeLlamaCppGGUF      RuntimeType = "llama_cpp_gguf"
	RuntimeMLXDirect         RuntimeType = "mlx_direct"
	RuntimeOtherOpenAICompat RuntimeType = "other_openai_compatible"
)

// JobLane distinguishes interactive from scheduled workloads.
type JobLane string

const (
	JobLaneFast       JobLane = "fast"
	JobLaneBackground JobLane = "background"
)

// ModelCapability describes one model this node can serve at a specific quantization.
type ModelCapability struct {
	ModelID          string      `json:"model_id"`
	Quantization     string      `json:"quantization"`
	Runtime          RuntimeType `json:"runtime"`
	MaxContextTokens int         `json:"max_context_tokens"`
	IsMoE            bool        `json:"is_moe"`
	ExpertShardIDs   []int       `json:"expert_shard_ids,omitempty"`
}

// MeasuredSignature is the output of bench/benchmark.go.
// Settlement reconciles against this, never against self-declared specs —
// that's the tier-fraud mitigation point (proposal §8.2/9.2).
type MeasuredSignature struct {
	TokensPerSecDecode  float64 `json:"tokens_per_sec_decode"`
	TokensPerSecPrefill float64 `json:"tokens_per_sec_prefill"`
	MeasuredAt          string  `json:"measured_at"` // ISO8601
	BenchmarkPromptID   string  `json:"benchmark_prompt_id"`
	SampleCount         int     `json:"sample_count"`
}

// CapabilityManifest is the full self-description sent to the pod coordinator
// on registration and each periodic refresh. Must reflect LIVE state —
// never serve a stale cached manifest.
type CapabilityManifest struct {
	NodeID             string `json:"node_id"`
	IsCluster          bool   `json:"is_cluster"`
	ClusterDeviceCount *int   `json:"cluster_device_count,omitempty"`
	// ClusterChipFamilies is one coarse chip family per cluster device (e.g.
	// "Apple M1") — deliberately excludes hostnames and exact chip variants
	// (Pro/Max/Ultra) so a cluster's hardware summary doesn't broadcast what
	// its operator named each machine. Empty for non-cluster nodes.
	ClusterChipFamilies  []string           `json:"cluster_chip_families,omitempty"`
	DeclaredMemoryGB     float64            `json:"declared_memory_gb"`
	DeclaredMemoryCapPct float64            `json:"declared_memory_cap_pct"`
	GeographicHint       string             `json:"geographic_hint,omitempty"`
	GeoLat               float64            `json:"geo_lat,omitempty"` // approximate latitude; 0 = not declared
	GeoLng               float64            `json:"geo_lng,omitempty"` // approximate longitude; 0 = not declared
	Models               []ModelCapability  `json:"models"`
	MeasuredSignature    *MeasuredSignature `json:"measured_signature,omitempty"`
	ReachabilityEndpoint string             `json:"reachability_endpoint"`
	PricePerUnit         map[string]float64 `json:"price_per_unit"`
	// HasSecureEnclave gates eligibility for SensitivityHighRequiresAttestation jobs.
	// This is a capability CHECK, not a confidentiality guarantee (proposal §8.1).
	HasSecureEnclave bool `json:"has_secure_enclave"`
	// ECDHPublicKey is this node's P-256 key-agreement public key (raw
	// uncompressed point, base64-encoded) — a client encrypts a job's payload
	// to this key so only this node can decrypt it (internal/payloadcrypto).
	// Empty on nodes that predate this field or don't support encrypted-pointer
	// jobs; such nodes are simply ineligible for a reservation
	// (POST /v1/reserve-node). Rides the existing manifest signature for free.
	ECDHPublicKey string `json:"ecdh_public_key,omitempty"`
	// TLSCertFingerprint is the SHA-256 fingerprint (hex) of this node's
	// --tls-cert, present only when the node serves its job endpoint over TLS.
	// The coordinator pins this exact fingerprint for all dispatches to this
	// node instead of chain-verifying — nodes are independently operated and
	// self-signed, so there is no shared CA to verify against. Tamper-evident
	// via the existing manifest signature: a MITM can't rewrite this field in
	// transit without invalidating the Ed25519 signature over the whole
	// manifest. Empty means this node serves plain HTTP (unchanged default).
	TLSCertFingerprint string `json:"tls_cert_fingerprint,omitempty"`
}

// Bytes serializes the manifest to canonical JSON bytes for signing.
func (m *CapabilityManifest) Bytes() ([]byte, error) {
	return json.Marshal(m)
}
