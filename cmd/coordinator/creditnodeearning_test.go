// Copyright (C) 2024 Open Inference Mesh
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"sync"
	"testing"

	"github.com/open-inference-mesh/oim/internal/coordinator"
	"github.com/open-inference-mesh/oim/internal/economics"
	"github.com/open-inference-mesh/oim/internal/protocol"
	"github.com/open-inference-mesh/oim/internal/settlement"
	"github.com/open-inference-mesh/oim/internal/wallet"
)

// registerCreditTestNode registers a real, non-simulated node with one model
// and returns its node ID — enough for creditNodeEarning's registry.Manifest
// lookup (the Simulated-exclusion check) to succeed.
func registerCreditTestNode(t *testing.T, r *coordinator.NodeRegistry, simulated bool) string {
	t.Helper()
	priv, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	nodeID := protocol.NodeIDFromPubKey(pub)
	manifest := protocol.CapabilityManifest{
		NodeID:    nodeID,
		Models:    []protocol.ModelCapability{{ModelID: "test-model", Quantization: "4bit"}},
		Simulated: simulated,
	}
	payload, err := manifest.Bytes()
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	sig, err := protocol.SignPayload(priv, payload)
	if err != nil {
		t.Fatalf("sign manifest: %v", err)
	}
	ok, err := r.Register(protocol.NodeRegistration{Manifest: manifest, PublicKey: pub, Signature: sig})
	if err != nil || !ok {
		t.Fatalf("register test node: ok=%v err=%v", ok, err)
	}
	return nodeID
}

// creditNodeEarning's solvency invariant (the bug this test guards): reward is
// always exactly economics.ProviderReward off the UNDISCOUNTED matrix cost —
// unaffected by any activity discount — while margin is derived from
// consumerCharge directly (consumerCharge - reward), so reward+margin always
// equals consumerCharge exactly, even when consumerCharge is less than the
// undiscounted economics.ConsumerCost (bootstrapping-economics fix, TODO.md
// Economic Sustainability). Regression test for a real bug caught during
// that work: crediting margin from the undiscounted cost while debiting the
// consumer at a discounted one would mint credit the consumer never paid for.
func TestCreditNodeEarning_MarginDerivedFromConsumerChargeNotFullCost(t *testing.T) {
	registry := coordinator.NewNodeRegistry()
	nodeID := registerCreditTestNode(t, registry, false)
	ledger := settlement.NewLedger()
	walletMgr := wallet.NewManager()
	var nodeUsers sync.Map

	fullCost := economics.ConsumerCost(economics.LaneFast, "moderate", 1000)
	reward := economics.ProviderReward(economics.LaneFast, "moderate", 1000)
	// A discounted charge partway between the floor (== reward, zero margin)
	// and the full cost (== today's ~25% margin).
	discountedCharge := (fullCost + reward) / 2

	creditNodeEarning(ledger, walletMgr, &nodeUsers, registry, nodeID, "job-1", economics.LaneFast, "moderate", 1000, discountedCharge)

	gotReward := ledger.GetBalance(nodeID).Total
	gotMargin := ledger.GetBalance(economics.TreasuryAccount).Total
	if gotReward != reward {
		t.Fatalf("node credited %.4f, want the undiscounted ProviderReward %.4f (must not move with the discount)", gotReward, reward)
	}
	if got := gotReward + gotMargin; got != discountedCharge {
		t.Fatalf("solvency broken: reward+margin=%.4f != consumerCharge=%.4f", got, discountedCharge)
	}
	if gotMargin >= fullCost-reward {
		t.Fatalf("margin %.4f should be compressed below the undiscounted margin %.4f", gotMargin, fullCost-reward)
	}
}

// At the discount floor (consumerCharge == reward exactly — a fully idle
// network), the treasury must receive zero margin, never negative.
func TestCreditNodeEarning_ZeroMarginWhenChargeEqualsReward(t *testing.T) {
	registry := coordinator.NewNodeRegistry()
	nodeID := registerCreditTestNode(t, registry, false)
	ledger := settlement.NewLedger()
	walletMgr := wallet.NewManager()
	var nodeUsers sync.Map

	reward := economics.ProviderReward(economics.LaneFast, "moderate", 1000)
	creditNodeEarning(ledger, walletMgr, &nodeUsers, registry, nodeID, "job-1", economics.LaneFast, "moderate", 1000, reward)

	if got := ledger.GetBalance(economics.TreasuryAccount).Total; got != 0 {
		t.Fatalf("treasury margin = %.4f, want exactly 0 at the discount floor", got)
	}
	if got := ledger.GetBalance(nodeID).Total; got != reward {
		t.Fatalf("node credited %.4f, want %.4f", got, reward)
	}
}

// A caller passing a consumerCharge below reward (shouldn't happen given
// economics.ConsumerCostWithActivityDiscount's own floor, but this function
// takes the value as a plain parameter) must never mint a negative margin.
func TestCreditNodeEarning_NegativeMarginClampedToZero(t *testing.T) {
	registry := coordinator.NewNodeRegistry()
	nodeID := registerCreditTestNode(t, registry, false)
	ledger := settlement.NewLedger()
	walletMgr := wallet.NewManager()
	var nodeUsers sync.Map

	reward := economics.ProviderReward(economics.LaneFast, "moderate", 1000)
	creditNodeEarning(ledger, walletMgr, &nodeUsers, registry, nodeID, "job-1", economics.LaneFast, "moderate", 1000, reward/2)

	if got := ledger.GetBalance(economics.TreasuryAccount).Total; got != 0 {
		t.Fatalf("treasury margin = %.4f, want clamped to 0, never negative", got)
	}
}

// Pre-existing behavior, unaffected by this change: a Simulated node's
// dispatch never mints anything at all, regardless of consumerCharge.
func TestCreditNodeEarning_SimulatedNodeStillNotCredited(t *testing.T) {
	registry := coordinator.NewNodeRegistry()
	nodeID := registerCreditTestNode(t, registry, true)
	ledger := settlement.NewLedger()
	walletMgr := wallet.NewManager()
	var nodeUsers sync.Map

	fullCost := economics.ConsumerCost(economics.LaneFast, "moderate", 1000)
	creditNodeEarning(ledger, walletMgr, &nodeUsers, registry, nodeID, "job-1", economics.LaneFast, "moderate", 1000, fullCost)

	if got := ledger.GetBalance(nodeID).Total; got != 0 {
		t.Fatalf("simulated node credited %.4f, want 0", got)
	}
	if got := ledger.GetBalance(economics.TreasuryAccount).Total; got != 0 {
		t.Fatalf("treasury credited %.4f for a simulated node's job, want 0", got)
	}
}
