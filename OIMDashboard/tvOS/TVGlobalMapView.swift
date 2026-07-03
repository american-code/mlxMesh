import SwiftUI
import MapKit

/// tvOS overview map: the whole mesh on a world map with the KNN proximity
/// overlay and pulsing node pins. Deliberately NON-interactive
/// (interactionModes: []) — a TV is a 10-foot display, not a touch surface, so
/// there's no pan/zoom/focus to fight. Node detail lives on the per-region tabs.
struct TVGlobalMapView: View {
    let nodes: [NodeSnapshot]

    private var geoNodes: [NodeSnapshot] {
        nodes.filter { $0.geoLat != 0 || $0.geoLng != 0 }
    }
    private var edges: [KNNEdge] { buildKNNEdges(geoNodes, k: 3) }

    var body: some View {
        Map(initialPosition: .automatic, interactionModes: []) {
            ForEach(edges) { edge in
                MapPolyline(coordinates: [edge.from, edge.to])
                    .stroke(edge.color.opacity(0.35), lineWidth: 1.4)
            }
            ForEach(geoNodes) { node in
                Annotation(
                    node.label,
                    coordinate: CLLocationCoordinate2D(latitude: node.geoLat, longitude: node.geoLng),
                    anchor: .center
                ) {
                    NodeMapPin(node: node)
                }
            }
        }
        .mapStyle(.standard(elevation: .flat, pointsOfInterest: .excludingAll))
    }
}
