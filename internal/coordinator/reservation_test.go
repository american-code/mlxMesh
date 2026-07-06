package coordinator

import (
	"testing"
	"time"
)

func TestReservationStore_CreateAndResolve(t *testing.T) {
	s := NewReservationStore()
	now := time.Now()
	id, err := s.Create(NodeTarget{NodeID: "node-1", Endpoint: "http://node-1:8765"}, now)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := s.Resolve(id, now.Add(time.Second))
	if !ok {
		t.Fatal("expected reservation to resolve")
	}
	if res.Target.NodeID != "node-1" || res.Target.Endpoint != "http://node-1:8765" {
		t.Errorf("unexpected reservation: %+v", res)
	}
}

func TestReservationStore_SingleUse(t *testing.T) {
	s := NewReservationStore()
	now := time.Now()
	id, _ := s.Create(NodeTarget{NodeID: "node-1", Endpoint: "http://node-1:8765"}, now)
	if _, ok := s.Resolve(id, now); !ok {
		t.Fatal("first resolve should succeed")
	}
	if _, ok := s.Resolve(id, now); ok {
		t.Error("second resolve of the same reservation should fail (single-use)")
	}
}

func TestReservationStore_ExpiredRejected(t *testing.T) {
	s := NewReservationStore()
	now := time.Now()
	id, _ := s.Create(NodeTarget{NodeID: "node-1", Endpoint: "http://node-1:8765"}, now)
	if _, ok := s.Resolve(id, now.Add(ReservationTTL+time.Second)); ok {
		t.Error("expected expired reservation to be rejected")
	}
}

func TestReservationStore_UnknownIDRejected(t *testing.T) {
	s := NewReservationStore()
	if _, ok := s.Resolve("nonexistent", time.Now()); ok {
		t.Error("expected unknown reservation id to be rejected")
	}
}
