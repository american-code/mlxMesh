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
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// NewPostgresLedger connects to a Postgres database at dsn and prepares it as
// a multi-coordinator-safe ledger backend. Unlike NewPersistentLedger
// (SQLite, single coordinator process), correctness here is enforced by
// Postgres transactions and row locks — not by Ledger's in-process mutex,
// which cannot serialize writes across separate coordinator processes at
// all. Balances are read directly from a maintained account_balances table
// rather than replayed into memory, so this constructor does not load full
// history on startup the way NewPersistentLedger does.
func NewPostgresLedger(dsn string) (*Ledger, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres ledger db: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres ledger db: %w", err)
	}
	if err := migratePostgresLedgerSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Ledger{db: db, backend: backendPostgres}, nil
}

// migratePostgresLedgerSchema creates the ledger's tables if they don't
// already exist. Each CREATE runs as its own Exec (rather than one
// multi-statement string, as migrateLedgerSchema uses for SQLite) because
// pgx's default prepared-statement exec mode rejects a query string
// containing more than one command.
func migratePostgresLedgerSchema(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS credit_entries (
			id BIGSERIAL PRIMARY KEY,
			user_id TEXT NOT NULL,
			origin TEXT NOT NULL,
			amount DOUBLE PRECISION NOT NULL,
			granted_or_earned_at TIMESTAMPTZ NOT NULL,
			source_reference TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_credit_entries_user ON credit_entries(user_id)`,
		`CREATE TABLE IF NOT EXISTS ledger_debits (
			id BIGSERIAL PRIMARY KEY,
			user_id TEXT NOT NULL,
			amount DOUBLE PRECISION NOT NULL,
			job_id TEXT NOT NULL,
			written_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ledger_debits_user ON ledger_debits(user_id)`,
		`CREATE TABLE IF NOT EXISTS admin_actions (
			id BIGSERIAL PRIMARY KEY,
			action TEXT NOT NULL,
			detail TEXT NOT NULL,
			amount DOUBLE PRECISION NOT NULL,
			performed_at TIMESTAMPTZ NOT NULL
		)`,
		// account_balances is the running total this backend reads from and
		// locks on — it is what lets DebitAccount/ClaimStartupGrantOnce enforce
		// correctness via a real row lock instead of a process-local mutex.
		`CREATE TABLE IF NOT EXISTS account_balances (
			user_id TEXT PRIMARY KEY,
			grant_total DOUBLE PRECISION NOT NULL DEFAULT 0,
			earned_total DOUBLE PRECISION NOT NULL DEFAULT 0,
			total_debited DOUBLE PRECISION NOT NULL DEFAULT 0
		)`,
		// The unique constraint here (not a Go-side "have we seen this user"
		// check) is what makes startup-grant dedup safe across N concurrent
		// coordinator processes.
		`CREATE TABLE IF NOT EXISTS startup_grant_claims (
			user_id TEXT PRIMARY KEY,
			claimed_at TIMESTAMPTZ NOT NULL
		)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate postgres ledger schema: %w", err)
		}
	}
	return nil
}

// grantEarnedDeltas classifies a credit entry into the (grant, earned) totals
// it should add to account_balances — the same split GetBalance/DebitAccount
// apply when reading, kept in one place so a new CreditOrigin can't be added
// to one side and forgotten on the other.
func grantEarnedDeltas(entry CreditEntry) (grantDelta, earnedDelta float64) {
	switch entry.Origin {
	case CreditOriginStartupGrant:
		return entry.Amount, 0
	case CreditOriginEarnedContrib, CreditOriginAdminAdjustment:
		return 0, entry.Amount
	default:
		return 0, 0
	}
}

func insertCreditEntryTx(ctx context.Context, tx *sql.Tx, entry CreditEntry) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO credit_entries (user_id, origin, amount, granted_or_earned_at, source_reference) VALUES ($1, $2, $3, $4, $5)`,
		entry.UserID, entry.Origin, entry.Amount, entry.GrantedOrEarnedAt, entry.SourceReference,
	)
	if err != nil {
		return fmt.Errorf("persist credit entry: %w", err)
	}
	return nil
}

// upsertBalanceTotalsTx adds grantDelta/earnedDelta into account_balances.
// Postgres's ON CONFLICT DO UPDATE is atomic under concurrent writers, so a
// pure additive credit needs no explicit row lock the way a debit does.
func upsertBalanceTotalsTx(ctx context.Context, tx *sql.Tx, userID string, grantDelta, earnedDelta float64) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO account_balances (user_id, grant_total, earned_total, total_debited)
		 VALUES ($1, $2, $3, 0)
		 ON CONFLICT (user_id) DO UPDATE SET
		   grant_total = account_balances.grant_total + EXCLUDED.grant_total,
		   earned_total = account_balances.earned_total + EXCLUDED.earned_total`,
		userID, grantDelta, earnedDelta,
	)
	if err != nil {
		return fmt.Errorf("upsert account balance: %w", err)
	}
	return nil
}

