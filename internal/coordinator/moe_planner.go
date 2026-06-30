package coordinator

import (
	"fmt"
	"sort"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// PlanMoEExpertAssignment returns {nodeID: []expertIDs} for MoE model sharding.
// Experts are distributed proportionally to each node's memory capacity
// (DeclaredMemoryGB * DeclaredMemoryCapPct — the best the coordinator can see without
// live memory readings, which live at the node agent / governor level).
//
// CRITICAL: Do NOT use this for dense-model pipeline-sharding across WAN nodes.
// Sequential token passing across 20–150 ms inter-hop WAN latency is not viable;
// MoE expert-sharding is the only WAN-viable strategy (proposal §3/6.2).
func PlanMoEExpertAssignment(modelID string, totalExperts int, candidates []protocol.CapabilityManifest) (map[string][]int, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no candidate nodes for MoE assignment (model=%s)", modelID)
	}
	if totalExperts <= 0 {
		return nil, fmt.Errorf("totalExperts must be positive, got %d", totalExperts)
	}

	type nodeCap struct {
		nodeID string
		cap    float64
	}
	caps := make([]nodeCap, 0, len(candidates))
	var totalCap float64
	for _, c := range candidates {
		cap := c.DeclaredMemoryGB * c.DeclaredMemoryCapPct
		if cap <= 0 {
			cap = 1.0 // minimum so no node gets a zero share
		}
		caps = append(caps, nodeCap{nodeID: c.NodeID, cap: cap})
		totalCap += cap
	}

	// Proportional assignment using the largest-remainder method so expert counts
	// always sum to exactly totalExperts with no rounding loss.
	counts := make([]int, len(caps))
	assigned := 0
	type rem struct {
		idx  int
		frac float64
	}
	rems := make([]rem, len(caps))
	for i, c := range caps {
		share := c.cap / totalCap
		exact := share * float64(totalExperts)
		floor := int(exact)
		counts[i] = floor
		assigned += floor
		rems[i] = rem{i, exact - float64(floor)}
	}
	// Distribute remaining experts to nodes with the highest fractional parts.
	sort.Slice(rems, func(a, b int) bool { return rems[a].frac > rems[b].frac })
	for i := 0; i < totalExperts-assigned; i++ {
		counts[rems[i].idx]++
	}

	// Sort by nodeID for deterministic expert-ID → node mapping.
	type idxCount struct {
		nodeID string
		count  int
	}
	sorted := make([]idxCount, len(caps))
	for i, c := range caps {
		sorted[i] = idxCount{c.nodeID, counts[i]}
	}
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].nodeID < sorted[b].nodeID })

	assignment := make(map[string][]int, len(sorted))
	expertID := 0
	for _, sc := range sorted {
		if sc.count == 0 {
			continue
		}
		experts := make([]int, sc.count)
		for i := range experts {
			experts[i] = expertID
			expertID++
		}
		assignment[sc.nodeID] = experts
	}
	return assignment, nil
}

// RouteTokenToExpertNode returns the nodeID that holds the first activated expert
// for this token's gating-network routing decision.
//
// tokenRoutingDecision supports two formats:
//
//	{"activated_experts": [2, 5]} — top-K routing (most common in practice)
//	{"expert_id": 3}             — single-expert routing
//
// When multiple experts are activated (top-K), only the first is used for routing
// in this implementation — full top-K fan-out is deferred until WAN fan-out cost
// is measured against real traffic (proposal §6.3).
func RouteTokenToExpertNode(tokenRoutingDecision map[string]any, currentAssignment map[string][]int) (string, error) {
	var expertIDs []int

	if raw, ok := tokenRoutingDecision["activated_experts"]; ok {
		switch v := raw.(type) {
		case []int:
			expertIDs = v
		case []any:
			for _, e := range v {
				switch i := e.(type) {
				case int:
					expertIDs = append(expertIDs, i)
				case float64: // JSON numbers decode as float64
					expertIDs = append(expertIDs, int(i))
				}
			}
		}
	} else if raw, ok := tokenRoutingDecision["expert_id"]; ok {
		switch v := raw.(type) {
		case int:
			expertIDs = []int{v}
		case float64:
			expertIDs = []int{int(v)}
		}
	}

	if len(expertIDs) == 0 {
		return "", fmt.Errorf("no activated experts in routing decision")
	}

	target := expertIDs[0]
	for nodeID, experts := range currentAssignment {
		for _, e := range experts {
			if e == target {
				return nodeID, nil
			}
		}
	}
	return "", fmt.Errorf("expert %d not found in current assignment", target)
}

// DetectExpertLoadImbalance surfaces nodes whose assigned experts are being activated
// disproportionately more than the baseline expectation.
//
// recentRoutingStats maps expertID → activation count over the measurement window.
//
// This function DETECTS and reports only — it must NOT mutate the assignment or
// initiate rebalancing. Expert load imbalance under real traffic is an explicitly
// unsolved, industry-wide problem (proposal §6.3). Attempting to solve it here
// would over-promise what v1 can deliver.
func DetectExpertLoadImbalance(assignment map[string][]int, recentRoutingStats map[int]int) []string {
	if len(assignment) == 0 || len(recentRoutingStats) == 0 {
		return nil
	}

	totalExperts := 0
	for _, experts := range assignment {
		totalExperts += len(experts)
	}
	if totalExperts == 0 {
		return nil
	}

	var totalActivations int
	for _, count := range recentRoutingStats {
		totalActivations += count
	}
	if totalActivations == 0 {
		return nil
	}

	const imbalanceThreshold = 2.0

	var imbalanced []string
	for nodeID, experts := range assignment {
		if len(experts) == 0 {
			continue
		}
		var nodeActivations int
		for _, expertID := range experts {
			nodeActivations += recentRoutingStats[expertID]
		}
		// Expected activations for this node if traffic were perfectly balanced
		// across all experts proportional to this node's expert count.
		expectedActivations := float64(totalActivations) * float64(len(experts)) / float64(totalExperts)
		if float64(nodeActivations) > imbalanceThreshold*expectedActivations {
			imbalanced = append(imbalanced, nodeID)
		}
	}

	sort.Strings(imbalanced) // deterministic output order
	return imbalanced
}
