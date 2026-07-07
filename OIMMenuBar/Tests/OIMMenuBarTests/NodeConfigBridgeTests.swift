import XCTest
@testable import OIMMenuBar

final class NodeConfigBridgeTests: XCTestCase {
    func testDecodesRealConfigShape() throws {
        // Mirrors internal/nodeconfig.Config's actual on-disk JSON exactly
        // (field names/nesting verified directly against config.go/schedule.go).
        let json = """
        {
          "exo_url": "http://localhost:52415",
          "memory_cap_pct": 0.5,
          "geographic_hint": "us",
          "reachability_endpoint": "",
          "pod_endpoint": "",
          "allowed_models": [],
          "sensitivity_cap": "moderate",
          "schedule": {
            "mode": "window",
            "daily_start": "22:00",
            "daily_end": "07:00",
            "days": ["fri", "sat"]
          }
        }
        """
        let config = try JSONDecoder().decode(NodeConfigBridge.self, from: Data(json.utf8))
        XCTAssertEqual(config.exoURL, "http://localhost:52415")
        XCTAssertEqual(config.memoryCapPct, 0.5)
        XCTAssertEqual(config.geographicHint, "us")
        XCTAssertEqual(config.schedule?.mode, "window")
        XCTAssertEqual(config.schedule?.dailyStart, "22:00")
        XCTAssertEqual(config.schedule?.dailyEnd, "07:00")
        XCTAssertEqual(config.schedule?.days, ["fri", "sat"])
    }

    func testMissingScheduleDecodesToNilNotCrash() throws {
        let json = """
        { "exo_url": "http://localhost:52415", "memory_cap_pct": 0.5, "geographic_hint": "us" }
        """
        let config = try JSONDecoder().decode(NodeConfigBridge.self, from: Data(json.utf8))
        XCTAssertNil(config.schedule)
    }

    func testLoadExistingNeverCrashesRegardlessOfFilePresence() {
        // Whether ~/.config/oim/config.json happens to exist on the test
        // runner is environment-dependent and not something this test
        // controls — the contract being verified is "never throws/crashes,"
        // not a specific return value.
        _ = NodeConfigBridge.loadExisting()
    }
}
