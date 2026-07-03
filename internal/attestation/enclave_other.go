//go:build !darwin

package attestation

// Signer is a no-op stub on non-Darwin platforms — there is no Secure
// Enclave to attest with. Nodes running here simply never call
// /nodes/{id}/attest-enclave and remain ineligible for
// SensitivityHighRequiresAttestation jobs, same as before this feature
// existed.
type Signer struct{}

// NewSigner returns a stub Signer. Every method returns ErrUnsupported.
func NewSigner() *Signer { return &Signer{} }

func (s *Signer) PublicKey() ([]byte, error) { return nil, ErrUnsupported }

func (s *Signer) Sign(msg []byte) ([]byte, error) { return nil, ErrUnsupported }
