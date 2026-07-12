import Foundation
import Observation

/// Supervises the embedded `oim node start` binary as a child process.
///
/// Lifecycle model: the node stops when this app quits (Postgres.app-style) —
/// deliberately NOT a detached background process. A detached node that
/// outlives the app risks the app losing track of it entirely: quit, relaunch,
/// and the new launch fails on `--listen :8765` already in use because the UI
/// has no memory of the process it no longer owns. "Launch at Login" (see
/// LaunchAtLoginController) gets the equivalent of persistence-across-reboot
/// WITHOUT that risk, because it's the app itself that's relaunched, and the
/// app remains the one true supervisor at all times.
@Observable
@MainActor
final class NodeProcessController {
    enum State: Equatable {
        case stopped
        case starting
        case running(nodeID: String)
        case failed(String)
    }

    struct LaunchOptions {
        var coordinatorURL: String
        var exoURL: String
        var cap: Double
        var region: String?
        var scheduleMode: String // "always" | "window"
        var scheduleStart: String?
        var scheduleEnd: String?
        var scheduleDays: [String]?
        // Explicit override for the address the coordinator dispatches TO.
        // Left empty, `oim node start` auto-derives this from --listen,
        // which defaults to "localhost:<port>" — meaningless (or actively
        // wrong) unless the coordinator runs on this same machine. A
        // contributor reachable only from behind NAT/a home router (i.e.
        // almost everyone, since the production coordinators are remote)
        // MUST set this to a real address the coordinator can dial back on
        // (a port-forwarded public IP/dynamic-DNS host, ideally with
        // --tls-cert/--tls-key — see README's TLS section), or every real
        // dispatch attempt to this node silently fails with a connection
        // error the operator never sees in this app (confirmed live: this
        // is exactly what was happening for a node with no override set,
        // against the real production seed).
        var reachabilityEndpoint: String?
        // Seconds between the node agent re-benchmarking every downloaded
        // model against Exo and submitting a signed result (--bench-interval,
        // internal/agent/agent.go). nil/0 = disabled. This is a SIGNED report
        // (the Go node process holds the real Ed25519 identity key, which
        // this Swift app never does), so it's the only channel that can
        // update the coordinator's near-real-time per-node metrics — the
        // separate local ModelWarmKeeper only talks to Exo directly and
        // never reports anything upstream. As a side effect, each
        // re-benchmark is a real inference call, so this also re-warms
        // whatever model Exo evicted while idle.
        var benchIntervalSec: Int?
    }

    private(set) var state: State = .stopped
    private(set) var recentLogLines: [String] = []
    private let logLineCap = 500

    private var process: Process?
    private var sawRegistered = false
    private var sawServingJobs = false
    private var pendingNodeID: String?
    private var isStoppingIntentionally = false

    func start(_ options: LaunchOptions) {
        guard process == nil else { return }
        guard let binaryURL = EmbeddedBinaryLocator.binaryURL else {
            state = .failed("Embedded oim binary not found in app bundle")
            return
        }

        var args = [
            "node", "start",
            "--coordinator", options.coordinatorURL,
            "--exo-url", options.exoURL,
            "--cap", String(options.cap),
        ]
        if let region = options.region, !region.isEmpty {
            args += ["--region", region]
        }
        if let endpoint = options.reachabilityEndpoint, !endpoint.isEmpty {
            args += ["--reachability-endpoint", endpoint]
        }
        if let benchSec = options.benchIntervalSec, benchSec > 0 {
            args += ["--bench-interval", String(benchSec)]
        }
        // Exo-down is intentionally non-fatal here (verified empirically
        // against internal/capability.AssembleManifest: a node with Exo
        // unreachable still registers and serves, just with zero models). We
        // never gate Start on Exo health — the UI surfaces that as a
        // "degraded" warning instead (see AppState's combined icon state).
        if options.scheduleMode == "window" {
            args += ["--schedule-mode", "window"]
            if let s = options.scheduleStart { args += ["--schedule-start", s] }
            if let e = options.scheduleEnd { args += ["--schedule-end", e] }
            if let days = options.scheduleDays, !days.isEmpty {
                args += ["--schedule-days", days.joined(separator: ",")]
            }
        }

        let proc = Process()
        proc.executableURL = binaryURL
        proc.arguments = args

        let outPipe = Pipe()
        proc.standardOutput = outPipe
        proc.standardError = outPipe // agent logs go to stdout, but capture both

        sawRegistered = false
        sawServingJobs = false
        pendingNodeID = nil
        isStoppingIntentionally = false
        recentLogLines.removeAll()
        state = .starting

        outPipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty, let text = String(data: data, encoding: .utf8) else { return }
            Task { @MainActor in self?.handle(chunk: text) }
        }

        proc.terminationHandler = { [weak self] p in
            outPipe.fileHandleForReading.readabilityHandler = nil
            Task { @MainActor in self?.handleTermination(status: p.terminationStatus) }
        }

        do {
            try proc.run()
            process = proc
        } catch {
            state = .failed("Failed to launch oim: \(error.localizedDescription)")
        }
    }

    /// Graceful stop: agent.Run installs signal.NotifyContext(SIGINT, SIGTERM)
    /// and logs "[agent] shutting down" — SIGTERM is the correct, already-
    /// supported shutdown path, not a hard kill.
    func stop() {
        guard let proc = process, proc.isRunning else { return }
        isStoppingIntentionally = true
        proc.terminate()
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(5))
            guard let self, proc.isRunning else { return }
            kill(proc.processIdentifier, SIGKILL) // fallback only — shouldn't normally fire
        }
    }

    private func handle(chunk: String) {
        for line in chunk.split(separator: "\n", omittingEmptySubsequences: true) {
            let lineStr = String(line)
            appendLog(lineStr)
            switch NodeLogParser.parse(lineStr) {
            case .registered(let nodeID):
                sawRegistered = true
                pendingNodeID = nodeID
            case .servingJobs:
                sawServingJobs = true
            case .shuttingDown, .other:
                break
            }
        }
        if sawRegistered, sawServingJobs, let id = pendingNodeID {
            state = .running(nodeID: id)
        }
    }

    private func appendLog(_ line: String) {
        recentLogLines.append(line)
        if recentLogLines.count > logLineCap {
            recentLogLines.removeFirst(recentLogLines.count - logLineCap)
        }
    }

    private func handleTermination(status: Int32) {
        process = nil
        let wasIntentional = isStoppingIntentionally
        isStoppingIntentionally = false

        if wasIntentional {
            state = .stopped
            return
        }
        switch state {
        case .running:
            state = .failed("oim exited unexpectedly (status \(status))")
        case .starting:
            state = .failed("oim exited before completing startup (status \(status)) — check the log")
        case .stopped, .failed:
            state = .stopped
        }
    }
}
