package federation

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

func signedEvent(t *testing.T, podID string, seq uint64, userID string, amount float64, priv, pub []byte) LedgerEvent {
	t.Helper()
	evt := LedgerEvent{
		PodID:     podID,
		Sequence:  seq,
		EventType: EventEarnedContrib,
		UserID:    userID,
		Amount:    amount,
		IssuedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	signed, err := Sign(evt, priv, pub)
	if err != nil {
		t.Fatalf("sign event: %v", err)
	}
	return signed
}

func TestSignAndVerifyLedgerEvent(t *testing.T) {
	priv, pub, _ := protocol.GenerateNodeIdentity()
	evt := signedEvent(t, "pod-us", 1, "user-1", 10.0, priv, pub)
	gotPub, ok := VerifySelfConsistent(evt)
	if !ok {
		t.Fatal("expected signature to verify")
	}
	if string(gotPub) != string(pub) {
		t.Fatal("recovered public key does not match signer")
	}
}

func TestVerifySelfConsistent_TamperedAmount(t *testing.T) {
	priv, pub, _ := protocol.GenerateNodeIdentity()
	evt := signedEvent(t, "pod-us", 1, "user-1", 10.0, priv, pub)
	evt.Amount = 1000.0 // a forged/rewritten amount after signing
	if _, ok := VerifySelfConsistent(evt); ok {
		t.Fatal("expected verification to fail after tampering with a signed field")
	}
}

func TestStore_AppendAndFetchSelfEvents(t *testing.T) {
	store, err := NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	priv, pub, _ := protocol.GenerateNodeIdentity()
	e1 := signedEvent(t, "pod-us", store.NextSequence(), "user-1", 10.0, priv, pub)
	if err := store.AppendSelfEvent(e1); err != nil {
		t.Fatalf("append: %v", err)
	}
	e2 := signedEvent(t, "pod-us", store.NextSequence(), "user-2", 5.0, priv, pub)
	if err := store.AppendSelfEvent(e2); err != nil {
		t.Fatalf("append: %v", err)
	}
	events := store.SelfEventsSince(0)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	events = store.SelfEventsSince(e1.Sequence)
	if len(events) != 1 || events[0].UserID != "user-2" {
		t.Fatalf("expected only the event after sequence %d, got %+v", e1.Sequence, events)
	}
}

func TestStore_StoreWitnessedEvent_PinsAndAccepts(t *testing.T) {
	store, err := NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	priv, pub, _ := protocol.GenerateNodeIdentity()
	evt := signedEvent(t, "pod-eu", 1, "user-1", 42.0, priv, pub)
	if err := store.StoreWitnessedEvent(evt); err != nil {
		t.Fatalf("expected first witnessed event to be accepted: %v", err)
	}
	if got := store.WitnessedGrossCredits("pod-eu", "user-1"); got != 42.0 {
		t.Fatalf("expected witnessed gross credits 42.0, got %v", got)
	}
	// Re-witnessing the exact same event (peers are polled repeatedly) is a no-op.
	if err := store.StoreWitnessedEvent(evt); err != nil {
		t.Fatalf("expected re-witnessing the identical event to be idempotent: %v", err)
	}
}

func TestStore_StoreWitnessedEvent_RejectsImpersonation(t *testing.T) {
	store, err := NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	priv1, pub1, _ := protocol.GenerateNodeIdentity()
	priv2, pub2, _ := protocol.GenerateNodeIdentity()

	first := signedEvent(t, "pod-eu", 1, "user-1", 10.0, priv1, pub1)
	if err := store.StoreWitnessedEvent(first); err != nil {
		t.Fatalf("expected first event to be accepted: %v", err)
	}
	// A different key now claims to be pod-eu — a forked/impersonating coordinator.
	impersonator := signedEvent(t, "pod-eu", 2, "user-1", 999.0, priv2, pub2)
	if err := store.StoreWitnessedEvent(impersonator); err != ErrKeyMismatch {
		t.Fatalf("expected ErrKeyMismatch for a different signing key under the same pod_id, got %v", err)
	}
}

func TestStore_StoreWitnessedEvent_DetectsForkedHistory(t *testing.T) {
	store, err := NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	priv, pub, _ := protocol.GenerateNodeIdentity()
	original := signedEvent(t, "pod-eu", 1, "user-1", 10.0, priv, pub)
	if err := store.StoreWitnessedEvent(original); err != nil {
		t.Fatalf("expected original event to be accepted: %v", err)
	}
	// Same pod, same key, same sequence number — but a DIFFERENT signed amount.
	// A honest pod never resigns a sequence number; this is a provable fork.
	rewritten := signedEvent(t, "pod-eu", 1, "user-1", 99999.0, priv, pub)
	if err := store.StoreWitnessedEvent(rewritten); err != ErrForkedHistory {
		t.Fatalf("expected ErrForkedHistory when a peer resigns a sequence number, got %v", err)
	}
}

func TestStore_PersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "federation.db")
	priv, pub, _ := protocol.GenerateNodeIdentity()

	store1, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	e1 := signedEvent(t, "pod-us", store1.NextSequence(), "user-1", 7.0, priv, pub)
	if err := store1.AppendSelfEvent(e1); err != nil {
		t.Fatalf("append: %v", err)
	}
	witnessed := signedEvent(t, "pod-eu", 1, "user-2", 3.0, priv, pub)
	if err := store1.StoreWitnessedEvent(witnessed); err != nil {
		t.Fatalf("store witnessed: %v", err)
	}

	// Simulate a coordinator restart against the same DB file.
	store2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if got := store2.NextSequence(); got != e1.Sequence+1 {
		t.Fatalf("expected sequence counter to resume at %d, got %d", e1.Sequence+1, got)
	}
	if got := store2.WitnessedGrossCredits("pod-eu", "user-2"); got != 3.0 {
		t.Fatalf("expected witnessed history to survive restart, got %v", got)
	}
	// The pinned peer key must also survive — an impersonator shouldn't get a
	// fresh TOFU chance just because the coordinator bounced.
	impersonatorPriv, impersonatorPub, _ := protocol.GenerateNodeIdentity()
	impersonator := signedEvent(t, "pod-eu", 2, "user-2", 1.0, impersonatorPriv, impersonatorPub)
	if err := store2.StoreWitnessedEvent(impersonator); err != ErrKeyMismatch {
		t.Fatalf("expected pinned peer key to survive restart and reject impersonation, got %v", err)
	}
}

// TestAuditInvariant_BalanceExceedingWitnessedCreditsIsInconsistent exercises
// the exact comparison cmd/coordinator's GET /federation/audit/{user_id}
// handler performs: a pod's live-reported balance must never exceed the sum
// of everything it has signed as credited to that user.
func TestAuditInvariant_BalanceExceedingWitnessedCreditsIsInconsistent(t *testing.T) {
	store, err := NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	priv, pub, _ := protocol.GenerateNodeIdentity()
	if err := store.StoreWitnessedEvent(signedEvent(t, "pod-eu", 1, "user-1", 100.0, priv, pub)); err != nil {
		t.Fatalf("store witnessed: %v", err)
	}
	witnessed := store.WitnessedGrossCredits("pod-eu", "user-1")

	honestClaimedBalance := 60.0 // spent some of the 100 credited — legitimate
	if honestClaimedBalance > witnessed {
		t.Fatal("expected an honest balance (<= witnessed gross credits) to be considered consistent")
	}

	forkedClaimedBalance := 500.0 // far more than this pod ever signed as credited
	if forkedClaimedBalance <= witnessed {
		t.Fatal("expected a balance exceeding witnessed gross credits to be flagged inconsistent")
	}
}
