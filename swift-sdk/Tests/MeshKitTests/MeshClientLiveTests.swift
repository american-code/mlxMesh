#if canImport(Darwin)
import Darwin
#elseif canImport(Glibc)
import Glibc
#endif
import Foundation
import XCTest
@testable import MeshKit

/// End-to-end tests against the REAL oim-coordinator + stub-exo + oim node
/// binaries — mirrors python-sdk/tests/test_integration.py and
/// tests/integration_test.go: build the Go binaries once, spin up a fresh
/// mesh per test, talk to it only through the public HTTP wire protocol, no
/// mocking. Skips cleanly (not a failure) when the `go` toolchain isn't
/// available.
final class MeshClientLiveTests: XCTestCase {
    private static let repoRoot: URL = {
        // This file lives at <repo>/swift-sdk/Tests/MeshKitTests/MeshClientLiveTests.swift
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
    }()

    private static var goAvailable: Bool = {
        let which = Process()
        which.executableURL = URL(fileURLWithPath: "/usr/bin/which")
        which.arguments = ["go"]
        which.standardOutput = Pipe()
        which.standardError = Pipe()
        try? which.run()
        which.waitUntilExit()
        return which.terminationStatus == 0
    }()

    private static var binDir: URL = {
        FileManager.default.temporaryDirectory.appendingPathComponent("meshkit-itest-\(UUID().uuidString)")
    }()

    private static var built = false

