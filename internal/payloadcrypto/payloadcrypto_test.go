package payloadcrypto

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	recipientPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte(`[{"role":"user","content":"hello mesh"}]`)

	ephemeralPubRaw, combined, err := Encrypt(recipientPriv.PublicKey(), plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := Decrypt(recipientPriv, ephemeralPubRaw, combined)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round trip mismatch: got %q want %q", got, plaintext)
	}
}

func TestDecrypt_WrongRecipientFails(t *testing.T) {
	recipientPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	otherPriv, _ := ecdh.P256().GenerateKey(rand.Reader)

	ephemeralPubRaw, combined, err := Encrypt(recipientPriv.PublicKey(), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(otherPriv, ephemeralPubRaw, combined); err == nil {
		t.Error("expected decrypt with the wrong recipient key to fail")
	}
}

func TestDecrypt_TamperedCiphertextFails(t *testing.T) {
	recipientPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	ephemeralPubRaw, combined, err := Encrypt(recipientPriv.PublicKey(), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), combined...)
	tampered[len(tampered)-1] ^= 0xFF // flip a bit in the GCM tag/ciphertext
	if _, err := Decrypt(recipientPriv, ephemeralPubRaw, tampered); err == nil {
		t.Error("expected decrypt of tampered ciphertext to fail")
	}
}

func TestDecrypt_ShortCiphertextRejected(t *testing.T) {
	recipientPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	ephemeralPubRaw := recipientPriv.PublicKey().Bytes() // any validly-shaped point
	if _, err := Decrypt(recipientPriv, ephemeralPubRaw, []byte("short")); err == nil {
		t.Error("expected too-short combined ciphertext to be rejected")
	}
}

// PublicKeyEncodingIsUncompressedPoint locks in the wire format (raw
// uncompressed 65-byte point) that must match CryptoKit's
// P256.KeyAgreement.PublicKey.rawRepresentation for cross-language
// compatibility — a regression here would silently break every iOS client.
func TestPublicKeyEncodingIsUncompressedPoint(t *testing.T) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw := priv.PublicKey().Bytes()
	if len(raw) != 65 {
		t.Fatalf("want 65-byte uncompressed point, got %d bytes", len(raw))
	}
	if raw[0] != 0x04 {
		t.Fatalf("want uncompressed-point prefix 0x04, got 0x%02x", raw[0])
	}
}
