import SwiftUI
import MapKit

struct GlobalMapView: View {
    let nodes: [NodeSnapshot]
    @Binding var selected: NodeSnapshot?

    private var geoNodes: [NodeSnapshot] {
        nodes.filter { $0.geoLat != 0 || $0.geoLng != 0 }
    }

    var body: some View {
        Map {
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
            MapZoomStepper()
            MapCompass()
        }
    }
}

struct NodeMapPin: View {
    let node: NodeSnapshot
    let isSelected: Bool

    @State private var pulsing = false

    var body: some View {
        let status = node.computedStatus
        ZStack {
            // Pulse ring — live nodes only
            if status == .live {
                Circle()
                    .fill(status.color.opacity(pulsing ? 0.12 : 0.28))
                    .frame(width: 22, height: 22)
                    .scaleEffect(pulsing ? 1.4 : 1.0)
                    .animation(.easeInOut(duration: 1.8).repeatForever(autoreverses: true), value: pulsing)
                    .onAppear { pulsing = true }
            }

            // Selection ring
            if isSelected {
                Circle()
                    .strokeBorder(status.color, lineWidth: 2.5)
                    .frame(width: 18, height: 18)
            }

            // Dot
            Circle()
                .fill(status.color)
                .frame(width: isSelected ? 10 : 8, height: isSelected ? 10 : 8)
                .shadow(color: status.color.opacity(0.5), radius: 3)
        }
    }
}