    private func buildBinariesOnce() throws {
        guard !Self.built else { return }
        try FileManager.default.createDirectory(at: Self.binDir, withIntermediateDirectories: true)
        for (name, pkg) in [("coordinator", "./cmd/coordinator"), ("stub-exo", "./cmd/stub-exo"), ("oim", "./cmd/oim")] {
            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/usr/bin/env")
            process.arguments = ["go", "build", "-o", Self.binDir.appendingPathComponent(name).path, pkg]
            process.currentDirectoryURL = Self.repoRoot
            let stderr = Pipe()
            process.standardError = stderr
            try process.run()
            process.waitUntilExit()
            if process.terminationStatus != 0 {
                let msg = String(data: stderr.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
                XCTFail("failed to build \(pkg): \(msg)")
            }
        }
        Self.built = true
    }

    private func freePort() -> Int {
        // Bind to port 0, read back the assigned port, close it — same
        // race-window tradeoff the Go integration tests already accept.
        let sock = socket(AF_INET, SOCK_STREAM, 0)
        defer { close(sock) }
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = 0
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        let result = withUnsafePointer(to: &addr) { ptr -> Int32 in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.bind(sock, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        precondition(result == 0, "failed to bind ephemeral port")
        var boundAddr = sockaddr_in()
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        withUnsafeMutablePointer(to: &boundAddr) { ptr in
            _ = ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(sock, $0, &len) }
        }
        return Int(UInt16(bigEndian: boundAddr.sin_port))
    }

    private func waitHealthy(_ url: URL, timeout: TimeInterval = 15) async throws {
        let deadline = Date().addingTimeInterval(timeout)
        var lastError: Error?
        while Date() < deadline {
            do {
                let (_, response) = try await URLSession.shared.data(from: url)
                if (response as? HTTPURLResponse)?.statusCode == 200 { return }
            } catch {
                lastError = error
            }
            try await Task.sleep(nanoseconds: 150_000_000)
        }
        throw lastError ?? NSError(domain: "MeshClientLiveTests", code: 1, userInfo: [NSLocalizedDescriptionKey: "\(url) never became healthy"])
    }

    private struct RunningMesh {
        let coordinatorURL: URL
        let processes: [Process]
    }

    private func startMesh(grantPoWBits: Int = 8) async throws -> RunningMesh {
        try buildBinariesOnce()
        let exoPort = freePort(), coordPort = freePort(), nodePort = freePort()
        let exoURL = URL(string: "http://127.0.0.1:\(exoPort)")!
        let coordURL = URL(string: "http://127.0.0.1:\(coordPort)")!
        let workDir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: workDir, withIntermediateDirectories: true)
        let nodeHome = workDir.appendingPathComponent("node-home")
        try FileManager.default.createDirectory(at: nodeHome, withIntermediateDirectories: true)

        func spawn(_ binary: String, _ args: [String], env: [String: String] = [:]) -> Process {
            let p = Process()
            p.executableURL = Self.binDir.appendingPathComponent(binary)
            p.arguments = args
            p.currentDirectoryURL = workDir
            var fullEnv = ProcessInfo.processInfo.environment
            for (k, v) in env { fullEnv[k] = v }
            p.environment = fullEnv
            p.standardOutput = FileHandle.nullDevice
            p.standardError = FileHandle.nullDevice
            // No controlling tty: without this the child inherits the test
            // runner's TTY as stdin, and governor.IsForegrounded() (which
            // checks whether this process is its tty's foreground process
            // group) then refuses every job with "pre-flight refused: node
            // is not foregrounded" — daemon mode (no tty at all) is the
            // correct, safe default for a test harness anyway.
            p.standardInput = FileHandle.nullDevice
            try? p.run()
            return p
        }

        var procs: [Process] = []
        procs.append(spawn("stub-exo", [], env: ["STUB_LISTEN": ":\(exoPort)", "STUB_RESPONSE_FILLER_WORDS": "20"]))
        try await waitHealthy(exoURL.appendingPathComponent("state"))

        procs.append(
            spawn(
                "coordinator",
                [
                    "--listen=:\(coordPort)", "--pod-id=meshkit-itest", "--region=us",
                    "--public-url=\(coordURL.absoluteString)", "--grant-pow-bits=\(grantPoWBits)",
                ]
            )
        )
        try await waitHealthy(coordURL.appendingPathComponent("health"))

        procs.append(
            spawn(
                "oim",
                [
                    "node", "start", "--coordinator=\(coordURL.absoluteString)", "--listen=:\(nodePort)",
                    "--exo-url=\(exoURL.absoluteString)", "--reachability-endpoint=http://127.0.0.1:\(nodePort)",
                    "--region=us", "--user-id=meshkit-itest-miner",
                ],
                env: ["HOME": nodeHome.path]
            )
        )

        // Wait for node registration.
        let deadline = Date().addingTimeInterval(15)
        var registered = false
        while Date() < deadline {
            let (data, _) = try await URLSession.shared.data(from: coordURL.appendingPathComponent("nodes"))
            if let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let nodes = obj["nodes"] as? [Any], !nodes.isEmpty {
                registered = true
                break
            }
            try await Task.sleep(nanoseconds: 200_000_000)
        }
        if !registered {
            procs.forEach { $0.terminate() }
            XCTFail("node never registered with coordinator")
        }
        return RunningMesh(coordinatorURL: coordURL, processes: procs)
    }

    private func stop(_ mesh: RunningMesh) {
        mesh.processes.forEach { $0.terminate() }
    }

    func testStartupGrantBalanceAndChatEndToEnd() async throws {
        try XCTSkipUnless(Self.goAvailable, "go toolchain not available")
        let mesh = try await startMesh()
        defer { stop(mesh) }

        let client = MeshClient(baseURL: mesh.coordinatorURL, userID: "meshkit-itest-consumer", timeout: 30)
        let grant = try await client.claimStartupGrant(difficultyBits: 8)
        XCTAssertGreaterThan(grant.amount, 0)

        let balance = try await client.balance()
        XCTAssertEqual(balance.total, grant.amount)

        let response = try await client.chat(model: "llama-3.2-3b", messages: [ChatMessage(role: "user", content: "hi")])
        XCTAssertTrue(response.content?.contains("Simulated response") ?? false)
        XCTAssertNotNil(response.servedByNodeID)
        XCTAssertGreaterThan(response.tokensPerSec ?? 0, 0)

        let balanceAfter = try await client.balance()
        XCTAssertLessThan(balanceAfter.total, balance.total)
    }

    func testStreamingChatParsesAllFramesIncludingTrailingUsage() async throws {
        try XCTSkipUnless(Self.goAvailable, "go toolchain not available")
        let mesh = try await startMesh()
        defer { stop(mesh) }

        let client = MeshClient(baseURL: mesh.coordinatorURL, userID: "meshkit-itest-stream-consumer", timeout: 30)
        _ = try await client.claimStartupGrant(difficultyBits: 8)

        var chunks: [ChatCompletionChunk] = []
        let stream = await client.streamChat(model: "llama-3.2-3b", messages: [ChatMessage(role: "user", content: "hi")])
        for try await chunk in stream {
            chunks.append(chunk)
        }
        XCTAssertFalse(chunks.isEmpty)

        let content = chunks.compactMap { $0.deltaContent }.joined()
        XCTAssertTrue(content.contains("Simulated response"))

        let usageFrames = chunks.filter { $0.isUsageFrame }
        XCTAssertEqual(usageFrames.count, 1)
        if let completionTokens = usageFrames.first?.usage?["completion_tokens"]?.value as? Int {
            XCTAssertGreaterThan(completionTokens, 0)
        } else if let completionTokens = usageFrames.first?.usage?["completion_tokens"]?.value as? Double {
            XCTAssertGreaterThan(completionTokens, 0)
        } else {
            XCTFail("expected completion_tokens in the usage frame")
        }
    }

    func testBackgroundLaneAssignAndExecute() async throws {
        try XCTSkipUnless(Self.goAvailable, "go toolchain not available")
        let mesh = try await startMesh()
        defer { stop(mesh) }

        let client = MeshClient(baseURL: mesh.coordinatorURL, userID: "meshkit-itest-bg-consumer", timeout: 30)
        let job = try await client.submitBackgroundJob(
            model: "llama-3.2-3b", jobID: "itest-bg-job-1", recurrence: RecurrenceSpec(intervalSeconds: 60)
        )
        XCTAssertEqual(job.jobID, "itest-bg-job-1")
        XCTAssertFalse(job.primary.isEmpty)

        let result = try await client.runBackgroundCycle(job, messages: [ChatMessage(role: "user", content: "hi")])
        XCTAssertTrue(result.content?.contains("Simulated response") ?? false)
    }

    func testInsufficientCreditsRaisesTypedError() async throws {
        try XCTSkipUnless(Self.goAvailable, "go toolchain not available")
        let mesh = try await startMesh()
        defer { stop(mesh) }

        let client = MeshClient(baseURL: mesh.coordinatorURL, userID: "meshkit-itest-broke", timeout: 30)
        do {
            _ = try await client.chat(model: "llama-3.2-3b", messages: [ChatMessage(role: "user", content: "hi")])
            XCTFail("expected MeshError.insufficientCredits")
        } catch MeshError.insufficientCredits(_, let balance, let required) {
            XCTAssertEqual(balance, 0)
            XCTAssertGreaterThan(required, 0)
        }
    }

    func testReserveNodeReturnsAUsableECDHKey() async throws {
        try XCTSkipUnless(Self.goAvailable, "go toolchain not available")
        let mesh = try await startMesh()
        defer { stop(mesh) }

        let client = MeshClient(baseURL: mesh.coordinatorURL, userID: "meshkit-itest-privacy-consumer", timeout: 30)
        let reservation = try await client.reserveNode(model: "llama-3.2-3b")
        XCTAssertFalse(reservation.reservationID.isEmpty)
        XCTAssertFalse(reservation.nodeID.isEmpty)
        XCTAssertFalse(reservation.ecdhPublicKey.isEmpty)
    }
}
