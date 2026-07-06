package protocol

import "testing"

func TestSignAndVerifyPodHealthDigest(t *testing.T) {
	priv, pub, err := GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	digest := PodHealthDigest{
		PodID:                "pod-us",
		RegionHint:           "us",
		ServableModelIDs:     []string{"llama-3.2-3b"},
		AggregateHealthScore: 0.9,
	}
	signed, err := SignPodHealthDigest(digest, priv, pub)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	gotPub, ok := VerifyPodHealthDigestSignature(signed)
	if !ok {
		t.Fatal("expected signature to verify")
	}
	if string(gotPub) != string(pub) {
		t.Fatal("recovered public key does not match signer")
	}
}

func TestVerifyPodHealthDigestSignature_TamperedField(t *testing.T) {
	priv, pub, err := GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	signed, err := SignPodHealthDigest(PodHealthDigest{PodID: "pod-us", AggregateHealthScore: 0.5}, priv, pub)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Tamper with a field the signature covers, post-signing.
	signed.AggregateHealthScore = 0.99
	if _, ok := VerifyPodHealthDigestSignature(signed); ok {
		t.Fatal("expected signature verification to fail after tampering")
	}
}

func TestVerifyPodHealthDigestSignature_MissingSignature(t *testing.T) {
	digest := PodHealthDigest{PodID: "pod-us"}
	if _, ok := VerifyPodHealthDigestSignature(digest); ok {
		t.Fatal("expected verification to fail for an unsigned digest")
	}
}

func TestVerifyPodHealthDigestSignature_WrongKey(t *testing.T) {
	priv, _, err := GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	_, otherPub, err := GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	// Sign with priv but claim otherPub as the signer — a forged public_key field.
	signed, err := SignPodHealthDigest(PodHealthDigest{PodID: "pod-us"}, priv, otherPub)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, ok := VerifyPodHealthDigestSignature(signed); ok {
		t.Fatal("expected verification to fail when public_key doesn't match the actual signer")
	}
}
