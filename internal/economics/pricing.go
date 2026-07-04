// Package economics is the single source of truth for what the mesh charges
// consumers and pays providers. Every credit that moves flows through these
// functions so the numbers can never drift between the debit path and the
// credit path.
//
// The model is deliberately a *spread*, not a zero-sum transfer: a provider is
// always paid LESS than the consumer is charged for the same work. The
// difference — the house edge — accrues to the network treasury, which funds
// startup grants, iOS coordination rewards, and long-run sustainability. This is
// what keeps credits from being a closed loop that either inflates to
// worthlessness or drains to zero:
//
//	consumer_cost  =  base × lane × sensitivity            (what the user spends)
//	provider_reward = consumer_cost × (1 − HouseEdge)      (what the node earns)
//	network_margin  = consumer_cost − provider_reward      (the house's cut)
//
// Fast (interactive) work is priced at a premium over background (batch) work;
// higher sensitivity tiers cost more (attestation overhead + trust premium). iOS
// coordination devices don't run inference, so they earn a small flat reward per
// encrypted pointer they serve, paid out of the treasury.
package economics

import "math"

// Lane distinguishes interactive fast-lane work from deferrable background work.
type Lane string

const (
	LaneFast       Lane = "fast"
	LaneBackground Lane = "background"
)

// TreasuryAccount is the reserved ledger user_id that collects the house edge.
// It is never a real user; its balance is the network's accumulated margin.
const TreasuryAccount = "oim-treasury"

// Tunable economic constants. Kept together so the whole model can be reasoned
// about — and adjusted — in one place.
const (
	// BaseCostPerKToken anchors the whole matrix: credits a consumer pays per
	// 1,000 output tokens for a fast-lane, moderate-sensitivity job. Chosen so
	// the 100-credit startup grant buys ~100 typical calls.
	BaseCostPerKToken = 1.0

	// HouseEdge is the network's margin: providers earn (1 − HouseEdge) of what
	// consumers pay. 0.25 = the house keeps 25% of every job. This is the
	// "casino" spread — the reward is always less than the spend.
	HouseEdge = 0.25

	// CoordinationRewardPerPointer is paid (from the treasury) to an iOS
	// coordination device for each encrypted payload pointer it serves. Small —
	// coordination is a lightweight security service, not compute — but nonzero
	// so hosting pointers is rewarded, not merely altruistic.
	CoordinationRewardPerPointer = 0.02
)

// laneMultiplier scales cost by lane. Fast is the premium (interactive,
// latency-sensitive); background is discounted to reward patience and fill idle
// capacity.
func laneMultiplier(l Lane) float64 {
	switch l {
	case LaneBackground:
		return 0.5
	default: // LaneFast and any unknown lane default to the premium tier
		return 1.0
	}
}

// sensitivityMultiplier scales cost by sensitivity tier. Accepts the protocol's
// tier strings ("low" / "moderate" / "high_requires_attestation"); anything
// unrecognized is treated as moderate.
func sensitivityMultiplier(sensitivity string) float64 {
	switch sensitivity {
	case "low":
		return 0.5
	case "high_requires_attestation":
		return 3.0
	default: // "moderate" and unknown
		return 1.0
	}
}

// ConsumerCost is what a consumer is charged for tokenCount output tokens of
// work in the given lane and sensitivity tier.
func ConsumerCost(lane Lane, sensitivity string, tokenCount int) float64 {
	if tokenCount <= 0 {
		return 0
	}
	perK := BaseCostPerKToken * laneMultiplier(lane) * sensitivityMultiplier(sensitivity)
	return round4(float64(tokenCount) / 1000.0 * perK)
}

// ProviderReward is what the serving node earns for the same work — always
// strictly less than ConsumerCost by the house edge.
func ProviderReward(lane Lane, sensitivity string, tokenCount int) float64 {
	return round4(ConsumerCost(lane, sensitivity, tokenCount) * (1 - HouseEdge))
}

// NetworkMargin is the treasury's cut: consumer cost minus provider reward.
func NetworkMargin(lane Lane, sensitivity string, tokenCount int) float64 {
	return round4(ConsumerCost(lane, sensitivity, tokenCount) - ProviderReward(lane, sensitivity, tokenCount))
}

// CoordinationReward is what an iOS coordination device earns for serving
// pointerCount encrypted pointers.
func CoordinationReward(pointerCount int) float64 {
	if pointerCount <= 0 {
		return 0
	}
	return round4(float64(pointerCount) * CoordinationRewardPerPointer)
}

func round4(f float64) float64 { return math.Round(f*10000) / 10000 }
