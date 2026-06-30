// Package settlement implements off-protocol payment accounting (proposal §9 and §10).
// No fund custody anywhere in this package — it produces verified records, never moves money.
// Settlement and payment are deliberately separate concerns (proposal §10).
//
// MILESTONE 5 — not implemented yet.
package settlement

import (
	"errors"
	"time"
)

var ErrNotImplemented = errors.New("milestone 5: not implemented")

// ResourceLine is one decomposed contribution line.
// Never blend these into one unit — the split is load-bearing (proposal §9.2).
type ResourceLine struct {
	ResourceType    string  `json:"resource_type"`    // "memory_hours" | "compute_cycles" | "bandwidth_relayed"
	MeasuredAmount  float64 `json:"measured_amount"`
	DeliveredAmount float64 `json:"delivered_amount"` // post-verification, post-overhead
	UnitPrice       float64 `json:"unit_price"`
}

// CreditOrigin distinguishes subsidy from earned contribution.
// This split is load-bearing — it answers "is the network running on real
// contribution yet, or still mostly on bootstrap grants?" (proposal §9.4).
type CreditOrigin string

const (
	CreditOriginStartupGrant    CreditOrigin = "startup_grant"
	CreditOriginEarnedContrib   CreditOrigin = "earned"
	// CreditOriginEarnedReferral is reserved — do NOT implement until a
	// dedicated growth-incentive design pass is done (Helium-shaped risk).
)

// CreditEntry is one ledger record.
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

// GetBalance returns a user's balance split by origin. MILESTONE 5.
func GetBalance(userID string) (Balance, error) { return Balance{}, ErrNotImplemented }

// CreditAccount is an append-only ledger write. MILESTONE 5.
func CreditAccount(entry CreditEntry) error { return ErrNotImplemented }

// DebitAccount spends credits against a submitted job.
// Debits grant_balance before earned_balance.
// Returns false on insufficient balance — callers must reject the job, not queue it.
// MILESTONE 5.
func DebitAccount(userID string, amount float64, jobID string) (bool, error) {
	return false, ErrNotImplemented
}

// ComputeShrinkage returns the gap between measured and delivered contribution.
// Report this explicitly — never silently absorb it (proposal §9.2).
func ComputeShrinkage(measured, delivered float64) float64 {
	return measured - delivered
}

// BuildDivisionOrder assembles the multi-line settlement record for one job/cycle.
// MILESTONE 5.
func BuildDivisionOrder(jobID, nodeID string, lines []ResourceLine) (map[string]any, error) {
	return nil, ErrNotImplemented
}

// CreateSettlementRecord signs the division order plus verification outcome.
// A record with verificationResult=false must still be created and published —
// it's evidence for dispute resolution. Silently dropping failed verifications
// erases the audit trail (proposal §10). MILESTONE 5.
func CreateSettlementRecord(divisionOrder map[string]any, verificationResult bool, signerPrivateKey []byte) (map[string]any, error) {
	return nil, ErrNotImplemented
}

// PaymentPointer declares where a node operator wants to be paid.
// The protocol never custodies funds — this only carries WHERE, not HOW.
type PaymentPointer struct {
	RailType           string `json:"rail_type"`            // "stablecoin" | "fiat_invoice" | "other"
	AddressOrReference string `json:"address_or_reference"`
}

// ValidatePaymentPointer does format/sanity validation only.
// Must NOT initiate any transaction — that would mean touching money. MILESTONE 5.
func ValidatePaymentPointer(p PaymentPointer) (bool, error) { return false, ErrNotImplemented }
