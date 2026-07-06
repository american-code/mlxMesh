package coordinator

import (
	"testing"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

func registerTestNode(t *testing.T, r *NodeRegistry, memGB, capPct, tps float64, simulated bool) {
	t.Helper()
	priv, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	manifest := protocol.CapabilityManifest{
		NodeID:               protocol.NodeIDFromPubKey(pub),
		DeclaredMemoryGB:     memGB,
		DeclaredMemoryCapPct: capPct,
		MeasuredSignature:    &protocol.MeasuredSignature{TokensPerSecDecode: tps},
		Simulated:            simulated,
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
}

// TestHealthDigest_RealVsSimulatedSplit verifies the "parity" ingredient
// added for task #49 (progressive decentralization): RealTotalMemoryGB and
// RealAggregateToksPerSec must count ONLY non-simulated nodes, while the
// existing Total* fields keep counting everyone — a forked/stale copy of the
// old aggregation logic that ignored Simulated would silently make every
// deployment look like it's already "at parity."
func TestHealthDigest_RealVsSimulatedSplit(t *testing.T) {
	r := NewNodeRegistry()
	registerTestNode(t, r, 100, 0.5, 50, true)  // simulated: 50 GB committed, 50 tok/s
	registerTestNode(t, r, 100, 0.5, 30, true)  // simulated: 50 GB committed, 30 tok/s
	registerTestNode(t, r, 200, 0.5, 80, false) // real: 100 GB committed, 80 tok/s

	digest := r.HealthDigest("pod-test", "us", "")

	if digest.NodeCountApprox != 3 {
		t.Fatalf("expected 3 total nodes, got %d", digest.NodeCountApprox)
	}
	if digest.RealNodeCountApprox != 1 {
		t.Fatalf("expected 1 real node, got %d", digest.RealNodeCountApprox)
	}
	const wantTotalMem, wantRealMem = 200.0, 100.0
	if digest.TotalMemoryGB != wantTotalMem {
		t.Fatalf("expected total memory %.1f, got %.1f", wantTotalMem, digest.TotalMemoryGB)
	}
	if digest.RealTotalMemoryGB != wantRealMem {
		t.Fatalf("expected real memory %.1f, got %.1f", wantRealMem, digest.RealTotalMemoryGB)
	}
	const wantTotalTPS, wantRealTPS = 160.0, 80.0
	if digest.AggregateToksPerSec != wantTotalTPS {
		t.Fatalf("expected total tok/s %.1f, got %.1f", wantTotalTPS, digest.AggregateToksPerSec)
	}
	if digest.RealAggregateToksPerSec != wantRealTPS {
		t.Fatalf("expected real tok/s %.1f, got %.1f", wantRealTPS, digest.RealAggregateToksPerSec)
	}
}

// TestHealthDigest_NoRealNodesYet documents the honest zero-value case: a
// seed-only deployment (today's actual state) reports zero real capacity
// rather than a misleadingly-omitted field that could read as "not measured."
func TestHealthDigest_NoRealNodesYet(t *testing.T) {
	r := NewNodeRegistry()
	registerTestNode(t, r, 64, 0.7, 40, true)

	digest := r.HealthDigest("pod-test", "us", "")
	if digest.RealNodeCountApprox != 0 || digest.RealTotalMemoryGB != 0 || digest.RealAggregateToksPerSec != 0 {
		t.Fatalf("expected all-zero real capacity for an all-simulated pod, got %+v", digest)
	}
	if digest.NodeCountApprox != 1 || digest.TotalMemoryGB == 0 {
		t.Fatalf("expected non-zero totals for the simulated node, got %+v", digest)
	}
}
