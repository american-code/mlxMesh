package tests

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/coordinator"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// TestJobQueueEnqueueRespectsSoftCapacity confirms Enqueue's admission check
// uses the (possibly retargeted) soft capacity, not the channel's real
// MaxQueueCapacity buffer — the actual mechanism that makes capacity dynamic
// despite Go channels being fixed-size once created. workers=0 so nothing
// drains the queue; each Enqueue call gets its own short-deadline context so
// admitted-but-undispatched jobs return promptly instead of hanging.
func TestJobQueueEnqueueRespectsSoftCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := coordinator.NewNodeRegistry()
	// NewJobQueue clamps initialCapacity to at least MinQueueCapacity, so the
	// soft cap under test is MinQueueCapacity itself, not an arbitrary small
	// number — a network is never given less room than the floor.
	const capacity = coordinator.MinQueueCapacity
	q := coordinator.NewJobQueue(ctx, capacity, 0 /* workers */, registry, 1)

	job := protocol.JobSpec{JobID: "j", ModelID: "m", Lane: protocol.JobLaneFast}

	// Fill the soft capacity with jobs that will sit until their own short
	// deadline — nothing drains them since there are 0 workers. Every call
	// gets a bounded context so a bug here fails this test fast instead of
	// hanging for the full 10-minute `go test` timeout.
	for i := 0; i < capacity; i++ {
		go func() {
			shortCtx, shortCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer shortCancel()
			_, _ = q.Enqueue(shortCtx, job, nil, 200*time.Millisecond)
		}()
	}

	// Poll until all jobs have actually landed in the channel before probing
	// the next one — Enqueue's channel send happens synchronously before it
	// blocks on the result, so Depth() converges quickly.
	deadline := time.Now().Add(2 * time.Second)
	for q.Depth() < capacity && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if q.Depth() != capacity {
		t.Fatalf("expected %d queued jobs, got depth=%d", capacity, q.Depth())
	}

	// One more Enqueue should be rejected immediately (queue_full), not
	// accepted into the channel's real MaxQueueCapacity=500 buffer.
	overflowCtx, overflowCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer overflowCancel()
	_, err := q.Enqueue(overflowCtx, job, nil, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "queue_full") {
		t.Fatalf("expected queue_full error at soft capacity, got: %v", err)
	}
}
