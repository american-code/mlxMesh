package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/directory"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// --- PodStore tests ---

func TestPodStoreQueryByModel(t *testing.T) {
	store := directory.NewPodStore(5 * time.Minute)

	store.Upsert(protocol.PodHealthDigest{
		PodID:                "pod-us-1",
		RegionHint:           "us",
		ServableModelIDs:     []string{"llama-3.2-3b", "mistral-7b"},
		AggregateHealthScore: 0.9,
		NodeCountApprox:      3,
	})
	store.Upsert(protocol.PodHealthDigest{
		PodID:                "pod-eu-1",
		RegionHint:           "eu",
		ServableModelIDs:     []string{"llama-3.2-3b"},
		AggregateHealthScore: 0.7,
		NodeCountApprox:      2,
	})
	store.Upsert(protocol.PodHealthDigest{
		PodID:                "pod-apac-1",
		RegionHint:           "apac",
		ServableModelIDs:     []string{"mistral-7b"},
		AggregateHealthScore: 0.8,
		NodeCountApprox:      1,
	})

	// llama-3.2-3b: both us and eu pods
	llamaPods := store.QueryForModel("llama-3.2-3b")
	if len(llamaPods) != 2 {
		t.Errorf("llama query: want 2 pods, got %d: %v", len(llamaPods), llamaPods)
	}

	// mistral-7b: us and apac pods
	mistralPods := store.QueryForModel("mistral-7b")
	if len(mistralPods) != 2 {
		t.Errorf("mistral query: want 2 pods, got %d: %v", len(mistralPods), mistralPods)
	}

	// unknown model: no pods
	unknown := store.QueryForModel("gpt-9000")
	if len(unknown) != 0 {
		t.Errorf("unknown model: want 0 pods, got %d", len(unknown))
	}
}

func TestPodStoreAllPodIDs(t *testing.T) {
	store := directory.NewPodStore(5 * time.Minute)
	for _, id := range []string{"pod-a", "pod-b", "pod-c"} {
		store.Upsert(protocol.PodHealthDigest{PodID: id, ServableModelIDs: []string{"llama-3.2-3b"}})
	}
	all := store.AllPodIDs()
	if len(all) != 3 {
		t.Errorf("want 3 pods, got %d", len(all))
	}
}

func TestPodStoreTTLExcludesStale(t *testing.T) {
	// TTL of 1ms — any pod registered should expire essentially immediately.
	store := directory.NewPodStore(1 * time.Millisecond)
	store.Upsert(protocol.PodHealthDigest{
		PodID:            "stale-pod",
		ServableModelIDs: []string{"llama-3.2-3b"},
	})
	time.Sleep(5 * time.Millisecond)

	pods := store.QueryForModel("llama-3.2-3b")
	if len(pods) != 0 {
		t.Errorf("stale pod should be excluded from query results; got %v", pods)
	}
	if store.LiveCount() != 0 {
		t.Errorf("LiveCount should be 0 after TTL expiry, got %d", store.LiveCount())
	}
}

func TestPodStoreUpsertRefreshesTTL(t *testing.T) {
	store := directory.NewPodStore(50 * time.Millisecond)
	digest := protocol.PodHealthDigest{PodID: "refresh-pod", ServableModelIDs: []string{"llama-3.2-3b"}}
	store.Upsert(digest)

	time.Sleep(30 * time.Millisecond) // not yet expired
	store.Upsert(digest)              // refresh TTL clock
	time.Sleep(30 * time.Millisecond) // would've expired from original time, but was refreshed

	if store.LiveCount() != 1 {
		t.Error("pod should still be live after TTL refresh via Upsert")
	}
}

// --- CentralizedResolver tests ---

func TestCentralizedResolverLiveEndpoint(t *testing.T) {
	// Serve a mock directory that returns a fixed pod list.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pods" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(protocol.DirectoryQueryResult{
			MatchingPods: []string{"pod-us-1", "pod-eu-1"},
			QueriedAt:    "2026-06-01T00:00:00Z",
		})
	}))
	defer srv.Close()

	resolver := directory.NewCentralizedResolver([]string{srv.URL})
	pods, err := resolver.ResolvePodsForModel("llama-3.2-3b", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 2 {
		t.Errorf("want 2 pods, got %d: %v", len(pods), pods)
	}
}

func TestCentralizedResolverCacheFallback(t *testing.T) {
	// Start a server, populate the resolver cache, then shut the server down.
	// The resolver should return cached results rather than erroring.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(protocol.DirectoryQueryResult{
			MatchingPods: []string{"pod-cached"},
			QueriedAt:    "2026-06-01T00:00:00Z",
		})
	}))

	resolver := directory.NewCentralizedResolver([]string{srv.URL})

	// Warm the cache with a live query.
	pods, err := resolver.ResolvePodsForModel("llama-3.2-3b", "")
	if err != nil || len(pods) != 1 {
		t.Fatalf("warm cache: want 1 pod, err=%v pods=%v", err, pods)
	}

	// Shut down the server — directory is now unavailable.
	srv.Close()

	// Resolver should fall back to cache without error.
	pods, err = resolver.ResolvePodsForModel("llama-3.2-3b", "")
	if err != nil {
		t.Fatalf("cache fallback: unexpected error: %v", err)
	}
	if len(pods) != 1 || pods[0] != "pod-cached" {
		t.Errorf("cache fallback: want [pod-cached], got %v", pods)
	}
}

func TestCentralizedResolverNoEndpointsNoCache(t *testing.T) {
	// No endpoints, no cache — must return error.
	resolver := directory.NewCentralizedResolver([]string{"http://localhost:19991"})
	_, err := resolver.ResolvePodsForModel("llama-3.2-3b", "")
	if err == nil {
		t.Fatal("should return error when all endpoints unreachable and no cache")
	}
}

