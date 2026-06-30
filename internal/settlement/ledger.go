// Package settlement implements off-protocol payment accounting (proposal §9 and §10).
// No fund custody anywhere in this package — it produces verified records, never moves money.
// Settlement and payment are deliberately separate concerns (proposal §10).
package settlement

import (
	"sync"
	"time"
)

// CreditOrigin distinguishes subsidy from earned contribution.
// This split is load-bearing — it answers "is the network running on real
// contribution yet, or still mostly on bootstrap grants?" (proposal §9.4).
type CreditOrigin string

const (
	CreditOriginStartupGrant  CreditOrigin = "startup_grant"
	CreditOriginEarnedContrib CreditOrigin = "earned"
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

// Ledger is a thread-safe, append-only credit ledger split by origin.
// Credits are written via CreditAccount; debits are recorded separately
// so the credit history is never mutated.
type Ledger struct {
	mu      sync.RWMutex
	entries []CreditEntry
	debits  []ledgerDebit
}

func NewLedger() *Ledger { return &Ledger{} }

// CreditAccount appends an earned or grant credit.
// Append-only — existing entries are never modified (proposal §9.4).
func (l *Ledger) CreditAccount(entry CreditEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
	return nil
}

// GetBalance returns the user's credit split by origin.
// Grant balance is consumed before earned balance during debits.
func (l *Ledger) GetBalance(userID string) Balance {
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
		case CreditOriginEarnedContrib:
			earnedTotal += e.Amount
		}
	}
	for _, d := range l.debits {
		if d.UserID == userID {
			totalDebited += d.Amount
		}
	}

	// Grant is debited first; the remainder rolls into earned.
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

// DebitAccount spends credits against a submitted job.
// Debits grant_balance before earned_balance.
// Returns false on insufficient balance — callers must reject the job, not queue it.
func (l *Ledger) DebitAccount(userID string, amount float64, jobID string) bool {
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
		case CreditOriginEarnedContrib:
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
	l.debits = append(l.debits, ledgerDebit{
		UserID:    userID,
		Amount:    amount,
		JobID:     jobID,
		WrittenAt: time.Now(),
	})
	return true
}

// TotalOutstandingGrantLiability returns the sum of all unspent startup-grant credits
// across all users. This is the network's current subsidy exposure — should decrease
// as verified capacity grows and grants decay (proposal §9.4).
func (l *Ledger) TotalOutstandingGrantLiability() float64 {
	l.mu.RLock()
	defer l.mu.RUnlock()

	type userState struct {
		grantTotal  float64
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
		grantUsed := min(s.totalDebited, s.grantTotal)
		liability += max(0.0, s.grantTotal-grantUsed)
	}
	return liability
}
