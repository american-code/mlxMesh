package coordinator

import (
	"strconv"
	"strings"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// This file is the server-side enforcement of the on-device-router fallback
// constraint (see the iOS Routing/ package). The design rule, enforced in code
// here and not merely documented:
//
//	The on-device router is an OPTIMIZATION HINT, never a requirement.
//	Every job must route correctly with a null, stale, or malicious hint.
//	The only thing a good hint changes is how much coordinator classification
//	work is skipped — never routing correctness, and never sensitivity downward.
//
// A plain JobSpec from a non-iOS client (no hint fields at all) is handled
// identically to a nil hint: full coordinator classification, normal routing.

// RouterHint mirrors the iOS OnDeviceRouter output. All fields are advisory.
type RouterHint struct {
	JobType         string  `json:"job_type"`
	SensitivityTier string  `json:"sensitivity_tier"`
	ModelFamily     string  `json:"model_family"`
	Confidence      float64 `json:"confidence"`
	RouterVersion   string  `json:"router_version"`
	IsFromMLModel   bool    `json:"is_from_ml_model"`
}

// CurrentRouterVersionNumber is the newest router the coordinator knows about.
// Hints from older routers are trusted less (their accuracy decays as the real
// job mix drifts away from what they were trained/tuned on). Bump when a new
// classifier ships.
const CurrentRouterVersionNumber = 1

// HintAccuracyStore supplies per-requester historical hint accuracy. Injected
// as an interface so weighting reads real history without coupling this package
// to the settlement ledger's concrete type. A nil store, or a requester with no
// history, yields weight 0 — i.e. the coordinator fully re-classifies, which is
// the safe default for anyone the network has never seen.
type HintAccuracyStore interface {
	// ClassificationAccuracy returns the requester's historical hint-vs-truth
	// agreement in [0,1] and whether any history exists at all.
	ClassificationAccuracy(requesterID string) (accuracy float64, hasHistory bool)
}

// ComputeHintWeight returns how much [0, 0.95] the coordinator should trust this
// hint. It NEVER returns 1.0 — the coordinator always does at least a
// lightweight sanity check. A nil hint (non-iOS client, disabled router)
// returns 0. All factors are multiplicative.
func ComputeHintWeight(requesterID string, hint *RouterHint, store HintAccuracyStore) float64 {
	if hint == nil {
		return 0 // no hint → coordinator owns classification entirely
	}

	// Factor 1 — requester history. A requester with no track record is not
	// trusted at all yet; the coordinator re-classifies and BUILDS the history
	// (see RecordHintAccuracy) so trust can grow over time.
	if store == nil {
		return 0
	}
	acc, hasHistory := store.ClassificationAccuracy(requesterID)
	if !hasHistory {
		return 0
	}
	// 50% accuracy → 0.3 weight, 90% accuracy → 0.8 weight, linear, clamped.
	histFactor := clamp01(1.25*acc - 0.325)

	// Factor 2 — router version freshness.
	verFactor := versionFactor(hint.RouterVersion)

	// Factor 3 — the classifier's own stated confidence. Below 0.5 the hint is
	// too uncertain to lean on at all → force full re-classification.
	if hint.Confidence < 0.5 {
		return 0
	}
	confFactor := clamp01(hint.Confidence) // 0.5→0.5 … 1.0→1.0

	// Factor 4 — ML models are trusted more than the coarse rule-based fallback.
	mlFactor := 1.0
	if !hint.IsFromMLModel {
		mlFactor = 0.7
	}

	w := histFactor * verFactor * confFactor * mlFactor
	if w > 0.95 {
		w = 0.95
	}
	return w
}

// versionFactor penalizes stale router versions. Current/newer → 1.0, one
// behind → 0.8, two+ behind → 0.4, unparseable/unknown → 0.2.
func versionFactor(version string) float64 {
	n, ok := trailingInt(version)
	if !ok {
		return 0.2
	}
	switch delta := CurrentRouterVersionNumber - n; {
	case delta <= 0:
		return 1.0
	case delta == 1:
		return 0.8
	default:
		return 0.4
	}
}

// trailingInt extracts the trailing integer of a router version string
// ("rule_based_v1" → 1, "core_ml_v2" → 2). Returns ok=false when there is no
// trailing digit run, which the caller treats as an unknown version.
func trailingInt(s string) (int, bool) {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	if i == len(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s[i:])
	if err != nil {
		return 0, false
	}
	return n, true
}

// tierRank orders sensitivity tiers so escalation can be compared. Unknown
// strings rank 0 (below everything), so a malformed hint/override can never
// out-rank a real coordinator classification.
func tierRank(t protocol.SensitivityTier) int {
	switch t {
	case protocol.SensitivityLow:
		return 1
	case protocol.SensitivityModerate:
		return 2
	case protocol.SensitivityHighRequiresAttestation:
		return 3
	default:
		return 0
	}
}

func maxTier(a, b protocol.SensitivityTier) protocol.SensitivityTier {
	if tierRank(a) >= tierRank(b) {
		return a
	}
	return b
}

// ValidateSensitivity returns the effective sensitivity tier for routing. It is
// IMPOSSIBLE to use to de-escalate below the coordinator's own classification:
// every return path yields a tier >= coordinatorTier.
//
//   - A user override escalates only: max(override, coordinator).
//   - The coordinator's classification wins over a lower hint.
//   - A higher hint is accepted only when the hint is well-trusted (weight>0.7),
//     since the user likely knows their own data is sensitive.
func ValidateSensitivity(
	hintedTier string,
	coordinatorTier protocol.SensitivityTier,
	sensitivityOverride string,
	hintWeight float64,
) protocol.SensitivityTier {
	// 1. Explicit user override — escalate-only via max().
	if sensitivityOverride != "" {
		return maxTier(protocol.SensitivityTier(sensitivityOverride), coordinatorTier)
	}
	// 2/3. A higher hint is honored only when well-trusted; otherwise (incl. any
	// lower hint) the coordinator's classification stands.
	hinted := protocol.SensitivityTier(hintedTier)
	if hintedTier != "" && tierRank(hinted) > tierRank(coordinatorTier) && hintWeight > 0.7 {
		return hinted
	}
	// 4. Default — coordinator wins. Guarantees result >= coordinatorTier.
	return coordinatorTier
}

// ShouldReclassify reports whether the coordinator must run its full independent
// classification pipeline. It fails safe: any doubt → true. It returns false
// (skip full pipeline, do only a lightweight sanity check) only when the hint is
// well-trusted AND the sensitivity is low/moderate AND the job type is a known,
// specific value.
func ShouldReclassify(hintPresent bool, hintWeight float64, hintedSensitivity, hintedJobType string) bool {
	if !hintPresent {
		return true // non-iOS / disabled router — coordinator classifies
	}
	if hintWeight < 0.3 {
		return true
	}
	// Never trust a client for attestation-gated routing.
	if hintedSensitivity == string(protocol.SensitivityHighRequiresAttestation) {
		return true
	}
	if hintedJobType == "" || hintedJobType == "unknown" {
		return true
	}
	if hintWeight >= 0.7 &&
		(hintedSensitivity == string(protocol.SensitivityLow) || hintedSensitivity == string(protocol.SensitivityModerate)) {
		return false
	}
	return true
}

// HintAccuracyRecorder receives post-job accuracy observations so future weights
// reflect a requester's real track record. Kept separate from HintAccuracyStore
// so read and write sides can be provided independently (e.g. the read side in
// routing hot-path, the write side called async after completion).
type HintAccuracyRecorder interface {
	RecordHintAccuracy(requesterID string, jobTypeMatch, sensitivityMatch bool)
}

// RecordHintAccuracy compares what the client hinted against what the
// coordinator actually classified and records the agreement. Call this
// asynchronously after a job completes — never block routing on it. A nil hint
// or nil recorder is a no-op.
func RecordHintAccuracy(
	recorder HintAccuracyRecorder,
	requesterID string,
	hint *RouterHint,
	coordinatorJobType string,
	coordinatorSensitivity protocol.SensitivityTier,
) {
	if recorder == nil || hint == nil {
		return
	}
	jobTypeMatch := strings.EqualFold(hint.JobType, coordinatorJobType)
	sensitivityMatch := strings.EqualFold(hint.SensitivityTier, string(coordinatorSensitivity))
	recorder.RecordHintAccuracy(requesterID, jobTypeMatch, sensitivityMatch)
}

// ResolveRouting is the single call the job-intake path makes: it composes hint
// weighting, the reclassify decision, and sensitivity validation.
//
// coordinatorTier is the coordinator's OWN classification of the job — never the
// client's claim. For a request with no hint and no override (every non-iOS
// client today), this returns effective == coordinatorTier and reclassified ==
// true, i.e. identical behavior to before hints existed. The effective tier is
// always >= coordinatorTier (de-escalation is impossible).
func ResolveRouting(
	requesterID string,
	hint *RouterHint,
	sensitivityOverride string,
	coordinatorTier protocol.SensitivityTier,
	store HintAccuracyStore,
) (effective protocol.SensitivityTier, reclassified bool, weight float64) {
	weight = ComputeHintWeight(requesterID, hint, store)
	var hintedSensitivity, hintedJobType string
	if hint != nil {
		hintedSensitivity = hint.SensitivityTier
		hintedJobType = hint.JobType
	}
	reclassified = ShouldReclassify(hint != nil, weight, hintedSensitivity, hintedJobType)
	effective = ValidateSensitivity(hintedSensitivity, coordinatorTier, sensitivityOverride, weight)
	return effective, reclassified, weight
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
