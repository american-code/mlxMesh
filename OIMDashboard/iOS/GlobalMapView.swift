import SwiftUI
import MapKit

struct GlobalMapView: View {
    let nodes: [NodeSnapshot]
    @Binding var selected: NodeSnapshot?

    // Explicit camera binding — reused across renders so pan/zoom survive the
    // route-pulse overlay's own ticking without disturbing the Map itself
    // (see below: the Map no longer rebuilds on that tick at all).
    @State private var cameraPosition: MapCameraPosition = .automatic

    private var geoNodes: [NodeSnapshot] {
        nodes.filter { $0.geoLat != 0 || $0.geoLng != 0 }
    }

    // Edges only depend on node topology, not on time — computed once per real
    // data refresh (onAppear/onChange below), not on every animation-timeline
    // tick. Recomputing the O(n²) KNN search on every frame just to feed the
    // (cheap) pulse-position interpolation would be wasted work on a hot path.
    @State private var edges: [KNNEdge] = []

    var body: some View {
        // MapReader exposes MapProxy.convert(_:to:), which projects a geo
        // coordinate to view-space using the Map's OWN current camera — that's
        // what lets the pulse dots below be a plain, cheap SwiftUI overlay
        // (positioned Circles) instead of MapKit Annotations, while still
        // tracking pan/zoom correctly.
        MapReader { proxy in
            ZStack {
                // The Map itself — all edges + node pin Annotations — is built
                // OUTSIDE any TimelineView, so it only rebuilds on a real data
                // refresh (onAppear/onChange below), not on every 10fps pulse
                // tick. MapKit Annotations are comparatively expensive
                // UIView-backed objects; previously this whole tree (dozens of
                // pins for a real pod) rebuilt 10×/sec purely to animate a
                // handful of pulse dots.
                Map(position: $cameraPosition) {
                    // KNN proximity edges
                    ForEach(edges) { edge in
                        MapPolyline(coordinates: [edge.from, edge.to])
                            .stroke(edge.color.opacity(0.35), lineWidth: 2.9)
                    }

                    // Node markers
                    ForEach(geoNodes) { node in
                        Annotation(
                            node.label,
                            coordinate: CLLocationCoordinate2D(latitude: node.geoLat, longitude: node.geoLng),
                            anchor: .center
                        ) {
                            NodeMapPin(node: node, isSelected: selected?.id == node.id)
                                .onTapGesture { selected = node }
                        }
                    }
                }
                .mapStyle(.standard(elevation: .flat, pointsOfInterest: .excludingAll))
                .mapControls {
                    // MapZoomStepper is macOS/visionOS-only; iOS zooms via pinch gesture.
                    MapCompass()
                }

                // Traveling light pulses along "hot" edges — an edge touching a
                // node with real coordinator-observed in-flight work. Sells the
                // "mesh" concept: routes visibly light up as inference flows.
                // This is the ONLY part of the view that re-renders at 10fps —
                // a handful of plain SwiftUI Circles positioned via the Map's
                // own projection, not MapKit Annotations, so the per-frame
                // cost is proportional to the (usually small) hot-edge count,
                // not the full node/edge set.
                TimelineView(.animation(minimumInterval: 0.1, paused: false)) { timeline in
                    let now = timeline.date.timeIntervalSinceReferenceDate
                    // Computed LIVE from the current nodes each render (not
                    // baked into the cached `edges`), because NodeSnapshot
                    // equality ignores inFlightJobs — so `.onChange(of: nodes)`
                    // below only refreshes edge GEOMETRY on membership changes,
                    // while hotness has to be recomputed here to actually track
                    // load as it moves. Cheap O(n) set build.
                    let hotNodeIDs = Set(geoNodes.filter { $0.inFlightJobs > 0 }.map(\.nodeId))
                    ForEach(routePulses(for: edges, hotNodeIDs: hotNodeIDs, at: now)) { pulse in
                        if let point = proxy.convert(pulse.coordinate, to: .local) {
                            RoutePulseDot(color: pulse.color)
                                .position(point)
                                .allowsHitTesting(false)
                        }
                    }
                }
            }
        }
        .onAppear { edges = buildKNNEdges(geoNodes, k: 3) }
        .onChange(of: nodes) { _, _ in edges = buildKNNEdges(geoNodes, k: 3) }
    }
}

// KNN mesh overlay, geo helpers, and NodeMapPin now live in
// Shared/MeshMapKit.swift so the tvOS overview map reuses them verbatim.
