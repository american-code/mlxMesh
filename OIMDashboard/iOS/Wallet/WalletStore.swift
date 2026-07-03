import CryptoKit
import Foundation
import Observation
import Security

/// WalletStore is the client half of the mesh "wallet" (see internal/wallet in
/// the coordinator). It is NOT an on-chain wallet: it holds an Ed25519 account
/// keypair that PROVES ownership of a ledger balance, so the same account —
/// and therefore the same credits — can be used from every device and recovered
/// after a device dies.
///
/// Two recovery paths, both handled here:
///   1. iCloud Keychain — the seed is stored with kSecAttrSynchronizable, so it
///      syncs to the user's other Apple devices automatically. Sign in on a new
///      iPad and the account (and its balance) is already there.
///   2. Recovery key — a Base32 rendering of the 32-byte account seed the user
///      can write down. Re-import it on any device (even a Mac Studio Go node,
///      which uses the same "oim" + hex(sha256(pubkey)) address scheme) to regain
///      control. The coordinator never held the key, so this is the ONLY backup.
@Observable
@MainActor
final class WalletStore {
    /// Account address = "oim" + hex(SHA-256(accountPublicKey)). This is the
    /// ledger user_id — deriving the same seed anywhere yields the same address
    /// and thus the same balance. nil means no wallet exists yet.
    private(set) var address: String?

    /// Session API key minted by POST /account/auth after a challenge is signed.
    /// Bound to `address`; present it as a Bearer token on authenticated calls.
    private(set) var sessionKey: String?
    private(set) var authError: String?

    /// Node IDs this account has authorized to earn into it (e.g. a Mac Studio
    /// running the Go node-agent). Persisted locally as a convenience list; the
    /// authoritative binding lives in the coordinator's wallet Manager.
    private(set) var linkedDevices: [String] = []

    var hasWallet: Bool { address != nil }
    var isAuthenticated: Bool { sessionKey != nil }

    private var seed: Data?

    private let service = "solutions.paydirt.mlxmesh.account"
    private let account = "account-seed-v1"
    private let linkedDefaultsKey = "mlxmesh_linked_devices"

    init() {
        load()
        linkedDevices = UserDefaults.standard.stringArray(forKey: linkedDefaultsKey) ?? []
    }

    // MARK: - Derived identity

    /// Raw 32-byte Ed25519 public key for this account, or nil if no wallet.
    var publicKey: Data? {
        guard let seed, let key = try? Curve25519.Signing.PrivateKey(rawRepresentation: seed) else { return nil }
        return key.publicKey.rawRepresentation
    }

    /// Human-writable backup of the account seed (Crockford Base32, grouped).
    /// Losing this AND iCloud Keychain means the balance is unrecoverable — by
    /// design; the coordinator cannot reset an account it never held a key for.
    var recoveryKey: String? {
        guard let seed else { return nil }
        return Self.groupedBase32(seed)
    }

    // MARK: - Lifecycle

    /// Creates a brand-new account (fresh random seed) and stores it in the
    /// iCloud Keychain. No-op if a wallet already exists.
    func createWallet() {
        guard seed == nil else { return }
        let key = Curve25519.Signing.PrivateKey()
        persist(seed: key.rawRepresentation)
    }

    /// Restores an account from a previously-shown recovery key. Returns false if
    /// the key doesn't decode to a valid 32-byte seed.
    @discardableResult
    func importWallet(recoveryKey text: String) -> Bool {
        guard let raw = Self.decodeBase32(text), raw.count == 32,
              (try? Curve25519.Signing.PrivateKey(rawRepresentation: raw)) != nil
        else { return false }
        persist(seed: raw)
        return true
    }

    /// Forgets the wallet on THIS device only (removes the local + synced
    /// Keychain item). The account still exists; re-import to regain it.
    func forget() {
        deleteFromKeychain()
        seed = nil
        address = nil
        sessionKey = nil
        authError = nil
    }

    private func persist(seed newSeed: Data) {
        saveToKeychain(newSeed)
        seed = newSeed
        address = Self.deriveAddress(fromSeed: newSeed)
        sessionKey = nil
    }

    private func load() {
        guard let raw = loadFromKeychain(), raw.count == 32 else { return }
        seed = raw
        address = Self.deriveAddress(fromSeed: raw)
    }

    // MARK: - Coordinator auth / linking

