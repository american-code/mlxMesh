import SwiftUI

// NetworkLoadView is the iOS counterpart of the web dashboard's "Network Load"
// (BackpressurePanel): an overall backpressure gauge plus a per-region queue /
// in-flight breakdown, driven by the live PodMetrics the coordinators report.
struct NetworkLoadView: View {
    @Environment(TopologyStore.self) private var store

    private func backpressureColor(_ pct: Double) -> Color {
        pct >= 70 ? NodeStatus.unreachable.color : pct >= 30 ? NodeStatus.degraded.color : NodeStatus.live.color
    }
    private func backpressureLabel(_ pct: Double) -> String {
        pct >= 70 ? "High" : pct >= 30 ? "Moderate" : "Normal"
    }

    private var totalQueueCap: Int {
        store.pods.reduce(0) { $0 + (store.metricsByPod[$1.podId]?.queueCapacity ?? 0) }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            // Header row
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Network Load").font(.headline)
                    Text("queue + in-flight · \(store.pods.count) region\(store.pods.count == 1 ? "" : "s")")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                HStack(spacing: 18) {
                    LoadMiniStat(label: "Queued", value: "\(store.totalQueued)")
                    LoadMiniStat(label: "In-flight", value: "\(store.totalInFlight)")
                    LoadMiniStat(label: "Cap", value: "\(totalQueueCap)")
                }
            }

            // Overall backpressure gauge
            VStack(alignment: .leading, spacing: 6) {
                Text("OVERALL BACKPRESSURE")
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(.secondary)
                    .kerning(0.5)
                HStack(spacing: 10) {
                    GeometryReader { geo in
                        ZStack(alignment: .leading) {
                            Capsule().fill(Color(.tertiarySystemFill))
                            Capsule()
                                .fill(LinearGradient(
                                    colors: [NodeStatus.live.color, backpressureColor(store.avgBackpressurePct)],
                                    startPoint: .leading, endPoint: .trailing))
                                .frame(width: max(6, geo.size.width * min(1, store.avgBackpressurePct / 100)))
                        }
                    }
                    .frame(height: 8)
                    Text("\(store.avgBackpressurePct, specifier: "%.1f")% \(backpressureLabel(store.avgBackpressurePct))")
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundStyle(backpressureColor(store.avgBackpressurePct))
                        .monospacedDigit()
                        .fixedSize()
                }
            }

            // Per-region breakdown
            if store.pods.count > 1 {
                Divider()
                ForEach(store.pods) { pod in
                    if let m = store.metricsByPod[pod.podId] {
                        RegionLoadRow(region: pod.regionHint, metrics: m, color: backpressureColor(m.backpressurePct))
                    }
                }
            }
        }
        .padding(16)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14))
    }
}

private struct RegionLoadRow: View {
    let region: String
    let metrics: PodMetrics
    let color: Color

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(region.uppercased())
                    .font(.system(size: 12, weight: .semibold))
                Spacer()
                Text("\(metrics.backpressurePct, specifier: "%.1f")%")
                    .font(.system(size: 11, weight: .semibold))
                    .foregroundStyle(color)
                    .padding(.horizontal, 7).padding(.vertical, 2)
                    .background(color.opacity(0.15), in: Capsule())
            }
            HStack(spacing: 12) {
                LoadBar(label: "Queue", value: metrics.queueDepth,
                        max: Swift.max(metrics.queueCapacity, 1), color: color)
                LoadBar(label: "In-flight", value: metrics.totalInFlight,
                        max: Swift.max(metrics.totalInFlight + 1, 10), color: .blue)
            }
        }
    }
}

private struct LoadBar: View {
    let label: String
    let value: Int
    let max: Int
    let color: Color

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(label).font(.system(size: 11)).foregroundStyle(.secondary)
                Spacer()
                Text("\(value)/\(max)").font(.system(size: 11)).monospacedDigit().foregroundStyle(.primary)
            }
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color(.tertiarySystemFill))
                    Capsule().fill(color)
                        .frame(width: Swift.max(4, geo.size.width * CGFloat(min(1.0, Double(value) / Double(Swift.max(max, 1))))))
                }
            }
            .frame(height: 6)
        }
    }
}

private struct LoadMiniStat: View {
    let label: String
    let value: String
    var body: some View {
        VStack(alignment: .trailing, spacing: 1) {
            Text(label.uppercased())
                .font(.system(size: 9, weight: .semibold))
                .foregroundStyle(.secondary)
                .kerning(0.4)
            Text(value)
                .font(.system(size: 15, weight: .bold, design: .rounded))
                .monospacedDigit()
        }
    }
}
