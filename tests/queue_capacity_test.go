package tests

import (
	"testing"

	"github.com/open-inference-mesh/oim/internal/coordinator"
)

func snap(status string, tps float64) coordinator.NodeSnapshot {
	return coordinator.NodeSnapshot{Status: status, MeasuredToksPerSec: tps}
}

// TestComputeQueueCapacityScalesWithThroughput mirrors the Bitcoin-difficulty
// analogy this feature is modeled on: capacity should grow with the network's
// realized "hash rate" (aggregate measured tok/s among LIVE nodes), and never
// count stale/unreachable nodes toward it.
func TestComputeQueueCapacityScalesWithThroughput(t *testing.T) {
	small := coordinator.ComputeQueueCapacity([]coordinator.NodeSnapshot{
		snap("live", 20), snap("live", 20),
	})
	large := coordinator.ComputeQueueCapacity([]coordinator.NodeSnapshot{
		snap("live", 200), snap("live", 200), snap("live", 200), snap("live", 200), snap("live", 200),
	})
	if large <= small {
		t.Errorf("expected a bigger/faster live pool to produce a larger capacity: small=%d large=%d", small, large)
	}
}

// TestComputeQueueCapacityIgnoresStaleNodes confirms only live nodes count —
// a node the coordinator hasn't heard from recently can't inflate capacity.
func TestComputeQueueCapacityIgnoresStaleNodes(t *testing.T) {
	withStale := coordinator.ComputeQueueCapacity([]coordinator.NodeSnapshot{
		snap("live", 50), snap("stale", 500), snap("unreachable", 500),
	})
	liveOnly := coordinator.ComputeQueueCapacity([]coordinator.NodeSnapshot{
		snap("live", 50),
	})
	if withStale != liveOnly {
		t.Errorf("stale/unreachable nodes should not affect capacity: withStale=%d liveOnly=%d", withStale, liveOnly)
	}
}

// TestComputeQueueCapacityFloorsOnUnbenchmarkedNodes confirms freshly
// registered nodes (MeasuredToksPerSec == 0, no benchmark run yet) still
// contribute a floor to capacity — "pool count" matters immediately, not only
// once every node has a benchmark result.
func TestComputeQueueCapacityFloorsOnUnbenchmarkedNodes(t *testing.T) {
	cap := coordinator.ComputeQueueCapacity([]coordinator.NodeSnapshot{
		snap("live", 0), snap("live", 0), snap("live", 0), snap("live", 0),
	})
	if cap < coordinator.MinQueueCapacity {
		t.Errorf("expected at least MinQueueCapacity from a floor-only computation, got %d", cap)
	}
	zero := coordinator.ComputeQueueCapacity(nil)
	if zero != coordinator.MinQueueCapacity {
		t.Errorf("expected MinQueueCapacity for an empty network, got %d", zero)
	}
}

// TestComputeQueueCapacityRespectsMax confirms an enormous network still
// clamps to MaxQueueCapacity rather than growing the underlying buffer
// unbounded.
func TestComputeQueueCapacityRespectsMax(t *testing.T) {
	nodes := make([]coordinator.NodeSnapshot, 0, 1000)
	for i := 0; i < 1000; i++ {
		nodes = append(nodes, snap("live", 1000))
	}
	cap := coordinator.ComputeQueueCapacity(nodes)
	if cap != coordinator.MaxQueueCapacity {
		t.Errorf("expected clamp to MaxQueueCapacity=%d, got %d", coordinator.MaxQueueCapacity, cap)
	}
}
