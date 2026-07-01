package coordinator

import (
	"context"
	"fmt"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

const (
	DefaultQueueCapacity = 50
	DefaultQueueWorkers  = 5
	DefaultQueueTimeout  = 30 * time.Second
	queueRetryInterval   = 400 * time.Millisecond
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
	capacity int
}

// NewJobQueue starts a bounded queue and worker goroutines that drain it.
// Workers run until ctx is cancelled (typically at coordinator shutdown).
func NewJobQueue(ctx context.Context, capacity, workers int, registry *NodeRegistry, maxAttempts int) *JobQueue {
	q := &JobQueue{
		ch:       make(chan *QueuedJob, capacity),
		capacity: capacity,
	}
	for i := 0; i < workers; i++ {
		go q.worker(ctx, registry, maxAttempts)
	}
	return q
}

// Enqueue submits a job to the queue and blocks until dispatched, the deadline
// passes, or the caller's context is cancelled.
// Returns a 429-style error immediately if the queue buffer is full.
func (q *JobQueue) Enqueue(ctx context.Context, job protocol.JobSpec, messages []map[string]any, timeout time.Duration) (map[string]any, error) {
	if timeout <= 0 {
		timeout = DefaultQueueTimeout
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
		return nil, fmt.Errorf("queue_full: %d/%d jobs queued — try again later", len(q.ch), cap(q.ch))
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

// Capacity returns the maximum number of jobs the queue can hold.
func (q *JobQueue) Capacity() int { return q.capacity }

// BackpressurePct returns queue saturation as a 0–100 float.
func (q *JobQueue) BackpressurePct() float64 {
	if q.capacity == 0 {
		return 0
	}
	return float64(len(q.ch)) / float64(q.capacity) * 100
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
