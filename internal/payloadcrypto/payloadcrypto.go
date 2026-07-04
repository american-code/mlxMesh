// Package payloadcrypto implements the node-side half of the mesh's
// client-encrypted payload-pointer scheme: ECDH(P-256) -> HKDF-SHA256 ->
// AES-256-GCM. Byte-compatible with the Swift client
// (OIMDashboard/iOS/Crypto/PayloadEncryption.swift) so a client can encrypt a
// job's messages to a node's public ECDH key and only that node can decrypt
// them — the coordinator threads the pointer (hash + fetch URL + ephemeral
// public key) but never sees the plaintext.
//
// Wire contract: the plaintext a client encrypts is the JSON encoding of the
// job's `messages` array (the same shape normally sent in the clear).
package payloadcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// hkdfInfo binds the derived key to this protocol/version, matching the
// Swift client's `hkdfInfo` constant EXACTLY — any mismatch here silently
// derives a different key and every decrypt fails with "gcm open" errors that
// look like corruption rather than a version skew.
var hkdfInfo = []byte("oim-payload-v1")

const (
	aesKeyLen   = 32 // AES-256
	gcmNonceLen = 12 // standard AES-GCM nonce size; matches AES.GCM.seal's default
)

// deriveKey runs ECDH then HKDF-SHA256(salt=nil, info=hkdfInfo) to produce the
// AES-256 key, identically on both the encrypt and decrypt side.
func deriveKey(shared []byte) ([]byte, error) {
	key := make([]byte, aesKeyLen)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, nil, hkdfInfo), key); err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	return key, nil
}

// Decrypt recovers the plaintext a client encrypted to recipientPriv's public
// key. ephemeralPubKeyRaw is the client's per-job ephemeral P-256 public key
// (raw uncompressed point, 65 bytes — CryptoKit's `.rawRepresentation`).
// combined is nonce || ciphertext || tag, exactly as produced by
// `AES.GCM.seal(...).combined` on the client.
func Decrypt(recipientPriv *ecdh.PrivateKey, ephemeralPubKeyRaw, combined []byte) ([]byte, error) {
	ephemeralPub, err := ecdh.P256().NewPublicKey(ephemeralPubKeyRaw)
	if err != nil {
		return nil, fmt.Errorf("parse ephemeral public key: %w", err)
	}
	shared, err := recipientPriv.ECDH(ephemeralPub)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}
	key, err := deriveKey(shared)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(combined) < gcmNonceLen {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := combined[:gcmNonceLen], combined[gcmNonceLen:]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("gcm open: %w", err)
	}
	return plaintext, nil
}

// Encrypt is the client-side half, provided for cross-implementation testing
// (round-tripping against Decrypt) and for any future Go-based client. Not
// used by node runtime code — nodes only ever decrypt.
func Encrypt(recipientPub *ecdh.PublicKey, plaintext []byte) (ephemeralPubKeyRaw, combined []byte, err error) {
	ephemeralPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ephemeral key: %w", err)
	}
	shared, err := ephemeralPriv.ECDH(recipientPub)
	if err != nil {
		return nil, nil, fmt.Errorf("ecdh: %w", err)
	}
	key, err := deriveKey(shared)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcmNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, plaintext, nil)
	return ephemeralPriv.PublicKey().Bytes(), append(nonce, sealed...), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return gcm, nil
}
