package economics

import "testing"

// Performance regression guard (task #28). Pricing is computed on every job
// dispatch and debit; it's pure arithmetic and must not allocate.
func TestPerf_PricingIsAllocationFree(t *testing.T) {
	if allocs := testing.AllocsPerRun(1000, func() {
		_ = ConsumerCost(LaneFast, "moderate", 512)
		_ = ProviderReward(LaneBackground, "high_requires_attestation", 512)
		_ = NetworkMargin(LaneFast, "low", 512)
	}); allocs != 0 {
		t.Errorf("pricing allocates %.1f/op, want 0", allocs)
	}
}

func BenchmarkConsumerCost(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ConsumerCost(LaneFast, "moderate", 512)
	}
}
