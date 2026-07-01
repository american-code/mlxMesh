import SwiftUI

struct TVContentView: View {
    @Environment(TopologyStore.self) private var store
    @State private var selectedPod: PodHealthDigest?
    @State private var selectedNode: NodeSnapshot?

    var body: some View {
        NavigationSplitView {
            List(store.pods, selection: $selectedPod) { pod in
                TVPodRow(pod: pod, nodes: store.nodesByPod[pod.podId] ?? [])
                    .tag(pod)
            }
            .navigationTitle("Open Inference Mesh")
        } detail: {
            if let pod = selectedPod {
                TVRegionDetailView(
                    pod: pod,
                    nodes: store.nodesByPod[pod.podId] ?? [],
                    selectedNode: $selectedNode
                )
            } else {
                TVGlobalView()
            }
        }
        .onChange(of: store.pods) { _, pods in
            if selectedPod == nil { selectedPod = pods.first }
        }
    }
}

// MARK: - Sidebar

struct TVPodRow: View {
    let pod: PodHealthDigest
    let nodes: [NodeSnapshot]

    private var statusCounts: [NodeStatus: Int] {
        nodes.reduce(into: [:]) { acc, n in acc[n.computedStatus, default: 0] += 1 }
    }

    private var healthColor: Color {
        let s = pod.aggregateHealthScore
        return s >= 0.7 ? NodeStatus.live.color : s >= 0.4 ? NodeStatus.degraded.color : NodeStatus.unreachable.color
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text(pod.podId).font(.headline)
                Spacer()
                Text(pod.aggregateHealthScore.formattedPct)
                    .font(.system(size: 17, weight: .bold, design: .rounded))
                    .foregroundStyle(healthColor)
                    .monospacedDigit()
            }

            HStack(spacing: 20) {
                TVMiniStat(label: "Nodes", value: "\(pod.nodeCountApprox)")
                TVMiniStat(label: "Tok/s", value: pod.aggregateToksPerSec.formattedTps)
                TVMiniStat(label: "Mem", value: pod.totalMemoryGb.formattedGb)
            }

            HStack(spacing: 12) {
                ForEach(NodeStatus.allCases, id: \.sortOrder) { status in
                    if let count = statusCounts[status], count > 0 {
                        HStack(spacing: 4) {
                            Circle().fill(status.color).frame(width: 7, height: 7)
                            Text("\(count)")
                                .font(.caption)
                                .foregroundStyle(status.color)
                        }
                    }
                }
            }
        }
        .padding(.vertical, 8)
    }
}

// MARK: - Detail: Region

struct TVRegionDetailView: View {
    let pod: PodHealthDigest
    let nodes: [NodeSnapshot]
    @Binding var selectedNode: NodeSnapshot?

    private var sorted: [NodeSnapshot] {
        nodes.sorted { $0.computedStatus.sortOrder < $1.computedStatus.sortOrder }
    }

    var body: some View {
        HStack(spacing: 0) {
            // Left: graph + aggregate stats
            VStack(spacing: 24) {
                HStack {
                    VStack(alignment: .leading, spacing: 4) {
                        Text(pod.podId).font(.title2.bold())
                        Text(pod.regionHint.uppercased() + " · " + pod.coordinatorEndpoint)
                            .font(.callout)
                            .foregroundStyle(.secondary)
                    }
                    Spacer()
                    TVHealthBadge(score: pod.aggregateHealthScore)
                }
                .padding(.horizontal, 40)
                .padding(.top, 30)

                NetworkGraphView(nodes: nodes,
                                 podId: pod.podId,
                                 region: pod.regionHint,
                                 selected: $selectedNode)
                    .frame(maxHeight: 420)
                    .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 20))
                    .padding(.horizontal, 40)

                HStack(spacing: 40) {
                    TVStatPill(label: "Tok / s", value: pod.aggregateToksPerSec.formattedTps,
                               color: NodeStatus.live.color)
                    TVStatPill(label: "Memory", value: pod.totalMemoryGb.formattedGb,
                               color: .purple)
                    TVStatPill(label: "Models", value: "\(pod.servableModelIds.count)",
                               color: .cyan)
                    TVStatPill(label: "Nodes", value: "\(pod.nodeCountApprox)",
                               color: .orange)
                }
                .padding(.horizontal, 40)

                Spacer()
            }
            .frame(maxWidth: .infinity)

