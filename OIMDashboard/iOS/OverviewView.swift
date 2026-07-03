import SwiftUI

struct OverviewView: View {
    @Environment(TopologyStore.self) private var store
    @Binding var selectedNode: NodeSnapshot?

    private let columns = [GridItem(.flexible()), GridItem(.flexible())]

    var body: some View {
        ScrollView {
            VStack(spacing: 20) {
                // Top stats bar
                StatsHeaderView()

                // Error banner
                if let err = store.error {
                    ErrorBanner(message: err)
                }

                // World map
                GlobalMapView(nodes: store.allNodes, selected: $selectedNode)
                    .frame(height: 260)
                    .clipShape(RoundedRectangle(cornerRadius: 16))
                    .shadow(radius: 4)

                // Try the mesh — interactive live query
                if !store.pods.isEmpty {
                    TryMeshView()
                }

                // Network Load — queue / in-flight / backpressure
                if !store.metricsByPod.isEmpty {
                    NetworkLoadView()
                }

                // Security & coordination layer (iOS pointer-host devices)
                CoordinationLayerView()

                // Pod cards
                LazyVGrid(columns: columns, spacing: 16) {
                    ForEach(store.pods) { pod in
                        PodSummaryView(pod: pod,
                                       nodes: store.nodesByPod[pod.podId] ?? [])
                    }
                }

                // Status legend
                StatusLegendView()
                    .padding(.bottom, 8)
            }
            .padding(.horizontal)
            .padding(.top, 8)
        }
        .background(Color(.systemGroupedBackground))
        .refreshable { await store.refresh() }
        .overlay(alignment: .bottomTrailing) {
            if let updated = store.lastUpdated {
                Text("Updated \(updated.formatted(date: .omitted, time: .shortened))")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .padding(10)
            }
        }
    }
}

// MARK: - Sub-views

struct StatsHeaderView: View {
    @Environment(TopologyStore.self) private var store

    private var hasMetrics: Bool { !store.metricsByPod.isEmpty }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 0) {
                StatPill(value: "\(store.liveCount)", label: "Live", color: NodeStatus.live.color)
                Divider().frame(height: 36)
                StatPill(value: store.totalTps.formattedTps, label: "Throughput")
                Divider().frame(height: 36)
                StatPill(value: store.totalMemoryGb.formattedGb, label: "Committed")
                Divider().frame(height: 36)
                StatPill(value: "\(store.pods.count)", label: "Regions")
            }
            .padding(.vertical, 12)

            if hasMetrics {
                Divider()
                HStack(spacing: 0) {
                    StatPill(value: "\(store.totalQueued)", label: "Queued",
                             color: store.totalQueued > 0 ? NodeStatus.degraded.color : .secondary)
                    Divider().frame(height: 36)
                    StatPill(value: "\(store.totalInFlight)", label: "In-flight",
                             color: store.totalInFlight > 0 ? .blue : .secondary)
                    Divider().frame(height: 36)
                    BackpressurePill(pct: store.avgBackpressurePct)
                }
                .padding(.vertical, 12)
            }
        }
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14))
    }
}

struct BackpressurePill: View {
    let pct: Double
    private var color: Color { pct >= 70 ? NodeStatus.unreachable.color : pct >= 30 ? NodeStatus.degraded.color : NodeStatus.live.color }
    private var label: String { pct >= 70 ? "High load" : pct >= 30 ? "Moderate" : "Normal" }

    var body: some View {
        VStack(spacing: 2) {
            Text("\(Int(pct.rounded()))%")
                .font(.system(size: 20, weight: .bold, design: .rounded))
                .foregroundStyle(color)
                .monospacedDigit()
            Text(label)
                .font(.caption2)
                .foregroundStyle(.secondary)
                .textCase(.uppercase)
                .kerning(0.5)
        }
        .frame(maxWidth: .infinity)
    }
}

struct StatPill: View {
    let value: String
    let label: String
    var color: Color = .primary

    var body: some View {
        VStack(spacing: 2) {
            Text(value)
                .font(.system(size: 20, weight: .bold, design: .rounded))
                .foregroundStyle(color)
                .monospacedDigit()
            Text(label)
                .font(.caption2)
                .foregroundStyle(.secondary)
                .textCase(.uppercase)
                .kerning(0.5)
        }
        .frame(maxWidth: .infinity)
    }
}

struct ErrorBanner: View {
    let message: String
    var body: some View {
        Label(message, systemImage: "exclamationmark.triangle.fill")
            .font(.caption)
            .foregroundStyle(.red)
            .padding(10)
            .background(.red.opacity(0.1), in: RoundedRectangle(cornerRadius: 10))
            .frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct StatusLegendView: View {
    private let statuses: [NodeStatus] = [.live, .degraded, .stale, .unreachable]
    var body: some View {
        HStack(spacing: 16) {
            ForEach(statuses, id: \.sortOrder) { s in
                Label(s.label, systemImage: "circle.fill")
                    .font(.caption2)
                    .foregroundStyle(s.color)
            }
        }
        .frame(maxWidth: .infinity)
    }
}
