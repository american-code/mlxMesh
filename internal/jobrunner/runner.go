// Package jobrunner executes inference jobs assigned by the pod coordinator.
// It dispatches to the local Exo instance via exoadapter — inference logic never lives here.
// Both lanes use the same underlying inference call; they differ in failure handling and
// continuity behavior, not in the mechanics (proposal §5).
package jobrunner

import (
	"context"
	"fmt"
	"net/http"

	"github.com/open-inference-mesh/oim/internal/exoadapter"
	"github.com/open-inference-mesh/oim/internal/governor"
	"github.com/open-inference-mesh/oim/internal/protocol"
	"github.com/open-inference-mesh/oim/internal/sse"
)

// Runner holds the local Exo client for this node.
type Runner struct {
	exo *exoadapter.Client
	// draftModels configures speculative decoding pairings, keyed by served
	// model_id — forward-compatible plumbing only (see
	// protocol.DraftModelConfig's doc comment). Nil/empty = not configured,
	// which is the common case today since Exo's HTTP API has no
	// draft-model parameter to actually use this yet.
	draftModels map[string]protocol.DraftModelConfig
}

func New(exoURL string, draftModels map[string]protocol.DraftModelConfig) *Runner {
	return &Runner{exo: exoadapter.New(exoURL), draftModels: draftModels}
}

// draftExtra returns the extra request-body fields for modelID's configured
// draft model, or nil when none is configured — merge the result into
// whatever `extra` map is otherwise being sent to Exo. Field names match
// mlx-lm's own --draft-model/--num-draft-tokens CLI flags so this is ready to
// take effect the moment Exo's /v1/chat/completions accepts them; today Exo
// ignores unrecognized JSON fields, so this is a no-op in practice.
func (r *Runner) draftExtra(modelID string) map[string]any {
	d, ok := r.draftModels[modelID]
	if !ok || d.DraftModelID == "" {
		return nil
	}
	extra := map[string]any{"draft_model": d.DraftModelID}
	if d.NumDraftTokens > 0 {
		extra["num_draft_tokens"] = d.NumDraftTokens
	}
	return extra
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
	result, err := r.exo.RunChatCompletion(ctx, spec.ModelID, messages, false, r.draftExtra(spec.ModelID))
	if err != nil {
		return nil, fmt.Errorf("fast-lane inference failed: %w", err)
	}
	return result, nil
}

// WarmModel asks Exo to create (or confirm) an active inference instance for
// modelID and blocks until it's ready — the node-side half of the
// JobLaneWarm control message (see internal/agent/pull.go's
// executePulledJob). Follows Exo's own documented sequence: preview a
// placement, create the instance from it, then await readiness (POST
// /instance is asynchronous — Exo's docs: "wait until the API sees the new
// instance for this model" before inferring). No memory pre-flight check
// here unlike ExecuteFastLane/ExecuteBackgroundLane — a deliberate warm-up
// request should proceed even if the node is at its contribution cap for
// REAL traffic, since warming doesn't itself serve a paying job.
func (r *Runner) WarmModel(ctx context.Context, modelID string) (map[string]any, error) {
	placements, err := r.exo.PreviewInstancePlacements(ctx, modelID)
	if err != nil {
		return nil, fmt.Errorf("preview instance placement: %w", err)
	}
	if len(placements) == 0 {
		return nil, fmt.Errorf("no instance placement available for model %s", modelID)
	}
	if _, err := r.exo.CreateInstance(ctx, placements[0]); err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}
	if err := r.exo.AwaitInstance(ctx, modelID); err != nil {
		return nil, fmt.Errorf("await instance: %w", err)
	}
	return map[string]any{"warmed": true, "model_id": modelID}, nil
}

// ExecuteFastLaneStreaming is ExecuteFastLane's streaming counterpart: it
// relays Exo's SSE response directly to w as it arrives (same
// header/Flusher pattern used elsewhere for SSE — see the coordinator's
// /nodes/stream handler) instead of returning one buffered blob, and returns
// the accumulated completion-token count read from the trailing SSE usage
// frame — the same accounting signal ExecuteFastLane returns via
// usage.completion_tokens, just sourced from a stream instead of one blob.
// Fast lane only; never called for background-lane jobs.
// headersSent reports whether any bytes were relayed to w before an error
// occurred — the caller (agent.go's handler) uses this to decide whether a
// normal JSON error response is still possible (nothing written yet) or the
// stream must simply be abandoned (the client already has partial output over
// an implicit 200).
func (r *Runner) ExecuteFastLaneStreaming(
	ctx context.Context,
	spec protocol.JobSpec,
	messages []map[string]any,
	capPct float64,
	w http.ResponseWriter,
) (tokensDelivered int, headersSent bool, err error) {
	if err := r.RefuseIfConstrained(capPct); err != nil {
		return 0, false, fmt.Errorf("pre-flight refused: %w", err)
	}
	resp, err := r.exo.StreamChatCompletion(ctx, spec.ModelID, messages, r.draftExtra(spec.ModelID))
	if err != nil {
		return 0, false, fmt.Errorf("fast-lane streaming inference failed: %w", err)
	}
	defer resp.Body.Close()

	sse.SetHeaders(w)
	started, tokens, err := sse.Relay(w, resp.Body)
	if err != nil {
		return tokens, started, fmt.Errorf("read exo stream: %w", err)
	}
	return tokens, started, nil
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
	for k, v := range r.draftExtra(spec.ModelID) {
		extra[k] = v
	}
	result, err := r.exo.RunChatCompletion(ctx, spec.ModelID, messages, false, extra)
	if err != nil {
		return nil, fmt.Errorf("background-lane inference failed: %w", err)
	}
	return result, nil
}
