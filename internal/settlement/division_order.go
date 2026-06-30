package settlement

import (
	"fmt"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// ResourceLine is one decomposed contribution line.
// Never blend these into one unit — the split is load-bearing (proposal §9.2).
type ResourceLine struct {
	ResourceType    string  `json:"resource_type"`    // "memory_hours" | "compute_cycles" | "bandwidth_relayed"
	MeasuredAmount  float64 `json:"measured_amount"`
	DeliveredAmount float64 `json:"delivered_amount"` // post-verification, post-overhead
	UnitPrice       float64 `json:"unit_price"`
}

// ComputeShrinkage returns the gap between measured and delivered contribution.
// Report explicitly per proposal §9.2 — never silently absorb into either number.
func ComputeShrinkage(measured, delivered float64) float64 {
	return measured - delivered
}

// BuildDivisionOrder assembles the multi-line settlement record for one completed job/cycle.
// This is what CreateSettlementRecord signs — this function only computes the numbers.
func BuildDivisionOrder(jobID, nodeID string, lines []ResourceLine) (map[string]any, error) {
	if jobID == "" || nodeID == "" {
		return nil, fmt.Errorf("job_id and node_id are required")
	}
	var totalMeasured, totalDelivered, totalValue float64
	for _, l := range lines {
		totalMeasured += l.MeasuredAmount
		totalDelivered += l.DeliveredAmount
		totalValue += l.DeliveredAmount * l.UnitPrice
	}
	return map[string]any{
		"job_id":          jobID,
		"node_id":         nodeID,
		"lines":           lines,
		"total_measured":  totalMeasured,
		"total_delivered": totalDelivered,
		"total_shrinkage": ComputeShrinkage(totalMeasured, totalDelivered),
		"total_value":     totalValue,
	}, nil
}

// ReconcileAgainstMeasuredSignature adjusts a claimed settlement amount if it exceeds
// what the node's measured signature supports within tolerance.
// This is the accounting-side half of tier-fraud mitigation:
// VerifyTierClaim is the detection side; this is where detected fraud reduces the payout.
func ReconcileAgainstMeasuredSignature(claimedAmount float64, sig *protocol.MeasuredSignature, tolerancePct float64) float64 {
	if sig == nil {
		return claimedAmount
	}
	maxAllowed := sig.TokensPerSecDecode * (1.0 + tolerancePct)
	if claimedAmount <= maxAllowed {
		return claimedAmount
	}
	return maxAllowed
}
