import SwiftUI

struct NetworkGraphView: View {
    let nodes: [NodeSnapshot]
    let podId: String
    let region: String
    @Binding var selected: NodeSnapshot?

    // Force-directed "gently alive" drift — nodes wander a small, bounded
    // distance from their ring "home" position and gently repel one another so
    // they never overlap, instead of sitting frozen in a diagram. Defaults on
    // for the interactive iOS app. tvOS explicitly passes false: it's a 10-foot
    // ambient display, not a touch surface, and a prior tvOS-scoped pass
    // (TVContentView.swift "Fix 6") deliberately avoided rippling visual changes
    // from this shared file into tvOS without on-device verification — constant
    // motion is a bigger behavioral change than a label-overlap fix, so it stays
    // gated rather than silently inherited.
    var driftEnabled: Bool = true

    // Placed node for layout + hit testing — the deterministic ring "home"
    // position drift wanders around and springs back toward.
    struct PlacedNode: Identifiable {
        let node: NodeSnapshot
        let x: CGFloat
        let y: CGFloat
        let radius: CGFloat
        let isInner: Bool   // inner ring — always labeled; outer labels de-densify
        var id: String { node.id }
    }

    // A PlacedNode's position after this frame's drift + collision-avoidance
    // pass. Everything that renders or hit-tests a node uses this, not
    // PlacedNode directly, so labels/taps/edges stay in sync with the moving
    // circle.
    struct AnimatedNode: Identifiable {
        let base: PlacedNode
        let x: CGFloat
        let y: CGFloat
        var id: String { base.id }
        var node: NodeSnapshot { base.node }
        var radius: CGFloat { base.radius }
        var isInner: Bool { base.isInner }
    }

    private func placed(in size: CGSize) -> [PlacedNode] {
        let cx = size.width / 2, cy = size.height / 2
        let span = min(size.width, size.height)
        let sorted = nodes.sorted { $0.nodeId < $1.nodeId }
        let inner = Array(sorted.prefix(8))
        let outer = Array(sorted.dropFirst(8))

        // Types are annotated explicitly throughout: the original one-liner mixed
        // CGFloat (cx/cy/r) with Double (cos/sin/.pi) in a single expression, which
        // forced Swift into an overload search so large it hit "unable to type-check
        // this expression in reasonable time" — the root cause of multi-minute (or
        // failed) builds. Splitting into typed steps makes inference trivial.
        func ring(_ arr: [NodeSnapshot], r: CGFloat, isInner: Bool) -> [PlacedNode] {
            let count = Double(max(arr.count, 1))
            return arr.enumerated().map { (offset, node) -> PlacedNode in
                let angle: Double = Double(offset) / count * .pi * 2 - .pi / 2
                let x: CGFloat = cx + r * CGFloat(cos(angle))
                let y: CGFloat = cy + r * CGFloat(sin(angle))
                return PlacedNode(node: node, x: x, y: y, radius: node.graphRadius, isInner: isInner)
            }
        }

        return ring(inner, r: span * 0.30, isInner: true) + ring(outer, r: span * 0.44, isInner: false)
    }

    // Deterministic pseudo-random triple in [0, 1) derived from a node id —
    // see MeshMapKit.swift's fnv1aUnitValues (the single shared FNV-1a
    // implementation this and the route-pulse map's edge seed both use).
    private func stableSeed(_ key: String) -> (Double, Double, Double) {
        let v = fnv1aUnitValues(key, count: 3)
        return (v[0], v[1], v[2])
    }

