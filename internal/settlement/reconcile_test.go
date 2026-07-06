package settlement

import (
	"testing"
	"time"
)

func creditEntry(user string, origin CreditOrigin, amount float64) CreditEntry {
	return CreditEntry{UserID: user, Origin: origin, Amount: amount, GrantedOrEarnedAt: time.Unix(0, 0)}
}

func TestReconcile_HealthyLedger(t *testing.T) {
	l := NewLedger()
	if err := l.CreditAccount(creditEntry("alice", CreditOriginStartupGrant, 100)); err != nil {
		t.Fatal(err)
	}
	if err := l.CreditAccount(creditEntry("bob", CreditOriginEarnedContrib, 50)); err != nil {
		t.Fatal(err)
	}
	if !l.DebitAccount("alice", 30, "job-1") {
		t.Fatal("debit should succeed")
	}

	r := l.Reconcile()
	if !r.Consistent {
		t.Errorf("healthy ledger should reconcile, got anomalies: %+v", r.Anomalies)
	}
	if r.TotalCredits != 150 {
		t.Errorf("TotalCredits = %v, want 150", r.TotalCredits)
	}
	if r.TotalDebits != 30 {
		t.Errorf("TotalDebits = %v, want 30", r.TotalDebits)
	}
	if r.TotalOutstanding != 120 { // alice 70 + bob 50
		t.Errorf("TotalOutstanding = %v, want 120", r.TotalOutstanding)
	}
	if r.UserCount != 2 {
		t.Errorf("UserCount = %d, want 2", r.UserCount)
	}
}

func TestReconcile_DetectsOverdraft(t *testing.T) {
	// Force a corrupt state that DebitAccount would never permit, to prove the
	// tripwire fires: inject a debit larger than the credit directly.
	l := NewLedger()
	if err := l.CreditAccount(creditEntry("mallory", CreditOriginStartupGrant, 10)); err != nil {
		t.Fatal(err)
	}
	l.debits = append(l.debits, ledgerDebit{UserID: "mallory", Amount: 999, JobID: "corrupt", WrittenAt: time.Unix(0, 0)})

	r := l.Reconcile()
	if r.Consistent {
		t.Fatal("expected reconciliation to flag the overdraft")
	}
	if len(r.Anomalies) != 1 || r.Anomalies[0].Kind != AnomalyOverdraft {
		t.Fatalf("expected one overdraft anomaly, got %+v", r.Anomalies)
	}
	if r.Anomalies[0].UserID != "mallory" {
		t.Errorf("anomaly user = %q, want mallory", r.Anomalies[0].UserID)
	}
}

func TestReconcile_DetectsOrphanDebit(t *testing.T) {
	l := NewLedger()
	// A debit for a user with zero credit entries.
	l.debits = append(l.debits, ledgerDebit{UserID: "ghost", Amount: 5, JobID: "j", WrittenAt: time.Unix(0, 0)})

	r := l.Reconcile()
	if r.Consistent {
		t.Fatal("expected an anomaly for a debit against a never-funded account")
	}
	if r.Anomalies[0].Kind != AnomalyOrphanDebit {
		t.Errorf("kind = %q, want orphan_debit", r.Anomalies[0].Kind)
	}
}

func TestReconcile_FloatEpsilonNotFlagged(t *testing.T) {
	l := NewLedger()
	if err := l.CreditAccount(creditEntry("alice", CreditOriginEarnedContrib, 0.1)); err != nil {
		t.Fatal(err)
	}
	if err := l.CreditAccount(creditEntry("alice", CreditOriginEarnedContrib, 0.2)); err != nil {
		t.Fatal(err)
	}
	// 0.1 + 0.2 != 0.3 exactly in float; debiting 0.3 must not read as overdraft.
	l.debits = append(l.debits, ledgerDebit{UserID: "alice", Amount: 0.3, JobID: "j", WrittenAt: time.Unix(0, 0)})

	if r := l.Reconcile(); !r.Consistent {
		t.Errorf("sub-epsilon float rounding must not be flagged as overdraft: %+v", r.Anomalies)
	}
}
