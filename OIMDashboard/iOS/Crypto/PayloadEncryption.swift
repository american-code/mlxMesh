import CryptoKit
import Foundation
import Network

struct EncryptedPayloadBundle {
    let ciphertext: Data
    let payloadHash: String           // SHA-256 of PLAINTEXT — content address
    let ephemeralPublicKeyData: Data  // included in HintPacket for node ECDH
    let fetchURL: String              // where the assigned node retrieves ciphertext
}

/// Client-side payload encryption. The plaintext NEVER leaves encrypt(); the
/// coordinator only ever receives the metadata in HintPacket (hash + fetch URL +
/// ephemeral public key). The assigned node fetches the ciphertext and decrypts
/// with its own private key via ECDH.
enum PayloadEncryption {

    /// HKDF context string — binds the derived key to this protocol/version so a
    /// key derived here can't be repurposed by another scheme sharing the curve.
    private static let hkdfInfo = Data("oim-payload-v1".utf8)

    enum CryptoError: Error { case sealFailed }

    static func encrypt(
        plaintext: Data,
        recipientPublicKey: P256.KeyAgreement.PublicKey
    ) throws -> EncryptedPayloadBundle {
        // 1. Fresh ephemeral keypair per job (never reused).
        let ephemeral = P256.KeyAgreement.PrivateKey()
        // 2. ECDH shared secret.
        let shared = try ephemeral.sharedSecretFromKeyAgreement(with: recipientPublicKey)
        // 3. Derive AES-256 key via HKDF-SHA256.
        let key = shared.hkdfDerivedSymmetricKey(
            using: SHA256.self, salt: Data(), sharedInfo: hkdfInfo, outputByteCount: 32)
        // 4. AES-GCM seal (combined = nonce || ciphertext || tag).
        let sealed = try AES.GCM.seal(plaintext, using: key)
        guard let combined = sealed.combined else { throw CryptoError.sealFailed }
        // 5. Content address = hash of the PLAINTEXT.
        let hash = Data(SHA256.hash(data: plaintext)).hexString
        // 6/7. Ephemeral private key is discarded when this scope exits.
        return EncryptedPayloadBundle(
            ciphertext: combined,
            payloadHash: hash,
            ephemeralPublicKeyData: ephemeral.publicKey.rawRepresentation,
            fetchURL: "" // filled in by storeLocally
        )
    }

    /// Inverse of encrypt(), run by the assigned node (including an iPad acting as
    /// a contribution node). Recovers the plaintext from ciphertext using the
    /// node's private key + the ephemeral public key.
    static func decrypt(
        ciphertext: Data,
        ephemeralPublicKeyData: Data,
        recipientPrivateKey: P256.KeyAgreement.PrivateKey
    ) throws -> Data {
        let ephemeralPub = try P256.KeyAgreement.PublicKey(rawRepresentation: ephemeralPublicKeyData)
        let shared = try recipientPrivateKey.sharedSecretFromKeyAgreement(with: ephemeralPub)
        let key = shared.hkdfDerivedSymmetricKey(
            using: SHA256.self, salt: Data(), sharedInfo: hkdfInfo, outputByteCount: 32)
        let box = try AES.GCM.SealedBox(combined: ciphertext)
        return try AES.GCM.open(box, using: key)
    }

    /// Stores ciphertext so the assigned node can fetch it, returning the fetch
    /// URL. v1: serve from LocalPayloadServer on the device's LAN IP. The hash IS
    /// the address — no separate index. Auto-expires after 5 min / after fetch.
    static func storeLocally(ciphertext: Data, hash: String) async throws -> String {
        let port = try LocalPayloadServer.shared.start()
        LocalPayloadServer.shared.servePayload(hash: hash, ciphertext: ciphertext)
        let ip = LocalNetwork.primaryIPv4() ?? "127.0.0.1"
        return "http://\(ip):\(port)/payload/\(hash)"
    }
}

extension Data {
    var hexString: String { map { String(format: "%02x", $0) }.joined() }
}

/// Minimal LAN address helper.
enum LocalNetwork {
    /// Best-effort primary non-loopback IPv4, for building the payload fetch URL
    /// the assigned node uses. Returns nil if none found (caller falls back).
    static func primaryIPv4() -> String? {
        var address: String?
        var ifaddr: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&ifaddr) == 0, let first = ifaddr else { return nil }
        defer { freeifaddrs(ifaddr) }
        for ptr in sequence(first: first, next: { $0.pointee.ifa_next }) {
            let flags = Int32(ptr.pointee.ifa_flags)
            guard (flags & IFF_UP) == IFF_UP, (flags & IFF_LOOPBACK) == 0 else { continue }
            let family = ptr.pointee.ifa_addr.pointee.sa_family
            guard family == UInt8(AF_INET) else { continue }
            let name = String(cString: ptr.pointee.ifa_name)
            guard name == "en0" || name == "en1" else { continue }
            var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            if getnameinfo(ptr.pointee.ifa_addr, socklen_t(ptr.pointee.ifa_addr.pointee.sa_len),
                           &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST) == 0 {
                address = String(cString: host)
                break
            }
        }
        return address
    }
}

