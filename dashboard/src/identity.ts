// Shared anonymous wallet identity — the SAME user_id and API key are used by
// both the Account tab and "Try the mesh", so a demo query and a manually
// managed account represent one wallet, not two disconnected identities.

const USER_ID_KEY = 'oim_user_id'
const API_KEY_STORAGE = 'oim_api_key'

export function getOrCreateUserId(): string {
  let id = localStorage.getItem(USER_ID_KEY)
  if (!id) {
    id = crypto.randomUUID()
    localStorage.setItem(USER_ID_KEY, id)
  }
  return id
}

export function getStoredApiKey(): string | null {
  return localStorage.getItem(API_KEY_STORAGE)
}

export function setStoredApiKey(key: string): void {
  localStorage.setItem(API_KEY_STORAGE, key)
}

export function clearStoredApiKey(): void {
  localStorage.removeItem(API_KEY_STORAGE)
}
