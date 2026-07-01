import SwiftUI

struct RegionView: View {
    let pod: PodHealthDigest
    let nodes: [NodeSnapshot]
    @Binding var selectedNode: NodeSnapshot?

    private var sorted: [NodeSnapshot] {
        nodes.sorted { $0.computedStatus.sortOrder < $1.computedStatus.sortOrder }
    }

    var body: some View {
        ScrollView {
            VStack(spacing: 16) {
                // Network graph
                NetworkGraphView(nodes: nodes,
                                 podId: pod.podId,
                                 region: pod.regionHint,
                                 selected: $selectedNode)
                    .frame(height: 380)
                    .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 16))

                // Node list
                VStack(spacing: 1) {
                    ForEach(sorted) { node in
                        NodeRowView(node: node)
                            .contentShape(Rectangle())
                            .onTapGesture { selectedNode = node }
                    }
                }
                .clipShape(RoundedRectangle(cornerRadius: 14))
            }
            .padding()
        }
        .background(Color(.systemGroupedBackground))
        .navigationTitle(pod.podId)
        .navigationBarTitleDisplayMode(.inline)
    }
}

struct NodeRowView: View {
    let node: NodeSnapshot

    var body: some View {
        let status = node.computedStatus
        HStack(spacing: 12) {
            // Status dot
            Circle()
                .fill(status.color)
                .frame(width: 9, height: 9)

            // Name + endpoint
            VStack(alignment: .leading, spacing: 2) {
                Text(node.label)
                    .font(.system(size: 14, weight: .medium))
                Text(node.reachabilityEndpoint)
                    .font(.system(size: 10, design: .monospaced))
                    .foregroundStyle(.secondary)
            }

            Spacer()

            // Stats
            VStack(alignment: .trailing, spacing: 2) {
                Text(node.measuredToksPerSec.formattedTps)
                    .font(.system(size: 13, weight: .semibold, design: .rounded))
                    .foregroundStyle(status.color)
                    .monospacedDigit()
                Text(node.declaredMemoryGb.formattedGb)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }

            Image(systemName: "chevron.right")
                .font(.caption)
                .foregroundStyle(.tertiary)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .background(Color(.secondarySystemGroupedBackground))
    }
}
