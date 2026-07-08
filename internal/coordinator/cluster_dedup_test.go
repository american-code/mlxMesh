package coordinator

import (
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// registerClusterTestNode registers a node whose manifest carries the given
// ClusterSignature (empty = not a cluster member) and returns its node ID.
// Every device in a real Exo ring registers exactly like this: an independent
// identity/keypair whose manifest claims the ring's FULL pooled capacity and
// carries the ring's shared signature.
func registerClusterTestNode(t *testing.T, r *NodeRegistry, clusterSig string, memGB, tps float64) string {
	t.Helper()
	priv, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	nodeID := protocol.NodeIDFromPubKey(pub)
	manifest := protocol.CapabilityManifest{
		NodeID:               nodeID,
		IsCluster:            clusterSig != "",
		ClusterSignature:     clusterSig,
		DeclaredMemoryGB:     memGB,
		DeclaredMemoryCapPct: 0.5,
		MeasuredSignature:    &protocol.MeasuredSignature{TokensPerSecDecode: tps},
		Models:               []protocol.ModelCapability{{ModelID: "test-model", Quantization: "4bit", Loaded: true}},
	}
	payload, err := manifest.Bytes()
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	sig, err := protocol.SignPayload(priv, payload)
	if err != nil {
		t.Fatalf("sign manifest: %v", err)
	}
	ok, err := r.Register(protocol.NodeRegistration{Manifest: manifest, PublicKey: pub, Signature: sig})
	if err != nil || !ok {
		t.Fatalf("register test node: ok=%v err=%v", ok, err)
	}
	return nodeID
}

// markStale pushes a node's lastSeen far enough back that isLive() fails —
// simulating the primary dropping off without waiting out livenessTTL.
func markStale(r *NodeRegistry, nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[nodeID]; ok {
		e.lastSeen = time.Now().Add(-2 * livenessTTL)
	}
}

// TestClusterDedup_RoutingSeesOneRegistrationPerRing is the core dedup
// guarantee: a 2-device Exo ring registers twice (each device claiming the
// ring's full pooled capacity), and Candidates/CandidatesWithLoad must
// surface exactly one of them — the lexicographically lowest live node ID.
func TestClusterDedup_RoutingSeesOneRegistrationPerRing(t *testing.T) {
	r := NewNodeRegistry()
	a := registerClusterTestNode(t, r, "ring-sig-1", 64, 40)
	b := registerClusterTestNode(t, r, "ring-sig-1", 64, 40)
	primary, standby := a, b
	if b < a {
		primary, standby = b, a
	}

	got, err := r.Candidates("test-model", "")
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(got) != 1 || got[0].NodeID != primary {
		t.Fatalf("expected only primary %s from Candidates, got %+v", primary, got)
	}

	withLoad, err := r.CandidatesWithLoad("test-model", "")
	if err != nil {
		t.Fatalf("CandidatesWithLoad: %v", err)
	}
	if len(withLoad) != 1 || withLoad[0].Manifest.NodeID != primary {
		t.Fatalf("expected only primary %s from CandidatesWithLoad, got %d entries", primary, len(withLoad))
	}
	_ = standby
}

// TestClusterDedup_DistinctRingsDontCollide: two different rings (different
// signatures) and a solo node (no signature) must all stay independently
// routable — dedup only collapses same-signature groups.
func TestClusterDedup_DistinctRingsDontCollide(t *testing.T) {
	r := NewNodeRegistry()
	registerClusterTestNode(t, r, "ring-sig-1", 64, 40)
	registerClusterTestNode(t, r, "ring-sig-2", 32, 20)
	registerClusterTestNode(t, r, "", 16, 10) // solo node, no signature

	got, err := r.Candidates("test-model", "")
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected all 3 distinct nodes routable, got %d: %+v", len(got), got)
	}
}

