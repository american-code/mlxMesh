import SwiftUI

// tvOS refinement pass — see the fix log at the bottom of this file for what
// each change addresses and what is deliberately deferred to on-device work.
struct TVContentView: View {
    @Environment(TopologyStore.self) private var store
    @State private var selection: String = "overview"
    @State private var selectedNode: NodeSnapshot?

    var body: some View {
        // FIX 1: TabView instead of NavigationSplitView. On tvOS the split-view
        // sidebar renders as a drawer that floats OVER the detail content — the
        // "pod switcher covering the graph" problem. A TabView's top tab bar
        // hides when the user scrolls into content and never occludes it.
        TabView(selection: $selection) {
            TVGlobalView()
                .tabItem { Text("Overview") }
                .tag("overview")

            ForEach(store.pods) { pod in
                TVRegionDetailView(
                    pod: pod,
                    nodes: store.nodesByPod[pod.podId] ?? [],
                    selectedNode: $selectedNode
                )
                .tabItem { Text(pod.podId) }
                .tag(pod.podId)
            }

            TryMeshPlaceholderView()
                .tabItem { Text("Try the mesh") }
                .tag("try")
        }
    }
}

// MARK: - Detail: Region

struct TVRegionDetailView: View {
    @Environment(TopologyStore.self) private var store
    let pod: PodHealthDigest
    let nodes: [NodeSnapshot]
    @Binding var selectedNode: NodeSnapshot?

    private var sorted: [NodeSnapshot] {
        nodes.sorted { $0.computedStatus.sortOrder < $1.computedStatus.sortOrder }
    }

    // Median t/s and max GB drive the node-row visual hierarchy (Fix 5): faster
    // -than-median nodes read green, and the largest-memory node's GB stays
    // bright while smaller tiers dim, so the eye finds the big contributors
    // without consciously parsing every number.
    private var tpsMedian: Double {
        let vals = nodes.map(\.measuredToksPerSec).sorted()
        guard !vals.isEmpty else { return 0 }
        return vals[vals.count / 2]
    }
    private var maxGb: Double {
        nodes.map(\.declaredMemoryGb).max() ?? 0
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

                // driftEnabled: false — tvOS is a 10-foot ambient display, not a
                // touch surface; the iOS-side "gently alive" force-directed drift
                // (added in a later, iOS-scoped pass) is constant motion that
                // hasn't been verified on-device here, same reasoning as Fix 6
                // below for not rippling untested visual changes into tvOS.
                NetworkGraphView(nodes: nodes,
                                 podId: pod.podId,
                                 region: pod.regionHint,
                                 selected: $selectedNode,
                                 driftEnabled: false)
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
                    // FIX 4: NODES is a neutral count, not a status — blue, never
                    // amber. Amber/gold is reserved for warning states so a glance
                    // at the screen never misreads a healthy node count as trouble.
                    TVStatPill(label: "Nodes", value: "\(pod.nodeCountApprox)",
                               color: .blue)
                }
                .padding(.horizontal, 40)

                if let metrics = store.metricsByPod[pod.podId] {
                    HStack(spacing: 40) {
                        TVStatPill(label: "Queued", value: "\(metrics.queueDepth)",
                                   color: metrics.queueDepth > 0 ? NodeStatus.degraded.color : .secondary)
                        // FIX 4: in-flight is neutral when low, warns only when high.
                        TVStatPill(label: "In-flight", value: "\(metrics.totalInFlight)",
                                   color: inFlightColor(metrics.totalInFlight))
                        // FIX 4: backpressure green at 0, amber > 10%, red > 50%.
                        TVStatPill(label: "Backpressure", value: "\(Int(metrics.backpressurePct.rounded()))%",
                                   color: backpressureColor(metrics.backpressurePct))
                    }
                    .padding(.horizontal, 40)
                }

