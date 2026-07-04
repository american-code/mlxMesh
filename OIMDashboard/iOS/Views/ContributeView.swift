import SwiftUI

/// Presents the iOS device's role as a COORDINATION + SECURITY participant: it
/// classifies queries on-device and hosts encrypted payload pointers so the
/// coordinator never sees raw prompts. This is additive — turning it off
/// degrades to normal coordinator routing, never breaks it. Runs on any modern
/// iPad (including a 2018 A12X iPad Pro); it is NOT inference contribution
/// (Exo, the compute backend, does not run on iOS).
struct ContributeView: View {

    // Owned by ContentView (not here) so the session — and the active
    // pointer-host — survive navigating away from this tab and the 5s data
    // polls that recreate the detail view. A @StateObject here would reset to
    // idle every time the view was rebuilt.
    @ObservedObject var session: ContributionSession
    @Environment(TopologyStore.self) private var store

    var body: some View {
        NavigationStack {
            VStack(spacing: 24) {
                header
                statusBadge

                if case .pausedThermal = session.state {
                    inlineWarning("Device is warm — pausing pointer hosting", color: .orange, icon: "thermometer.high")
                }
                if case .pausedMemory = session.state {
                    inlineWarning("Memory pressure — pausing pointer hosting", color: .orange, icon: "memorychip")
                }
                if isActiveish { liveStats }
                if case let .ended(summary) = session.state {
                    Text("Session complete — \(summary.pointersServed) pointers served")
                        .font(.headline).foregroundStyle(NodeStatus.live.color)
                }

                toggleButton
                if let err = session.lastError {
                    inlineWarning(err, color: .red, icon: "exclamationmark.triangle.fill")
                }
                Spacer()
            }
            .frame(maxWidth: 520)
            .padding()
            .navigationTitle("Coordinate")
        }
    }

    private var header: some View {
        VStack(spacing: 10) {
            Image(systemName: "lock.shield.fill")
                .font(.system(size: 48))
                .foregroundStyle(NodeStatus.live.color)
            Text("Coordination & security layer")
                .font(.title2.bold())
            Text("This device classifies queries on-device and hosts encrypted pointers, so the coordinator never sees your raw prompts. It's an extra layer — the mesh routes normally when it's off.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
    }

    private var statusBadge: some View {
        HStack(spacing: 8) {
            Circle().fill(stateColor).frame(width: 10, height: 10)
            Text(stateLabel).font(.headline)
        }
    }

    private var liveStats: some View {
        VStack(spacing: 14) {
            // A live "it's actually running" line — the role is passive (it hosts
            // encrypted pointers and waits), so the counters below stay 0 until
            // traffic arrives; this row confirms the host is up regardless.
            if session.state == .active {
                TimelineView(.periodic(from: .now, by: 1)) { _ in
                    HStack(spacing: 8) {
                        Circle().fill(NodeStatus.live.color).frame(width: 8, height: 8)
                        Text(hostingLine).font(.subheadline.weight(.medium))
                    }
                }
            }
            HStack(spacing: 28) {
                stat("Pointers", "\(session.pointersServedThisSession)")
                stat("In-flight", "\(session.inFlightPointerCount)")
            }
            HStack(spacing: 10) {
                Circle().fill(thermalColor).frame(width: 9, height: 9)
                Text("Thermal: \(thermalLabel)").font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding()
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14))
    }

    private var hostingLine: String {
        var parts = ["Hosting encrypted pointers"]
        if session.hostPort != 0 { parts.append("on :\(session.hostPort)") }
        if let since = session.activeSince {
            parts.append("· \(Int(Date().timeIntervalSince(since)))s")
        }
        return parts.joined(separator: " ")
    }

    private var toggleButton: some View {
        Button {
            Task {
                if isActiveish {
                    await session.endSession(reason: .userInitiated)
                } else {
                    // Resolve the network coordinator from the configured directory
                    // (NetworkClient rewrites a loopback host to the reachable one),
                    // falling back to the directory host if no pod is known yet.
                    let podEndpoint: String? = store.pods.first?.coordinatorEndpoint
                    let endpoint = podEndpoint.map { NetworkClient.resolvedCoordinator($0) }
                        ?? NetworkClient.directoryURL
                    await session.startSession(
                        deviceId: DeviceIdentity.current,
                        geographicHint: Locale.current.region?.identifier.lowercased() ?? "us",
                        coordinatorURL: URL(string: endpoint) ?? URL(string: "http://localhost:9000")!)
                }
            }
        } label: {
            Text(isActiveish ? "Stop" : "Participate")
                .font(.title3.bold())
                .frame(maxWidth: .infinity)
                .padding()
        }
        .buttonStyle(.borderedProminent)
        .tint(isActiveish ? .red : NodeStatus.live.color)
    }

    // MARK: - Derived UI state

    private var isActiveish: Bool {
        switch session.state {
        case .starting, .active, .pausedThermal, .pausedMemory, .ending: return true
        default: return false
        }
    }

    private var stateLabel: String {
        switch session.state {
        case .idle: return "Idle"
        case .starting: return "Starting…"
        case .active: return "Active"
        case .pausedThermal: return "Paused (thermal)"
        case .pausedMemory: return "Paused (memory)"
        case .ending: return "Ending…"
        case .ended: return "Ended"
        }
    }

    private var stateColor: Color {
        switch session.state {
        case .active: return NodeStatus.live.color
        case .pausedThermal, .pausedMemory, .starting, .ending: return .orange
        default: return .secondary
        }
    }

    private var thermalColor: Color {
        switch session.thermalState {
        case .nominal, .fair: return NodeStatus.live.color
        case .serious: return .orange
        case .critical: return .red
        @unknown default: return .secondary
        }
    }
    private var thermalLabel: String {
        switch session.thermalState {
        case .nominal: return "nominal"
        case .fair: return "fair"
        case .serious: return "serious"
        case .critical: return "critical"
        @unknown default: return "unknown"
        }
    }

    private func stat(_ label: String, _ value: String) -> some View {
        VStack(spacing: 2) {
            Text(value).font(.system(size: 22, weight: .bold, design: .rounded)).monospacedDigit()
            Text(label.uppercased()).font(.caption2).foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity)
    }

    private func inlineWarning(_ text: String, color: Color, icon: String) -> some View {
        Label(text, systemImage: icon)
            .font(.callout)
            .foregroundStyle(color)
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(color.opacity(0.12), in: RoundedRectangle(cornerRadius: 10))
    }

}
