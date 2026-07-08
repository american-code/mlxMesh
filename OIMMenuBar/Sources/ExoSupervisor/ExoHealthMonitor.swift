import Foundation
import Observation

/// Minimal seam over URLSession so ExoHealthMonitor is unit-testable without
/// real networking or global URLProtocol registration — URLSession itself
/// already satisfies this signature, so production code needs no behavior
/// change, just an injectable type.
protocol HTTPDataFetching {
    func data(for request: URLRequest) async throws -> (Data, URLResponse)
}
extension URLSession: HTTPDataFetching {}

/// Polls Exo's own health endpoint on a timer. This — not process/app
/// presence — is the only source of truth for "is Exo actually serving":
/// Exo's packaged app has a documented history (upstream issue #960) of
/// launching without its API server coming up, so a supervising app that
/// only checks "is the process running" can be confidently wrong.
@Observable
@MainActor
final class ExoHealthMonitor {
    enum Health: Equatable {
        case checking
        case unreachable
        case healthy
    }

    private(set) var health: Health = .checking

    /// 127.0.0.1, NOT "localhost", is deliberate. On macOS `localhost`
    /// resolves to IPv6 `::1` first, but Exo's API server binds IPv4 only —
    /// so a `localhost` connection tries `::1:52415`, gets an immediate
    /// "connection refused" (the flood of `nw_socket_handle_socket_event …
    /// SO_ERROR [61: Connection refused]` on `::1.52415` in the console), and
    /// can report Exo as unreachable even while it's running fine on IPv4.
    /// Pinning IPv4 skips the doomed IPv6 attempt entirely. This value also
    /// flows to the node as `--exo-url` (AppState.startNode), so the Go
    /// exoadapter hits IPv4 too.
    var exoURL = "http://127.0.0.1:52415"

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
    }

    func checkOnce() async {
        guard let url = URL(string: "\(exoURL)/state") else {
            health = .unreachable
            return
        }
        var request = URLRequest(url: url)
        request.timeoutInterval = 3
        do {
            let (_, response) = try await session.data(for: request)
            if let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) {
                health = .healthy
            } else {
                health = .unreachable
            }
        } catch {
            health = .unreachable
        }
    }
}
