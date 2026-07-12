import CryptoKit
import Foundation
import XCTest
@testable import MeshKit

/// Unit tests for Wallet — address derivation, persistence, and the
/// client-side "refuse before any network call" enforcement. No live
/// coordinator needed here; see MeshClientLiveTests for the real
/// challenge/response round-trip against the actual Go binary.
final class WalletTests: XCTestCase {
    /// A pure in-memory WalletStorage for tests that don't want to touch disk.
    final class InMemoryWalletStorage: WalletStorage {
        private let box = Box()
        private final class Box: @unchecked Sendable {
            var seed: Data?
        }

        func save(seed: Data) throws { box.seed = seed }
        func load() throws -> Data? { box.seed }
    }

    func testCreateProducesA32ByteSeedAndMatchingAddress() {
        let wallet = Wallet.create()
        XCTAssertTrue(wallet.address.hasPrefix("oim"))
        XCTAssertEqual(wallet.address.count, "oim".count + 64)  // sha256 hex digest, no truncation
    }

    func testAddressIsDeterministicForTheSameSeed() throws {
        let seed = Data((0..<32).map { UInt8($0) })
        let w1 = try Wallet(seed: seed)
        let w2 = try Wallet(seed: seed)
        XCTAssertEqual(w1.address, w2.address)
    }

    func testDifferentSeedsGiveDifferentAddresses() {
        XCTAssertNotEqual(Wallet.create().address, Wallet.create().address)
    }

    func testRejectsWrongLengthSeed() {
        XCTAssertThrowsError(try Wallet(seed: Data("too short".utf8)))
    }

    func testSignProducesASelfVerifiableSignature() throws {
        let wallet = Wallet.create()
        let message = Data("oim-account-auth:some-address:some-nonce".utf8)
        let signature = try wallet.sign(message)
        XCTAssertEqual(signature.count, 64)

        guard let pubKeyData = Data(base64Encoded: wallet.publicKeyBase64) else {
            return XCTFail("publicKeyBase64 was not valid base64")
        }
        let publicKey = try Curve25519.Signing.PublicKey(rawRepresentation: pubKeyData)
        XCTAssertTrue(publicKey.isValidSignature(signature, for: message))
    }

    func testSaveAndLoadRoundTrip() throws {
        let storage = InMemoryWalletStorage()
        let w1 = Wallet.create()
        try w1.save(storage: storage)
        let w2 = try Wallet.load(storage: storage)
        XCTAssertEqual(w1.address, w2.address)
    }

    func testFileStorageSetsOwnerOnlyPermissions() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let path = dir.appendingPathComponent("wallet.json")
        defer { try? FileManager.default.removeItem(at: dir) }

        try Wallet.create().save(storage: FileWalletStorage(path: path))
        let attrs = try FileManager.default.attributesOfItem(atPath: path.path)
        let permissions = attrs[.posixPermissions] as? NSNumber
        XCTAssertEqual(permissions?.uint16Value, 0o600)
    }

    func testLoadMissingWalletThrowsNotFound() {
        XCTAssertThrowsError(try Wallet.load(storage: InMemoryWalletStorage())) { error in
            guard case WalletError.notFound = error else {
                return XCTFail("expected WalletError.notFound, got \(error)")
            }
        }
    }

    func testLoadOrCreatePersistsOnFirstCallAndReusesOnSecond() throws {
        let storage = InMemoryWalletStorage()
        let w1 = try Wallet.loadOrCreate(storage: storage)
        let w2 = try Wallet.loadOrCreate(storage: storage)
        XCTAssertEqual(w1.address, w2.address)
    }

    func testClientAutoPopulatesUserIDFromWallet() async throws {
        let wallet = Wallet.create()
        let client = try MeshClient(baseURL: URL(string: "http://example.invalid")!, wallet: wallet)
        let userID = await client.userID
        XCTAssertEqual(userID, wallet.address)
    }

    func testClientRejectsConflictingUserIDAndWallet() {
        let wallet = Wallet.create()
        XCTAssertThrowsError(
            try MeshClient(
                baseURL: URL(string: "http://example.invalid")!, userID: "not-the-wallet-address", wallet: wallet
            )
        ) { error in
            guard case MeshClientError.conflictingWalletAndUserID = error else {
                return XCTFail("expected MeshClientError.conflictingWalletAndUserID, got \(error)")
            }
        }
    }

    func testClientWithNoCredentialRefusesBeforeAnyNetworkCall() async throws {
        // A deliberately unroutable address — if the SDK tried to make an
        // HTTP call at all here, this would time out/fail as a network
        // error, not noCredentialConfigured.
        let client = try MeshClient(
            baseURL: URL(string: "http://127.0.0.1:1")!, userID: "someone", timeout: 1
        )
        do {
            _ = try await client.chat(model: "llama-3.2-3b", messages: [ChatMessage(role: "user", content: "hi")])
            XCTFail("expected MeshClientError.noCredentialConfigured")
        } catch MeshClientError.noCredentialConfigured {
            // expected
        }
    }
}
