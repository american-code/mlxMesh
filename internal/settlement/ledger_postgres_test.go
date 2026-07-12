// Copyright (C) 2024 Open Inference Mesh
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package settlement

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// newTestPostgresLedger skips the test cleanly if TEST_POSTGRES_DSN isn't
// set — same "skip cleanly if the dependency isn't there" precedent already
// used by the Python/Swift SDK integration tests (which skip if the go
// toolchain isn't on PATH), so `go test ./...` stays zero-dependency by
// default. TEST_POSTGRES_DSN must be a postgres:// URL (the same form
// --ledger-db-url expects). Each test gets its own schema (via search_path)
// so tests can run without stepping on each other's rows.
func newTestPostgresLedger(t *testing.T) *Ledger {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping Postgres-backed ledger test")
	}

	schema := fmt.Sprintf("ledger_test_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	if _, err := admin.Exec(fmt.Sprintf("CREATE SCHEMA %s", schema)); err != nil {
		admin.Close()
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		admin.Exec(fmt.Sprintf("DROP SCHEMA %s CASCADE", schema))
		admin.Close()
	})

	scopedDSN, err := addSearchPath(dsn, schema)
	if err != nil {
		t.Fatalf("build scoped dsn: %v", err)
	}
	l, err := NewPostgresLedger(scopedDSN)
	if err != nil {
		t.Fatalf("open postgres ledger: %v", err)
	}
	t.Cleanup(func() { l.db.Close() })
	return l
}

func addSearchPath(dsn, schema string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse TEST_POSTGRES_DSN as a URL: %w", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func TestPostgresLedger_CreditAndBalanceRoundTrip(t *testing.T) {
	l := newTestPostgresLedger(t)
	if err := l.CreditAccount(creditEntry("alice", CreditOriginStartupGrant, 100)); err != nil {
		t.Fatal(err)
	}
	if err := l.CreditAccount(creditEntry("alice", CreditOriginEarnedContrib, 25)); err != nil {
		t.Fatal(err)
	}
	bal := l.GetBalance("alice")
	if bal.GrantBalance != 100 || bal.EarnedBalance != 25 || bal.Total != 125 {
		t.Errorf("balance = %+v, want grant=100 earned=25 total=125", bal)
	}
}

func TestPostgresLedger_DebitGrantFirstOrdering(t *testing.T) {
	l := newTestPostgresLedger(t)
	if err := l.CreditAccount(creditEntry("alice", CreditOriginStartupGrant, 100)); err != nil {
		t.Fatal(err)
	}
	if err := l.CreditAccount(creditEntry("alice", CreditOriginEarnedContrib, 50)); err != nil {
		t.Fatal(err)
	}
	if !l.DebitAccount("alice", 120, "job-1") {
		t.Fatal("expected debit to succeed")
	}
	bal := l.GetBalance("alice")
	if bal.GrantBalance != 0 {
		t.Errorf("GrantBalance = %v, want 0 (grant spent first)", bal.GrantBalance)
	}
	if bal.EarnedBalance != 30 {
		t.Errorf("EarnedBalance = %v, want 30 (remainder after 120 - 100 grant)", bal.EarnedBalance)
	}
}

func TestPostgresLedger_DebitRejectsOverdraft(t *testing.T) {
	l := newTestPostgresLedger(t)
	if err := l.CreditAccount(creditEntry("alice", CreditOriginStartupGrant, 10)); err != nil {
		t.Fatal(err)
	}
	if l.DebitAccount("alice", 11, "job-1") {
		t.Fatal("expected debit to be rejected as an overdraft")
	}
	bal := l.GetBalance("alice")
	if bal.Total != 10 {
		t.Errorf("Total = %v, want 10 — rejected debit must not partially apply", bal.Total)
	}
}

