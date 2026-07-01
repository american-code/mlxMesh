import SwiftUI

struct ContentView: View {
    @Environment(TopologyStore.self) private var store
    @Environment(\.horizontalSizeClass) private var hSizeClass
    @State private var selectedNode: NodeSnapshot?
    @State private var sidebarItem: String? = "overview"

    var body: some View {
        Group {
            if hSizeClass == .regular {
                iPadLayout
            } else {
                iPhoneLayout
            }
        }
        .sheet(item: $selectedNode) { node in
            NodeDetailView(node: node)
        }
    }

    // MARK: iPhone — tab bar
    private var iPhoneLayout: some View {
        TabView {
            NavigationStack {
                OverviewView(selectedNode: $selectedNode)
                    .navigationTitle("OIM")
                    .navigationBarTitleDisplayMode(.large)
                    .toolbar { refreshButton }
            }
            .tabItem { Label("Overview", systemImage: "globe") }

            NavigationStack {
                GlobalMapView(nodes: store.allNodes, selected: $selectedNode)
                    .navigationTitle("Map")
                    .ignoresSafeArea(edges: .bottom)
            }
            .tabItem { Label("Map", systemImage: "map.fill") }

            ForEach(store.pods) { pod in
                NavigationStack {
                    RegionView(pod: pod,
                               nodes: store.nodesByPod[pod.podId] ?? [],
                               selectedNode: $selectedNode)
                }
                .tabItem { Label(pod.regionHint.uppercased(), systemImage: "network") }
            }

            NavigationStack {
                SettingsView()
                    .navigationTitle("Settings")
            }
            .tabItem { Label("Settings", systemImage: "gear") }
        }
    }

    // MARK: iPad / visionOS — sidebar
    private var iPadLayout: some View {
        NavigationSplitView(columnVisibility: .constant(.all)) {
            List(selection: $sidebarItem) {
                Section("Network") {
                    Label("Overview", systemImage: "globe").tag("overview")
                    Label("Global Map", systemImage: "map.fill").tag("map")
                }
                Section("Regions") {
                    ForEach(store.pods) { pod in
                        Label(pod.regionHint.uppercased() + " · " + pod.podId,
                              systemImage: "network").tag("region-\(pod.podId)")
                    }
                }
                Section {
                    Label("Settings", systemImage: "gear").tag("settings")
                }
            }
            .navigationTitle("OIM")
            .toolbar { refreshButton }
        } detail: {
            detailView(for: sidebarItem)
        }
    }

    @ViewBuilder
    private func detailView(for item: String?) -> some View {
        switch item {
        case "map":
            GlobalMapView(nodes: store.allNodes, selected: $selectedNode)
                .ignoresSafeArea()
        case "settings":
            SettingsView()
        case let str where str?.hasPrefix("region-") == true:
            let podId = String(str!.dropFirst(7))
            if let pod = store.pods.first(where: { $0.podId == podId }) {
                RegionView(pod: pod,
                           nodes: store.nodesByPod[pod.podId] ?? [],
                           selectedNode: $selectedNode)
            }
        default:
            OverviewView(selectedNode: $selectedNode)
        }
    }

    private var refreshButton: some ToolbarContent {
        ToolbarItem(placement: .primaryAction) {
            Button {
                Task { await store.refresh() }
            } label: {
                Label("Refresh", systemImage: store.isLoading ? "arrow.triangle.2.circlepath" : "arrow.clockwise")
                    .symbolEffect(.rotate, isActive: store.isLoading)
            }
        }
    }
}
