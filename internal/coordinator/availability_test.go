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
				{ModelID: "llama-3.2-3b", Quantization: "4bit"},
				{ModelID: "qwen-7b", Quantization: "8bit"},
			},
		},
		{NodeID: "node-b", Models: []protocol.ModelCapability{{ModelID: "other-model"}}},
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
