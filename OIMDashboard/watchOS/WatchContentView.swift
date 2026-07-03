import SwiftUI

struct WatchContentView: View {
    @Environment(TopologyStore.self) private var store

    var body: some View {
        NavigationStack {
            Group {
                if store.pods.isEmpty && !store.isLoading {
                    VStack(spacing: 6) {
                        Image(systemName: "exclamationmark.triangle")
                            .foregroundStyle(.orange)
                        Text(store.error != nil ? "No connection" : "Loading…")
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }
                } else {
                    List {
                        Section("Global") {
                            HStack(spacing: 0) {
                                WatchStat(label: "Live",
                                          value: "\(store.liveCount)",
                                          color: NodeStatus.live.color)
                                WatchStat(label: "Tok/s",
                                          value: store.totalTps.formattedTps)
                                WatchStat(label: "Mem",
                                          value: store.totalMemoryGb.formattedGb)
                            }
                            if !store.metricsByPod.isEmpty {
                                HStack(spacing: 0) {
                                    WatchStat(label: "Queued",
                                              value: "\(store.totalQueued)",
                                              color: store.totalQueued > 0 ? NodeStatus.degraded.color : .primary)
                                    WatchStat(label: "In-flight",
                                              value: "\(store.totalInFlight)",
                                              color: store.totalInFlight > 0 ? .blue : .primary)
                                }
                            }
                        }

                        ForEach(store.pods) { pod in
                            let nodes = store.nodesByPod[pod.podId] ?? []
                            Section(pod.regionHint.uppercased()) {
                                NavigationLink {
                                    WatchRegionView(pod: pod, nodes: nodes)
                                } label: {
                                    HStack(spacing: 6) {
                                        Circle()
                                            .fill(healthColor(pod.aggregateHealthScore))
                                            .frame(width: 7, height: 7)
                                        Text(pod.podId)
                                            .font(.system(size: 13))
                                        Spacer()
                                        Text(pod.aggregateHealthScore.formattedPct)
                                            .font(.system(size: 12, weight: .semibold, design: .rounded))
                                            .foregroundStyle(healthColor(pod.aggregateHealthScore))
                                            .monospacedDigit()
                                    }
                                }
                                HStack(spacing: 10) {
                                    Text("\(pod.nodeCountApprox) nodes")
                                        .font(.caption2)
                                        .foregroundStyle(.secondary)
                                    Text(pod.aggregateToksPerSec.formattedTps)
                                        .font(.caption2)
                                        .foregroundStyle(.secondary)
                                        .monospacedDigit()
                                }
                            }
                        }
                    }
                }
            }
            .navigationTitle("mlxMesh")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    if store.isLoading {
                        ProgressView()
                            .progressViewStyle(.circular)
                            .scaleEffect(0.55)
                    }
                }
            }
        }
    }

    private func healthColor(_ score: Double) -> Color {
        score >= 0.7 ? NodeStatus.live.color : score >= 0.4 ? NodeStatus.degraded.color : NodeStatus.unreachable.color
    }
}

struct WatchRegionView: View {
    let pod: PodHealthDigest
    let nodes: [NodeSnapshot]

    private var sorted: [NodeSnapshot] {
        nodes.sorted { $0.computedStatus.sortOrder < $1.computedStatus.sortOrder }
    }

    var body: some View {
        List {
            Section {
                HStack(spacing: 0) {
                    WatchStat(label: "Tok/s",
                              value: pod.aggregateToksPerSec.formattedTps,
                              color: NodeStatus.live.color)
                    WatchStat(label: "Mem",
                              value: pod.totalMemoryGb.formattedGb)
                    WatchStat(label: "Health",
                              value: pod.aggregateHealthScore.formattedPct)
                }
            }

            Section("Nodes") {
                ForEach(sorted) { node in
                    let status = node.computedStatus
                    HStack(spacing: 6) {
                        Circle()
                            .fill(status.color)
                            .frame(width: 7, height: 7)
                        Text(node.label)
                            .font(.system(size: 12))
                            .lineLimit(1)
                        Spacer()
                        Text(node.measuredToksPerSec.formattedTps)
                            .font(.system(size: 11, design: .rounded))
                            .foregroundStyle(status.color)
                            .monospacedDigit()
                    }
                }
            }
        }
        .navigationTitle(pod.podId)
        .navigationBarTitleDisplayMode(.inline)
    }
}

struct WatchStat: View {
    let label: String
    let value: String
    var color: Color = .primary

    var body: some View {
        VStack(spacing: 2) {
            Text(value)
                .font(.system(size: 12, weight: .bold, design: .rounded))
                .foregroundStyle(color)
                .monospacedDigit()
                .minimumScaleFactor(0.7)
            Text(label)
                .font(.system(size: 7, weight: .semibold))
                .foregroundStyle(.secondary)
                .textCase(.uppercase)
        }
        .frame(maxWidth: .infinity)
    }
}
