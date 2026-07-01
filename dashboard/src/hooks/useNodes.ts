import { useState, useEffect, useCallback } from 'react'
import { fetchNodes } from '../api'
import type { NodeSnapshot } from '../types'

export function useNodes(coordinatorURL: string | null, intervalMs = 5000) {
  const [nodes, setNodes] = useState<NodeSnapshot[]>([])
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    if (!coordinatorURL) return
    try {
      const res = await fetchNodes(coordinatorURL)
      setNodes(res.nodes ?? [])
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [coordinatorURL])

  useEffect(() => {
    setNodes([])
    refresh()
    const id = setInterval(refresh, intervalMs)
    return () => clearInterval(id)
  }, [refresh, intervalMs])

  return { nodes, error }
}
