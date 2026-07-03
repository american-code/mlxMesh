package tests

import (
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
	"github.com/open-inference-mesh/oim/internal/wallet"
)

func newAccount(t *testing.T) (priv, pub []byte, address string) {
	t.Helper()
	priv, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate account key: %v", err)
	}
	return priv, pub, wallet.AddressFromPubKey(pub)
}

func sign(t *testing.T, priv, msg []byte) []byte {
	t.Helper()
	sig, err := protocol.SignPayload(priv, msg)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return sig
}

func TestAddressIsDeterministicAndUnique(t *testing.T) {
	_, pubA, addrA := newAccount(t)
	if again := wallet.AddressFromPubKey(pubA); again != addrA {
		t.Errorf("address not deterministic: %q vs %q", addrA, again)
	}
	_, _, addrB := newAccount(t)
	if addrA == addrB {
		t.Error("distinct keys produced the same address")
	}
}

// TestChallengeAuthHappyPath is the core prove-ownership flow.
func TestChallengeAuthHappyPath(t *testing.T) {
	m := wallet.NewManager()
	priv, pub, addr := newAccount(t)
	now := time.Unix(1_800_000_000, 0)

	ch, err := m.IssueChallenge(addr, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	sig := sign(t, priv, ch.SigningMessage())
	if err := m.VerifyChallenge(addr, ch.Nonce, pub, sig, now.Add(time.Second)); err != nil {
		t.Fatalf("verify should succeed: %v", err)
	}
}

// TestRecoveryOnNewDevice models "computer breaks, buy a new one, re-import the
// account key": the same account key, on a fresh Manager session, authenticates
// to the SAME address — so the 10k-credit ledger row (keyed by that address) is
// immediately theirs again. The credits never lived on the broken device.
func TestRecoveryOnNewDevice(t *testing.T) {
	priv, pub, addr := newAccount(t)
	now := time.Unix(1_800_000_000, 0)

	// "New device" = fresh manager, same account key re-imported.
	m := wallet.NewManager()
	ch, _ := m.IssueChallenge(addr, now)
	sig := sign(t, priv, ch.SigningMessage())
	if err := m.VerifyChallenge(addr, ch.Nonce, pub, sig, now); err != nil {
		t.Fatalf("recovered key must authenticate to the same address: %v", err)
	}
	if wallet.AddressFromPubKey(pub) != addr {
		t.Fatal("recovered key derived a different address — balance would be lost")
	}
}

func TestChallengeRejectsWrongKey(t *testing.T) {
	m := wallet.NewManager()
	_, _, addr := newAccount(t)
	otherPriv, otherPub, _ := newAccount(t)
	now := time.Unix(1_800_000_000, 0)

	ch, _ := m.IssueChallenge(addr, now)
	sig := sign(t, otherPriv, ch.SigningMessage())
	// A different key doesn't derive to addr → mismatch, never even checks sig.
	if err := m.VerifyChallenge(addr, ch.Nonce, otherPub, sig, now); err != wallet.ErrKeyAddressMismatch {
		t.Errorf("expected key/address mismatch, got %v", err)
	}
}

func TestChallengeRejectsExpired(t *testing.T) {
	m := wallet.NewManager()
	priv, pub, addr := newAccount(t)
	now := time.Unix(1_800_000_000, 0)

	ch, _ := m.IssueChallenge(addr, now)
	sig := sign(t, priv, ch.SigningMessage())
	late := now.Add(wallet.ChallengeTTL + time.Minute)
	if err := m.VerifyChallenge(addr, ch.Nonce, pub, sig, late); err != wallet.ErrChallengeExpired {
		t.Errorf("expected expired, got %v", err)
	}
}

func TestChallengeIsOneTimeUse(t *testing.T) {
	m := wallet.NewManager()
	priv, pub, addr := newAccount(t)
	now := time.Unix(1_800_000_000, 0)

	ch, _ := m.IssueChallenge(addr, now)
	sig := sign(t, priv, ch.SigningMessage())
	if err := m.VerifyChallenge(addr, ch.Nonce, pub, sig, now); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	// Replaying the same nonce must fail — it was consumed.
	if err := m.VerifyChallenge(addr, ch.Nonce, pub, sig, now); err != wallet.ErrUnknownChallenge {
		t.Errorf("replayed nonce should be rejected, got %v", err)
	}
}

func TestChallengeRejectsTamperedSignature(t *testing.T) {
	m := wallet.NewManager()
	priv, pub, addr := newAccount(t)
	now := time.Unix(1_800_000_000, 0)

	ch, _ := m.IssueChallenge(addr, now)
	sig := sign(t, priv, ch.SigningMessage())
	sig[0] ^= 0xFF
	if err := m.VerifyChallenge(addr, ch.Nonce, pub, sig, now); err != wallet.ErrBadSignature {
		t.Errorf("expected bad signature, got %v", err)
	}
}

// TestTwoDevicesConsolidateToOneAccount models "Mac Studio node + iPad Pro node
// → one balance": both devices link their own node key to the same account, and
// both then resolve to the same account address for crediting.
func TestTwoDevicesConsolidateToOneAccount(t *testing.T) {
	m := wallet.NewManager()
	accPriv, accPub, addr := newAccount(t)

	// Each device has its own independent node identity.
	_, macPub, _ := newAccount(t)
	_, ipadPub, _ := newAccount(t)
	macNodeID := protocol.NodeIDFromPubKey(macPub)
	ipadNodeID := protocol.NodeIDFromPubKey(ipadPub)

	now := time.Unix(1_800_000_000, 0)
	for _, dev := range []string{macNodeID, ipadNodeID} {
		linkSig := sign(t, accPriv, []byte("oim-account-link:"+addr+":"+dev))
		if err := m.LinkDevice(addr, dev, accPub, linkSig, now); err != nil {
			t.Fatalf("link %s: %v", dev, err)
		}
	}

	for _, dev := range []string{macNodeID, ipadNodeID} {
		got, ok := m.AccountForDevice(dev)
		if !ok || got != addr {
			t.Errorf("device %s should credit account %s, got (%q, %v)", dev, addr, got, ok)
		}
	}
}

func TestLinkDeviceRequiresAccountSignature(t *testing.T) {
	m := wallet.NewManager()
	_, accPub, addr := newAccount(t)
	imposterPriv, _, _ := newAccount(t)
	_, devPub, _ := newAccount(t)
	dev := protocol.NodeIDFromPubKey(devPub)
	now := time.Unix(1_800_000_000, 0)

	// Signature from a non-account key must not be able to attach a device to
	// someone else's balance.
	badSig := sign(t, imposterPriv, []byte("oim-account-link:"+addr+":"+dev))
	if err := m.LinkDevice(addr, dev, accPub, badSig, now); err != wallet.ErrBadSignature {
		t.Errorf("expected bad signature for imposter link, got %v", err)
	}
	if _, ok := m.AccountForDevice(dev); ok {
		t.Error("device must not be linked after a rejected authorization")
	}
}

func TestUnlinkDeviceRevokes(t *testing.T) {
	m := wallet.NewManager()
	accPriv, accPub, addr := newAccount(t)
	_, devPub, _ := newAccount(t)
	dev := protocol.NodeIDFromPubKey(devPub)
	now := time.Unix(1_800_000_000, 0)

	msg := []byte("oim-account-link:" + addr + ":" + dev)
	if err := m.LinkDevice(addr, dev, accPub, sign(t, accPriv, msg), now); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := m.UnlinkDevice(addr, dev, accPub, sign(t, accPriv, msg)); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if _, ok := m.AccountForDevice(dev); ok {
		t.Error("device should be unlinked after revocation")
	}
}
