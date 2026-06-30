// Package coordinator implements the pod coordinator — one per geographic/latency pod.
// Routing decisions are made here; the directory layer only does discovery.
package coordinator

import (
	"fmt"
	"sync"
	"time"

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

// HealthDigest returns the aggregate pod health for the directory layer.
// Deliberately aggregate-only — individual node data never leaves the pod (proposal §7.1).
func (r *NodeRegistry) HealthDigest(podID, regionHint string) protocol.PodHealthDigest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	modelSet := map[string]bool{}
	liveCount := 0
	totalTPS := 0.0
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
	}
	models := make([]string, 0, len(modelSet))
	for m := range modelSet {
		models = append(models, m)
	}
	health := 0.0
	if liveCount > 0 && totalTPS > 0 {
		// Normalize: 100 tok/s per node → health score 1.0
		health = min(1.0, totalTPS/float64(liveCount)/100.0)
	}
	return protocol.PodHealthDigest{
		PodID:                podID,
		RegionHint:           regionHint,
		ServableModelIDs:     models,
		AggregateHealthScore: health,
		NodeCountApprox:      liveCount,
	}
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
