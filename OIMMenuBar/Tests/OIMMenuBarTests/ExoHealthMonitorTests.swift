import XCTest
@testable import OIMMenuBar

private struct StubFetcher: HTTPDataFetching {
    let statusCode: Int?
    let error: Error?

    func data(for request: URLRequest) async throws -> (Data, URLResponse) {
        if let error { throw error }
        let response = HTTPURLResponse(
            url: request.url!, statusCode: statusCode ?? 200,
            httpVersion: nil, headerFields: nil
        )!
        return (Data(), response)
    }
}

private struct AnyError: Error {}

final class ExoHealthMonitorTests: XCTestCase {
    @MainActor
    func testHealthyOn200() async {
        let monitor = ExoHealthMonitor(session: StubFetcher(statusCode: 200, error: nil))
        await monitor.checkOnce()
        XCTAssertEqual(monitor.health, .healthy)
    }

    @MainActor
    func testUnreachableOnNonSuccessStatus() async {
        let monitor = ExoHealthMonitor(session: StubFetcher(statusCode: 503, error: nil))
        await monitor.checkOnce()
        XCTAssertEqual(monitor.health, .unreachable)
    }

    @MainActor
    func testUnreachableOnNetworkError() async {
        let monitor = ExoHealthMonitor(session: StubFetcher(statusCode: nil, error: AnyError()))
        await monitor.checkOnce()
        XCTAssertEqual(monitor.health, .unreachable)
    }

    @MainActor
    func testUnreachableOnMalformedURL() async {
        let monitor = ExoHealthMonitor(session: StubFetcher(statusCode: 200, error: nil))
        monitor.exoURL = "not a url \u{0}"
        await monitor.checkOnce()
        XCTAssertEqual(monitor.health, .unreachable)
    }
}
