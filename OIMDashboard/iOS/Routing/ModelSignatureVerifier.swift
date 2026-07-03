import Foundation
import CryptoKit

/// Verifies that a Core ML model package was signed by the project's known key.
/// A failed verification must cause CoreMLRouter to fall back to RuleBasedRouter —
/// never crash, never proceed with an unverified model.
enum ModelSignatureVerifier {

    /// Embedded project public key (Ed25519, base64 raw representation).
    /// Compile-time constant — NEVER fetched at runtime; a runtime-fetched key
    /// would let an attacker who controls the network also swap the key.
    ///
    /// Project model-signing public key (Ed25519 raw, base64). The matching
    /// PRIVATE key lives only in the build/release environment (env
    /// OIM_MODEL_SIGNING_KEY for tools/train-router) — never in the app or repo.
    /// Rotating this key invalidates every previously shipped model, by design.
    private static let projectPublicKeyBase64 = "WaS6/PV+PJfCtuluDqBTnDRimEigZFJLZ2amK8mbeRA="

    /// Minimum model version the app will load. Bumped when an old classifier is
    /// retired. Compared against the version string embedded in the model's
    /// co-located `.version` file (single line, e.g. "core_ml_v3").
    static let minimumSupportedVersion = 1

    /// Returns true only if: the co-located signature is valid over SHA-256 of the
    /// model package, it was made by the embedded project key, and the model's
    /// embedded version is >= minimumSupportedVersion. Any failure returns false
    /// and logs locally — the reason is NEVER surfaced to the coordinator/network.
    static func verify(modelURL: URL) -> Bool {
        guard let publicKey = loadProjectKey() else {
            log("no valid project signing key configured — treating model as unverified")
            return false
        }

        let sigURL = modelURL.appendingPathExtension("sig")
        guard let signature = try? Data(contentsOf: sigURL) else {
            log("missing signature file at \(sigURL.lastPathComponent)")
            return false
        }

        guard let modelData = try? Data(contentsOf: modelURL) else {
            log("could not read model package")
            return false
        }
        let digest = Data(SHA256.hash(data: modelData))

        guard publicKey.isValidSignature(signature, for: digest) else {
            log("signature does not verify under the project key")
            return false
        }

        guard modelVersion(for: modelURL) >= minimumSupportedVersion else {
            log("model version below minimum supported (\(minimumSupportedVersion))")
            return false
        }
        return true
    }

    private static func loadProjectKey() -> Curve25519.Signing.PublicKey? {
        guard projectPublicKeyBase64 != "REPLACE_WITH_REAL_PROJECT_ED25519_PUBLIC_KEY",
              let raw = Data(base64Encoded: projectPublicKeyBase64),
              let key = try? Curve25519.Signing.PublicKey(rawRepresentation: raw)
        else { return nil }
        return key
    }

    /// Reads the trailing integer of the co-located ".version" file
    /// ("core_ml_v3" -> 3). Missing/unparseable -> 0 (fails the minimum check).
    private static func modelVersion(for modelURL: URL) -> Int {
        let versionURL = modelURL.appendingPathExtension("version")
        guard let raw = try? String(contentsOf: versionURL, encoding: .utf8) else { return 0 }
        let digits = raw.reversed().prefix { $0.isNumber }.reversed()
        return Int(String(digits)) ?? 0
    }

    private static func log(_ message: String) {
        // Local diagnostics only — deliberately not sent anywhere.
        print("[ModelSignatureVerifier] \(message)")
    }
}
