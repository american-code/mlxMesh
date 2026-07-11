import Foundation
import XCTest
@testable import MeshKit

final class MeshErrorTests: XCTestCase {
    private func response(status: Int, headers: [String: String] = [:]) -> HTTPURLResponse {
        HTTPURLResponse(
            url: URL(string: "http://example.test/")!, statusCode: status,
            httpVersion: "HTTP/1.1", headerFields: headers
        )!
    }

    private func jsonBody(_ dict: [String: Any]) -> Data {
        try! JSONSerialization.data(withJSONObject: dict)
    }

    func test2xxIsANoop() {
        XCTAssertNil(MeshErrorMapper.error(for: response(status: 200), body: jsonBody(["ok": true])))
    }

    func test402RaisesInsufficientCreditsWithFields() {
        let body = jsonBody(["error": "insufficient_credits", "balance": 0.5, "required": 2.0])
        guard case .insufficientCredits(let message, let balance, let required)? = MeshErrorMapper.error(for: response(status: 402), body: body) else {
            return XCTFail("expected .insufficientCredits")
        }
        XCTAssertEqual(message, "insufficient_credits")
        XCTAssertEqual(balance, 0.5)
        XCTAssertEqual(required, 2.0)
    }

    func test409RaisesReservationExpired() {
        let body = jsonBody(["error": "reservation_expired_or_unknown: re-reserve and re-encrypt"])
        guard case .reservationExpired? = MeshErrorMapper.error(for: response(status: 409), body: body) else {
            return XCTFail("expected .reservationExpired")
        }
    }

    func test429RaisesRateLimitedWithRetryAfter() {
        let body = jsonBody(["error": "rate limit exceeded, retry shortly"])
        guard case .rateLimited(_, let retryAfter)? = MeshErrorMapper.error(for: response(status: 429, headers: ["Retry-After": "1"]), body: body) else {
            return XCTFail("expected .rateLimited")
        }
        XCTAssertEqual(retryAfter, 1.0)
    }

    func test429WithoutRetryAfterHeaderStillRaises() {
        let body = jsonBody(["error": "per-account quota exceeded, retry shortly"])
        guard case .rateLimited(_, let retryAfter)? = MeshErrorMapper.error(for: response(status: 429), body: body) else {
            return XCTFail("expected .rateLimited")
        }
        XCTAssertNil(retryAfter)
    }

    func test503RaisesNoCapacity() {
        let body = jsonBody(["error": "no eligible nodes available for job job-123 (tried 3)"])
        guard case .noCapacity? = MeshErrorMapper.error(for: response(status: 503), body: body) else {
            return XCTFail("expected .noCapacity")
        }
    }

    func testGeneric4xxRaisesAPIError() {
        let body = jsonBody(["error": "parse request: unexpected EOF"])
        guard case .api(let message, let statusCode)? = MeshErrorMapper.error(for: response(status: 400), body: body) else {
            return XCTFail("expected .api")
        }
        XCTAssertEqual(statusCode, 400)
        XCTAssertTrue(message.contains("parse request"))
    }

    func testNonJSONBodyStillRaisesWithRawText() {
        let body = Data("upstream proxy error".utf8)
        guard case .api(let message, _)? = MeshErrorMapper.error(for: response(status: 500), body: body) else {
            return XCTFail("expected .api")
        }
        XCTAssertTrue(message.contains("upstream proxy error"))
    }
}
