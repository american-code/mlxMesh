package coordinator

import (
	"sync"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// MeasurementStore holds the most recently submitted MeasuredSignature per node.
// Separate from NodeRegistry — nodes submit via the reputation client; the registry
// holds the claimed signature from initial registration.
type MeasurementStore struct {
	mu      sync.RWMutex
	entries map[string]*protocol.MeasuredSignature
}

func NewMeasurementStore() *MeasurementStore {
	return &MeasurementStore{entries: make(map[string]*protocol.MeasuredSignature)}
}

// Store saves a submitted benchmark measurement for a node, replacing any prior entry.
func (s *MeasurementStore) Store(nodeID string, sig *protocol.MeasuredSignature) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[nodeID] = sig
}

// Get returns the stored measurement for a node, or (nil, false) if none submitted yet.
func (s *MeasurementStore) Get(nodeID string) (*protocol.MeasuredSignature, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sig, ok := s.entries[nodeID]
	return sig, ok
}