                Spacer()
            }
            .frame(maxWidth: .infinity)

            Divider().padding(.vertical, 40)

            // Right: node list
            List(sorted, selection: $selectedNode) { node in
                TVNodeRow(node: node, tpsMedian: tpsMedian, maxGb: maxGb).tag(node)
            }
            .listStyle(.plain)
            .frame(width: 520)
            // FIX 3a: a bottom fade signals "more nodes below" when the list
            // overflows the visible area — more TV-appropriate than a scrollbar.
            // Shown heuristically once the count is likely to overflow; precise
            // scroll-offset detection is deferred (see fix log 3b).
            .overlay(alignment: .bottom) {
                LinearGradient(colors: [.clear, Color(white: 0.06).opacity(0.95)],
                               startPoint: .top, endPoint: .bottom)
                    .frame(height: 70)
                    .allowsHitTesting(false)
                    .opacity(nodes.count > 8 ? 1 : 0)
            }
        }
    }

    private func inFlightColor(_ n: Int) -> Color {
        if n > 20 { return NodeStatus.unreachable.color }
        if n > 5 { return NodeStatus.degraded.color }
        if n > 0 { return .blue }
        return .secondary
    }
    private func backpressureColor(_ pct: Double) -> Color {
        if pct > 50 { return NodeStatus.unreachable.color }
        if pct > 10 { return NodeStatus.degraded.color }
        return NodeStatus.live.color
    }
}

// MARK: - Detail: Global overview

// FIX 48: the Overview IS the global map now — a full-bleed world map with the
// KNN mesh overlay and pulsing node pins, with the aggregate stats floating in a
// glass bar along the bottom. This is the "main page = global map + stats" the
// 10-foot experience wanted, instead of a bare "select a region" splash.
struct TVGlobalView: View {
    @Environment(TopologyStore.self) private var store

    var body: some View {
        ZStack(alignment: .bottom) {
            TVGlobalMapView(nodes: store.allNodes)
                .ignoresSafeArea()
                .overlay(alignment: .top) { titleBar }

            statsBar
                .padding(.horizontal, 60)
                .padding(.bottom, 50)
        }
    }

    // Title + connection state, top-leading, over the map.
    private var titleBar: some View {
        HStack(spacing: 16) {
            Text("mlxMesh").font(.largeTitle.bold())
            if store.pods.isEmpty {
                Label("Connecting…", systemImage: "antenna.radiowaves.left.and.right")
                    .font(.title3).foregroundStyle(.secondary)
            } else if store.isLoading {
                ProgressView().scaleEffect(0.8)
            }
            Spacer()
        }
        .padding(.horizontal, 60)
        .padding(.top, 40)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            LinearGradient(colors: [Color(white: 0.06).opacity(0.85), .clear],
                           startPoint: .top, endPoint: .bottom)
        )
    }

    // Aggregate stat pills in a floating glass bar along the bottom edge.
    private var statsBar: some View {
        VStack(spacing: 24) {
            HStack(spacing: 44) {
                TVStatPill(label: "Live Nodes",  value: "\(store.liveCount)",
                           color: NodeStatus.live.color)
                TVStatPill(label: "Total Tok/s", value: store.totalTps.formattedTps,
                           color: .purple)
                TVStatPill(label: "Committed Mem", value: store.totalMemoryGb.formattedGb,
                           color: .cyan)
                // FIX 4: neutral count → blue, not amber.
                TVStatPill(label: "Regions",     value: "\(store.pods.count)",
                           color: .blue)
                if !store.metricsByPod.isEmpty {
                    TVStatPill(label: "In-flight", value: "\(store.totalInFlight)",
                               color: store.totalInFlight > 0 ? .blue : .secondary)
                }
                // Security/coordination layer (iOS pointer-host devices).
                TVStatPill(label: "🛡 Coordination", value: "\(store.allCoordination.count)",
                           color: store.allCoordination.isEmpty ? .secondary : .purple)
            }
        }
        .padding(28)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 28))
        .overlay(RoundedRectangle(cornerRadius: 28).strokeBorder(.white.opacity(0.08), lineWidth: 1))
    }
}

// MARK: - Try the mesh (placeholder)

