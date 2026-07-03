//go:build darwin

package attestation

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>

// Generates an ephemeral (kSecAttrIsPermanent omitted, defaults to false)
// Secure Enclave-backed P-256 key. Ephemeral by design: it avoids keychain
// ACL/entitlement pitfalls entirely for an unsigned CLI binary, and it needs
// no cross-restart persistence — an agent process re-registers with the
// coordinator on every restart anyway, so re-attesting with a fresh key each
// time is consistent with the rest of the protocol's restart behavior.
static SecKeyRef enclave_generate_key(CFErrorRef *error) {
    int keySizeValue = 256;
    CFNumberRef keySize = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &keySizeValue);

    const void *keys[] = {
        (const void *)kSecAttrKeyType,
        (const void *)kSecAttrKeySizeInBits,
        (const void *)kSecAttrTokenID,
        (const void *)kSecUseDataProtectionKeychain,
    };
    const void *values[] = {
        (const void *)kSecAttrKeyTypeECSECPrimeRandom,
        (const void *)keySize,
        (const void *)kSecAttrTokenIDSecureEnclave,
        (const void *)kCFBooleanTrue,
    };
    CFDictionaryRef attrs = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 4,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);

    SecKeyRef key = SecKeyCreateRandomKey(attrs, error);

    CFRelease(attrs);
    CFRelease(keySize);
    return key;
}

// Returns the raw X9.63 uncompressed public key bytes (0x04 || X || Y) for
// privateKey's counterpart.
static CFDataRef enclave_public_key_bytes(SecKeyRef privateKey, CFErrorRef *error) {
    SecKeyRef publicKey = SecKeyCopyPublicKey(privateKey);
    if (publicKey == NULL) {
        return NULL;
    }
    CFDataRef data = SecKeyCopyExternalRepresentation(publicKey, error);
    CFRelease(publicKey);
    return data;
}

// Signs a pre-computed SHA-256 digest, returning a DER-encoded ECDSA
// signature. Using the "Digest" algorithm variant means Security.framework
// signs exactly the bytes we pass (no re-hashing), matching
// protocol.VerifyP256Signature's ecdsa.VerifyASN1(pub, sha256.Sum256(payload), sig)
// on the coordinator side.
static CFDataRef enclave_sign_digest(SecKeyRef privateKey, const unsigned char *digest, CFIndex digestLen, CFErrorRef *error) {
    CFDataRef digestData = CFDataCreate(kCFAllocatorDefault, digest, digestLen);
    CFDataRef sig = SecKeyCreateSignature(privateKey, kSecKeyAlgorithmECDSASignatureDigestX962SHA256, digestData, error);
    CFRelease(digestData);
    return sig;
}

// Null checks as C helpers, called directly on the typed cgo values (no
// unsafe.Pointer conversion needed on the Go side) — go vet's unsafeptr
// analyzer flags Go-level unsafe.Pointer(cfRef) conversions as a suspicious
// pattern even though it's safe here (these are opaque, non-Go-GC'd CF
// objects); staying entirely on the C side of the boundary sidesteps that
// false positive.
static int seckey_is_null(SecKeyRef k) { return k == NULL; }
static int cfdata_is_null(CFDataRef d) { return d == NULL; }
static int cferror_is_null(CFErrorRef e) { return e == NULL; }
static int cfstring_is_null(CFStringRef s) { return s == NULL; }
*/
import "C"

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"unsafe"
)

// Signer generates (on first use) and signs with an ephemeral Secure
// Enclave-backed P-256 key via macOS Security.framework. The private key
// never leaves the enclave — only PublicKey() and Sign() are exposed, and
// there is no method that could export the raw private scalar even if asked.
type Signer struct {
	mu      sync.Mutex
	privKey C.SecKeyRef
	pubKey  []byte
}

// NewSigner returns a Signer. Key generation is lazy — it happens on the
// first PublicKey() or Sign() call, so constructing a Signer on a machine
// without a Secure Enclave (or without arm64) is free; only using it fails.
func NewSigner() *Signer { return &Signer{} }

func (s *Signer) ensureKey() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if C.seckey_is_null(s.privKey) == 0 {
		return nil
	}
	var genErr C.CFErrorRef
	key := C.enclave_generate_key(&genErr)
	if C.seckey_is_null(key) != 0 {
		return fmt.Errorf("generate secure enclave key: %w", cfErrorToGo(genErr))
	}

	var pubErr C.CFErrorRef
	pubData := C.enclave_public_key_bytes(key, &pubErr)
	if C.cfdata_is_null(pubData) != 0 {
		C.CFRelease(C.CFTypeRef(key))
		return fmt.Errorf("copy secure enclave public key: %w", cfErrorToGo(pubErr))
	}
	defer C.CFRelease(C.CFTypeRef(pubData))

	s.privKey = key
	s.pubKey = cfDataToBytes(pubData)
	return nil
}

// PublicKey returns the raw X9.63 uncompressed P-256 public key bytes,
// generating a fresh Secure Enclave-backed key on first call.
func (s *Signer) PublicKey() ([]byte, error) {
	if err := s.ensureKey(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pubKey, nil
}

// Sign returns a DER-encoded ECDSA-P256 signature over SHA-256(msg), produced
// by the Secure Enclave. The signing operation itself happens inside the
// enclave hardware — this call never has access to the private key bytes.
func (s *Signer) Sign(msg []byte) ([]byte, error) {
	if err := s.ensureKey(); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(msg)

	cDigest := C.CBytes(digest[:])
	defer C.free(cDigest)

	s.mu.Lock()
	defer s.mu.Unlock()
	var signErr C.CFErrorRef
	sig := C.enclave_sign_digest(s.privKey, (*C.uchar)(cDigest), C.CFIndex(len(digest)), &signErr)
	if C.cfdata_is_null(sig) != 0 {
		return nil, fmt.Errorf("sign with secure enclave: %w", cfErrorToGo(signErr))
	}
	defer C.CFRelease(C.CFTypeRef(sig))
	return cfDataToBytes(sig), nil
}

func cfDataToBytes(d C.CFDataRef) []byte {
	length := C.CFDataGetLength(d)
	ptr := C.CFDataGetBytePtr(d)
	return C.GoBytes(unsafe.Pointer(ptr), C.int(length))
}

func cfErrorToGo(err C.CFErrorRef) error {
	if C.cferror_is_null(err) != 0 {
		return fmt.Errorf("unknown secure enclave error")
	}
	defer C.CFRelease(C.CFTypeRef(err))
	desc := C.CFErrorCopyDescription(err)
	code := int(C.CFErrorGetCode(err))
	if C.cfstring_is_null(desc) != 0 {
		return fmt.Errorf("secure enclave error (code %d)", code)
	}
	defer C.CFRelease(C.CFTypeRef(desc))
	return fmt.Errorf("secure enclave error (code %d): %s", code, cfStringToGo(desc))
}

func cfStringToGo(s C.CFStringRef) string {
	length := C.CFStringGetLength(s)
	maxSize := C.CFStringGetMaximumSizeForEncoding(length, C.kCFStringEncodingUTF8) + 1
	buf := make([]byte, int(maxSize))
	ok := C.CFStringGetCString(s, (*C.char)(unsafe.Pointer(&buf[0])), maxSize, C.kCFStringEncodingUTF8)
	if ok == 0 {
		return "<unreadable CFString>"
	}
	return C.GoString((*C.char)(unsafe.Pointer(&buf[0])))
}
