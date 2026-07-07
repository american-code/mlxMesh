import SwiftUI

@main
struct OIMMenuBarApp: App {
    @State private var appState = AppState()

    var body: some Scene {
        // .window (not .menu) style so the popover can host real SwiftUI
        // controls — sliders, toggles, a scrolling log view — rather than
        // being limited to plain menu items.
        MenuBarExtra(
            appState.iconState.summary,
            systemImage: appState.iconState.symbolName,
            isInserted: .constant(true)
        ) {
            MenuBarView(appState: appState)
        }
        .menuBarExtraStyle(.window)
    }
}