    /// Runs the challenge → sign → auth handshake, minting a session key bound to
    /// this account address. Safe to call repeatedly (re-auth).
    func authenticate(coordinatorURL: String) async {
        guard let address, let seed, let publicKey else {
            authError = "No wallet on this device"
            return
        }
        authError = nil
        do {
            let nonce = try await NetworkClient.requestChallenge(coordinatorURL: coordinatorURL, address: address)
            let msg = Data("oim-account-auth:\(address):\(nonce)".utf8)
            let sig = try Ed25519.sign(msg, privateKey: seed)
            sessionKey = try await NetworkClient.authenticate(
                coordinatorURL: coordinatorURL, address: address, nonce: nonce,
                publicKey: publicKey, signature: sig)
        } catch {
            sessionKey = nil
            authError = error.localizedDescription
        }
    }

    /// Authorizes `deviceNodeID` (e.g. a Mac Studio node) to earn into this
    /// account. The account key signs the binding, proving the account owner
    /// consents — so nobody can attach their device to someone else's balance.
    /// Returns nil on success, or an error message.
    func linkDevice(_ deviceNodeID: String, coordinatorURL: String) async -> String? {
        let trimmed = deviceNodeID.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return "Enter a device / node ID" }
        guard let address, let seed, let publicKey else { return "No wallet on this device" }
        do {
            let msg = Data("oim-account-link:\(address):\(trimmed)".utf8)
            let sig = try Ed25519.sign(msg, privateKey: seed)
            try await NetworkClient.linkDevice(
                coordinatorURL: coordinatorURL, address: address,
                deviceNodeID: trimmed, accountPublicKey: publicKey, signature: sig)
            if !linkedDevices.contains(trimmed) {
                linkedDevices.append(trimmed)
                UserDefaults.standard.set(linkedDevices, forKey: linkedDefaultsKey)
            }
            return nil
        } catch {
            return error.localizedDescription
        }
    }

    // MARK: - Address derivation (matches internal/wallet.AddressFromPubKey)

    private static func deriveAddress(fromSeed seed: Data) -> String? {
        guard let key = try? Curve25519.Signing.PrivateKey(rawRepresentation: seed) else { return nil }
        let digest = SHA256.hash(data: key.publicKey.rawRepresentation)
        return "oim" + digest.map { String(format: "%02x", $0) }.joined()
    }

    // MARK: - Keychain (iCloud-synchronized)

    private func baseQuery() -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            // Synchronizable → the seed rides iCloud Keychain to the user's other
            // Apple devices, giving automatic cross-device consolidation.
            kSecAttrSynchronizable as String: kCFBooleanTrue!,
        ]
    }

    private func saveToKeychain(_ data: Data) {
        deleteFromKeychain()
        var q = baseQuery()
        q[kSecValueData as String] = data
        q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        SecItemAdd(q as CFDictionary, nil)
    }

    private func loadFromKeychain() -> Data? {
        var q = baseQuery()
        q[kSecReturnData as String] = kCFBooleanTrue!
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess else { return nil }
        return out as? Data
    }

    private func deleteFromKeychain() {
        SecItemDelete(baseQuery() as CFDictionary)
    }

    // MARK: - Crockford Base32 (no I/L/O/U — unambiguous when written by hand)

    private static let alphabet = Array("0123456789ABCDEFGHJKMNPQRSTVWXYZ")

    static func groupedBase32(_ data: Data) -> String {
        let raw = base32(data)
        // Group into 4-char blocks for legibility.
        var out = "", i = 0
        for ch in raw {
            if i > 0 && i % 4 == 0 { out.append("-") }
            out.append(ch); i += 1
        }
        return out
    }

    private static func base32(_ data: Data) -> String {
        var bits = 0, value = 0, out = ""
        for byte in data {
            value = (value << 8) | Int(byte); bits += 8
            while bits >= 5 {
                out.append(alphabet[(value >> (bits - 5)) & 0x1F]); bits -= 5
            }
        }
        if bits > 0 { out.append(alphabet[(value << (5 - bits)) & 0x1F]) }
        return out
    }

    static func decodeBase32(_ text: String) -> Data? {
        let clean = text.uppercased().filter { $0 != "-" && $0 != " " }
        var lookup = [Character: Int]()
        for (i, c) in alphabet.enumerated() { lookup[c] = i }
        var bits = 0, value = 0
        var out = [UInt8]()
        for ch in clean {
            guard let v = lookup[ch] else { return nil }
            value = (value << 5) | v; bits += 5
            if bits >= 8 { out.append(UInt8((value >> (bits - 8)) & 0xFF)); bits -= 8 }
        }
        return Data(out)
    }
}
