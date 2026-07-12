package agent

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/open-inference-mesh/oim/internal/jobrunner"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// pullClaimClientTimeout bounds one claim request client-side, set just above
// the coordinator's server-side long-poll window (pullClaimTimeout, 25s) so a
// healthy 204 always arrives first and only a genuinely stuck connection trips
// this.
const pullClaimClientTimeout = 35 * time.Second

// pulledJob mirrors the coordinator's PendingJob wire shape (job_id, job,
// messages) — decoded locally so the agent needs no import dependency on the
// coordinator package.
type pulledJob struct {
	JobID    string           `json:"job_id"`
	Job      protocol.JobSpec `json:"job"`
	Messages []map[string]any `json:"messages"`
}

// runPullLoop is the node half of the mining-pool model: claim work over an
// outbound connection to the coordinator, run it via Exo, post the result
// back — forever, until ctx is canceled. Because the node opens every
// connection, no inbound reachability (port forwarding / UPnP / NAT traversal)
// is ever needed. Only runs on pull-mode nodes.
func runPullLoop(ctx context.Context, cfg Config, priv []byte, nodeID string, runner *jobrunner.Runner, ecdhPriv *ecdh.PrivateKey, chaosActive, scheduleActive *atomic.Bool) {
	log.Printf("[agent] pull loop started — claiming work from %s", cfg.CoordinatorURL)
	for {
		if ctx.Err() != nil {
			return
		}
		// Same gating as the inbound handler: don't take work while a chaos
		// window or an out-of-schedule window has this node paused.
		if chaosActive.Load() || !scheduleActive.Load() {
			if !sleepCtx(ctx, 2*time.Second) {
				return
			}
			continue
		}

		job, ok := claimJob(ctx, cfg.CoordinatorURL, nodeID, priv)
		if !ok {
			// No job this round (long-poll timeout) or a transient error. A
			// short backoff keeps a persistent error (coordinator down) from
			// becoming a hot loop; a normal 204 already blocked ~25s so this
			// barely delays the next poll.
			if !sleepCtx(ctx, 500*time.Millisecond) {
				return
			}
			continue
		}

		result, execErr := executePulledJob(ctx, runner, ecdhPriv, cfg.CapacityPct, job)
		if err := submitJobResult(ctx, cfg.CoordinatorURL, nodeID, priv, job.JobID, result, execErr); err != nil {
			log.Printf("[agent] pull: submit result job=%s: %v", job.JobID, err)
		}
	}
}

// sleepCtx sleeps for d unless ctx is canceled first. Returns false if ctx was
// canceled (caller should stop).
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// claimJob does one signed long-poll to POST /jobs/claim. Returns (job, true)
// when the coordinator handed us work, (nil, false) on a 204 long-poll timeout
// or any transient error (the loop just re-polls). Never returns an error the
// caller must handle — a pull node's job is to keep asking.
func claimJob(ctx context.Context, coordinatorURL, nodeID string, priv []byte) (*pulledJob, bool) {
	req := protocol.ClaimRequest{NodeID: nodeID, Timestamp: time.Now().Unix()}
	signingBytes, err := req.SigningBytes()
	if err != nil {
		return nil, false
	}
	sig, err := protocol.SignPayload(priv, signingBytes)
	if err != nil {
		return nil, false
	}
	req.Signature = sig
	body, err := json.Marshal(req)
	if err != nil {
		return nil, false
	}

	cctx, cancel := context.WithTimeout(ctx, pullClaimClientTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(cctx, http.MethodPost, coordinatorURL+"/jobs/claim", bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := coordinatorClient.Do(httpReq)
	if err != nil {
		return nil, false // network/timeout — re-poll
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, false // long-poll expired with no work
	}
	if resp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(resp.Body)
		log.Printf("[agent] pull: claim HTTP %d: %s", resp.StatusCode, rb)
		return nil, false
	}
	var job pulledJob
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, false
	}
	return &job, true
}

// executePulledJob runs a claimed job through the SAME jobrunner path the
// inbound HTTP handler uses (runner.ExecuteFastLane / ExecuteBackgroundLane),
// including the encrypted-pointer fetch+decrypt when the job carries one. Only
// the transport differs from push delivery; the execution is identical.
func executePulledJob(ctx context.Context, runner *jobrunner.Runner, ecdhPriv *ecdh.PrivateKey, capPct float64, pj *pulledJob) (map[string]any, error) {
	messages := pj.Messages
	if pj.Job.PayloadFetchURL != "" {
		decoded, err := fetchAndDecryptPayload(ctx, pj.Job.PayloadFetchURL, pj.Job.PayloadEphemeralPubKey, ecdhPriv)
		if err != nil {
			return nil, fmt.Errorf("payload decrypt: %w", err)
		}
		messages = decoded
	}
	spec := pj.Job
	if spec.JobID == "" {
		spec.JobID = pj.JobID
	}
	// JobLaneWarm is a node-local control message, not billable inference —
	// see protocol.JobLaneWarm's doc comment. Checked first since it carries
	// no messages/payload and needs none of the fast/background handling.
	if spec.Lane == protocol.JobLaneWarm {
		return runner.WarmModel(ctx, spec.ModelID)
	}
	if spec.Lane == protocol.JobLaneBackground {
		return runner.ExecuteBackgroundLane(ctx, spec, messages, capPct, false)
	}
	return runner.ExecuteFastLane(ctx, spec, messages, capPct)
}

// submitJobResult posts a completed job's output back to the coordinator over
// the node's outbound connection (signed, retried — postJSON handles backoff).
// A node-side execution error is reported in the Error field so the
// coordinator's waiter fails cleanly rather than hanging to its deadline.
func submitJobResult(ctx context.Context, coordinatorURL, nodeID string, priv []byte, jobID string, result map[string]any, execErr error) error {
	req := protocol.JobResultRequest{
		NodeID:    nodeID,
		JobID:     jobID,
		Result:    result,
		Timestamp: time.Now().Unix(),
	}
	if execErr != nil {
		req.Error = execErr.Error()
	}
	signingBytes, err := req.SigningBytes()
	if err != nil {
		return fmt.Errorf("build signing bytes: %w", err)
	}
	sig, err := protocol.SignPayload(priv, signingBytes)
	if err != nil {
		return fmt.Errorf("sign result: %w", err)
	}
	req.Signature = sig
	return postJSON(ctx, coordinatorURL+"/jobs/result", req)
}
