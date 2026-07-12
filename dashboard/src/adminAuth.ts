// BDFL admin login (task #96): the operator pastes their BDFL private key
// into the login form once per session; the challenge nonce is signed
// entirely in this tab using @noble/ed25519 (async — its default
// etc.sha512Async uses the browser's crypto.subtle, no extra hash dependency
// needed) and only the resulting signature ever crosses the network. The
// pasted key itself is never stored — see signChallenge below.

import { signAsync } from '@noble/ed25519'

// Deliberately sessionStorage, NOT localStorage (unlike the low-privilege
// device UUID in identity.ts) — an admin session grants treasury-adjustment
// and node-deregistration authority, so it should not silently persist
// across browser restarts.
const SESSION_STORAGE_KEY = 'oim_admin_session'

function hexToBytes(hex: string): Uint8Array {
  const clean = hex.trim().replace(/^0x/i, '')
  if (clean.length % 2 !== 0 || !/^[0-9a-fA-F]*$/.test(clean)) {
    throw new Error('Private key must be a hex string')
  }
  const out = new Uint8Array(clean.length / 2)
  for (let i = 0; i < out.length; i++) {
    out[i] = parseInt(clean.substr(i * 2, 2), 16)
  }
  return out
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = ''
  for (const b of bytes) binary += String.fromCharCode(b)
  return btoa(binary)
}

// signChallenge signs the coordinator's admin-auth nonce with the pasted
// BDFL private key, entirely client-side.
//
// `oim admin keygen` prints Go's Ed25519 private key: 64 bytes (RFC 8032
// seed || its own public key), hex-encoded as 128 characters. @noble/ed25519's
// sign() takes only the 32-byte SEED — so only the first half of the pasted
// hex is used; the trailing public-key half is discarded (it's redundant,
// not a second secret).
export async function signChallenge(privateKeyHex: string, nonce: string): Promise<string> {
  const clean = privateKeyHex.trim().replace(/^0x/i, '')
  if (clean.length !== 128) {
    throw new Error('Expected a 64-byte (128 hex character) private key, exactly as printed by `oim admin keygen`')
  }
  const seed = hexToBytes(clean.slice(0, 64))
  const message = new TextEncoder().encode(`oim-admin-auth:${nonce}`)
  const signature = await signAsync(message, seed)
  return bytesToBase64(signature)
}

export function getAdminSessionToken(): string | null {
  return sessionStorage.getItem(SESSION_STORAGE_KEY)
}

export function setAdminSessionToken(token: string): void {
  sessionStorage.setItem(SESSION_STORAGE_KEY, token)
}

export function clearAdminSessionToken(): void {
  sessionStorage.removeItem(SESSION_STORAGE_KEY)
}
