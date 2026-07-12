import type {
  TopologyResponse, NodesResponse, Balance, NodeDetection, NodeConfig, PodHealthDigest,
  ReconciliationReport, AdminAction, DeploymentRecord, DeploymentHistory,
} from './types'
import { mineProofOfWork, DEFAULT_GRANT_POW_BITS } from './pow'
import { getStoredApiKey, setStoredApiKey } from './identity'

export interface TestQueryResult {
  content: string
  servedByNodeId: string | null
  lane: 'fast' | 'background' | null
  tokensPerSec: number | null
  latencyMs: number | null
}

// VITE_DIRECTORY_URL accepts a comma-separated list so this dashboard isn't
// hard-dependent on any single directory instance (task #49, progressive
// decentralization) — mirrors the coordinator's own --directory flag, which
// accepts the same list-of-endpoints shape and registers with all of them.
const DIRECTORIES = (import.meta.env.VITE_DIRECTORY_URL ?? 'http://localhost:9100')
  .split(',')
  .map((url: string) => url.trim())
  .filter(Boolean)

export async function fetchTopology(): Promise<TopologyResponse> {
  const errors: string[] = []
  for (const directory of DIRECTORIES) {
    try {
      const res = await fetch(`${directory}/topology`)
      if (!res.ok) throw new Error(`Directory returned ${res.status}`)
      return await res.json()
    } catch (e) {
      errors.push(`${directory}: ${(e as Error).message}`)
    }
  }
  throw new Error(`All configured directories unreachable — ${errors.join('; ')}`)
}

export async function fetchNodes(coordinatorURL: string): Promise<NodesResponse> {
  const res = await fetch(`${coordinatorURL}/nodes`)
  if (!res.ok) throw new Error(`Coordinator returned ${res.status}`)
  return res.json()
}

export async function fetchBalance(coordinatorURL: string, userId: string): Promise<Balance> {
  const res = await fetch(`${coordinatorURL}/users/${userId}/balance`)
  if (!res.ok) throw new Error(`Balance returned ${res.status}`)
  return res.json()
}

// fetchBalanceAllPods is the "debt collector": each pod currently runs its own
// independent ledger (no cross-pod federation yet — proposal M7 is still
// unsolved), so a wallet's real balance is whatever's sitting in EVERY pod
// that ever credited or granted it, not just whichever pod the dashboard
// happens to be looking at. Asks every known pod and sums what each admits to
// owing, so a balance never appears to "vanish" just because a different pod
// answered this time. Pods that don't know this user (or are unreachable)
// silently contribute zero rather than failing the whole lookup.
export async function fetchBalanceAllPods(pods: PodHealthDigest[], userId: string): Promise<Balance> {
  const results = await Promise.allSettled(
    pods.map(p => fetchBalance(p.coordinator_endpoint, userId))
  )
  return results.reduce<Balance>((sum, r) => {
    if (r.status !== 'fulfilled') return sum
    return {
      grant_balance: sum.grant_balance + r.value.grant_balance,
      earned_balance: sum.earned_balance + r.value.earned_balance,
      total: sum.total + r.value.total,
    }
  }, { grant_balance: 0, earned_balance: 0, total: 0 })
}

// Claiming a grant requires a proof-of-work nonce (Fable security review:
// Sybil-farmable grants — user_id is a free client-generated UUID, so
// per-user dedup alone doesn't stop minting unlimited disposable identities).
// Mining runs synchronously on the main thread; the default 18-bit
// difficulty typically resolves in well under a second.
export async function claimStartupGrant(
  coordinatorURL: string,
  userId: string,
): Promise<{ amount: number; status?: string }> {
  const nonce = mineProofOfWork(userId, DEFAULT_GRANT_POW_BITS)
  const res = await fetch(`${coordinatorURL}/users/${userId}/startup-grant`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ nonce }),
  })
  if (!res.ok) throw new Error(`Grant returned ${res.status}`)
  return res.json()
}

