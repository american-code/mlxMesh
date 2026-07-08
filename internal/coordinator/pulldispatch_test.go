package coordinator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

func testJob(id string) protocol.JobSpec {
	return protocol.JobSpec{JobID: id, ModelID: "m", Lane: protocol.JobLaneFast}
}

// TestPullDispatch_RoundTrip is the core happy path: Dispatch blocks, a
// concurrent Claim receives the job, SubmitResult unblocks Dispatch with the
// node's output.
func TestPullDispatch_RoundTrip(t *testing.T) {
	pd := NewPullDispatcher()
	nodeID := "node-a"

	claimed := make(chan *PendingJob, 1)
	go func() {
		job, ok := pd.Claim(context.Background(), nodeID)
		if ok {
			claimed <- job
		}
	}()

	resultCh := make(chan map[string]any, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := pd.Dispatch(context.Background(), nodeID, testJob("j1"), []map[string]any{{"role": "user"}})
		resultCh <- res
		errCh <- err
	}()

	var got *PendingJob
	select {
	case got = <-claimed:
	case <-time.After(2 * time.Second):
		t.Fatal("claim never received the dispatched job")
	}
	if got.JobID != "j1" {
		t.Fatalf("claimed wrong job: %s", got.JobID)
	}

	if !pd.SubmitResult("j1", map[string]any{"answer": 42}, nil) {
		t.Fatal("SubmitResult should have found the waiter")
	}

	select {
	case res := <-resultCh:
		if res["answer"] != 42 {
			t.Fatalf("Dispatch got wrong result: %v", res)
		}
		if err := <-errCh; err != nil {
			t.Fatalf("Dispatch returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Dispatch never unblocked after SubmitResult")
	}
}

func TestPullDispatch_ClaimTimeout(t *testing.T) {
	pd := NewPullDispatcher()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, ok := pd.Claim(ctx, "idle-node"); ok {
		t.Fatal("expected Claim to time out with no work queued")
	}
}

// TestPullDispatch_DispatchTimeoutWhenNobodyClaims proves a dead/unreachable
// pull node surfaces as a dispatch error (which the router treats like any
// push failure — mark unreachable, try the next candidate).
func TestPullDispatch_DispatchTimeoutWhenNobodyClaims(t *testing.T) {
	pd := NewPullDispatcher()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := pd.Dispatch(ctx, "dead-node", testJob("j2"), nil)
	if err == nil {
		t.Fatal("expected Dispatch to error when no node claims the job")
	}
}

// TestPullDispatch_ResultAfterWaiterGoneIsSafe: a late result (the Dispatch
// already timed out and gave up) must be a harmless no-op, never a panic or
// block.
func TestPullDispatch_ResultAfterWaiterGoneIsSafe(t *testing.T) {
	pd := NewPullDispatcher()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _ = pd.Dispatch(ctx, "node-b", testJob("j3"), nil) // times out, waiter removed
	if pd.SubmitResult("j3", map[string]any{"x": 1}, nil) {
		t.Fatal("SubmitResult for an already-abandoned job should return false")
	}
}

func TestPullDispatch_SubmitResultUnknownJob(t *testing.T) {
	pd := NewPullDispatcher()
	if pd.SubmitResult("never-dispatched", nil, nil) {
		t.Fatal("SubmitResult for an unknown job must return false")
	}
}

// TestPullDispatch_ExecErrorPropagates: a node reporting an execution error is
// surfaced as the Dispatch error.
func TestPullDispatch_ExecErrorPropagates(t *testing.T) {
	pd := NewPullDispatcher()
	nodeID := "node-c"
	go func() {
		_, _ = pd.Claim(context.Background(), nodeID)
	}()
	errCh := make(chan error, 1)
	go func() {
		_, err := pd.Dispatch(context.Background(), nodeID, testJob("j4"), nil)
		errCh <- err
	}()
	// Give Dispatch time to register its waiter and enqueue.
	time.Sleep(100 * time.Millisecond)
	pd.SubmitResult("j4", nil, errors.New("exo blew up"))
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected the node's execution error to propagate to Dispatch")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Dispatch never returned")
	}
}

// TestPullDispatch_ConcurrentNodes: many nodes claiming/dispatching at once
// don't cross wires — the race detector is the real assertion here.
func TestPullDispatch_ConcurrentNodes(t *testing.T) {
	pd := NewPullDispatcher()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		nodeID := "n" + string(rune('A'+i))
		jobID := "job-" + nodeID
		wg.Add(1)
		go func() {
			defer wg.Done()
			go func() {
				if _, ok := pd.Claim(context.Background(), nodeID); ok {
					pd.SubmitResult(jobID, map[string]any{"node": nodeID}, nil)
				}
			}()
			res, err := pd.Dispatch(context.Background(), nodeID, testJob(jobID), nil)
			if err != nil {
				t.Errorf("%s dispatch: %v", nodeID, err)
				return
			}
			if res["node"] != nodeID {
				t.Errorf("%s got a crossed result: %v", nodeID, res)
			}
		}()
	}
	wg.Wait()
}
