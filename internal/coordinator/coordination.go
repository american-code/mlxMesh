package coordinator

import (
	"sync"
	"time"
)

// CoordinationTTL is how long a coordination participant stays "live" after its
// last announce. Devices re-announce on a heartbeat; a clean withdraw removes
// them immediately, and this TTL catches devices that vanished without one.
const CoordinationTTL = 90 * time.Second

// CoordinationParticipant is an iOS device participating as a SECURITY /
// coordination layer — it classifies on-device and hosts encrypted payload
// pointers. It is NOT an inference node: it carries no models and must never be
// selected by the fast/background routers. It lives in its own registry
// precisely so it can be surfaced to dashboards (distinct icon, toggleable
// layer) without ever leaking into inference routing.
type CoordinationParticipant struct {
	DeviceID       string `json:"device_id"`
	Role           string `json:"role"` // e.g. "pointer_host"
	IsMobile       bool   `json:"is_mobile"`
	GeographicHint string `json:"geographic_hint"`
	LastSeenAt     string `json:"last_seen_at"`
	// PointersServed counts inference requests whose encrypted payload was
	// fetched from this device's pointer path — the concrete "work" a
	// coordination participant does. Monotonic for the participant's lifetime;
	// preserved across heartbeat re-announces and surfaced to dashboards.
	PointersServed int64 `json:"pointers_served"`
}

// CoordinationRegistry tracks live coordination participants. Separate from
// NodeRegistry so the inference routers can never accidentally dispatch a job to
// a device that only hosts pointers.
type CoordinationRegistry struct {
	mu    sync.RWMutex
	items map[string]coordEntry
}

type coordEntry struct {
	participant CoordinationParticipant
	lastSeen    time.Time
}

func NewCoordinationRegistry() *CoordinationRegistry {
	return &CoordinationRegistry{items: make(map[string]coordEntry)}
}

// Announce records or refreshes a participant. Missing DeviceID is ignored.
func (r *CoordinationRegistry) Announce(p CoordinationParticipant, now time.Time) {
	if p.DeviceID == "" {
		return
	}
	if p.Role == "" {
		p.Role = "pointer_host"
	}
	p.LastSeenAt = now.UTC().Format(time.RFC3339)
	r.mu.Lock()
	// Preserve the served-pointer tally across heartbeat re-announces — the
	// client doesn't (and shouldn't need to) report its own count back.
	if prev, ok := r.items[p.DeviceID]; ok {
		p.PointersServed = prev.participant.PointersServed
	}
	r.items[p.DeviceID] = coordEntry{participant: p, lastSeen: now}
	r.mu.Unlock()
}

// RecordPointerServed increments the served-pointer counter for a live
// participant, returning true if the device was known. Unknown/expired devices
// are ignored (false) — a stale pointer reference must not resurrect a departed
// participant.
func (r *CoordinationRegistry) RecordPointerServed(deviceID string) bool {
	if deviceID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.items[deviceID]
	if !ok {
		return false
	}
	e.participant.PointersServed++
	r.items[deviceID] = e
	return true
}

// Withdraw removes a participant immediately (clean departure).
func (r *CoordinationRegistry) Withdraw(deviceID string) {
	r.mu.Lock()
	delete(r.items, deviceID)
	r.mu.Unlock()
}

// SnapshotAt returns participants still live at `now`, evicting expired ones.
// Deterministic for tests; the handler calls Snapshot() with the wall clock.
func (r *CoordinationRegistry) SnapshotAt(now time.Time) []CoordinationParticipant {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]CoordinationParticipant, 0, len(r.items))
	for id, e := range r.items {
		if now.Sub(e.lastSeen) > CoordinationTTL {
			delete(r.items, id)
			continue
		}
		out = append(out, e.participant)
	}
	return out
}

// Snapshot returns the currently-live participants.
func (r *CoordinationRegistry) Snapshot() []CoordinationParticipant {
	return r.SnapshotAt(time.Now())
}

// Count returns the number of live participants.
func (r *CoordinationRegistry) Count() int {
	return len(r.SnapshotAt(time.Now()))
}
