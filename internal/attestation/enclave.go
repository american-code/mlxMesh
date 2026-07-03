// Package attestation generates and signs with a Secure Enclave-backed P-256
// key so a node can cryptographically prove it possesses real Secure Enclave
// hardware — closing the gap left by the self-declared
// CapabilityManifest.HasSecureEnclave boolean (Fable security review:
// self-declared attestation, unenforced privacy claims). Darwin builds use
// real Security.framework Secure Enclave keys (enclave_darwin.go); every
// other platform gets a stub that always returns ErrUnsupported
// (enclave_other.go).
package attestation

import "errors"

// ErrUnsupported is returned by Signer methods when this platform (or this
// specific machine) has no usable Secure Enclave.
var ErrUnsupported = errors.New("secure enclave attestation not supported on this platform")
