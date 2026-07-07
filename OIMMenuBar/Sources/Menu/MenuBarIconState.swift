import SwiftUI

extension AppState.IconState {
    /// SF Symbol per state. Idle/Healthy use the plain template rendering
    /// (adopts the menu bar's normal black/white) so a fully-fine node stays
    /// unobtrusive per macOS HIG for menu bar extras — Degraded/Error use a
    /// filled + colored variant specifically so they're hard to miss.
    var symbolName: String {
        switch self {
        case .idle: return "circle.hexagongrid"
        case .healthy: return "circle.hexagongrid.fill"
        case .degraded: return "circle.hexagongrid.fill"
        case .error: return "exclamationmark.triangle.fill"
        }
    }

    var tint: Color? {
        switch self {
        case .idle, .healthy: return nil // template color — no forced tint
        case .degraded: return .orange
        case .error: return .red
        }
    }

    var summary: String {
        switch self {
        case .idle: return "Not contributing"
        case .healthy: return "Contributing"
        case .degraded: return "Contributing — needs attention"
        case .error: return "Error"
        }
    }
}
