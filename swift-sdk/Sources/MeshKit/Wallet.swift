import CryptoKit
import Foundation

/// Portable, recoverable account identity — the client-side half of
/// `internal/wallet.Manager`'s challenge/response protocol. Mirrors
/// `OIMDashboard/iOS/Wallet/WalletStore.swift`'s keypair/address logic
/// exactly, but with pluggable storage rather than one app's hardcoded
/// Keychain access group — MeshKit is a library other apps embed.
///
/// Wire format matches `internal/wallet/wallet.go` and
/// `cmd/coordinator/main.go`'s `POST /account/challenge` / `POST
/// /account/auth` handlers exactly:
///   - address        = "oim" + hex(sha256(raw 32-byte Ed25519 public key))
///   - signed message  = "oim-account-auth:{address}:{nonce}" (UTF-8 bytes)
///   - public_key/signature on the wire are standard (padded) base64.
public struct Wallet: Sendable {
    public let address: String
    /// The raw 32-byte Ed25519 seed — stored as plain Data (not a live
    /// CryptoKit key object) so Wallet stays trivially Sendable for use from
    /// the MeshClient actor. Never sent over the network; only used locally
    /// to sign and to derive the public key.
    private let seed: Data

    public init(seed: Data) throws {
        guard seed.count == 32 else {
            throw WalletError.invalidSeedLength(seed.count)
        }
        self.seed = seed
        let key = try Curve25519.Signing.PrivateKey(rawRepresentation: seed)
        self.address = Wallet.deriveAddress(publicKey: key.publicKey)
    }

    /// Generates a brand-new random keypair. Nothing is persisted until you
    /// call .save() — the caller decides whether/where to keep it.
    public static func create() -> Wallet {
        let key = Curve25519.Signing.PrivateKey()
        // A freshly-generated Curve25519 private key's rawRepresentation is
        // always exactly 32 bytes — this cannot actually fail.
        return try! Wallet(seed: key.rawRepresentation)
    }

    private static func deriveAddress(publicKey: Curve25519.Signing.PublicKey) -> String {
        let digest = SHA256.hash(data: publicKey.rawRepresentation)
        return "oim" + digest.map { String(format: "%02x", $0) }.joined()
    }

    /// Raw Ed25519 signature over message (64 bytes).
    public func sign(_ message: Data) throws -> Data {
        let key = try Curve25519.Signing.PrivateKey(rawRepresentation: seed)
        return try key.signature(for: message)
    }

    /// Standard (padded) base64 of the raw 32-byte public key — the exact
    /// encoding POST /account/auth expects in its public_key field.
    public var publicKeyBase64: String {
        let key = try! Curve25519.Signing.PrivateKey(rawRepresentation: seed)
        return key.publicKey.rawRepresentation.base64EncodedString()
    }

    // MARK: - Persistence

    public func save(storage: WalletStorage = FileWalletStorage()) throws {
        try storage.save(seed: seed)
    }

    /// Loads a previously-saved wallet. Throws WalletError.notFound if there
    /// isn't one — use loadOrCreate() for silent first-run creation instead.
    public static func load(storage: WalletStorage = FileWalletStorage()) throws -> Wallet {
        guard let seed = try storage.load() else {
            throw WalletError.notFound
        }
        return try Wallet(seed: seed)
    }

    /// The common case: use the wallet in storage if one exists, otherwise
    /// generate a fresh one and persist it immediately so the address is
    /// stable across process restarts from the very first run.
    public static func loadOrCreate(storage: WalletStorage = FileWalletStorage()) throws -> Wallet {
        if let seed = try storage.load() {
            return try Wallet(seed: seed)
        }
        let wallet = Wallet.create()
        try wallet.save(storage: storage)
        return wallet
    }
}

public enum WalletError: Error, LocalizedError {
    case invalidSeedLength(Int)
    case notFound

    public var errorDescription: String? {
        switch self {
        case .invalidSeedLength(let n): return "wallet seed must be 32 bytes, got \(n)"
        case .notFound: return "no wallet found at the configured storage location"
        }
    }
}

/// Pluggable persistence for a wallet's seed. MeshKit ships a file-based
/// default (FileWalletStorage) that works on all four supported platforms
/// without assuming Keychain or any one app's access group — embedding apps
/// that want Keychain-backed storage (as OIMDashboard's own WalletStore
/// does) supply their own conformance.
public protocol WalletStorage: Sendable {
    func save(seed: Data) throws
    func load() throws -> Data?
}

/// Default storage: a JSON file `{"seed": "<64 hex chars>"}` under the
/// platform's Application Support directory, POSIX permissions 0600.
public struct FileWalletStorage: WalletStorage {
    private let path: URL

    public init(path: URL? = nil) {
        if let path {
            self.path = path
        } else {
            let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
                ?? FileManager.default.temporaryDirectory
            self.path = base.appendingPathComponent("mlxmesh", isDirectory: true).appendingPathComponent("wallet.json")
        }
    }

    public func save(seed: Data) throws {
        let dir = path.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let hex = seed.map { String(format: "%02x", $0) }.joined()
        let json = try JSONSerialization.data(withJSONObject: ["seed": hex])
        try json.write(to: path, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: path.path)
    }

    public func load() throws -> Data? {
        guard FileManager.default.fileExists(atPath: path.path) else { return nil }
        let data = try Data(contentsOf: path)
        guard let obj = try JSONSerialization.jsonObject(with: data) as? [String: String],
              let hex = obj["seed"], let seed = Data(hexEncoded: hex)
        else {
            throw WalletError.notFound
        }
        return seed
    }
}

extension Data {
    fileprivate init?(hexEncoded hex: String) {
        guard hex.count % 2 == 0 else { return nil }
        var data = Data(capacity: hex.count / 2)
        var idx = hex.startIndex
        while idx < hex.endIndex {
            let next = hex.index(idx, offsetBy: 2)
            guard let byte = UInt8(hex[idx..<next], radix: 16) else { return nil }
            data.append(byte)
            idx = next
        }
        self = data
    }
}
