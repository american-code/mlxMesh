package directory

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

func hexEncodePub(pub []byte) string { return hex.EncodeToString(pub) }

func writeAllowlist(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal allowlist: %v", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write allowlist: %v", err)
	}
}

func signedDigest(t *testing.T, podID string, priv, pub []byte) protocol.PodHealthDigest {
	t.Helper()
	signed, err := protocol.SignPodHealthDigest(protocol.PodHealthDigest{PodID: podID, RegionHint: "us"}, priv, pub)
	if err != nil {
		t.Fatalf("sign digest: %v", err)
	}
	return signed
}

func TestPinStore_TOFU_FirstRegistrationWins(t *testing.T) {
	store, err := NewPinStore("")
	if err != nil {
		t.Fatalf("new pin store: %v", err)
	}
	priv, pub, _ := protocol.GenerateNodeIdentity()
	digest := signedDigest(t, "pod-us", priv, pub)
	if err := store.Verify(digest); err != nil {
		t.Fatalf("expected first registration to be accepted: %v", err)
	}
}

func TestPinStore_RejectsImpersonation(t *testing.T) {
	store, err := NewPinStore("")
	if err != nil {
		t.Fatalf("new pin store: %v", err)
	}
	priv1, pub1, _ := protocol.GenerateNodeIdentity()
	priv2, pub2, _ := protocol.GenerateNodeIdentity()

	legit := signedDigest(t, "pod-us", priv1, pub1)
	if err := store.Verify(legit); err != nil {
		t.Fatalf("expected legitimate registration to succeed: %v", err)
	}

	// A forked/malicious coordinator claims the SAME pod_id with a DIFFERENT key.
	impersonator := signedDigest(t, "pod-us", priv2, pub2)
	if err := store.Verify(impersonator); err == nil {
		t.Fatal("expected impersonation attempt (different key, same pod_id) to be rejected")
	}
}

func TestPinStore_RejectsUnsignedOrForgedDigest(t *testing.T) {
	store, err := NewPinStore("")
	if err != nil {
		t.Fatalf("new pin store: %v", err)
	}
	// No signature at all.
	if err := store.Verify(protocol.PodHealthDigest{PodID: "pod-us"}); err == nil {
		t.Fatal("expected unsigned digest to be rejected")
	}
}

func TestPinStore_PersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")

	priv1, pub1, _ := protocol.GenerateNodeIdentity()
	store1, err := NewPinStore(path)
	if err != nil {
		t.Fatalf("new pin store: %v", err)
	}
	if err := store1.Verify(signedDigest(t, "pod-us", priv1, pub1)); err != nil {
		t.Fatalf("expected registration to succeed: %v", err)
	}

	// Simulate a directory restart: fresh store loaded from the same path.
	store2, err := NewPinStore(path)
	if err != nil {
		t.Fatalf("reload pin store: %v", err)
	}
	priv2, pub2, _ := protocol.GenerateNodeIdentity()
	impersonator := signedDigest(t, "pod-us", priv2, pub2)
	if err := store2.Verify(impersonator); err == nil {
		t.Fatal("expected the reloaded store to still remember the pin and reject impersonation after a restart")
	}
}

func TestAllowlistPinStore_RejectsUnlistedPod(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_pods.json")
	priv, pub, _ := protocol.GenerateNodeIdentity()

	writeAllowlist(t, path, map[string]string{"pod-us": hexEncodePub(pub)})

	store, err := NewAllowlistPinStore(path)
	if err != nil {
		t.Fatalf("new allowlist store: %v", err)
	}
	if err := store.Verify(signedDigest(t, "pod-us", priv, pub)); err != nil {
		t.Fatalf("expected allowlisted pod to be accepted: %v", err)
	}
	otherPriv, otherPub, _ := protocol.GenerateNodeIdentity()
	if err := store.Verify(signedDigest(t, "pod-unlisted", otherPriv, otherPub)); err == nil {
		t.Fatal("expected a pod_id absent from the allowlist to be rejected outright, not auto-learned")
	}
}
