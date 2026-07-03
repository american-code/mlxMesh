import Foundation

/// Top-level router. Picks CoreMLRouter when it is available and
/// signature-verified, otherwise RuleBasedRouter. Callers ALWAYS get a
/// RouterHint — never nil, never an error, never a throw. This is the client
/// half of the "hint is an optimization, never a requirement" contract; the
/// coordinator half is coordinator/hintvalidator.go.
final class OnDeviceRouter {

    private let mlRouter = CoreMLRouter()
    private let ruleRouter = RuleBasedRouter()

    func classify(queryText: String) async -> RouterHint {
        if mlRouter.available {
            let hint = await mlRouter.classify(queryText: queryText)
            // A ready ML model can still return low-confidence unknown; when it
            // does, defer to the rule-based pass rather than shipping a useless
            // hint the coordinator would just discard anyway.
            if hint.confidence > 0 && hint.jobType != .unknown {
                log("classified via Core ML (\(hint.jobType.rawValue), conf \(hint.confidence))")
                return hint
            }
        }
        let hint = ruleRouter.classify(queryText: queryText)
        log("classified via rule-based (\(hint.jobType.rawValue))")
        return hint
    }

    func currentVersion() -> String {
        mlRouter.available ? mlRouter.version : ruleRouter.version
    }

    private func log(_ message: String) {
        // Local diagnostics only — which router ran is never sent to the
        // coordinator beyond RouterHint.isFromMLModel.
        print("[OnDeviceRouter] \(message)")
    }
}
