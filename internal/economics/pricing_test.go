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

func approx(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