    // Applies wander + collision-avoidance on top of the ring "home" positions.
    // Wander is a small sum-of-sines offset per node (bounded amplitude acts as
    // an implicit spring back toward home — no persisted velocity state to tune
    // or have blow up). Collision avoidance is 2 relaxation passes pushing any
    // pair that drifted closer than their combined radius apart — the ring
    // layout starts overlap-free and wander amplitude is small relative to ring
    // spacing, so 2 passes reliably clears the rare overlap without the cost or
    // instability of a full iterative force simulation.
    private func animate(_ items: [PlacedNode], at now: TimeInterval) -> [AnimatedNode] {
        guard driftEnabled else {
            return items.map { AnimatedNode(base: $0, x: $0.x, y: $0.y) }
        }

        var animated: [AnimatedNode] = items.map { item in
            let seed = stableSeed(item.node.nodeId)
            let freq1 = 0.35 + seed.0 * 0.25
            let freq2 = 0.6 + seed.1 * 0.3
            let amp: CGFloat = 5 + CGFloat(seed.2) * 3
            let dx = CGFloat(sin(now * freq1 + seed.0 * .pi * 2)) * amp
                   + CGFloat(sin(now * freq2 + seed.1 * .pi * 2)) * amp * 0.35
            let dy = CGFloat(cos(now * freq1 * 0.85 + seed.1 * .pi * 2)) * amp
                   + CGFloat(cos(now * freq2 * 1.15 + seed.2 * .pi * 2)) * amp * 0.35
            return AnimatedNode(base: item, x: item.x + dx, y: item.y + dy)
        }

        for _ in 0..<2 {
            for i in 0..<animated.count {
                for j in (i + 1)..<animated.count {
                    let a = animated[i], b = animated[j]
                    let ddx = b.x - a.x, ddy = b.y - a.y
                    let minDist = a.radius + b.radius + 10
                    // Cheap squared-distance reject first: the ring layout keeps
                    // the vast majority of pairs far apart (different rings, or
                    // opposite sides of one ring), so sqrt/hypot is only paid on
                    // the rare pair that actually overlaps.
                    let sq = ddx * ddx + ddy * ddy
                    if sq < minDist * minDist {
                        let dist = max(sqrt(sq), 0.001)
                        let push = (minDist - dist) / 2
                        let ux = ddx / dist, uy = ddy / dist
                        animated[i] = AnimatedNode(base: a.base, x: a.x - ux * push, y: a.y - uy * push)
                        animated[j] = AnimatedNode(base: b.base, x: b.x + ux * push, y: b.y + uy * push)
                    }
                }
            }
        }
        return animated
    }

