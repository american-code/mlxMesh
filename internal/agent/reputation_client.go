package agent

import (
	"context"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// ReportJobOutcome posts a job completion record to the pod coordinator.
// tokensDelivered is read from the Exo response usage field when available;
// pass 0 when the node could not determine the count.
// Non-fatal on error — reporting failure must not stop the agent.
func ReportJobOutcome(ctx context.Context, coordinatorURL, nodeID, jobID string, success bool, latencyMs float64, tokensDelivered int) error {
	return postJSON(ctx, coordinatorURL+"/nodes/"+nodeID+"/job-outcome", map[string]any{
		"job_id":           jobID,
		"success":          success,
		"latency_ms":       latencyMs,
		"tokens_delivered": tokensDelivered,
	})
}

// SubmitBenchmarkResult posts a freshly measured MeasuredSignature to the pod coordinator.
// This closes the tier-claim fraud gap (proposal §8.2/9.2): nodes must prove their
// claimed performance on a recurring schedule, not just at initial registration.
func SubmitBenchmarkResult(ctx context.Context, coordinatorURL, nodeID string, sig *protocol.MeasuredSignature) error {
	return postJSON(ctx, coordinatorURL+"/nodes/"+nodeID+"/benchmark-result", sig)
}