/// Minimal local HTTP server that serves encrypted payloads to assigned nodes.
/// Runs only while a job is in-flight. Serves exactly `GET /payload/{hash}`;
/// every other route is 404. Entries auto-remove after first successful GET or a
/// 5-minute timeout. Uses Network.framework — no third-party HTTP dependency.
final class LocalPayloadServer {

    static let shared = LocalPayloadServer()
    private init() {}

    private var listener: NWListener?
    private var port: UInt16 = 0
    private let queue = DispatchQueue(label: "oim.payloadserver")
    private var payloads: [String: Data] = [:]
    private var expiryTimers: [String: DispatchSourceTimer] = [:]

    /// Idempotent start on an OS-assigned port; returns the bound port. Blocks
    /// the caller only until the listener reports ready (typically a few ms),
    /// via a semaphore signaled from the listener's own callback queue — the
    /// previous version busy-waited ON that same serial queue, which starved the
    /// ready callback and left the port stuck at 0.
    func start() throws -> UInt16 {
        if let existing = currentPort(), existing != 0 { return existing }

        let l = try NWListener(using: .tcp)
        let ready = DispatchSemaphore(value: 0)
        l.newConnectionHandler = { [weak self] conn in self?.handle(conn) }
        l.stateUpdateHandler = { [weak self] state in
            switch state {
            case .ready:
                self?.queue.async { self?.port = l.port?.rawValue ?? 0; ready.signal() }
            case .failed, .cancelled:
                ready.signal()
            default:
                break
            }
        }
        queue.async { self.listener = l }
        l.start(queue: queue)
        _ = ready.wait(timeout: .now() + 3) // off the listener's queue — no starvation
        return currentPort() ?? 0
    }

    private func currentPort() -> UInt16? {
        queue.sync { port }
    }

    func servePayload(hash: String, ciphertext: Data) {
        queue.async { [weak self] in
            guard let self else { return }
            self.payloads[hash] = ciphertext
            self.scheduleExpiry(hash: hash)
        }
    }

    func stop() {
        queue.async { [weak self] in
            guard let self else { return }
            self.listener?.cancel()
            self.listener = nil
            self.port = 0
            self.payloads.removeAll()
            self.expiryTimers.values.forEach { $0.cancel() }
            self.expiryTimers.removeAll()
        }
    }

    // MARK: - internals (all on `queue`)

    private func scheduleExpiry(hash: String) {
        expiryTimers[hash]?.cancel()
        let timer = DispatchSource.makeTimerSource(queue: queue)
        timer.schedule(deadline: .now() + 300) // 5 minutes
        timer.setEventHandler { [weak self] in self?.remove(hash: hash) }
        timer.resume()
        expiryTimers[hash] = timer
    }

    private func remove(hash: String) {
        payloads[hash] = nil
        expiryTimers[hash]?.cancel()
        expiryTimers[hash] = nil
    }

    private func handle(_ conn: NWConnection) {
        conn.start(queue: queue)
        conn.receive(minimumIncompleteLength: 1, maximumLength: 8192) { [weak self] data, _, _, _ in
            guard let self, let data, let request = String(data: data, encoding: .utf8) else {
                conn.cancel(); return
            }
            let response = self.route(request)
            conn.send(content: response, completion: .contentProcessed { _ in conn.cancel() })
        }
    }

    /// Parses the request line and returns a full HTTP response. Only
    /// `GET /payload/{hash}` for a known hash returns 200; everything else 404.
    private func route(_ request: String) -> Data {
        let firstLine = request.split(separator: "\r\n", maxSplits: 1).first.map(String.init) ?? ""
        let parts = firstLine.split(separator: " ")
        guard parts.count >= 2, parts[0] == "GET" else { return httpResponse(status: "404 Not Found", body: Data()) }
        let path = String(parts[1])
        let prefix = "/payload/"
        guard path.hasPrefix(prefix) else { return httpResponse(status: "404 Not Found", body: Data()) }
        let hash = String(path.dropFirst(prefix.count))
        guard let ciphertext = payloads[hash] else { return httpResponse(status: "404 Not Found", body: Data()) }
        // Content is single-fetch: remove after serving.
        remove(hash: hash)
        return httpResponse(status: "200 OK", body: ciphertext, contentType: "application/octet-stream")
    }

    private func httpResponse(status: String, body: Data, contentType: String = "text/plain") -> Data {
        let header = "HTTP/1.1 \(status)\r\nContent-Type: \(contentType)\r\nContent-Length: \(body.count)\r\nConnection: close\r\n\r\n"
        return Data(header.utf8) + body
    }
}