const DEFAULT_LOCAL_AGENT = 'http://localhost:8765'
const LOCAL_AGENT_STORAGE_KEY = 'oim_local_agent_url'

// The node agent's --listen port is whatever the operator chose when they ran
// `oim node start` — 8765 is just a convenient default, not a fixed contract.
// Anyone running their real agent on a different port (e.g. alongside this
// same repo's Docker simulation stack, which already owns 8765) needs a way
// to point the dashboard at it, or Node Setup silently shows the wrong node.
export function getLocalAgentURL(): string {
  return localStorage.getItem(LOCAL_AGENT_STORAGE_KEY) || DEFAULT_LOCAL_AGENT
}

export function setLocalAgentURL(url: string): void {
  const trimmed = url.trim().replace(/\/+$/, '')
  if (trimmed) localStorage.setItem(LOCAL_AGENT_STORAGE_KEY, trimmed)
}

export function resetLocalAgentURL(): void {
  localStorage.removeItem(LOCAL_AGENT_STORAGE_KEY)
}

export const DEFAULT_LOCAL_AGENT_URL = DEFAULT_LOCAL_AGENT

export async function fetchLocalDetect(): Promise<NodeDetection> {
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), 2500)
  try {
    const res = await fetch(`${getLocalAgentURL()}/detect`, { signal: ctrl.signal })
    if (!res.ok) throw new Error(`Agent returned ${res.status}`)
    return res.json()
  } finally {
    clearTimeout(timer)
  }
}

export async function saveLocalConfig(cfg: NodeConfig): Promise<{ status: string; path: string }> {
  const res = await fetch(`${getLocalAgentURL()}/config`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(cfg),
  })
  if (!res.ok) throw new Error(`Config save returned ${res.status}`)
  return res.json()
}

export async function generateApiKey(
  coordinatorURL: string,
  userId: string,
): Promise<{ api_key: string; user_id: string; note: string }> {
  const res = await fetch(`${coordinatorURL}/users/${userId}/api-key`, { method: 'POST' })
  if (!res.ok) throw new Error(`Key generation returned ${res.status}`)
  return res.json()
}

export async function checkApiKeyExists(
  coordinatorURL: string,
  userId: string,
): Promise<{ exists: boolean }> {
  const res = await fetch(`${coordinatorURL}/users/${userId}/api-key`)
  if (!res.ok) throw new Error(`Key check returned ${res.status}`)
  return res.json()
}

export async function revokeApiKey(coordinatorURL: string, userId: string): Promise<void> {
  // Must present this account's own key: the coordinator ownership-gates
  // DELETE /users/{id}/api-key (any authenticated caller used to be able to
  // revoke anyone's key, which reopened the account-takeover path). The user is
  // revoking their OWN key here, so send it as the Bearer token to prove control.
  const headers: Record<string, string> = {}
  const key = getStoredApiKey()
  if (key) headers.Authorization = `Bearer ${key}`
  const res = await fetch(`${coordinatorURL}/users/${userId}/api-key`, { method: 'DELETE', headers })
  if (!res.ok) throw new Error(`Key revoke returned ${res.status}`)
}

// ensureDemoCredentials makes "Try the mesh" a true one-click demo. The
// coordinator correctly requires a Bearer token for /v1/chat/completions (the
// metered, billing-protected path) — this was invisible in local dev where
// --api-key is unset, but on a real deploy an anonymous visitor has neither a
// key nor any balance yet. Rather than asking a first-time visitor to go
// generate a key and solve a proof-of-work challenge before they can even see
// the mesh respond, this transparently provisions both behind the scenes:
// mint (or reuse) this device's API key, then best-effort claim the startup
// grant so there's a balance to spend from. Both endpoints are already
// exempt from the admin Bearer gate for exactly this bootstrap reason (see
// isSelfAuthenticatingWrite in cmd/coordinator/main.go).
export async function ensureDemoCredentials(coordinatorURL: string, userId: string, forceNew = false): Promise<string> {
  const existing = forceNew ? null : getStoredApiKey()
  if (existing) return existing

  const { api_key } = await generateApiKey(coordinatorURL, userId)
  setStoredApiKey(api_key)

  // Best-effort: a fresh key has no balance yet. Ignore failures (e.g.
  // already claimed under a different flow) — the query attempt below will
  // surface a clear insufficient-credits error if this didn't work.
  try {
    await claimStartupGrant(coordinatorURL, userId)
  } catch { /* non-fatal */ }

  return api_key
}

