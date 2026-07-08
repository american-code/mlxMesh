package coordinator

import "github.com/open-inference-mesh/oim/internal/protocol"

// availabilityProbePrompt is deliberately tiny and generic — the point of the
// probe is proving the node can genuinely produce a real completion, not
// exercising any particular content. A short, fixed prompt keeps the reward
// naturally cheap (economics.ProviderReward scales with observed output
// tokens) without needing a separate pricing knob for this path.
const availabilityProbePrompt = "Reply with a single word to confirm you're online."

// SelectProbeTarget picks the first (already oldest-idle-sorted, per
// NodeRegistry.IdleCandidates) node from candidates and one of its own
// advertised, actively-loaded models. Deliberately reuses the node's own
// self-reported model list rather than a guessed/global model name — this is
// the same eligibility signal real routing already trusts (registry.Candidates),
// so a probe never asks a node for something it never claimed to serve.
//
// Only a LOADED model is picked — DispatchToResolvedNode (unlike
// DispatchFastLane's rankCandidates) bypasses eligibility scoring entirely
// and dispatches straight to whatever job/target it's given, so probing a
// downloaded-but-cold model would fail the probe and call
// registry.MarkUnreachable on an otherwise perfectly healthy, reachable node —
// a false negative caused entirely by which of its models happened to be
// picked. Returns ok=false if candidates is empty, the chosen node advertises
// no models, or none of its models are currently loaded (an idle node with
// nothing warm genuinely isn't available for anything right now, so it
// correctly earns no availability reward this round rather than being
// falsely marked unreachable).
func SelectProbeTarget(candidates []protocol.CapabilityManifest) (protocol.CapabilityManifest, protocol.ModelCapability, bool) {
	if len(candidates) == 0 {
		return protocol.CapabilityManifest{}, protocol.ModelCapability{}, false
	}
	chosen := candidates[0]
	for _, model := range chosen.Models {
		if model.Loaded {
			return chosen, model, true
		}
	}
	return protocol.CapabilityManifest{}, protocol.ModelCapability{}, false
}

// BuildProbeJob constructs the minimal JobSpec + chat messages for one
// availability-reward probe. Fast lane avoids the RecurrenceSpec
// JobSpec.Validate() requires for background-lane specs — the *economics*
// lane used to price the reward is a separate parameter passed directly to
// economics.ProviderReward by the caller, not derived from this JobSpec, so
// dispatching fast-lane here has no effect on how cheaply the probe pays.
// Sensitivity is always the lowest tier: a synthetic probe carries no real
// payload and needs no attestation guarantees.
func BuildProbeJob(jobID, modelID, quantization string) (protocol.JobSpec, []map[string]any) {
	job := protocol.JobSpec{
		JobID:           jobID,
		RequesterID:     "oim-availability-prober",
		ModelID:         modelID,
		Lane:            protocol.JobLaneFast,
		Sensitivity:     protocol.SensitivityLow,
		MaxPricePerUnit: 0,
	}
	if quantization != "" {
		job.QuantizationRequired = quantization
	}
	messages := []map[string]any{
		{"role": "user", "content": availabilityProbePrompt},
	}
	return job, messages
}
