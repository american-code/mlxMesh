import SwiftUI

struct NetworkGraphView: View {
    let nodes: [NodeSnapshot]
    let podId: String
    let region: String
    @Binding var selected: NodeSnapshot?

    // Placed node for layout + hit testing
    struct PlacedNode: Identifiable {
        let node: NodeSnapshot
        let x: CGFloat
        let y: CGFloat
        let radius: CGFloat
        var id: String { node.id }
    }

    private func placed(in size: CGSize) -> [PlacedNode] {
        let cx = size.width / 2, cy = size.height / 2
        let span = min(size.width, size.height)
        let inner = Array(nodes.prefix(8))
        let outer = Array(nodes.dropFirst(8))

        func ring(_ arr: [NodeSnapshot], r: CGFloat) -> [PlacedNode] {
            arr.enumerated().map { i, node in
                let angle = Double(i) / Double(arr.count) * .pi * 2 - .pi / 2
                return PlacedNode(
                    node: node,
                    x: cx + r * cos(angle),
                    y: cy + r * sin(angle),
                    radius: node.graphRadius
                )
            }
        }

        return ring(inner, r: span * 0.30) + ring(outer, r: span * 0.44)
    }

    var body: some View {
        GeometryReader { geo in
            let items = placed(in: geo.size)
            let cx = geo.size.width / 2
            let cy = geo.size.height / 2

            ZStack {
                // Canvas: grid + edges + coordinator + node circles
                TimelineView(.animation(minimumInterval: 1.8, paused: false)) { _ in
                    Canvas { ctx, size in
                        // Dashed ring guides
                        for r in [size.width * 0.30, size.width * 0.44] {
                            let ringRect = CGRect(x: cx - r, y: cy - r, width: r * 2, height: r * 2)
                            ctx.stroke(
                                Circle().path(in: ringRect),
                                with: .color(.secondary.opacity(0.12)),
                                style: StrokeStyle(lineWidth: 1, dash: [5, 10])
                            )
                        }

                        // Edges
                        for item in items {
                            var path = Path()
                            path.move(to: CGPoint(x: cx, y: cy))
                            path.addLine(to: CGPoint(x: item.x, y: item.y))
                            ctx.stroke(path,
                                with: .color(item.node.computedStatus.color.opacity(0.18)),
                                lineWidth: 1.5)
                        }

                        // Coordinator
                        let cr: CGFloat = 22
                        let coordRect = CGRect(x: cx - cr, y: cy - cr, width: cr * 2, height: cr * 2)
                        ctx.fill(Circle().path(in: coordRect), with: .color(.indigo.opacity(0.85)))
                        ctx.stroke(Circle().path(in: coordRect), with: .color(.indigo), lineWidth: 1.5)

                        // Nodes
                        for item in items {
                            let status = item.node.computedStatus
                            let col = status.color
                            let r = item.radius
                            let rect = CGRect(x: item.x - r, y: item.y - r, width: r * 2, height: r * 2)
                            let isSelected = selected?.id == item.node.id

                            // Pulse ring (live)
                            if status == .live {
                                let pr = r + 5
                                let pRect = CGRect(x: item.x - pr, y: item.y - pr, width: pr * 2, height: pr * 2)
                                ctx.stroke(Circle().path(in: pRect),
                                    with: .color(col.opacity(0.22)),
                                    lineWidth: 1.5)
                            }

                            // Selection ring
                            if isSelected {
                                let sr = r + 8
                                let sRect = CGRect(x: item.x - sr, y: item.y - sr, width: sr * 2, height: sr * 2)
                                ctx.stroke(Circle().path(in: sRect),
                                    with: .color(col.opacity(0.85)),
                                    lineWidth: 2)
                            }

                            // Fill — hollow for unreachable
                            let alpha: Double = status == .unreachable ? 0.2 : 0.88
                            ctx.fill(Circle().path(in: rect), with: .color(col.opacity(alpha)))
                            if status == .unreachable || status == .stale {
                                ctx.stroke(Circle().path(in: rect), with: .color(col), lineWidth: 1.5)
                            }
                        }
                    }
                }

                // Region label on coordinator
                Text(region.uppercased())
                    .font(.system(size: 13, weight: .bold))
                    .foregroundStyle(.white)
                    .position(x: cx, y: cy)
                    .allowsHitTesting(false)

                // Pod ID below coordinator
                Text(podId)
                    .font(.system(size: 9))
                    .foregroundStyle(.secondary)
                    .position(x: cx, y: cy + 33)
                    .allowsHitTesting(false)

                // Node hostname labels
                ForEach(items) { item in
                    Text(item.node.label)
                        .font(.system(size: 8, weight: .medium))
                        .foregroundStyle(.secondary)
                        .position(x: item.x, y: item.y + item.radius + 11)
                        .allowsHitTesting(false)
                }

                // Invisible tap targets (44pt minimum)
                ForEach(items) { item in
                    let tapSize = max(44, item.radius * 2 + 16)
                    Circle()
                        .fill(Color.clear)
                        .frame(width: tapSize, height: tapSize)
                        .contentShape(Circle())
                        .position(x: item.x, y: item.y)
                        .onTapGesture { selected = item.node }
                }
            }
        }
    }
}

