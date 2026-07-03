import Foundation

/// Output of the on-device classifier. Both CoreMLRouter and RuleBasedRouter
/// produce this identical type — the coordinator cannot tell which produced it
/// beyond the isFromMLModel flag, which affects hint weighting only.
///
/// Nothing here is authoritative: the coordinator treats every field as an
/// advisory suggestion and re-derives sensitivity/job-type itself when the hint
/// is weak, stale, missing, or attestation-gated (see coordinator/hintvalidator.go).
struct RouterHint: Codable, Equatable {
    let jobType: JobType
    let sensitivityTier: SensitivityTier
    let modelFamily: ModelFamily
    let confidence: Float        // 0.0-1.0, classifier's own stated confidence
    let routerVersion: String    // semantic version of the model or rule set
    let isFromMLModel: Bool       // false = rule-based fallback, lower coordinator weight
}

enum JobType: String, Codable {
    case schemaLookup       = "schema_lookup"
    case anomalyDetection   = "anomaly_detection"
    case queryOptimization  = "query_optimization"
    case summarization      = "summarization"
    case classification     = "classification"
    case generalChat        = "general_chat"
    case codeGeneration     = "code_generation"
    case unknown            = "unknown"
}

enum SensitivityTier: String, Codable {
    case low                     = "low"
    case moderate                = "moderate"
    case highRequiresAttestation = "high_requires_attestation"
}

enum ModelFamily: String, Codable {
    case small3B  = "small_3b"   // fits 7.7 GB nodes
    case mid13B   = "mid_13b"
    case large70B = "large_70b"
    case moeAny   = "moe_any"    // any MoE-capable node
    case unknown  = "unknown"
}
