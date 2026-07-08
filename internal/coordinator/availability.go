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
// advertised models. Deliberately reuses the node's own self-reported model
// list rather than a guessed/global model name — this is the same
// eligibility signal real routing already trusts (registry.Candidates),
// so a probe never asks a node for something it never claimed to serve.
// Returns ok=false if candidates is empty or the chosen node advertises no
// models (the latter shouldn't happen given IdleCandidates already filters
// on it, but this function must stay safe called with any slice).
func SelectProbeTarget(candidates []protocol.CapabilityManifest) (protocol.CapabilityManifest, protocol.ModelCapability, bool) {
	if len(candidates) == 0 {
		return protocol.CapabilityManifest{}, protocol.ModelCapability{}, false
	}
	chosen := candidates[0]
	if len(chosen.Models) == 0 {
		return protocol.CapabilityManifest{}, protocol.ModelCapability{}, false
	}
	return chosen, chosen.Models[0], true
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
