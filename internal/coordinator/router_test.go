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
