import SwiftUI

struct NodeStatusRow: View {
    @Bindable var appState: AppState

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Image(systemName: statusSymbol)
                    .foregroundStyle(statusColor)
                Text("Node")
                    .fontWeight(.medium)
                Spacer()
                actionButton
            }
            Text(statusLabel)
                .font(.caption)
                .foregroundStyle(.secondary)
            if case .running = appState.nodeController.state {
                NodeActivityIndicator(activity: appState.nodeActivity)
            }
            if appState.isExoDegraded {
                Text("0 models available — Exo not detected")
                    .font(.caption)
                    .foregroundStyle(.orange)
            }
            if case .running = appState.nodeController.state {
                if let line = clusterMemoryLine {
                    Text(line)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
                if let line = jobActivityLine {
                    Text(line)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
                if let (text, isWarning) = reachabilityLine {
                    Text(text)
                        .font(.caption2)
                        .foregroundStyle(isWarning ? .orange : .secondary)
                }
            }
        }
    }

    // How this node receives work. In the default pull mode it connects OUT to
    // the coordinator (like an ASIC pointed at a mining pool), so there's
    // nothing to configure and no NAT/reachability concern — hence no warning.
    // "manual" is the opt-in push mode (an explicit "Reachable at (advanced)"
    // address). Driven off /detect's port_mapping.
    private var reachabilityLine: (text: String, isWarning: Bool)? {
        guard let snapshot = appState.detectMonitor.snapshot else { return nil }
        switch snapshot.portMapping {
        case "pull":
            return ("Connected — receiving work ✓", false)
        case "manual":
            return ("Reachable — manually configured ✓", false)
        default:
            return nil
        }
    }

    // "This node: 3 Exo peers, 42/128 GB used" (or, when not clustered, just
    // the GB figures) — one glance answer to "how much of my Mac is this
    // actually using," sourced from the local node's own /detect endpoint.
    private var clusterMemoryLine: String? {
        guard let snapshot = appState.detectMonitor.snapshot, snapshot.totalRAMGB > 0 else { return nil }
        let used = String(format: "%.0f", snapshot.usedRAMGB)
        let total = String(format: "%.0f", snapshot.totalRAMGB)
        if snapshot.isCluster, snapshot.clusterDeviceCount > 1 {
            return "This node: \(snapshot.clusterDeviceCount) Exo peers, \(used)/\(total) GB used"
        }
        return "This node: \(used)/\(total) GB used"
    }

    // "2 in-flight · pod queue 3/50 (6.0% backpressure)" — this node's own
    // in-flight count plus the shared pod-wide queue signal, from the same
    // coordinator /nodes response the web dashboard's header pills use.
    private var jobActivityLine: String? {
        guard let inFlight = appState.podMetricsMonitor.inFlightJobs else { return nil }
        var line = "\(inFlight) in-flight"
        if let metrics = appState.podMetricsMonitor.metrics {
            let backpressure = String(format: "%.1f", metrics.backpressurePct)
            line += " · pod queue \(metrics.queueDepth)/\(metrics.queueCapacity) (\(backpressure)% backpressure)"
        }
        return line
    }

    private var statusSymbol: String {
        switch appState.nodeController.state {
        case .stopped: return "circle"
        case .starting: return "circle.dotted"
        case .running: return "checkmark.circle.fill"
        case .failed: return "xmark.circle.fill"
        }
    }

    private var statusColor: Color {
        switch appState.nodeController.state {
        case .stopped: return .secondary
        case .starting: return .secondary
        case .running: return appState.isExoDegraded ? .orange : .green
        case .failed: return .red
        }
    }

    private var statusLabel: String {
        switch appState.nodeController.state {
        case .stopped: return "Stopped"
        case .starting: return "Starting…"
        case .running(let nodeID):
            return "Running — \(String(nodeID.prefix(12)))…"
        case .failed(let message):
            return message
        }
    }

    @ViewBuilder
    private var actionButton: some View {
        switch appState.nodeController.state {
        case .stopped, .failed:
            Button("Start") { appState.startNode() }
                .controlSize(.small)
        case .starting:
            Button("Cancel") { appState.stopNode() }
                .controlSize(.small)
        case .running:
            Button("Stop") { appState.stopNode() }
                .controlSize(.small)
        }
    }
}