// creditAccountPG is CreditAccount's Postgres path — see CreditAccount.
func (l *Ledger) creditAccountPG(entry CreditEntry) error {
	ctx := context.Background()
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin credit tx: %w", err)
	}
	defer tx.Rollback()

	if err := insertCreditEntryTx(ctx, tx, entry); err != nil {
		return err
	}
	grantDelta, earnedDelta := grantEarnedDeltas(entry)
	if err := upsertBalanceTotalsTx(ctx, tx, entry.UserID, grantDelta, earnedDelta); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit credit tx: %w", err)
	}
	return nil
}

// claimStartupGrantOncePG is ClaimStartupGrantOnce's Postgres path. The
// uniqueness check that used to be "scan l.entries under l.mu" becomes an
// INSERT ... ON CONFLICT DO NOTHING against startup_grant_claims inside the
// same transaction as the credit — this is the piece that stays correct when
// two coordinator processes race to claim the same user's grant at once,
// which a Go mutex (scoped to one process) cannot arbitrate.
func (l *Ledger) claimStartupGrantOncePG(entry CreditEntry) (bool, error) {
	ctx := context.Background()
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin claim tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO startup_grant_claims (user_id, claimed_at) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		entry.UserID, entry.GrantedOrEarnedAt,
	)
	if err != nil {
		return false, fmt.Errorf("claim startup grant: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check claim rows affected: %w", err)
	}
	if rows == 0 {
		return false, nil // already claimed by this or another coordinator; tx rolls back via defer
	}

	if err := insertCreditEntryTx(ctx, tx, entry); err != nil {
		return false, err
	}
	grantDelta, earnedDelta := grantEarnedDeltas(entry)
	if err := upsertBalanceTotalsTx(ctx, tx, entry.UserID, grantDelta, earnedDelta); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit claim tx: %w", err)
	}
	return true, nil
}

// getBalancePG is GetBalance's Postgres path: a single indexed read instead
// of a full-history scan. GetBalance has no error return (matching the other
// two backends, which can't fail once constructed), so a query error here
// falls back to a zero Balance rather than panicking.
func (l *Ledger) getBalancePG(userID string) Balance {
	ctx := context.Background()
	var grantTotal, earnedTotal, totalDebited float64
	err := l.db.QueryRowContext(ctx,
		`SELECT grant_total, earned_total, total_debited FROM account_balances WHERE user_id = $1`,
		userID,
	).Scan(&grantTotal, &earnedTotal, &totalDebited)
	if err != nil && err != sql.ErrNoRows {
		return Balance{}
	}
	return balanceFromTotals(grantTotal, earnedTotal, totalDebited)
}

// debitAccountPG is DebitAccount's Postgres path. SELECT ... FOR UPDATE takes
// a real row lock on this user's balance row, so two coordinator processes
// racing to debit the same user serialize on Postgres instead of both reading
// a stale balance and both approving an overdraft — the concrete fix
// "multi-coordinator write load" needed.
func (l *Ledger) debitAccountPG(userID string, amount float64, jobID string) bool {
	ctx := context.Background()
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return false
	}
	defer tx.Rollback()

	var grantTotal, earnedTotal, totalDebited float64
	err = tx.QueryRowContext(ctx,
		`SELECT grant_total, earned_total, total_debited FROM account_balances WHERE user_id = $1 FOR UPDATE`,
		userID,
	).Scan(&grantTotal, &earnedTotal, &totalDebited)
	if err != nil && err != sql.ErrNoRows {
		return false
	}
	// sql.ErrNoRows: no credits ever issued to this user. Totals stay zero, so
	// the availability check below correctly refuses the debit.

	available := grantTotal + earnedTotal - totalDebited
	if amount > available {
		return false // insufficient balance — tx rolls back via defer, no partial write
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ledger_debits (user_id, amount, job_id, written_at) VALUES ($1, $2, $3, $4)`,
		userID, amount, jobID, time.Now(),
	); err != nil {
		return false
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO account_balances (user_id, grant_total, earned_total, total_debited)
		 VALUES ($1, 0, 0, $2)
		 ON CONFLICT (user_id) DO UPDATE SET
		   total_debited = account_balances.total_debited + EXCLUDED.total_debited`,
		userID, amount,
	); err != nil {
		return false
	}
	if err := tx.Commit(); err != nil {
		return false
	}
	return true
}

