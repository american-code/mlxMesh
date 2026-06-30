package directory

import (
	"sync"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

type podEntry struct {
	digest   protocol.PodHealthDigest
	lastSeen time.Time
}

// PodStore holds aggregate pod health digests for the directory layer.
// Does NOT hold: individual node data, job assignments, payloads, or settlement data (proposal §7.1).
type PodStore struct {
	mu     sync.RWMutex
	pods   map[string]podEntry
	podTTL time.Duration
}

func NewPodStore(ttl time.Duration) *PodStore {
	return &PodStore{
		pods:   make(map[string]podEntry),
		podTTL: ttl,
	}
}

// Upsert adds or replaces a pod's health digest and resets its TTL clock.
func (s *PodStore) Upsert(digest protocol.PodHealthDigest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pods[digest.PodID] = podEntry{digest: digest, lastSeen: time.Now()}
}

// QueryForModel returns pod IDs of live pods that report serving modelID.
// Quantization filtering is NOT available at digest granularity — the directory
// intentionally sees only aggregate capability, not per-model-variant detail.
// Callers must contact the pod coordinator for fine-grained availability checks.
func (s *PodStore) QueryForModel(modelID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []string
	for _, e := range s.pods {
		if time.Since(e.lastSeen) > s.podTTL {
			continue
		}
		for _, m := range e.digest.ServableModelIDs {
			if m == modelID {
				result = append(result, e.digest.PodID)
				break
			}
		}
	}
	return result
}

// AllPodIDs returns pod IDs for all live (within TTL) pods.
func (s *PodStore) AllPodIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []string
	for _, e := range s.pods {
		if time.Since(e.lastSeen) > s.podTTL {
			continue
		}
		result = append(result, e.digest.PodID)
	}
	return result
}

// LiveCount returns the number of live pods.
func (s *PodStore) LiveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, e := range s.pods {
		if time.Since(e.lastSeen) <= s.podTTL {
			n++
		}
	}
	return n
}
