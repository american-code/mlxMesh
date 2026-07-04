// Package coordinator — merge parallel sub-task results into a single response.
//
// The merger is the final stage of the parallel processing pipeline:
//
//	SplitDocument → parallel inference on N nodes → parallelverifier → merger
//
// It refuses to incorporate any sub-task result whose verification did not pass.
// A failed verification is a job failure, not a silent quality degradation.
// This is a hard constraint from the parallel processing spec.
package coordinator

import (
	"context"
	"fmt"
	"strings"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// MergeInput is one verified sub-task result handed to the merger.
// VerificationPassed must be true for the input to be incorporated.
// Any MergeInput with VerificationPassed=false causes ExecuteMerge to return an error.
type MergeInput struct {
	SubTaskID          string         `json:"sub_task_id"`
	SubTaskType        string         `json:"sub_task_type"` // matches SubTaskType constants in decomposer.go
	Result             map[string]any `json:"result"`
	Position           string         `json:"position"`            // "left" | "right" | "" (for non-split sub-tasks)
	VerificationPassed bool           `json:"verification_passed"` // must be true before incorporating
}

// MergedResult is the output of ExecuteMerge, ready to return to the requester.
type MergedResult struct {
	OriginalJobID           string         `json:"original_job_id"`
	MergedOutput            map[string]any `json:"merged_output"`
	SubTaskCount            int            `json:"sub_task_count"`
	AnyVerificationFailures bool           `json:"any_verification_failures"` // always false on success; true only on partial-merge path (not used)
	MergeNodeID             string         `json:"merge_node_id,omitempty"`   // node that ran the merge inference call, if any
}

// SelectMergeNode picks the node that will run the merge inference call.
// Prefers a node that did NOT participate in any of the sub-task inputs to avoid
// correlated errors. Falls back to the first eligible node when all nodes ran sub-tasks.
//
// podNodes is the full list of live nodes in the pod.
// subTaskResults carries the NodeIDs that already ran sub-tasks.
// Returns the endpoint plus its TLS pin (task: coordinator->node TLS) — both
// travel together since the pin only means anything paired with its endpoint.
func SelectMergeNode(podNodes []protocol.CapabilityManifest, subTaskResults []MergeInput) (endpoint, tlsFingerprint string) {
	usedNodeIDs := make(map[string]bool, len(subTaskResults))
	for _, r := range subTaskResults {
		if nid, ok := r.Result["_node_id"].(string); ok {
			usedNodeIDs[nid] = true
		}
	}
	for _, n := range podNodes {
		if !usedNodeIDs[n.NodeID] {
			return n.ReachabilityEndpoint, n.TLSCertFingerprint
		}
	}
	// All nodes participated — fall back to first.
	if len(podNodes) > 0 {
		return podNodes[0].ReachabilityEndpoint, podNodes[0].TLSCertFingerprint
	}
	return "", ""
}

// BuildMergePrompt constructs the system+user prompt for the merge inference call.
// The merge model receives the sub-task outputs in position order (left then right
// for document splits) and is instructed to produce a single coherent combined response.
//
// This is a stub — the actual prompt template should be tuned against the specific
// model and task type once Milestone 2 pilot data is available.
func BuildMergePrompt(subTaskResults []MergeInput, originalJobSpec protocol.JobSpec) string {
	var sb strings.Builder
	sb.WriteString("You are a result merger. You receive the outputs of parallel inference sub-tasks ")
	sb.WriteString("that were run on separate halves of a large input document. ")
	sb.WriteString("Produce a single coherent response that combines them faithfully, ")
	sb.WriteString("without adding information not present in the sub-task outputs.\n\n")

	// Emit sub-task results in position order: left before right, then unordered.
	ordered := make([]MergeInput, 0, len(subTaskResults))
	for _, r := range subTaskResults {
		if r.Position == "left" {
			ordered = append([]MergeInput{r}, ordered...)
		} else {
			ordered = append(ordered, r)
		}
	}
	for i, r := range ordered {
		fmt.Fprintf(&sb, "## Sub-task %d", i+1)
		if r.Position != "" {
			fmt.Fprintf(&sb, " (%s)", r.Position)
		}
		sb.WriteString("\n")
		if content, ok := r.Result["choices"].([]any); ok && len(content) > 0 {
			if choice, ok := content[0].(map[string]any); ok {
				if msg, ok := choice["message"].(map[string]any); ok {
					if text, ok := msg["content"].(string); ok {
						sb.WriteString(text)
					}
				}
			}
		}
		sb.WriteString("\n\n")
	}
	sb.WriteString("Combine the above into a single response for job: ")
	sb.WriteString(originalJobSpec.JobID)
	return sb.String()
}

// ExecuteMerge combines subTaskResults into a single MergedResult by running a
// merge inference call on mergeNodeEndpoint.
//
// Hard failure modes (returns error, never silently degrades):
//   - Any MergeInput has VerificationPassed=false → job failure
//   - mergeNodeEndpoint is empty → job failure
//   - The merge inference call itself fails → job failure
//
// The caller is responsible for providing a verified set of inputs. Passing an
// unverified input is a bug in the caller, not a condition ExecuteMerge handles
// by falling back to single-node output.
func ExecuteMerge(
	ctx context.Context,
	mergeInputs []MergeInput,
	originalJobSpec protocol.JobSpec,
	mergeNodeEndpoint, tlsFingerprint string,
) (MergedResult, error) {
	// Gate 1: all inputs must be verified.
	for _, inp := range mergeInputs {
		if !inp.VerificationPassed {
			return MergedResult{}, fmt.Errorf(
				"merge job %s: sub-task %q has VerificationPassed=false; "+
					"incorporating an unverified result is not permitted — treating as job failure",
				originalJobSpec.JobID, inp.SubTaskID,
			)
		}
	}

	// Gate 2: merge node must be reachable.
	if mergeNodeEndpoint == "" {
		return MergedResult{}, fmt.Errorf(
			"merge job %s: no merge node endpoint available", originalJobSpec.JobID,
		)
	}

	// Build the merge prompt and dispatch to the merge node.
	mergePrompt := BuildMergePrompt(mergeInputs, originalJobSpec)
	mergeMessages := []map[string]any{
		{"role": "system", "content": mergePrompt},
	}
	// Include the original user message if it can be extracted from the job.
	// In the current architecture PayloadRef is encrypted; the merge prompt itself
	// carries the sub-task outputs, which is sufficient.

	result, err := dispatchToNode(ctx, originalJobSpec, mergeMessages, mergeNodeEndpoint, tlsFingerprint)
	if err != nil {
		return MergedResult{}, fmt.Errorf(
			"merge job %s: merge inference failed: %w", originalJobSpec.JobID, err,
		)
	}

	return MergedResult{
		OriginalJobID:           originalJobSpec.JobID,
		MergedOutput:            result,
		SubTaskCount:            len(mergeInputs),
		AnyVerificationFailures: false,
		MergeNodeID:             mergeNodeEndpoint,
	}, nil
}

// MergeSplitOutputs is a convenience wrapper for the common two-split case.
// It constructs MergeInput values from left and right inference results,
// verifies both passed, and delegates to ExecuteMerge.
func MergeSplitOutputs(
	ctx context.Context,
	leftResult, rightResult map[string]any,
	leftVerified, rightVerified bool,
	leftSplitID, rightSplitID string,
	strategy SplitStrategy,
	originalJobSpec protocol.JobSpec,
	mergeNodeEndpoint, tlsFingerprint string,
) (MergedResult, error) {
	inputs := []MergeInput{
		{
			SubTaskID:          leftSplitID,
			SubTaskType:        "document_split",
			Result:             leftResult,
			Position:           "left",
			VerificationPassed: leftVerified,
		},
		{
			SubTaskID:          rightSplitID,
			SubTaskType:        "document_split",
			Result:             rightResult,
			Position:           "right",
			VerificationPassed: rightVerified,
		},
	}
	_ = strategy // reserved for strategy-specific merge heuristics in a future iteration
	return ExecuteMerge(ctx, inputs, originalJobSpec, mergeNodeEndpoint, tlsFingerprint)
}
