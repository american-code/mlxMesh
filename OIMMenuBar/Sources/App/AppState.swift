import Foundation
import Observation

/// Root coordinator tying together Exo supervision, node supervision, wallet
/// linking, and settings. Owns the one canonical answer to "what should the
/// menu bar icon look like right now" so that logic lives in exactly one
/// place rather than being re-derived per view.
@Observable
@MainActor
final class AppState {
    let exoMonitor = ExoHealthMonitor()
    let nodeController = NodeProcessController()
    let walletCoordinator = WalletLinkCoordinator()
    let launchAtLogin = LaunchAtLoginController()
    let detectMonitor = LocalDetectMonitor()
    let podMetricsMonitor = NodePodMetricsMonitor()

    enum Region: String, CaseIterable, Identifiable {
        case us, eu
        var id: String { rawValue }

        /// Deliberately NOT the CLI's own --coordinator default
        /// (http://localhost:9000, a local dev value) — this app always
        /// targets the real production seed. See RUNBOOK.md.
        var coordinatorURL: String {
            switch self {
            case .us: return "https://us.mlxmesh.net"
            case .eu: return "https://eu.mlxmesh.net"
            }
        }

        var displayName: String {
            switch self {
            case .us: return "United States"
            case .eu: return "Europe"
            }
        }
    }

    enum ScheduleMode: String { case always, window }

    enum Weekday: String, CaseIterable, Identifiable {
        case mon, tue, wed, thu, fri, sat, sun
        var id: String { rawValue }
        var shortLabel: String { rawValue.prefix(1).uppercased() + rawValue.dropFirst() }
    }

    var memoryCapPct: Double
    var region: Region
    var scheduleMode: ScheduleMode
    var scheduleStart: String
    var scheduleEnd: String
    var scheduleDays: Set<Weekday>
    /// Empty = auto-derive (matches prior behavior exactly; only meaningful
    /// on the same machine as the coordinator). Set this to a real,
    /// externally-reachable address (public IP/DDNS host + a port-forwarded
    /// port, e.g. "http://your-ddns-host.example:8765") if the coordinator
    /// is remote — the production seed always is. See LaunchOptions'
    /// reachabilityEndpoint doc comment for why this is required, not
    /// cosmetic.
    var reachabilityEndpoint: String

    private let defaults = UserDefaults.standard
    private enum Keys {
        static let cap = "oim.menubar.cap"
        static let region = "oim.menubar.region"
        static let scheduleMode = "oim.menubar.scheduleMode"
        static let scheduleStart = "oim.menubar.scheduleStart"
        static let scheduleEnd = "oim.menubar.scheduleEnd"
        static let scheduleDays = "oim.menubar.scheduleDays"
        static let reachabilityEndpoint = "oim.menubar.reachabilityEndpoint"
    }

    init() {
        // Prefill from any existing ~/.config/oim/config.json (a prior direct
        // CLI run) only when this app has no saved preference of its own yet.
        let existing = NodeConfigBridge.loadExisting()

        memoryCapPct = defaults.object(forKey: Keys.cap) != nil
            ? defaults.double(forKey: Keys.cap)
            : (existing?.memoryCapPct ?? 0.5)

        if let saved = defaults.string(forKey: Keys.region), let r = Region(rawValue: saved) {
            region = r
        } else {
            region = .us
        }

        if let saved = defaults.string(forKey: Keys.scheduleMode), let m = ScheduleMode(rawValue: saved) {
            scheduleMode = m
        } else {
            scheduleMode = (existing?.schedule?.mode == "window") ? .window : .always
        }

        scheduleStart = defaults.string(forKey: Keys.scheduleStart) ?? existing?.schedule?.dailyStart ?? "22:00"
        scheduleEnd = defaults.string(forKey: Keys.scheduleEnd) ?? existing?.schedule?.dailyEnd ?? "07:00"

        if let saved = defaults.stringArray(forKey: Keys.scheduleDays) {
            scheduleDays = Set(saved.compactMap(Weekday.init))
        } else {
            scheduleDays = Set((existing?.schedule?.days ?? []).compactMap(Weekday.init))
        }

        // Sanitize any prefilled value: a loopback address (e.g.
        // "http://localhost:8765", which older pre-pull builds auto-derived and
        // left in ~/.config/oim/config.json) is never a valid direct address —
        // a remote coordinator can't reach this Mac's loopback. Left in the
        // field it silently forced the old push path and the node earned
        // nothing. Blank it so the node uses the default outbound pull mode.
        reachabilityEndpoint = Self.sanitizedReachability(
            defaults.string(forKey: Keys.reachabilityEndpoint) ?? existing?.reachabilityEndpoint ?? "")

        exoMonitor.startPolling()
    }

