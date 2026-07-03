import Foundation

/// The ONLY thing sent to the coordinator for a new job. The raw query payload
/// is NOT here — the coordinator routes on metadata alone and never sees
/// plaintext (payloadHash + payloadFetchURL point the assigned node at the
/// ciphertext instead).
struct HintPacket: Codable, Equatable {

    // Job identity
    let jobId: String                        // client-generated UUID
    let requesterId: String                  // account address (wallet) or node identity

    // Routing hint — coordinator treats as a weighted suggestion, never ground
    // truth. nil when the on-device router is disabled/absent (non-iOS clients);
    // the coordinator then re-classifies fully. Absent hint == normal routing.
    let hint: RouterHint?

    // Payload reference — passed to the assigned node, never dereferenced by the
    // coordinator.
    let payloadHash: String                  // SHA-256 of plaintext — content address
    let payloadFetchURL: String              // node fetches ciphertext from here
    let ephemeralPublicKeyBase64: String     // node uses this for ECDH decryption

    // User controls
    let sensitivityOverride: SensitivityTier? // escalate-only; coordinator enforces
    let maxParallelNodes: Int                // blast-radius cap, default 1
    let allowDecomposition: Bool             // opt-in, default false
    let requireDeterministicOutput: Bool     // must be true for checksum verification

    // Lane
    let lane: String                         // "fast" or "background"
    let recurrenceIntervalSeconds: Int?      // nil for fast lane, cadence for background

    /// Convenience constructor applying the documented defaults so callers only
    /// specify what they mean to change.
    init(
        jobId: String = UUID().uuidString,
        requesterId: String,
        hint: RouterHint?,
        payloadHash: String,
        payloadFetchURL: String,
        ephemeralPublicKeyBase64: String,
        sensitivityOverride: SensitivityTier? = nil,
        maxParallelNodes: Int = 1,
        allowDecomposition: Bool = false,
        requireDeterministicOutput: Bool = true,
        lane: String = "fast",
        recurrenceIntervalSeconds: Int? = nil
    ) {
        self.jobId = jobId
        self.requesterId = requesterId
        self.hint = hint
        self.payloadHash = payloadHash
        self.payloadFetchURL = payloadFetchURL
        self.ephemeralPublicKeyBase64 = ephemeralPublicKeyBase64
        self.sensitivityOverride = sensitivityOverride
        self.maxParallelNodes = maxParallelNodes
        self.allowDecomposition = allowDecomposition
        self.requireDeterministicOutput = requireDeterministicOutput
        self.lane = lane
        self.recurrenceIntervalSeconds = recurrenceIntervalSeconds
    }
}
