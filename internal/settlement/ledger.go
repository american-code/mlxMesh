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

// Package settlement implements off-protocol payment accounting (proposal §9 and §10).
// No fund custody anywhere in this package — it produces verified records, never moves money.
// Settlement and payment are deliberately separate concerns (proposal §10).
package settlement

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registers itself as "sqlite" — no CGO needed
)

// CreditOrigin distinguishes subsidy from earned contribution.
// This split is load-bearing — it answers "is the network running on real
// contribution yet, or still mostly on bootstrap grants?" (proposal §9.4).
type CreditOrigin string

const (
	CreditOriginStartupGrant  CreditOrigin = "startup_grant"
	CreditOriginEarnedContrib CreditOrigin = "earned"
	// CreditOriginAdminAdjustment marks a manual treasury credit injected by
	// an authenticated admin action (see internal/adminauth, cmd/coordinator
	// POST /admin/treasury/credit) — real, operator-authorized value, so it
	// tallies into the "earned" bucket everywhere below (GetBalance,
	// DebitAccount, Reconcile), never the Sybil-resistant per-user startup
	// grant bucket, which specifically means "one free grant a new real user
	// claimed," not an operator top-up.
	CreditOriginAdminAdjustment CreditOrigin = "admin_adjustment"
	// CreditOriginEarnedReferral is reserved — do NOT implement until a dedicated
	// growth-incentive design pass is done (Helium-shaped risk, proposal §11).
)

// CreditEntry is one append-only ledger record.
type CreditEntry struct {
	UserID            string       `json:"user_id"`
	Origin            CreditOrigin `json:"origin"`
	Amount            float64      `json:"amount"`
	GrantedOrEarnedAt time.Time    `json:"granted_or_earned_at"`
	SourceReference   string       `json:"source_reference"` // settlement record id or grant batch id
}

// Balance holds a user's credit split by origin.
// Grant and earned balances must never be collapsed into one number —
// the dashboard needs to show them separately (proposal Milestone 5a).
type Balance struct {
	GrantBalance  float64 `json:"grant_balance"`
	EarnedBalance float64 `json:"earned_balance"`
	Total         float64 `json:"total"`
}

type ledgerDebit struct {
	UserID    string
	Amount    float64
	JobID     string
	WrittenAt time.Time
}

// ledgerBackend discriminates how a Ledger enforces correctness. Memory and
// SQLite share the exact same code path below (a single in-process mutex
// guards both) — only Postgres dispatches to a different implementation,
// in ledger_postgres.go, where correctness is enforced by the database
// itself (transactions, row locks, unique constraints) so that N coordinator
// processes can safely share one Postgres instance.
type ledgerBackend int

const (
	backendMemory ledgerBackend = iota
	backendSQLite
	backendPostgres
)

// balanceFromTotals applies the grant-first debit ordering shared by every
// backend: grant credits are always spent before earned credits.
func balanceFromTotals(grantTotal, earnedTotal, totalDebited float64) Balance {
	grantUsed := min(totalDebited, grantTotal)
	earnedUsed := max(0.0, totalDebited-grantTotal)
	grantBal := max(0.0, grantTotal-grantUsed)
	earnedBal := max(0.0, earnedTotal-earnedUsed)
	return Balance{
		GrantBalance:  grantBal,
		EarnedBalance: earnedBal,
		Total:         grantBal + earnedBal,
	}
}

// outstandingGrantLiability is one user's unspent startup-grant balance,
// grant-first debit ordering applied — shared by every backend.
func outstandingGrantLiability(grantTotal, totalDebited float64) float64 {
	grantUsed := min(totalDebited, grantTotal)
	return max(0.0, grantTotal-grantUsed)
}

// AdminAction is one entry in the admin audit trail — a record of the
// administrative act itself (who did what, when, why), distinct from and
// in addition to whatever ledger effect it had. A treasury credit
// injection writes BOTH a CreditEntry (the ledger effect, via
// CreditAccount) and an AdminAction (the audit record, via
// RecordAdminAction) — the former alone would leave no queryable trail of
// "an admin did this, with this stated reason," since credit_entries has
// no actor/reason concept.
type AdminAction struct {
	Action      string    `json:"action"`
	Detail      string    `json:"detail"` // free-text reason, e.g. the "reason field" TODO.md asks for
	Amount      float64   `json:"amount"`
	PerformedAt time.Time `json:"performed_at"`
}

