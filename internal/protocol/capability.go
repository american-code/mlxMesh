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
	// JobLaneWarm is a node-local control message, not a billable inference
	// job: it carries only ModelID and asks the node to create+await an Exo
	// instance for it. Reuses the exact same claim/execute/submit transport
	// as real jobs (see internal/agent/pull.go's executePulledJob) rather
	// than a separate mechanism, since a warm-up request has an identical
	// shape (coordinator hands the node something, node does work against
	// Exo, node reports back).
	JobLaneWarm JobLane = "warm"
)

// ModelCapability describes one model this node can serve at a specific quantization.
type ModelCapability struct {
	ModelID          string      `json:"model_id"`
	Quantization     string      `json:"quantization"`
	Runtime          RuntimeType `json:"runtime"`
	MaxContextTokens int         `json:"max_context_tokens"`
	IsMoE            bool        `json:"is_moe"`
	ExpertShardIDs   []int       `json:"expert_shard_ids,omitempty"`
	// Loaded reports whether Exo currently has an active inference instance
	// for this model — distinct from merely being downloaded to disk (which
	// is the only thing that gates whether a model appears in this list at
	// all). A model can be listed with Loaded:false: it's genuinely servable
	// hardware-wise, just not warm right now. Dispatch eligibility (see
	// coordinator.ScoreForFastLane) requires Loaded:true; a cold model is
	// visible but not routable until warmed (see POST /nodes/{id}/warm-model).
	Loaded bool `json:"loaded,omitempty"`
	// DraftModelID/NumDraftTokens mirror an operator's configured speculative-
	// decoding pairing for this model (see nodeconfig.DraftModelConfig) —
	// informational only. Exo's HTTP API has no speculative-decoding
	// parameter today (only the underlying mlx-lm CLI's --draft-model/
	// --num-draft-tokens flags do), so this never changes dispatch behavior;
	// it just lets the dashboard/coordinator show what a node is configured
	// to request once Exo's API supports it. Empty DraftModelID means no
	// draft model is configured for this model_id.
	DraftModelID   string `json:"draft_model_id,omitempty"`
	NumDraftTokens int    `json:"num_draft_tokens,omitempty"`
}

// DraftModelConfig pairs a served model with a smaller "draft" model for
// speculative decoding — forward-compatible plumbing for TODO.md's
// "Speculative decoding on node side." exo-explore/exo's /v1/chat/completions
// and /instance HTTP APIs do not accept a draft-model parameter as of this
// writing (verified against exo's own docs/api.md); only the underlying
// mlx-lm CLI (`mlx_lm.generate --draft-model ... --num-draft-tokens ...`)
// supports this today. Configuring this is inert until Exo's API grows a
// matching parameter — the field names here match mlx-lm's own flags so
// this repo is ready to pass them through the moment it does, with zero
// protocol changes needed on this end (see exoadapter.Client.RunChatCompletion's
// existing opaque extra-params passthrough).
type DraftModelConfig struct {
	DraftModelID   string `json:"draft_model_id"`
	NumDraftTokens int    `json:"num_draft_tokens,omitempty"` // 0 = let Exo pick its own default
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
	ClusterChipFamilies []string `json:"cluster_chip_families,omitempty"`
	// ClusterSignature is an opaque hash of this cluster's sorted device-ID
	// set (empty for non-cluster nodes, and empty even for some cluster
	// nodes — see capability.DetectClusterNode). Used ONLY by the
	// coordinator to detect that two independently-registering nodes
	// describe the same physical Exo ring — every device in a ring runs its
	// own agent and registers separately, each claiming the ring's full
	// pooled capacity; this is the signal that collapses them. Never
	// decodable back to real device IDs. Rides the existing manifest
	// signature so it can't be forged/stripped in transit.
	ClusterSignature     string             `json:"cluster_signature,omitempty"`
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
	// Simulated marks a node seeded via the OIM_SIMULATED_NODE backdoor
	// (internal/capability.AssembleManifest) rather than a real operator
	// contributing real hardware. Rides the existing manifest signature so it
	// can't be stripped in transit — the dashboard and API consumers use this
	// to label demo capacity distinctly from measured real capacity.
	Simulated bool `json:"simulated,omitempty"`
	// PullDelivery marks a node that receives work by long-polling the
	// coordinator (outbound-only, "mining-pool" model) instead of the
	// coordinator dialing INTO the node at ReachabilityEndpoint. When true,
	// the coordinator routes this node's jobs through the PullDispatcher
	// mailbox rather than an outbound HTTP POST — so the node needs no inbound
	// reachability at all (no port forwarding / UPnP / NAT traversal). A node
	// sets this when started without an explicit reachability endpoint;
	// ReachabilityEndpoint is empty/ignored in that case. Absent = false =
	// legacy push delivery (the simulated Docker fleet, LAN nodes with an
	// explicit endpoint). Rides the existing manifest signature so it can't be
	// flipped in transit.
	PullDelivery bool `json:"pull_delivery,omitempty"`
}

// Bytes serializes the manifest to canonical JSON bytes for signing.
func (m *CapabilityManifest) Bytes() ([]byte, error) {
	return json.Marshal(m)
}
