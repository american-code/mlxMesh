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

    /// Set when a Keychain read/write fails for a reason other than "no item
    /// yet" (errSecItemNotFound) — most often errSecMissingEntitlement, which
    /// means THIS build's signing team doesn't actually hold the
    /// `keychainAccessGroup` entitlement it's asking for (see that constant's
    /// doc comment). Previously these failures were silently swallowed —
    /// SecItemAdd's status was never checked — which looked exactly like "the
    /// wallet reset" on every launch: the write silently failed, so the next
    /// launch's read found nothing and createWallet() minted a fresh one.
    /// Cleared once usingLocalFallback is true — at that point the wallet IS
    /// persisting (see fallback storage below), just not via Keychain, so
    /// this stops being an active error and becomes informational.
    private(set) var keychainError: String?

    /// True once the seed has been persisted via the local-file fallback
    /// (fallbackFileURL) rather than Keychain — meaning the wallet DOES
    /// survive an app relaunch on THIS device, but is NOT syncing to other
    /// Apple devices via iCloud Keychain the way a real Keychain-backed
    /// wallet would. This is a deliberate degraded-but-working mode for when
    /// Keychain access is denied (see keychainError) rather than leaving the
    /// wallet unpersisted — the seed/recovery key themselves are never the
    /// problem in that case, only where this device is allowed to store them.
    private(set) var usingLocalFallback = false

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
    /// Keychain item, and the local fallback file if one exists). The
    /// account still exists; re-import to regain it.
    func forget() {
        deleteFromKeychain()
        deleteFallbackFile()
        seed = nil
        address = nil
        sessionKey = nil
        authError = nil
        keychainError = nil
        usingLocalFallback = false
    }

    private func persist(seed newSeed: Data) {
        saveSeed(newSeed)
        seed = newSeed
        address = Self.deriveAddress(fromSeed: newSeed)
        sessionKey = nil
    }

    private func load() {
        guard let raw = loadSeed(), raw.count == 32 else { return }
        seed = raw
        address = Self.deriveAddress(fromSeed: raw)
    }

    // MARK: - Combined storage (Keychain, falling back to a local file)

    /// Tries Keychain first (the real, intended path — gives iCloud sync
    /// across the user's other Apple devices). On any Keychain failure,
    /// falls back to a plain file in this app's own sandboxed Application
    /// Support directory — no special entitlement needed for that at all,
    /// since it's just the app writing to its own private container. The
    /// wallet/seed itself is never the problem in that failure mode; only
    /// where this device is permitted to store it is. See usingLocalFallback.
    private func saveSeed(_ data: Data) {
        saveToKeychain(data)
        if keychainError == nil {
            usingLocalFallback = false
            deleteFallbackFile() // Keychain now authoritative; don't leave a stale duplicate on disk
            return
        }
        do {
            try saveToFallbackFile(data)
            usingLocalFallback = true
            keychainError = nil // fallback succeeded — wallet IS persisting, just not via Keychain; see usingLocalFallback
        } catch {
            usingLocalFallback = false
            keychainError = (keychainError ?? "") + " (local fallback also failed: \(error.localizedDescription))"
        }
    }

    private func loadSeed() -> Data? {
        if let raw = loadFromKeychain() {
            usingLocalFallback = false
            return raw
        }
        if let raw = loadFromFallbackFile() {
            usingLocalFallback = true
            keychainError = nil // loaded fine from fallback — not an active error, see usingLocalFallback
            return raw
        }
        return nil
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

    /// Revokes `deviceNodeID`'s earnings binding — the same account-signed
    /// message linkDevice uses (the coordinator's wallet.Manager treats
    /// link/unlink as the identical signature check), so it needs no new
    /// server trust surface. Removes the entry from the local advisory
    /// `linkedDevices` list on success; the coordinator is the source of
    /// truth either way. Returns nil on success, or an error message.
    func unlinkDevice(_ deviceNodeID: String, coordinatorURL: String) async -> String? {
        let trimmed = deviceNodeID.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return "Enter a device / node ID" }
        guard let address, let seed, let publicKey else { return "No wallet on this device" }
        do {
            let msg = Data("oim-account-link:\(address):\(trimmed)".utf8)
            let sig = try Ed25519.sign(msg, privateKey: seed)
            try await NetworkClient.unlinkDevice(
                coordinatorURL: coordinatorURL, address: address,
                deviceNodeID: trimmed, accountPublicKey: publicKey, signature: sig)
            linkedDevices.removeAll { $0 == trimmed }
            UserDefaults.standard.set(linkedDevices, forKey: linkedDefaultsKey)
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

    // Explicit, shared Keychain Access Group — MUST match the
    // `keychain-access-groups` entitlement in every target's .entitlements
    // file (OIMDashboard's iOS/watch/tv targets AND OIMMenuBar), and the
    // literal team ID here must match DEVELOPMENT_TEAM in both project.yml
    // files (currently "WXDFFW3882" for both).
    //
    // Without an explicit access group, SecItemAdd/SecItemCopyMatching fall
    // back to a group implicitly derived from EACH APP'S OWN bundle
    // identifier — meaning OIMDashboard (iOS) and OIMMenuBar (macOS), being
    // different bundle IDs, would each get a DIFFERENT default group even
    // though both are signed by the same team. Two real bugs traced to this:
    // (1) the Mac app appearing to "reset" its wallet on some rebuilds —
    // Xcode's ad-hoc/dev signing during active development can shift enough
    // for the OS to no longer recognize the previous build as the same
    // keychain-ACL holder for an *implicit* group, whereas an explicit,
    // named group is stable across that; (2) the wallet was never actually
    // syncing between the Mac and iPad apps via iCloud Keychain at all —
    // each had always been reading/writing its own isolated implicit group,
    // so "sign into the same iCloud account and it just appears" (this
    // project's stated design) was not actually true until this was added.
    private let keychainAccessGroup = "WXDFFW3882.com.openinferencemesh.shared-wallet"

    private func baseQuery() -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecAttrAccessGroup as String: keychainAccessGroup,
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
        let status = SecItemAdd(q as CFDictionary, nil)
        if status == errSecSuccess {
            keychainError = nil
        } else {
            // Previously ignored entirely — a failed write here means the seed
            // ONLY lives in this in-memory session and silently vanishes on next
            // launch, which looks exactly like "the wallet keeps resetting."
            // errSecMissingEntitlement specifically means this build's signing
            // team doesn't hold the `keychainAccessGroup` entitlement it's
            // asking for — see that constant's doc comment.
            keychainError = Self.describeKeychainStatus(status)
        }
    }

    private func loadFromKeychain() -> Data? {
        var q = baseQuery()
        q[kSecReturnData as String] = kCFBooleanTrue!
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound {
            keychainError = nil // genuinely no wallet yet — not an error
            return nil
        }
        guard status == errSecSuccess else {
            keychainError = Self.describeKeychainStatus(status)
            return nil
        }
        keychainError = nil
        return out as? Data
    }

    private static func describeKeychainStatus(_ status: OSStatus) -> String {
        let msg = (SecCopyErrorMessageString(status, nil) as String?) ?? "unknown error"
        if status == errSecMissingEntitlement {
            return "Keychain write denied (errSecMissingEntitlement) — this build's signing team doesn't match the keychain-access-groups entitlement (WXDFFW3882.com.openinferencemesh.shared-wallet). Check Signing & Capabilities in Xcode. \(msg)"
        }
        return "Keychain error \(status): \(msg)"
    }

    private func deleteFromKeychain() {
        SecItemDelete(baseQuery() as CFDictionary)
    }

    // MARK: - Local file fallback (device-local only, no iCloud sync)

    // Same directory/naming convention as the Mac/CLI side's FileWalletStorage
    // (swift-sdk/Sources/MeshKit/Wallet.swift) — Application Support, app-scoped
    // subfolder — but this app's sandbox container makes that already private to
    // this app, so there's no cross-app collision risk to name around.
    private var fallbackFileURL: URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? FileManager.default.temporaryDirectory
        return base.appendingPathComponent("mlxmesh", isDirectory: true)
            .appendingPathComponent("wallet-seed-fallback.bin")
    }

    private func saveToFallbackFile(_ data: Data) throws {
        let dir = fallbackFileURL.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        // .completeFileUnlessOpen: unreadable while the device is locked (unlike
        // the default class), but still writable by a background refresh with
        // the file already open — the meaningful protection level on iOS, where
        // Data Protection classes do the real work POSIX chmod bits did on macOS.
        try data.write(to: fallbackFileURL, options: [.atomic, .completeFileProtectionUnlessOpen])
    }

    private func loadFromFallbackFile() -> Data? {
        try? Data(contentsOf: fallbackFileURL)
    }

    private func deleteFallbackFile() {
        try? FileManager.default.removeItem(at: fallbackFileURL)
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