// TestClusterDedup_IdleProbeSeesOneRegistrationPerRing guards the money path
// that motivated this fix (observed live): the availability-reward prober's
// candidate pool must contain at most ONE registration per physical ring, or
// it independently probes and credits both for one ring's worth of standing
// hardware.
func TestClusterDedup_IdleProbeSeesOneRegistrationPerRing(t *testing.T) {
	r := NewNodeRegistry()
	a := registerClusterTestNode(t, r, "ring-sig-1", 64, 40)
	b := registerClusterTestNode(t, r, "ring-sig-1", 64, 40)
	setRegisteredAt(r, a, time.Now().Add(-time.Hour))
	setRegisteredAt(r, b, time.Now().Add(-time.Hour))
	primary := a
	if b < a {
		primary = b
	}

	got := r.IdleCandidates(30 * time.Minute)
	if len(got) != 1 || got[0].NodeID != primary {
		t.Fatalf("expected exactly one probe-eligible registration (primary %s) for the ring, got %+v", primary, got)
	}
}

// TestClusterDedup_HealthDigestCountsRingOnce: each duplicate registration
// already claims the ring's FULL pooled memory/throughput, so counting both
// reports the same physical capacity twice.
func TestClusterDedup_HealthDigestCountsRingOnce(t *testing.T) {
	r := NewNodeRegistry()
	registerClusterTestNode(t, r, "ring-sig-1", 64, 40)
	registerClusterTestNode(t, r, "ring-sig-1", 64, 40)

	digest := r.HealthDigest("pod-test", "us", "")
	if digest.NodeCountApprox != 1 {
		t.Fatalf("expected the ring to count as 1 node, got %d", digest.NodeCountApprox)
	}
	if digest.TotalMemoryGB != 32 { // 64 GB * 0.5 cap, counted once
		t.Fatalf("expected 32 GB committed (counted once), got %.1f", digest.TotalMemoryGB)
	}
	if digest.AggregateToksPerSec != 40 {
		t.Fatalf("expected 40 tok/s (counted once), got %.1f", digest.AggregateToksPerSec)
	}
}

// TestClusterDedup_SnapshotLabelsStandbyButKeepsBoth: operator transparency —
// both registrations stay visible in Snapshot(), with exactly the non-primary
// flagged ClusterStandby, mirroring how Simulated nodes are labeled rather
// than hidden.
func TestClusterDedup_SnapshotLabelsStandbyButKeepsBoth(t *testing.T) {
	r := NewNodeRegistry()
	a := registerClusterTestNode(t, r, "ring-sig-1", 64, 40)
	b := registerClusterTestNode(t, r, "ring-sig-1", 64, 40)
	primary := a
	if b < a {
		primary = b
	}

	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected both registrations visible in Snapshot, got %d", len(snap))
	}
	for _, n := range snap {
		wantStandby := n.NodeID != primary
		if n.ClusterStandby != wantStandby {
			t.Fatalf("node %s: expected ClusterStandby=%v, got %v", n.NodeID, wantStandby, n.ClusterStandby)
		}
	}
}

// TestClusterDedup_StandbyPromotedInstantlyWhenPrimaryGoesStale proves the
// stateless design's no-promotion-delay property: the standby set is
// recomputed from live entries on every call, so the moment the primary
// stops being live the standby is eligible — no flags to flip, no ticker to
// wait for.
func TestClusterDedup_StandbyPromotedInstantlyWhenPrimaryGoesStale(t *testing.T) {
	r := NewNodeRegistry()
	a := registerClusterTestNode(t, r, "ring-sig-1", 64, 40)
	b := registerClusterTestNode(t, r, "ring-sig-1", 64, 40)
	primary, standby := a, b
	if b < a {
		primary, standby = b, a
	}

	markStale(r, primary)

	got, err := r.Candidates("test-model", "")
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(got) != 1 || got[0].NodeID != standby {
		t.Fatalf("expected former standby %s to be instantly routable after primary went stale, got %+v", standby, got)
	}
}
