package coordinator

import (
	"testing"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

func TestSelectProbeTarget_EmptyCandidates(t *testing.T) {
	_, _, ok := SelectProbeTarget(nil)
	if ok {
		t.Fatal("expected ok=false for empty candidates")
	}
}

func TestSelectProbeTarget_NoModelsOnChosenNode(t *testing.T) {
	candidates := []protocol.CapabilityManifest{{NodeID: "node-a", Models: nil}}
	_, _, ok := SelectProbeTarget(candidates)
	if ok {
		t.Fatal("expected ok=false when the chosen candidate advertises no models")
	}
}

func TestSelectProbeTarget_PicksFirstCandidateAndItsOwnModel(t *testing.T) {
	candidates := []protocol.CapabilityManifest{
		{
			NodeID: "node-a",
			Models: []protocol.ModelCapability{
				{ModelID: "llama-3.2-3b", Quantization: "4bit", Loaded: true},
				{ModelID: "qwen-7b", Quantization: "8bit", Loaded: true},
			},
		},
		{NodeID: "node-b", Models: []protocol.ModelCapability{{ModelID: "other-model", Loaded: true}}},
	}
	manifest, model, ok := SelectProbeTarget(candidates)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if manifest.NodeID != "node-a" {
		t.Fatalf("expected node-a (first candidate), got %s", manifest.NodeID)
	}
	if model.ModelID != "llama-3.2-3b" {
		t.Fatalf("expected the node's own first advertised model, got %s", model.ModelID)
	}
}

// A downloaded-but-cold model must never be picked: DispatchToResolvedNode
// (unlike DispatchFastLane) bypasses eligibility scoring and dispatches
// straight to whatever it's given, so probing a cold model would fail the
// probe and incorrectly mark an otherwise-healthy, reachable node unreachable.
func TestSelectProbeTarget_SkipsColdModelPicksLoadedOne(t *testing.T) {
	candidates := []protocol.CapabilityManifest{
		{
			NodeID: "node-a",
			Models: []protocol.ModelCapability{
				{ModelID: "cold-model", Loaded: false},
				{ModelID: "warm-model", Loaded: true},
			},
		},
	}
	manifest, model, ok := SelectProbeTarget(candidates)
	if !ok {
		t.Fatal("expected ok=true (node has a loaded model, just not its first one)")
	}
	if manifest.NodeID != "node-a" || model.ModelID != "warm-model" {
		t.Fatalf("expected node-a/warm-model, got %s/%s", manifest.NodeID, model.ModelID)
	}
}

// A node with models but none of them loaded must be skipped entirely rather
// than probed — it genuinely isn't available for anything right now.
func TestSelectProbeTarget_NoLoadedModelsOnChosenNode(t *testing.T) {
	candidates := []protocol.CapabilityManifest{
		{NodeID: "node-a", Models: []protocol.ModelCapability{{ModelID: "cold-model", Loaded: false}}},
	}
	_, _, ok := SelectProbeTarget(candidates)
	if ok {
		t.Fatal("expected ok=false when the chosen candidate has no loaded models")
	}
}

func TestBuildProbeJob_ValidatesAsFastLane(t *testing.T) {
	job, messages := BuildProbeJob("probe-job-1", "llama-3.2-3b", "4bit")

	if job.Lane != protocol.JobLaneFast {
		t.Fatalf("expected fast lane (avoids the RecurrenceSpec requirement), got %s", job.Lane)
	}
	if job.Sensitivity != protocol.SensitivityLow {
		t.Fatalf("expected lowest sensitivity tier, got %s", job.Sensitivity)
	}
	if job.ModelID != "llama-3.2-3b" || job.QuantizationRequired != "4bit" {
		t.Fatalf("expected model/quantization to be threaded through, got %+v", job)
	}
	if err := job.Validate(); err != nil {
		t.Fatalf("expected a valid JobSpec, got error: %v", err)
	}
	if len(messages) != 1 || messages[0]["role"] != "user" {
		t.Fatalf("expected a single user message, got %+v", messages)
	}
}

func TestBuildProbeJob_EmptyQuantizationOmitted(t *testing.T) {
	job, _ := BuildProbeJob("probe-job-2", "llama-3.2-3b", "")
	if job.QuantizationRequired != "" {
		t.Fatalf("expected no quantization requirement when none given, got %q", job.QuantizationRequired)
	}
}

// ScaledProbeBudget — the bootstrapping-economics fix (TODO.md, Economic
// Sustainability): more idle nodes probed per round the quieter the network.
func TestScaledProbeBudget_FullyIdleReturnsCeiling(t *testing.T) {
	if b := ScaledProbeBudget(0, 40, 3, 15); b != 15 {
		t.Fatalf("at 0%% backpressure want the ceiling (15), got %d", b)
	}
}

func TestScaledProbeBudget_AtOrAboveCeilingReturnsFloor(t *testing.T) {
	if b := ScaledProbeBudget(40, 40, 3, 15); b != 3 {
		t.Fatalf("at the backpressure ceiling want the floor (3), got %d", b)
	}
	if b := ScaledProbeBudget(90, 40, 3, 15); b != 3 {
		t.Fatalf("above the backpressure ceiling want the floor (3), got %d", b)
	}
}

func TestScaledProbeBudget_TapersBetweenFloorAndCeiling(t *testing.T) {
	b := ScaledProbeBudget(20, 40, 3, 15) // halfway between 0 and the 40% ceiling
	if b <= 3 || b >= 15 {
		t.Fatalf("expected a budget strictly between floor and ceiling at 50%% of the way there, got %d", b)
	}
}

func TestScaledProbeBudget_MonotonicNonIncreasingInBackpressure(t *testing.T) {
	prev := ScaledProbeBudget(0, 40, 3, 15)
	for bp := 1.0; bp <= 40; bp++ {
		b := ScaledProbeBudget(bp, 40, 3, 15)
		if b > prev {
			t.Fatalf("budget rose from %d to %d as backpressure increased to %.0f — must be non-increasing", prev, b, bp)
		}
		prev = b
	}
}

// Degenerate params fall back to floor rather than misbehaving (e.g.
// dividing by a zero/negative ceiling, or a caller passing ceiling <= floor).
func TestScaledProbeBudget_DegenerateParamsFallBackToFloor(t *testing.T) {
	if b := ScaledProbeBudget(10, 0, 3, 15); b != 3 {
		t.Fatalf("backpressureCeiling<=0 should fall back to floor (3), got %d", b)
	}
	if b := ScaledProbeBudget(10, 40, 5, 5); b != 5 {
		t.Fatalf("ceiling==floor should fall back to floor (5), got %d", b)
	}
	if b := ScaledProbeBudget(10, 40, 5, 3); b != 5 {
		t.Fatalf("ceiling<floor should fall back to floor (5), got %d", b)
	}
}
