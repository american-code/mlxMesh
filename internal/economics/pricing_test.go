package economics

import "testing"

// The load-bearing invariant: a provider is NEVER paid as much as the consumer
// is charged. If this ever fails, the network is a zero-sum (or worse) loop.
func TestRewardAlwaysLessThanCost(t *testing.T) {
	lanes := []Lane{LaneFast, LaneBackground}
	tiers := []string{"low", "moderate", "high_requires_attestation", "unknown"}
	for _, l := range lanes {
		for _, s := range tiers {
			cost := ConsumerCost(l, s, 1000)
			reward := ProviderReward(l, s, 1000)
			margin := NetworkMargin(l, s, 1000)
			if reward >= cost {
				t.Errorf("%s/%s: reward %.4f must be < cost %.4f", l, s, reward, cost)
			}
			if margin <= 0 {
				t.Errorf("%s/%s: margin %.4f must be > 0 (house always takes a cut)", l, s, margin)
			}
			// Solvency: cost = reward + margin exactly (no credits created/destroyed
			// outside the treasury).
			if got := reward + margin; !approx(got, cost) {
				t.Errorf("%s/%s: reward+margin %.4f != cost %.4f", l, s, got, cost)
			}
		}
	}
}

func TestLaneAndSensitivityOrdering(t *testing.T) {
	// Fast costs more than background at the same sensitivity.
	if ConsumerCost(LaneFast, "moderate", 1000) <= ConsumerCost(LaneBackground, "moderate", 1000) {
		t.Error("fast lane must cost more than background")
	}
	// high > moderate > low.
	hi := ConsumerCost(LaneFast, "high_requires_attestation", 1000)
	mod := ConsumerCost(LaneFast, "moderate", 1000)
	lo := ConsumerCost(LaneFast, "low", 1000)
	if !(hi > mod && mod > lo) {
		t.Errorf("sensitivity ordering broken: low=%.2f mod=%.2f high=%.2f", lo, mod, hi)
	}
}

func TestAnchorAndMatrixValues(t *testing.T) {
	// Anchor: fast + moderate + 1k tokens = base cost.
	if c := ConsumerCost(LaneFast, "moderate", 1000); !approx(c, 1.0) {
		t.Errorf("anchor cost want 1.0, got %.4f", c)
	}
	// Provider earns 75% of it.
	if r := ProviderReward(LaneFast, "moderate", 1000); !approx(r, 0.75) {
		t.Errorf("anchor reward want 0.75, got %.4f", r)
	}
	// Background moderate = half the fast cost.
	if c := ConsumerCost(LaneBackground, "moderate", 1000); !approx(c, 0.5) {
		t.Errorf("background moderate want 0.5, got %.4f", c)
	}
}

func TestZeroAndNegativeTokens(t *testing.T) {
	if ConsumerCost(LaneFast, "moderate", 0) != 0 || ProviderReward(LaneFast, "moderate", -5) != 0 {
		t.Error("zero/negative token counts must cost/reward nothing")
	}
}

func TestCoordinationReward(t *testing.T) {
	if r := CoordinationReward(10); !approx(r, 0.2) {
		t.Errorf("10 pointers want 0.2, got %.4f", r)
	}
	if CoordinationReward(0) != 0 {
		t.Error("zero pointers = zero reward")
	}
}

// ActivityDiscount bounds and monotonicity — the bootstrapping-economics fix
// (TODO.md, Economic Sustainability).
func TestActivityDiscount_BoundsAndMonotonicity(t *testing.T) {
	if d := ActivityDiscount(0); !approx(d, ActivityDiscountFloor) {
		t.Errorf("at 0%% backpressure want floor %.4f, got %.4f", ActivityDiscountFloor, d)
	}
	if d := ActivityDiscount(-5); !approx(d, ActivityDiscountFloor) {
		t.Errorf("negative backpressure should clamp to floor, got %.4f", d)
	}
	if d := ActivityDiscount(ActivityCeilingBackpressurePct); !approx(d, 1.0) {
		t.Errorf("at the ceiling want no discount (1.0), got %.4f", d)
	}
	if d := ActivityDiscount(ActivityCeilingBackpressurePct + 50); !approx(d, 1.0) {
		t.Errorf("above the ceiling want no discount (1.0), got %.4f", d)
	}
	// Monotonic non-decreasing as backpressure rises.
	prev := ActivityDiscount(0)
	for bp := 1.0; bp <= 100; bp++ {
		d := ActivityDiscount(bp)
		if d < prev-1e-9 {
			t.Fatalf("ActivityDiscount not monotonic at bp=%.0f: %.6f < prev %.6f", bp, d, prev)
		}
		prev = d
	}
}

// ConsumerCostWithActivityDiscount must never let the treasury pay out more
// than it collects — the same solvency invariant TestRewardAlwaysLessThanCost
// guards for the undiscounted path, swept across backpressure levels too.
func TestConsumerCostWithActivityDiscount_NeverUndercutsProviderReward(t *testing.T) {
	lanes := []Lane{LaneFast, LaneBackground}
	tiers := []string{"low", "moderate", "high_requires_attestation", "unknown"}
	for _, l := range lanes {
		for _, s := range tiers {
			reward := ProviderReward(l, s, 1000)
			for bp := 0.0; bp <= 100; bp += 5 {
				discounted := ConsumerCostWithActivityDiscount(l, s, 1000, bp)
				if discounted < reward-1e-9 {
					t.Errorf("%s/%s bp=%.0f: discounted cost %.4f < provider reward %.4f", l, s, bp, discounted, reward)
				}
			}
		}
	}
}

// At a fully idle network, the discount should compress the treasury's
// margin all the way to zero (discounted cost == provider reward) — the
// specific anchor the bootstrapping fix is built around.
func TestConsumerCostWithActivityDiscount_ZeroMarginAtFullyIdle(t *testing.T) {
	reward := ProviderReward(LaneFast, "moderate", 1000)
	discounted := ConsumerCostWithActivityDiscount(LaneFast, "moderate", 1000, 0)
	if !approx(discounted, reward) {
		t.Errorf("at 0%% backpressure want discounted cost == provider reward (%.4f), got %.4f", reward, discounted)
	}
}

// The provider's payout must never move because of this consumer-side
// discount — that's the whole point: node economics stay stable regardless
// of network activity, only the treasury's cut compresses.
func TestConsumerCostWithActivityDiscount_ProviderRewardUnaffected(t *testing.T) {
	for bp := 0.0; bp <= 100; bp += 10 {
		_ = ConsumerCostWithActivityDiscount(LaneFast, "moderate", 1000, bp)
		if r := ProviderReward(LaneFast, "moderate", 1000); !approx(r, 0.75) {
			t.Errorf("bp=%.0f: ProviderReward drifted to %.4f, want 0.75 (must be independent of the discount)", bp, r)
		}
	}
}

func approx(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
