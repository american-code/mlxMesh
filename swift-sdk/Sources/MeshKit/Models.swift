import Foundation

/// Wire types for the coordinator/directory JSON protocol — mirror
/// internal/protocol, internal/settlement, and internal/coordinator Go
/// structs field-for-field. Response types are Codable with
/// `.convertFromSnakeCase`; request bodies are built as plain dictionaries
/// (matching the existing NetworkClient.swift precedent — Codable's rigidity
/// doesn't fit request shapes with mixed optional/empty fields well).

public struct ChatMessage: Sendable {
    public let role: String
    public let content: String

    public init(role: String, content: String) {
        self.role = role
        self.content = content
    }

    func toDict() -> [String: String] {
        ["role": role, "content": content]
    }
}

public enum Sensitivity: String, Sendable {
    case low
    case moderate
    case highRequiresAttestation = "high_requires_attestation"
}

public struct ChatCompletion {
    public let id: String
    public let object: String
    public let model: String
    public let created: Int
    public let choices: [[String: AnyCodable]]
    public let usage: [String: AnyCodable]
    public let servedByNodeID: String?
    public let lane: String?
    public let latencyMs: Int?
    public let tokensPerSec: Double?

    /// Convenience accessor for choices[0].message.content, the common case.
    public var content: String? {
        guard let first = choices.first,
              let message = first["message"]?.value as? [String: Any] else { return nil }
        return message["content"] as? String
    }

    init(raw: [String: Any]) {
        id = raw["id"] as? String ?? ""
        object = raw["object"] as? String ?? ""
        model = raw["model"] as? String ?? ""
        created = raw["created"] as? Int ?? 0
        choices = (raw["choices"] as? [[String: Any]] ?? []).map { $0.mapValues { AnyCodable($0) } }
        usage = (raw["usage"] as? [String: Any] ?? [:]).mapValues { AnyCodable($0) }
        servedByNodeID = raw["oim_served_by_node_id"] as? String
        lane = raw["oim_lane"] as? String
        latencyMs = raw["oim_latency_ms"] as? Int
        tokensPerSec = raw["oim_tokens_per_sec"] as? Double
    }
}

/// One SSE frame. `usage` is only populated on the trailing frame, which has
/// an empty `choices` array — callers must check `isUsageFrame` before
/// assuming a `deltaContent` is present.
public struct ChatCompletionChunk {
    public let id: String
    public let object: String
    public let model: String
    public let created: Int
    public let choices: [[String: AnyCodable]]
    public let usage: [String: AnyCodable]?

    public var deltaContent: String? {
        guard let first = choices.first,
              let delta = first["delta"]?.value as? [String: Any] else { return nil }
        return delta["content"] as? String
    }

    public var isUsageFrame: Bool {
        choices.isEmpty && usage != nil
    }

    init(raw: [String: Any]) {
        id = raw["id"] as? String ?? ""
        object = raw["object"] as? String ?? ""
        model = raw["model"] as? String ?? ""
        created = raw["created"] as? Int ?? 0
        choices = (raw["choices"] as? [[String: Any]] ?? []).map { $0.mapValues { AnyCodable($0) } }
        usage = (raw["usage"] as? [String: Any]).map { $0.mapValues { AnyCodable($0) } }
    }
}

public struct Balance: Codable, Sendable {
    public let grantBalance: Double
    public let earnedBalance: Double
    public let total: Double

    enum CodingKeys: String, CodingKey {
        case grantBalance = "grant_balance"
        case earnedBalance = "earned_balance"
        case total
    }
}

public struct CreditEntry: Codable, Sendable {
    public let userID: String
    public let origin: String
    public let amount: Double
    public let grantedOrEarnedAt: String
    public let sourceReference: String

    enum CodingKeys: String, CodingKey {
        case userID = "user_id"
        case origin
        case amount
        case grantedOrEarnedAt = "granted_or_earned_at"
        case sourceReference = "source_reference"
    }
}

public struct PodHealthDigest: Codable, Sendable {
    public let podID: String
    public let regionHint: String
    public let coordinatorEndpoint: String?
    public let servableModelIDs: [String]
    public let aggregateHealthScore: Double
    public let nodeCountApprox: Int
    public let totalMemoryGB: Double
    public let aggregateToksPerSec: Double
    public let realNodeCountApprox: Int?
    public let realTotalMemoryGB: Double?
    public let realAggregateToksPerSec: Double?

    enum CodingKeys: String, CodingKey {
        case podID = "pod_id"
        case regionHint = "region_hint"
        case coordinatorEndpoint = "coordinator_endpoint"
        case servableModelIDs = "servable_model_ids"
        case aggregateHealthScore = "aggregate_health_score"
        case nodeCountApprox = "node_count_approx"
        case totalMemoryGB = "total_memory_gb"
        case aggregateToksPerSec = "aggregate_toks_per_sec"
        case realNodeCountApprox = "real_node_count_approx"
        case realTotalMemoryGB = "real_total_memory_gb"
        case realAggregateToksPerSec = "real_aggregate_toks_per_sec"
    }
}

/// Response from POST /v1/reserve-node — pins a node for an encrypted-pointer
/// job for coordinator.ReservationTTL (30s).
public struct Reservation: Codable, Sendable {
    public let reservationID: String
    public let nodeID: String
    public let ecdhPublicKey: String  // base64, raw uncompressed P-256 point
    public let expiresAt: String

    enum CodingKeys: String, CodingKey {
        case reservationID = "reservation_id"
        case nodeID = "node_id"
        case ecdhPublicKey = "ecdh_public_key"
        case expiresAt = "expires_at"
    }
}

public struct RecurrenceSpec: Sendable {
    public let intervalSeconds: Int
    public let maxJitterSeconds: Int

    public init(intervalSeconds: Int, maxJitterSeconds: Int = 0) {
        self.intervalSeconds = intervalSeconds
        self.maxJitterSeconds = maxJitterSeconds
    }

    func toDict() -> [String: Int] {
        ["interval_seconds": intervalSeconds, "max_jitter_seconds": maxJitterSeconds]
    }
}

/// Response from POST /jobs/background/assign — a persisted sticky-session
/// assignment. Only `jobID` is needed to call `runBackgroundCycle`; the rest
/// is kept for inspection.
public struct BackgroundJob {
    public let jobID: String
    public let primary: String
    public let backups: [String]
    public let jobSpec: [String: AnyCodable]

    init(raw: [String: Any]) {
        jobID = raw["job_id"] as? String ?? ""
        primary = raw["primary"] as? String ?? ""
        backups = raw["backups"] as? [String] ?? []
        jobSpec = (raw["job_spec"] as? [String: Any] ?? [:]).mapValues { AnyCodable($0) }
    }
}

/// Minimal type-erased JSON value wrapper — response bodies here are
/// heterogeneous OpenAI-shaped dictionaries, not fixed schemas, so this
/// avoids a full custom Codable implementation for every possible nested
/// shape. Not Sendable: values originate from JSONSerialization
/// (String/NSNumber/Bool/Array/Dictionary/NSNull) and are read synchronously
/// right after being returned from a MeshClient call, never stored across
/// concurrency domains.
public struct AnyCodable {
    public let value: Any

    init(_ value: Any) {
        self.value = value
    }
}
