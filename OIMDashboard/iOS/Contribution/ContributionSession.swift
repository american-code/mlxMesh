import UIKit
import Foundation

/// Manages an iOS device's participation as a COORDINATION + SECURITY node — it
/// runs the on-device classifier and hosts encrypted payload pointers so the
/// coordinator never sees raw prompts. It does NOT serve LLM inference (Exo does
/// not run on iOS; iOS was never meant to be a compute node — see
/// CoordinationBridge). Because the work is light (classification + crypto +
/// pointer hosting), this runs on any modern iPad, not just M-series.
///
/// The layer is additive: ending a session degrades routing to normal
/// coordinator behavior, never breaks it.
@MainActor
final class ContributionSession: ObservableObject {

    @Published var state: SessionState = .idle
    @Published var pointersServedThisSession: Int = 0
    @Published var inFlightPointerCount: Int = 0
    @Published var thermalState: ProcessInfo.ThermalState = .nominal
    @Published var lastError: String?
    /// Port the encrypted-pointer host is listening on while active (0 = not up).
    @Published var hostPort: UInt16 = 0
    /// When the current active session started, for an uptime readout.
    @Published var activeSince: Date?

    enum SessionState: Equatable {
        case idle
        case starting
        case active
        case pausedThermal
        case pausedMemory
        case ending(reason: EndReason)
        case ended(summary: SessionSummary)
    }

    enum EndReason: Equatable {
        case userInitiated
        case thermalCritical
        case memoryPressure
        case appBackgrounding
        case coordinatorDisconnected
    }

    struct SessionSummary: Equatable {
        let pointersServed: Int
        let durationSeconds: Double
        let endReason: EndReason
    }

    private let bridge: CoordinationBridge
    private let thermal = ThermalMonitor()
    private let memory = MemoryPressureMonitor()

    private var deviceId: String = ""
    private var geographicHint: String = "us"
    private var startedAt: Date?
    private var backgroundTask: UIBackgroundTaskIdentifier = .invalid
    private var memoryResumeWorkItem: DispatchWorkItem?
    private var willResignObserver: NSObjectProtocol?
    private var heartbeatTask: Task<Void, Never>?

    init(bridge: CoordinationBridge = CoordinationBridge()) {
        self.bridge = bridge
    }

    // MARK: - Start

    func startSession(deviceId: String, geographicHint: String, coordinatorURL: URL) async {
        guard state == .idle || isEnded else { return }
        state = .starting
        lastError = nil
        self.deviceId = deviceId
        self.geographicHint = geographicHint
        // Announce to the network coordinator (resolved from the configured
        // directory), never localhost — the device may be anywhere.
        bridge.setCoordinator(coordinatorURL)
        startedAt = Date()

        wireThermal()
        wireMemory()
        willResignObserver = NotificationCenter.default.addObserver(
            forName: UIApplication.willResignActiveNotification, object: nil, queue: .main
        ) { [weak self] _ in
            Task { await self?.endSession(reason: .appBackgrounding) }
        }

        // Start hosting encrypted pointers.
        do {
            hostPort = try LocalPayloadServer.shared.start()
        } catch {
            lastError = "Could not start the local pointer host: \(error.localizedDescription)"
            await endSession(reason: .coordinatorDisconnected)
            return
        }
        bridge.setAcceptingPointers(true)
        await bridge.announceParticipation(deviceId: deviceId, geographicHint: geographicHint)
        startHeartbeat()

        activeSince = Date()
        state = .active
    }

