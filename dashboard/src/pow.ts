// Minimal synchronous SHA-256 used to mine a startup-grant proof-of-work
// nonce entirely on the main thread, avoiding the per-call microtask
// overhead SubtleCrypto.digest would add across hundreds of thousands of
// iterations. Byte-for-byte matches the coordinator's
// settlement.VerifyProofOfWork: sha256(utf8(userID) || bigEndianUint64(nonce)).

// Default difficulty (leading zero bits) — mirrors settlement.DefaultGrantPoWBits.
export const DEFAULT_GRANT_POW_BITS = 18

const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
])

function rotr(x: number, n: number): number {
  return (x >>> n) | (x << (32 - n))
}

function sha256(msg: Uint8Array): Uint8Array {
  const h = new Uint32Array([
    0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
    0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
  ])

  const bitLen = msg.length * 8
  const padded = new Uint8Array((msg.length + 9 + 63) & ~63)
  padded.set(msg)
  padded[msg.length] = 0x80
  const dv = new DataView(padded.buffer)
  // Message lengths here are tiny (well under 2^32 bits), so the length
  // field's high 32 bits are always zero — only the low word is written.
  dv.setUint32(padded.length - 4, bitLen >>> 0, false)

  const w = new Uint32Array(64)
  for (let offset = 0; offset < padded.length; offset += 64) {
    for (let i = 0; i < 16; i++) w[i] = dv.getUint32(offset + i * 4, false)
    for (let i = 16; i < 64; i++) {
      const s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >>> 3)
      const s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >>> 10)
      w[i] = (w[i - 16] + s0 + w[i - 7] + s1) | 0
    }
    let a = h[0], b = h[1], c = h[2], d = h[3], e = h[4], f = h[5], g = h[6], hh = h[7]
    for (let i = 0; i < 64; i++) {
      const S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)
      const ch = (e & f) ^ (~e & g)
      const t1 = (hh + S1 + ch + K[i] + w[i]) | 0
      const S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)
      const maj = (a & b) ^ (a & c) ^ (b & c)
      const t2 = (S0 + maj) | 0
      hh = g; g = f; f = e; e = (d + t1) | 0
      d = c; c = b; b = a; a = (t1 + t2) | 0
    }
    h[0] = (h[0] + a) | 0; h[1] = (h[1] + b) | 0; h[2] = (h[2] + c) | 0; h[3] = (h[3] + d) | 0
    h[4] = (h[4] + e) | 0; h[5] = (h[5] + f) | 0; h[6] = (h[6] + g) | 0; h[7] = (h[7] + hh) | 0
  }

  const out = new Uint8Array(32)
  const outView = new DataView(out.buffer)
  for (let i = 0; i < 8; i++) outView.setUint32(i * 4, h[i], false)
  return out
}

function leadingZeroBits(digest: Uint8Array): number {
  let count = 0
  for (const byte of digest) {
    if (byte === 0) { count += 8; continue }
    for (let mask = 0x80; mask !== 0; mask >>= 1) {
      if (byte & mask) return count
      count++
    }
  }
  return count
}

// Mines a nonce such that sha256(utf8(userID) || bigEndianUint64(nonce)) has
// at least difficultyBits leading zero bits — the client-side half of the
// coordinator's startup-grant proof-of-work gate (Fable security review:
// Sybil-farmable grants — user_id was a free client-generated UUID, so
// per-user dedup alone didn't stop minting unlimited disposable identities).
// At the default 18-bit difficulty this typically completes in well under a
// second; runs synchronously since a one-time grant claim doesn't warrant a
// Web Worker.
export function mineProofOfWork(userID: string, difficultyBits: number): number {
  if (difficultyBits <= 0) return 0
  const prefix = new TextEncoder().encode(userID)
  const buf = new Uint8Array(prefix.length + 8)
  buf.set(prefix)
  const view = new DataView(buf.buffer)

  for (let nonce = 0; nonce < Number.MAX_SAFE_INTEGER; nonce++) {
    const hi = Math.floor(nonce / 0x100000000)
    const lo = nonce >>> 0
    view.setUint32(prefix.length, hi, false)
    view.setUint32(prefix.length + 4, lo, false)
    if (leadingZeroBits(sha256(buf)) >= difficultyBits) return nonce
  }
  throw new Error('proof-of-work: exhausted nonce space')
}
