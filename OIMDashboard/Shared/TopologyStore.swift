import Foundation
import Observation

@Observable
@MainActor
final class TopologyStore {
    var pods: [PodHealthDigest] = []
    var nodesByPod: [String: [NodeSnapshot]] = [:]
    var metricsByPod: [String: PodMetrics] = [:]
    var coordinationByPod: [String: [CoordinationParticipant]] = [:]
    var lastUpdated: Date?
    var error: String?
    var isLoading = false

    /// iOS security/coordination participants across all pods (distinct from
    /// inference nodes).
    var allCoordination: [CoordinationParticipant] { pods.flatMap { coordinationByPod[$0.podId] ?? [] } }

    // Convenience aggregates
    var allNodes: [NodeSnapshot] { pods.flatMap { nodesByPod[$0.podId] ?? [] } }
    var liveCount: Int { allNodes.filter { $0.computedStatus == .live }.count }
    var totalTps: Double { allNodes.reduce(0) { $0 + $1.measuredToksPerSec } }
    var totalMemoryGb: Double { allNodes.reduce(0) { $0 + $1.committedMemoryGb } }

    // Queue/backpressure aggregates — mirrors the web dashboard's header pills.
    private var allMetrics: [PodMetrics] { Array(metricsByPod.values) }
    var totalQueued: Int { allMetrics.reduce(0) { $0 + $1.queueDepth } }
    var totalInFlight: Int { allMetrics.reduce(0) { $0 + $1.totalInFlight } }
    var avgBackpressurePct: Double {
        allMetrics.isEmpty ? 0 : allMetrics.reduce(0) { $0 + $1.backpressurePct } / Double(allMetrics.count)
    }

    private var refreshTask: Task<Void, Never>?
    private var pollInterval: TimeInterval = 5

    func start(interval: TimeInterval = 5) {
        pollInterval = interval
        refreshTask?.cancel()
        refreshTask = Task { [weak self] in
            guard let self else { return }
            while !Task.isCancelled {
                await self.refresh()
                try? await Task.sleep(for: .seconds(self.pollInterval))
            }
        }
    }

    func stop() {
        refreshTask?.cancel()
        refreshTask = nil
    }

    func refresh() async {
        guard !isLoading else { return }
        isLoading = true
        defer { isLoading = false }
        do {
            let topology = try await NetworkClient.fetchTopology()
            pods = topology.pods

            // Fetch all regions in parallel
            await withTaskGroup(of: (String, NodesResponse?).self) { group in
                for pod in topology.pods {
                    group.addTask {
                        let response = try? await NetworkClient.fetchNodes(coordinatorURL: pod.coordinatorEndpoint)
                        return (pod.podId, response)
                    }
                }
                for await (podId, response) in group {
                    nodesByPod[podId] = (response?.nodes ?? []).filter { !$0.clusterStandby }
                    metricsByPod[podId] = response?.metrics
                    coordinationByPod[podId] = response?.coordinationNodes ?? []
                }
            }
            lastUpdated = .now
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }
}