    /// Re-announces on a heartbeat so the coordinator's TTL (CoordinationTTL, 90s)
    /// keeps this device listed while active. Cancelled on any session end.
    private func startHeartbeat() {
        heartbeatTask?.cancel()
        heartbeatTask = Task { [weak self] in
            // Pull the coordinator's count once on start so the stat isn't stuck
            // at 0 until the first heartbeat.
            if let self, self.isActiveState { await self.refreshPointersServed() }
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 30_000_000_000) // 30s
                guard let self, !Task.isCancelled else { return }
                if self.isActiveState {
                    await self.bridge.announceParticipation(
                        deviceId: self.deviceId, geographicHint: self.geographicHint)
                    await self.refreshPointersServed()
                }
            }
        }
    }

    /// Syncs the "Pointers" stat from the coordinator's authoritative served count
    /// for this device. The coordinator increments it whenever it attributes an
    /// encrypted pointer to this device — the real, credited work — so this
    /// reflects earnings-generating activity instead of staying pinned at 0.
    private func refreshPointersServed() async {
        if let n = await bridge.fetchPointersServed(deviceId: deviceId) {
            pointersServedThisSession = n
        }
    }

    private var isActiveState: Bool { state == .active || state == .pausedThermal || state == .pausedMemory }

    // MARK: - End (identical clean path for EVERY reason)

    func endSession(reason: EndReason) async {
        if case .ending = state { return }
        if case .ended = state { return }
        state = .ending(reason: reason)

        bridge.setAcceptingPointers(false)
        backgroundTask = UIApplication.shared.beginBackgroundTask(withName: "oim.coord.shutdown") { [weak self] in
            self?.forceEndSession()
        }
        await drainInFlight(timeout: 10)
        await bridge.withdrawParticipation(deviceId: deviceId)
        LocalPayloadServer.shared.stop()
        teardownObservers()
        endBackgroundTask()

        state = .ended(summary: SessionSummary(
            pointersServed: pointersServedThisSession,
            durationSeconds: startedAt.map { Date().timeIntervalSince($0) } ?? 0,
            endReason: reason))
    }

    private func forceEndSession() {
        bridge.setAcceptingPointers(false)
        inFlightPointerCount = 0
        let id = deviceId
        Task { await bridge.withdrawParticipation(deviceId: id) }
        LocalPayloadServer.shared.stop()
        teardownObservers()
        endBackgroundTask()
        state = .ended(summary: SessionSummary(
            pointersServed: pointersServedThisSession,
            durationSeconds: startedAt.map { Date().timeIntervalSince($0) } ?? 0,
            endReason: .appBackgrounding))
    }

    // MARK: - Internals

    private var isEnded: Bool { if case .ended = state { return true }; return false }

    private func wireThermal() {
        thermal.onStateChange = { [weak self] newState in
            guard let self else { return }
            self.thermalState = newState
            switch newState {
            case .nominal, .fair:
                if self.state == .pausedThermal {
                    self.bridge.setAcceptingPointers(true)
                    self.state = .active
                }
            case .serious:
                self.bridge.setAcceptingPointers(false)
                if self.state == .active { self.state = .pausedThermal }
            case .critical:
                Task { await self.endSession(reason: .thermalCritical) }
            @unknown default:
                break
            }
        }
        thermal.startMonitoring()
    }

    private func wireMemory() {
        memory.onMemoryWarning = { [weak self] in
            guard let self else { return }
            self.bridge.setAcceptingPointers(false)
            if self.state == .active { self.state = .pausedMemory }
            URLCache.shared.removeAllCachedResponses()
            self.memoryResumeWorkItem?.cancel()
            let work = DispatchWorkItem { [weak self] in
                guard let self else { return }
                if self.state == .pausedMemory {
                    self.bridge.setAcceptingPointers(true)
                    self.state = .active
                }
            }
            self.memoryResumeWorkItem = work
            DispatchQueue.main.asyncAfter(deadline: .now() + 60, execute: work)
        }
        memory.startMonitoring()
    }

    private func drainInFlight(timeout: TimeInterval) async {
        let deadline = Date().addingTimeInterval(timeout)
        while inFlightPointerCount > 0 && Date() < deadline {
            try? await Task.sleep(nanoseconds: 200_000_000)
        }
    }

    private func teardownObservers() {
        heartbeatTask?.cancel()
        heartbeatTask = nil
        thermal.stopMonitoring()
        memory.stopMonitoring()
        memoryResumeWorkItem?.cancel()
        memoryResumeWorkItem = nil
        if let willResignObserver {
            NotificationCenter.default.removeObserver(willResignObserver)
            self.willResignObserver = nil
        }
    }

    private func endBackgroundTask() {
        if backgroundTask != .invalid {
            UIApplication.shared.endBackgroundTask(backgroundTask)
            backgroundTask = .invalid
        }
    }
}
