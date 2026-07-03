// Copyright (C) 2024 Open Inference Mesh
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// ScoreForFastLane computes a routing score using MEASURED throughput only.
// Self-declared specs are never used — that is the tier-fraud mitigation point (proposal §9.2).
// inFlight is the coordinator-observed concurrent job count for this node; each additional
// in-flight job halves the node's effective score so the router naturally load-balances.
// enclaveAttested must come from the registry's coordinator-VERIFIED status
// (NodeWithLoad.EnclaveAttested), never from node.HasSecureEnclave — that field
// is self-declared by the node and proves nothing (Fable security review:
// self-declared attestation, unenforced privacy claims).
// Returns -Inf for ineligible nodes (wrong sensitivity, missing model, price over ceiling).
func ScoreForFastLane(node protocol.CapabilityManifest, job protocol.JobSpec, inFlight int32, enclaveAttested bool) float64 {
	if job.Sensitivity == protocol.SensitivityHighRequiresAttestation && !enclaveAttested {
		return math.Inf(-1)
	}
	if !hasModel(node, job.ModelID, job.QuantizationRequired) {
		return math.Inf(-1)
	}
	if job.MaxPricePerUnit > 0 {
		if price, ok := node.PricePerUnit["compute_cycles"]; ok && price > job.MaxPricePerUnit {
			return math.Inf(-1)
		}
	}
	base := 1.0
	if node.MeasuredSignature != nil {
		base = node.MeasuredSignature.TokensPerSecDecode
	}
	// Load penalty: each in-flight job reduces effective score.
	// At 1 in-flight: ×0.67; at 2: ×0.5; at 3: ×0.4; at 6: ×0.25.
	return base / (1.0 + float64(inFlight)*0.5)
}

