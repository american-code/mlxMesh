import CryptoKit
import Foundation

/// MeshClient — the coordinator-facing client (fast lane, background lane,
/// account/balance, and the privacy-mode encrypted-pointer flow).
///
/// Every call sets `X-OIM-User-ID` when `userID` is configured, so the
/// consumer is actually debited — the root README's documented examples in
/// other languages omit this, silently running in the anonymous/unmetered
/// path. This client always sets it by construction.
///
/// Every BILLABLE call (chat/streamChat/submitBackgroundJob/etc.) refuses
/// outright — before making any request — unless a credential is
/// configured: either a Wallet (see ensureCredential) or a pre-existing
/// apiKey/userID pair. A client with a Wallet mints its own apiKey on first
/// use via the real challenge/response account-auth flow (see
/// authenticate()), and transparently re-authenticates once on a 401 (see
/// withReauth) — the same one-retry pattern the dashboard's
/// runTestQueryWithAutoAuth already uses.
public actor MeshClient {
    public let baseURL: URL
    public private(set) var apiKey: String?
    public let userID: String?
    private let wallet: Wallet?
    private let timeout: TimeInterval
    private let session: URLSession

    public init(
        baseURL: URL, apiKey: String? = nil, userID: String? = nil, wallet: Wallet? = nil, timeout: TimeInterval = 120
    ) throws {
        self.baseURL = baseURL
        self.wallet = wallet
        var resolvedUserID = userID
        if let wallet {
            if let userID, userID != wallet.address {
                throw MeshClientError.conflictingWalletAndUserID(userID: userID, walletAddress: wallet.address)
            }
            resolvedUserID = wallet.address
        }
        self.apiKey = apiKey
        self.userID = resolvedUserID
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

    // MARK: - Wallet auth

    /// Runs the real challenge/response account-auth flow (POST
    /// /account/challenge -> sign -> POST /account/auth) and stores the
    /// resulting apiKey. Requires a wallet to have been passed to the
    /// initializer. Called automatically by billable methods on first use
    /// and again on a 401 — most callers never need to call this directly.
    @discardableResult
    public func authenticate() async throws -> String {
        guard let wallet else {
            throw MeshClientError.noCredentialConfigured("authenticate()")
        }
        let challengeRequest = try makeRequest(path: "/account/challenge", body: ["address": wallet.address])
        let challenge = try await send(challengeRequest)
        guard let nonce = challenge["nonce"] as? String else {
            throw MeshError.api(message: "malformed /account/challenge response", statusCode: 0)
        }
        let message = Data("oim-account-auth:\(wallet.address):\(nonce)".utf8)
        let signature = try wallet.sign(message)
        let authRequest = try makeRequest(
            path: "/account/auth",
            body: [
                "address": wallet.address,
                "nonce": nonce,
                "public_key": wallet.publicKeyBase64,
                "signature": signature.base64EncodedString(),
            ]
        )
        let authResponse = try await send(authRequest)
        guard let key = authResponse["api_key"] as? String else {
            throw MeshError.api(message: "malformed /account/auth response", statusCode: 0)
        }
        self.apiKey = key
        return key
    }

    /// Refuses to proceed with a billable call unless a credential is
    /// available: an existing apiKey, or a wallet that can mint one. Runs
    /// BEFORE any request is sent — the fix for a client silently sending an
    /// unauthenticated (and possibly free, or possibly rejected depending on
    /// the deployment) job.
    private func ensureCredential() async throws {
        if apiKey != nil { return }
        if wallet != nil {
            try await authenticate()
            return
        }
        throw MeshClientError.noCredentialConfigured(
            "pass apiKey:/userID: or wallet: to MeshClient(...) before submitting a billable request"
        )
    }

    /// Calls operation(); on a 401 with a wallet configured, re-authenticates
    /// once and retries exactly once — mirrors the dashboard's
    /// runTestQueryWithAutoAuth.
    private func withReauth<T>(_ operation: () async throws -> T) async throws -> T {
        do {
            return try await operation()
        } catch MeshError.api(_, let statusCode) where statusCode == 401 && wallet != nil {
            try await authenticate()
            return try await operation()
        }
    }

    /// Whether a wallet is configured — used by streamChat's manual retry
    /// (it can't use withReauth directly since the "request" there is a
    /// stream-open, not a throwing async call returning a value).
    private func walletConfigured() -> Bool {
        wallet != nil
    }

    /// Test-only hook to simulate a stale/invalid apiKey (e.g. the
    /// coordinator restarted with a fresh in-memory key store) without
    /// exposing a public mutable setter on a value that should normally only
    /// ever be assigned by authenticate(). Internal, not public — reachable
    /// from tests via @testable import.
    func setAPIKeyForTesting(_ key: String) {
        self.apiKey = key
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
        try await ensureCredential()
        let body = chatBody(model: model, messages: messages, maxTokens: maxTokens, sensitivity: sensitivity, maxPricePerUnit: maxPricePerUnit, stream: false)
        let raw = try await withReauth {
            let request = try self.makeRequest(path: "/v1/chat/completions", body: body)
            return try await self.send(request)
        }
        return ChatCompletion(raw: raw)
    }

    /// Opens the streaming POST and returns the raw byte stream + response —
    /// factored out so streamChat can retry it once on a 401 before any
    /// bytes are relayed to the caller.
    private func openChatStream(body: [String: Any]) async throws -> (URLSession.AsyncBytes, HTTPURLResponse) {
        let request = try makeRequest(path: "/v1/chat/completions", body: body)
        let (bytes, response) = try await session.bytes(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw MeshError.api(message: "not an HTTP response", statusCode: 0)
        }
        return (bytes, http)
    }

    public func streamChat(
        model: String, messages: [ChatMessage], maxTokens: Int = 2048,
        sensitivity: Sensitivity = .moderate, maxPricePerUnit: Double = 0
    ) -> AsyncThrowingStream<ChatCompletionChunk, Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    try await self.ensureCredential()
                    let body = self.chatBody(model: model, messages: messages, maxTokens: maxTokens, sensitivity: sensitivity, maxPricePerUnit: maxPricePerUnit, stream: true)
                    var (bytes, http) = try await self.openChatStream(body: body)
                    if http.statusCode == 401, await self.walletConfigured() {
                        try await self.authenticate()
                        (bytes, http) = try await self.openChatStream(body: body)
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
        try await ensureCredential()
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
        let raw = try await withReauth {
            let request = try self.makeRequest(path: "/jobs/background/assign", body: jobSpec)
            return try await self.send(request)
        }
        return BackgroundJob(raw: raw)
    }

    public func runBackgroundCycle(_ job: BackgroundJob, messages: [ChatMessage]) async throws -> ChatCompletion {
        try await ensureCredential()
        let body: [String: Any] = ["job_id": job.jobID, "messages": messages.map { $0.toDict() }]
        let raw = try await withReauth {
            let request = try self.makeRequest(path: "/jobs/background/execute", body: body)
            return try await self.send(request)
        }
        return ChatCompletion(raw: raw)
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
        try await ensureCredential()
        let raw = try await withReauth {
            let request = try self.makeRequest(path: "/v1/reserve-node", body: ["model": model, "sensitivity": sensitivity.rawValue])
            return try await self.send(request)
        }
        let data = try JSONSerialization.data(withJSONObject: raw)
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
        try await ensureCredential()
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
        let raw = try await withReauth {
            let request = try self.makeRequest(path: "/v1/chat/completions", body: body)
            return try await self.send(request)
        }
        return ChatCompletion(raw: raw)
    }
}

public enum MeshClientError: Error, LocalizedError {
    case userIDRequired(String)
    case invalidReservationKey
    /// A billable call was attempted with neither a Wallet nor a
    /// pre-existing apiKey configured — refused before any HTTP request.
    case noCredentialConfigured(String)
    /// `wallet:` and an explicit conflicting `userID:` were both passed to
    /// the initializer — omit userID when passing a wallet, it's derived
    /// automatically from wallet.address.
    case conflictingWalletAndUserID(userID: String, walletAddress: String)

    public var errorDescription: String? {
        switch self {
        case .userIDRequired(let method): return "\(method) requires userID to be set on the client"
        case .invalidReservationKey: return "reservation.ecdhPublicKey is not valid base64"
        case .noCredentialConfigured(let detail):
            return "No credential configured — \(detail)"
        case .conflictingWalletAndUserID(let userID, let walletAddress):
            return "userID=\"\(userID)\" conflicts with wallet.address=\"\(walletAddress)\" — omit userID when passing a wallet"
        }
    }
}
