import type { NodeSnapshot, NodeStatus } from './types'

// Expected tok/s by memory tier — used to flag reduced performance.
const EXPECTED_TPS: Record<number, number> = {
  12: 22, 16: 32, 24: 50, 32: 68, 48: 100, 64: 145, 128: 360, 256: 760,
}

function expectedTps(gb: number): number {
  return EXPECTED_TPS[Math.round(gb)] ?? gb * 2.2
}

// Heartbeats fire every ~30s. If last_seen_at is >75s old the node missed at
// least two cycles — treat it as stale regardless of what the backend says.
// This backstops a slow or stale coordinator response.
const FRONTEND_STALE_MS = 75_000

export function computeNodeStatus(node: NodeSnapshot): NodeStatus {
  if (node.status === 'unreachable') return 'unreachable'
  if (node.status === 'stale') return 'stale'
  if (node.last_seen_at) {
    const ageMs = Date.now() - new Date(node.last_seen_at).getTime()
    if (ageMs > FRONTEND_STALE_MS) return 'stale'
  }
  if (node.measured_toks_per_sec < expectedTps(node.declared_memory_gb) * 0.5) return 'degraded'
  return 'live'
}

export const STATUS_COLORS: Record<NodeStatus, string> = {
  live:        '#3fb950',
  degraded:    '#d29922',
  stale:       '#db6d28',
  unreachable: '#f85149',
}

export const STATUS_LABELS: Record<NodeStatus, string> = {
  live:        'Live',
  degraded:    'Reduced perf',
  stale:       'Stale',
  unreachable: 'Unreachable',
}

export function statusColor(s: NodeStatus): string {
  return STATUS_COLORS[s]
}

// Map declared memory (GB) → SVG circle radius.
export function memToRadius(gb: number): number {
  // log scale so 256 GB exo clusters are visually prominent but don't dwarf 32 GB nodes
  return 6 + Math.log2(Math.max(gb, 8)) * 3.2
}

// Extract hostname from reachability_endpoint (e.g. "http://node-us-3:8767" → "node-us-3").
export function nodeLabel(node: NodeSnapshot): string {
  try {
    return new URL(node.reachability_endpoint).hostname
  } catch {
    return node.node_id.slice(0, 8)
  }
}

export function formatTps(tps: number): string {
  return tps >= 1 ? `${Math.round(tps)} t/s` : '—'
}

export function formatMem(gb: number): string {
  return `${gb % 1 === 0 ? gb : gb.toFixed(1)} GB`
}

export function formatTime(iso: string): string {
  try { return new Date(iso).toLocaleTimeString() } catch { return iso }
}

// True for high-memory exo cluster nodes (128 GB or 256 GB multi-device arrays).
export function isExoNode(node: NodeSnapshot): boolean {
  return node.is_cluster || node.declared_memory_gb >= 128
}
