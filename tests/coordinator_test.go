package tests

import (
	"context"
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/coordinator"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// makeTestNode generates a signed registration for a test node with the given model.
func makeTestNode(t *testing.T, modelID, quantization string, tps float64, hasEnclave bool) protocol.NodeRegistration {
	t.Helper()
	priv, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	nodeID := protocol.NodeIDFromPubKey(pub)
	manifest := protocol.CapabilityManifest{
		NodeID:               nodeID,
		DeclaredMemoryGB:     16.0,
		DeclaredMemoryCapPct: 0.5,
		ReachabilityEndpoint: "http://localhost:9999",
		HasSecureEnclave:     hasEnclave,
		Models: []protocol.ModelCapability{
			{ModelID: modelID, Quantization: quantization, Runtime: protocol.RuntimeExoMLX, MaxContextTokens: 4096},
		},
		PricePerUnit: map[string]float64{"compute_cycles": 0.001},
	}
	if tps > 0 {
		manifest.MeasuredSignature = &protocol.MeasuredSignature{
			TokensPerSecDecode: tps,
			SampleCount:        3,
			BenchmarkPromptID:  "medium",
			MeasuredAt:         "2026-01-01T00:00:00Z",
		}
	}
	payload, err := manifest.Bytes()
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	sig, err := protocol.SignPayload(priv, payload)
	if err != nil {
		t.Fatalf("sign manifest: %v", err)
	}
	return protocol.NodeRegistration{Manifest: manifest, PublicKey: pub, Signature: sig}
}

func TestRegistryRegisterAndCandidates(t *testing.T) {
	r := coordinator.NewNodeRegistry()

	reg := makeTestNode(t, "llama-3.2-3b", "4bit", 45.0, false)
	ok, err := r.Register(reg)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !ok {
		t.Fatal("Register returned false; signature should have verified")
	}

	candidates, err := r.Candidates("llama-3.2-3b", "4bit")
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	if candidates[0].NodeID != reg.Manifest.NodeID {
		t.Errorf("candidate node_id mismatch")
	}
}

func TestRegistryRejectsWrongSignature(t *testing.T) {
	r := coordinator.NewNodeRegistry()

	reg := makeTestNode(t, "llama-3.2-3b", "4bit", 45.0, false)
	reg.Signature[0] ^= 0xFF // corrupt the signature

	ok, err := r.Register(reg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("Register should return false for corrupted signature")
	}
}

func TestRegistryRejectsMismatchedNodeID(t *testing.T) {
	r := coordinator.NewNodeRegistry()

	reg := makeTestNode(t, "llama-3.2-3b", "4bit", 45.0, false)
	reg.Manifest.NodeID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // wrong node_id

	_, err := r.Register(reg)
	if err == nil {
		t.Fatal("Register should return an error when node_id doesn't match public key")
	}
}

func TestRegistryLivenessTTL(t *testing.T) {
	// This test verifies the liveness contract without waiting 90s.
	// We use IsLive directly after MarkUnreachable to test that path.
	r := coordinator.NewNodeRegistry()

	reg := makeTestNode(t, "llama-3.2-3b", "4bit", 45.0, false)
	ok, err := r.Register(reg)
	if err != nil || !ok {
		t.Fatalf("Register failed: ok=%v err=%v", ok, err)
	}

	nodeID := reg.Manifest.NodeID
	if !r.IsLive(nodeID) {
		t.Fatal("node should be live immediately after registration")
	}

	r.MarkUnreachable(nodeID)
	if r.IsLive(nodeID) {
		t.Fatal("node should not be live after MarkUnreachable")
	}

	// Refresh clears the unreachable flag.
	if err := r.Refresh(nodeID, reg.Manifest); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !r.IsLive(nodeID) {
		t.Fatal("node should be live again after Refresh")
	}
}

func TestFastLaneScoring(t *testing.T) {
	job := protocol.JobSpec{
		JobID:       "test-job",
		ModelID:     "llama-3.2-3b",
		Lane:        protocol.JobLaneFast,
		Sensitivity: protocol.SensitivityModerate,
	}

	// Measured node should score higher than unmeasured.
	measured := protocol.CapabilityManifest{
		NodeID:  "aaa",
		Models:  []protocol.ModelCapability{{ModelID: "llama-3.2-3b", Quantization: ""}},
		MeasuredSignature: &protocol.MeasuredSignature{TokensPerSecDecode: 80.0},
		HasSecureEnclave: false,
	}
	unmeasured := protocol.CapabilityManifest{
		NodeID:  "bbb",
		Models:  []protocol.ModelCapability{{ModelID: "llama-3.2-3b", Quantization: ""}},
		HasSecureEnclave: false,
	}

	sm := coordinator.ScoreForFastLane(measured, job)
	su := coordinator.ScoreForFastLane(unmeasured, job)
	if sm <= su {
		t.Errorf("measured node (%.1f) should score higher than unmeasured (%.1f)", sm, su)
	}
}

func TestFastLaneScoringEnclaveGate(t *testing.T) {
	sensitiveJob := protocol.JobSpec{
		JobID:       "test-job",
		ModelID:     "llama-3.2-3b",
		Lane:        protocol.JobLaneFast,
		Sensitivity: protocol.SensitivityHighRequiresAttestation,
	}

	withEnclave := protocol.CapabilityManifest{
		NodeID:           "aaa",
		Models:           []protocol.ModelCapability{{ModelID: "llama-3.2-3b"}},
		HasSecureEnclave: true,
		MeasuredSignature: &protocol.MeasuredSignature{TokensPerSecDecode: 50.0},
	}
	withoutEnclave := protocol.CapabilityManifest{
		NodeID:           "bbb",
		Models:           []protocol.ModelCapability{{ModelID: "llama-3.2-3b"}},
		HasSecureEnclave: false,
		MeasuredSignature: &protocol.MeasuredSignature{TokensPerSecDecode: 200.0},
	}

	scoreWith := coordinator.ScoreForFastLane(withEnclave, sensitiveJob)
	scoreWithout := coordinator.ScoreForFastLane(withoutEnclave, sensitiveJob)

	if scoreWith <= 0 {
		t.Errorf("enclave-capable node should be eligible for HIGH_REQUIRES_ATTESTATION, got score %.1f", scoreWith)
	}
	if !isNegInf(scoreWithout) {
		t.Errorf("non-enclave node should score -Inf for HIGH_REQUIRES_ATTESTATION job, got %.1f", scoreWithout)
	}
}

func TestBackgroundAssignment(t *testing.T) {
	r := coordinator.NewNodeRegistry()

	// Register 3 nodes to satisfy primary + 2 backups.
	for i, tps := range []float64{80.0, 50.0, 30.0} {
		reg := makeTestNode(t, "llama-3.2-3b", "4bit", tps, false)
		ok, err := r.Register(reg)
		if err != nil || !ok {
			t.Fatalf("node %d register: ok=%v err=%v", i, ok, err)
		}
	}

	job := protocol.JobSpec{
		JobID:           "bg-job-1",
		ModelID:         "llama-3.2-3b",
		QuantizationRequired: "4bit",
		Lane:            protocol.JobLaneBackground,
		Sensitivity:     protocol.SensitivityModerate,
		RedundancyDepth: 2,
		Recurrence:      &protocol.RecurrenceSpec{IntervalSeconds: 300, MaxJitterSeconds: 30},
	}

	a, err := coordinator.AssignBackgroundJob(job, r)
	if err != nil {
		t.Fatalf("AssignBackgroundJob: %v", err)
	}
	if a.Primary == "" {
		t.Error("assignment must have a primary node")
	}
	if len(a.Backups) != 2 {
		t.Errorf("got %d backups, want 2", len(a.Backups))
	}
	if a.Primary == a.Backups[0] || a.Primary == a.Backups[1] {
		t.Error("primary and backups must be distinct nodes")
	}
}

func TestResolveForCycleFailover(t *testing.T) {
	r := coordinator.NewNodeRegistry()

	reg1 := makeTestNode(t, "llama-3.2-3b", "4bit", 80.0, false)
	reg2 := makeTestNode(t, "llama-3.2-3b", "4bit", 50.0, false)
	for _, reg := range []protocol.NodeRegistration{reg1, reg2} {
		ok, err := r.Register(reg)
		if err != nil || !ok {
			t.Fatalf("register: ok=%v err=%v", ok, err)
		}
	}

	assignment := &coordinator.BackgroundAssignment{
		JobID:   "bg-job-2",
		Primary: reg1.Manifest.NodeID,
		Backups: []string{reg2.Manifest.NodeID},
	}

	// Primary up → returns primary, is_continuation=true.
	nodeID, isCont, err := coordinator.ResolveForCycle(assignment, r)
	if err != nil {
		t.Fatalf("ResolveForCycle (primary up): %v", err)
	}
	if nodeID != reg1.Manifest.NodeID {
		t.Errorf("want primary %s, got %s", reg1.Manifest.NodeID, nodeID)
	}
	if !isCont {
		t.Error("isContinuation should be true when primary is live")
	}

	// Primary down → promotes backup, is_continuation=false.
	r.MarkUnreachable(reg1.Manifest.NodeID)
	nodeID, isCont, err = coordinator.ResolveForCycle(assignment, r)
	if err != nil {
		t.Fatalf("ResolveForCycle (primary down): %v", err)
	}
	if nodeID != reg2.Manifest.NodeID {
		t.Errorf("want backup %s, got %s", reg2.Manifest.NodeID, nodeID)
	}
	if isCont {
		t.Error("isContinuation must be false when a backup is promoted (cold start on that node)")
	}

	// All down → error.
	r.MarkUnreachable(reg2.Manifest.NodeID)
	_, _, err = coordinator.ResolveForCycle(assignment, r)
	if err == nil {
		t.Fatal("ResolveForCycle should return error when all nodes are down")
	}
}

func TestHealthDigest(t *testing.T) {
	r := coordinator.NewNodeRegistry()

	for _, tps := range []float64{100.0, 80.0} {
		reg := makeTestNode(t, "llama-3.2-3b", "4bit", tps, false)
		ok, err := r.Register(reg)
		if err != nil || !ok {
			t.Fatalf("register: %v", err)
		}
	}

	digest := r.HealthDigest("pod-us-1", "us")
	if digest.PodID != "pod-us-1" {
		t.Errorf("pod_id mismatch")
	}
	if digest.NodeCountApprox != 2 {
		t.Errorf("want 2 nodes, got %d", digest.NodeCountApprox)
	}
	if digest.AggregateHealthScore <= 0 || digest.AggregateHealthScore > 1.0 {
		t.Errorf("health score %.2f out of range [0,1]", digest.AggregateHealthScore)
	}
	if len(digest.ServableModelIDs) == 0 {
		t.Error("ServableModelIDs should not be empty")
	}
}

func TestAssignmentStore(t *testing.T) {
	store := coordinator.NewAssignmentStore()

	a := &coordinator.BackgroundAssignment{
		JobID:   "job-abc",
		Primary: "node-1",
		Backups: []string{"node-2"},
	}
	store.Save(a)

	got, ok := store.Get("job-abc")
	if !ok {
		t.Fatal("Get should find saved assignment")
	}
	if got.Primary != "node-1" {
		t.Errorf("got primary %s, want node-1", got.Primary)
	}

	_, ok = store.Get("nonexistent")
	if ok {
		t.Fatal("Get should return false for unknown job_id")
	}
}

// isNegInf returns true if f is negative infinity.
func isNegInf(f float64) bool {
	return f < -1e300
}

// unused import guard
var _ = time.Second
var _ = context.Background
