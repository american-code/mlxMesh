import Foundation

enum NetworkClient {
    static var directoryURL = "http://localhost:9100"

    private static let decoder: JSONDecoder = {
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
        let url = URL(string: "\(coordinatorURL)/nodes")!
        let (data, _) = try await URLSession.shared.data(from: url)
        return try decoder.decode(NodesResponse.self, from: data)
    }
}
