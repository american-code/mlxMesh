import SwiftUI
import MapKit

struct GlobalMapView: View {
    let nodes: [NodeSnapshot]
    @Binding var selected: NodeSnapshot?

    private var geoNodes: [NodeSnapshot] {
        nodes.filter { $0.geoLat != 0 || $0.geoLng != 0 }
    }

    private var knnEdges: [KNNEdge] { buildKNNEdges(geoNodes, k: 3) }

    var body: some View {
        Map {
            // KNN proximity edges
            ForEach(knnEdges) { edge in
                MapPolyline(coordinates: [edge.from, edge.to])
                    .stroke(edge.color.opacity(0.35), lineWidth: 1.2)
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
    }
}

// MARK: - KNN

struct KNNEdge: Identifiable {
    let id = UUID()
    let from: CLLocationCoordinate2D
    let to: CLLocationCoordinate2D
    let color: Color
}

private func haversineKm(_ lat1: Double, _ lng1: Double, _ lat2: Double, _ lng2: Double) -> Double {
    let R = 6371.0
    let φ1 = lat1 * .pi / 180, φ2 = lat2 * .pi / 180
    let Δφ = (lat2 - lat1) * .pi / 180
    let Δλ = (lng2 - lng1) * .pi / 180
    let a = sin(Δφ/2)*sin(Δφ/2) + cos(φ1)*cos(φ2)*sin(Δλ/2)*sin(Δλ/2)
    return R * 2 * atan2(sqrt(a), sqrt(1-a))
}

private func worseStatus(_ a: NodeSnapshot, _ b: NodeSnapshot) -> NodeStatus {
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

// MARK: - Pin

struct NodeMapPin: View {
    let node: NodeSnapshot
    let isSelected: Bool
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
