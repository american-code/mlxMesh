import type { TopologyResponse, NodesResponse } from './types'

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
