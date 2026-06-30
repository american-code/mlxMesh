// Package directory implements the librarian layer — global discovery only.
// This layer must never see job payloads or sit in the inference data path (proposal §7.1).
// Node agents and pod coordinators depend on the Resolver interface, never on a
// specific implementation — this makes staged evolution (centralized→federated→DHT)
// possible without breaking existing clients (proposal §7.3).
package directory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// Resolver is the pluggable directory abstraction (proposal §7.3).
// Callers should depend on this interface, not on a specific implementation.
type Resolver interface {
	ResolvePodsForModel(modelID, quantization string) ([]string, error)
	RegisterPod(ctx context.Context, digest protocol.PodHealthDigest) error
}

// CentralizedResolver is the bootstrap-stage implementation (proposal §7.3 stage 1).
// Queries one or two known operator-run directory endpoints.
// Maintains a last-known-good cache: if all endpoints fail, callers degrade to cached
// state rather than erroring out — this was identified as more valuable than the
// 2-instance redundancy itself (proposal §7.3 discussion).
type CentralizedResolver struct {
	Endpoints  []string
	HTTPClient *http.Client
	mu         sync.RWMutex
	cache      map[string]cacheEntry
}

type cacheEntry struct {
	pods      []string
	writtenAt time.Time
}

func NewCentralizedResolver(endpoints []string) *CentralizedResolver {
	return &CentralizedResolver{
		Endpoints:  endpoints,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		cache:      make(map[string]cacheEntry),
	}
}

// ResolvePodsForModel returns pod IDs that can serve the given model.
// Tries each endpoint in order; falls back to the cached result if all fail.
func (r *CentralizedResolver) ResolvePodsForModel(modelID, quantization string) ([]string, error) {
	key := cacheKey(modelID, quantization)

	for _, endpoint := range r.Endpoints {
		pods, err := r.fetchFromEndpoint(endpoint, modelID, quantization)
		if err == nil {
			r.setCache(key, pods)
			return pods, nil
		}
		log.Printf("[resolver] endpoint %s unavailable: %v", endpoint, err)
	}

	// All endpoints failed — return cached result (even if stale).
	r.mu.RLock()
	entry, ok := r.cache[key]
	r.mu.RUnlock()
	if ok {
		age := time.Since(entry.writtenAt).Truncate(time.Second)
		log.Printf("[resolver] all directory endpoints unreachable; serving %d cached pods for %q (age %s)", len(entry.pods), modelID, age)
		return entry.pods, nil
	}

	return nil, fmt.Errorf("all directory endpoints unreachable and no cached result for model %s", modelID)
}

// RegisterPod pushes this pod's aggregate health digest to all configured directory endpoints.
func (r *CentralizedResolver) RegisterPod(ctx context.Context, digest protocol.PodHealthDigest) error {
	b, err := json.Marshal(digest)
	if err != nil {
		return fmt.Errorf("marshal digest: %w", err)
	}
	var lastErr error
	for _, endpoint := range r.Endpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/pods/register", bytes.NewReader(b))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := r.HTTPClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("POST %s/pods/register: %w", endpoint, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			lastErr = fmt.Errorf("directory %s HTTP %d", endpoint, resp.StatusCode)
			continue
		}
		lastErr = nil // at least one succeeded
	}
	return lastErr
}

func (r *CentralizedResolver) fetchFromEndpoint(endpoint, modelID, quantization string) ([]string, error) {
	url := endpoint + "/pods?model_id=" + modelID
	if quantization != "" {
		url += "&quantization=" + quantization
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		rb, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, rb)
	}
	var result protocol.DirectoryQueryResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return result.MatchingPods, nil
}

func (r *CentralizedResolver) setCache(key string, pods []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[key] = cacheEntry{pods: pods, writtenAt: time.Now()}
}

func cacheKey(modelID, quantization string) string {
	return modelID + "|" + quantization
}

// FederatedResolver is the stage-2 implementation.
// Reputation-gated, periodically-rotated community-run directory instances.
// Do NOT implement until CentralizedResolver is validated in production.
//
// MILESTONE 7 — not implemented yet.
type FederatedResolver struct{}

func (r *FederatedResolver) ResolvePodsForModel(modelID, quantization string) ([]string, error) {
	return nil, errors.New("milestone 7: not implemented")
}
func (r *FederatedResolver) RegisterPod(ctx context.Context, digest protocol.PodHealthDigest) error {
	return errors.New("milestone 7: not implemented")
}

// DHTResolver is the stage-3 fully-decentralized Kademlia-style implementation.
// Out of scope for initial build — carries real Sybil/eclipse risk (proposal §7.3/7.4).
// Revisit only after stage 2 has operating history and a dedicated threat-modeling pass.
type DHTResolver struct{}

func (r *DHTResolver) ResolvePodsForModel(modelID, quantization string) ([]string, error) {
	return nil, errors.New("out of scope: DHT resolver requires dedicated threat-modeling pass (proposal §7.3/7.4)")
}
func (r *DHTResolver) RegisterPod(ctx context.Context, digest protocol.PodHealthDigest) error {
	return errors.New("out of scope: DHT resolver requires dedicated threat-modeling pass (proposal §7.3/7.4)")
}
