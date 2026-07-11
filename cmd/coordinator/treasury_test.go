package main

import (
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/coordinator"
	"github.com/open-inference-mesh/oim/internal/economics"
	"github.com/open-inference-mesh/oim/internal/metrics"
	"github.com/open-inference-mesh/oim/internal/protocol"
	"github.com/open-inference-mesh/oim/internal/settlement"
	"github.com/open-inference-mesh/oim/internal/wallet"
)

// linkTestDevice links a fresh, real device node ID to a fresh, real account
// address via a genuine signature — mirrors tests/wallet_test.go's
// TestTwoDevicesConsolidateToOneAccount, needed here because
// creditPointerHost only reaches the treasury floor check for a device that
// resolves to a linked wallet account.
func linkTestDevice(t *testing.T, walletMgr *wallet.Manager) (deviceID, accountAddr string) {
	t.Helper()
	accPriv, accPub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate account key: %v", err)
	}
	_, devPub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate device key: %v", err)
	}
	addr := wallet.AddressFromPubKey(accPub)
	devID := protocol.NodeIDFromPubKey(devPub)
	linkSig, err := protocol.SignPayload(accPriv, []byte("oim-account-link:"+addr+":"+devID))
	if err != nil {
		t.Fatalf("sign link: %v", err)
	}
	if err := walletMgr.LinkDevice(addr, devID, accPub, linkSig, time.Now()); err != nil {
		t.Fatalf("link device: %v", err)
	}
	return devID, addr
}

func TestCreditPointerHost_TreasurySufficientDebitsNormally(t *testing.T) {
	ledger := settlement.NewLedger()
	coordReg := coordinator.NewCoordinationRegistry()
	walletMgr := wallet.NewManager()
	mx := metrics.New()
	device, addr := linkTestDevice(t, walletMgr)

	_ = ledger.CreditAccount(settlement.CreditEntry{
		UserID: economics.TreasuryAccount, Origin: settlement.CreditOriginEarnedContrib,
		Amount: 100, GrantedOrEarnedAt: time.Now(), SourceReference: "seed",
	})
	coordReg.Announce(coordinator.CoordinationParticipant{DeviceID: device}, time.Now())

	creditPointerHost(ledger, coordReg, walletMgr, mx, device, "job-1")

	reward := economics.CoordinationReward(1)
	if got := ledger.GetBalance(addr).Total; got != reward {
		t.Fatalf("participant balance = %.4f, want %.4f", got, reward)
	}
	if got := ledger.GetBalance(economics.TreasuryAccount).Total; got != 100-reward {
		t.Fatalf("treasury balance = %.4f, want %.4f", got, 100-reward)
	}
	if got := mx.Counter(`oim_coordination_reward_skipped_total{reason="treasury_insufficient"}`).Value(); got != 0 {
		t.Fatalf("expected the skip counter untouched when treasury has funds, got %d", got)
	}
}

// TestCreditPointerHost_TreasuryInsufficientSkipsDebitAndIncrementsCounter is
// the regression test for the exact gap TODO.md's "Treasury balance
// monitoring" item names: the floor check silently protected the treasury
// from overdraft but gave no signal when it actually started refusing to
// pay. The participant must still be credited (existing behavior,
// unchanged) — only the treasury-side debit and its accompanying
// observability signal are new/asserted here.
func TestCreditPointerHost_TreasuryInsufficientSkipsDebitAndIncrementsCounter(t *testing.T) {
	ledger := settlement.NewLedger() // treasury starts at 0 — insufficient by construction
	coordReg := coordinator.NewCoordinationRegistry()
	walletMgr := wallet.NewManager()
	mx := metrics.New()
	device, addr := linkTestDevice(t, walletMgr)
	coordReg.Announce(coordinator.CoordinationParticipant{DeviceID: device}, time.Now())

	creditPointerHost(ledger, coordReg, walletMgr, mx, device, "job-1")

	reward := economics.CoordinationReward(1)
	if got := ledger.GetBalance(addr).Total; got != reward {
		t.Fatalf("participant should still be credited even when the treasury can't pay, got %.4f want %.4f", got, reward)
	}
	if got := ledger.GetBalance(economics.TreasuryAccount).Total; got != 0 {
		t.Fatalf("treasury must not go negative — expected balance still 0, got %.4f", got)
	}
	if got := mx.Counter(`oim_coordination_reward_skipped_total{reason="treasury_insufficient"}`).Value(); got != 1 {
		t.Fatalf("expected the skip counter incremented exactly once, got %d", got)
	}
}

func TestCreditPointerHost_UnknownHostNeverTouchesCounter(t *testing.T) {
	ledger := settlement.NewLedger()
	coordReg := coordinator.NewCoordinationRegistry()
	walletMgr := wallet.NewManager()
	mx := metrics.New()

	creditPointerHost(ledger, coordReg, walletMgr, mx, "never-registered-device", "job-1")

	if got := mx.Counter(`oim_coordination_reward_skipped_total{reason="treasury_insufficient"}`).Value(); got != 0 {
		t.Fatalf("an unlinked/unknown host must never reach the treasury floor check, got skip count %d", got)
	}
}
