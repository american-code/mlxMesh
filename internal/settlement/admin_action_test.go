package settlement

import (
	"path/filepath"
	"testing"
	"time"
)

// TestAdminAdjustment_CountsAsEarnedInBalance guards the exact gap that
// would otherwise exist: GetBalance's tallying switch only recognized
// CreditOriginStartupGrant/CreditOriginEarnedContrib before
// CreditOriginAdminAdjustment was wired in — an admin-injected credit would
// have silently vanished from the reported balance.
func TestAdminAdjustment_CountsAsEarnedInBalance(t *testing.T) {
	l := NewLedger()
	if err := l.CreditAccount(creditEntry("oim-treasury", CreditOriginAdminAdjustment, 50)); err != nil {
		t.Fatal(err)
	}
	bal := l.GetBalance("oim-treasury")
	if bal.EarnedBalance != 50 {
		t.Errorf("EarnedBalance = %v, want 50 (admin adjustment should tally as earned)", bal.EarnedBalance)
	}
	if bal.GrantBalance != 0 {
		t.Errorf("GrantBalance = %v, want 0 — admin adjustment must never count as a startup grant", bal.GrantBalance)
	}
	if bal.Total != 50 {
		t.Errorf("Total = %v, want 50", bal.Total)
	}
}

// TestAdminAdjustment_IsSpendable guards the matching gap in DebitAccount's
// own tallying switch — an admin-injected credit must actually be usable,
// not just visible in GetBalance.
func TestAdminAdjustment_IsSpendable(t *testing.T) {
	l := NewLedger()
	if err := l.CreditAccount(creditEntry("oim-treasury", CreditOriginAdminAdjustment, 50)); err != nil {
		t.Fatal(err)
	}
	if !l.DebitAccount("oim-treasury", 30, "job-1") {
		t.Fatal("expected the admin-adjustment credit to be spendable")
	}
	if got := l.GetBalance("oim-treasury").Total; got != 20 {
		t.Errorf("balance after spend = %v, want 20", got)
	}
}

// TestAdminAdjustment_ReconcilesCleanly guards the third tallying site —
// Reconcile's own switch — the same way: an admin-injected credit must be
// counted in TotalEarnedCredits/TotalCredits/TotalOutstanding and must not
// spuriously trip the overdraft/orphan-debit tripwire.
func TestAdminAdjustment_ReconcilesCleanly(t *testing.T) {
	l := NewLedger()
	if err := l.CreditAccount(creditEntry("oim-treasury", CreditOriginAdminAdjustment, 50)); err != nil {
		t.Fatal(err)
	}
	if !l.DebitAccount("oim-treasury", 20, "job-1") {
		t.Fatal("debit should succeed")
	}

	r := l.Reconcile()
	if !r.Consistent {
		t.Fatalf("expected a clean reconciliation, got anomalies: %+v", r.Anomalies)
	}
	if r.TotalEarnedCredits != 50 {
		t.Errorf("TotalEarnedCredits = %v, want 50", r.TotalEarnedCredits)
	}
	if r.TotalGrantCredits != 0 {
		t.Errorf("TotalGrantCredits = %v, want 0", r.TotalGrantCredits)
	}
	if r.TotalCredits != 50 {
		t.Errorf("TotalCredits = %v, want 50", r.TotalCredits)
	}
	if r.TotalOutstanding != 30 {
		t.Errorf("TotalOutstanding = %v, want 30", r.TotalOutstanding)
	}
}

func TestRecentAdminActions_NewestFirstAndRespectsLimit(t *testing.T) {
	l := NewLedger()
	base := time.Unix(1_800_000_000, 0)
	for i, reason := range []string{"first", "second", "third"} {
		if err := l.RecordAdminAction("treasury_credit", reason, float64(i+1), base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	all := l.RecentAdminActions(0)
	if len(all) != 3 {
		t.Fatalf("expected 3 actions with limit=0 (no cap), got %d", len(all))
	}
	if all[0].Detail != "third" || all[1].Detail != "second" || all[2].Detail != "first" {
		t.Fatalf("expected newest-first order, got %+v", all)
	}

	limited := l.RecentAdminActions(2)
	if len(limited) != 2 || limited[0].Detail != "third" || limited[1].Detail != "second" {
		t.Fatalf("expected the 2 most recent, got %+v", limited)
	}
}

func TestRecentAdminActions_EmptyLedgerReturnsEmpty(t *testing.T) {
	l := NewLedger()
	if got := l.RecentAdminActions(10); len(got) != 0 {
		t.Fatalf("expected no actions on a fresh ledger, got %+v", got)
	}
}

// TestAdminActions_PersistAcrossReopen mirrors the existing persistence
// test pattern used elsewhere in this codebase (e.g.
// TestAPIKeyStore_PersistsAcrossReopen) — an admin action written to a
// persistent ledger must survive a coordinator restart, not just live in
// memory.
func TestAdminActions_PersistAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	l1, err := NewPersistentLedger(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	if err := l1.RecordAdminAction("treasury_credit", "quarterly top-up", 100, now); err != nil {
		t.Fatal(err)
	}

	l2, err := NewPersistentLedger(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	got := l2.RecentAdminActions(10)
	if len(got) != 1 {
		t.Fatalf("expected 1 admin action to survive reopen, got %+v", got)
	}
	if got[0].Detail != "quarterly top-up" || got[0].Amount != 100 {
		t.Errorf("unexpected reloaded action: %+v", got[0])
	}
}