// FIX 7: TryMeshView is not built for tvOS yet (Siri Remote text entry +
// ephemeral identity is a v2 interaction). A clean placeholder beats a
// half-built one.
// TODO(v2): implement live query submission from the TV — ephemeral identity
// only (no persistent Keychain writes), Siri Remote / on-screen keyboard input,
// reuse NetworkClient.submitTestQuery, show reply + measured t/s.
struct TryMeshPlaceholderView: View {
    var body: some View {
        VStack(spacing: 20) {
            Image(systemName: "bubble.left.and.text.bubble.right")
                .font(.system(size: 64))
                .foregroundStyle(NodeStatus.live.color)
            Text("Try the mesh")
                .font(.largeTitle.bold())
            Text("Coming soon — submit a query to the mesh from your TV.")
                .font(.title3)
                .foregroundStyle(.secondary)
        }
        .padding()
    }
}

// MARK: - Reusable components

struct TVNodeRow: View {
    let node: NodeSnapshot
    var tpsMedian: Double = 0
    var maxGb: Double = 0

    var body: some View {
        let status = node.computedStatus
        // FIX 5: taller rows (72pt min), clearer hierarchy. t/s reads green when
        // above the pod median; a smaller-tier node's GB dims relative to the
        // biggest contributor so scale is legible at a glance.
        let fastForPod = tpsMedian > 0 && node.measuredToksPerSec > tpsMedian
        let isSmallTier = maxGb > 0 && node.declaredMemoryGb < maxGb * 0.5
        HStack(spacing: 16) {
            Circle().fill(status.color).frame(width: 12, height: 12)
            VStack(alignment: .leading, spacing: 4) {
                Text(node.label).font(.system(size: 26, weight: .semibold))
                Text(status.label)
                    .font(.system(size: 18))
                    .foregroundStyle(status.color)
            }
            Spacer()
            VStack(alignment: .trailing, spacing: 4) {
                Text(node.measuredToksPerSec.formattedTps)
                    .font(.system(size: 26, weight: .semibold, design: .rounded))
                    .foregroundStyle(fastForPod ? NodeStatus.live.color : .primary)
                    .monospacedDigit()
                Text(node.declaredMemoryGb.formattedGb)
                    .font(.system(size: 18))
                    .foregroundStyle(isSmallTier ? .tertiary : .secondary)
                    .monospacedDigit()
            }
        }
        .frame(minHeight: 72)
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

// MARK: - Fix log
//
// Applied this pass (swiftc-typecheck verified; visual sizing chosen for 10ft):
//   Fix 1  Pod switcher no longer overlays content — TabView replaces the
//          NavigationSplitView drawer that floated over the detail.
//   Fix 2  Switcher is now legible top tabs (system-sized) instead of cramped
//          NODES/TOK/S/MEM cards; per-pod detail keeps large stat pills.
//   Fix 3a Bottom fade affordance on the node list when it overflows.
//   Fix 4  Neutral counts (Nodes/Regions) are blue; amber reserved for warnings;
//          in-flight and backpressure use the specified thresholds.
//   Fix 5  Node rows are taller (72pt) with clear hierarchy; fast nodes' t/s go
//          green, small-tier GB values dim.
//   Fix 7  Try-the-mesh placeholder tab with a v2 TODO.
//
// Resolved by a later, iOS-scoped pass (rippled into tvOS as a pure
// improvement — no tvOS-specific work needed):
//   Fix 6  Graph label overlap was fixed directly in the SHARED
//          NetworkGraphView (radial-outward label placement + de-densifying on
//          crowded outer rings). That same pass also added a force-directed
//          "drift" animation to the graph, which IS gated off here
//          (driftEnabled: false, see the call site above) since constant
//          motion is a bigger, unverified-on-device behavioral change than a
//          static label fix.
//
// Deferred — require a real Apple TV to verify, do not ship blind:
//   Fix 3b Focus routing into the node list (Siri Remote down-swipe). List is
//          focusable by default; confirm on device before relying on it.
//   Fix 8  Idle auto-rotate between pods needs reliable Siri-Remote interaction
//          detection (no UIApplication on tvOS) so rotation never fights the
//          user; unsafe to enable without on-device testing.
