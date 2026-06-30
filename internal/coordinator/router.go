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
	"sync"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// ScoreForFastLane computes a routing score using MEASURED throughput only.
// Self-declared specs are never used — that is the tier-fraud mitigation point (proposal §9.2).
// Returns -Inf for ineligible nodes (wrong sensitivity, missing model, price over ceiling).
func ScoreForFastLane(node protocol.CapabilityManifest, job protocol.JobSpec) float64 {
	if job.Sensitivity == protocol.SensitivityHighRequiresAttestation && !node.HasSecureEnclave {
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
	if node.MeasuredSignature != nil {
		return node.MeasuredSignature.TokensPerSecDecode
	}
	// No benchmark yet: score 1.0 so the node is eligible but ranked last among
	// measured nodes. This allows new nodes to receive jobs while encouraging benchmarking.
	return 1.0
}

// DispatchFastLane selects the best eligible node and dispatches the job.
// On failure, marks that node unreachable and tries the next — never retries the same node.
func DispatchFastLane(
	ctx context.Context,
	job protocol.JobSpec,
	messages []map[string]any,
	registry *NodeRegistry,
	maxAttempts int,
) (map[string]any, error) {
	candidates, err := registry.Candidates(job.ModelID, job.QuantizationRequired)
	if err != nil {
		return nil, fmt.Errorf("fetch candidates: %w", err)
	}

	type scored struct {
		score float64
		node  protocol.CapabilityManifest
	}
	var ranked []scored
	for _, n := range candidates {
		s := ScoreForFastLane(n, job)
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
		result, err := dispatchToNode(ctx, job, messages, r.node.ReachabilityEndpoint)
		if err != nil {
			registry.MarkUnreachable(r.node.NodeID)
			continue
		}
		return result, nil
	}
	return nil, fmt.Errorf("no eligible nodes available for job %s (tried %d)", job.JobID, attempted)
}

// BackgroundAssignment is the persisted sticky-session record for a recurring job.
type BackgroundAssignment struct {
	JobID   string   `json:"job_id"`
	Primary string   `json:"primary"`  // node_id
	Backups []string `json:"backups"`  // ordered by preference
}

// AssignBackgroundJob returns a persisted assignment with primary + backup nodes.
// The assignment MUST be stored by the caller — recomputing fresh each cycle defeats sticky-session.
func AssignBackgroundJob(job protocol.JobSpec, registry *NodeRegistry) (*BackgroundAssignment, error) {
	candidates, err := registry.Candidates(job.ModelID, job.QuantizationRequired)
	if err != nil {
		return nil, fmt.Errorf("fetch candidates: %w", err)
	}

	type scored struct {
		score float64
		node  protocol.CapabilityManifest
	}
	var ranked []scored
	for _, n := range candidates {
		s := ScoreForFastLane(n, job) // same eligibility scoring
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
		backups = append(backups, ranked[i].node.NodeID)
	}
	return &BackgroundAssignment{
		JobID:   job.JobID,
		Primary: ranked[0].node.NodeID,
		Backups: backups,
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
