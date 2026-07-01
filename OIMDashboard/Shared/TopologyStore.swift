import Foundation
import Observation

@Observable
@MainActor
final class TopologyStore {
    var pods: [PodHealthDigest] = []
    var nodesByPod: [String: [NodeSnapshot]] = [:]
    var lastUpdated: Date?
    var error: String?
    var isLoading = false

    // Convenience aggregates
    var allNodes: [NodeSnapshot] { pods.flatMap { nodesByPod[$0.podId] ?? [] } }
    var liveCount: Int { allNodes.filter { $0.computedStatus == .live }.count }
    var totalTps: Double { allNodes.reduce(0) { $0 + $1.measuredToksPerSec } }
    var totalMemoryGb: Double { allNodes.reduce(0) { $0 + $1.committedMemoryGb } }

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
            await withTaskGroup(of: (String, [NodeSnapshot]).self) { group in
                for pod in topology.pods {
                    group.addTask {
                        let nodes = try? await NetworkClient.fetchNodes(coordinatorURL: pod.coordinatorEndpoint)
                        return (pod.podId, nodes?.nodes ?? [])
                    }
                }
                for await (podId, nodes) in group {
                    nodesByPod[podId] = nodes
                }
            }
            lastUpdated = .now
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }
}
