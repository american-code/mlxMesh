import { useState, useEffect, useCallback } from 'react'
import { fetchTopology } from '../api'
import type { TopologyResponse } from '../types'

export function useTopology(intervalMs = 5000) {
  const [data, setData] = useState<TopologyResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)

  const refresh = useCallback(async () => {
    try {
      const res = await fetchTopology()
      setData(res)
      setError(null)
      setLastUpdated(new Date())
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  useEffect(() => {
    refresh()
    const id = setInterval(refresh, intervalMs)
    return () => clearInterval(id)
  }, [refresh, intervalMs])

  return { data, error, lastUpdated, refresh }
}
