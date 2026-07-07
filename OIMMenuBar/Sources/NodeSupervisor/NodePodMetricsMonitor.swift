import Foundation
import Observation

/// Polls the coordinator's /nodes for this node's own in-flight job count and
/// the pod-wide queue/backpressure signal (PodMetrics) — the same figures the
/// web dashboard's header pills are driven from. A one-line answer to "is my
/// Mac actually being used right now," not the full topology graph
/// NetworkGraphView/TopologyStore already draw elsewhere.
@Observable
@MainActor
final class NodePodMetricsMonitor {
    private(set) var inFlightJobs: Int?
    private(set) var metrics: PodMetrics?

    private var pollTask: Task<Void, Never>?

    func startPolling(nodeID: String, coordinatorURL: String, interval: TimeInterval = 15) {
        stopPolling()
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.checkOnce(nodeID: nodeID, coordinatorURL: coordinatorURL)
                try? await Task.sleep(for: .seconds(interval))
            }
        }
    }

    func stopPolling() {
        pollTask?.cancel()
        pollTask = nil
        inFlightJobs = nil
        metrics = nil
    }

    private func checkOnce(nodeID: String, coordinatorURL: String) async {
        do {
            let response = try await NetworkClient.fetchNodes(coordinatorURL: coordinatorURL)
            metrics = response.metrics
            inFlightJobs = response.nodes.first(where: { $0.nodeId == nodeID })?.inFlightJobs
        } catch {
            // Leave last-known values in place rather than flicker on a single
            // missed poll — same reasoning as ExoHealthMonitor/LocalDetectMonitor.
        }
    }
}
