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
	MeasuredAt          string  `json:"measured_at"`        // ISO8601
	BenchmarkPromptID   string  `json:"benchmark_prompt_id"`
	SampleCount         int     `json:"sample_count"`
}

// CapabilityManifest is the full self-description sent to the pod coordinator
// on registration and each periodic refresh. Must reflect LIVE state —
// never serve a stale cached manifest.
type CapabilityManifest struct {
	NodeID               string             `json:"node_id"`
	IsCluster            bool               `json:"is_cluster"`
	ClusterDeviceCount   *int               `json:"cluster_device_count,omitempty"`
	DeclaredMemoryGB     float64            `json:"declared_memory_gb"`
	DeclaredMemoryCapPct float64            `json:"declared_memory_cap_pct"`
	GeographicHint       string             `json:"geographic_hint,omitempty"`
	Models               []ModelCapability  `json:"models"`
	MeasuredSignature    *MeasuredSignature `json:"measured_signature,omitempty"`
	ReachabilityEndpoint string             `json:"reachability_endpoint"`
	PricePerUnit         map[string]float64 `json:"price_per_unit"`
}

// Bytes serializes the manifest to canonical JSON bytes for signing.
func (m *CapabilityManifest) Bytes() ([]byte, error) {
	return json.Marshal(m)
}
