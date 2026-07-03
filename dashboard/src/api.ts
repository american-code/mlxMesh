import type { TopologyResponse, NodesResponse, Balance, NodeDetection, NodeConfig } from './types'
import { mineProofOfWork, DEFAULT_GRANT_POW_BITS } from './pow'

export interface TestQueryResult {
  content: string
  servedByNodeId: string | null
  lane: 'fast' | 'background' | null
  tokensPerSec: number | null
  latencyMs: number | null
}

const DIRECTORY = import.meta.env.VITE_DIRECTORY_URL ?? 'http://localhost:9100'

export async function fetchTopology(): Promise<TopologyResponse> {
  const res = await fetch(`${DIRECTORY}/topology`)
  if (!res.ok) throw new Error(`Directory returned ${res.status}`)
  return res.json()
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
  const res = await fetch(`${coordinatorURL}/users/${userId}/api-key`, { method: 'DELETE' })
  if (!res.ok) throw new Error(`Key revoke returned ${res.status}`)
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
): Promise<TestQueryResult> {
  const res = await fetch(`${coordinatorURL}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
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
