package coordinator

import (
	"math"
	"testing"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// TestRecordObservedThroughput_FirstSampleSetsValue confirms the first
// observation for a node becomes its rolling average outright — no EMA blend
// needed when there's nothing to blend with yet.
func TestRecordObservedThroughput_FirstSampleSetsValue(t *testing.T) {
	r := NewNodeRegistry()
	registerTestNode(t, r, 64, 0.7, 10, false)
	nodeID := onlyNodeID(t, r)

	r.RecordObservedThroughput(nodeID, 50)

	snap := onlySnapshot(t, r)
	if snap.ObservedToksPerSec == nil || *snap.ObservedToksPerSec != 50 {
		t.Fatalf("expected observed tps 50, got %+v", snap.ObservedToksPerSec)
	}
	if snap.ObservedSampleCount != 1 {
		t.Fatalf("expected sample count 1, got %d", snap.ObservedSampleCount)
	}
}

// TestRecordObservedThroughput_EMABlendsSubsequentSamples confirms later
// samples are blended via the documented EMA weight rather than replacing
// the running average outright or being averaged with equal weight.
func TestRecordObservedThroughput_EMABlendsSubsequentSamples(t *testing.T) {
	r := NewNodeRegistry()
	registerTestNode(t, r, 64, 0.7, 10, false)
	nodeID := onlyNodeID(t, r)

	r.RecordObservedThroughput(nodeID, 50)
	r.RecordObservedThroughput(nodeID, 100)

	want := 50*(1-throughputEMAAlpha) + 100*throughputEMAAlpha
	snap := onlySnapshot(t, r)
	if snap.ObservedToksPerSec == nil || math.Abs(*snap.ObservedToksPerSec-want) > 1e-9 {
		t.Fatalf("expected EMA-blended tps %.4f, got %+v", want, snap.ObservedToksPerSec)
	}
	if snap.ObservedSampleCount != 2 {
		t.Fatalf("expected sample count 2, got %d", snap.ObservedSampleCount)
	}
}

// TestRecordObservedThroughput_UnknownNodeNoOp confirms recording against a
// node ID that was never registered is silently ignored rather than panicking
// or fabricating an entry.
func TestRecordObservedThroughput_UnknownNodeNoOp(t *testing.T) {
	r := NewNodeRegistry()
	r.RecordObservedThroughput("does-not-exist", 50) // must not panic
}

// TestRecordObservedThroughput_NonPositiveNoOp confirms a zero or negative
// sample (which should never happen given the caller's own guards, but
// defends the invariant directly at the source of truth) never overwrites an
// existing observed value.
func TestRecordObservedThroughput_NonPositiveNoOp(t *testing.T) {
	r := NewNodeRegistry()
	registerTestNode(t, r, 64, 0.7, 10, false)
	nodeID := onlyNodeID(t, r)

	r.RecordObservedThroughput(nodeID, 50)
	r.RecordObservedThroughput(nodeID, 0)
	r.RecordObservedThroughput(nodeID, -5)

	snap := onlySnapshot(t, r)
	if snap.ObservedToksPerSec == nil || *snap.ObservedToksPerSec != 50 {
		t.Fatalf("expected observed tps to remain 50, got %+v", snap.ObservedToksPerSec)
	}
	if snap.ObservedSampleCount != 1 {
		t.Fatalf("expected sample count to remain 1, got %d", snap.ObservedSampleCount)
	}
}

// TestSnapshot_PrefersObservedOverClaimed confirms MeasuredToksPerSec (the
// field the dashboard's "Reduced perf" status reads) reflects the
// coordinator's own observation once one exists, not the node's stale
// self-declared/benchmarked claim — this is the change that actually clears
// a stale degraded badge for a node that has since served real traffic.
func TestSnapshot_PrefersObservedOverClaimed(t *testing.T) {
	r := NewNodeRegistry()
	registerTestNode(t, r, 64, 0.7, 10, false) // claimed: 10 tok/s (stale benchmark)
	nodeID := onlyNodeID(t, r)

	r.RecordObservedThroughput(nodeID, 61) // real traffic measured 61 tok/s

	snap := onlySnapshot(t, r)
	if snap.MeasuredToksPerSec != 61 {
		t.Fatalf("expected MeasuredToksPerSec to reflect observed 61, got %.1f", snap.MeasuredToksPerSec)
	}
}

// TestHealthDigest_PrefersObservedOverClaimed mirrors the Snapshot test above
// for the directory-facing aggregate digest.
func TestHealthDigest_PrefersObservedOverClaimed(t *testing.T) {
	r := NewNodeRegistry()
	registerTestNode(t, r, 64, 0.7, 10, false)
	nodeID := onlyNodeID(t, r)

	r.RecordObservedThroughput(nodeID, 61)

	digest := r.HealthDigest("pod-test", "us", "")
	if digest.AggregateToksPerSec != 61 {
		t.Fatalf("expected aggregate tok/s to reflect observed 61, got %.1f", digest.AggregateToksPerSec)
	}
}

// TestVerifiedCapacityScore_UnaffectedByObservedThroughput is the guarantee
// this whole feature depends on: the fraud-verification/grant-decay path
// must keep comparing the CLAIMED manifest signature against a SUBMITTED
// benchmark, and must never be swayed by the coordinator-observed EMA — that
// EMA is not something a node ever submits, so letting it leak into the
// verified-capacity calculation would let a node's claim escape mismatch
// detection just by serving requests.
func TestVerifiedCapacityScore_UnaffectedByObservedThroughput(t *testing.T) {
	r := NewNodeRegistry()
	registerTestNode(t, r, 64, 0.7, 10, false) // claims 10 tok/s
	nodeID := onlyNodeID(t, r)

	measurements := NewMeasurementStore()
	measurements.Store(nodeID, &protocol.MeasuredSignature{TokensPerSecDecode: 10, TokensPerSecPrefill: 0})

	before := r.VerifiedCapacityScore(measurements, 0.20)

	// A large observed throughput swing must not change the verified score —
	// it isn't derived from claimed-vs-submitted-benchmark at all.
	r.RecordObservedThroughput(nodeID, 500)

	after := r.VerifiedCapacityScore(measurements, 0.20)
	if before != after {
		t.Fatalf("VerifiedCapacityScore changed after RecordObservedThroughput: before=%.2f after=%.2f", before, after)
	}
}

func onlyNodeID(t *testing.T, r *NodeRegistry) string {
	t.Helper()
	snap := onlySnapshot(t, r)
	return snap.NodeID
}

func onlySnapshot(t *testing.T, r *NodeRegistry) NodeSnapshot {
	t.Helper()
	snaps := r.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("expected exactly 1 node in registry, got %d", len(snaps))
	}
	return snaps[0]
}
