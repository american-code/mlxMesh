import SwiftUI
import MapKit

// Shared map primitives used by the iOS interactive map (GlobalMapView) and the
// tvOS 10-foot overview map (TVGlobalMapView). Kept in Shared so the KNN mesh
// overlay and node pins render identically on both platforms.

// MARK: - KNN proximity edges (the "mesh" overlay)

struct KNNEdge: Identifiable {
    let id = UUID()
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
                from: CLLocationCoordinate2D(latitude: node.geoLat, longitude: node.geoLng),
                to: CLLocationCoordinate2D(latitude: neighbor.geoLat, longitude: neighbor.geoLng),
                color: worseStatus(node, neighbor).color
            ))
        }
    }
    return edges
}

// MARK: - Node pin

struct NodeMapPin: View {
    let node: NodeSnapshot
    var isSelected: Bool = false
    @State private var pulsing = false

    var body: some View {
        let status = node.computedStatus
        ZStack {
            if status == .live {
                Circle()
                    .fill(status.color.opacity(pulsing ? 0.12 : 0.28))
                    .frame(width: 22, height: 22)
                    .scaleEffect(pulsing ? 1.4 : 1.0)
                    .animation(.easeInOut(duration: 1.8).repeatForever(autoreverses: true), value: pulsing)
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
                .shadow(color: status.color.opacity(0.5), radius: 3)
        }
    }
}
