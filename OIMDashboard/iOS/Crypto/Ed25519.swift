import CryptoKit
import Foundation

/// Thin Ed25519 helper over CryptoKit's Curve25519, matching the coordinator's
/// Ed25519 registration/settlement signatures (internal/protocol/crypto.go). Raw
/// 32-byte public keys and 64-byte signatures interoperate with the Go side.
enum Ed25519 {

    /// Signs `payload` with a 32-byte raw Ed25519 private key.
    static func sign(_ payload: Data, privateKey: Data) throws -> Data {
        let key = try Curve25519.Signing.PrivateKey(rawRepresentation: privateKey)
        return try key.signature(for: payload)
    }

    /// Verifies a signature under a 32-byte raw Ed25519 public key.
    static func verify(_ signature: Data, payload: Data, publicKey: Data) -> Bool {
        guard let key = try? Curve25519.Signing.PublicKey(rawRepresentation: publicKey) else { return false }
        return key.isValidSignature(signature, for: payload)
    }

    /// Generates a fresh keypair as (privateRaw, publicRaw).
    static func generate() -> (privateKey: Data, publicKey: Data) {
        let key = Curve25519.Signing.PrivateKey()
        return (key.rawRepresentation, key.publicKey.rawRepresentation)
    }
}
