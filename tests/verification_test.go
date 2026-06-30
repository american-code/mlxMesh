package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-inference-mesh/oim/internal/coordinator"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// --- VerifyTierClaim tests ---

func TestVerifyTierClaimFraudDetection(t *testing.T) {
	// Node claims 200 tok/s but submits an actual measurement of 10 tok/s (20× gap).
	store := coordinator.NewMeasurementStore()

	claimed := protocol.MeasuredSignature{
		TokensPerSecDecode:  200.0,
		TokensPerSecPrefill: 500.0,
		BenchmarkPromptID:   "medium",
		SampleCount:         3,
		MeasuredAt:          "2026-01-01T00:00:00Z",
	}
	actual := protocol.MeasuredSignature{
		TokensPerSecDecode:  10.0, // 20× slower than claimed — clear fraud
		TokensPerSecPrefill: 25.0,
		BenchmarkPromptID:   "medium",
		SampleCount:         3,
		MeasuredAt:          "2026-06-01T00:00:00Z",
	}
	store.Store("fraud-node", &actual)

	ok, err := coordinator.VerifyTierClaim("fraud-node", claimed, store, 0.20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("VerifyTierClaim should return false for a node misreporting its tier by 20×")
	}
}

func TestVerifyTierClaimHonestNode(t *testing.T) {
	// Node claims 50 tok/s, submits 48 tok/s — within 20% tolerance.
	store := coordinator.NewMeasurementStore()

	claimed := protocol.MeasuredSignature{
		TokensPerSecDecode:  50.0,
		TokensPerSecPrefill: 200.0,
		BenchmarkPromptID:   "medium",
		SampleCount:         3,
		MeasuredAt:          "2026-01-01T00:00:00Z",
	}
	actual := protocol.MeasuredSignature{
		TokensPerSecDecode:  48.0, // 4% below claimed — honest variance
		TokensPerSecPrefill: 195.0,
		BenchmarkPromptID:   "medium",
		SampleCount:         3,
		MeasuredAt:          "2026-06-01T00:00:00Z",
	}
	store.Store("honest-node", &actual)

	ok, err := coordinator.VerifyTierClaim("honest-node", claimed, store, 0.20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("VerifyTierClaim should return true for an honest node within tolerance")
	}
}

func TestVerifyTierClaimNoMeasurement(t *testing.T) {
	// No benchmark submitted yet — ErrNoMeasurement.
	store := coordinator.NewMeasurementStore()
	claimed := protocol.MeasuredSignature{TokensPerSecDecode: 50.0, TokensPerSecPrefill: 200.0}

	ok, err := coordinator.VerifyTierClaim("unknown-node", claimed, store, 0.20)
	if err == nil {
		t.Fatal("should return ErrNoMeasurement when no submitted measurement exists")
	}
	if ok {
		t.Error("should return false when no measurement is available")
	}
}

func TestMeasurementStoreReplaces(t *testing.T) {
	// Store should replace the prior entry when a new measurement arrives.
	store := coordinator.NewMeasurementStore()
	sig1 := &protocol.MeasuredSignature{TokensPerSecDecode: 100.0}
	sig2 := &protocol.MeasuredSignature{TokensPerSecDecode: 50.0}

	store.Store("node-x", sig1)
	store.Store("node-x", sig2)

	got, ok := store.Get("node-x")
	if !ok {
		t.Fatal("Get should find stored measurement")
	}
	if got.TokensPerSecDecode != 50.0 {
		t.Errorf("want 50.0 (latest), got %.1f", got.TokensPerSecDecode)
	}
}

// --- StatisticalBaselineCheck tests ---

func TestStatisticalBaselineCheckEmptyBaseline(t *testing.T) {
	// No baseline — always passes.
	outputs := []map[string]any{
		{"choices": []any{map[string]any{"message": map[string]any{"content": "hello world"}}}},
	}
	ok, err := coordinator.StatisticalBaselineCheck("job-1", outputs, map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("empty baseline should return true (no reference to check against)")
	}
}

