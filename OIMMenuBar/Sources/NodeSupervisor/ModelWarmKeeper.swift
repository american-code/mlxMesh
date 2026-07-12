import Foundation
import Observation

/// Periodically re-warms this node's downloaded models by hitting Exo's OWN
/// local API directly — a workaround for a known Exo behavior: Exo's
/// inference workers can get recycled/evicted from memory after a period of
/// inactivity even though the coordinator/dashboard still shows the model as
/// "loaded." The dashboard reflects the last PLACEMENT event it received, not
/// live memory residency on the node — Exo's model lifecycle has separate
/// Placement and Inference phases, and nothing pushes a "this instance was
/// evicted" event back up. Left unaddressed, the first real request after an
/// idle stretch silently pays a full cold load (re-placing shards, reloading
/// weights into RAM) instead of the fast warm path the dashboard implied was
/// ready.
///
/// Earlier version routed this through the coordinator's POST
/// /nodes/{id}/warm-model — real-world testing found that indirection
/// unreliable (the node agent's own path to Exo wasn't always reachable/
/// compliant at the moment a warm-model job landed) and it required minting a
/// synthetic consumer credential just to satisfy that endpoint's Bearer-token
/// auth gate. This version is simpler and matches what was actually verified
/// to work: talk to Exo directly, on localhost, exactly like
/// ExoHealthMonitor's existing `/state` poll already does — no coordinator
/// round trip, no auth, no credential minting, nothing to break on a region
/// switch. A real (if minimal) chat completion is used rather than the
/// preview-placement/create-instance/await-instance dance nodeagent's
/// WarmModel does server-side, since forcing an actual inference call is the
/// most direct way to prove — and force — real warmth, not just ask Exo to
/// confirm it.
@Observable
@MainActor
final class ModelWarmKeeper {
    private(set) var lastWarmedAt: Date?
    private(set) var lastError: String?
    private(set) var isWarming = false

    private let session: HTTPDataFetching
    private var pollTask: Task<Void, Never>?

    init(session: HTTPDataFetching = URLSession.shared) {
        self.session = session
    }

    /// interval default: 5 minutes (lowered from an initial 20 — real-world
    /// testing showed 20 minutes was NOT tight enough to reliably beat Exo's
    /// actual idle-eviction window). Exo's own idle-eviction window is an
    /// upstream behavior this project doesn't control or have a fixed number
    /// for, so `NodeSupervisor` settings expose this as operator-tunable —
    /// someone who observes eviction sooner (or never) on their own hardware
    /// can tighten or loosen it further.
    func startPolling(exoURL: String, interval: TimeInterval = 5 * 60) {
        // resettingState: false — a restart (e.g. the user nudging the
        // interval Stepper in Settings, which fires on every step) must NOT
        // wipe the "kept warm — 4m ago" status and drop back into the 30s
        // "checking…" delay, because the models are still warm; nothing about
        // the actual warm state changed. Only a genuine stop (node stopped)
        // clears status.
        stopPolling(resettingState: false)
        pollTask = Task { [weak self] in
            // Give Exo a little time to finish its own startup before the
            // first attempt, rather than racing it and finding nothing
            // downloaded yet.
            try? await Task.sleep(for: .seconds(30))
            while !Task.isCancelled {
                await self?.warmOnce(exoURL: exoURL)
                try? await Task.sleep(for: .seconds(interval))
            }
        }
    }

    func stopPolling(resettingState: Bool = true) {
        pollTask?.cancel()
        pollTask = nil
        if resettingState {
            lastWarmedAt = nil
            lastError = nil
            isWarming = false
        }
    }

    /// Re-warms every model Exo reports as downloaded — not just one pinned
    /// model, since a node can serve several, and re-warming an already-warm
    /// model is a cheap, fast no-op-ish completion, not a wasted reload.
    /// Failures are per-model and don't abort the sweep: one stuck shard
    /// shouldn't stop the others from getting re-warmed. Runs all models
    /// CONCURRENTLY — a cold multi-shard load can legitimately take minutes,
    /// so warming N models serially could overrun the whole warm interval.
    private func warmOnce(exoURL: String) async {
        isWarming = true
        defer { isWarming = false }
        do {
            let modelIDs = try await downloadedModelIDs(exoURL: exoURL)
            guard !modelIDs.isEmpty else {
                lastError = nil // nothing downloaded yet — not an error, just nothing to do
                return
            }
            let failures = await withTaskGroup(of: String?.self) { group in
                for modelID in modelIDs {
                    group.addTask { [weak self] in
                        do {
                            try await self?.warmModel(exoURL: exoURL, modelID: modelID)
                            return nil
                        } catch {
                            return "\(modelID): \(error.localizedDescription)"
                        }
                    }
                }
                var collected: [String] = []
                for await result in group where result != nil {
                    collected.append(result!)
                }
                return collected
            }
            if failures.isEmpty {
                lastWarmedAt = Date()
                lastError = nil
            } else {
                lastError = failures.joined(separator: "; ")
            }
        } catch {
            lastError = "list downloaded models: \(error.localizedDescription)"
        }
    }

    /// GET {exoURL}/models?downloaded=true — the same catalog endpoint
    /// exoadapter.GetDownloadedModels (Go) reads. Deliberately skips that
    /// function's stricter cross-check against /state's per-shard download
    /// ledger: a false positive here (attempting to warm a model that's still
    /// mid-download) just makes one warmModel call fail harmlessly, whereas
    /// that cross-check exists on the Go side to avoid ADVERTISING an
    /// incomplete model to the coordinator — a correctness requirement this
    /// purely-local warming action doesn't share.
    private func downloadedModelIDs(exoURL: String) async throws -> [String] {
        guard let url = URL(string: "\(exoURL)/models?downloaded=true") else {
            throw NetworkError.message("invalid Exo URL")
        }
        var request = URLRequest(url: url)
        request.timeoutInterval = 10
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            throw NetworkError.message("Exo /models returned a non-2xx response")
        }
        guard let array = try JSONSerialization.jsonObject(with: data) as? [[String: Any]] else {
            throw NetworkError.message("Exo /models: unexpected response shape")
        }
        return array.compactMap { $0["id"] as? String }.filter { !$0.isEmpty }
    }

    /// POST {exoURL}/v1/chat/completions — a minimal (max_tokens: 1) real
    /// completion, exactly the OpenAI-compatible shape
    /// exoadapter.RunChatCompletion (Go) sends. A genuine inference call is
    /// the most direct way to force Exo to actually place shards and load
    /// weights if it evicted them — more robust than asking Exo to merely
    /// "confirm" an instance exists, since that confirm-only path is exactly
    /// what testing showed to be unreliable when routed through the node
    /// agent.
    private func warmModel(exoURL: String, modelID: String) async throws {
        guard let url = URL(string: "\(exoURL)/v1/chat/completions") else {
            throw NetworkError.message("invalid Exo URL")
        }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        // A cold multi-shard load can legitimately take minutes; stay well
        // above that rather than the default 60s.
        request.timeoutInterval = 240
        request.httpBody = try JSONSerialization.data(withJSONObject: [
            "model": modelID,
            "messages": [["role": "user", "content": "hi"]],
            "max_tokens": 1,
            "stream": false,
        ])
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            let body = String(data: data, encoding: .utf8) ?? ""
            throw NetworkError.message("Exo returned \((response as? HTTPURLResponse)?.statusCode ?? -1): \(body.prefix(200))")
        }
    }
}
