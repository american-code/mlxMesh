import CryptoKit
import Foundation

/// Client half of the coordinator's startup-grant proof-of-work gate. Mirrors
/// internal/settlement/pow.go (and the web dashboard's pow.ts) EXACTLY:
/// find a nonce such that sha256(utf8(userID) || bigEndianUint64(nonce)) has at
/// least `difficultyBits` leading zero bits. Without this, POST /startup-grant
/// returns 400 "insufficient proof of work" and the balance never moves.
enum ProofOfWork {
    /// Must equal the coordinator's --grant-pow-bits (settlement.DefaultGrantPoWBits).
    /// At 18 bits this averages ~262k SHA-256 hashes — sub-second on device.
    static let defaultGrantBits = 18

    /// Mines a valid nonce for `userID`. CPU-bound; call from a detached task so
    /// it never blocks the main actor.
    static func mineStartupGrant(userID: String, difficultyBits: Int = defaultGrantBits) -> UInt64 {
        if difficultyBits <= 0 { return 0 }
        let prefix = Array(userID.utf8)
        var buf = [UInt8](repeating: 0, count: prefix.count + 8)
        buf.replaceSubrange(0..<prefix.count, with: prefix)

        var nonce: UInt64 = 0
        while true {
            // Big-endian nonce into the trailing 8 bytes (MSB at prefix.count).
            var n = nonce
            var i = prefix.count + 7
            while i >= prefix.count {
                buf[i] = UInt8(n & 0xFF)
                n >>= 8
                i -= 1
            }
            if leadingZeroBits(SHA256.hash(data: Data(buf))) >= difficultyBits {
                return nonce
            }
            nonce &+= 1
        }
    }

    static func leadingZeroBits<S: Sequence>(_ digest: S) -> Int where S.Element == UInt8 {
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
}
