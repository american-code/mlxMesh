package tests

import (
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/coordinator"
)

func TestCoordinationAnnounceAndSnapshot(t *testing.T) {
	r := coordinator.NewCoordinationRegistry()
	now := time.Unix(1_800_000_000, 0)
	r.Announce(coordinator.CoordinationParticipant{DeviceID: "ipad-1", IsMobile: true, GeographicHint: "us"}, now)
	snap := r.SnapshotAt(now)
	if len(snap) != 1 || snap[0].DeviceID != "ipad-1" {
		t.Fatalf("expected ipad-1 in snapshot, got %v", snap)
	}
	if snap[0].Role != "pointer_host" {
		t.Errorf("expected default role pointer_host, got %q", snap[0].Role)
	}
}

func TestCoordinationWithdrawRemoves(t *testing.T) {
	r := coordinator.NewCoordinationRegistry()
	now := time.Unix(1_800_000_000, 0)
	r.Announce(coordinator.CoordinationParticipant{DeviceID: "ipad-1"}, now)
	r.Withdraw("ipad-1")
	if len(r.SnapshotAt(now)) != 0 {
		t.Error("withdrawn device should not appear")
	}
}

func TestCoordinationTTLEviction(t *testing.T) {
	r := coordinator.NewCoordinationRegistry()
	now := time.Unix(1_800_000_000, 0)
	r.Announce(coordinator.CoordinationParticipant{DeviceID: "ipad-1"}, now)
	// Past TTL with no heartbeat → evicted.
	late := now.Add(coordinator.CoordinationTTL + time.Second)
	if len(r.SnapshotAt(late)) != 0 {
		t.Error("stale device should be evicted after TTL")
	}
	// A heartbeat before expiry keeps it alive.
	r.Announce(coordinator.CoordinationParticipant{DeviceID: "ipad-2"}, now)
	beat := now.Add(coordinator.CoordinationTTL - time.Second)
	r.Announce(coordinator.CoordinationParticipant{DeviceID: "ipad-2"}, beat)
	stillLive := beat.Add(coordinator.CoordinationTTL - time.Second)
	if len(r.SnapshotAt(stillLive)) != 1 {
		t.Error("heartbeated device should stay live")
	}
}

func TestCoordinationPointerServedCounter(t *testing.T) {
	r := coordinator.NewCoordinationRegistry()
	now := time.Unix(1_800_000_000, 0)
	r.Announce(coordinator.CoordinationParticipant{DeviceID: "ipad-1"}, now)

	// Two served pointers → count of 2 in the snapshot.
	if !r.RecordPointerServed("ipad-1") || !r.RecordPointerServed("ipad-1") {
		t.Fatal("RecordPointerServed should return true for a live device")
	}
	snap := r.SnapshotAt(now)
	if len(snap) != 1 || snap[0].PointersServed != 2 {
		t.Fatalf("expected PointersServed=2, got %v", snap)
	}

	// A heartbeat re-announce must NOT reset the tally.
	beat := now.Add(30 * time.Second)
	r.Announce(coordinator.CoordinationParticipant{DeviceID: "ipad-1"}, beat)
	if got := r.SnapshotAt(beat)[0].PointersServed; got != 2 {
		t.Errorf("heartbeat must preserve served count, got %d", got)
	}

	// Unknown / departed devices are ignored — a stale pointer can't resurrect one.
	if r.RecordPointerServed("ghost") {
		t.Error("RecordPointerServed must return false for an unknown device")
	}
}

func TestCoordinationIgnoresEmptyDeviceID(t *testing.T) {
	r := coordinator.NewCoordinationRegistry()
	now := time.Unix(1_800_000_000, 0)
	r.Announce(coordinator.CoordinationParticipant{DeviceID: ""}, now)
	if len(r.SnapshotAt(now)) != 0 {
		t.Error("empty device_id must be ignored")
	}
}
