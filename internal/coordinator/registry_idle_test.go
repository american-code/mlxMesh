package coordinator

import (
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

var idleTestModel = []protocol.ModelCapability{{ModelID: "test-model", Quantization: "4bit"}}

// registerIdleTestNode registers a node with the given models/simulated flag
// and returns its node ID. Unlike registerTestNode (registry_health_digest_test.go),
// this lets callers control Models directly, since IdleCandidates requires at
// least one advertised model.
func registerIdleTestNode(t *testing.T, r *NodeRegistry, models []protocol.ModelCapability, simulated bool) string {
	t.Helper()
	priv, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	nodeID := protocol.NodeIDFromPubKey(pub)
	manifest := protocol.CapabilityManifest{
		NodeID:    nodeID,
		Models:    models,
		Simulated: simulated,
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

// setRegisteredAt backdates a test node's registeredAt directly — same-package
// test access to the unexported field, used to simulate "registered a while
// ago" without waiting in real time.
func setRegisteredAt(r *NodeRegistry, nodeID string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[nodeID]; ok {
		e.registeredAt = at
	}
}

func setLastJobServedAt(r *NodeRegistry, nodeID string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[nodeID]; ok {
		e.lastJobServedAt = at
	}
}

func TestMarkJobServed_UpdatesTimestamp(t *testing.T) {
	r := NewNodeRegistry()
	nodeID := registerIdleTestNode(t, r, idleTestModel, false)

	before := time.Now()
	r.MarkJobServed(nodeID)

	r.mu.RLock()
	got := r.entries[nodeID].lastJobServedAt
	r.mu.RUnlock()
	if got.Before(before) {
		t.Fatalf("expected lastJobServedAt updated to ~now, got %v (test call started at %v)", got, before)
	}
}

func TestMarkJobServed_UnknownNodeIsNoop(t *testing.T) {
	r := NewNodeRegistry()
	r.MarkJobServed("nonexistent-node") // must not panic
}

func TestIdleCandidates_ExcludesSimulatedNodes(t *testing.T) {
	r := NewNodeRegistry()
	simID := registerIdleTestNode(t, r, idleTestModel, true)
	realID := registerIdleTestNode(t, r, idleTestModel, false)
	setRegisteredAt(r, simID, time.Now().Add(-time.Hour))
	setRegisteredAt(r, realID, time.Now().Add(-time.Hour))

	got := r.IdleCandidates(30 * time.Minute)
	if len(got) != 1 || got[0].NodeID != realID {
		t.Fatalf("expected only the real (non-simulated) node, got %+v", got)
	}
}

func TestIdleCandidates_ExcludesZeroModelNodes(t *testing.T) {
	r := NewNodeRegistry()
	nodeID := registerIdleTestNode(t, r, nil, false)
	setRegisteredAt(r, nodeID, time.Now().Add(-time.Hour))

	got := r.IdleCandidates(30 * time.Minute)
	if len(got) != 0 {
		t.Fatalf("expected a node advertising zero models to be excluded, got %+v", got)
	}
}

func TestIdleCandidates_RespectsCutoff(t *testing.T) {
	r := NewNodeRegistry()
	nodeID := registerIdleTestNode(t, r, idleTestModel, false)
	setLastJobServedAt(r, nodeID, time.Now().Add(-time.Minute)) // recently served

	got := r.IdleCandidates(30 * time.Minute)
	if len(got) != 0 {
		t.Fatalf("expected a recently-served node to be excluded, got %+v", got)
	}
}

// TestIdleCandidates_NeverServedFallsBackToRegisteredAt is the regression test
// for the bug this design almost had: falling back to lastSeen (which resets
// every heartbeat and is required fresh by isLive()) would make a
// never-served node look perpetually "just active" and never clear the idle
// cutoff. registeredAt, which Refresh() never touches, is the correct
// baseline.
func TestIdleCandidates_NeverServedFallsBackToRegisteredAt(t *testing.T) {
	r := NewNodeRegistry()
	longRegistered := registerIdleTestNode(t, r, idleTestModel, false)
	setRegisteredAt(r, longRegistered, time.Now().Add(-time.Hour))
	// Simulate heartbeats continuing to refresh lastSeen while never serving
	// a real job — lastJobServedAt stays zero throughout.
	if err := r.Refresh(longRegistered, r.mustManifest(t, longRegistered)); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	got := r.IdleCandidates(30 * time.Minute)
	if len(got) != 1 || got[0].NodeID != longRegistered {
		t.Fatalf("expected the long-registered, never-served node to be idle-eligible despite fresh heartbeats, got %+v", got)
	}

	freshlyRegistered := registerIdleTestNode(t, r, idleTestModel, false)
	got = r.IdleCandidates(30 * time.Minute)
	for _, m := range got {
		if m.NodeID == freshlyRegistered {
			t.Fatalf("a just-registered node must not appear idle-eligible yet, got %+v", got)
		}
	}
}

func TestIdleCandidates_SortedOldestFirst(t *testing.T) {
	r := NewNodeRegistry()
	older := registerIdleTestNode(t, r, idleTestModel, false)
	newer := registerIdleTestNode(t, r, idleTestModel, false)
	setLastJobServedAt(r, older, time.Now().Add(-2*time.Hour))
	setLastJobServedAt(r, newer, time.Now().Add(-time.Hour))

	got := r.IdleCandidates(30 * time.Minute)
	if len(got) != 2 || got[0].NodeID != older || got[1].NodeID != newer {
		t.Fatalf("expected oldest-idle-first ordering [%s, %s], got %+v", older, newer, got)
	}
}

// mustManifest is a small test helper to fetch a registered node's manifest
// for re-use in a Refresh() call.
func (r *NodeRegistry) mustManifest(t *testing.T, nodeID string) protocol.CapabilityManifest {
	t.Helper()
	m, ok := r.Manifest(nodeID)
	if !ok {
		t.Fatalf("manifest not found for %s", nodeID)
	}
	return m
}
