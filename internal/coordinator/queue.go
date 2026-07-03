package coordinator

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

const (
	// DefaultQueueCapacity seeds capacity before the first retarget tick runs
	// (e.g. the first ~queueRetargetInterval after startup, before any node
	// has registered) — after that, actual capacity tracks the network's real
	// size via retargetCapacity, same as DefaultQueueWorkers/Timeout are just
	// starting parameters, not the whole story.
	DefaultQueueCapacity = 50
	DefaultQueueWorkers  = 5
	DefaultQueueTimeout  = 30 * time.Second
	queueRetryInterval   = 400 * time.Millisecond

	// Queue capacity retargets periodically from live network size, the same
	// way Bitcoin retargets mining difficulty from realized hash rate every
	// 2016 blocks rather than every block — a slow, averaged cadence so one
	// flaky node flapping online/offline doesn't make queue_capacity (and the
	// backpressure_pct clients see) jitter on every heartbeat.
	QueueRetargetInterval = 30 * time.Second

	MinQueueCapacity = 10
	// MaxQueueCapacity also sizes the underlying channel buffer — a hard
	// ceiling no burst can exceed even mid-retarget-interval.
	MaxQueueCapacity = 500

	// tpsPerQueueSlot: one queue slot per ~2 tok/s of live aggregate measured
	// throughput — this is "network speed," the real analogue of hash rate.
	tpsPerQueueSlot = 2.0
	// nodeFloorPerLiveNode: minimum slots contributed per live node even
	// before it's been benchmarked (MeasuredToksPerSec == 0 right after
	// registration) — this is "pool count," so a freshly-grown network still
	// gets more room immediately, not only once benchmarks land.
	nodeFloorPerLiveNode = 3
)

type queueResult struct {
	data map[string]any
	err  error
}

// QueuedJob is a pending inference job waiting for a node to become available.
type QueuedJob struct {
	ctx      context.Context
	job      protocol.JobSpec
	messages []map[string]any
	result   chan queueResult
	deadline time.Time
}

// JobQueue is a bounded FIFO of pending inference jobs backed by a worker pool.
// When fast-lane dispatch fails (all nodes unreachable or overloaded), callers
// with X-OIM-Queue: true are held here until a node accepts the job or the
// per-request deadline expires. Semantics mirror MQTT QoS-1: the coordinator
// retains the message and delivers it at least once within the deadline.
type JobQueue struct {
	ch       chan *QueuedJob
	capacity atomic.Int64 // soft admission limit; ch's real buffer is always MaxQueueCapacity
}

// NewJobQueue starts a bounded queue and worker goroutines that drain it.
// Workers run until ctx is cancelled (typically at coordinator shutdown).
// initialCapacity seeds admission before the first retarget tick; pass
// DefaultQueueCapacity unless a caller has a specific reason not to.
func NewJobQueue(ctx context.Context, initialCapacity, workers int, registry *NodeRegistry, maxAttempts int) *JobQueue {
	q := &JobQueue{
		ch: make(chan *QueuedJob, MaxQueueCapacity),
	}
	q.capacity.Store(int64(clampInt(initialCapacity, MinQueueCapacity, MaxQueueCapacity)))
	for i := 0; i < workers; i++ {
		go q.worker(ctx, registry, maxAttempts)
	}
	go q.retargetLoop(ctx, registry)
	return q
}

// retargetLoop recomputes capacity from live network size on a slow, fixed
// cadence — see QueueRetargetInterval for why this isn't recomputed on every
// registration/heartbeat.
func (q *JobQueue) retargetLoop(ctx context.Context, registry *NodeRegistry) {
	ticker := time.NewTicker(QueueRetargetInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q.capacity.Store(int64(ComputeQueueCapacity(registry.Snapshot())))
		}
	}
}

