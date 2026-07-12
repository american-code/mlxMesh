import SwiftUI
import MapKit

struct NodeDetailView: View {
    let node: NodeSnapshot
    @Environment(\.dismiss) private var dismiss

    private var status: NodeStatus { node.computedStatus }

    var body: some View {
        NavigationStack {
            List {
                // Status header
                Section {
                    HStack(spacing: 10) {
                        StatusIndicator(status: status)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(status.label)
                                .font(.headline)
                                .foregroundStyle(status.color)
                            Text(node.label)
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                        }
                        Spacer()
                    }
                    .padding(.vertical, 4)
                }

                // Performance stats
                Section("Performance") {
                    StatRow(label: "Throughput", value: node.measuredToksPerSec.formattedTps,
                            valueColor: status.color)
                    StatRow(label: "Declared Memory", value: node.declaredMemoryGb.formattedGb)
                    StatRow(label: "Committed Memory", value: node.committedMemoryGb.formattedGb)
                    StatRow(label: "In-flight jobs", value: "\(node.inFlightJobs)",
                            valueColor: node.inFlightJobs > 0 ? .blue : nil)
                }

                // Models
                if let models = node.models, !models.isEmpty {
                    Section("Models") {
                        ForEach(models, id: \.modelId) { model in
                            VStack(alignment: .leading, spacing: 4) {
                                HStack(spacing: 6) {
                                    Text(model.modelId)
                                        .font(.system(size: 14, weight: .medium))
                                    LoadedBadge(loaded: model.loaded ?? false)
                                }
                                HStack(spacing: 8) {
                                    Chip(model.quantization)
                                    Chip(model.runtime)
                                    Chip("\(model.maxContextTokens.formatted()) ctx")
                                    if model.isMoe { Chip("MoE") }
                                }
                            }
                            .padding(.vertical, 4)
                        }
                    }
                }

                // Location
                Section("Location") {
                    StatRow(label: "Region", value: node.geographicHint.uppercased())
                    StatRow(label: "Latitude", value: String(format: "%.4f°", node.geoLat))
                    StatRow(label: "Longitude", value: String(format: "%.4f°", node.geoLng))

                    if node.geoLat != 0 || node.geoLng != 0 {
                        Map(position: .constant(.region(
                            MKCoordinateRegion(
                                center: CLLocationCoordinate2D(latitude: node.geoLat, longitude: node.geoLng),
                                latitudinalMeters: 500_000,
                                longitudinalMeters: 500_000
                            )
                        ))) {
                            Annotation(node.label,
                                       coordinate: CLLocationCoordinate2D(latitude: node.geoLat, longitude: node.geoLng)) {
                                Circle()
                                    .fill(status.color)
                                    .frame(width: 14, height: 14)
                                    .shadow(color: status.color.opacity(0.5), radius: 4)
                            }
                        }
                        .mapStyle(.standard(pointsOfInterest: .excludingAll))
                        .frame(height: 180)
                        .clipShape(RoundedRectangle(cornerRadius: 10))
                        .listRowInsets(EdgeInsets(top: 6, leading: 0, bottom: 6, trailing: 0))
                    }
                }

                // Capabilities
                Section("Capabilities") {
                    // enclaveAttested is coordinator-verified hardware proof; hasSecureEnclave
                    // is merely self-declared by the node and not trusted for gating — the
                    // same distinction the web dashboard draws (see NodeSnapshot.enclaveAttested).
                    VStack(alignment: .leading, spacing: 3) {
                        StatRow(label: "Secure Enclave",
                                value: node.enclaveAttested ? "Attested"
                                    : node.hasSecureEnclave ? "Claimed, unverified" : "Not available",
                                valueColor: node.enclaveAttested ? NodeStatus.live.color
                                    : node.hasSecureEnclave ? NodeStatus.degraded.color : nil)
                        if node.hasSecureEnclave && !node.enclaveAttested {
                            Text("Self-declared only — not cryptographically verified. High-sensitivity jobs will not route here.")
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                        }
                    }
                    StatRow(label: "Cluster",
                            value: node.isCluster
                                ? "Yes · \(node.clusterDeviceCount ?? 1) devices"
                                : "Single device")
                    if let families = node.clusterChipFamilies, !families.isEmpty {
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Chip families")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            HStack(spacing: 6) {
                                ForEach(Array(Set(families)).sorted(), id: \.self) { family in
                                    Chip(family)
                                }
                            }
                        }
                        .padding(.vertical, 2)
                    }
                }

                // Identity
                Section("Identity") {
                    StatRow(label: "Last Seen", value: formattedLastSeen)
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Node ID")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        Text(node.nodeId)
                            .font(.system(size: 11, design: .monospaced))
                            .textSelection(.enabled)
                    }
                    .padding(.vertical, 2)
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Endpoint")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        Text(node.reachabilityEndpoint)
                            .font(.system(size: 12, design: .monospaced))
                            .foregroundStyle(.blue)
                            .textSelection(.enabled)
                    }
                    .padding(.vertical, 2)
                }
            }
            .navigationTitle(node.label)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }

    private var formattedLastSeen: String {
        guard let date = ISO8601DateFormatter().date(from: node.lastSeenAt) else { return node.lastSeenAt }
        return date.formatted(date: .omitted, time: .standard)
    }
}

// MARK: - Components

struct StatusIndicator: View {
    let status: NodeStatus
    @State private var pulsing = false

    var body: some View {
        ZStack {
            Circle()
                .fill(status.color.opacity(pulsing ? 0.15 : 0.3))
                .frame(width: 36, height: 36)
                .scaleEffect(pulsing ? 1.2 : 1)
                .animation(status == .live ? .easeInOut(duration: 1.6).repeatForever(autoreverses: true) : .default, value: pulsing)
            Image(systemName: status.systemImage)
                .foregroundStyle(status.color)
                .font(.system(size: 16))
        }
        .onAppear { if status == .live { pulsing = true } }
    }
}

struct StatRow: View {
    let label: String
    let value: String
    var valueColor: Color? = nil

    var body: some View {
        HStack {
            Text(label).foregroundStyle(.secondary)
            Spacer()
            Text(value)
                .foregroundStyle(valueColor ?? .primary)
                .fontWeight(valueColor != nil ? .semibold : .regular)
                .monospacedDigit()
        }
    }
}

struct Chip: View {
    let text: String
    init(_ text: String) { self.text = text }
    var body: some View {
        Text(text)
            .font(.system(size: 10, weight: .medium))
            .foregroundStyle(.secondary)
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .background(.quaternary, in: Capsule())
    }
}

/// Distinct from Chip (which is neutral/informational) — this is a status
/// signal: whether Exo currently has an active inference instance for this
/// model, not just whether it's downloaded to disk. Green "Loaded" is
/// dispatchable right now; amber "Cold" is visible but not routable until
/// warmed (see the "Load" trigger, WarmModel).
struct LoadedBadge: View {
    let loaded: Bool
    var body: some View {
        Text(loaded ? "Loaded" : "Cold")
            .font(.system(size: 9, weight: .semibold))
            .textCase(.uppercase)
            .foregroundStyle(loaded ? .green : .orange)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background((loaded ? Color.green : Color.orange).opacity(0.15), in: Capsule())
    }
}