func TestStatisticalBaselineCheckWithinBounds(t *testing.T) {
	baseline := map[string]any{
		"mean_length":   100.0,
		"stddev_length": 20.0,
	}
	// Content length ≈ 95 chars — well within 3×stddev of mean.
	content := make([]byte, 95)
	for i := range content {
		content[i] = 'a'
	}
	outputs := []map[string]any{
		{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}}}},
	}
	ok, err := coordinator.StatisticalBaselineCheck("job-2", outputs, baseline)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("output within 3× stddev should pass baseline check")
	}
}

func TestStatisticalBaselineCheckDetectsAnomaly(t *testing.T) {
	// Baseline: mean=100, stddev=10. An output of length 5 is 9.5 stddevs below mean.
	baseline := map[string]any{
		"mean_length":   100.0,
		"stddev_length": 10.0,
	}
	outputs := []map[string]any{
		{"choices": []any{map[string]any{"message": map[string]any{"content": "short"}}}},
	}
	ok, err := coordinator.StatisticalBaselineCheck("job-3", outputs, baseline)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("anomalous output (9.5 stddevs below mean) should fail baseline check")
	}
}

// --- SpotCheckFastLane tests ---

func TestSpotCheckSkipsAtRateZero(t *testing.T) {
	// sampleRate=0 always skips — returns true regardless of verifier.
	ctx := context.Background()
	ok, err := coordinator.SpotCheckFastLane(ctx, "job-skip", nil, "llama-3.2-3b",
		map[string]any{}, "http://localhost:19999", 0.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("sampleRate=0 should always return true (job not sampled)")
	}
}

func TestSpotCheckConsistentOutputs(t *testing.T) {
	// sampleRate=1: dispatch to a mock verifier that returns a similar-length response.
	primaryContent := "The answer is 42, because deep thought computed it."
	verifierContent := "The answer is 42 — deep thought took 7.5 million years to compute it."

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{"message": map[string]any{"content": verifierContent}},
			},
		})
	}))
	defer srv.Close()

	primaryResult := map[string]any{
		"choices": []any{
			map[string]any{"message": map[string]any{"content": primaryContent}},
		},
	}

	ctx := context.Background()
	ok, err := coordinator.SpotCheckFastLane(ctx, "job-check", []map[string]any{
		{"role": "user", "content": "what is the answer?"},
	}, "llama-3.2-3b", primaryResult, srv.URL, 1.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("similar-length responses should be considered consistent")
	}
}

func TestSpotCheckInconsistentOutputs(t *testing.T) {
	// sampleRate=1: verifier returns a wildly different content length (>2× ratio).
	primaryContent := "The answer to life, the universe, and everything is forty-two."
	verifierContent := "x" // single character — way too short relative to primary

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{"message": map[string]any{"content": verifierContent}},
			},
		})
	}))
	defer srv.Close()

	primaryResult := map[string]any{
		"choices": []any{
			map[string]any{"message": map[string]any{"content": primaryContent}},
		},
	}

	ctx := context.Background()
	ok, err := coordinator.SpotCheckFastLane(ctx, "job-diverge", []map[string]any{
		{"role": "user", "content": "what is the answer?"},
	}, "llama-3.2-3b", primaryResult, srv.URL, 1.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("verifier response 60× shorter than primary should fail consistency check")
	}
}

// --- Registry ClaimedSignature ---

func TestClaimedSignatureFromRegistry(t *testing.T) {
	r := coordinator.NewNodeRegistry()

	// Node with a benchmark.
	reg := makeTestNode(t, "llama-3.2-3b", "4bit", 80.0, false)
	ok, err := r.Register(reg)
	if err != nil || !ok {
		t.Fatalf("register: ok=%v err=%v", ok, err)
	}

	sig, err := r.ClaimedSignature(reg.Manifest.NodeID)
	if err != nil {
		t.Fatalf("ClaimedSignature: %v", err)
	}
	if sig == nil {
		t.Fatal("expected non-nil claimed signature")
	}
	if sig.TokensPerSecDecode != 80.0 {
		t.Errorf("want 80.0, got %.1f", sig.TokensPerSecDecode)
	}
}

func TestClaimedSignatureUnknownNode(t *testing.T) {
	r := coordinator.NewNodeRegistry()
	_, err := r.ClaimedSignature("no-such-node")
	if err == nil {
		t.Fatal("ClaimedSignature should return error for unknown node")
	}
}
