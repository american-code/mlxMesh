import CryptoKit
import Foundation

/// MeshClient — the coordinator-facing client (fast lane, background lane,
/// account/balance, and the privacy-mode encrypted-pointer flow).
///
/// Every call sets `X-OIM-User-ID` when `userID` is configured, so the
/// consumer is actually debited — the root README's documented examples in
/// other languages omit this, silently running in the anonymous/unmetered
/// path. This client always sets it by construction.
public actor MeshClient {
    public let baseURL: URL
    public let apiKey: String?
    public let userID: String?
    private let timeout: TimeInterval
    private let session: URLSession

    public init(baseURL: URL, apiKey: String? = nil, userID: String? = nil, timeout: TimeInterval = 120) {
        self.baseURL = baseURL
        self.apiKey = apiKey
        self.userID = userID
        self.timeout = timeout
        let config = URLSessionConfiguration.default
        config.timeoutIntervalForRequest = timeout
        self.session = URLSession(configuration: config)
    }

    private func headers() -> [String: String] {
        var h: [String: String] = ["Content-Type": "application/json"]
        if let apiKey { h["Authorization"] = "Bearer \(apiKey)" }
        if let userID { h["X-OIM-User-ID"] = userID }
        return h
    }

    private func makeRequest(path: String, body: [String: Any]) throws -> URLRequest {
        var request = URLRequest(url: baseURL.appendingPathComponent(path))
        request.httpMethod = "POST"
        for (k, v) in headers() { request.setValue(v, forHTTPHeaderField: k) }
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        return request
    }

    private func makeGetRequest(path: String, query: [String: String] = [:]) -> URLRequest {
        var components = URLComponents(url: baseURL.appendingPathComponent(path), resolvingAgainstBaseURL: false)!
        if !query.isEmpty {
            components.queryItems = query.map { URLQueryItem(name: $0.key, value: $0.value) }
        }
        var request = URLRequest(url: components.url!)
        request.httpMethod = "GET"
        for (k, v) in headers() { request.setValue(v, forHTTPHeaderField: k) }
        return request
    }

    private func send(_ request: URLRequest) async throws -> [String: Any] {
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw MeshError.api(message: "not an HTTP response", statusCode: 0)
        }
        if let error = MeshErrorMapper.error(for: http, body: data) { throw error }
        return (try? JSONSerialization.jsonObject(with: data) as? [String: Any]) ?? [:]
    }

    private func chatBody(
        model: String, messages: [ChatMessage], maxTokens: Int, sensitivity: Sensitivity,
        maxPricePerUnit: Double, stream: Bool
    ) -> [String: Any] {
        [
            "model": model,
            "messages": messages.map { $0.toDict() },
            "max_tokens": maxTokens,
            "stream": stream,
            "oim_sensitivity": sensitivity.rawValue,
            "oim_max_price_per_unit": maxPricePerUnit,
        ]
    }

    // MARK: - Fast lane

    public func chat(
        model: String, messages: [ChatMessage], maxTokens: Int = 2048,
        sensitivity: Sensitivity = .moderate, maxPricePerUnit: Double = 0
    ) async throws -> ChatCompletion {
        let body = chatBody(model: model, messages: messages, maxTokens: maxTokens, sensitivity: sensitivity, maxPricePerUnit: maxPricePerUnit, stream: false)
        let request = try makeRequest(path: "/v1/chat/completions", body: body)
        return ChatCompletion(raw: try await send(request))
    }

    public func streamChat(
        model: String, messages: [ChatMessage], maxTokens: Int = 2048,
        sensitivity: Sensitivity = .moderate, maxPricePerUnit: Double = 0
    ) -> AsyncThrowingStream<ChatCompletionChunk, Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    let body = self.chatBody(model: model, messages: messages, maxTokens: maxTokens, sensitivity: sensitivity, maxPricePerUnit: maxPricePerUnit, stream: true)
                    let request = try self.makeRequest(path: "/v1/chat/completions", body: body)
                    let (bytes, response) = try await self.session.bytes(for: request)
                    guard let http = response as? HTTPURLResponse else {
                        throw MeshError.api(message: "not an HTTP response", statusCode: 0)
                    }
                    if http.statusCode >= 400 {
                        var errorBody = Data()
                        for try await byte in bytes { errorBody.append(byte) }
                        if let error = MeshErrorMapper.error(for: http, body: errorBody) { throw error }
                    }
                    for try await line in bytes.lines {
                        guard line.hasPrefix("data: ") else { continue }
                        let payload = String(line.dropFirst("data: ".count))
                        if payload == "[DONE]" { break }
                        guard let data = payload.data(using: .utf8),
                              let raw = try JSONSerialization.jsonObject(with: data) as? [String: Any]
                        else { continue }
                        continuation.yield(ChatCompletionChunk(raw: raw))
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    // MARK: - Background lane
    // A genuinely different endpoint set from the fast lane, not a `lane`
    // flag on /v1/chat/completions — the coordinator hardcodes fast lane
    // there. Background jobs are assigned once (sticky-session
    // primary/backup selection persisted server-side) then executed per
    // recurrence cycle.

    public func submitBackgroundJob(
        model: String, jobID: String, sensitivity: Sensitivity = .moderate,
        recurrence: RecurrenceSpec? = nil, allowDecomposition: Bool = false,
        redundancyDepth: Int = 0, maxPricePerUnit: Double = 0
    ) async throws -> BackgroundJob {
        var jobSpec: [String: Any] = [
            "job_id": jobID,
            "requester_id": userID ?? "",
            "model_id": model,
            "lane": "background",
            "sensitivity": sensitivity.rawValue,
            "max_price_per_unit": maxPricePerUnit,
            "redundancy_depth": redundancyDepth,
            "allow_decomposition": allowDecomposition,
        ]
        if let recurrence { jobSpec["recurrence"] = recurrence.toDict() }
        let request = try makeRequest(path: "/jobs/background/assign", body: jobSpec)
        return BackgroundJob(raw: try await send(request))
    }

    public func runBackgroundCycle(_ job: BackgroundJob, messages: [ChatMessage]) async throws -> ChatCompletion {
        let body: [String: Any] = ["job_id": job.jobID, "messages": messages.map { $0.toDict() }]
        let request = try makeRequest(path: "/jobs/background/execute", body: body)
        return ChatCompletion(raw: try await send(request))
    }

    // MARK: - Account

    public func balance() async throws -> Balance {
        guard let userID else { throw MeshClientError.userIDRequired("balance()") }
        let request = makeGetRequest(path: "/users/\(userID)/balance")
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw MeshError.api(message: "not an HTTP response", statusCode: 0)
        }
        if let error = MeshErrorMapper.error(for: http, body: data) { throw error }
        return try JSONDecoder().decode(Balance.self, from: data)
    }

    /// Mines the proof-of-work nonce automatically. Idempotent server-side —
    /// a second claim returns the existing grant, not an error.
    public func claimStartupGrant(difficultyBits: Int? = nil) async throws -> CreditEntry {
        guard let userID else { throw MeshClientError.userIDRequired("claimStartupGrant()") }
        let bits = difficultyBits ?? ProofOfWork.defaultGrantPoWBits
        let nonce = ProofOfWork.mine(userID: userID, difficultyBits: bits)
        let request = try makeRequest(path: "/users/\(userID)/startup-grant", body: ["nonce": nonce])
        let raw = try await send(request)
        if raw["status"] as? String == "already_claimed" {
            return CreditEntry(
                userID: userID, origin: "grant", amount: raw["amount"] as? Double ?? 0,
                grantedOrEarnedAt: "", sourceReference: "startup-grant"
            )
        }
        let data = try JSONSerialization.data(withJSONObject: raw)
        return try JSONDecoder().decode(CreditEntry.self, from: data)
    }

    // MARK: - Privacy mode (encrypted-pointer)

    /// Pins a node (30s TTL) whose ecdhPublicKey you then encrypt to —
    /// required because the ciphertext can only be decrypted by that one
    /// node's private key. Not compatible with streaming.
    public func reserveNode(model: String, sensitivity: Sensitivity = .moderate) async throws -> Reservation {
        let request = try makeRequest(path: "/v1/reserve-node", body: ["model": model, "sensitivity": sensitivity.rawValue])
        let data = try JSONSerialization.data(withJSONObject: try await send(request))
        return try JSONDecoder().decode(Reservation.self, from: data)
    }

    /// Encrypts `messages` to the reserved node's key and submits the
    /// pointer. Does NOT host the ciphertext for you — `fetchURL` must
    /// already serve the bytes this computes (see PayloadEncryption.encrypt)
    /// over HTTP(S) reachable by the assigned node; hosting is inherently
    /// application-specific. Streaming is not available on this path — a
    /// reservation always returns buffered.
    public func submitEncrypted(
        _ reservation: Reservation, messages: [ChatMessage], fetchURL: URL,
        payloadHash: String = "", maxTokens: Int = 2048
    ) async throws -> ChatCompletion {
        let plaintext = try JSONSerialization.data(withJSONObject: messages.map { $0.toDict() })
        guard let recipientKeyData = Data(base64Encoded: reservation.ecdhPublicKey) else {
            throw MeshClientError.invalidReservationKey
        }
        // x963Representation: the coordinator sends Go's SEC1 uncompressed
        // point (0x04||X||Y, 65 bytes) — see PayloadEncryption.swift's doc
        // comment for why .rawRepresentation (64 bytes, no prefix) is wrong here.
        let recipientKey = try P256.KeyAgreement.PublicKey(x963Representation: recipientKeyData)
        let encrypted = try PayloadEncryption.encrypt(plaintext: plaintext, recipientPublicKey: recipientKey)

        let body: [String: Any] = [
            "model": "",
            "messages": [],
            "max_tokens": maxTokens,
            "oim_reservation_id": reservation.reservationID,
            "oim_payload_hash": payloadHash,
            "oim_payload_fetch_url": fetchURL.absoluteString,
            "oim_ephemeral_public_key": encrypted.ephemeralPublicKeyBase64,
        ]
        let request = try makeRequest(path: "/v1/chat/completions", body: body)
        return ChatCompletion(raw: try await send(request))
    }
}

public enum MeshClientError: Error, LocalizedError {
    case userIDRequired(String)
    case invalidReservationKey

    public var errorDescription: String? {
        switch self {
        case .userIDRequired(let method): return "\(method) requires userID to be set on the client"
        case .invalidReservationKey: return "reservation.ecdhPublicKey is not valid base64"
        }
    }
}
