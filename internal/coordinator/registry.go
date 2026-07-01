// Package coordinator implements the pod coordinator — one per geographic/latency pod.
// Routing decisions are made here; the directory layer only does discovery.
package coordinator

import (
	"fmt"
	"sync"
	"time"

	"github.com/open-inference-mesh/oim/internal/bench"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

const livenessTTL = 90 * time.Second

type nodeEntry struct {
	manifest    protocol.CapabilityManifest
	publicKey   []byte
	lastSeen    time.Time
	unreachable bool
}

func (e *nodeEntry) isLive() bool {
	return time.Since(e.lastSeen) < livenessTTL && !e.unreachable
}

// NodeRegistry is a live, in-memory scoreboard of every node registered to this pod.
// It decays — stale entries are excluded from Candidates without explicit removal.
type NodeRegistry struct {
	mu      sync.RWMutex
	entries map[string]*nodeEntry
}

func NewNodeRegistry() *NodeRegistry {
	return &NodeRegistry{entries: make(map[string]*nodeEntry)}
}

// Register verifies the signature and node_id derivation before accepting.
// Returns false (without error) on signature failure — the caller decides how to respond.
func (r *NodeRegistry) Register(reg protocol.NodeRegistration) (bool, error) {
	expectedID := protocol.NodeIDFromPubKey(reg.PublicKey)
	if expectedID != reg.Manifest.NodeID {
		return false, fmt.Errorf("node_id mismatch: manifest %s, pubkey derives %s",
			reg.Manifest.NodeID, expectedID)
	}
	payload, err := reg.Manifest.Bytes()
	if err != nil {
		return false, fmt.Errorf("serialize manifest: %w", err)
	}
	if !protocol.VerifySignature(reg.PublicKey, payload, reg.Signature) {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[reg.Manifest.NodeID] = &nodeEntry{
		manifest:    reg.Manifest,
		publicKey:   reg.PublicKey,
		lastSeen:    time.Now(),
		unreachable: false,
	}
	return true, nil
}

// Refresh updates a node's manifest and last-seen timestamp. Clears unreachable flag.
func (r *NodeRegistry) Refresh(nodeID string, manifest protocol.CapabilityManifest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[nodeID]
	if !ok {
		return fmt.Errorf("node %s not registered; send a full registration first", nodeID)
	}
	e.manifest = manifest
	e.lastSeen = time.Now()
	e.unreachable = false
	return nil
}

// MarkUnreachable is called by the router on failed dispatch — not just missed heartbeat.
// The node must re-register or send a refresh to clear this flag.
func (r *NodeRegistry) MarkUnreachable(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[nodeID]; ok {
		e.unreachable = true
	}
}

// IsLive returns true if the node is registered, within TTL, and not marked unreachable.
func (r *NodeRegistry) IsLive(nodeID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[nodeID]
	return ok && e.isLive()
}

// Candidates returns all currently-eligible nodes for a job.
// Filtering only — no scoring/ranking (that's the router's job).
// Excludes nodes past the liveness TTL even if not explicitly marked unreachable.
func (r *NodeRegistry) Candidates(modelID, quantization string) ([]protocol.CapabilityManifest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []protocol.CapabilityManifest
	for _, e := range r.entries {
		if !e.isLive() {
			continue
		}
		if !hasModel(e.manifest, modelID, quantization) {
			continue
		}
		out = append(out, e.manifest)
	}
	return out, nil
}

// ClaimedSignature returns the MeasuredSignature from a node's registered manifest,
// or (nil, nil) if the node registered without a benchmark. Returns error if node unknown.
func (r *NodeRegistry) ClaimedSignature(nodeID string) (*protocol.MeasuredSignature, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[nodeID]
	if !ok {
		return nil, fmt.Errorf("node %s not registered", nodeID)
	}
	return e.manifest.MeasuredSignature, nil
}

// HealthDigest returns the aggregate pod health for the directory layer.
// Deliberately aggregate-only — individual node data never leaves the pod (proposal §7.1).
// coordinatorEndpoint is the public URL clients should use to reach this coordinator;
// pass empty string to omit it from the digest.
func (r *NodeRegistry) HealthDigest(podID, regionHint, coordinatorEndpoint string) protocol.PodHealthDigest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	modelSet := map[string]bool{}
	liveCount := 0
	totalTPS := 0.0
	totalMemGB := 0.0
	for _, e := range r.entries {
		if !e.isLive() {
			continue
		}
		liveCount++
		for _, m := range e.manifest.Models {
			modelSet[m.ModelID] = true
		}
		if e.manifest.MeasuredSignature != nil {
			totalTPS += e.manifest.MeasuredSignature.TokensPerSecDecode
		}
		totalMemGB += e.manifest.DeclaredMemoryGB * e.manifest.DeclaredMemoryCapPct
	}
	models := make([]string, 0, len(modelSet))
	for m := range modelSet {
		models = append(models, m)
	}
	health := 0.0
	if liveCount > 0 && totalTPS > 0 {
		health = min(1.0, totalTPS/float64(liveCount)/100.0)
	}
	return protocol.PodHealthDigest{
		PodID:                podID,
		RegionHint:           regionHint,
		CoordinatorEndpoint:  coordinatorEndpoint,
		ServableModelIDs:     models,
		AggregateHealthScore: health,
		NodeCountApprox:      liveCount,
		TotalMemoryGB:        totalMemGB,
		AggregateToksPerSec:  totalTPS,
	}
}

// NodeSnapshot is a dashboard-friendly view of one live node's state.
type NodeSnapshot struct {
	NodeID               string                     `json:"node_id"`
	Status               string                     `json:"status"` // "live" | "stale" | "unreachable"
	GeographicHint       string                     `json:"geographic_hint"`
	GeoLat               float64                    `json:"geo_lat,omitempty"` // 0 = not declared
	GeoLng               float64                    `json:"geo_lng,omitempty"` // 0 = not declared
	ReachabilityEndpoint string                     `json:"reachability_endpoint"`
	DeclaredMemoryGB     float64                    `json:"declared_memory_gb"`
	CommittedMemoryGB    float64                    `json:"committed_memory_gb"` // declared * cap_pct
	Models               []protocol.ModelCapability `json:"models"`
	MeasuredToksPerSec   float64                    `json:"measured_toks_per_sec"` // 0 if not yet benchmarked
	HasSecureEnclave     bool                       `json:"has_secure_enclave"`
	IsCluster            bool                       `json:"is_cluster"`
	ClusterDeviceCount   *int                       `json:"cluster_device_count,omitempty"`
	LastSeenAt           string                     `json:"last_seen_at"`
}

// Snapshot returns a point-in-time view of all registered nodes (live and recently stale).
func (r *NodeRegistry) Snapshot() []NodeSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]NodeSnapshot, 0, len(r.entries))
	for _, e := range r.entries {
		status := "live"
		if e.unreachable {
			status = "unreachable"
		} else if !e.isLive() {
			status = "stale"
		}
		tps := 0.0
		if e.manifest.MeasuredSignature != nil {
			tps = e.manifest.MeasuredSignature.TokensPerSecDecode
		}
		out = append(out, NodeSnapshot{
			NodeID:               e.manifest.NodeID,
			Status:               status,
			GeographicHint:       e.manifest.GeographicHint,
			GeoLat:               e.manifest.GeoLat,
			GeoLng:               e.manifest.GeoLng,
			ReachabilityEndpoint: e.manifest.ReachabilityEndpoint,
			DeclaredMemoryGB:     e.manifest.DeclaredMemoryGB,
			CommittedMemoryGB:    e.manifest.DeclaredMemoryGB * e.manifest.DeclaredMemoryCapPct,
			Models:               e.manifest.Models,
			MeasuredToksPerSec:   tps,
			HasSecureEnclave:     e.manifest.HasSecureEnclave,
			IsCluster:            e.manifest.IsCluster,
			ClusterDeviceCount:   e.manifest.ClusterDeviceCount,
			LastSeenAt:           e.lastSeen.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return out
}

// VerifiedCapacityScore sums the measured TPS of all live nodes whose submitted benchmark
// passes tier verification within tolerancePct of their claimed signature.
// Nodes that have never submitted a measurement, whose measurement diverges too far from
// their claim, or that are not currently live contribute zero to the score.
// This is the input to settlement/grant_decay — spinning up junk nodes that never pass
// verification must not drive grants toward zero (proposal §9.4).
func (r *NodeRegistry) VerifiedCapacityScore(measurements *MeasurementStore, tolerancePct float64) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var score float64
	for _, e := range r.entries {
		if !e.isLive() || e.manifest.MeasuredSignature == nil {
			continue
		}
		measured, ok := measurements.Get(e.manifest.NodeID)
		if !ok {
			continue
		}
		if bench.CompareSignatures(e.manifest.MeasuredSignature, measured, tolerancePct) {
			score += measured.TokensPerSecDecode
		}
	}
	return score
}

func hasModel(m protocol.CapabilityManifest, modelID, quantization string) bool {
	for _, model := range m.Models {
		if model.ModelID != modelID {
			continue
		}
		if quantization != "" && model.Quantization != quantization {
			continue
		}
		return true
	}
	return false
}
