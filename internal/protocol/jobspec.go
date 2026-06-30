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
	return nil
}
