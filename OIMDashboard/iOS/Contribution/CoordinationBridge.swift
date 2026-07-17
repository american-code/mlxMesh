import Foundation
import CryptoKit

/// CoordinationBridge is the iOS device's participation in the mesh as a
/// COORDINATION + SECURITY layer — NOT an inference compute node.
///
/// Why not compute: Exo (the mesh's inference backend) is a Python + MLX daemon
/// that runs on macOS/Linux only — it does not run on iOS/iPadOS (confirmed
/// against exo-explore/exo). iOS devices were never meant to serve inference.
/// Their job is to add a security layer to routing via encrypted payload
/// pointers, and this layer is purely ADDITIVE: when no iOS device is present,
/// the coordinator routes normally (see coordinator/hintvalidator.go — a nil
/// hint forces full coordinator classification).
///
/// This role runs on any modern iPad (e.g. a 2018 A12X iPad Pro), not just
/// M-series: the on-device classifier runs on the Neural Engine and the crypto
/// is CryptoKit — neither needs the GPU headroom an M-series chip provides.
final class CoordinationBridge {

    private var podEndpoint: URL
    private let session: URLSession
    private let acceptingLock = NSLock()
    private var accepting = true

    // The network coordinator this device announces to. Set from the resolved
    // topology (the directory the user configured in Settings), NOT localhost —
    // an iOS device runs from anywhere and reaches the network over the internet,
    // so localhost would mean "this iPad" and announce into the void.
    init(podEndpoint: URL? = nil) {
        self.podEndpoint = podEndpoint ?? URL(string: "http://localhost:9000")!
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 10
        self.session = URLSession(configuration: cfg)
    }

    /// Repoints the bridge at the resolved network coordinator before announcing.
    func setCoordinator(_ url: URL) { podEndpoint = url }

    // MARK: - Secure submission (the security layer, client-side)

    /// Prepares a job the secure way: classify on-device, encrypt the payload to
    /// the recipient node's key, host the ciphertext locally, and return the
    /// HintPacket (metadata only). The coordinator receives the packet and NEVER
    /// the plaintext. `recipientPublicKey` is the assigned node's P-256 key
    /// (known after the coordinator assigns a node; for a self-hosted pointer
    /// flow it can be an ephemeral recipient the node reconstructs).
    func prepareSecureJob(
        promptText: String,
        plaintext: Data,
        requesterId: String,
        recipientPublicKey: P256.KeyAgreement.PublicKey,
        router: OnDeviceRouter,
        lane: String = "fast"
    ) async throws -> HintPacket {
        let hint = await router.classify(queryText: promptText)
        let bundle = try PayloadEncryption.encrypt(plaintext: plaintext, recipientPublicKey: recipientPublicKey)
        let fetchURL = try await PayloadEncryption.storeLocally(ciphertext: bundle.ciphertext)
        return HintPacket(
            requesterId: requesterId,
            hint: hint,
            payloadHash: bundle.payloadHash,
            payloadFetchURL: fetchURL,
            ephemeralPublicKeyBase64: bundle.ephemeralPublicKeyData.base64EncodedString(),
            lane: lane
        )
    }

    // MARK: - Participation announce/withdraw (best-effort)

    /// Announces this device as an available encrypted-pointer host for the pod.
    /// Best-effort and tolerant of a missing endpoint: the coordination layer is
    /// additive, so a failed announce simply means normal (non-iOS) routing
    /// continues. TODO(coordinator): add POST /coordination/announce so pods can
    /// prefer iOS-hosted pointer custody when available.
    func announceParticipation(deviceId: String, geographicHint: String) async {
        var req = URLRequest(url: podEndpoint.appendingPathComponent("coordination/announce"))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try? JSONSerialization.data(withJSONObject: [
            "device_id": deviceId, "role": "pointer_host", "is_mobile": true,
            "geographic_hint": geographicHint,
        ])
        _ = try? await session.data(for: req)
    }

    /// Fetches this device's coordinator-observed served-pointer count. The
    /// coordinator is the source of truth: it increments the counter whenever it
    /// attributes an encrypted pointer to this device, so the UI reflects real
    /// credited work rather than a local guess (a node fetching from this device's
    /// LAN host can't be observed from here, e.g. across a Docker sim). Returns
    /// nil on any failure — the caller keeps the last known value.
    func fetchPointersServed(deviceId: String) async -> Int? {
        let req = URLRequest(url: podEndpoint.appendingPathComponent("nodes"))
        guard let (data, _) = try? await session.data(for: req),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let list = obj["coordination_nodes"] as? [[String: Any]] else { return nil }
        for p in list where p["device_id"] as? String == deviceId {
            if let n = p["pointers_served"] as? Int { return n }
            if let n = p["pointers_served"] as? NSNumber { return n.intValue }
        }
        return nil
    }

    /// Clean withdrawal — never throws (runs during shutdown).
    func withdrawParticipation(deviceId: String) async {
        var req = URLRequest(url: podEndpoint.appendingPathComponent("coordination/withdraw"))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try? JSONSerialization.data(withJSONObject: ["device_id": deviceId])
        _ = try? await session.data(for: req)
    }

    // MARK: - Drain gate

    func setAcceptingPointers(_ value: Bool) {
        acceptingLock.lock(); accepting = value; acceptingLock.unlock()
    }
    var isAcceptingPointers: Bool {
        acceptingLock.lock(); defer { acceptingLock.unlock() }; return accepting
    }
}
