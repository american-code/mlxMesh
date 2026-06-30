package tests

import (
	"sort"
	"testing"

	"github.com/open-inference-mesh/oim/internal/coordinator"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// buildMoECandidate builds a CapabilityManifest with the given memory parameters.
func buildMoECandidate(nodeID string, memGB, capPct float64) protocol.CapabilityManifest {
	return protocol.CapabilityManifest{
		NodeID:               nodeID,
		DeclaredMemoryGB:     memGB,
		DeclaredMemoryCapPct: capPct,
		ReachabilityEndpoint: "http://localhost:9999",
		Models: []protocol.ModelCapability{
			{ModelID: "mixtral-8x7b", Quantization: "4bit", Runtime: protocol.RuntimeExoMLX,
				MaxContextTokens: 4096, IsMoE: true},
		},
	}
}

// allExpertIDs collects all expert IDs from an assignment into a sorted slice.
func allExpertIDs(assignment map[string][]int) []int {
	var ids []int
	for _, experts := range assignment {
		ids = append(ids, experts...)
	}
	sort.Ints(ids)
	return ids
}

// --- PlanMoEExpertAssignment tests ---

func TestPlanMoEExpertAssignmentBasic(t *testing.T) {
	// Two equal-capacity nodes, 8 experts → 4 each.
	candidates := []protocol.CapabilityManifest{
		buildMoECandidate("node-a", 16.0, 0.5), // 8 GB effective
		buildMoECandidate("node-b", 16.0, 0.5), // 8 GB effective
	}
	assignment, err := coordinator.PlanMoEExpertAssignment("mixtral-8x7b", 8, candidates)
	if err != nil {
		t.Fatalf("PlanMoEExpertAssignment: %v", err)
	}
	if len(assignment["node-a"]) != 4 {
		t.Errorf("node-a: want 4 experts, got %d", len(assignment["node-a"]))
	}
	if len(assignment["node-b"]) != 4 {
		t.Errorf("node-b: want 4 experts, got %d", len(assignment["node-b"]))
	}
}

func TestPlanMoEExpertAssignmentWeighted(t *testing.T) {
	// node-a 4 GB, node-b 2 GB, node-c 2 GB → shares 0.5/0.25/0.25 → 4/2/2 experts.
	candidates := []protocol.CapabilityManifest{
		buildMoECandidate("node-a", 8.0, 0.5),  // 4 GB
		buildMoECandidate("node-b", 8.0, 0.25), // 2 GB
		buildMoECandidate("node-c", 8.0, 0.25), // 2 GB
	}
	assignment, err := coordinator.PlanMoEExpertAssignment("mixtral-8x7b", 8, candidates)
	if err != nil {
		t.Fatalf("PlanMoEExpertAssignment: %v", err)
	}
	if len(assignment["node-a"]) != 4 {
		t.Errorf("node-a: want 4 experts (50%% share), got %d", len(assignment["node-a"]))
	}
	if len(assignment["node-b"]) != 2 {
		t.Errorf("node-b: want 2 experts (25%% share), got %d", len(assignment["node-b"]))
	}
	if len(assignment["node-c"]) != 2 {
		t.Errorf("node-c: want 2 experts (25%% share), got %d", len(assignment["node-c"]))
	}
}

func TestPlanMoEExpertAssignmentNoExpertsLost(t *testing.T) {
	// All expert IDs {0..11} must be present exactly once across any assignment.
	candidates := []protocol.CapabilityManifest{
		buildMoECandidate("node-x", 32.0, 0.5),
		buildMoECandidate("node-y", 16.0, 0.5),
		buildMoECandidate("node-z", 8.0, 0.5),
	}
	totalExperts := 12
	assignment, err := coordinator.PlanMoEExpertAssignment("mixtral-8x7b", totalExperts, candidates)
	if err != nil {
		t.Fatalf("PlanMoEExpertAssignment: %v", err)
	}
	ids := allExpertIDs(assignment)
	if len(ids) != totalExperts {
		t.Errorf("total assigned experts: want %d, got %d", totalExperts, len(ids))
	}
	for i, id := range ids {
		if id != i {
			t.Errorf("expert ID gap or duplicate at position %d: got %d", i, id)
			break
		}
	}
}

func TestPlanMoEExpertAssignmentNoCandidates(t *testing.T) {
	_, err := coordinator.PlanMoEExpertAssignment("mixtral-8x7b", 8, nil)
	if err == nil {
		t.Error("no candidates should return error")
	}
}

func TestPlanMoEExpertAssignmentSingleNode(t *testing.T) {
	// One node gets all experts.
	candidates := []protocol.CapabilityManifest{
		buildMoECandidate("solo-node", 64.0, 0.5),
	}
	assignment, err := coordinator.PlanMoEExpertAssignment("mixtral-8x7b", 8, candidates)
	if err != nil {
		t.Fatalf("PlanMoEExpertAssignment: %v", err)
	}
	if len(assignment["solo-node"]) != 8 {
		t.Errorf("single node: want all 8 experts, got %d", len(assignment["solo-node"]))
	}
}

func TestPlanMoEExpertAssignmentInvalidTotalExperts(t *testing.T) {
	candidates := []protocol.CapabilityManifest{buildMoECandidate("node-a", 16.0, 0.5)}
	_, err := coordinator.PlanMoEExpertAssignment("mixtral-8x7b", 0, candidates)
	if err == nil {
		t.Error("totalExperts=0 should return error")
	}
}

// --- RouteTokenToExpertNode tests ---

func TestRouteTokenToExpertNodeActivatedExperts(t *testing.T) {
	// Token activates expert 3 → should route to node-y.
	assignment := map[string][]int{
		"node-x": {0, 1, 2},
		"node-y": {3, 4, 5},
	}
	decision := map[string]any{"activated_experts": []any{float64(3)}}
	nodeID, err := coordinator.RouteTokenToExpertNode(decision, assignment)
	if err != nil {
		t.Fatalf("RouteTokenToExpertNode: %v", err)
	}
	if nodeID != "node-y" {
		t.Errorf("expert 3 is on node-y; got %q", nodeID)
	}
}

func TestRouteTokenToExpertNodeSingleExpertKey(t *testing.T) {
	// {"expert_id": 1} format — routes to node-x.
	assignment := map[string][]int{
		"node-x": {0, 1, 2},
		"node-y": {3, 4, 5},
	}
	decision := map[string]any{"expert_id": float64(1)}
	nodeID, err := coordinator.RouteTokenToExpertNode(decision, assignment)
	if err != nil {
		t.Fatalf("RouteTokenToExpertNode: %v", err)
	}
	if nodeID != "node-x" {
		t.Errorf("expert 1 is on node-x; got %q", nodeID)
	}
}

func TestRouteTokenToExpertNodeUnknownExpert(t *testing.T) {
	assignment := map[string][]int{"node-a": {0, 1}}
	decision := map[string]any{"activated_experts": []any{float64(99)}}
	_, err := coordinator.RouteTokenToExpertNode(decision, assignment)
	if err == nil {
		t.Error("expert not in assignment should return error")
	}
}

func TestRouteTokenToExpertNodeEmptyDecision(t *testing.T) {
	assignment := map[string][]int{"node-a": {0, 1}}
	_, err := coordinator.RouteTokenToExpertNode(map[string]any{}, assignment)
	if err == nil {
		t.Error("empty routing decision should return error")
	}
}

// --- DetectExpertLoadImbalance tests ---

func TestDetectExpertLoadImbalanceDetectsHotNode(t *testing.T) {
	// 3 nodes × 3 experts each (9 total).
	// node-a experts activated 100× each; node-b and node-c experts 10× each.
	// overall avg per expert = (300+30+30)/9 = 40; node-a avg = 100 > 2×40 = 80 → flagged.
	// Note: with only 2 equal-sized nodes the 2× threshold is mathematically unreachable
	// (one node can never hold >100% of activations), so this test uses 3 nodes.
	assignment := map[string][]int{
		"node-a": {0, 1, 2},
		"node-b": {3, 4, 5},
		"node-c": {6, 7, 8},
	}
	stats := map[int]int{0: 100, 1: 100, 2: 100, 3: 10, 4: 10, 5: 10, 6: 10, 7: 10, 8: 10}
	flagged := coordinator.DetectExpertLoadImbalance(assignment, stats)
	if len(flagged) == 0 {
		t.Fatal("node-a activates experts 2.5× the average rate; should be flagged")
	}
	if flagged[0] != "node-a" {
		t.Errorf("expected node-a to be flagged; got %v", flagged)
	}
	// node-b and node-c should not be flagged
	for _, nodeID := range flagged {
		if nodeID == "node-b" || nodeID == "node-c" {
			t.Errorf("node %s should not be flagged (below-average activation rate)", nodeID)
		}
	}
}

func TestDetectExpertLoadImbalanceBalanced(t *testing.T) {
	// All experts equally activated → no node flagged.
	assignment := map[string][]int{
		"node-a": {0, 1},
		"node-b": {2, 3},
	}
	stats := map[int]int{0: 50, 1: 50, 2: 50, 3: 50}
	flagged := coordinator.DetectExpertLoadImbalance(assignment, stats)
	if len(flagged) != 0 {
		t.Errorf("balanced activation: want no flagged nodes, got %v", flagged)
	}
}

func TestDetectExpertLoadImbalanceEmptyStats(t *testing.T) {
	assignment := map[string][]int{"node-a": {0, 1}}
	flagged := coordinator.DetectExpertLoadImbalance(assignment, map[int]int{})
	if flagged != nil {
		t.Errorf("empty stats: want nil, got %v", flagged)
	}
}

func TestDetectExpertLoadImbalanceDoesNotMutateAssignment(t *testing.T) {
	// Confirm the function only detects — the assignment must be unchanged after the call.
	assignment := map[string][]int{
		"node-a": {0, 1},
		"node-b": {2, 3},
	}
	before := map[string]int{
		"node-a": len(assignment["node-a"]),
		"node-b": len(assignment["node-b"]),
	}
	stats := map[int]int{0: 200, 1: 200, 2: 5, 3: 5}
	coordinator.DetectExpertLoadImbalance(assignment, stats)

	for nodeID, wantLen := range before {
		if len(assignment[nodeID]) != wantLen {
			t.Errorf("node %s: assignment mutated (want %d experts, got %d)", nodeID, wantLen, len(assignment[nodeID]))
		}
	}
}
