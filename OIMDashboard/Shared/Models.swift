import Foundation

struct NodeSnapshot: Codable, Identifiable, Hashable {
    let nodeId: String
    let status: String          // "live" | "stale" | "unreachable"
    let geographicHint: String
    let geoLat: Double
    let geoLng: Double
    let reachabilityEndpoint: String
    let declaredMemoryGb: Double
    let committedMemoryGb: Double
    let models: [ModelCapability]
    let measuredToksPerSec: Double
    let hasSecureEnclave: Bool
    let isCluster: Bool
    let clusterDeviceCount: Int?
    let lastSeenAt: String

    var id: String { nodeId }

    func hash(into hasher: inout Hasher) { hasher.combine(nodeId) }
    static func == (lhs: NodeSnapshot, rhs: NodeSnapshot) -> Bool { lhs.nodeId == rhs.nodeId }
}

struct ModelCapability: Codable, Hashable {
    let modelId: String
    let quantization: String
    let runtime: String
    let maxContextTokens: Int
    let isMoe: Bool
}

struct PodHealthDigest: Codable, Identifiable {
    let podId: String
    let regionHint: String
    let coordinatorEndpoint: String
    let servableModelIds: [String]
    let aggregateHealthScore: Double
    let nodeCountApprox: Int
    let totalMemoryGb: Double
    let aggregateToksPerSec: Double

    var id: String { podId }
}

struct TopologyResponse: Codable {
    let pods: [PodHealthDigest]
    let podCount: Int
    let queriedAt: String
}

struct NodesResponse: Codable {
    let podId: String
    let region: String
    let nodes: [NodeSnapshot]
}
