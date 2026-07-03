import Foundation

/// Keyword/pattern heuristic classifier. Always available — no model files,
/// no CoreML, no network. This is the zero-dependency path and the correct
/// stand-in until a trained Core ML model exists (see CoreMLRouter).
///
/// Produces the identical RouterHint shape as CoreMLRouter; the only observable
/// difference downstream is isFromMLModel == false (which the coordinator
/// weights slightly lower) and a fixed, honest confidence of 0.6 — rules are
/// coarse and should never claim more certainty than they have.
final class RuleBasedRouter {

    let version = "rule_based_v1"

    // Job-type rules, applied in order — first match wins. Each maps to the
    // model family that type typically needs.
    private struct JobRule {
        let keywords: [String]
        let jobType: JobType
        let modelFamily: ModelFamily
    }

    private let jobRules: [JobRule] = [
        JobRule(keywords: ["select", "join", "where", "index", "explain", "query plan"],
                jobType: .queryOptimization, modelFamily: .mid13B),
        JobRule(keywords: ["anomaly", "outlier", "spike", "alert", "threshold"],
                jobType: .anomalyDetection, modelFamily: .small3B),
        JobRule(keywords: ["summarize", "summary", "tldr", "overview"],
                jobType: .summarization, modelFamily: .mid13B),
        JobRule(keywords: ["classify", "categorize", "label", "tag"],
                jobType: .classification, modelFamily: .small3B),
        JobRule(keywords: ["func", "class", "def", "import", "var", "let", "const", "code"],
                jobType: .codeGeneration, modelFamily: .mid13B),
    ]

    // High-sensitivity markers → attestation-gated. Coordinator still re-verifies;
    // this only makes the client ASK for the stricter tier (escalation is always
    // allowed, de-escalation never is).
    private let highSensitivityMarkers = [
        "ssn", "social security", "credit card", "password", "passport", "dob", "date of birth",
    ]
    // Personal-data markers → moderate, but only in a personal context.
    private let piiMarkers = ["email", "phone", "address", "name"]
    private let personalContext = ["customer", "personal", "private", "individual", "user's", "my "]

    func classify(queryText: String) -> RouterHint {
        let text = queryText.lowercased()

        // --- Job type (first match wins) ---
        var jobType: JobType = .generalChat
        var modelFamily: ModelFamily = .unknown
        for rule in jobRules where containsAny(text, rule.keywords) {
            jobType = rule.jobType
            modelFamily = rule.modelFamily
            break
        }

        // --- Sensitivity ---
        let sensitivity = classifySensitivity(text)

        return RouterHint(
            jobType: jobType,
            sensitivityTier: sensitivity,
            modelFamily: modelFamily,
            confidence: 0.6,        // honest: rules are coarse
            routerVersion: version,
            isFromMLModel: false
        )
    }

    private func classifySensitivity(_ text: String) -> SensitivityTier {
        if containsAny(text, highSensitivityMarkers) {
            return .highRequiresAttestation
        }
        if containsAny(text, piiMarkers) && containsAny(text, personalContext) {
            return .moderate
        }
        return .low
    }

    private func containsAny(_ text: String, _ needles: [String]) -> Bool {
        for n in needles where text.contains(n) {
            return true
        }
        return false
    }
}
