// Package coordinator implements the pod coordinator — one per geographic/latency pod.
// Real-time routing decisions happen here; the directory layer never makes routing
// decisions, only discovery.
//
// MILESTONE 2 — not implemented yet.
package coordinator

import (
	"errors"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

var ErrNotImplemented = errors.New("milestone 2: not implemented")

// NodeRegistry is a live, in-memory scoreboard of every node registered to this pod.
// It decays — stale entries are evicted, not preserved as historical record.
type NodeRegistry struct{}

func NewNodeRegistry() *NodeRegistry { return &NodeRegistry{} }

// Register verifies the signature before accepting. Returns false on failure —
// never silently accept an unsigned or mis-signed registration.
func (r *NodeRegistry) Register(manifest protocol.CapabilityManifest, signature []byte) (bool, error) {
	return false, ErrNotImplemented
}

// Refresh updates a node's last-seen timestamp. Liveness decay depends on this.
func (r *NodeRegistry) Refresh(nodeID string, manifest protocol.CapabilityManifest) error {
	return ErrNotImplemented
}

// MarkUnreachable is called by routers on failed dispatch, not just missed heartbeat.
func (r *NodeRegistry) MarkUnreachable(nodeID string) error {
	return ErrNotImplemented
}

// Candidates returns all currently-eligible nodes for a job — filtering only,
// no scoring/ranking. Excludes nodes whose last-seen exceeds the liveness threshold.
func (r *NodeRegistry) Candidates(modelID, quantization, lane string) ([]protocol.CapabilityManifest, error) {
	return nil, ErrNotImplemented
}
