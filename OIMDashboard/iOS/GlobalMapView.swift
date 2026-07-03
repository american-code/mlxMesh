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

// KNN mesh overlay, geo helpers, and NodeMapPin now live in
// Shared/MeshMapKit.swift so the tvOS overview map reuses them verbatim.
