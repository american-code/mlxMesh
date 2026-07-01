import SwiftUI

enum NodeStatus: Equatable, CaseIterable {
    case live, degraded, stale, unreachable
    static var allCases: [NodeStatus] { [.live, .degraded, .stale, .unreachable] }
}

extension NodeStatus {
    var color: Color {
        switch self {
        case .live:        return Color(red: 0.25, green: 0.73, blue: 0.32)
        case .degraded:    return Color(red: 0.82, green: 0.60, blue: 0.13)
        case .stale:       return Color(red: 0.86, green: 0.43, blue: 0.16)
        case .unreachable: return Color(red: 0.97, green: 0.32, blue: 0.29)
        }
    }

    var label: String {
        switch self {
        case .live:        return "Live"
        case .degraded:    return "Reduced perf"
        case .stale:       return "Stale"
        case .unreachable: return "Unreachable"
        }
    }

    var systemImage: String {
        switch self {
        case .live:        return "circle.fill"
        case .degraded:    return "exclamationmark.triangle.fill"
        case .stale:       return "clock.fill"
        case .unreachable: return "xmark.circle.fill"
        }
    }

    var sortOrder: Int {
        switch self { case .live: 0; case .degraded: 1; case .stale: 2; case .unreachable: 3 }
    }
}

private let expectedTpsTable: [Double: Double] = [12: 22, 16: 32, 24: 50, 32: 68, 48: 100, 64: 145]

extension NodeSnapshot {
    var computedStatus: NodeStatus {
        switch status {
        case "unreachable": return .unreachable
        case "stale":       return .stale
        default:
            let expected = expectedTpsTable[declaredMemoryGb.rounded()] ?? declaredMemoryGb * 2.2
            return measuredToksPerSec < expected * 0.5 ? .degraded : .live
        }
    }

    // Extracts "node-us-3" from "http://node-us-3:8767"
    var label: String {
        guard let url = URL(string: reachabilityEndpoint), let host = url.host else {
            return String(nodeId.prefix(8))
        }
        return host
    }

    // Normalized memory radius for graph drawing (7–20 pt)
    var graphRadius: Double { 7 + min(declaredMemoryGb, 64) / 64 * 13 }
}

extension Double {
    var formattedTps: String {
        self >= 1 ? "\(Int(self.rounded())) t/s" : "—"
    }
    var formattedGb: String {
        truncatingRemainder(dividingBy: 1) == 0 ? "\(Int(self)) GB" : String(format: "%.1f GB", self)
    }
    var formattedPct: String { "\(Int((self * 100).rounded()))%" }
}
