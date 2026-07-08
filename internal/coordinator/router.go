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

	"github.com/open-inference-mesh/oim/internal/httptls"
	"github.com/open-inference-mesh/oim/internal/protocol"
	"github.com/open-inference-mesh/oim/internal/sse"
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

// rankCandidates scores candidates for job via ScoreForFastLane and returns
// them sorted best-first, filtering out ineligible nodes (-Inf score). Shared
// by DispatchFastLane and PickBestNode so "who is eligible and best" is
// computed identically whether or not a dispatch immediately follows.
func rankCandidates(candidates []NodeWithLoad, job protocol.JobSpec) []NodeWithLoad {
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
	out := make([]NodeWithLoad, len(ranked))
	for i, r := range ranked {
		out[i] = r.node
	}
	return out
}

// filterPushOnly drops pull-delivery nodes from a ranked candidate list. Used
// by the streaming path, which can't relay SSE over the request/response pull
// mailbox (v1). Preserves order.
func filterPushOnly(ranked []NodeWithLoad) []NodeWithLoad {
	out := ranked[:0:0]
	for _, n := range ranked {
		if !n.Manifest.PullDelivery {
			out = append(out, n)
		}
	}
	return out
}

// PickBestNode selects the best eligible node for job WITHOUT dispatching —
// used by the coordinator's node-reservation endpoint (POST /v1/reserve-node),
// which must hand a client a node's public key before any request exists to
// dispatch (node-side pointer consumption, M8). Uses the identical
// eligibility/scoring rules as DispatchFastLane, so a reservation reflects a
// real, dispatchable choice.
func PickBestNode(job protocol.JobSpec, registry *NodeRegistry) (NodeWithLoad, error) {
	candidates, err := registry.CandidatesWithLoad(job.ModelID, job.QuantizationRequired)
	if err != nil {
		return NodeWithLoad{}, fmt.Errorf("fetch candidates: %w", err)
	}
	ranked := rankCandidates(candidates, job)
	if len(ranked) == 0 {
		return NodeWithLoad{}, fmt.Errorf("no eligible nodes available")
	}
	return ranked[0], nil
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
	ranked := rankCandidates(candidates, job)

	attempted := 0
	for _, node := range ranked {
		if attempted >= maxAttempts {
			break
		}
		attempted++
		registry.IncrInFlight(node.Manifest.NodeID)
		dispatchStart := time.Now()
		result, err := registry.deliverJob(ctx, node.Manifest.NodeID, node.Manifest.PullDelivery, TargetFromManifest(node.Manifest), job, messages)
		elapsed := time.Since(dispatchStart)
		registry.DecrInFlight(node.Manifest.NodeID)
		if err != nil {
			registry.MarkUnreachable(node.Manifest.NodeID)
			continue
		}
		// Tag the response with which node served it and on which lane, so a
		// requester's own dashboard can draw its own request's route without the
		// coordinator needing to broadcast per-job routing to anyone else — every
		// caller sees only the answer to its own request (proposal §7.1 privacy split).
		if result != nil {
			result["oim_served_by_node_id"] = node.Manifest.NodeID
			result["oim_lane"] = string(job.Lane)
			result["oim_latency_ms"] = elapsed.Milliseconds()
			// Measured from THIS request's own wall-clock time and actual completion
			// tokens — never the node's self-declared/benchmarked signature — so
			// "Try the mesh" can show a real, this-request tok/s figure.
			if tokens := completionTokensFromResult(result); tokens > 0 && elapsed > 0 {
				result["oim_tokens_per_sec"] = math.Round(float64(tokens)/elapsed.Seconds()*100) / 100
			}
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
// around it like every other dispatch path. The caller supplies the full
// NodeTarget (endpoint + TLS pin, from TargetFromManifest or a Reservation) so
// the pin can never be paired with a different node's endpoint.
func DispatchToResolvedNode(ctx context.Context, job protocol.JobSpec, messages []map[string]any, registry *NodeRegistry, target NodeTarget) (map[string]any, error) {
	registry.IncrInFlight(target.NodeID)
	result, err := registry.deliverJob(ctx, target.NodeID, registry.IsPullNode(target.NodeID), target, job, messages)
	registry.DecrInFlight(target.NodeID)
	if err != nil {
		registry.MarkUnreachable(target.NodeID)
		return nil, err
	}
	return result, nil
}

// completionTokensFromResult reads usage.completion_tokens from an OpenAI-shaped
// chat-completion response, returning 0 when absent rather than guessing.
func completionTokensFromResult(result map[string]any) int {
	usage, ok := result["usage"].(map[string]any)
	if !ok {
		return 0
	}
	if n, ok := usage["completion_tokens"].(float64); ok && n > 0 {
		return int(n)
	}
	return 0
}

// DispatchFastLaneStreaming is DispatchFastLane's streaming counterpart —
// fast lane only (background lane stays buffered/polling by design). It
// selects the best eligible node exactly like DispatchFastLane, but relays
// the node's SSE response directly to clientW as it arrives instead of
// buffering the whole reply, returning the serving node ID and the observed
// completion-token count (read from the trailing SSE usage frame) once the
// stream ends.
//
// headersSent distinguishes two failure modes for the caller: if a node
// couldn't be reached AT ALL, headersSent is false and the caller can still
// return a normal error status (exactly like DispatchFastLane failing). If a
// node accepted the request and streaming began before it failed, headersSent
// is true — the client has already received partial output over a 200
// response, so nothing more can be done at the HTTP-status level; the caller
// should just log and stop. Never retries once relay has started: an
// in-progress stream can't be silently restarted on a different node without
// corrupting what the client already received.
func DispatchFastLaneStreaming(
	ctx context.Context,
	job protocol.JobSpec,
	messages []map[string]any,
	registry *NodeRegistry,
	maxAttempts int,
	clientW http.ResponseWriter,
) (servedByNodeID string, tokens int, headersSent bool, err error) {
	candidates, err := registry.CandidatesWithLoad(job.ModelID, job.QuantizationRequired)
	if err != nil {
		return "", 0, false, fmt.Errorf("fetch candidates: %w", err)
	}
	ranked := rankCandidates(candidates, job)
	// SSE passthrough requires the coordinator to hold the node's streaming
	// HTTP response open — impossible over the pull mailbox, which is
	// request/response only. Pull nodes are therefore skipped for streaming in
	// v1 (buffered delivery to them still works fully). This only affects
	// stream:true requests; "Try the mesh" and the availability probe are
	// buffered, so earning is unaffected. Documented in README as a known v1 gap.
	ranked = filterPushOnly(ranked)

	attempted := 0
	for _, node := range ranked {
		if attempted >= maxAttempts {
			break
		}
		attempted++
		registry.IncrInFlight(node.Manifest.NodeID)
		started, tok, dispatchErr := dispatchToNodeStreaming(ctx, job, messages, TargetFromManifest(node.Manifest), clientW)
		registry.DecrInFlight(node.Manifest.NodeID)
		if dispatchErr != nil {
			if started {
				return "", tok, true, dispatchErr
			}
			registry.MarkUnreachable(node.Manifest.NodeID)
			continue
		}
		return node.Manifest.NodeID, tok, true, nil
	}
	return "", 0, false, fmt.Errorf("no eligible nodes available for job %s (tried %d)", job.JobID, attempted)
}

// dispatchToNodeStreaming is dispatchToNode's streaming counterpart: POSTs
// stream:true to the node and relays its SSE response line-by-line to clientW
// as it arrives, returning the completion-token count read from the trailing
// SSE usage frame. started reports whether any bytes were relayed to clientW
// before an error occurred — see DispatchFastLaneStreaming's doc comment for
// why that distinction matters to the caller's retry logic.
func dispatchToNodeStreaming(
	ctx context.Context,
	job protocol.JobSpec,
	messages []map[string]any,
	target NodeTarget,
	clientW http.ResponseWriter,
) (started bool, tokens int, err error) {
	payload := map[string]any{
		"model":    job.ModelID,
		"messages": messages,
		"stream":   true,
	}
	// Encrypted-pointer path (M8): same forwarding as the buffered dispatchToNode
	// below — a streaming request with a payload pointer (legal since streaming
	// is gated only on the absence of a reservation, not the absence of a
	// pointer) must still let the node fetch+decrypt it, or the node silently
	// runs inference on the empty placeholder messages instead.
	if job.PayloadRef != "" {
		payload["oim_payload_hash"] = job.PayloadRef
		payload["oim_payload_fetch_url"] = job.PayloadFetchURL
		payload["oim_ephemeral_public_key"] = job.PayloadEphemeralPubKey
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return false, 0, fmt.Errorf("marshal dispatch payload: %w", err)
	}

	// Same 120s ceiling as the buffered path (task #53) — a hung Exo must not
	// hold a concurrency-limiter slot, or a node's in-flight counter, forever.
	client := httpClientForTarget(target, 120*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		target.Endpoint+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return false, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-OIM-Job-ID", job.JobID)

	resp, err := client.Do(req)
	if err != nil {
		return false, 0, fmt.Errorf("dispatch to %s: %w", target.Endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return false, 0, fmt.Errorf("node %s returned HTTP %d: %s", target.Endpoint, resp.StatusCode, body)
	}

	// oim_served_by_node_id/oim_lane travel as response headers instead of the
	// buffered path's trailing JSON fields (there's no buffered blob left to
	// tag) — must be set before sse.Relay's first Write() locks in the header
	// set. Harmless to set even if clientW turns out not to be an
	// http.Flusher below — sse.Relay's own check is what actually gates
	// whether streaming proceeds.
	clientW.Header().Set("X-OIM-Served-By-Node-Id", target.NodeID)
	clientW.Header().Set("X-OIM-Lane", string(job.Lane))
	sse.SetHeaders(clientW)

	started, tokens, err = sse.Relay(clientW, resp.Body)
	if err != nil {
		return started, tokens, fmt.Errorf("read node stream: %w", err)
	}
	return started, tokens, nil
}

// NodeTarget is everything dispatch needs to reach one node: its endpoint and
// the TLS certificate fingerprint pinned at that node's registration. The pin
// only means anything paired with its endpoint — threading them as one value
// makes it impossible to dial node A's endpoint with node B's pin.
type NodeTarget struct {
	NodeID         string
	Endpoint       string
	TLSFingerprint string // hex SHA-256 of the node's leaf cert; empty for plain-HTTP nodes
}

// TargetFromManifest builds the dispatch target for a node from its signed
// manifest — the only place an endpoint and its TLS pin should ever be paired.
func TargetFromManifest(m protocol.CapabilityManifest) NodeTarget {
	return NodeTarget{
		NodeID:         m.NodeID,
		Endpoint:       m.ReachabilityEndpoint,
		TLSFingerprint: m.TLSCertFingerprint,
	}
}

// httpClientForTarget returns a plain client for an http:// node, or a
// certificate-pinned one (task: coordinator->node TLS) for an https:// node —
// nodes are independently operated and self-signed, so trust is TOFU
// fingerprint-pinning against what was recorded at that node's registration,
// not chain verification against a shared CA. An empty fingerprint on an
// https:// endpoint always fails closed (see httptls.PinnedClientTLSConfig).
func httpClientForTarget(target NodeTarget, timeout time.Duration) *http.Client {
	if strings.HasPrefix(target.Endpoint, "https://") {
		return httptls.PinnedClient(target.TLSFingerprint, timeout)
	}
	return &http.Client{Timeout: timeout}
}

// dispatchToNode makes a POST to the node's /v1/chat/completions endpoint and returns the response.
func dispatchToNode(
	ctx context.Context,
	job protocol.JobSpec,
	messages []map[string]any,
	target NodeTarget,
) (map[string]any, error) {
	payload := map[string]any{
		"model":    job.ModelID,
		"messages": messages,
		"stream":   false,
	}
	// Encrypted-pointer path (M8): forward the pointer so the assigned node can
	// fetch + decrypt it. Previously dropped silently here — the coordinator
	// threaded these fields into JobSpec but never actually sent them onward,
	// which is why no node ever consumed a pointer end-to-end.
	if job.PayloadRef != "" {
		payload["oim_payload_hash"] = job.PayloadRef
		payload["oim_payload_fetch_url"] = job.PayloadFetchURL
		payload["oim_ephemeral_public_key"] = job.PayloadEphemeralPubKey
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal dispatch payload: %w", err)
	}

	client := httpClientForTarget(target, 120*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		target.Endpoint+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-OIM-Job-ID", job.JobID)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dispatch to %s: %w", target.Endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("node %s returned HTTP %d: %s", target.Endpoint, resp.StatusCode, body)
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
// enforce this at the dispatch layer; the check here is defense-in-depth.
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
		replicaResult, err := dispatchToNode(ctx, replicaJob, replicaMessages, TargetFromManifest(replicaNode))
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

	mergeTarget := SelectMergeNode(podNodes, mergeInputs)
	merged, err := ExecuteMerge(ctx, mergeInputs, job, mergeTarget)
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
			result, err := dispatchToNode(ctx, subJob, subMessages, TargetFromManifest(w.node))
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
