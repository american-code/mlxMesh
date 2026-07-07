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

    /// Matches oim's own exoadapter default (internal/exoadapter) and Exo's
    /// confirmed default port/endpoint.
    var exoURL = "http://localhost:52415"

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
