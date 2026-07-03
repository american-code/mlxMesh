// tools/train-router — reproducible Create ML pipeline for the on-device intent
// router (Step 10 / v1 scaffold). Runs entirely on a Mac, no iOS device:
//
//   1. Generates a labeled dataset (composite label
//      "jobType|sensitivity|modelFamily").
//   2. Trains an MLTextClassifier (MaxEnt) on it.
//   3. Writes IntentClassifier.mlmodel (single file — easy to sign).
//   4. Signs SHA-256(model) with the project Ed25519 key from the env var
//      OIM_MODEL_SIGNING_KEY (base64 raw private key) and writes .sig + .version
//      alongside, matching OIMDashboard's ModelSignatureVerifier.
//
// Usage:
//   OIM_MODEL_SIGNING_KEY=<base64-ed25519-priv> \
//     swift tools/train-router/main.swift <output-dir>
//
// HONEST SCOPE: this dataset is templated, so the resulting model largely
// reproduces the keyword rules — it validates the whole load→verify→predict
// path with a REAL signed artifact; it does not add real intelligence. That
// arrives with v2 (on-device teacher-student distillation using the local Exo
// LLM as the teacher). Keep RuleBasedRouter as the runtime default until then.

import CreateML
import CoreML
import CryptoKit
import Foundation

let outputDir = CommandLine.arguments.count > 1
    ? URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
    : URL(fileURLWithPath: FileManager.default.currentDirectoryPath)

// MARK: - Dataset generation

// jobType -> model family (mirrors RuleBasedRouter's mapping).
let jobs: [(job: String, family: String, templates: [String])] = [
    ("query_optimization", "mid_13b", [
        "select %@ from %@ where status = active", "explain the query plan for %@",
        "add an index on the %@ column", "optimize this join between %@ and orders",
        "why is this where clause on %@ slow", "rewrite this select to use an index",
    ]),
    ("anomaly_detection", "small_3b", [
        "detect the anomaly in %@ traffic", "flag outliers in the %@ metric",
        "alert me on spikes in %@", "is this %@ reading a threshold breach",
        "find the outlier in %@ latency", "spike detected in %@, investigate",
    ]),
    ("summarization", "mid_13b", [
        "summarize the %@ report", "give me a tldr of the %@ thread",
        "overview of the %@ document", "summary of this quarter's %@",
        "condense the %@ notes", "what's the gist of the %@ update",
    ]),
    ("classification", "small_3b", [
        "classify these %@ records", "categorize the %@ entries",
        "label each %@ row", "tag the %@ items by type",
        "sort these %@ into buckets", "assign a category to each %@",
    ]),
    ("code_generation", "mid_13b", [
        "write a func to parse %@", "def that validates %@",
        "implement a class for %@ handling", "import a library and process %@",
        "let me refactor this %@ code", "const helper to format %@",
    ]),
    ("general_chat", "unknown", [
        "hello how are you today", "what can this network do",
        "tell me something interesting", "how does the mesh work",
        "good morning, any updates", "thanks, that was helpful",
    ]),
]

let fillers = ["users", "orders", "traffic", "sessions", "revenue", "logs"]

// Sensitivity variants appended to a base text. jobType is unchanged; only the
// sensitivity field of the composite label differs.
let moderateSuffixes = [" for customer email records", " including user names and addresses", " for this individual's account"]
let highSuffixes = [" including SSN and passport numbers", " with credit card and password fields", " containing date of birth data"]

var texts: [String] = []
var labels: [String] = []

func add(_ text: String, job: String, sensitivity: String, family: String) {
    texts.append(text)
    labels.append("\(job)|\(sensitivity)|\(family)")
}

for entry in jobs {
    for template in entry.templates {
        for filler in fillers {
            let base = template.contains("%@") ? String(format: template, filler, filler) : template
            add(base, job: entry.job, sensitivity: "low", family: entry.family)
        }
        // A couple of moderate/high variants per template for balanced sensitivity.
        add(String(format: template.contains("%@") ? template : template + " %@", "records") + moderateSuffixes[0],
            job: entry.job, sensitivity: "moderate", family: entry.family)
        add(String(format: template.contains("%@") ? template : template + " %@", "records") + highSuffixes[0],
            job: entry.job, sensitivity: "high_requires_attestation", family: entry.family)
    }
}

print("[train-router] generated \(texts.count) labeled examples")

// MARK: - Train

do {
    let table = try MLDataTable(dictionary: ["text": texts, "label": labels])
    let classifier = try MLTextClassifier(trainingData: table, textColumn: "text", labelColumn: "label")

    let modelURL = outputDir.appendingPathComponent("IntentClassifier.mlmodel")
    try classifier.write(to: modelURL)
    print("[train-router] wrote \(modelURL.path)")

    // MARK: - Sign (matches ModelSignatureVerifier: sign over SHA-256(model file))

    guard let keyB64 = ProcessInfo.processInfo.environment["OIM_MODEL_SIGNING_KEY"],
          let keyRaw = Data(base64Encoded: keyB64),
          let priv = try? Curve25519.Signing.PrivateKey(rawRepresentation: keyRaw) else {
        print("[train-router] ERROR: OIM_MODEL_SIGNING_KEY not set to a valid base64 Ed25519 private key — model written but UNSIGNED")
        exit(2)
    }
    let modelData = try Data(contentsOf: modelURL)
    let digest = Data(SHA256.hash(data: modelData))
    let signature = try priv.signature(for: digest)
    try signature.write(to: modelURL.appendingPathExtension("sig"))
    try "core_ml_v1".write(to: modelURL.appendingPathExtension("version"), atomically: true, encoding: .utf8)
    print("[train-router] signed → \(modelURL.lastPathComponent).sig (+ .version)")
    print("[train-router] DONE")
} catch {
    print("[train-router] ERROR: \(error)")
    exit(1)
}
