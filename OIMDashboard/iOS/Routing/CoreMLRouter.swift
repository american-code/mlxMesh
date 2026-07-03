import CoreML
import Foundation

/// Primary classifier — a fine-tuned DistilBERT/MobileBERT via Core ML.
///
/// Status: no trained model ships yet, so this loads nothing and reports
/// `available == false`, which makes OnDeviceRouter use RuleBasedRouter. That is
/// the correct stand-in — NOT random placeholder outputs. When a real, signed
/// `.mlmodelc` is added to the bundle, loadModel() picks it up automatically.
///
/// Constraints when a model is present:
///   - CPU + Neural Engine only (config.computeUnits = .cpuAndNeuralEngine) — must
///     NOT use the GPU, so it never competes with iPad contribution mode for GPU.
///   - < 100 MB on disk, < 50ms on A15 / < 20ms on M-series.
final class CoreMLRouter {

    private var model: MLModel?
    private var isReady = false
    private(set) var version = "core_ml_unloaded"

    /// Bundle resource name of the compiled model (without extension).
    private let modelResourceName = "IntentClassifier"

    init() {
        loadModel()
    }

    var available: Bool { isReady }

    private func loadModel() {
        // Ship a signed single-file .mlmodel (easy to hash/sign as one file) and
        // compile it at load time, rather than a pre-compiled .mlmodelc directory.
        guard let modelURL = Bundle.main.url(forResource: modelResourceName, withExtension: "mlmodel") else {
            // No model bundled yet — expected today. Rule-based takes over.
            isReady = false
            return
        }
        // Never load an unverified model — signature is checked on the raw file
        // BEFORE compilation, so a tampered model is rejected before Core ML
        // touches it.
        guard ModelSignatureVerifier.verify(modelURL: modelURL) else {
            isReady = false
            return
        }
        let config = MLModelConfiguration()
        config.computeUnits = .cpuAndNeuralEngine // explicitly exclude GPU
        do {
            let compiled = try MLModel.compileModel(at: modelURL)
            model = try MLModel(contentsOf: compiled, configuration: config)
            version = readVersion(near: modelURL)
            isReady = true
        } catch {
            // Any load failure → unavailable, never throw. Rule-based takes over.
            isReady = false
        }
    }

    /// Runs the model. If unavailable or execution fails for ANY reason, returns a
    /// low-confidence unknown hint — this signals the coordinator to re-classify
    /// fully; it is NOT an error. Never throws.
    func classify(queryText: String) async -> RouterHint {
        guard isReady, let model else {
            return unknownHint()
        }
        do {
            let input = try MLDictionaryFeatureProvider(dictionary: ["text": queryText])
            let out = try await model.prediction(from: input)
            return interpret(out)
        } catch {
            return unknownHint()
        }
    }

    /// Maps the model's output to a RouterHint. The Create ML text classifier
    /// emits a single `label` string in the composite form
    /// "jobType|sensitivity|modelFamily" (see tools/train-router); we split it
    /// back into the three fields. The MaxEnt classifier does not expose a
    /// per-prediction probability in its compiled form, so confidence is a fixed
    /// model-level value — still higher than the rule-based 0.6, and the
    /// coordinator additionally weights isFromMLModel=true. A future model with a
    /// probability head can populate real per-prediction confidence here.
    private func interpret(_ out: MLFeatureProvider) -> RouterHint {
        let label = out.featureValue(for: "label")?.stringValue ?? ""
        let parts = label.split(separator: "|", maxSplits: 2).map(String.init)
        guard parts.count == 3 else { return unknownHint() }

        // Read a real probability if the model happens to expose one; else fixed.
        var confidence: Float = 0.85
        if let probs = out.featureValue(for: "labelProbability")?.dictionaryValue,
           let p = probs[label as NSObject] as? Double {
            confidence = Float(p)
        }

        return RouterHint(
            jobType: JobType(rawValue: parts[0]) ?? .unknown,
            sensitivityTier: SensitivityTier(rawValue: parts[1]) ?? .low,
            modelFamily: ModelFamily(rawValue: parts[2]) ?? .unknown,
            confidence: confidence,
            routerVersion: version,
            isFromMLModel: true
        )
    }

    private func unknownHint() -> RouterHint {
        RouterHint(jobType: .unknown, sensitivityTier: .low, modelFamily: .unknown,
                   confidence: 0.0, routerVersion: version, isFromMLModel: true)
    }

    private func readVersion(near modelURL: URL) -> String {
        let versionURL = modelURL.appendingPathExtension("version")
        if let raw = try? String(contentsOf: versionURL, encoding: .utf8) {
            return raw.trimmingCharacters(in: .whitespacesAndNewlines)
        }
        return "core_ml_v1"
    }
}
