package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/coordinator"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// TestDispatchFastLaneTagsMeasuredTokensPerSec guards a real feature: "Try the
// mesh" should show a tok/s figure MEASURED from this request's own wall-clock
// time, not the node's self-declared/benchmarked signature. The stub node
// sleeps briefly and returns a fixed completion_tokens count, so the test can
// assert the coordinator computed tokens/elapsed_seconds itself.
func TestDispatchFastLaneTagsMeasuredTokensPerSec(t *testing.T) {
	const sleepMs = 100
	const completionTokens = 50

	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(sleepMs * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "hi"}}},
			"usage":   map[string]any{"completion_tokens": completionTokens},
		})
	}))
	defer node.Close()

	r := coordinator.NewNodeRegistry()
	reg := makeTestNodeAt(t, "llama-3.2-3b", "4bit", 45.0, node.URL)
	if ok, err := r.Register(reg); err != nil || !ok {
		t.Fatalf("Register: ok=%v err=%v", ok, err)
	}

	job := protocol.JobSpec{JobID: "job-1", ModelID: "llama-3.2-3b", Lane: protocol.JobLaneFast}
	result, err := coordinator.DispatchFastLane(context.Background(), job,
		[]map[string]any{{"role": "user", "content": "hi"}}, r, 3)
	if err != nil {
		t.Fatalf("DispatchFastLane: %v", err)
	}

	tps, ok := result["oim_tokens_per_sec"].(float64)
	if !ok {
		t.Fatalf("expected oim_tokens_per_sec in result, got %v", result)
	}
	// completion_tokens / elapsed(~100ms) should land well above the node's
	// declared 45 tok/s signature — proves it's measured, not copied from the
	// manifest — while staying within a generous bound for CI scheduling jitter.
	if tps < 200 || tps > 5000 {
		t.Errorf("oim_tokens_per_sec = %v, want roughly 500 (50 tokens / ~0.1s)", tps)
	}

	latencyMs, ok := result["oim_latency_ms"].(int64)
	if !ok {
		t.Fatalf("expected oim_latency_ms (int64) in result, got %v (%T)", result["oim_latency_ms"], result["oim_latency_ms"])
	}
	if latencyMs < sleepMs {
		t.Errorf("oim_latency_ms = %d, want >= %d (the node's own sleep)", latencyMs, sleepMs)
	}
}

// TestDispatchFastLaneOmitsTokensPerSecWithoutUsage confirms no fabricated
// figure is shown when the node's response carries no usage field at all.
func TestDispatchFastLaneOmitsTokensPerSecWithoutUsage(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "hi"}}},
		})
	}))
	defer node.Close()

	r := coordinator.NewNodeRegistry()
	reg := makeTestNodeAt(t, "llama-3.2-3b", "4bit", 45.0, node.URL)
	if ok, err := r.Register(reg); err != nil || !ok {
		t.Fatalf("Register: ok=%v err=%v", ok, err)
	}

	job := protocol.JobSpec{JobID: "job-2", ModelID: "llama-3.2-3b", Lane: protocol.JobLaneFast}
	result, err := coordinator.DispatchFastLane(context.Background(), job,
		[]map[string]any{{"role": "user", "content": "hi"}}, r, 3)
	if err != nil {
		t.Fatalf("DispatchFastLane: %v", err)
	}
	if _, present := result["oim_tokens_per_sec"]; present {
		t.Errorf("expected no oim_tokens_per_sec without a usage field, got %v", result["oim_tokens_per_sec"])
	}
}

// makeTestNodeAt is makeTestNode with an explicit reachability endpoint, so
// DispatchFastLane's HTTP call actually lands on a real httptest.Server
// instead of the fixed "http://localhost:9999" placeholder makeTestNode uses.
func makeTestNodeAt(t *testing.T, modelID, quantization string, tps float64, endpoint string) protocol.NodeRegistration {
	t.Helper()
	reg := makeTestNode(t, modelID, quantization, tps, false)
	reg.Manifest.ReachabilityEndpoint = endpoint
	priv, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	reg.Manifest.NodeID = protocol.NodeIDFromPubKey(pub)
	reg.PublicKey = pub
	payload, err := reg.Manifest.Bytes()
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	sig, err := protocol.SignPayload(priv, payload)
	if err != nil {
		t.Fatalf("sign manifest: %v", err)
	}
	reg.Signature = sig
	return reg
}