func TestPostgresLedger_ClaimStartupGrantOnce(t *testing.T) {
	l := newTestPostgresLedger(t)
	entry := creditEntry("alice", CreditOriginStartupGrant, 40)
	claimed, err := l.ClaimStartupGrantOnce(entry)
	if err != nil || !claimed {
		t.Fatalf("first claim: claimed=%v err=%v, want true/nil", claimed, err)
	}
	claimed, err = l.ClaimStartupGrantOnce(entry)
	if err != nil || claimed {
		t.Fatalf("second claim: claimed=%v err=%v, want false/nil", claimed, err)
	}
	if bal := l.GetBalance("alice"); bal.GrantBalance != 40 {
		t.Errorf("GrantBalance = %v, want 40 (only the first claim should have credited)", bal.GrantBalance)
	}
}

func TestPostgresLedger_AdminActionsAndReconcile(t *testing.T) {
	l := newTestPostgresLedger(t)
	if err := l.RecordAdminAction("treasury_credit", "bootstrap", 25, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := l.CreditAccount(creditEntry("alice", CreditOriginStartupGrant, 100)); err != nil {
		t.Fatal(err)
	}
	if !l.DebitAccount("alice", 30, "job-1") {
		t.Fatal("expected debit to succeed")
	}

	actions := l.RecentAdminActions(10)
	if len(actions) != 1 || actions[0].Detail != "bootstrap" {
		t.Fatalf("RecentAdminActions = %+v, want one entry with detail 'bootstrap'", actions)
	}

	report := l.Reconcile()
	if !report.Consistent {
		t.Fatalf("expected a healthy ledger to reconcile clean, got anomalies: %+v", report.Anomalies)
	}
	if report.TotalGrantCredits != 100 || report.TotalDebits != 30 {
		t.Errorf("report = %+v, want TotalGrantCredits=100 TotalDebits=30", report)
	}

	liability := l.TotalOutstandingGrantLiability()
	if liability != 70 {
		t.Errorf("TotalOutstandingGrantLiability = %v, want 70 (100 granted - 30 debited)", liability)
	}
}

// TestPostgresLedger_ConcurrentClaimStartupGrantOnce is the test that
// actually justifies this backend's design: N goroutines race to claim the
// same user's startup grant against ONE Postgres instance, simulating N
// coordinator processes doing the same thing. A design that relied on
// Ledger's in-process mutex for this check (as SQLite/memory do) could never
// pass this across real separate processes — only the database-level unique
// constraint in startup_grant_claims can arbitrate it correctly here.
func TestPostgresLedger_ConcurrentClaimStartupGrantOnce(t *testing.T) {
	l := newTestPostgresLedger(t)
	entry := creditEntry("alice", CreditOriginStartupGrant, 40)

	const n = 20
	var wg sync.WaitGroup
	results := make([]bool, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = l.ClaimStartupGrantOnce(entry)
		}(i)
	}
	wg.Wait()

	successes := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: unexpected error: %v", i, err)
		}
		if results[i] {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("successes = %d, want exactly 1 out of %d concurrent claims", successes, n)
	}
	if bal := l.GetBalance("alice"); bal.GrantBalance != 40 {
		t.Errorf("GrantBalance = %v, want 40 — exactly one claim's credit should have landed", bal.GrantBalance)
	}
}

// TestPostgresLedger_ConcurrentDebitNeverOverdraws is the debit-side
// counterpart: N goroutines simultaneously try to debit more than the
// account holds in total. SELECT ... FOR UPDATE's row lock is what prevents
// two goroutines (standing in for two coordinator processes) from both
// reading the same pre-debit balance and both approving a spend the account
// can't cover.
func TestPostgresLedger_ConcurrentDebitNeverOverdraws(t *testing.T) {
	l := newTestPostgresLedger(t)
	if err := l.CreditAccount(creditEntry("alice", CreditOriginStartupGrant, 100)); err != nil {
		t.Fatal(err)
	}

	const n = 20
	const amountEach = 10.0 // n * amountEach = 200, double the funded 100
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = l.DebitAccount("alice", amountEach, fmt.Sprintf("job-%d", i))
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, ok := range results {
		if ok {
			successes++
		}
	}
	if successes != 10 {
		t.Errorf("successes = %d, want exactly 10 (100 balance / 10 per debit)", successes)
	}
	if bal := l.GetBalance("alice"); bal.Total != 0 {
		t.Errorf("Total = %v, want 0 — exactly the funded amount should have been spendable, no more", bal.Total)
	}
}
