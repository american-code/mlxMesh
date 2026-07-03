package protocol

import "fmt"

// SensitivityTier drives routing eligibility (proposal §8.1).
// Never add a fourth tier that implies guarantees this system cannot make.
type SensitivityTier string

const (
	SensitivityLow                     SensitivityTier = "low"
	SensitivityModerate                SensitivityTier = "moderate"
	SensitivityHighRequiresAttestation SensitivityTier = "high_requires_attestation"
)

// RecurrenceSpec defines background-lane cadence.
type RecurrenceSpec struct {
	IntervalSeconds  int `json:"interval_seconds"`
	MaxJitterSeconds int `json:"max_jitter_seconds"`
}

// JobSpec flows from requester → pod coordinator → node agent → back.
// PayloadRef is a pointer to the encrypted payload; the raw payload never
// appears in this struct or travels through the coordinator/directory.
type JobSpec struct {
	JobID                string          `json:"job_id"`
	RequesterID          string          `json:"requester_id"`
	ModelID              string          `json:"model_id"`
	QuantizationRequired string          `json:"quantization_required,omitempty"`
	Lane                 JobLane         `json:"lane"`
	Sensitivity          SensitivityTier `json:"sensitivity"`
	MaxPricePerUnit      float64         `json:"max_price_per_unit"`
	Recurrence           *RecurrenceSpec `json:"recurrence,omitempty"`
	RedundancyDepth      int             `json:"redundancy_depth"`
	PayloadRef           string          `json:"payload_ref"`
	// PayloadFetchURL / PayloadEphemeralPubKey complete the encrypted-pointer
	// path: the assigned node fetches ciphertext from PayloadFetchURL and derives
	// the ECDH shared secret from PayloadEphemeralPubKey. The coordinator only
	// passes these through — it never fetches the payload or holds a key. Empty
	// for legacy/plaintext requests.
	PayloadFetchURL        string `json:"payload_fetch_url,omitempty"`
	PayloadEphemeralPubKey string `json:"payload_ephemeral_pubkey,omitempty"`

	// Parallel processing controls — all opt-in, all default to zero/false.
	// Existing single-node job specs require no changes.
	// None of these fields are valid on fast-lane jobs; Validate() enforces this.
	AllowDecomposition         bool `json:"allow_decomposition,omitempty"`
	AllowDocumentSplitting     bool `json:"allow_document_splitting,omitempty"`
	RequireDeterministicOutput bool `json:"require_deterministic_output,omitempty"` // enforces temperature=0; required for parallel checksum verification
	MaxParallelNodes           int  `json:"max_parallel_nodes,omitempty"`            // ceiling on concurrent node usage; 0 treated as 1
}

// Validate rejects specs that violate the dual-lane contract.
func (s *JobSpec) Validate() error {
	if s.Lane == JobLaneFast && s.Recurrence != nil {
		return fmt.Errorf("fast-lane job cannot have a RecurrenceSpec")
	}
	if s.Lane == JobLaneBackground && s.Recurrence == nil {
		return fmt.Errorf("background-lane job requires a RecurrenceSpec")
	}
	if s.Sensitivity == SensitivityHighRequiresAttestation && s.RedundancyDepth < 1 {
		return fmt.Errorf(
			"HIGH_REQUIRES_ATTESTATION job requires redundancy_depth >= 1: " +
				"a single attestation-capable node failure must not silently drop a sensitive job",
		)
	}
	// Parallel processing is strictly background-lane only.
	// Autoregressive generation cannot be parallelised mid-sequence — token N
	// depends on all prior tokens, so document splitting produces incoherent
	// half-contexts. Decomposition at the coordinator level has the same problem
	// for interactive generation. This is a hard architectural constraint, not a
	// policy decision.
	if s.Lane == JobLaneFast && (s.AllowDecomposition || s.AllowDocumentSplitting) {
		return fmt.Errorf(
			"fast-lane job %q cannot enable decomposition or document splitting: "+
				"autoregressive generation cannot be parallelised mid-sequence",
			s.JobID,
		)
	}
	return nil
}
