package coordinator

import (
	"fmt"
	"testing"
)

func TestHashRing_SameKeySameMemberAcrossRebuilds(t *testing.T) {
	members := []string{"node-a", "node-b", "node-c", "node-d"}
	first, ok := newHashRing(members).get("some-prefix-key")
	if !ok {
		t.Fatal("expected a member for a non-empty ring")
	}
	for i := 0; i < 20; i++ {
		got, ok := newHashRing(members).get("some-prefix-key")
		if !ok || got != first {
			t.Fatalf("ring rebuilt from the identical member set gave a different answer: %q vs %q", got, first)
		}
	}
}

func TestHashRing_EmptyRingReturnsFalse(t *testing.T) {
	if _, ok := newHashRing(nil).get("anything"); ok {
		t.Fatal("expected ok=false for an empty ring")
	}
}

func TestHashRing_RemovingAnUnrelatedMemberDoesNotRemapEveryKey(t *testing.T) {
	// The defining property of consistent hashing over plain hash%N: removing
	// one member should only remap the fraction of keys IT owned, not the
	// whole keyspace. Generate many keys, record their owners with 5 members,
	// then again with 1 removed, and confirm most keys keep the same owner.
	full := []string{"node-a", "node-b", "node-c", "node-d", "node-e"}
	reduced := []string{"node-a", "node-b", "node-c", "node-d"} // node-e removed

	ringFull := newHashRing(full)
	ringReduced := newHashRing(reduced)

	remapped := 0
	const trials = 2000
	for i := 0; i < trials; i++ {
		key := fmt.Sprintf("key-%d", i)
		before, _ := ringFull.get(key)
		if before == "node-e" {
			continue // owned by the removed member — must remap, not counted
		}
		after, _ := ringReduced.get(key)
		if after != before {
			remapped++
		}
	}
	// Plain hash%N would remap close to 100% of surviving keys on membership
	// change; consistent hashing should remap only a small fraction.
	if remapped > trials/10 {
		t.Fatalf("expected only a small fraction of unrelated keys to remap, got %d/%d", remapped, trials)
	}
}
