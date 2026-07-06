package settlement

import (
	"testing"
	"time"
)

// TestLedger_OnCredit_FiresOnCreditAccount verifies the hook the coordinator
// wires to internal/federation (task #52, M7) fires with the exact entry
// that was credited, exactly once, after the ledger write succeeds.
func TestLedger_OnCredit_FiresOnCreditAccount(t *testing.T) {
	l := NewLedger()
	var got []CreditEntry
	l.SetOnCredit(func(e CreditEntry) { got = append(got, e) })

	entry := CreditEntry{
		UserID:            "user-1",
		Origin:            CreditOriginEarnedContrib,
		Amount:            12.5,
		GrantedOrEarnedAt: time.Now(),
		SourceReference:   "record-1",
	}
	if err := l.CreditAccount(entry); err != nil {
		t.Fatalf("credit account: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 hook invocation, got %d", len(got))
	}
	if got[0].UserID != "user-1" || got[0].Amount != 12.5 {
		t.Fatalf("hook received wrong entry: %+v", got[0])
	}
}

// TestLedger_OnCredit_FiresOnceOnFirstGrantClaim verifies the hook fires for
// a successful ClaimStartupGrantOnce, and does NOT fire again on the
// already-claimed path — a duplicate federation event for a grant that was
// never actually re-issued would be its own false-positive fork signal.
func TestLedger_OnCredit_FiresOnceOnFirstGrantClaim(t *testing.T) {
	l := NewLedger()
	var got []CreditEntry
	l.SetOnCredit(func(e CreditEntry) { got = append(got, e) })

	entry := CreditEntry{
		UserID:            "user-1",
		Origin:            CreditOriginStartupGrant,
		Amount:            100,
		GrantedOrEarnedAt: time.Now(),
		SourceReference:   "grant-1",
	}
	claimed, err := l.ClaimStartupGrantOnce(entry)
	if err != nil || !claimed {
		t.Fatalf("expected first claim to succeed: claimed=%v err=%v", claimed, err)
	}

	// Re-claim attempt for the same user — must not credit or fire the hook again.
	claimed, err = l.ClaimStartupGrantOnce(entry)
	if err != nil || claimed {
		t.Fatalf("expected second claim to be a no-op: claimed=%v err=%v", claimed, err)
	}

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 hook invocation across both calls, got %d", len(got))
	}
}

// TestLedger_OnCredit_DoesNotFireOnDebit ensures spending credits never
// triggers a spurious "credit" federation event — only issuance does.
func TestLedger_OnCredit_DoesNotFireOnDebit(t *testing.T) {
	l := NewLedger()
	var got []CreditEntry
	l.SetOnCredit(func(e CreditEntry) { got = append(got, e) })

	if err := l.CreditAccount(CreditEntry{UserID: "user-1", Origin: CreditOriginEarnedContrib, Amount: 10, GrantedOrEarnedAt: time.Now()}); err != nil {
		t.Fatalf("credit account: %v", err)
	}
	if ok := l.DebitAccount("user-1", 5, "job-1"); !ok {
		t.Fatal("expected debit to succeed")
	}
	if len(got) != 1 {
		t.Fatalf("expected the hook to fire only for the credit, not the debit; got %d invocations", len(got))
	}
}
