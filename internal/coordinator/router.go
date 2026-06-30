package coordinator

import "github.com/open-inference-mesh/oim/internal/protocol"

// ScoreForFastLane computes a routing score using MEASURED throughput (never
// self-declared spec — that's the tier-fraud mitigation point).
// HIGH_REQUIRES_ATTESTATION jobs score non-enclave nodes as -∞, not just lower.
//
// MILESTONE 2 — not implemented yet.
func ScoreForFastLane(node protocol.CapabilityManifest, job protocol.JobSpec) (float64, error) {
	return 0, ErrNotImplemented
}

// DispatchFastLane selects the best node and dispatches, failing over to the next
// candidate (never retrying the same failed node within one call).
//
// MILESTONE 2 — not implemented yet.
func DispatchFastLane(job protocol.JobSpec, registry *NodeRegistry, maxAttempts int) (map[string]any, error) {
	return nil, ErrNotImplemented
}

// AssignBackgroundJob returns {"primary": nodeID, "backups": [nodeID, ...]} and
// MUST be persisted — recomputing fresh each cycle defeats sticky-session.
//
// MILESTONE 2 — not implemented yet.
func AssignBackgroundJob(job protocol.JobSpec, registry *NodeRegistry) (map[string]any, error) {
	return nil, ErrNotImplemented
}

// ResolveForCycle returns (nodeID, isContinuation) for one recurrence cycle.
// If primary is down, promotes the next backup with isContinuation=false.
//
// MILESTONE 2 — not implemented yet.
func ResolveForCycle(job protocol.JobSpec, persistedAssignment map[string]any, registry *NodeRegistry) (string, bool, error) {
	return "", false, ErrNotImplemented
}
