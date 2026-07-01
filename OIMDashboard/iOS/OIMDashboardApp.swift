import SwiftUI

@main
struct OIMDashboardApp: App {
    @State private var store = TopologyStore()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environment(store)
                .task { store.start() }
        }
        #if os(visionOS)
        .defaultSize(width: 1200, height: 800)
        #endif
    }
}
