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

    var body: some View {
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
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14))
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
