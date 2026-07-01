import SwiftUI

@main
struct OIMTVApp: App {
    @State private var store = TopologyStore()

    var body: some Scene {
        WindowGroup {
            TVContentView()
                .environment(store)
                .task { store.start(interval: 10) }
        }
    }
}
