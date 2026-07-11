package coordinator

import (
	"testing"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// TestEffectiveManifest_NilObservedPassesThrough confirms a node with no
// coordinator-observed sample yet scores on its claimed/self-declared
// signature exactly as before this feature existed — the cold-start/
// bootstrap case.
func TestEffectiveManifest_NilObservedPassesThrough(t *testing.T) {
	claimed := &protocol.MeasuredSignature{TokensPerSecDecode: 42, TokensPerSecPrefill: 100}
	node := NodeWithLoad{
		Manifest: protocol.CapabilityManifest{MeasuredSignature: claimed},
	}
	got := effectiveManifest(node)
	if got.MeasuredSignature != claimed {
		t.Fatalf("expected claimed signature untouched, got %+v", got.MeasuredSignature)
	}
}

// TestEffectiveManifest_ObservedOverridesDecodeRate confirms a set
// ObservedTPS overrides only TokensPerSecDecode, leaving other claimed
// fields (e.g. prefill rate) alone — this feature only ever had a
// coordinator-measured decode-rate signal to feed back in.
func TestEffectiveManifest_ObservedOverridesDecodeRate(t *testing.T) {
	claimed := &protocol.MeasuredSignature{TokensPerSecDecode: 42, TokensPerSecPrefill: 100}
	observed := 61.0
	node := NodeWithLoad{
		Manifest:    protocol.CapabilityManifest{MeasuredSignature: claimed},
		ObservedTPS: &observed,
	}
	got := effectiveManifest(node)
	if got.MeasuredSignature == claimed {
		t.Fatalf("expected a copy, not the original claimed signature pointer, to be mutated")
	}
	if got.MeasuredSignature.TokensPerSecDecode != 61 {
		t.Fatalf("expected decode rate overridden to 61, got %.1f", got.MeasuredSignature.TokensPerSecDecode)
	}
	if got.MeasuredSignature.TokensPerSecPrefill != 100 {
		t.Fatalf("expected prefill rate untouched at 100, got %.1f", got.MeasuredSignature.TokensPerSecPrefill)
	}
	// The original claimed signature must be untouched — effectiveManifest
	// must never mutate the registry's own manifest data in place.
	if claimed.TokensPerSecDecode != 42 {
		t.Fatalf("mutated the original claimed signature in place: %.1f", claimed.TokensPerSecDecode)
	}
}

// TestEffectiveManifest_SynthesizesSignatureWhenClaimedNil confirms a node
// that registered without ever running oim bench run (MeasuredSignature ==
// nil) still gets scored on its observed throughput once real traffic
// exists, rather than being stuck on ScoreForFastLane's flat 1.0 fallback.
func TestEffectiveManifest_SynthesizesSignatureWhenClaimedNil(t *testing.T) {
	observed := 61.0
	node := NodeWithLoad{
		Manifest:    protocol.CapabilityManifest{MeasuredSignature: nil},
		ObservedTPS: &observed,
	}
	got := effectiveManifest(node)
	if got.MeasuredSignature == nil {
		t.Fatal("expected a synthesized MeasuredSignature, got nil")
	}
	if got.MeasuredSignature.TokensPerSecDecode != 61 {
		t.Fatalf("expected decode rate 61, got %.1f", got.MeasuredSignature.TokensPerSecDecode)
	}
}

// rankTestNode builds an eligible NodeWithLoad for "test-model" with the
// given claimed decode tok/s — used to exercise rankCandidates' ordering
// without needing a real registry.
func rankTestNode(id string, tps float64) NodeWithLoad {
	return NodeWithLoad{
		Manifest: protocol.CapabilityManifest{
			NodeID:            id,
			Models:            []protocol.ModelCapability{{ModelID: "test-model", Loaded: true}},
			MeasuredSignature: &protocol.MeasuredSignature{TokensPerSecDecode: tps},
		},
	}
}

var rankTestJob = protocol.JobSpec{ModelID: "test-model", Sensitivity: protocol.SensitivityLow}

// TestRankCandidates_IneligibleNodesExcludedFromWindow confirms the -Inf
// filtering happens before the power-of-two window is applied — an
// ineligible node (wrong model) must never occupy a window slot or become
// primary regardless of the random sample.
func TestRankCandidates_IneligibleNodesExcludedFromWindow(t *testing.T) {
	wrongModel := NodeWithLoad{Manifest: protocol.CapabilityManifest{
		NodeID: "wrong-model", Models: []protocol.ModelCapability{{ModelID: "other-model", Loaded: true}},
	}}
	eligible := rankTestNode("eligible", 50)
	for i := 0; i < 50; i++ {
		out := rankCandidates([]NodeWithLoad{wrongModel, eligible}, rankTestJob)
		if len(out) != 1 || out[0].Manifest.NodeID != "eligible" {
			t.Fatalf("expected only the eligible node, got %+v", out)
		}
	}
}

// TestRankCandidates_ZeroOrOneCandidateIsNoop confirms the power-of-two
// window logic never panics or misbehaves on the boundary cases it must
// skip entirely.
func TestRankCandidates_ZeroOrOneCandidateIsNoop(t *testing.T) {
	if out := rankCandidates(nil, rankTestJob); len(out) != 0 {
		t.Fatalf("expected empty output for no candidates, got %+v", out)
	}
	solo := rankTestNode("solo", 10)
	out := rankCandidates([]NodeWithLoad{solo}, rankTestJob)
	if len(out) != 1 || out[0].Manifest.NodeID != "solo" {
		t.Fatalf("expected the single candidate unchanged, got %+v", out)
	}
}

// TestRankCandidates_PrimaryVariesAcrossCalls is the core herding-fix
// guarantee: with several equally-eligible candidates, repeated calls must
// NOT always deterministically pick the same node as primary — a plain
// greedy argmax would, and that determinism is exactly what causes many
// concurrent requests to pile onto one node before load penalties can react.
func TestRankCandidates_PrimaryVariesAcrossCalls(t *testing.T) {
	nodes := []NodeWithLoad{
		rankTestNode("a", 100),
		rankTestNode("b", 99),
		rankTestNode("c", 98),
	}
	seenPrimaries := map[string]bool{}
	for i := 0; i < 200; i++ {
		out := rankCandidates(nodes, rankTestJob)
		seenPrimaries[out[0].Manifest.NodeID] = true
	}
	if len(seenPrimaries) < 2 {
		t.Fatalf("expected primary selection to vary across calls, always got %+v", seenPrimaries)
	}
}

// TestRankCandidates_PrimaryNeverOutsidePowerOfTwoWindow bounds the quality
// tradeoff: with many candidates spanning a wide score range, the primary
// pick must only ever come from the top powerOfTwoWindow — never a
// far-worse node from deep in the list, which "power of two choices" isn't
// meant to risk.
func TestRankCandidates_PrimaryNeverOutsidePowerOfTwoWindow(t *testing.T) {
	nodes := make([]NodeWithLoad, 10)
	for i := range nodes {
		// Descending scores: index 0 is best (90), index 9 is worst (0).
		nodes[i] = rankTestNode(string(rune('a'+i)), float64(90-i*10))
	}
	for i := 0; i < 200; i++ {
		out := rankCandidates(nodes, rankTestJob)
		primary := out[0].Manifest.NodeID
		if primary != "a" && primary != "b" && primary != "c" {
			t.Fatalf("primary %q fell outside the top-%d window", primary, powerOfTwoWindow)
		}
	}
}

// TestRankCandidates_FallbackOrderStaysDeterministicBestFirst confirms the
// retry/fallback tail (everything after the primary) keeps its
// score-sorted order — only the primary slot is randomized, since a
// dispatch FAILURE (not load) is what fallback recovers from.
func TestRankCandidates_FallbackOrderStaysDeterministicBestFirst(t *testing.T) {
	nodes := []NodeWithLoad{
		rankTestNode("a", 90),
		rankTestNode("b", 80),
		rankTestNode("c", 70),
		rankTestNode("d", 60), // outside the top-3 window — never touched by the swap
	}
	for i := 0; i < 50; i++ {
		out := rankCandidates(nodes, rankTestJob)
		ids := make([]string, len(out))
		for j, n := range out {
			ids[j] = n.Manifest.NodeID
		}
		// Whichever of a/b/c became primary, the remaining three must still
		// appear in descending-score order.
		var rest []string
		for _, id := range ids[1:] {
			rest = append(rest, id)
		}
		for j := 1; j < len(rest); j++ {
			if rest[j-1] == "d" && rest[j] != "d" {
				t.Fatalf("d (lowest score) must always be last, got order %v", ids)
			}
		}
		if rest[len(rest)-1] != "d" {
			t.Fatalf("expected d (never in the swap window) last in fallback order, got %v", ids)
		}
	}
}
