import CryptoKit
import Foundation

/// Reads the real `oim` node's own persistent identity — deliberately NOT a
/// separate Swift-side device-identity scheme. `DeviceIdentity.swift` (in
/// OIMDashboard/iOS) exists only because iOS can't run Exo/oim and so has no
/// real compute-node identity to link; a Mac running this app DOES have one
/// (`~/.config/oim/node_identity.json`, written by `internal/identity`), and
/// linking that — not a second invented ID — is what makes the account's
/// earnings match the node that's actually doing the work.
enum NodeIdentityReader {
    struct Identity {
        let nodeID: String
        let publicKeyHex: String
    }

    private static var identityPath: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".config/oim/node_identity.json")
    }

    /// Reads the identity file and derives the node ID exactly as
    /// `protocol.NodeIDFromPubKey` does on the Go side: SHA-256(pubkey), hex
    /// encoded, truncated to the first 32 hex characters (== the first 16
    /// raw digest bytes). Returns nil if the node has never been started
    /// (no identity file yet) or the file is malformed.
    static func read() -> Identity? {
        guard let data = try? Data(contentsOf: identityPath),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let pubHex = json["public_key"] as? String,
              let pubKey = Data(hexEncoded: pubHex)
        else { return nil }

        let digest = SHA256.hash(data: pubKey)
        let nodeID = digest.map { String(format: "%02x", $0) }.joined().prefix(32)
        return Identity(nodeID: String(nodeID), publicKeyHex: pubHex)
    }
}

private extension Data {
    init?(hexEncoded hex: String) {
        let chars = Array(hex)
        guard chars.count % 2 == 0 else { return nil }
        var bytes = [UInt8]()
        bytes.reserveCapacity(chars.count / 2)
        var i = 0
        while i < chars.count {
            guard let b = UInt8(String(chars[i...i + 1]), radix: 16) else { return nil }
            bytes.append(b)
            i += 2
        }
        self = Data(bytes)
    }
}
