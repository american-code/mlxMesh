import Foundation

/// Model discovery against the directory service. The coordinator itself
/// has no /topology endpoint — that's the separate `oim-directory` binary,
/// which each coordinator pod periodically reports its signed
/// PodHealthDigest to. A third-party app discovers which pods currently
/// serve a model BEFORE submitting a job by querying the directory, then
/// talks to that pod's own coordinatorEndpoint for the actual
/// /v1/chat/completions call.
public actor MeshDirectory {
    public let baseURL: URL
    private let timeout: TimeInterval
    private let session: URLSession

    public init(baseURL: URL, timeout: TimeInterval = 10) {
        self.baseURL = baseURL
        self.timeout = timeout
        let config = URLSessionConfiguration.default
        config.timeoutIntervalForRequest = timeout
        self.session = URLSession(configuration: config)
    }

    private func get(path: String, query: [String: String] = [:]) async throws -> Data {
        var components = URLComponents(url: baseURL.appendingPathComponent(path), resolvingAgainstBaseURL: false)!
        if !query.isEmpty {
            components.queryItems = query.map { URLQueryItem(name: $0.key, value: $0.value) }
        }
        let (data, response) = try await session.data(from: components.url!)
        guard let http = response as? HTTPURLResponse else {
            throw MeshError.api(message: "not an HTTP response", statusCode: 0)
        }
        if let error = MeshErrorMapper.error(for: http, body: data) { throw error }
        return data
    }

    /// GET /topology — every pod the directory currently knows about.
    public func topology() async throws -> [PodHealthDigest] {
        struct Response: Codable { let pods: [PodHealthDigest] }
        let data = try await get(path: "/topology")
        return try JSONDecoder().decode(Response.self, from: data).pods
    }

    /// GET /pods?model_id=... — pod IDs advertising this model. Aggregate-only
    /// (no quantization filter at this layer) — check the specific pod's own
    /// GET /nodes for per-node/per-quantization availability.
    public func findPods(modelID: String) async throws -> [String] {
        struct Response: Codable {
            let matchingPods: [String]
            enum CodingKeys: String, CodingKey { case matchingPods = "matching_pods" }
        }
        let data = try await get(path: "/pods", query: ["model_id": modelID])
        return try JSONDecoder().decode(Response.self, from: data).matchingPods
    }
}
