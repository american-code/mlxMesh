package tests

import (
	"testing"

	"github.com/open-inference-mesh/oim/internal/coordinator"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// fakeAccuracyStore lets tests inject a requester's historical hint accuracy.
type fakeAccuracyStore struct {
	acc     float64
	history bool
}

func (f fakeAccuracyStore) ClassificationAccuracy(string) (float64, bool) {
	return f.acc, f.history
}

// --- ComputeHintWeight ---

func TestHintWeightNilHintIsZero(t *testing.T) {
	if w := coordinator.ComputeHintWeight("u", nil, fakeAccuracyStore{0.9, true}); w != 0 {
		t.Errorf("nil hint must weigh 0, got %v", w)
	}
}

func TestHintWeightNewRequesterIsZero(t *testing.T) {
	hint := &coordinator.RouterHint{JobType: "summarization", Confidence: 0.9, RouterVersion: "rule_based_v1", IsFromMLModel: true}
	// No history → coordinator fully re-classifies and builds trust from scratch.
	if w := coordinator.ComputeHintWeight("new-user", hint, fakeAccuracyStore{0, false}); w != 0 {
		t.Errorf("new requester must weigh 0, got %v", w)
	}
}

func TestHintWeightLowConfidenceIsZero(t *testing.T) {
	hint := &coordinator.RouterHint{JobType: "summarization", Confidence: 0.4, RouterVersion: "rule_based_v1", IsFromMLModel: true}
	if w := coordinator.ComputeHintWeight("u", hint, fakeAccuracyStore{0.9, true}); w != 0 {
		t.Errorf("confidence < 0.5 must weigh 0, got %v", w)
	}
}

func TestHintWeightNeverReachesOne(t *testing.T) {
	// Best possible hint: perfect accuracy, current version, full confidence, ML.
	hint := &coordinator.RouterHint{JobType: "summarization", Confidence: 1.0, RouterVersion: "rule_based_v1", IsFromMLModel: true}
	w := coordinator.ComputeHintWeight("u", hint, fakeAccuracyStore{1.0, true})
	if w >= 1.0 {
		t.Errorf("weight must always stay below 1.0 (coordinator always sanity-checks), got %v", w)
	}
	if w <= 0 {
		t.Errorf("a strong hint should carry positive weight, got %v", w)
	}
}

func TestHintWeightRuleBasedPenalized(t *testing.T) {
	base := fakeAccuracyStore{0.9, true}
	ml := &coordinator.RouterHint{JobType: "x", Confidence: 1.0, RouterVersion: "rule_based_v1", IsFromMLModel: true}
	rule := &coordinator.RouterHint{JobType: "x", Confidence: 1.0, RouterVersion: "rule_based_v1", IsFromMLModel: false}
	if coordinator.ComputeHintWeight("u", rule, base) >= coordinator.ComputeHintWeight("u", ml, base) {
		t.Error("rule-based hint should weigh less than ML hint")
	}
}

func TestHintWeightStaleVersionPenalized(t *testing.T) {
	base := fakeAccuracyStore{0.9, true}
	// CurrentRouterVersionNumber is 1, so "v0" would be one behind; use an
	// unknown-format version to exercise the unknown penalty path.
	cur := &coordinator.RouterHint{JobType: "x", Confidence: 1.0, RouterVersion: "rule_based_v1", IsFromMLModel: true}
	unknown := &coordinator.RouterHint{JobType: "x", Confidence: 1.0, RouterVersion: "mystery", IsFromMLModel: true}
	if coordinator.ComputeHintWeight("u", unknown, base) >= coordinator.ComputeHintWeight("u", cur, base) {
		t.Error("unknown router version should weigh less than the current version")
	}
}

// --- ValidateSensitivity: de-escalation must be impossible ---

func TestSensitivityCannotDeEscalateViaHint(t *testing.T) {
	// Coordinator says HIGH; a malicious client hints LOW with max weight.
	got := coordinator.ValidateSensitivity("low", protocol.SensitivityHighRequiresAttestation, "", 0.95)
	if got != protocol.SensitivityHighRequiresAttestation {
		t.Errorf("client must not de-escalate below coordinator tier; got %v", got)
	}
}

func TestSensitivityCannotDeEscalateViaOverride(t *testing.T) {
	// User "override" to LOW while coordinator classified MODERATE — max() wins.
	got := coordinator.ValidateSensitivity("", protocol.SensitivityModerate, "low", 0.95)
	if got != protocol.SensitivityModerate {
		t.Errorf("override must be escalate-only; got %v", got)
	}
}

func TestSensitivityOverrideEscalates(t *testing.T) {
	got := coordinator.ValidateSensitivity("", protocol.SensitivityLow, "high_requires_attestation", 0.0)
	if got != protocol.SensitivityHighRequiresAttestation {
		t.Errorf("override should escalate to high; got %v", got)
	}
}

func TestSensitivityHigherHintAcceptedOnlyWhenTrusted(t *testing.T) {
	// Coordinator LOW, hint MODERATE.
	trusted := coordinator.ValidateSensitivity("moderate", protocol.SensitivityLow, "", 0.8)
	if trusted != protocol.SensitivityModerate {
		t.Errorf("well-trusted higher hint should be honored; got %v", trusted)
	}
	untrusted := coordinator.ValidateSensitivity("moderate", protocol.SensitivityLow, "", 0.5)
	if untrusted != protocol.SensitivityLow {
		t.Errorf("low-trust hint must not raise tier; coordinator wins; got %v", untrusted)
	}
}

func TestSensitivityGarbageOverrideNeverDeEscalates(t *testing.T) {
	// An unparseable override ranks 0; must not drop below coordinator tier.
	got := coordinator.ValidateSensitivity("", protocol.SensitivityModerate, "garbage", 0.95)
	if got != protocol.SensitivityModerate {
		t.Errorf("garbage override must not de-escalate; got %v", got)
	}
}

// --- ShouldReclassify: fail-safe fallback ---

func TestReclassifyWhenNoHint(t *testing.T) {
	if !coordinator.ShouldReclassify(false, 0.9, "low", "summarization") {
		t.Error("absent hint (non-iOS/disabled) must force full re-classification")
	}
}

func TestReclassifyWhenAttestationTier(t *testing.T) {
	// Never trust the client for attestation-gated jobs, even at max weight.
	if !coordinator.ShouldReclassify(true, 0.95, "high_requires_attestation", "classification") {
		t.Error("attestation-tier hint must always force re-classification")
	}
}

func TestReclassifyWhenLowWeightOrUnknownType(t *testing.T) {
	if !coordinator.ShouldReclassify(true, 0.2, "low", "summarization") {
		t.Error("weight < 0.3 must force re-classification")
	}
	if !coordinator.ShouldReclassify(true, 0.9, "low", "unknown") {
		t.Error("unknown job type must force re-classification")
	}
	if !coordinator.ShouldReclassify(true, 0.9, "low", "") {
		t.Error("empty job type must force re-classification")
	}
}

func TestReclassifySkippedOnlyWhenStrongAndSafe(t *testing.T) {
	if coordinator.ShouldReclassify(true, 0.8, "low", "summarization") {
		t.Error("strong hint + low sensitivity + known type should skip full pipeline")
	}
	if coordinator.ShouldReclassify(true, 0.75, "moderate", "query_optimization") {
		t.Error("strong hint + moderate sensitivity + known type should skip full pipeline")
	}
}

// --- RecordHintAccuracy ---

type capturingRecorder struct {
	called                  bool
	jobTypeMatch, sensMatch bool
	requesterID             string
}

func (c *capturingRecorder) RecordHintAccuracy(id string, jt, s bool) {
	c.called, c.requesterID, c.jobTypeMatch, c.sensMatch = true, id, jt, s
}

func TestRecordHintAccuracyComputesMatches(t *testing.T) {
	rec := &capturingRecorder{}
	hint := &coordinator.RouterHint{JobType: "summarization", SensitivityTier: "low"}
	coordinator.RecordHintAccuracy(rec, "u", hint, "summarization", protocol.SensitivityModerate)
	if !rec.called || !rec.jobTypeMatch || rec.sensMatch {
		t.Errorf("expected jobType match, sensitivity mismatch; got called=%v jt=%v sens=%v", rec.called, rec.jobTypeMatch, rec.sensMatch)
	}
}

func TestRecordHintAccuracyNilSafe(t *testing.T) {
	// Must not panic on nil recorder or nil hint.
	coordinator.RecordHintAccuracy(nil, "u", &coordinator.RouterHint{}, "x", protocol.SensitivityLow)
	rec := &capturingRecorder{}
	coordinator.RecordHintAccuracy(rec, "u", nil, "x", protocol.SensitivityLow)
	if rec.called {
		t.Error("nil hint should be a no-op")
	}
}

// --- ResolveRouting: the intake integration point ---

func TestResolveRoutingHintlessRoutesIdentically(t *testing.T) {
	// A non-iOS client (nil hint, no override) must resolve to exactly the
	// coordinator's own tier and be flagged for full classification — i.e. no
	// behavior change from before hints existed.
	for _, tier := range []protocol.SensitivityTier{
		protocol.SensitivityLow, protocol.SensitivityModerate, protocol.SensitivityHighRequiresAttestation,
	} {
		eff, reclassified, weight := coordinator.ResolveRouting("anon", nil, "", tier, nil)
		if eff != tier {
			t.Errorf("hintless request must route on coordinator tier %v, got %v", tier, eff)
		}
		if !reclassified {
			t.Errorf("hintless request must force re-classification (tier %v)", tier)
		}
		if weight != 0 {
			t.Errorf("hintless request must weigh 0, got %v", weight)
		}
	}
}

func TestResolveRoutingOverrideEscalatesOnly(t *testing.T) {
	// Malicious low override against a moderate coordinator tier → moderate.
	eff, _, _ := coordinator.ResolveRouting("u", nil, "low", protocol.SensitivityModerate, nil)
	if eff != protocol.SensitivityModerate {
		t.Errorf("override must not de-escalate; got %v", eff)
	}
	// Legit escalation low→high.
	eff2, _, _ := coordinator.ResolveRouting("u", nil, "high_requires_attestation", protocol.SensitivityLow, nil)
	if eff2 != protocol.SensitivityHighRequiresAttestation {
		t.Errorf("override should escalate; got %v", eff2)
	}
}