func TestCentralizedResolverTriesAllEndpoints(t *testing.T) {
	// First endpoint fails; second succeeds. Should still return results.
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(protocol.DirectoryQueryResult{
			MatchingPods: []string{"pod-from-fallback"},
			QueriedAt:    "2026-06-01T00:00:00Z",
		})
	}))
	defer good.Close()

	resolver := directory.NewCentralizedResolver([]string{"http://localhost:19992", good.URL})
	pods, err := resolver.ResolvePodsForModel("llama-3.2-3b", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 1 || pods[0] != "pod-from-fallback" {
		t.Errorf("want [pod-from-fallback], got %v", pods)
	}
}

func TestCentralizedResolverRegisterPod(t *testing.T) {
	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pods/register" && r.Method == http.MethodPost {
			received.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "registered"})
	}))
	defer srv.Close()

	resolver := directory.NewCentralizedResolver([]string{srv.URL})
	err := resolver.RegisterPod(context.Background(), protocol.PodHealthDigest{
		PodID:                "pod-test",
		ServableModelIDs:     []string{"llama-3.2-3b"},
		AggregateHealthScore: 0.9,
	})
	if err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}
	if received.Load() != 1 {
		t.Errorf("want 1 registration request, got %d", received.Load())
	}
}

// --- Gossip tests ---

func TestGossipPropagates(t *testing.T) {
	// Directory A receives a digest and propagates it to directory B.
	peerReceived := make(chan protocol.PodHealthDigest, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gossip/digest" {
			var d protocol.PodHealthDigest
			json.NewDecoder(r.Body).Decode(&d)
			peerReceived <- d
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "stored"})
	}))
	defer peer.Close()

	storeA := directory.NewPodStore(5 * time.Minute)
	gossipA := directory.NewGossip(storeA, peer.URL)

	digest := protocol.PodHealthDigest{
		PodID:                "pod-propagated",
		ServableModelIDs:     []string{"llama-3.2-3b"},
		AggregateHealthScore: 0.85,
	}
	gossipA.ReceivePodDigest(digest)

	// Wait for async propagation.
	select {
	case received := <-peerReceived:
		if received.PodID != "pod-propagated" {
			t.Errorf("peer got pod_id %q, want %q", received.PodID, "pod-propagated")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for gossip propagation")
	}

	// Also verify storeA has the digest locally.
	pods := storeA.QueryForModel("llama-3.2-3b")
	if len(pods) != 1 || pods[0] != "pod-propagated" {
		t.Errorf("storeA should have the digest locally; got %v", pods)
	}
}

func TestGossipStandaloneNoForward(t *testing.T) {
	// No peer configured — should still store locally without errors.
	store := directory.NewPodStore(5 * time.Minute)
	gossip := directory.NewGossip(store, "") // empty peerURL = standalone

	gossip.ReceivePodDigest(protocol.PodHealthDigest{
		PodID:            "solo-pod",
		ServableModelIDs: []string{"llama-3.2-3b"},
	})
	if store.LiveCount() != 1 {
		t.Error("standalone gossip should store digest locally")
	}
}

// --- End-to-end: directory HTTP server ---

func TestDirectoryHTTPServerEndToEnd(t *testing.T) {
	// Spin up a mock directory using httptest.
	store := directory.NewPodStore(5 * time.Minute)
	gossip := directory.NewGossip(store, "")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /pods/register", func(w http.ResponseWriter, r *http.Request) {
		var d protocol.PodHealthDigest
		json.NewDecoder(r.Body).Decode(&d)
		gossip.ReceivePodDigest(d)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "registered"})
	})
	mux.HandleFunc("GET /pods", func(w http.ResponseWriter, r *http.Request) {
		modelID := r.URL.Query().Get("model_id")
		pods := store.QueryForModel(modelID)
		if pods == nil {
			pods = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(protocol.DirectoryQueryResult{
			MatchingPods: pods,
			QueriedAt:    "2026-06-01T00:00:00Z",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resolver := directory.NewCentralizedResolver([]string{srv.URL})

	// Register two pods with different model sets.
	resolver.RegisterPod(context.Background(), protocol.PodHealthDigest{
		PodID:                "pod-us-1",
		ServableModelIDs:     []string{"llama-3.2-3b", "mistral-7b"},
		AggregateHealthScore: 0.9,
	})
	resolver.RegisterPod(context.Background(), protocol.PodHealthDigest{
		PodID:                "pod-eu-1",
		ServableModelIDs:     []string{"llama-3.2-3b"},
		AggregateHealthScore: 0.7,
	})

	// Query for llama — should return both pods.
	llamaPods, err := resolver.ResolvePodsForModel("llama-3.2-3b", "")
	if err != nil {
		t.Fatalf("resolve llama: %v", err)
	}
	if len(llamaPods) != 2 {
		t.Errorf("llama: want 2 pods, got %d: %v", len(llamaPods), llamaPods)
	}

	// Query for mistral — should return only pod-us-1.
	mistralPods, err := resolver.ResolvePodsForModel("mistral-7b", "")
	if err != nil {
		t.Fatalf("resolve mistral: %v", err)
	}
	if len(mistralPods) != 1 || mistralPods[0] != "pod-us-1" {
		t.Errorf("mistral: want [pod-us-1], got %v", mistralPods)
	}

	// Simulate directory going down — should degrade to cached state.
	srv.Close()
	cached, err := resolver.ResolvePodsForModel("llama-3.2-3b", "")
	if err != nil {
		t.Fatalf("cache fallback after server close: %v", err)
	}
	if len(cached) != 2 {
		t.Errorf("cache fallback: want 2 pods, got %d: %v", len(cached), cached)
	}
}