// ComputeQueueCapacity derives queue capacity from the live network's
// aggregate measured throughput (network speed) with a per-live-node floor
// (pool count), so capacity grows and shrinks with the network the same way
// Bitcoin's difficulty tracks realized hash rate — more/faster live nodes
// can drain a deeper queue, so a deeper queue is worth holding.
func ComputeQueueCapacity(nodes []NodeSnapshot) int {
	liveCount := 0
	var aggregateTps float64
	for _, n := range nodes {
		if n.Status != "live" {
			continue
		}
		liveCount++
		aggregateTps += n.MeasuredToksPerSec
	}
	fromThroughput := int(aggregateTps / tpsPerQueueSlot)
	floor := liveCount * nodeFloorPerLiveNode
	cap := fromThroughput
	if cap < floor {
		cap = floor
	}
	return clampInt(cap, MinQueueCapacity, MaxQueueCapacity)
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// Enqueue submits a job to the queue and blocks until dispatched, the deadline
// passes, or the caller's context is cancelled.
// Returns a 429-style error immediately if the queue buffer is full.
func (q *JobQueue) Enqueue(ctx context.Context, job protocol.JobSpec, messages []map[string]any, timeout time.Duration) (map[string]any, error) {
	if timeout <= 0 {
		timeout = DefaultQueueTimeout
	}
	// Soft admission check against the current (retargeted) capacity, not the
	// channel's real buffer (always MaxQueueCapacity) — this is what actually
	// makes capacity "dynamic" despite Go channels being fixed-size once made.
	if int64(len(q.ch)) >= q.capacity.Load() {
		return nil, fmt.Errorf("queue_full: %d/%d jobs queued — try again later", len(q.ch), q.capacity.Load())
	}
	qj := &QueuedJob{
		ctx:      ctx,
		job:      job,
		messages: messages,
		result:   make(chan queueResult, 1),
		deadline: time.Now().Add(timeout),
	}
	select {
	case q.ch <- qj:
	default:
		return nil, fmt.Errorf("queue_full: %d/%d jobs queued — try again later", len(q.ch), q.capacity.Load())
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-qj.result:
		return res.data, res.err
	}
}

// Depth returns the current number of jobs waiting in the queue.
func (q *JobQueue) Depth() int { return len(q.ch) }

// Capacity returns the current (retargeted) maximum number of jobs the queue
// will admit — not a fixed constant; see ComputeQueueCapacity.
func (q *JobQueue) Capacity() int { return int(q.capacity.Load()) }

// BackpressurePct returns queue saturation as a 0–100 float.
func (q *JobQueue) BackpressurePct() float64 {
	capacity := q.capacity.Load()
	if capacity == 0 {
		return 0
	}
	return float64(len(q.ch)) / float64(capacity) * 100
}

func (q *JobQueue) worker(ctx context.Context, registry *NodeRegistry, maxAttempts int) {
	for {
		select {
		case <-ctx.Done():
			return
		case qj := <-q.ch:
			q.processJob(ctx, qj, registry, maxAttempts)
		}
	}
}

func (q *JobQueue) processJob(ctx context.Context, qj *QueuedJob, registry *NodeRegistry, maxAttempts int) {
	for {
		if time.Now().After(qj.deadline) {
			qj.result <- queueResult{err: fmt.Errorf("job %s: queue deadline exceeded", qj.job.JobID)}
			return
		}
		if qj.ctx.Err() != nil {
			qj.result <- queueResult{err: qj.ctx.Err()}
			return
		}

		dispatchCtx, cancel := context.WithDeadline(qj.ctx, qj.deadline)
		result, err := DispatchFastLane(dispatchCtx, qj.job, qj.messages, registry, maxAttempts)
		cancel()

		if err == nil {
			qj.result <- queueResult{data: result}
			return
		}

		// No node accepted — back off and retry; nodes recover via heartbeat refresh.
		select {
		case <-ctx.Done():
			qj.result <- queueResult{err: fmt.Errorf("coordinator shutting down")}
			return
		case <-qj.ctx.Done():
			qj.result <- queueResult{err: qj.ctx.Err()}
			return
		case <-time.After(queueRetryInterval):
		}
	}
}
