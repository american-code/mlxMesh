// Package directory implements the librarian layer — global discovery only.
// This layer must never see job payloads or sit in the inference data path (proposal §7.1).
// Node agents and pod coordinators depend on the Resolver interface, never on a
// specific implementation — this makes staged evolution (centralized→federated→DHT)
// possible without breaking existing clients.
package directory

import (
	"errors"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

var ErrNotImplemented = errors.New("milestone 4: not implemented")

// Resolver is the pluggable directory abstraction (proposal §7.3).
type Resolver interface {
	ResolvePodsForModel(modelID, quantization string) ([]string, error)
	RegisterPod(digest protocol.PodHealthDigest) error
}

// CentralizedResolver is the bootstrap-stage implementation.
// Queries one or two known operator-run directory endpoints.
// Client-side caching of last-known-good results is REQUIRED so callers
// degrade gracefully if both endpoints are briefly unreachable.
//
// MILESTONE 4 — not implemented yet.
type CentralizedResolver struct {
	Endpoints []string
}

func (r *CentralizedResolver) ResolvePodsForModel(modelID, quantization string) ([]string, error) {
	return nil, ErrNotImplemented
}
func (r *CentralizedResolver) RegisterPod(digest protocol.PodHealthDigest) error {
	return ErrNotImplemented
}

// FederatedResolver is the stage-2 implementation.
// Reputation-gated, periodically-rotated community-run directory instances.
// Do NOT implement until CentralizedResolver is validated in production.
//
// MILESTONE 7 — not implemented yet.
type FederatedResolver struct{}

func (r *FederatedResolver) ResolvePodsForModel(modelID, quantization string) ([]string, error) {
	return nil, errors.New("milestone 7: not implemented")
}
func (r *FederatedResolver) RegisterPod(digest protocol.PodHealthDigest) error {
	return errors.New("milestone 7: not implemented")
}

// DHTResolver is the stage-3 fully-decentralized Kademlia-style implementation.
// Out of scope for initial build — carries real Sybil/eclipse risk (proposal §7.4).
// Revisit only after stage 2 has operating history and a dedicated threat-modeling pass.
type DHTResolver struct{}

func (r *DHTResolver) ResolvePodsForModel(modelID, quantization string) ([]string, error) {
	return nil, errors.New("out of scope: DHT resolver requires dedicated threat-modeling pass (proposal §7.3/7.4)")
}
func (r *DHTResolver) RegisterPod(digest protocol.PodHealthDigest) error {
	return errors.New("out of scope: DHT resolver requires dedicated threat-modeling pass (proposal §7.3/7.4)")
}
