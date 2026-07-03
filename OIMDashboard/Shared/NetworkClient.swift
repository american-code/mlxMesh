import Foundation

enum NetworkClient {
    static var directoryURL = "http://192.168.1.135:9100"

    static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return d
    }()

    static func fetchTopology() async throws -> TopologyResponse {
        let url = URL(string: "\(directoryURL)/topology")!
        let (data, _) = try await URLSession.shared.data(from: url)
        return try decoder.decode(TopologyResponse.self, from: data)
    }

    static func fetchNodes(coordinatorURL: String) async throws -> NodesResponse {
        let url = URL(string: "\(resolvedCoordinator(coordinatorURL))/nodes")!
        let (data, _) = try await URLSession.shared.data(from: url)
        return try decoder.decode(NodesResponse.self, from: data)
    }

    // resolvedCoordinator rewrites a coordinator endpoint's host to whatever host
    // we actually reached the directory on — but ONLY when the coordinator
    // advertises a loopback address.
    //
    // Why this exists: the Docker coordinators advertise themselves with
    // --public-url=http://localhost:9000 (and :9001). That's fine for the web
    // dashboard running on the same Mac, but from a physical iPhone/iPad,
    // "localhost" is the device itself, so /nodes, /balance, and query calls all
    // fail — the app shows pods (from the directory digest) but zero node data.
    // Since every service runs on the same machine, substituting the directory's
    // host (e.g. 192.168.1.135) for the coordinator's loopback host makes the
    // whole app work over LAN with the user only changing ONE field in Settings.
    // Non-loopback coordinator URLs are left untouched.
    static func resolvedCoordinator(_ endpoint: String) -> String {
        guard var comps = URLComponents(string: endpoint) else { return endpoint }
        let host = comps.host ?? ""
        let isLoopback = host == "localhost" || host == "127.0.0.1" || host == "::1"
        guard isLoopback,
              let dirComps = URLComponents(string: directoryURL),
              let dirHost = dirComps.host,
              dirHost != "localhost", dirHost != "127.0.0.1", dirHost != "::1"
        else { return endpoint }
        comps.host = dirHost
        return comps.string ?? endpoint
    }

    // MARK: - Balance

    static func fetchBalance(coordinatorURL: String, userId: String) async throws -> Balance {
        let url = URL(string: "\(resolvedCoordinator(coordinatorURL))/users/\(userId)/balance")!
        let (data, _) = try await URLSession.shared.data(from: url)
        return try decoder.decode(Balance.self, from: data)
    }

    static func claimStartupGrant(coordinatorURL: String, userId: String, nonce: UInt64) async throws {
        var req = URLRequest(url: URL(string: "\(resolvedCoordinator(coordinatorURL))/users/\(userId)/startup-grant")!)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["nonce": nonce])
        _ = try await URLSession.shared.data(for: req)
    }

    // MARK: - Wallet (portable account identity → credit consolidation)

    // requestChallenge asks the coordinator for a one-time nonce the account key
    // must sign to prove ownership. Mirrors POST /account/challenge {address}.
    static func requestChallenge(coordinatorURL: String, address: String) async throws -> String {
        var req = URLRequest(url: URL(string: "\(resolvedCoordinator(coordinatorURL))/account/challenge")!)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["address": address])
        let (data, resp) = try await URLSession.shared.data(for: req)
        try Self.ensureOK(resp, data)
        let ch = try decoder.decode(WalletChallenge.self, from: data)
        return ch.nonce
    }

    // authenticate submits the signed challenge; on success the coordinator mints
    // a session oim_ API key bound to the address. POST /account/auth.
    static func authenticate(coordinatorURL: String, address: String, nonce: String,
                             publicKey: Data, signature: Data) async throws -> String {
        var req = URLRequest(url: URL(string: "\(resolvedCoordinator(coordinatorURL))/account/auth")!)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: [
            "address": address,
            "nonce": nonce,
            "public_key": publicKey.base64EncodedString(),
            "signature": signature.base64EncodedString(),
        ])
        let (data, resp) = try await URLSession.shared.data(for: req)
        try Self.ensureOK(resp, data)
        let out = try decoder.decode(WalletAuthResponse.self, from: data)
        return out.apiKey
    }

    // linkDevice binds a contributing device's node ID to this account so its
    // earnings consolidate here. Account-signed. POST /account/{address}/link-device.
    static func linkDevice(coordinatorURL: String, address: String, deviceNodeID: String,
                           accountPublicKey: Data, signature: Data) async throws {
        var req = URLRequest(url: URL(string: "\(resolvedCoordinator(coordinatorURL))/account/\(address)/link-device")!)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: [
            "device_node_id": deviceNodeID,
            "account_public_key": accountPublicKey.base64EncodedString(),
            "signature": signature.base64EncodedString(),
        ])
        let (data, resp) = try await URLSession.shared.data(for: req)
        try Self.ensureOK(resp, data)
    }

    // ensureOK surfaces the coordinator's JSON {"error": "..."} body on non-2xx
    // (challenge/auth/link all return 400/401 with a reason), instead of the
    // opaque success URLSession gives for any HTTP status.
    private static func ensureOK(_ resp: URLResponse, _ data: Data) throws {
        guard let http = resp as? HTTPURLResponse else { return }
        guard (200..<300).contains(http.statusCode) else {
            if let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let msg = obj["error"] as? String {
                throw NetworkError.message(msg)
            }
            throw NetworkError.message("HTTP \(http.statusCode)")
        }
    }

    // MARK: - Try the mesh

    // submitTestQuery sends a real inference job through the mesh and returns the
    // reply plus this-request-measured throughput/latency, exactly like the web
    // dashboard's "Try the mesh" (coordinator tags oim_tokens_per_sec /
    // oim_latency_ms on the response).
    static func submitTestQuery(coordinatorURL: String, prompt: String, model: String = "llama-3.2-3b") async throws -> TestQueryResult {
        var req = URLRequest(url: URL(string: "\(resolvedCoordinator(coordinatorURL))/v1/chat/completions")!)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: [
            "model": model,
            "messages": [["role": "user", "content": prompt]],
            "max_tokens": 256,
        ])
        let (data, _) = try await URLSession.shared.data(for: req)
        let obj = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        let choices = obj["choices"] as? [[String: Any]]
        let message = choices?.first?["message"] as? [String: Any]
        let content = message?["content"] as? String ?? "(empty response)"
        return TestQueryResult(
            content: content,
            servedByNodeId: obj["oim_served_by_node_id"] as? String,
            lane: obj["oim_lane"] as? String,
            tokensPerSec: obj["oim_tokens_per_sec"] as? Double,
            latencyMs: (obj["oim_latency_ms"] as? Int) ?? (obj["oim_latency_ms"] as? Double).map { Int($0) }
        )
    }
}

enum NetworkError: LocalizedError {
    case message(String)
    var errorDescription: String? { if case .message(let m) = self { return m }; return nil }
}

// Wire shapes for the coordinator's wallet endpoints (snake_case → camelCase via
// the shared decoder's keyDecodingStrategy).
struct WalletChallenge: Codable {
    let address: String
    let nonce: String
    let expiresAt: String
}

struct WalletAuthResponse: Codable {
    let address: String
    let apiKey: String
}

struct TestQueryResult {
    let content: String
    let servedByNodeId: String?
    let lane: String?
    let tokensPerSec: Double?
    let latencyMs: Int?
}
