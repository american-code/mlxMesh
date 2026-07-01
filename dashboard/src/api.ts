import type { TopologyResponse, NodesResponse, Balance, NodeDetection, NodeConfig } from './types'

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

export async function claimStartupGrant(
  coordinatorURL: string,
  userId: string,
): Promise<{ amount: number; status?: string }> {
  const res = await fetch(`${coordinatorURL}/users/${userId}/startup-grant`, { method: 'POST' })
  if (!res.ok) throw new Error(`Grant returned ${res.status}`)
  return res.json()
}

const LOCAL_AGENT = 'http://localhost:8765'

export async function fetchLocalDetect(): Promise<NodeDetection> {
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), 2500)
  try {
    const res = await fetch(`${LOCAL_AGENT}/detect`, { signal: ctrl.signal })
    if (!res.ok) throw new Error(`Agent returned ${res.status}`)
    return res.json()
  } finally {
    clearTimeout(timer)
  }
}

export async function saveLocalConfig(cfg: NodeConfig): Promise<{ status: string; path: string }> {
  const res = await fetch(`${LOCAL_AGENT}/config`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(cfg),
  })
  if (!res.ok) throw new Error(`Config save returned ${res.status}`)
  return res.json()
}
