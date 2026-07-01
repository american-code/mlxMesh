import SwiftUI

@main
struct OIMWatchApp: App {
    @State private var store = TopologyStore()

    var body: some Scene {
        WindowGroup {
            WatchContentView()
                .environment(store)
                .task { store.start(interval: 10) }
        }
    }
}
