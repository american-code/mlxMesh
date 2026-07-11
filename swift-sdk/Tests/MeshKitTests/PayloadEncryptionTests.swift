import CryptoKit
import Foundation
import XCTest
@testable import MeshKit

final class PayloadEncryptionTests: XCTestCase {
    func testEncryptThenDecryptRoundTrip() throws {
        let recipientPriv = P256.KeyAgreement.PrivateKey()
        let plaintext = Data(#"[{"role":"user","content":"round trip"}]"#.utf8)

        let encrypted = try PayloadEncryption.encrypt(plaintext: plaintext, recipientPublicKey: recipientPriv.publicKey)
        XCTAssertEqual(encrypted.ephemeralPublicKeyData.count, 65)  // uncompressed P-256 point

        let recovered = try PayloadEncryption.decrypt(
            ciphertext: encrypted.ciphertext,
            ephemeralPublicKeyData: encrypted.ephemeralPublicKeyData,
            recipientPrivateKey: recipientPriv
        )
        XCTAssertEqual(recovered, plaintext)
    }

    func testCiphertextBlobLayoutIsNonceThenCiphertextPlusTag() throws {
        let recipientPriv = P256.KeyAgreement.PrivateKey()
        let encrypted = try PayloadEncryption.encrypt(plaintext: Data("x".utf8), recipientPublicKey: recipientPriv.publicKey)
        XCTAssertGreaterThanOrEqual(encrypted.ciphertext.count, 12 + 16)  // 12-byte nonce + 16-byte GCM tag
    }

    func testWrongRecipientKeyFailsToDecrypt() throws {
        let recipientPriv = P256.KeyAgreement.PrivateKey()
        let wrongPriv = P256.KeyAgreement.PrivateKey()
        let encrypted = try PayloadEncryption.encrypt(plaintext: Data("secret".utf8), recipientPublicKey: recipientPriv.publicKey)
        XCTAssertThrowsError(
            try PayloadEncryption.decrypt(
                ciphertext: encrypted.ciphertext,
                ephemeralPublicKeyData: encrypted.ephemeralPublicKeyData,
                recipientPrivateKey: wrongPriv
            )
        )
    }

    // MARK: - Cross-language interop

    private static let repoRoot: URL = {
        // This file lives at <repo>/swift-sdk/Tests/MeshKitTests/PayloadEncryptionTests.swift
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
    }()

    private static func goToolchainAvailable() -> Bool {
        let which = Process()
        which.executableURL = URL(fileURLWithPath: "/usr/bin/which")
        which.arguments = ["go"]
        which.standardOutput = Pipe()
        which.standardError = Pipe()
        try? which.run()
        which.waitUntilExit()
        return which.terminationStatus == 0
    }

    /// Builds the same go_interop_helper the Python SDK's crypto interop
    /// test uses (python-sdk/tests/go_interop_helper) — one Go helper shared
    /// by both SDKs' test suites, proving the SAME real
    /// internal/payloadcrypto.Decrypt accepts ciphertext from both.
    private func buildGoHelper() throws -> URL {
        let outDir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: outDir, withIntermediateDirectories: true)
        let outBinary = outDir.appendingPathComponent("go_interop_helper")

        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        process.arguments = ["go", "build", "-o", outBinary.path, "./python-sdk/tests/go_interop_helper"]
        process.currentDirectoryURL = Self.repoRoot
        let stderr = Pipe()
        process.standardError = stderr
        try process.run()
        process.waitUntilExit()
        guard process.terminationStatus == 0 else {
            let msg = String(data: stderr.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
            XCTFail("failed to build go_interop_helper: \(msg)")
            throw NSError(domain: "PayloadEncryptionTests", code: 1)
        }
        return outBinary
    }

    func testSwiftEncryptedPayloadDecryptsWithRealGoPayloadcrypto() throws {
        try XCTSkipUnless(Self.goToolchainAvailable(), "go toolchain not available — skipping cross-language interop test")
        let goHelper = try buildGoHelper()

        let recipientPriv = P256.KeyAgreement.PrivateKey()
        // Go's ecdh.P256().NewPrivateKey expects the raw 32-byte big-endian
        // scalar — CryptoKit's rawRepresentation for a P256 KeyAgreement
        // private key IS that raw scalar.
        let privScalarB64 = recipientPriv.rawRepresentation.base64EncodedString()

        let plaintext = Data(#"[{"role":"user","content":"hello from swift interop test"}]"#.utf8)
        let encrypted = try PayloadEncryption.encrypt(plaintext: plaintext, recipientPublicKey: recipientPriv.publicKey)
        let combinedB64 = encrypted.ciphertext.base64EncodedString()

        let process = Process()
        process.executableURL = goHelper
        process.arguments = [privScalarB64, encrypted.ephemeralPublicKeyBase64, combinedB64]
        let stdout = Pipe()
        let stderr = Pipe()
        process.standardOutput = stdout
        process.standardError = stderr
        try process.run()
        process.waitUntilExit()

        let stderrText = String(data: stderr.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
        XCTAssertEqual(process.terminationStatus, 0, "go helper failed: \(stderrText)")
        let stdoutData = stdout.fileHandleForReading.readDataToEndOfFile()
        XCTAssertEqual(stdoutData, plaintext)
    }
}
