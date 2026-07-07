import XCTest
@testable import OIMMenuBar

final class NodeLogParserTests: XCTestCase {
    func testRegisteredLineExtractsNodeID() {
        let line = "2026/07/07 12:00:00 [agent] registered with coordinator https://us.mlxmesh.net as node fce726c7983c4c412d43f5b60437a0ed"
        guard case .registered(let nodeID) = NodeLogParser.parse(line) else {
            return XCTFail("expected .registered, got \(NodeLogParser.parse(line))")
        }
        XCTAssertEqual(nodeID, "fce726c7983c4c412d43f5b60437a0ed")
    }

    func testServingJobsLineDetected() {
        let line = "2026/07/07 12:00:00 [agent] serving jobs at http://:8765"
        XCTAssertEqual(NodeLogParser.parse(line), .servingJobs)
    }

    func testShuttingDownLineDetected() {
        let line = "2026/07/07 12:00:05 [agent] shutting down"
        XCTAssertEqual(NodeLogParser.parse(line), .shuttingDown)
    }

    func testUnrelatedLineIsOther() {
        let line = "Node ID:         abc123"
        XCTAssertEqual(NodeLogParser.parse(line), .other)
    }

    func testMalformedRegisteredLineDoesNotCrash() {
        // Missing " as node " — must fall back to .other, not force-unwrap/crash.
        let line = "[agent] registered with coordinator https://us.mlxmesh.net"
        XCTAssertEqual(NodeLogParser.parse(line), .other)
    }
}