    var body: some View {
        GeometryReader { geo in
            let items = placed(in: geo.size)
            let cx = geo.size.width / 2
            let cy = geo.size.height / 2

            // Whole graph (canvas + labels + tap targets) lives inside one
            // TimelineView so drift, collision avoidance, and the live pulse
            // rings all read the same `now` and stay in sync — labels/taps that
            // updated on a different clock than the circles would visibly lag
            // behind them. 20fps (not the display's full refresh rate) is plenty
            // smooth for gentle wander and keeps the per-frame cost — rebuilding
            // labels/tap targets for every node — bounded.
            // 20fps when the graph is "alive" (iOS); a slow ~0.5Hz tick when
            // drift is disabled (tvOS) so the shared view doesn't quietly
            // redraw a 10-foot display 20×/sec and, together with the static
            // pulse ring below, keeps tvOS as calm as it was before this pass
            // added motion — the whole point of driftEnabled:false there.
            TimelineView(.animation(minimumInterval: driftEnabled ? 0.05 : 2.0, paused: false)) { timeline in
                let now = timeline.date.timeIntervalSinceReferenceDate
                let animated = animate(items, at: now)

                ZStack {
                    // Canvas: grid + edges + coordinator + node circles
                    Canvas { ctx, size in
                        // Dashed ring guides — mark the fixed "home" rings, not
                        // affected by drift.
                        for r in [size.width * 0.30, size.width * 0.44] {
                            let ringRect = CGRect(x: cx - r, y: cy - r, width: r * 2, height: r * 2)
                            ctx.stroke(
                                Circle().path(in: ringRect),
                                with: .color(.secondary.opacity(0.12)),
                                style: StrokeStyle(lineWidth: 1, dash: [5, 10])
                            )
                        }

                        // Edges — track the node's drifted position so the
                        // hub-spoke line breathes gently with it.
                        for item in animated {
                            var path = Path()
                            path.move(to: CGPoint(x: cx, y: cy))
                            path.addLine(to: CGPoint(x: item.x, y: item.y))
                            ctx.stroke(path,
                                with: .color(item.node.computedStatus.color.opacity(0.18)),
                                lineWidth: 1.5)
                        }

                        // Coordinator — fixed at center, the hub every node
                        // drifts around, never itself.
                        let cr: CGFloat = 22
                        let coordRect = CGRect(x: cx - cr, y: cy - cr, width: cr * 2, height: cr * 2)
                        ctx.fill(Circle().path(in: coordRect), with: .color(.indigo.opacity(0.85)))
                        ctx.stroke(Circle().path(in: coordRect), with: .color(.indigo), lineWidth: 1.5)

                        // Nodes
                        for item in animated {
                            let status = item.node.computedStatus
                            let col = status.color
                            let r = item.radius
                            let rect = CGRect(x: item.x - r, y: item.y - r, width: r * 2, height: r * 2)
                            let isSelected = selected?.id == item.node.id

                            // Pulse ring (live). When animation is on (iOS) it
                            // expands + fades on a loop and a busier node pulses
                            // faster/wider so the graph reads "alive" and
                            // load-aware. When off (tvOS, driftEnabled:false) it
                            // falls back to the prior STATIC ring — no `now`
                            // dependence — so the 10-foot display shows no motion.
                            if status == .live {
                                if driftEnabled {
                                    let load = max(0, item.node.inFlightJobs)
                                    let speed = 2.0 + Double(load) * 0.7
                                    let phase = (sin(now * speed) + 1) / 2   // 0…1
                                    let spread: CGFloat = load > 0 ? 12 : 6
                                    let pr = r + 3 + CGFloat(phase) * spread
                                    let pRect = CGRect(x: item.x - pr, y: item.y - pr, width: pr * 2, height: pr * 2)
                                    ctx.stroke(Circle().path(in: pRect),
                                        with: .color(col.opacity(0.30 * (1 - phase) + 0.06)),
                                        lineWidth: 1.5)
                                } else {
                                    let pr = r + 5
                                    let pRect = CGRect(x: item.x - pr, y: item.y - pr, width: pr * 2, height: pr * 2)
                                    ctx.stroke(Circle().path(in: pRect), with: .color(col.opacity(0.22)), lineWidth: 1.5)
                                }
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

                        // Node hostname labels — drawn directly into the Canvas
                        // (not separate SwiftUI Text views) so they redraw as
                        // part of the same single per-frame Canvas pass instead
                        // of N individually-diffed views rebuilt at up to 20fps.
                        // Placed radially OUTWARD from center so adjacent
                        // markers' labels fan apart instead of stacking straight
                        // down (the mlxmesh-node-28/22/19 overlap). On a dense
                        // outer ring the radial fan still isn't enough, so there
                        // only the inner ring, the selected node, and small
                        // graphs (≤20 nodes) keep a label.
                        for item in animated {
                            let isSel = selected?.id == item.node.id
                            guard item.isInner || animated.count <= 20 || isSel else { continue }
                            let dx = item.x - cx
                            let dy = item.y - cy
                            let len = max(hypot(dx, dy), 1)
                            let off = item.radius + 12
                            let labelPos = CGPoint(x: item.x + dx / len * off, y: item.y + dy / len * off)
                            let label = Text(item.node.label)
                                .font(.system(size: 8, weight: isSel ? .bold : .medium))
                                .foregroundColor(isSel ? Color.primary : .secondary)
                            ctx.draw(label, at: labelPos, anchor: .center)
                        }
                    }
                    #if os(tvOS)
                    // tvOS: the Siri Remote has no location-based tap —
                    // SpatialTapGesture is unavailable on this platform — and
                    // this file's own policy (see driftEnabled's doc comment)
                    // is to never ship an unverified tvOS interaction change.
                    // Preserve the exact prior mechanism: one invisible,
                    // focus-engine-navigable Circle per node.
                    .overlay {
                        ForEach(animated) { item in
                            let tapSize = max(44, item.radius * 2 + 16)
                            Circle()
                                .fill(Color.clear)
                                .frame(width: tapSize, height: tapSize)
                                .contentShape(Circle())
                                .position(x: item.x, y: item.y)
                                .onTapGesture { selected = item.node }
                        }
                    }
                    #else
                    // iOS: single tap-hit test over the whole canvas instead
                    // of one invisible Circle view per node (also rebuilt
                    // every frame) — finds the nearest node's CURRENT
                    // (this-frame) drifted position to the tap and selects it
                    // if within a 44pt-minimum touch radius, matching the
                    // prior per-node tap-target sizing without the per-frame
                    // view cost.
                    .contentShape(Rectangle())
                    .gesture(
                        SpatialTapGesture()
                            .onEnded { value in
                                guard let nearest = animated.min(by: {
                                    hypot($0.x - value.location.x, $0.y - value.location.y) <
                                    hypot($1.x - value.location.x, $1.y - value.location.y)
                                }) else { return }
                                let hitRadius = max(22, nearest.radius + 8) // 44pt minimum touch target
                                if hypot(nearest.x - value.location.x, nearest.y - value.location.y) <= hitRadius {
                                    selected = nearest.node
                                }
                            }
                    )
                    #endif

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

                    // (Node hostname labels are now drawn directly inside the
                    // Canvas above, not as separate SwiftUI views — see the
                    // comment there.)

                    // Status legend — green/gold/etc. was unlabeled on this screen.
                    VStack {
                        Spacer()
                        HStack(spacing: 12) {
                            ForEach([NodeStatus.live, .degraded, .stale, .unreachable], id: \.sortOrder) { s in
                                HStack(spacing: 4) {
                                    Circle().fill(s.color).frame(width: 7, height: 7)
                                    Text(s.label).font(.system(size: 9)).foregroundStyle(.secondary)
                                }
                            }
                        }
                        .padding(.horizontal, 10)
                        .padding(.vertical, 5)
                        .background(.ultraThinMaterial, in: Capsule())
                        .padding(.bottom, 8)
                    }
                    .allowsHitTesting(false)
                    // (Tap hit-testing is now the single SpatialTapGesture
                    // attached to the Canvas above — see the comment there.)
                }
            }
        }
    }
}