// DispatchFastLane selects the best eligible node (by measured TPS adjusted for current
// load) and dispatches the job. On failure it marks that node unreachable and tries the
// next — never retries the same node. In-flight counters are tracked atomically so that
// concurrent dispatches naturally load-balance across nodes.
func DispatchFastLane(
	ctx context.Context,
	job protocol.JobSpec,
	messages []map[string]any,
	registry *NodeRegistry,
	maxAttempts int,
) (map[string]any, error) {
	candidates, err := registry.CandidatesWithLoad(job.ModelID, job.QuantizationRequired)
	if err != nil {
		return nil, fmt.Errorf("fetch candidates: %w", err)
	}

	type scored struct {
		score float64
		node  NodeWithLoad
	}
	var ranked []scored
	for _, n := range candidates {
		s := ScoreForFastLane(n.Manifest, job, n.InFlight, n.EnclaveAttested)
		if !math.IsInf(s, -1) {
			ranked = append(ranked, scored{s, n})
		}
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	attempted := 0
	for _, r := range ranked {
		if attempted >= maxAttempts {
			break
		}
		attempted++
		registry.IncrInFlight(r.node.Manifest.NodeID)
		result, err := dispatchToNode(ctx, job, messages, r.node.Manifest.ReachabilityEndpoint)
		registry.DecrInFlight(r.node.Manifest.NodeID)
		if err != nil {
			registry.MarkUnreachable(r.node.Manifest.NodeID)
			continue
		}
		// Tag the response with which node served it and on which lane, so a
		// requester's own dashboard can draw its own request's route without the
		// coordinator needing to broadcast per-job routing to anyone else — every
		// caller sees only the answer to its own request (proposal §7.1 privacy split).
		if result != nil {
			result["oim_served_by_node_id"] = r.node.Manifest.NodeID
			result["oim_lane"] = string(job.Lane)
		}
		return result, nil
	}
	return nil, fmt.Errorf("no eligible nodes available for job %s (tried %d)", job.JobID, attempted)
}

// BackgroundAssignment is the persisted sticky-session record for a recurring job.
// JobSpec is stored alongside the node selection so /jobs/background/execute can
// resolve routing (including whether decomposition applies) without the caller
// re-sending the full spec on every recurrence cycle.
type BackgroundAssignment struct {
	JobID   string           `json:"job_id"`
	Primary string           `json:"primary"` // node_id
	Backups []string         `json:"backups"` // ordered by preference
	JobSpec protocol.JobSpec `json:"job_spec"`
}

// AssignBackgroundJob returns a persisted assignment with primary + backup nodes.
// The assignment MUST be stored by the caller — recomputing fresh each cycle defeats sticky-session.
func AssignBackgroundJob(job protocol.JobSpec, registry *NodeRegistry) (*BackgroundAssignment, error) {
	candidates, err := registry.CandidatesWithLoad(job.ModelID, job.QuantizationRequired)
	if err != nil {
		return nil, fmt.Errorf("fetch candidates: %w", err)
	}

	type scored struct {
		score float64
		node  NodeWithLoad
	}
	var ranked []scored
	for _, n := range candidates {
		s := ScoreForFastLane(n.Manifest, job, n.InFlight, n.EnclaveAttested)
		if !math.IsInf(s, -1) {
			ranked = append(ranked, scored{s, n})
		}
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	needed := 1 + job.RedundancyDepth
	if len(ranked) < needed {
		return nil, fmt.Errorf("need %d nodes (1 primary + %d backups) but only %d eligible",
			needed, job.RedundancyDepth, len(ranked))
	}
	backups := make([]string, 0, job.RedundancyDepth)
	for i := 1; i <= job.RedundancyDepth; i++ {
		backups = append(backups, ranked[i].node.Manifest.NodeID)
	}
	return &BackgroundAssignment{
		JobID:   job.JobID,
		Primary: ranked[0].node.Manifest.NodeID,
		Backups: backups,
		JobSpec: job,
	}, nil
}

// ResolveForCycle returns (nodeID, isContinuation) for one recurrence cycle.
// isContinuation=true means the primary is still live (model reload can be skipped).
// isContinuation=false means a backup was promoted (cold start on that node).
// Returns an error only when ALL nodes in the assignment are down.
func ResolveForCycle(assignment *BackgroundAssignment, registry *NodeRegistry) (string, bool, error) {
	if registry.IsLive(assignment.Primary) {
		return assignment.Primary, true, nil
	}
	for _, backup := range assignment.Backups {
		if registry.IsLive(backup) {
			return backup, false, nil
		}
	}
	return "", false, fmt.Errorf("background job %s: all assigned nodes are down (primary=%s, backups=%v)",
		assignment.JobID, assignment.Primary, assignment.Backups)
}

// AssignmentStore is a thread-safe in-memory store for persisted background assignments.
type AssignmentStore struct {
	mu   sync.RWMutex
	data map[string]*BackgroundAssignment
}

func NewAssignmentStore() *AssignmentStore {
	return &AssignmentStore{data: make(map[string]*BackgroundAssignment)}
}

func (s *AssignmentStore) Save(a *BackgroundAssignment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[a.JobID] = a
}

func (s *AssignmentStore) Get(jobID string) (*BackgroundAssignment, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.data[jobID]
	return a, ok
}

// DispatchToResolvedNode dispatches a job to a specific, already-selected node.
// Exported for /jobs/background/execute: unlike DispatchFastLane, the caller
// has already resolved which node should handle this cycle via
// ResolveForCycle (sticky-session) — this skips candidate scoring entirely and
// just makes the call, incrementing/decrementing the node's in-flight counter
// around it like every other dispatch path.
func DispatchToResolvedNode(ctx context.Context, job protocol.JobSpec, messages []map[string]any, registry *NodeRegistry, nodeID, nodeEndpoint string) (map[string]any, error) {
	registry.IncrInFlight(nodeID)
	result, err := dispatchToNode(ctx, job, messages, nodeEndpoint)
	registry.DecrInFlight(nodeID)
	if err != nil {
		registry.MarkUnreachable(nodeID)
		return nil, err
	}
	return result, nil
}

// dispatchToNode makes a POST to the node's /v1/chat/completions endpoint and returns the response.
func dispatchToNode(
	ctx context.Context,
	job protocol.JobSpec,
	messages []map[string]any,
	nodeEndpoint string,
) (map[string]any, error) {
	payload := map[string]any{
		"model":    job.ModelID,
		"messages": messages,
		"stream":   false,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal dispatch payload: %w", err)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		nodeEndpoint+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-OIM-Job-ID", job.JobID)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dispatch to %s: %w", nodeEndpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("node %s returned HTTP %d: %s", nodeEndpoint, resp.StatusCode, body)
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode node response: %w", err)
	}
	return result, nil
}

// RouteWithDecomposition is the entry point for background-lane jobs that have
// AllowDecomposition=true. It attempts query decomposition, dispatches sub-tasks
// in parallel, verifies results, and merges them.
//
// Fast-lane guard: if job.Lane == JobLaneFast this function returns immediately
// with an error — fast-lane jobs must never reach this code path. Callers should
// enforce this at the dispatch layer; the check here is defence-in-depth.
//
// Fallback contract: any ErrNotImplemented from the decomposer causes RouteWithDecomposition
// to fall back to single-node DispatchFastLane/AssignBackgroundJob routing. The caller
// never sees ErrNotImplemented — it is a coordinator-internal signal.
func RouteWithDecomposition(
	ctx context.Context,
	job protocol.JobSpec,
	messages []map[string]any,
	registry *NodeRegistry,
	model DecompositionModel,
	maxAttempts int,
	jobQueue *JobQueue,
) (map[string]any, error) {
	// Hard gate: this function must never be called for fast-lane jobs.
	if job.Lane == protocol.JobLaneFast {
		return nil, fmt.Errorf(
			"RouteWithDecomposition called for fast-lane job %q: "+
				"decomposition is background-lane only; this is a caller bug",
			job.JobID,
		)
	}

	// Attempt decomposition.
	decomposed, err := DecomposeJob(job, model)
	if err != nil {
		// ErrNotImplemented: decomposition not applicable — fall back to single-node.
		return DispatchFastLane(ctx, job, messages, registry, maxAttempts)
	}

	if !IsDecompositionWorthIt(decomposed, 50.0) {
		return DispatchFastLane(ctx, job, messages, registry, maxAttempts)
	}

	// Collect live nodes for this job's model.
	candidates, err := registry.CandidatesWithLoad(job.ModelID, job.QuantizationRequired)
	if err != nil || len(candidates) == 0 {
		return DispatchFastLane(ctx, job, messages, registry, maxAttempts)
	}
	// Same coordinator-verified-attestation gate as ScoreForFastLane, applied
	// here too — decomposition sub-tasks fan out to multiple nodes, so a high-
	// sensitivity job must never hand a shard to a node whose Secure Enclave
	// claim hasn't actually been verified (defense in depth alongside the
	// fast-lane/background-lane gate).
	if job.Sensitivity == protocol.SensitivityHighRequiresAttestation {
		attested := candidates[:0]
		for _, c := range candidates {
			if c.EnclaveAttested {
				attested = append(attested, c)
			}
		}
		candidates = attested
		if len(candidates) == 0 {
			return DispatchFastLane(ctx, job, messages, registry, maxAttempts)
		}
	}
	podNodes := make([]protocol.CapabilityManifest, len(candidates))
	for i, c := range candidates {
		podNodes[i] = c.Manifest
	}

	// Cap concurrency to MaxParallelNodes if set.
	subTasks := decomposed.SubTasks
	if job.MaxParallelNodes > 0 && len(subTasks) > job.MaxParallelNodes {
		subTasks = subTasks[:job.MaxParallelNodes]
	}

	// Dispatch analytical sub-tasks (all except MERGE) in parallel.
	analyticalTasks := make([]SubTask, 0, len(subTasks))
	for _, t := range subTasks {
		if t.SubTaskType != SubTaskMerge {
			analyticalTasks = append(analyticalTasks, t)
		}
	}

	subResults, err := DispatchSubTasksInParallel(ctx, analyticalTasks, podNodes)
	if err != nil {
		// Parallel dispatch failed; fall back to single-node for the original job.
		return DispatchFastLane(ctx, job, messages, registry, maxAttempts)
	}

	for i, res := range subResults {
		if res == nil {
			return nil, fmt.Errorf("RouteWithDecomposition job %s: sub-task %d returned nil", job.JobID, i)
		}
	}

	// Parallel verification, when it applies, works by REPLICATION not comparison
	// across siblings: sub-tasks are heterogeneous (schema-lookup vs anomaly-detection
	// vs ...), so their checksums can never match each other — that pairing was the
	// original bug here. Instead, spot-check ONE sampled sub-task by re-dispatching
	// its identical request to a second, independent node and comparing those two
	// results. A verified job means the mesh is producing deterministic output for
	// this job right now; an unverified job must fail outright, never merge partial
	// trust (per the "no silent low-quality response" constraint).
	verificationPassed := true
	estimatedInputTokens := estimateAnalyticalPromptTokens(analyticalTasks)
	if ShouldUseParallelVerification(job, len(podNodes), estimatedInputTokens) && len(analyticalTasks) >= 1 && len(podNodes) >= 2 {
		verifyIdx := 0 // sample the first sub-task; a future pass could randomize
		// DispatchSubTasksInParallel assigns podNodes[i % len(podNodes)] as the primary
		// node for sub-task i — the next node in the ring is guaranteed distinct from
		// it as long as len(podNodes) >= 2, which is already gated above.
		replicaNode := podNodes[(verifyIdx+1)%len(podNodes)]

		replicaJob, replicaMessages := buildSubTaskJob(analyticalTasks[verifyIdx])
		replicaResult, err := dispatchToNode(ctx, replicaJob, replicaMessages, replicaNode.ReachabilityEndpoint)
		if err != nil {
			verificationPassed = false // can't verify — fail closed, don't merge on faith
		} else {
			checkA, errA := ComputeOutputChecksum(subResults[verifyIdx])
			checkB, errB := ComputeOutputChecksum(replicaResult)
			verificationPassed = errA == nil && errB == nil && checkA == checkB
		}
	}

	mergeInputs := make([]MergeInput, 0, len(subResults))
	for i, res := range subResults {
		mergeInputs = append(mergeInputs, MergeInput{
			SubTaskID:          analyticalTasks[i].SubTaskID,
			SubTaskType:        string(analyticalTasks[i].SubTaskType),
			Result:             res,
			VerificationPassed: verificationPassed,
		})
	}

	// Check for any verification failures before merging.
	for _, mi := range mergeInputs {
		if !mi.VerificationPassed {
			return nil, fmt.Errorf(
				"RouteWithDecomposition job %s: sub-task %s failed verification; job failed",
				job.JobID, mi.SubTaskID,
			)
		}
	}

	mergeEndpoint := SelectMergeNode(podNodes, mergeInputs)
	merged, err := ExecuteMerge(ctx, mergeInputs, job, mergeEndpoint)
	if err != nil {
		return nil, fmt.Errorf("RouteWithDecomposition job %s: merge: %w", job.JobID, err)
	}
	return merged.MergedOutput, nil
}

// DispatchSubTasksInParallel fans out sub-tasks to available nodes using goroutines.
// Each sub-task is assigned the next available node in round-robin order.
// Results are returned in the same order as subTasks — a nil entry indicates that
// sub-task failed; callers must treat nil as a job failure.
//
// WaitForDependencies is called before each sub-task dispatch to block tasks that
// have unresolved DependsOn entries (currently only MERGE tasks, which are excluded
// from the input by convention).
func DispatchSubTasksInParallel(
	ctx context.Context,
	subTasks []SubTask,
	podNodes []protocol.CapabilityManifest,
) ([]map[string]any, error) {
	if len(podNodes) == 0 {
		return nil, fmt.Errorf("DispatchSubTasksInParallel: no pod nodes available")
	}

	results := make([]map[string]any, len(subTasks))
	completedResults := make(map[string]map[string]any)
	var mu sync.Mutex

	type work struct {
		index   int
		subTask SubTask
		node    protocol.CapabilityManifest
	}

	workItems := make([]work, len(subTasks))
	for i, t := range subTasks {
		workItems[i] = work{
			index:   i,
			subTask: t,
			node:    podNodes[i%len(podNodes)],
		}
	}

	errCh := make(chan error, len(subTasks))
	var wg sync.WaitGroup

	for _, w := range workItems {
		wg.Add(1)
		go func(w work) {
			defer wg.Done()

			// Block until dependencies are met (with 30s timeout per sub-task).
			if !WaitForDependencies(w.subTask, completedResults, &mu, 30*time.Second) {
				errCh <- fmt.Errorf("sub-task %s: dependency timeout", w.subTask.SubTaskID)
				return
			}

			subJob, subMessages := buildSubTaskJob(w.subTask)
			result, err := dispatchToNode(ctx, subJob, subMessages, w.node.ReachabilityEndpoint)
			if err != nil {
				errCh <- fmt.Errorf("sub-task %s: dispatch: %w", w.subTask.SubTaskID, err)
				return
			}
			mu.Lock()
			results[w.index] = result
			completedResults[w.subTask.SubTaskID] = result
			mu.Unlock()
		}(w)
	}

	wg.Wait()
	close(errCh)

	var errs []string
	for err := range errCh {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("DispatchSubTasksInParallel: %s", strings.Join(errs, "; "))
	}
	return results, nil
}

// estimateAnalyticalPromptTokens sums estimateTokens (splitter.go) across all
// analytical sub-task prompts, giving ShouldUseParallelVerification a real size
// signal instead of a hardcoded placeholder.
func estimateAnalyticalPromptTokens(tasks []SubTask) int {
	total := 0
	for _, t := range tasks {
		total += estimateTokens(t.Prompt)
	}
	return total
}

// buildSubTaskJob constructs the scoped JobSpec and message payload for dispatching
// one decomposed sub-task. Shared by DispatchSubTasksInParallel's primary dispatch
// and RouteWithDecomposition's verification replica dispatch — both must build the
// identical request for a checksum comparison between them to mean anything.
func buildSubTaskJob(t SubTask) (protocol.JobSpec, []map[string]any) {
	subJob := protocol.JobSpec{
		JobID:                  t.SubTaskID,
		RequesterID:            t.ParentJobID,
		ModelID:                t.ModelID,
		Lane:                   protocol.JobLaneBackground,
		AllowDecomposition:     false, // sub-tasks are never further decomposed
		AllowDocumentSplitting: false,
	}
	subMessages := []map[string]any{
		{"role": "user", "content": t.Prompt},
	}
	return subJob, subMessages
}

// WaitForDependencies blocks until all SubTaskIDs listed in subTask.DependsOn are
// present in completedResults. Returns false if timeout elapses before all
// dependencies are satisfied.
//
// mu protects completedResults; callers must pass the same mutex used by the
// goroutines that write to completedResults.
func WaitForDependencies(
	subTask SubTask,
	completedResults map[string]map[string]any,
	mu *sync.Mutex,
	timeout time.Duration,
) bool {
	if len(subTask.DependsOn) == 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		allDone := true
		for _, dep := range subTask.DependsOn {
			if _, ok := completedResults[dep]; !ok {
				allDone = false
				break
			}
		}
		mu.Unlock()
		if allDone {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
