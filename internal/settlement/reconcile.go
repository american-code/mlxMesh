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

// Ledger reconciliation & audit trail. DebitAccount already refuses an
// overdraft atomically at spend time, so a healthy ledger can never contain
// one — which is exactly why a periodic reconciliation is worth running: it is
// the tripwire that catches a state where that guarantee was somehow violated
// (a bug in a new code path, a partial/corrupt SQLite recovery, a manual DB
// edit). "The numbers reconcile" should be provable on demand, not assumed.

// AnomalyKind classifies a reconciliation finding.
type AnomalyKind string

const (
	// AnomalyOverdraft: a user's total debits exceed their total credits — the
	// balance is negative, meaning credits were spent that were never issued.
	// This is the free-money invariant violation and the most serious finding.
	AnomalyOverdraft AnomalyKind = "overdraft"
	// AnomalyOrphanDebit: a debit exists for a user_id that has no credit
	// entries at all. Distinct from overdraft (which needs at least some
	// credit) — points at a debit written against a never-funded account.
	AnomalyOrphanDebit AnomalyKind = "orphan_debit"
)

// LedgerAnomaly is one reconciliation finding for a single user.
type LedgerAnomaly struct {
	UserID      string      `json:"user_id"`
	Kind        AnomalyKind `json:"kind"`
	CreditTotal float64     `json:"credit_total"`
	DebitTotal  float64     `json:"debit_total"`
	Detail      string      `json:"detail"`
}

// ReconciliationReport is the audit summary of the whole ledger at one instant.
// Consistent is the single bit an operator or an alert should key on; the
// totals are the books, and Anomalies enumerates any user whose account
// violates the credits >= debits invariant.
type ReconciliationReport struct {
	Consistent         bool            `json:"consistent"`
	UserCount          int             `json:"user_count"`
	TotalGrantCredits  float64         `json:"total_grant_credits"`
	TotalEarnedCredits float64         `json:"total_earned_credits"`
	TotalCredits       float64         `json:"total_credits"`
	TotalDebits        float64         `json:"total_debits"`
	TotalOutstanding   float64         `json:"total_outstanding"` // sum of every user's non-negative balance
	Anomalies          []LedgerAnomaly `json:"anomalies"`
}

// reconcileEpsilon absorbs float rounding across many credit/debit adds so a
// balance that is negative only by a sub-cent floating-point artifact is not
// reported as an overdraft. Well below the smallest real charge.
const reconcileEpsilon = 1e-6

type reconcileState struct {
	grant   float64
	earned  float64
	debited float64
}

// Reconcile scans the entire ledger once and returns an audit report. It never
// mutates state. Runs under the read lock, so it is safe to call concurrently
// with live credits/debits — the report reflects a consistent snapshot.
func (l *Ledger) Reconcile() ReconciliationReport {
	if l.backend == backendPostgres {
		return l.reconcilePG()
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	users := make(map[string]*reconcileState)
	ensure := func(id string) *reconcileState {
		s, ok := users[id]
		if !ok {
			s = &reconcileState{}
			users[id] = s
		}
		return s
	}

	var report ReconciliationReport
	for _, e := range l.entries {
		s := ensure(e.UserID)
		switch e.Origin {
		case CreditOriginStartupGrant:
			s.grant += e.Amount
			report.TotalGrantCredits += e.Amount
		case CreditOriginEarnedContrib, CreditOriginAdminAdjustment:
			s.earned += e.Amount
			report.TotalEarnedCredits += e.Amount
		}
	}
	for _, d := range l.debits {
		ensure(d.UserID).debited += d.Amount
		report.TotalDebits += d.Amount
	}

	report.TotalCredits = report.TotalGrantCredits + report.TotalEarnedCredits
	report.UserCount = len(users)

	for id, s := range users {
		credits := s.grant + s.earned
		balance := credits - s.debited
		if balance > 0 {
			report.TotalOutstanding += balance
		}
		if s.debited > credits+reconcileEpsilon {
			kind := AnomalyOverdraft
			if credits == 0 {
				kind = AnomalyOrphanDebit
			}
			report.Anomalies = append(report.Anomalies, LedgerAnomaly{
				UserID:      id,
				Kind:        kind,
				CreditTotal: credits,
				DebitTotal:  s.debited,
				Detail:      "debits exceed credits — spent more than was ever issued",
			})
		}
	}
	report.Consistent = len(report.Anomalies) == 0
	return report
}
