package coordinator

import (
	"testing"
	"time"
)

func TestCoordinationRegistry_AnnounceAndSnapshot(t *testing.T) {
	r := NewCoordinationRegistry()
	now := time.Now()
	r.Announce(CoordinationParticipant{DeviceID: "ipad-1", IsMobile: true, Region: "us"}, now)

	snap := r.SnapshotAt(now)
	if len(snap) != 1 {
		t.Fatalf("want 1 participant, got %d", len(snap))
	}
	if snap[0].Role != "pointer_host" {
		t.Errorf("empty role should default to pointer_host, got %q", snap[0].Role)
	}
	if snap[0].Region != "us" {
		t.Errorf("region not preserved: %q", snap[0].Region)
	}
}

func TestCoordinationRegistry_TTLEviction(t *testing.T) {
	r := NewCoordinationRegistry()
	base := time.Now()
	r.Announce(CoordinationParticipant{DeviceID: "ghost"}, base)

	// One tick past the TTL: the ghost is evicted.
	if got := r.SnapshotAt(base.Add(CoordinationTTL + time.Second)); len(got) != 0 {
		t.Fatalf("expired participant should be evicted, got %d", len(got))
	}
}

// PointersServed is the credited-work counter; it must persist across heartbeat
// re-announces (the client never reports it back) and never resurrect an
// unknown/expired device.
func TestCoordinationRegistry_PointersServedPersistsAcrossReannounce(t *testing.T) {
	r := NewCoordinationRegistry()
	now := time.Now()
	r.Announce(CoordinationParticipant{DeviceID: "ipad-1", Region: "us"}, now)

	for i := 0; i < 3; i++ {
		if !r.RecordPointerServed("ipad-1") {
			t.Fatal("RecordPointerServed should return true for a live device")
		}
	}
	// A heartbeat re-announce must NOT reset the tally.
	r.Announce(CoordinationParticipant{DeviceID: "ipad-1", Region: "us"}, now.Add(time.Second))

	snap := r.SnapshotAt(now.Add(time.Second))
	if snap[0].PointersServed != 3 {
		t.Errorf("pointers_served reset across re-announce: got %d, want 3", snap[0].PointersServed)
	}

	// Unknown device is ignored (no resurrection).
	if r.RecordPointerServed("never-seen") {
		t.Error("RecordPointerServed should return false for an unknown device")
	}
}