// Ledger is a thread-safe, append-only credit ledger split by origin.
// Credits are written via CreditAccount; debits are recorded separately
// so the credit history is never mutated.
//
// db is nil for a pure in-memory ledger (tests, or a coordinator run without
// --db-path) — behavior is identical to before persistence was added. When db
// is set (via NewPersistentLedger), every write is also committed to SQLite
// before the in-memory slices are updated, so balances and grant history
// survive a coordinator restart instead of silently resetting to zero.
type Ledger struct {
	mu           sync.RWMutex
	entries      []CreditEntry
	debits       []ledgerDebit
	adminActions []AdminAction
	db           *sql.DB
	backend      ledgerBackend
	onCredit     func(CreditEntry) // optional; see SetOnCredit
}

// SetOnCredit registers a callback fired (outside the ledger's lock) after
// every successful credit, whether via CreditAccount or
// ClaimStartupGrantOnce. Used to witness this pod's own credit issuance into
// a signed, federatable history (internal/federation, task #52/M7) without
// the settlement package needing to know that federation exists at all.
func (l *Ledger) SetOnCredit(fn func(CreditEntry)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onCredit = fn
}

func (l *Ledger) notifyCredit(entry CreditEntry) {
	l.mu.RLock()
	fn := l.onCredit
	l.mu.RUnlock()
	if fn != nil {
		fn(entry)
	}
}

func NewLedger() *Ledger { return &Ledger{} }

// NewPersistentLedger opens (creating if needed) a SQLite-backed ledger at
// dbPath and replays its history into memory. Losing the ledger on every
// restart was the single biggest "not production-ready" gap after the
// unsigned-write-endpoint holes — this closes it without changing the
// Ledger's public method surface, so existing callers need no changes.
func NewPersistentLedger(dbPath string) (*Ledger, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open ledger db %s: %w", dbPath, err)
	}
	if err := migrateLedgerSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	l := &Ledger{db: db, backend: backendSQLite}
	if err := l.loadFromDB(); err != nil {
		db.Close()
		return nil, err
	}
	return l, nil
}

func migrateLedgerSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS credit_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			origin TEXT NOT NULL,
			amount REAL NOT NULL,
			granted_or_earned_at TEXT NOT NULL,
			source_reference TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS ledger_debits (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			amount REAL NOT NULL,
			job_id TEXT NOT NULL,
			written_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS admin_actions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action TEXT NOT NULL,
			detail TEXT NOT NULL,
			amount REAL NOT NULL,
			performed_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_credit_entries_user ON credit_entries(user_id);
		CREATE INDEX IF NOT EXISTS idx_ledger_debits_user ON ledger_debits(user_id);
	`)
	if err != nil {
		return fmt.Errorf("migrate ledger schema: %w", err)
	}
	return nil
}

// loadFromDB replays the full persisted history into memory on startup.
// Balances are computed from these in-memory slices exactly as before —
// this function's only job is to make sure they start non-empty.
func (l *Ledger) loadFromDB() error {
	rows, err := l.db.Query(`SELECT user_id, origin, amount, granted_or_earned_at, source_reference FROM credit_entries ORDER BY id`)
	if err != nil {
		return fmt.Errorf("load credit entries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e CreditEntry
		var granted string
		if err := rows.Scan(&e.UserID, &e.Origin, &e.Amount, &granted, &e.SourceReference); err != nil {
			return fmt.Errorf("scan credit entry: %w", err)
		}
		e.GrantedOrEarnedAt, _ = time.Parse(time.RFC3339Nano, granted)
		l.entries = append(l.entries, e)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate credit entries: %w", err)
	}

	debitRows, err := l.db.Query(`SELECT user_id, amount, job_id, written_at FROM ledger_debits ORDER BY id`)
	if err != nil {
		return fmt.Errorf("load debits: %w", err)
	}
	defer debitRows.Close()
	for debitRows.Next() {
		var d ledgerDebit
		var written string
		if err := debitRows.Scan(&d.UserID, &d.Amount, &d.JobID, &written); err != nil {
			return fmt.Errorf("scan debit: %w", err)
		}
		d.WrittenAt, _ = time.Parse(time.RFC3339Nano, written)
		l.debits = append(l.debits, d)
	}
	if err := debitRows.Err(); err != nil {
		return fmt.Errorf("iterate debits: %w", err)
	}

	actionRows, err := l.db.Query(`SELECT action, detail, amount, performed_at FROM admin_actions ORDER BY id`)
	if err != nil {
		return fmt.Errorf("load admin actions: %w", err)
	}
	defer actionRows.Close()
	for actionRows.Next() {
		var a AdminAction
		var performed string
		if err := actionRows.Scan(&a.Action, &a.Detail, &a.Amount, &performed); err != nil {
			return fmt.Errorf("scan admin action: %w", err)
		}
		a.PerformedAt, _ = time.Parse(time.RFC3339Nano, performed)
		l.adminActions = append(l.adminActions, a)
	}
	return actionRows.Err()
}

// CreditAccount appends an earned or grant credit.
// Append-only — existing entries are never modified (proposal §9.4).
// When backed by SQLite, the row is committed to disk before this returns —
// callers can treat a nil error as "durably recorded," not just "in memory."
func (l *Ledger) CreditAccount(entry CreditEntry) error {
	if l.backend == backendPostgres {
		err := l.creditAccountPG(entry)
		if err == nil {
			l.notifyCredit(entry)
		}
		return err
	}
	err := func() error {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.creditLocked(entry)
	}()
	if err == nil {
		l.notifyCredit(entry)
	}
	return err
}

// creditLocked performs the actual write (memory + optional SQLite) and must
// only be called with l.mu already held — factored out so ClaimStartupGrantOnce
// can do its check-then-credit atomically under one lock without deadlocking
// on CreditAccount's own lock acquisition.
func (l *Ledger) creditLocked(entry CreditEntry) error {
	if l.db != nil {
		_, err := l.db.Exec(
			`INSERT INTO credit_entries (user_id, origin, amount, granted_or_earned_at, source_reference) VALUES (?, ?, ?, ?, ?)`,
			entry.UserID, entry.Origin, entry.Amount, entry.GrantedOrEarnedAt.Format(time.RFC3339Nano), entry.SourceReference,
		)
		if err != nil {
			return fmt.Errorf("persist credit entry: %w", err)
		}
	}
	l.entries = append(l.entries, entry)
	return nil
}

// ClaimStartupGrantOnce atomically checks whether entry.UserID already has a
// startup-grant credit entry and, if not, credits entry and returns
// claimed=true. Returns claimed=false (no credit written) if a startup-grant
// entry already exists for this user.
//
// This replaces a separate in-memory "claimed users" set that used to guard
// this check — that set reset on every coordinator restart, meaning anyone
// could re-claim a startup grant simply by waiting for (or forcing) a
// coordinator bounce. Checking the ledger itself is the durable, restart-safe
// fix, and doing the check-and-credit under this method's single lock
// acquisition prevents two concurrent claims for the same user from both
// succeeding (proposal §9.4).
func (l *Ledger) ClaimStartupGrantOnce(entry CreditEntry) (claimed bool, err error) {
	if l.backend == backendPostgres {
		claimed, err = l.claimStartupGrantOncePG(entry)
		if claimed {
			l.notifyCredit(entry)
		}
		return claimed, err
	}
	claimed, err = func() (bool, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		for _, e := range l.entries {
			if e.UserID == entry.UserID && e.Origin == CreditOriginStartupGrant {
				return false, nil
			}
		}
		if err := l.creditLocked(entry); err != nil {
			return false, err
		}
		return true, nil
	}()
	if claimed {
		l.notifyCredit(entry)
	}
	return claimed, err
}

// GetBalance returns the user's credit split by origin.
// Grant balance is consumed before earned balance during debits.
func (l *Ledger) GetBalance(userID string) Balance {
	if l.backend == backendPostgres {
		return l.getBalancePG(userID)
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	var grantTotal, earnedTotal, totalDebited float64
	for _, e := range l.entries {
		if e.UserID != userID {
			continue
		}
		switch e.Origin {
		case CreditOriginStartupGrant:
			grantTotal += e.Amount
		case CreditOriginEarnedContrib, CreditOriginAdminAdjustment:
			earnedTotal += e.Amount
		}
	}
	for _, d := range l.debits {
		if d.UserID == userID {
			totalDebited += d.Amount
		}
	}

	return balanceFromTotals(grantTotal, earnedTotal, totalDebited)
}

// DebitAccount spends credits against a submitted job.
// Debits grant_balance before earned_balance.
// Returns false on insufficient balance — callers must reject the job, not queue it.
func (l *Ledger) DebitAccount(userID string, amount float64, jobID string) bool {
	if l.backend == backendPostgres {
		return l.debitAccountPG(userID, amount, jobID)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	var grantTotal, earnedTotal, totalDebited float64
	for _, e := range l.entries {
		if e.UserID != userID {
			continue
		}
		switch e.Origin {
		case CreditOriginStartupGrant:
			grantTotal += e.Amount
		case CreditOriginEarnedContrib, CreditOriginAdminAdjustment:
			earnedTotal += e.Amount
		}
	}
	for _, d := range l.debits {
		if d.UserID == userID {
			totalDebited += d.Amount
		}
	}

	if amount > grantTotal+earnedTotal-totalDebited {
		return false
	}
	debit := ledgerDebit{
		UserID:    userID,
		Amount:    amount,
		JobID:     jobID,
		WrittenAt: time.Now(),
	}
	if l.db != nil {
		// Fail closed: if the debit can't be durably recorded, refuse the spend
		// rather than let the in-memory balance drift ahead of what a restart
		// would recover — an unpersisted debit is a free-money bug waiting to happen.
		_, err := l.db.Exec(
			`INSERT INTO ledger_debits (user_id, amount, job_id, written_at) VALUES (?, ?, ?, ?)`,
			debit.UserID, debit.Amount, debit.JobID, debit.WrittenAt.Format(time.RFC3339Nano),
		)
		if err != nil {
			return false
		}
	}
	l.debits = append(l.debits, debit)
	return true
}

// TotalOutstandingGrantLiability returns the sum of all unspent startup-grant credits
// across all users. This is the network's current subsidy exposure — should decrease
// as verified capacity grows and grants decay (proposal §9.4).
func (l *Ledger) TotalOutstandingGrantLiability() float64 {
	if l.backend == backendPostgres {
		return l.totalOutstandingGrantLiabilityPG()
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	type userState struct {
		grantTotal   float64
		totalDebited float64
	}
	users := map[string]*userState{}
	ensure := func(id string) *userState {
		if _, ok := users[id]; !ok {
			users[id] = &userState{}
		}
		return users[id]
	}

	for _, e := range l.entries {
		if e.Origin == CreditOriginStartupGrant {
			ensure(e.UserID).grantTotal += e.Amount
		}
	}
	for _, d := range l.debits {
		ensure(d.UserID).totalDebited += d.Amount
	}

	var liability float64
	for _, s := range users {
		liability += outstandingGrantLiability(s.grantTotal, s.totalDebited)
	}
	return liability
}

// RecordAdminAction appends an audit-trail entry for an administrative act
// (e.g. a manual treasury credit injection) — separate from and in addition
// to whatever CreditEntry/debit that act produced, since neither of those
// carry an actor/reason concept. Append-only, same durability contract as
// CreditAccount: when backed by SQLite, the row is committed before this
// returns.
func (l *Ledger) RecordAdminAction(action, detail string, amount float64, performedAt time.Time) error {
	if l.backend == backendPostgres {
		// No in-memory mirror here: RecentAdminActions reads straight from
		// Postgres for this backend, since a local slice would only ever
		// reflect this process's own writes, not the other coordinators
		// sharing the same database.
		return l.recordAdminActionPG(action, detail, amount, performedAt)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db != nil {
		_, err := l.db.Exec(
			`INSERT INTO admin_actions (action, detail, amount, performed_at) VALUES (?, ?, ?, ?)`,
			action, detail, amount, performedAt.Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("persist admin action: %w", err)
		}
	}
	l.adminActions = append(l.adminActions, AdminAction{
		Action:      action,
		Detail:      detail,
		Amount:      amount,
		PerformedAt: performedAt,
	})
	return nil
}

// RecentAdminActions returns up to limit most-recent admin actions, newest
// first.
func (l *Ledger) RecentAdminActions(limit int) []AdminAction {
	if l.backend == backendPostgres {
		return l.recentAdminActionsPG(limit)
	}

	l.mu.RLock()
	defer l.mu.RUnlock()
	if limit <= 0 || limit > len(l.adminActions) {
		limit = len(l.adminActions)
	}
	out := make([]AdminAction, limit)
	for i := 0; i < limit; i++ {
		out[i] = l.adminActions[len(l.adminActions)-1-i]
	}
	return out
}
