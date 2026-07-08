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
    let models: [ModelCapability]?  // Optional to handle null from nodes with no models
    let measuredToksPerSec: Double
    let hasSecureEnclave: Bool   // self-declared by the node — informational only
    let enclaveAttested: Bool    // coordinator-verified Secure Enclave proof — trust this, not hasSecureEnclave
    let isCluster: Bool
    let clusterDeviceCount: Int?
    // One coarse chip family per cluster device (e.g. "Apple M1") — no hostnames,
    // no exact chip variant. Empty/absent for non-cluster nodes.
    let clusterChipFamilies: [String]?
    let lastSeenAt: String
    let inFlightJobs: Int
    // Decorative/seed capacity, not a real operator's hardware — absent/false
    // for genuine contributor nodes. See pickDemoModel in TryMeshView.swift.
    let simulated: Bool?

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
    // Whether Exo currently has an active inference instance for this model —
    // distinct from merely being downloaded to disk (the only thing that
    // gates whether a model appears in this list at all). Optional/absent on
    // older coordinators that predate this field.
    let loaded: Bool?
}

struct PodHealthDigest: Codable, Identifiable, Hashable {
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

// PodMetrics is a live snapshot of one coordinator's job queue and in-flight
// load — the same figures the web dashboard's header pills (Queued/In-flight/
// backpressure) are driven from.
struct PodMetrics: Codable {
    let queueDepth: Int
    let queueCapacity: Int
    let backpressurePct: Double
    let totalInFlight: Int
}

// CoordinationParticipant is an iOS device acting as a security/coordination
// layer (hosts encrypted payload pointers). Not an inference node — shown with
// a distinct icon and a toggleable layer.
struct CoordinationParticipant: Codable, Identifiable, Hashable {
    let deviceId: String
    let role: String
    let isMobile: Bool
    let geographicHint: String
    let lastSeenAt: String
    // Encrypted-payload pointers this device has served — the concrete work a
    // coordination participant does. Optional for backward-compat with older
    // coordinators that don't emit the field.
    let pointersServed: Int?
    var id: String { deviceId }
}

struct NodesResponse: Codable {
    let podId: String
    let region: String
    let nodes: [NodeSnapshot]
    let coordinationNodes: [CoordinationParticipant]?
    let metrics: PodMetrics?
}

// Balance lives in Shared (not the iOS-only AccountView) because NetworkClient,
// which is shared across all platform targets, references it in fetchBalance.
struct Balance: Codable {
    let grantBalance: Double
    let earnedBalance: Double
    let total: Double
}
