import SwiftUI
import MapKit

// Shared map primitives used by the iOS interactive map (GlobalMapView) and the
// tvOS 10-foot overview map (TVGlobalMapView). Kept in Shared so the KNN mesh
// overlay and node pins render identically on both platforms.

// MARK: - KNN proximity edges (the "mesh" overlay)

struct KNNEdge: Identifiable {
    // Stable node-pair key (not a fresh UUID) — buildKNNEdges recomputes edges
    // on every call, so a random id here would reshuffle SwiftUI's ForEach
    // identity (and the per-edge animation seed derived from it) every time
    // instead of once per real topology change.
    let id: String
    // Endpoint node IDs — carried so "is this edge hot?" can be evaluated LIVE
    // against the current in-flight counts each render, rather than baked in at
    // edge-construction time. This matters because NodeSnapshot's Equatable
    // compares only node_id, so the edge cache (rebuilt via .onChange(of:
    // nodes)) never refreshes when only inFlightJobs changes — a hotness flag
    // frozen into the edge here would never light up as real load moved.
    let aID: String
    let bID: String
    let from: CLLocationCoordinate2D
    let to: CLLocationCoordinate2D
    let color: Color
}

func haversineKm(_ lat1: Double, _ lng1: Double, _ lat2: Double, _ lng2: Double) -> Double {
    let R = 6371.0
    let φ1 = lat1 * .pi / 180, φ2 = lat2 * .pi / 180
    let Δφ = (lat2 - lat1) * .pi / 180
    let Δλ = (lng2 - lng1) * .pi / 180
    let a = sin(Δφ/2)*sin(Δφ/2) + cos(φ1)*cos(φ2)*sin(Δλ/2)*sin(Δλ/2)
    return R * 2 * atan2(sqrt(a), sqrt(1-a))
}

func worseStatus(_ a: NodeSnapshot, _ b: NodeSnapshot) -> NodeStatus {
    a.computedStatus.sortOrder >= b.computedStatus.sortOrder ? a.computedStatus : b.computedStatus
}

// MARK: - Deterministic pseudo-random values from a string key (FNV-1a)

// Returns `count` pseudo-random values in [0, 1), derived from `key` via
// FNV-1a — NOT real randomness. Single shared implementation for every
// "stable per-key wander/animation seed" need in this module (currently:
// NetworkGraphView's node-wander seed, 3 values; this file's route-pulse edge
// seed, 2 values) so the hash itself has only one place to get right. Two
// properties matter at every call site: stable across frames AND app
// relaunches (a given key always produces the same sequence, so a node/edge
// always animates its own distinctive way instead of jittering
// unpredictably), and cheap (no crypto, no Hasher per-process-seed subtlety
// to reason about).
func fnv1aUnitValues(_ key: String, count: Int) -> [Double] {
    var hash: UInt32 = 2166136261
    for byte in key.utf8 {
        hash = (hash ^ UInt32(byte)) &* 16777619
    }
    var h = UInt64(hash)
    var values: [Double] = []
    values.reserveCapacity(count)
    for _ in 0..<count {
        values.append(Double(h % 1000) / 1000)
        h /= 1000
    }
    return values
}

func buildKNNEdges(_ nodes: [NodeSnapshot], k: Int = 3) -> [KNNEdge] {
    var seen = Set<String>()
    var edges: [KNNEdge] = []

    for node in nodes {
        let sorted = nodes
            .filter { $0.nodeId != node.nodeId }
            .sorted { a, b in
                haversineKm(node.geoLat, node.geoLng, a.geoLat, a.geoLng) <
                haversineKm(node.geoLat, node.geoLng, b.geoLat, b.geoLng)
            }
            .prefix(k)

        for neighbor in sorted {
            let key = [node.nodeId, neighbor.nodeId].sorted().joined(separator: "|")
            guard !seen.contains(key) else { continue }
            seen.insert(key)
            edges.append(KNNEdge(
                id: key,
                aID: node.nodeId,
                bID: neighbor.nodeId,
                from: CLLocationCoordinate2D(latitude: node.geoLat, longitude: node.geoLng),
                to: CLLocationCoordinate2D(latitude: neighbor.geoLat, longitude: neighbor.geoLng),
                color: worseStatus(node, neighbor).color
            ))
        }
    }
    return edges
}