            Divider().padding(.vertical, 40)

            // Right: node list
            List(sorted, selection: $selectedNode) { node in
                TVNodeRow(node: node).tag(node)
            }
            .listStyle(.grouped)
            .frame(width: 500)
            .navigationTitle(pod.podId)
        }
    }
}

// MARK: - Detail: Global (no selection)

struct TVGlobalView: View {
    @Environment(TopologyStore.self) private var store

    var body: some View {
        VStack(spacing: 50) {
            VStack(spacing: 12) {
                Text("Open Inference Mesh")
                    .font(.largeTitle.bold())
                Text("Select a region from the sidebar")
                    .font(.title3)
                    .foregroundStyle(.secondary)
            }

            HStack(spacing: 50) {
                TVStatPill(label: "Live Nodes",  value: "\(store.liveCount)",
                           color: NodeStatus.live.color)
                TVStatPill(label: "Total Tok/s", value: store.totalTps.formattedTps,
                           color: .purple)
                TVStatPill(label: "Committed Mem", value: store.totalMemoryGb.formattedGb,
                           color: .cyan)
                TVStatPill(label: "Regions",     value: "\(store.pods.count)",
                           color: .orange)
            }

            if store.isLoading {
                ProgressView("Refreshing…")
                    .foregroundStyle(.secondary)
            }
        }
        .padding()
    }
}

// MARK: - Reusable components

struct TVNodeRow: View {
    let node: NodeSnapshot

    var body: some View {
        let status = node.computedStatus
        HStack(spacing: 14) {
            Circle().fill(status.color).frame(width: 10, height: 10)
            VStack(alignment: .leading, spacing: 4) {
                Text(node.label).font(.headline)
                Text(status.label)
                    .font(.caption)
                    .foregroundStyle(status.color)
            }
            Spacer()
            VStack(alignment: .trailing, spacing: 4) {
                Text(node.measuredToksPerSec.formattedTps)
                    .font(.system(size: 17, weight: .bold, design: .rounded))
                    .foregroundStyle(status.color)
                    .monospacedDigit()
                Text(node.declaredMemoryGb.formattedGb)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 6)
    }
}

struct TVStatPill: View {
    let label: String
    let value: String
    var color: Color = .primary

    var body: some View {
        VStack(spacing: 8) {
            Text(value)
                .font(.system(size: 38, weight: .bold, design: .rounded))
                .foregroundStyle(color)
                .monospacedDigit()
            Text(label)
                .font(.callout)
                .foregroundStyle(.secondary)
                .textCase(.uppercase)
                .kerning(0.5)
        }
        .padding(.horizontal, 28)
        .padding(.vertical, 18)
        .background(color.opacity(0.08), in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).strokeBorder(color.opacity(0.2), lineWidth: 1))
    }
}

struct TVMiniStat: View {
    let label: String
    let value: String

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label)
                .font(.caption2)
                .foregroundStyle(.secondary)
                .textCase(.uppercase)
            Text(value)
                .font(.system(size: 16, weight: .bold, design: .rounded))
                .monospacedDigit()
        }
    }
}

struct TVHealthBadge: View {
    let score: Double
    var color: Color {
        score >= 0.7 ? NodeStatus.live.color : score >= 0.4 ? NodeStatus.degraded.color : NodeStatus.unreachable.color
    }
    var body: some View {
        Text(score.formattedPct)
            .font(.system(size: 18, weight: .bold, design: .rounded))
            .foregroundStyle(color)
            .padding(.horizontal, 14)
            .padding(.vertical, 7)
            .background(color.opacity(0.12), in: RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(color.opacity(0.3), lineWidth: 1))
    }
}
