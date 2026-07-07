import Observation
import ServiceManagement

/// Wraps the modern (macOS 13+) login-item API. Deliberately NOT a hand-
/// rolled ~/Library/LaunchAgents plist — SMAppService is what Apple recommends
/// today (SMLoginItemSetEnabled has been in the process of deprecation since
/// macOS 13), and it registers the APP (not the node process directly), so
/// the app remains the one true supervisor of the node at all times — see
/// NodeProcessController's doc comment on why that matters.
@Observable
@MainActor
final class LaunchAtLoginController {
    var isEnabled: Bool {
        SMAppService.mainApp.status == .enabled
    }

    func setEnabled(_ enabled: Bool) throws {
        if enabled {
            try SMAppService.mainApp.register()
        } else {
            try SMAppService.mainApp.unregister()
        }
    }
}