    /// Returns `endpoint` unless it resolves to a loopback/unspecified host
    /// (localhost, 127.x, ::1, 0.0.0.0) — those are never reachable by a remote
    /// coordinator, so we drop them to "" and let the node run in default pull
    /// mode. Mirrors the agent's own `isLoopbackReachability` guard so the UI and
    /// the node binary agree on what counts as a usable direct address.
    static func sanitizedReachability(_ endpoint: String) -> String {
        let trimmed = endpoint.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return "" }
        // Normalize a scheme first: URLComponents parses a bare "localhost:8765"
        // as scheme=localhost with no host, so prepend one when it's missing.
        let withScheme = trimmed.contains("://") ? trimmed : "http://" + trimmed
        var host = trimmed
        if let comps = URLComponents(string: withScheme), let h = comps.host {
            host = h // URLComponents already strips the [] around an IPv6 host
        }
        let loopbackHosts: Set<String> = ["localhost", "127.0.0.1", "::1", "0.0.0.0"]
        if loopbackHosts.contains(host.lowercased()) || host.hasPrefix("127.") {
            return ""
        }
        return trimmed
    }

    func persistSettings() {
        defaults.set(memoryCapPct, forKey: Keys.cap)
        defaults.set(region.rawValue, forKey: Keys.region)
        defaults.set(scheduleMode.rawValue, forKey: Keys.scheduleMode)
        defaults.set(scheduleStart, forKey: Keys.scheduleStart)
        defaults.set(scheduleEnd, forKey: Keys.scheduleEnd)
        defaults.set(scheduleDays.map(\.rawValue), forKey: Keys.scheduleDays)
        defaults.set(reachabilityEndpoint, forKey: Keys.reachabilityEndpoint)
    }

    // MARK: - Node control

    func startNode() {
        walletCoordinator.resetSessionLinkState()
        let options = NodeProcessController.LaunchOptions(
            coordinatorURL: region.coordinatorURL,
            exoURL: exoMonitor.exoURL,
            cap: memoryCapPct,
            region: region.rawValue,
            scheduleMode: scheduleMode.rawValue,
            scheduleStart: scheduleMode == .window ? scheduleStart : nil,
            scheduleEnd: scheduleMode == .window ? scheduleEnd : nil,
            scheduleDays: scheduleMode == .window ? scheduleDays.map(\.rawValue) : nil,
            reachabilityEndpoint: {
                let sane = Self.sanitizedReachability(reachabilityEndpoint)
                return sane.isEmpty ? nil : sane
            }()
        )
        nodeController.start(options)
    }

    func stopNode() {
        nodeController.stop()
    }

    /// Links the running node to the wallet account. Idempotent — safe to
    /// call every time the node reaches `.running` (including across app
    /// launches for the same node), since there's no read-only "already
    /// linked?" endpoint to check first (see WalletLinkCoordinator).
    func linkNodeIfPossible() async {
        guard case .running(let nodeID) = nodeController.state else { return }
        guard walletCoordinator.walletStore.hasWallet else { return }
        await walletCoordinator.linkCurrentNode(nodeID: nodeID, coordinatorURL: region.coordinatorURL)
    }

    /// Starts/stops the local (/detect) and coordinator (/nodes) pollers that
    /// back the "This node: N peers, X/Y GB used" and in-flight/queue summary
    /// lines — both endpoints only mean anything while a node process is
    /// actually up, so they track the node's own state rather than polling
    /// unconditionally like exoMonitor does.
    func nodeStateChanged(_ newState: NodeProcessController.State) {
        if case .running(let nodeID) = newState {
            Task { await linkNodeIfPossible() }
            detectMonitor.startPolling()
            podMetricsMonitor.startPolling(nodeID: nodeID, coordinatorURL: region.coordinatorURL)
        } else {
            detectMonitor.stopPolling()
            podMetricsMonitor.stopPolling()
        }
    }

    // MARK: - Combined state for the menu bar icon / banners

    var isNodeRunning: Bool {
        if case .running = nodeController.state { return true }
        return false
    }

    /// The state the proactive "you're contributing for free" banner and the
    /// degraded icon key off — true only once the node is actually running
    /// and hasn't been confirmed linked this session.
    var showsUnlinkedWarning: Bool {
        isNodeRunning && !walletCoordinator.linkedThisSession
    }

    var isExoDegraded: Bool {
        isNodeRunning && exoMonitor.health != .healthy
    }

    enum IconState {
        case idle, healthy, degraded, error
    }

    var iconState: IconState {
        if case .failed = nodeController.state { return .error }
        guard isNodeRunning else { return .idle }
        if isExoDegraded || showsUnlinkedWarning { return .degraded }
        return .healthy
    }
}
