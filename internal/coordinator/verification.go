package coordinator

import "github.com/open-inference-mesh/oim/internal/protocol"

// SpotCheckFastLane re-runs a sampled fraction of fast-lane jobs on a second node.
// SampleRate is configurable — baked-in 100% defeats efficiency, 0% defeats verification.
//
// MILESTONE 3 — not implemented yet.
func SpotCheckFastLane(jobID string, primaryResult map[string]any, verifierNodeID string, sampleRate float64) (bool, error) {
	return false, ErrNotImplemented
}

// StatisticalBaselineCheck compares background-lane output distribution over time
// against a trusted baseline. Lower overhead than per-request redundancy,
// appropriate for the longer time horizon (proposal §8.2).
//
// MILESTONE 3 — not implemented yet.
func StatisticalBaselineCheck(jobID string, recentOutputs []map[string]any, baselineDist map[string]any) (bool, error) {
	return false, ErrNotImplemented
}

// VerifyTierClaim triggers a benchmark against the node and compares to its
// claimed MeasuredSignature within tolerance. Called on a recurring schedule —
// not only at registration. Closes the reward-asymmetry fraud gap (proposal §8.2/9.2).
//
// MILESTONE 3 — not implemented yet.
func VerifyTierClaim(nodeID string, claimed protocol.MeasuredSignature) (bool, error) {
	return false, ErrNotImplemented
}

// PlanMoEExpertAssignment returns {nodeID: []expertIDs} for MoE model sharding.
// Must respect each node's EnforceContributionCap-derived memory ceiling.
// Do NOT attempt dense-model pipeline-sharding across WAN nodes — see proposal §3/6.2.
//
// MILESTONE 6 — not implemented yet. Do not start before M1-5 are validated.
func PlanMoEExpertAssignment(modelID string, totalExperts int, candidates []protocol.CapabilityManifest) (map[string][]int, error) {
	return nil, ErrNotImplemented
}