// runTestQueryWithAutoAuth wraps submitTestQuery with the same one-retry
// recovery as the iOS client: a stored key can be stale (e.g. minted against
// a coordinator whose api-key store has since reset, or from a stray earlier
// session) — rather than leaving the user stuck on a 401, mint a fresh key
// once and retry before giving up.
export async function runTestQueryWithAutoAuth(
  coordinatorURL: string,
  prompt: string,
  model: string,
  userId: string,
): Promise<TestQueryResult> {
  const apiKey = await ensureDemoCredentials(coordinatorURL, userId)
  try {
    return await submitTestQuery(coordinatorURL, prompt, model, apiKey)
  } catch (e) {
    const msg = (e as Error).message
    if (!msg.includes('401')) throw e
    const freshKey = await ensureDemoCredentials(coordinatorURL, userId, true)
    return await submitTestQuery(coordinatorURL, prompt, model, freshKey)
  }
}

// warmModel triggers POST /nodes/{id}/warm-model — the "Load model" action
// next to a Cold-badged model in NodeDetail. Reuses the same demo-credential
// flow as Try the Mesh (this write goes through the standard Bearer/API-key
// auth gate, same as any other consumer-initiated action) and the same
// one-retry-on-401 recovery as runTestQueryWithAutoAuth. Can legitimately
// take minutes (a cold multi-shard model load), so no client-side timeout is
// set here — the coordinator's own warmModelTimeout is the real bound.
export async function warmModel(coordinatorURL: string, nodeId: string, modelId: string, userId: string): Promise<void> {
  const call = async (apiKey: string) => {
    const res = await fetch(`${coordinatorURL}/nodes/${nodeId}/warm-model`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${apiKey}` },
      body: JSON.stringify({ model_id: modelId }),
    })
    if (!res.ok) throw new Error(`warm-model returned ${res.status}`)
  }
  const apiKey = await ensureDemoCredentials(coordinatorURL, userId)
  try {
    await call(apiKey)
  } catch (e) {
    const msg = (e as Error).message
    if (!msg.includes('401')) throw e
    await call(await ensureDemoCredentials(coordinatorURL, userId, true))
  }
}

// submitTestQuery sends a real inference job through the mesh from the browser.
// The response carries which node served it (oim_served_by_node_id) — that's
// how the dashboard can light up a route for THIS request only, without the
// coordinator broadcasting anyone else's routing to anyone (privacy split,
// proposal §7.1: only the requester ever learns which node served them).
export async function submitTestQuery(
  coordinatorURL: string,
  prompt: string,
  model?: string,
  apiKey?: string,
): Promise<TestQueryResult> {
  const res = await fetch(`${coordinatorURL}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(apiKey ? { Authorization: `Bearer ${apiKey}` } : {}),
    },
    body: JSON.stringify({
      model: model || undefined,
      messages: [{ role: 'user', content: prompt }],
      max_tokens: 256,
    }),
  })
  if (!res.ok) throw new Error(`Query returned ${res.status}`)
  const data = await res.json()
  const content = data?.choices?.[0]?.message?.content ?? ''
  return {
    content,
    servedByNodeId: data?.oim_served_by_node_id ?? null,
    lane: data?.oim_lane ?? null,
    tokensPerSec: data?.oim_tokens_per_sec ?? null,
    latencyMs: data?.oim_latency_ms ?? null,
  }
}

