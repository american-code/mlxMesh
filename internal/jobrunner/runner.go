// Package jobrunner executes inference jobs assigned by the pod coordinator.
// It dispatches to the local Exo instance via exoadapter — inference logic never lives here.
// Both lanes use the same underlying inference call; they differ in failure handling and
// continuity behavior, not in the mechanics (proposal §5).
package jobrunner

import (
	"context"
	"fmt"

	"github.com/open-inference-mesh/oim/internal/exoadapter"
	"github.com/open-inference-mesh/oim/internal/governor"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// Runner holds the local Exo client for this node.
type Runner struct {
	exo *exoadapter.Client
}

func New(exoURL string) *Runner {
	return &Runner{exo: exoadapter.New(exoURL)}
}

// RefuseIfConstrained returns an error if this node should not accept the job right now.
// Must be called before accepting ANY job assignment — never reach Exo without passing this.
func (r *Runner) RefuseIfConstrained(capPct float64) error {
	if !governor.IsForegrounded() {
		return fmt.Errorf("node is not foregrounded; refusing job to avoid OS throttle")
	}
	committed, err := governor.EnforceContributionCap(capPct)
	if err != nil {
		return fmt.Errorf("read contribution cap: %w", err)
	}
	if committed <= 0 {
		return fmt.Errorf("contribution cap is %.2f GB; no memory available to commit", committed)
	}
	return nil
}

// ExecuteFastLane runs a single interactive request.
// Raises immediately on failure — retry policy lives at the coordinator level (proposal §5).
// Never retries internally.
func (r *Runner) ExecuteFastLane(
	ctx context.Context,
	spec protocol.JobSpec,
	messages []map[string]any,
	capPct float64,
) (map[string]any, error) {
	if err := r.RefuseIfConstrained(capPct); err != nil {
		return nil, fmt.Errorf("pre-flight refused: %w", err)
	}
	result, err := r.exo.RunChatCompletion(ctx, spec.ModelID, messages, false, nil)
	if err != nil {
		return nil, fmt.Errorf("fast-lane inference failed: %w", err)
	}
	return result, nil
}

// ExecuteBackgroundLane runs one cycle of a recurring job.
// isContinuation=true means this node previously ran the prior cycle (sticky-session).
// When continuing, avoid unnecessary instance teardown/recreate — that reload cost is
// exactly what sticky-session assignment exists to avoid (proposal §5).
func (r *Runner) ExecuteBackgroundLane(
	ctx context.Context,
	spec protocol.JobSpec,
	messages []map[string]any,
	capPct float64,
	isContinuation bool,
) (map[string]any, error) {
	if err := r.RefuseIfConstrained(capPct); err != nil {
		return nil, fmt.Errorf("pre-flight refused: %w", err)
	}
	extra := map[string]any{"oim_is_continuation": isContinuation}
	result, err := r.exo.RunChatCompletion(ctx, spec.ModelID, messages, false, extra)
	if err != nil {
		return nil, fmt.Errorf("background-lane inference failed: %w", err)
	}
	return result, nil
}
