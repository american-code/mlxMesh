package settlement

import (
	"errors"
	"fmt"
	"time"
)

// ErrStartupGrantAlreadyClaimed is returned by IssueStartupGrant when userID
// already has a startup-grant entry in the ledger. Callers should treat this
// as "return the existing balance," not as a hard failure.
var ErrStartupGrantAlreadyClaimed = errors.New("startup grant already claimed for this user")

// CapacityThreshold is one step in the stepped grant decay function for a pod.
type CapacityThreshold struct {
	MinVerifiedCapacityScore float64
	GrantMultiplier          float64 // 1.0 = full base grant, 0.0 = no grant
}

// DEFAULT_DECAY_STEPS are the stepped decay thresholds.
// Core design decisions — do not silently change without re-deriving the reasoning (proposal §9.4):
// 1. Decay is keyed to VERIFIED capacity, never raw registration count.
// 2. Steps are discrete (not smooth/linear) so the threshold is publicly explainable.
// Tune against real Milestone 2 pilot throughput numbers before launch — these are placeholders.
var DEFAULT_DECAY_STEPS = []CapacityThreshold{
	{MinVerifiedCapacityScore: 0.0, GrantMultiplier: 1.0},
	{MinVerifiedCapacityScore: 500.0, GrantMultiplier: 0.5},
	{MinVerifiedCapacityScore: 2000.0, GrantMultiplier: 0.0},
}

// BASE_GRANT_AMOUNT is the base grant in protocol credit units.
// Tune to equal "enough to run N cycles of the SQL-metrics reference workload" before launch.
// Not an arbitrary number — the grant must be redeemable for something meaningful.
const BASE_GRANT_AMOUNT float64 = 100.0

// NodeCapacitySource provides verified capacity data for a pod.
// Implemented by the coordinator — settlement depends on this interface, not the concrete type.
// This decoupling lets the coordinator evolve its registry representation without changing settlement logic.
type NodeCapacitySource interface {
	VerifiedCapacityForPod(podID string) float64
}

// CurrentGrantMultiplier returns the applicable grant multiplier for the given verified capacity score.
// Steps are evaluated in ascending order of MinVerifiedCapacityScore;
// the last applicable step (highest met threshold) wins.
func CurrentGrantMultiplier(score float64, steps []CapacityThreshold) float64 {
	multiplier := 0.0
	for _, step := range steps {
		if score >= step.MinVerifiedCapacityScore {
			multiplier = step.GrantMultiplier
		}
	}
	return multiplier
}

// IssueStartupGrant issues a one-time startup grant sized by the pod's current verified capacity.
// Grant = BASE_GRANT_AMOUNT * CurrentGrantMultiplier(pod's verified capacity score).
// Dedup is enforced against the ledger itself (see Ledger.ClaimStartupGrantOnce) —
// returns ErrStartupGrantAlreadyClaimed if userID already has a startup-grant
// entry, so repeated claims (including across a coordinator restart, when the
// ledger is persistent) can never mint more than one grant per user (proposal §9.4).
func IssueStartupGrant(ledger *Ledger, userID, assignedPodID string, src NodeCapacitySource, steps []CapacityThreshold) (CreditEntry, error) {
	if userID == "" {
		return CreditEntry{}, fmt.Errorf("user_id is required")
	}
	score := src.VerifiedCapacityForPod(assignedPodID)
	multiplier := CurrentGrantMultiplier(score, steps)
	entry := CreditEntry{
		UserID:            userID,
		Origin:            CreditOriginStartupGrant,
		Amount:            BASE_GRANT_AMOUNT * multiplier,
		GrantedOrEarnedAt: time.Now(),
		SourceReference:   "startup_grant:" + assignedPodID,
	}
	claimed, err := ledger.ClaimStartupGrantOnce(entry)
	if err != nil {
		return CreditEntry{}, fmt.Errorf("credit account: %w", err)
	}
	if !claimed {
		return CreditEntry{}, ErrStartupGrantAlreadyClaimed
	}
	return entry, nil
}
