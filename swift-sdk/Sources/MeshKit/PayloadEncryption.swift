import CryptoKit
import Foundation

/// Client-side half of the encrypted-pointer payload scheme: ECDH(P-256) ->
/// HKDF-SHA256 -> AES-256-GCM. Byte-compatible with
/// internal/payloadcrypto/payloadcrypto.go (Go) and mlxmesh.crypto (Python) —
/// `hkdfInfo` and the nonce||ciphertext||tag layout must match exactly across
/// all three, or decryption fails looking like corruption rather than a
/// version mismatch.
///
/// Adapted from OIMDashboard/iOS/Crypto/PayloadEncryption.swift, with one
/// real bug fixed in the port: the dashboard version uses
/// `.rawRepresentation` for the ephemeral public key, which for CryptoKit's
/// P256.KeyAgreement.PublicKey is the COMPACT 64-byte form (raw X||Y, no
/// prefix) — but Go's `crypto/ecdh` (and this scheme's own wire contract)
/// expects the SEC1 UNCOMPRESSED form (0x04 || X || Y, 65 bytes), which is
/// `.x963Representation` in CryptoKit. Caught live by this SDK's
/// cross-language interop test (PayloadEncryptionTests.swift) — a byte-count
/// mismatch that would have made every real encrypted-pointer job
/// undecryptable by an actual Go node. The dashboard's
/// `storeLocally`/`LocalPayloadServer`/`LocalNetwork` are deliberately NOT
/// included here — hosting the ciphertext somewhere the assigned node can
/// fetch it is inherently application-specific, and an SDK baking in one
/// LAN-only hosting strategy would be wrong for most callers.
public enum PayloadEncryption {

    private static let hkdfInfo = Data("oim-payload-v1".utf8)

    public enum CryptoError: Error {
        case sealFailed
    }

    /// Encrypts `plaintext` (conventionally the JSON encoding of the job's
    /// `messages` array — see MeshClient.submitEncrypted) to `recipientPublicKey`,
    /// the ECDH key returned by POST /v1/reserve-node. Generates a fresh
    /// ephemeral keypair per call — never reuse one across jobs.
    public static func encrypt(
        plaintext: Data,
        recipientPublicKey: P256.KeyAgreement.PublicKey
    ) throws -> EncryptedPayload {
        let ephemeral = P256.KeyAgreement.PrivateKey()
        let shared = try ephemeral.sharedSecretFromKeyAgreement(with: recipientPublicKey)
        let key = shared.hkdfDerivedSymmetricKey(
            using: SHA256.self, salt: Data(), sharedInfo: hkdfInfo, outputByteCount: 32)
        let sealed = try AES.GCM.seal(plaintext, using: key)
        guard let combined = sealed.combined else { throw CryptoError.sealFailed }
        return EncryptedPayload(
            ciphertext: combined,
            ephemeralPublicKeyData: ephemeral.publicKey.x963Representation
        )
    }

    /// Inverse of `encrypt` — provided for round-trip testing and for any
    /// Swift-side tooling that needs to decrypt (in production, only the
    /// assigned node ever decrypts).
    public static func decrypt(
        ciphertext: Data,
        ephemeralPublicKeyData: Data,
        recipientPrivateKey: P256.KeyAgreement.PrivateKey
    ) throws -> Data {
        let ephemeralPub = try P256.KeyAgreement.PublicKey(x963Representation: ephemeralPublicKeyData)
        let shared = try recipientPrivateKey.sharedSecretFromKeyAgreement(with: ephemeralPub)
        let key = shared.hkdfDerivedSymmetricKey(
            using: SHA256.self, salt: Data(), sharedInfo: hkdfInfo, outputByteCount: 32)
        let box = try AES.GCM.SealedBox(combined: ciphertext)
        return try AES.GCM.open(box, using: key)
    }
}

/// `ciphertext` is `nonce || ciphertext || tag` (AES.GCM.SealedBox.combined).
/// `ephemeralPublicKeyData` is the raw uncompressed P-256 point (65 bytes) —
/// travels as a separate base64 field on the wire (`oim_ephemeral_public_key`),
/// never embedded in `ciphertext`.
public struct EncryptedPayload {
    public let ciphertext: Data
    public let ephemeralPublicKeyData: Data

    public var ephemeralPublicKeyBase64: String {
        ephemeralPublicKeyData.base64EncodedString()
    }
}
