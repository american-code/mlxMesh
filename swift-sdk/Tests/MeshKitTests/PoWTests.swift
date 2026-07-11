import CryptoKit
import Foundation
import XCTest
@testable import MeshKit

final class PoWTests: XCTestCase {
    func testMineProducesAValidSolutionAtLowDifficulty() {
        for bits in [8, 10, 12] {
            let nonce = ProofOfWork.mine(userID: "test-user", difficultyBits: bits)
            XCTAssertTrue(ProofOfWork.verify(userID: "test-user", nonce: nonce, difficultyBits: bits))
        }
    }

    private func leadingZeroBits(userID: String, nonce: UInt64) -> Int {
        var data = Data(userID.utf8)
        withUnsafeBytes(of: nonce.bigEndian) { data.append(contentsOf: $0) }
        let digest = SHA256.hash(data: data)
        var count = 0
        for byte in digest {
            if byte == 0 { count += 8; continue }
            var mask: UInt8 = 0x80
            while mask != 0 {
                if byte & mask != 0 { return count }
                count += 1
                mask >>= 1
            }
        }
        return count
    }

    func testVerifyIsExactAtTheBoundary() {
        let nonce = ProofOfWork.mine(userID: "boundary-user", difficultyBits: 10)
        let actualBits = leadingZeroBits(userID: "boundary-user", nonce: nonce)
        XCTAssertTrue(ProofOfWork.verify(userID: "boundary-user", nonce: nonce, difficultyBits: actualBits))
        XCTAssertFalse(ProofOfWork.verify(userID: "boundary-user", nonce: nonce, difficultyBits: actualBits + 1))
    }

    func testZeroDifficultyAlwaysPasses() {
        XCTAssertTrue(ProofOfWork.verify(userID: "anyone", nonce: 0, difficultyBits: 0))
        XCTAssertTrue(ProofOfWork.verify(userID: "anyone", nonce: 123456, difficultyBits: 0))
    }

    func testUserIDIsMixedIntoTheHash() {
        let nonce = ProofOfWork.mine(userID: "user-a", difficultyBits: 14)
        XCTAssertTrue(ProofOfWork.verify(userID: "user-a", nonce: nonce, difficultyBits: 14))
        XCTAssertFalse(ProofOfWork.verify(userID: "user-b", nonce: nonce, difficultyBits: 14))
    }
}