// MARK: - Route pulses (traveling light along a "hot" edge)

struct RoutePulse: Identifiable {
    let id: String
    let coordinate: CLLocationCoordinate2D
    let color: Color
}

// Computes one traveling pulse per hot edge, at its position along that edge
// for the given timeline time. `hotNodeIDs` is passed in fresh each render
// (computed live from the current node in-flight counts) rather than read off a
// cached flag, so pulses track load as it actually moves — see KNNEdge.aID.
// Linear lat/lng interpolation, not geodesic — fine at the short KNN-neighbor
// distances these edges span, and the same simplification MapPolyline's
// straight segments already make.
func routePulses(for edges: [KNNEdge], hotNodeIDs: Set<String>, at time: TimeInterval) -> [RoutePulse] {
    edges.filter { hotNodeIDs.contains($0.aID) || hotNodeIDs.contains($0.bID) }.map { edge in
        let seed = fnv1aUnitValues(edge.id, count: 2)
        let speedSeed = seed[0], phaseSeed = seed[1]
        let duration = 2.6 + speedSeed * 1.8   // seconds to cross the edge
        let progress = (time / duration + phaseSeed).truncatingRemainder(dividingBy: 1)
        let lat = edge.from.latitude + (edge.to.latitude - edge.from.latitude) * progress
        let lng = edge.from.longitude + (edge.to.longitude - edge.from.longitude) * progress
        return RoutePulse(id: edge.id, coordinate: CLLocationCoordinate2D(latitude: lat, longitude: lng), color: edge.color)
    }
}

struct RoutePulseDot: View {
    let color: Color
    var body: some View {
        Circle()
            .fill(.white)
            .frame(width: 3, height: 3)
            .shadow(color: color, radius: 5)
            .shadow(color: color.opacity(0.8), radius: 2)
    }
}

// MARK: - Node pin

struct NodeMapPin: View {
    let node: NodeSnapshot
    var isSelected: Bool = false
    @State private var pulsing = false

    // Live load drives the glow: an idle node sits dim, a busy one glows
    // brighter and pulses faster + wider. inFlightJobs is the coordinator's own
    // in-flight count for this node (Shared/Models.swift) — real load, not a
    // self-report. Values are clamped so a very hot node can't balloon the pin.
    private var load: Int { max(0, node.inFlightJobs) }
    private var isActive: Bool { load > 0 }
    private var period: Double { isActive ? max(0.7, 1.8 - Double(load) * 0.25) : 2.2 }
    private var haloScale: CGFloat { isActive ? 1.5 + min(CGFloat(load) * 0.12, 0.6) : 1.25 }
    private var coreGlow: CGFloat { isActive ? 6 + min(CGFloat(load) * 1.5, 8) : 3 }

    var body: some View {
        let status = node.computedStatus
        ZStack {
            if status == .live {
                Circle()
                    .fill(status.color.opacity(pulsing ? (isActive ? 0.05 : 0.10)
                                                       : (isActive ? 0.34 : 0.24)))
                    .frame(width: 22, height: 22)
                    .scaleEffect(pulsing ? haloScale : 1.0)
                    .animation(.easeInOut(duration: period).repeatForever(autoreverses: true), value: pulsing)
                    .onAppear { pulsing = true }
            }
            if isSelected {
                Circle()
                    .strokeBorder(status.color, lineWidth: 2.5)
                    .frame(width: 18, height: 18)
            }
            Circle()
                .fill(status.color)
                .frame(width: isSelected ? 10 : 8, height: isSelected ? 10 : 8)
                .shadow(color: status.color.opacity(isActive ? 0.85 : 0.5), radius: coreGlow)
        }
    }
}
