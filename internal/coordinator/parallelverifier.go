// Package coordinator — parallel split verification for background-lane jobs.
//
// Cheaper than full re-running the job on a second node: both halves of a split
// document are processed in parallel on different nodes, the output checksums are
// compared, and a full re-run is triggered only when they diverge.
//
// This module EXTENDS verification.go — it does not replace SpotCheckFastLane or
// StatisticalBaselineCheck. Those remain the verification path for single-node
// fast-lane and background-lane jobs respectively.
//
// Critical constraint: checksum comparison is only valid for deterministic outputs
// (temperature=0, structured JSON, classification labels). Never apply this to
// free-text generation — temperature>0 produces different tokens on every call even
// when both nodes are correct, so the checksums will always diverge.
// ShouldUseParallelVerification enforces this gate strictly.
package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// SplitVerificationResult records one node's output for one document split.
type SplitVerificationResult struct {
	SplitID          string `json:"split_id"`
	NodeID           string `json:"node_id"`
	OutputChecksum   string `json:"output_checksum"` // SHA-256 of canonical inference output JSON
	InputChecksum    string `json:"input_checksum"`  // DocumentSplit.Checksum — for audit trail
	Passed           bool   `json:"passed"`
	DivergenceDetail string `json:"divergence_detail,omitempty"` // populated only when Passed=false
}

// ComputeOutputChecksum produces a stable SHA-256 hex digest of an inference output
// for comparison across nodes. Canonical JSON serialization (keys sorted) ensures
// that structurally equivalent outputs hash identically regardless of field order.
//
// Only valid when temperature=0 is enforced on the job — see package comment.
// Do not call this for free-text generation outputs.
func ComputeOutputChecksum(inferenceOutput map[string]any) (string, error) {
	canonical, err := canonicalJSON(inferenceOutput)
	if err != nil {
		return "", fmt.Errorf("compute output checksum: canonical JSON: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalJSON marshals a map to JSON with keys sorted, producing a stable
// byte sequence for hashing regardless of Go's random map iteration order.
func canonicalJSON(v map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	ordered := make([]byte, 0, 256)
	ordered = append(ordered, '{')
	for i, k := range keys {
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		// Recursively canonicalise nested maps; leave everything else to json.Marshal.
		var vb []byte
		if nested, ok := v[k].(map[string]any); ok {
			vb, err = canonicalJSON(nested)
		} else {
			vb, err = json.Marshal(v[k])
		}
		if err != nil {
			return nil, err
		}
		ordered = append(ordered, kb...)
		ordered = append(ordered, ':')
		ordered = append(ordered, vb...)
		if i < len(keys)-1 {
			ordered = append(ordered, ',')
		}
	}
	ordered = append(ordered, '}')
	return ordered, nil
}

// VerifySplitPair compares the output checksums of left and right split results
// from two different nodes. tolerance=0.0 requires exact checksum equality
// (appropriate for classification and structured JSON outputs).
//
// A non-zero tolerance would require semantic similarity rather than checksum
// comparison — only implement that if temperature=0 determinism cannot be
// achieved for a specific workload class.
func VerifySplitPair(left, right SplitVerificationResult, tolerance float64) bool {
	if tolerance != 0.0 {
		// Non-zero tolerance requires semantic similarity metrics, not checksums.
		// This path is not yet implemented; callers must use tolerance=0.0 or
		// fall back to SpotCheckFastLane for non-deterministic workloads.
		return false
	}
	return left.OutputChecksum == right.OutputChecksum
}

// TriggerFullRerunIfDiverged runs the original unsplit job on fallbackNodeEndpoint
// when left and right split checksums do not match. Returns the fallback result
// when a rerun is triggered, or (nil, nil) when checksums matched.
//
// The fallback node must be a third node that did NOT participate in the original
// split — pass a node from the pod that was NOT in the split assignment.
// Running the fallback on a node that already diverged provides no new information.
//
// STUB: actual dispatch is implemented; the fallback node selection policy
// (ensuring it is independent of the split nodes) is enforced by the caller.
func TriggerFullRerunIfDiverged(
	ctx context.Context,
	jobID string,
	splitResults []SplitVerificationResult,
	fallbackNodeEndpoint, fallbackTLSFingerprint string,
	job protocol.JobSpec,
	messages []map[string]any,
) (map[string]any, error) {
	// Check if all split results passed.
	allPassed := true
	for _, r := range splitResults {
		if !r.Passed {
			allPassed = false
			break
		}
	}
	if allPassed {
		return nil, nil // no divergence; no rerun needed
	}

	if fallbackNodeEndpoint == "" {
		return nil, fmt.Errorf("trigger full rerun job %s: no fallback node endpoint provided", jobID)
	}

	// Full re-run of the original unsplit job on the fallback node.
	// dispatchToNode is defined in router.go (same package).
	result, err := dispatchToNode(ctx, job, messages, fallbackNodeEndpoint, fallbackTLSFingerprint)
	if err != nil {
		return nil, fmt.Errorf("trigger full rerun job %s: fallback dispatch: %w", jobID, err)
	}
	return result, nil
}

// ShouldUseParallelVerification returns true only when ALL of the following hold:
//  1. Lane is background (parallel verification is never applied to fast-lane jobs)
//  2. RequireDeterministicOutput is set (temperature=0 enforced — checksums only
//     make sense for deterministic outputs)
//  3. The job's payload is large enough that splitting saves more than verification adds
//     (threshold: estimated input > 500 tokens)
//  4. At least 2 live nodes in the pod are eligible for the job's model/quantization
//
// If any condition fails, callers must fall back to SpotCheckFastLane or
// StatisticalBaselineCheck from verification.go, not this module.
func ShouldUseParallelVerification(job protocol.JobSpec, eligibleNodeCount int, estimatedInputTokens int) bool {
	if job.Lane != protocol.JobLaneBackground {
		return false
	}
	if !job.RequireDeterministicOutput {
		// Without temperature=0 enforcement, output checksums are not comparable.
		// This is the most common disqualification — operators must explicitly opt in.
		return false
	}
	if estimatedInputTokens < 500 {
		// Below this threshold, the coordination overhead of splitting and verifying
		// exceeds the parallelism gain. Tune against Milestone 2 pilot data.
		return false
	}
	if eligibleNodeCount < 2 {
		// Parallel verification requires at least two independent nodes.
		return false
	}
	return true
}
