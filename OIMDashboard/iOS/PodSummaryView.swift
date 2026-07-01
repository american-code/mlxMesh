import SwiftUI

struct PodSummaryView: View {
    let pod: PodHealthDigest
    let nodes: [NodeSnapshot]

    private var statusCounts: [NodeStatus: Int] {
        nodes.reduce(into: [:]) { acc, n in acc[n.computedStatus, default: 0] += 1 }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            // Header row
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(pod.podId)
                        .font(.headline)
                    Text(pod.regionHint.uppercased() + " · " + pod.coordinatorEndpoint)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .minimumScaleFactor(0.8)
                }
                Spacer()
                HealthBadge(score: pod.aggregateHealthScore)
            }

            // Stat grid
            Grid(alignment: .leading, horizontalSpacing: 12, verticalSpacing: 6) {
                GridRow {
                    MiniStat(label: "Nodes", value: "\(pod.nodeCountApprox)")
                    MiniStat(label: "Memory", value: pod.totalMemoryGb.formattedGb)
                }
                GridRow {
                    MiniStat(label: "Tok/s", value: pod.aggregateToksPerSec.formattedTps)
                    MiniStat(label: "Models", value: "\(pod.servableModelIds.count)")
                }
            }

            // Status breakdown dots
            HStack(spacing: 8) {
                ForEach(NodeStatus.allCases, id: \.sortOrder) { status in
                    if let count = statusCounts[status] {
                        Label("\(count)", systemImage: "circle.fill")
                            .font(.caption2)
                            .foregroundStyle(status.color)
                    }
                }
            }
        }
        .padding(14)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14))
    }
}

struct HealthBadge: View {
    let score: Double
    var color: Color {
        score >= 0.7 ? NodeStatus.live.color : score >= 0.4 ? NodeStatus.degraded.color : NodeStatus.unreachable.color
    }
    var body: some View {
        Text(score.formattedPct)
            .font(.system(size: 14, weight: .bold, design: .rounded))
            .foregroundStyle(color)
            .padding(.horizontal, 9)
            .padding(.vertical, 4)
            .background(color.opacity(0.12), in: RoundedRectangle(cornerRadius: 8))
            .overlay(RoundedRectangle(cornerRadius: 8).strokeBorder(color.opacity(0.3), lineWidth: 1))
    }
}

struct MiniStat: View {
    let label: String
    let value: String
    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(label)
                .font(.system(size: 9, weight: .semibold))
                .foregroundStyle(.secondary)
                .textCase(.uppercase)
                .kerning(0.4)
            Text(value)
                .font(.system(size: 17, weight: .bold, design: .rounded))
                .monospacedDigit()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}
