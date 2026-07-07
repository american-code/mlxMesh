import Foundation
import Observation

/// Polls the LOCAL running `oim` node's own `/detect` endpoint (the same one
/// the web dashboard's Node Setup tab uses) for cluster device count and
/// aggregate memory — already computed identically to what this node reports
/// to the mesh (see internal/capability.AssembleManifest / DetectClusterNode),
/// so this is a read of the real number, not a re-derivation of it in Swift.
///
/// Deliberately NOT talking to Exo directly: /detect's cluster aggregation
/// already handles the multi-device-Exo-cluster case (a solo governor.SystemInfo()
/// call only sees the one machine `oim node start` runs on and badly
/// under-reports a clustered node's true capacity).
@Observable
@MainActor
final class LocalDetectMonitor {
    struct Snapshot: Equatable {
        var isCluster: Bool
        var clusterDeviceCount: Int
        var totalRAMGB: Double
        var availableRAMGB: Double
        // "auto" (UPnP/NAT-PMP succeeded), "manual" (an explicit
        // --reachability-endpoint is configured), or "unavailable" (neither —
        // this node is very likely unreachable by the coordinator right now,
        // even though it can register and heartbeat fine). See
        // internal/agent.Run's reachability resolution for where this comes from.
        var portMapping: String
        var reachabilityEndpoint: String

        var usedRAMGB: Double { max(0, totalRAMGB - availableRAMGB) }
    }

    private(set) var snapshot: Snapshot?

    /// Matches cmd/oim's own `--listen` default (":8765") — NodeProcessController
    /// never overrides it, so the local job server's /detect endpoint is always here.
    var detectURL = "http://localhost:8765/detect"

    private let session: HTTPDataFetching
    private var pollTask: Task<Void, Never>?

    init(session: HTTPDataFetching = URLSession.shared) {
        self.session = session
    }

    func startPolling(interval: TimeInterval = 5) {
        stopPolling()
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.checkOnce()
                try? await Task.sleep(for: .seconds(interval))
            }
        }
    }

    func stopPolling() {
        pollTask?.cancel()
        pollTask = nil
        snapshot = nil
    }

    func checkOnce() async {
        guard let url = URL(string: detectURL) else { return }
        var request = URLRequest(url: url)
        request.timeoutInterval = 3
        do {
            let (data, response) = try await session.data(for: request)
            guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else { return }
            let decoded = try JSONDecoder().decode(DetectResponse.self, from: data)
            snapshot = Snapshot(
                isCluster: decoded.isCluster ?? false,
                clusterDeviceCount: decoded.clusterDeviceCount ?? 1,
                totalRAMGB: decoded.totalRAMGB ?? 0,
                availableRAMGB: decoded.availableRAMGB ?? 0,
                portMapping: decoded.portMapping ?? "unavailable",
                reachabilityEndpoint: decoded.reachabilityEndpoint ?? ""
            )
        } catch {
            // Leave the last-known snapshot in place rather than flicker to nil
            // on a single missed poll (e.g. node briefly busy under load).
        }
    }

    private struct DetectResponse: Decodable {
        let isCluster: Bool?
        let clusterDeviceCount: Int?
        let totalRAMGB: Double?
        let availableRAMGB: Double?
        let portMapping: String?
        let reachabilityEndpoint: String?

        enum CodingKeys: String, CodingKey {
            case isCluster = "is_cluster"
            case clusterDeviceCount = "cluster_device_count"
            case totalRAMGB = "total_ram_gb"
            case availableRAMGB = "available_ram_gb"
            case portMapping = "port_mapping"
            case reachabilityEndpoint = "reachability_endpoint"
        }
    }
}