// ── Admin panel (task #96) ──────────────────────────────────────────────
// Every call below (besides the challenge/authenticate pair, which
// self-verify a signature) sends the BDFL session token as a Bearer token —
// the coordinator accepts it interchangeably with the static --api-key.

export async function requestAdminChallenge(coordinatorURL: string): Promise<{ nonce: string; expires_at: string }> {
  const res = await fetch(`${coordinatorURL}/admin/challenge`, { method: 'POST' })
  if (!res.ok) throw new Error(`Admin challenge returned ${res.status}`)
  return res.json()
}

export async function authenticateAdmin(
  coordinatorURL: string,
  nonce: string,
  signature: string,
): Promise<{ session_token: string; expires_at: string }> {
  const res = await fetch(`${coordinatorURL}/admin/authenticate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ nonce, signature }),
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body?.error || `Admin authenticate returned ${res.status}`)
  }
  return res.json()
}

function adminHeaders(sessionToken: string): Record<string, string> {
  return { Authorization: `Bearer ${sessionToken}` }
}

export async function fetchTreasuryBalance(coordinatorURL: string, sessionToken: string): Promise<Balance> {
  const res = await fetch(`${coordinatorURL}/admin/treasury`, { headers: adminHeaders(sessionToken) })
  if (!res.ok) throw new Error(`Treasury fetch returned ${res.status}`)
  const data = await res.json()
  return data.balance
}

export async function fetchReconcileReport(coordinatorURL: string, sessionToken: string): Promise<ReconciliationReport> {
  const res = await fetch(`${coordinatorURL}/admin/reconcile`, { headers: adminHeaders(sessionToken) })
  if (!res.ok) throw new Error(`Reconcile fetch returned ${res.status}`)
  return res.json()
}

export async function fetchAuditLog(coordinatorURL: string, sessionToken: string, limit = 50): Promise<AdminAction[]> {
  const res = await fetch(`${coordinatorURL}/admin/audit-log?limit=${limit}`, { headers: adminHeaders(sessionToken) })
  if (!res.ok) throw new Error(`Audit log fetch returned ${res.status}`)
  const data = await res.json()
  return data.actions ?? []
}

// fetchDeploymentHistory reads GET /admin/deployments — the read-only view of
// what `oim deploy` (internal/deploytool) has written on this coordinator's
// own host. 404 means the coordinator wasn't started with
// --deployment-history-path (not configured on this deployment), which the
// panel treats as "nothing to show" rather than an error.
export async function fetchDeploymentHistory(coordinatorURL: string, sessionToken: string): Promise<DeploymentRecord[]> {
  const res = await fetch(`${coordinatorURL}/admin/deployments`, { headers: adminHeaders(sessionToken) })
  if (res.status === 404) return []
  if (!res.ok) throw new Error(`Deployment history fetch returned ${res.status}`)
  const data: DeploymentHistory = await res.json()
  return data.records ?? []
}

export async function postTreasuryCredit(
  coordinatorURL: string,
  sessionToken: string,
  amount: number,
  reason: string,
): Promise<Balance> {
  const res = await fetch(`${coordinatorURL}/admin/treasury/credit`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...adminHeaders(sessionToken) },
    body: JSON.stringify({ amount, reason }),
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body?.error || `Treasury credit returned ${res.status}`)
  }
  const data = await res.json()
  return data.balance
}

export async function deregisterNode(coordinatorURL: string, sessionToken: string, nodeId: string): Promise<void> {
  const res = await fetch(`${coordinatorURL}/nodes/${nodeId}`, {
    method: 'DELETE',
    headers: adminHeaders(sessionToken),
  })
  if (!res.ok) throw new Error(`Deregister returned ${res.status}`)
}
