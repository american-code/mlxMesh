package coordinator

import (
	"context"
	"fmt"
	"sync"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// PullDispatcher delivers jobs to nodes that receive work by long-polling the
// coordinator (outbound-only, "mining-pool" model) instead of accepting an
// inbound dispatch connection. This is what removes the entire NAT /
// port-forwarding / reachability problem class: the node opens the connection,
// so the coordinator never has to reach IN to it.
//
// Flow for one job:
//  1. Router calls Dispatch(nodeID, job) — registers a result waiter keyed by
//     job ID, hands the job to the node's queue, and blocks.
//  2. The node's claim loop, blocked in Claim(nodeID), receives the job and
//     runs it locally via Exo.
//  3. The node calls the coordinator's /jobs/result, which calls SubmitResult,
//     unblocking the Dispatch waiter with the node's output.
//
// A dead/slow node simply never claims (or never returns a result); Dispatch's
// context deadline fires and the router treats it exactly like a failed push
// dispatch — mark unreachable, try the next candidate. No special-casing.
type PullDispatcher struct {
	mu      sync.Mutex
	queues  map[string]chan *PendingJob // nodeID → jobs waiting to be claimed
	waiters map[string]chan JobResult   // jobID → result channel

	// queueDepth bounds how many jobs can sit unclaimed for a single node
	// before Dispatch fails fast (rather than blocking on a full channel) —
	// a node that isn't draining its queue is effectively unreachable.
	queueDepth int
}

// PendingJob is one unit of work handed to a node's claim loop.
type PendingJob struct {
	JobID    string           `json:"job_id"`
	Job      protocol.JobSpec `json:"job"`
	Messages []map[string]any `json:"messages"`
}

// JobResult is the node's completion for a pending job.
type JobResult struct {
	Result map[string]any
	Err    error
}

// DefaultPullQueueDepth is a small per-node backlog cap. A pull node claims one
// job per outbound connection and can hold several connections open, so a deep
// per-node queue isn't needed — a backlog this size already means the node is
// falling behind and further jobs should route elsewhere.
const DefaultPullQueueDepth = 16

func NewPullDispatcher() *PullDispatcher {
	return &PullDispatcher{
		queues:     make(map[string]chan *PendingJob),
		waiters:    make(map[string]chan JobResult),
		queueDepth: DefaultPullQueueDepth,
	}
}

// queueFor returns (creating if needed) the job channel for a node.
func (p *PullDispatcher) queueFor(nodeID string) chan *PendingJob {
	p.mu.Lock()
	defer p.mu.Unlock()
	q, ok := p.queues[nodeID]
	if !ok {
		q = make(chan *PendingJob, p.queueDepth)
		p.queues[nodeID] = q
	}
	return q
}

// Dispatch hands job to nodeID's claim queue and blocks until the node returns
// a result or ctx is done (the router passes a deadline-bounded ctx). Returns
// an error the router treats like any dispatch failure — it will mark the node
// unreachable and try the next candidate.
func (p *PullDispatcher) Dispatch(ctx context.Context, nodeID string, job protocol.JobSpec, messages []map[string]any) (map[string]any, error) {
	pending := &PendingJob{JobID: job.JobID, Job: job, Messages: messages}
	resultCh := make(chan JobResult, 1)

	p.mu.Lock()
	p.waiters[job.JobID] = resultCh
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiters, job.JobID)
		p.mu.Unlock()
	}()

	q := p.queueFor(nodeID)
	// Enqueue without blocking forever: a full queue means the node isn't
	// draining, so fail fast and let the router pick someone else.
	select {
	case q <- pending:
	case <-ctx.Done():
		return nil, fmt.Errorf("pull dispatch to %s: %w", nodeID, ctx.Err())
	default:
		return nil, fmt.Errorf("pull dispatch to %s: node queue full (%d) — node not draining work", nodeID, p.queueDepth)
	}

	select {
	case res := <-resultCh:
		return res.Result, res.Err
	case <-ctx.Done():
		return nil, fmt.Errorf("pull dispatch to %s: no result before deadline: %w", nodeID, ctx.Err())
	}
}

// Claim blocks until a job is queued for nodeID or ctx is done (the endpoint
// passes a ~25s long-poll deadline). Returns ok=false on timeout so the node
// simply re-polls. Never blocks the coordinator: each claim is one node's own
// outbound request.
func (p *PullDispatcher) Claim(ctx context.Context, nodeID string) (*PendingJob, bool) {
	q := p.queueFor(nodeID)
	select {
	case job := <-q:
		return job, true
	case <-ctx.Done():
		return nil, false
	}
}

// SubmitResult delivers a node's completion to the blocked Dispatch waiter.
// A no-op (returns false) if no waiter exists — e.g. the Dispatch already timed
// out and gave up, or a duplicate/late result arrives. Never blocks: the
// waiter channel is buffered (size 1).
func (p *PullDispatcher) SubmitResult(jobID string, result map[string]any, execErr error) bool {
	p.mu.Lock()
	ch, ok := p.waiters[jobID]
	p.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- JobResult{Result: result, Err: execErr}:
		return true
	default:
		// Already delivered (buffered slot taken) — ignore the duplicate.
		return false
	}
}
