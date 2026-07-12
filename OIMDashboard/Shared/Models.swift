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

    // Custom decoding: geoLat/geoLng follow the coordinator's own "0 = not
    // declared" contract (internal/coordinator/registry.go's GeoLat/GeoLng
    // both have `json:"...,omitempty"` — the KEY IS OMITTED, not sent as 0,
    // whenever a node never got a geo coordinate, e.g. its `oim node start`
    // process's IP auto-geolocation failed at startup and it wasn't given
    // explicit --lat/--lng). A plain non-optional Double here throws on that
    // missing key — and because Codable's array decoding aborts the WHOLE
    // array on one bad element, a SINGLE node without a declared location
    // silently took down every other node in its pod's /nodes response too.
    // This is what caused "only EU online" in practice: exactly one real US
    // node's geo-detect failed, and its missing keys broke decoding for the
    // entire pod-us node list while pod-eu (no undeclared-geo nodes) decoded
    // fine — nothing was actually down.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        nodeId = try c.decode(String.self, forKey: .nodeId)
        status = try c.decode(String.self, forKey: .status)
        geographicHint = try c.decode(String.self, forKey: .geographicHint)
        geoLat = try c.decodeIfPresent(Double.self, forKey: .geoLat) ?? 0
        geoLng = try c.decodeIfPresent(Double.self, forKey: .geoLng) ?? 0
        reachabilityEndpoint = try c.decode(String.self, forKey: .reachabilityEndpoint)
        declaredMemoryGb = try c.decode(Double.self, forKey: .declaredMemoryGb)
        committedMemoryGb = try c.decode(Double.self, forKey: .committedMemoryGb)
        models = try c.decodeIfPresent([ModelCapability].self, forKey: .models)
        measuredToksPerSec = try c.decode(Double.self, forKey: .measuredToksPerSec)
        hasSecureEnclave = try c.decode(Bool.self, forKey: .hasSecureEnclave)
        enclaveAttested = try c.decode(Bool.self, forKey: .enclaveAttested)
        isCluster = try c.decode(Bool.self, forKey: .isCluster)
        clusterDeviceCount = try c.decodeIfPresent(Int.self, forKey: .clusterDeviceCount)
        clusterChipFamilies = try c.decodeIfPresent([String].self, forKey: .clusterChipFamilies)
        lastSeenAt = try c.decode(String.self, forKey: .lastSeenAt)
        inFlightJobs = try c.decode(Int.self, forKey: .inFlightJobs)
        simulated = try c.decodeIfPresent(Bool.self, forKey: .simulated)
    }
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
