import { useState, useEffect } from 'react'
import { fetchNodes } from '../api'
import type { NodeSnapshot, PodHealthDigest, PodMetrics, CoordinationParticipant } from '../types'

export interface PodNodes {
  pod: PodHealthDigest
  nodes: NodeSnapshot[]
  coordination: CoordinationParticipant[]
  metrics: PodMetrics | null
  error: string | null
}

// Connects to each coordinator's /nodes/stream SSE endpoint.
// Falls back to polling /nodes every intervalMs if the stream errors.
export function useAllNodes(pods: PodHealthDigest[], intervalMs = 5000): PodNodes[] {
  const [results, setResults] = useState<PodNodes[]>([])

  useEffect(() => {
    if (pods.length === 0) { setResults([]); return }

    // Seed with empty entries so the grid renders immediately
    setResults(pods.map(pod => ({ pod, nodes: [], coordination: [], metrics: null, error: null })))

    const cleanupFns: (() => void)[] = []

    pods.forEach((pod, idx) => {
      let pollTimer: ReturnType<typeof setInterval> | null = null
      let es: EventSource | null = null

      function update(nodes: NodeSnapshot[], coordination: CoordinationParticipant[], metrics: PodMetrics | null, error: string | null) {
        setResults(prev => {
          // Guard against stale closures from a previous effect run
          if (prev.length !== pods.length) return prev
          const next = [...prev]
          next[idx] = { pod, nodes, coordination, metrics, error }
          return next
        })
      }

      async function poll() {
        try {
          const res = await fetchNodes(pod.coordinator_endpoint)
          update(res.nodes ?? [], res.coordination_nodes ?? [], res.metrics ?? null, null)
        } catch (e) {
          update([], [], null, e instanceof Error ? e.message : String(e))
        }
      }

      function startPolling() {
        poll()
        pollTimer = setInterval(poll, intervalMs)
      }

      // Try SSE first; fall back to polling on any connection error
      es = new EventSource(`${pod.coordinator_endpoint}/nodes/stream`)

      es.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data)
          update(data.nodes ?? [], data.coordination_nodes ?? [], data.metrics ?? null, null)
        } catch { /* ignore parse errors */ }
      }

      es.onerror = () => {
        es?.close()
        es = null
        // Only start polling if we haven't already (onerror can fire multiple times)
        if (pollTimer === null) startPolling()
      }

      cleanupFns.push(() => {
        es?.close()
        if (pollTimer !== null) clearInterval(pollTimer)
      })
    })

    return () => cleanupFns.forEach(fn => fn())
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pods.length, intervalMs])

  return results
}
