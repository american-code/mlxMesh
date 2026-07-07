import Foundation

/// Parses one line of the `oim node start` process's stdout. "Confirmed
/// running" requires seeing BOTH signals below, in order — matching the two
/// real log lines emitted by internal/agent/agent.go. Watching for these
/// (rather than trusting the process merely being alive) is what lets the UI
/// distinguish "started" from "started AND actually registered/serving."
enum NodeLogSignal: Equatable {
    case registered(nodeID: String)
    case servingJobs
    case shuttingDown
    case other
}

enum NodeLogParser {
    private static let registeredPrefix = "[agent] registered with coordinator "
    private static let servingPrefix = "[agent] serving jobs at "
    private static let shuttingDownMarker = "[agent] shutting down"

    static func parse(_ line: String) -> NodeLogSignal {
        if line.contains(shuttingDownMarker) {
            return .shuttingDown
        }
        if let range = line.range(of: registeredPrefix) {
            // "...registered with coordinator <url> as node <node_id>"
            let rest = line[range.upperBound...]
            if let asNodeRange = rest.range(of: " as node ") {
                let nodeID = rest[asNodeRange.upperBound...].trimmingCharacters(in: .whitespacesAndNewlines)
                if !nodeID.isEmpty {
                    return .registered(nodeID: nodeID)
                }
            }
        }
        if line.contains(servingPrefix) {
            return .servingJobs
        }
        return .other
    }
}
