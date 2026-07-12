import CryptoKit
import Foundation

/// Proof-of-work nonce mining for POST /users/{id}/startup-grant. Matches
/// internal/settlement/pow.go's VerifyProofOfWork exactly: the coordinator
/// requires a UInt64 nonce such that sha256(user_id_bytes ++
/// big_endian_uint64(nonce)) has >= difficultyBits leading zero BITS (not
/// bytes). The default of 18 bits averages ~262k hashes — sub-second on real
/// hardware.
public enum ProofOfWork {
    public static let defaultGrantPoWBits = 18

    static func leadingZeroBits(_ digest: SHA256.Digest) -> Int {
        var count = 0
        for byte in digest {
            if byte == 0 {
                count += 8
                continue
            }
            var mask: UInt8 = 0x80
            while mask != 0 {
                if byte & mask != 0 { return count }
                count += 1
                mask >>= 1
            }
        }
        return count
    }

    /// Pure check, mirroring the Go side — used by mine(_:difficultyBits:)
    /// and exposed for interop testing.
    public static func verify(userID: String, nonce: UInt64, difficultyBits: Int) -> Bool {
        if difficultyBits <= 0 { return true }
        var data = Data(userID.utf8)
        withUnsafeBytes(of: nonce.bigEndian) { data.append(contentsOf: $0) }
        let digest = SHA256.hash(data: data)
        return leadingZeroBits(digest) >= difficultyBits
    }

    /// Brute-forces the smallest nonce satisfying `verify`. Deterministic
    /// starting point (0) — a claim is idempotent server-side, so it doesn't
    /// matter which valid nonce is submitted.
    public static func mine(userID: String, difficultyBits: Int = defaultGrantPoWBits) -> UInt64 {
        var nonce: UInt64 = 0
        while !verify(userID: userID, nonce: nonce, difficultyBits: difficultyBits) {
            nonce += 1
        }
        return nonce
    }
}