// totalOutstandingGrantLiabilityPG is TotalOutstandingGrantLiability's
// Postgres path — one aggregate query instead of a full scan.
func (l *Ledger) totalOutstandingGrantLiabilityPG() float64 {
	ctx := context.Background()
	var liability sql.NullFloat64
	err := l.db.QueryRowContext(ctx,
		`SELECT SUM(GREATEST(grant_total - LEAST(total_debited, grant_total), 0)) FROM account_balances`,
	).Scan(&liability)
	if err != nil {
		return 0
	}
	return liability.Float64
}

// recordAdminActionPG is RecordAdminAction's Postgres path.
func (l *Ledger) recordAdminActionPG(action, detail string, amount float64, performedAt time.Time) error {
	_, err := l.db.ExecContext(context.Background(),
		`INSERT INTO admin_actions (action, detail, amount, performed_at) VALUES ($1, $2, $3, $4)`,
		action, detail, amount, performedAt,
	)
	if err != nil {
		return fmt.Errorf("persist admin action: %w", err)
	}
	return nil
}

// recentAdminActionsPG is RecentAdminActions's Postgres path. limit<=0 means
// "all", matching the in-memory backend's semantics — expressed as omitting
// the LIMIT clause rather than passing a sentinel value.
func (l *Ledger) recentAdminActionsPG(limit int) []AdminAction {
	ctx := context.Background()
	var rows *sql.Rows
	var err error
	if limit <= 0 {
		rows, err = l.db.QueryContext(ctx,
			`SELECT action, detail, amount, performed_at FROM admin_actions ORDER BY id DESC`)
	} else {
		rows, err = l.db.QueryContext(ctx,
			`SELECT action, detail, amount, performed_at FROM admin_actions ORDER BY id DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []AdminAction
	for rows.Next() {
		var a AdminAction
		if err := rows.Scan(&a.Action, &a.Detail, &a.Amount, &a.PerformedAt); err != nil {
			return out
		}
		out = append(out, a)
	}
	return out
}

// reconcilePG is Reconcile's Postgres path. Deliberately re-derives totals
// from the raw credit_entries/ledger_debits audit tables via GROUP BY,
// rather than from account_balances — Reconcile exists to catch the
// maintained table drifting from the log, so it must not trust that table.
func (l *Ledger) reconcilePG() ReconciliationReport {
	ctx := context.Background()

	type userTotals struct {
		grant, earned, debited float64
	}
	users := make(map[string]*userTotals)
	ensure := func(id string) *userTotals {
		t, ok := users[id]
		if !ok {
			t = &userTotals{}
			users[id] = t
		}
		return t
	}

	var report ReconciliationReport

	creditRows, err := l.db.QueryContext(ctx,
		`SELECT user_id,
		    SUM(CASE WHEN origin = 'startup_grant' THEN amount ELSE 0 END),
		    SUM(CASE WHEN origin IN ('earned', 'admin_adjustment') THEN amount ELSE 0 END)
		 FROM credit_entries GROUP BY user_id`)
	if err != nil {
		return report
	}
	func() {
		defer creditRows.Close()
		for creditRows.Next() {
			var userID string
			var grant, earned float64
			if err := creditRows.Scan(&userID, &grant, &earned); err != nil {
				return
			}
			t := ensure(userID)
			t.grant = grant
			t.earned = earned
			report.TotalGrantCredits += grant
			report.TotalEarnedCredits += earned
		}
	}()

	debitRows, err := l.db.QueryContext(ctx,
		`SELECT user_id, SUM(amount) FROM ledger_debits GROUP BY user_id`)
	if err != nil {
		return report
	}
	func() {
		defer debitRows.Close()
		for debitRows.Next() {
			var userID string
			var debited float64
			if err := debitRows.Scan(&userID, &debited); err != nil {
				return
			}
			ensure(userID).debited = debited
			report.TotalDebits += debited
		}
	}()

	report.TotalCredits = report.TotalGrantCredits + report.TotalEarnedCredits
	report.UserCount = len(users)

	for id, t := range users {
		credits := t.grant + t.earned
		balance := credits - t.debited
		if balance > 0 {
			report.TotalOutstanding += balance
		}
		if t.debited > credits+reconcileEpsilon {
			kind := AnomalyOverdraft
			if credits == 0 {
				kind = AnomalyOrphanDebit
			}
			report.Anomalies = append(report.Anomalies, LedgerAnomaly{
				UserID:      id,
				Kind:        kind,
				CreditTotal: credits,
				DebitTotal:  t.debited,
				Detail:      "debits exceed credits — spent more than was ever issued",
			})
		}
	}
	report.Consistent = len(report.Anomalies) == 0
	return report
}
