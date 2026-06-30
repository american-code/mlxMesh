package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"

	"github.com/open-inference-mesh/oim/internal/bench"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

var (
	ErrNotImplemented = errors.New("milestone 6: not implemented")
	ErrNoMeasurement  = errors.New("no submitted benchmark measurement for this node")
)

// SpotCheckFastLane re-runs the job on a verifier node and compares output consistency.
// sampleRate controls what fraction of fast-lane jobs are checked (0.0 = never, 1.0 = always).
// Returns true if outputs are consistent, or if the job was not selected for this check cycle.
//
// Sampling is probabilistic — 100% defeats efficiency, 0% defeats verification (proposal §8.2).
func SpotCheckFastLane(
	ctx context.Context,
	jobID string,
	messages []map[string]any,
	modelID string,
	primaryResult map[string]any,
	verifierEndpoint string,
	sampleRate float64,
) (bool, error) {
	if rand.Float64() >= sampleRate {
		return true, nil // not selected for this sampling window
	}

	body, err := json.Marshal(map[string]any{
		"model":    modelID,
		"messages": messages,
	})
	if err != nil {
		return false, fmt.Errorf("marshal verifier request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, verifierEndpoint+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("build verifier request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("verifier dispatch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		rb, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("verifier HTTP %d: %s", resp.StatusCode, rb)
	}

	var verifierResult map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&verifierResult); err != nil {
		return false, fmt.Errorf("parse verifier result: %w", err)
	}

	// Both responses must be non-empty and within a 2× content-length ratio.
	// Full text equality is too strict for non-deterministic LLMs.
	primaryLen := responseContentLen(primaryResult)
	verifierLen := responseContentLen(verifierResult)
	if primaryLen == 0 || verifierLen == 0 {
		return false, fmt.Errorf("spot check %s: empty response (primary=%d verifier=%d chars)", jobID, primaryLen, verifierLen)
	}
	ratio := float64(verifierLen) / float64(primaryLen)
	return ratio >= 0.5 && ratio <= 2.0, nil
}

// StatisticalBaselineCheck verifies that recent background-lane outputs fall within
// the distribution of a trusted baseline. An empty or incomplete baselineDist is a no-op.
// baselineDist must contain "mean_length" (float64) and "stddev_length" (float64).
func StatisticalBaselineCheck(
	jobID string,
	recentOutputs []map[string]any,
	baselineDist map[string]any,
) (bool, error) {
	if len(baselineDist) == 0 || len(recentOutputs) == 0 {
		return true, nil
	}
	meanLen, hasMean := baselineDist["mean_length"].(float64)
	stddevLen, hasStddev := baselineDist["stddev_length"].(float64)
	if !hasMean || !hasStddev || stddevLen <= 0 {
		return true, nil // incomplete baseline — treat as no-op
	}

	var total int
	for _, out := range recentOutputs {
		total += responseContentLen(out)
	}
	observedMean := float64(total) / float64(len(recentOutputs))

	diff := observedMean - meanLen
	if diff < 0 {
		diff = -diff
	}
	return diff <= 3*stddevLen, nil
}

// VerifyTierClaim compares a node's submitted benchmark result against its claimed
// MeasuredSignature within tolerancePct. Returns ErrNoMeasurement if no benchmark
// has been submitted yet. Call this on a recurring per-node schedule — not only at
// registration — to catch tier-fraud over time (proposal §8.2/9.2).
func VerifyTierClaim(
	nodeID string,
	claimed protocol.MeasuredSignature,
	measurements *MeasurementStore,
	tolerancePct float64,
) (bool, error) {
	measured, ok := measurements.Get(nodeID)
	if !ok {
		return false, ErrNoMeasurement
	}
	return bench.CompareSignatures(&claimed, measured, tolerancePct), nil
}

// responseContentLen returns the character length of the first choice's message content.
func responseContentLen(result map[string]any) int {
	choices, _ := result["choices"].([]any)
	if len(choices) == 0 {
		return 0
	}
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	content, _ := msg["content"].(string)
	return len(content)
}

// PlanMoEExpertAssignment returns {nodeID: []expertIDs} for MoE model sharding.
// Must respect each node's EnforceContributionCap-derived memory ceiling.
// Do NOT attempt dense-model pipeline-sharding across WAN nodes — see proposal §3/6.2.
//
// MILESTONE 6 — not implemented yet. Do not start before M1-5 are validated.
func PlanMoEExpertAssignment(modelID string, totalExperts int, candidates []protocol.CapabilityManifest) (map[string][]int, error) {
	return nil, ErrNotImplemented
}
