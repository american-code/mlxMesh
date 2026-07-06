package coordinator

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// ReservationTTL bounds how long a node reservation stays valid — long enough
// for a client to encrypt a payload and submit the job, short enough to limit
// how long a picked node is "spoken for" without ever actually being
// dispatched to.
const ReservationTTL = 30 * time.Second

// Reservation pins one upcoming job to a specific, already-selected node.
// Node-side pointer consumption (M8) needs this: a client must know the
// recipient's ECDH public key BEFORE it can encrypt a payload, but normal
// dispatch only picks a node once the (already-encrypted) request arrives.
//
// The full NodeTarget (endpoint + TLS fingerprint) is captured at reservation
// time from the same manifest that supplied the ECDH key the client encrypts
// to — so the pin dispatched against is always the one paired with that
// endpoint, even if the node re-registers within the TTL.
type Reservation struct {
	Target    NodeTarget
	ExpiresAt time.Time
}

// ReservationStore is a short-lived, in-memory TTL store — same lifecycle
// shape as wallet.Manager's challenge map (single-use, short-lived,
// opportunistically garbage-collected on each Create).
type ReservationStore struct {
	mu    sync.Mutex
	items map[string]Reservation
}

func NewReservationStore() *ReservationStore {
	return &ReservationStore{items: make(map[string]Reservation)}
}

// Create reserves target under a fresh random ID, valid for ReservationTTL
// from now. Opportunistically evicts expired entries.
func (s *ReservationStore) Create(target NodeTarget, now time.Time) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate reservation id: %w", err)
	}
	id := hex.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.items {
		if now.After(v.ExpiresAt) {
			delete(s.items, k)
		}
	}
	s.items[id] = Reservation{Target: target, ExpiresAt: now.Add(ReservationTTL)}
	return id, nil
}

// Resolve consumes (single-use) a reservation, returning its node if the ID is
// known and unexpired. A stale or unknown ID returns ok=false rather than an
// error — callers should reject the job outright (the ciphertext was bound to
// that specific node's key and can't simply be rerouted elsewhere), not
// silently fall back to normal routing.
func (s *ReservationStore) Resolve(id string, now time.Time) (Reservation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.items[id]
	if !ok {
		return Reservation{}, false
	}
	delete(s.items, id) // single-use
	if now.After(r.ExpiresAt) {
		return Reservation{}, false
	}
	return r, true
}
