import type { NodeSnapshot, NodeStatus } from './types'

// Expected tok/s by memory tier — used to flag reduced performance.
const EXPECTED_TPS: Record<number, number> = {
  12: 22, 16: 32, 24: 50, 32: 68, 48: 100, 64: 145,
}

function expectedTps(gb: number): number {
  return EXPECTED_TPS[Math.round(gb)] ?? gb * 2.2
}

export function computeNodeStatus(node: NodeSnapshot): NodeStatus {
  if (node.status === 'unreachable') return 'unreachable'
  if (node.status === 'stale') return 'stale'
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
  return 7 + (Math.min(gb, 64) / 64) * 13
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
