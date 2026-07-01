import type { PodHealthDigest, PodMetrics } from '../types'

interface BackpressurePanelProps {
  pods: PodHealthDigest[]
  metricsPerPod: (PodMetrics | null)[]
}

function backpressureColor(pct: number): string {
  if (pct >= 70) return '#f85149'
  if (pct >= 30) return '#d29922'
  return '#3fb950'
}

function LoadBar({
  value, max, color, label,
}: { value: number; max: number; color: string; label: string }) {
  const pct = max > 0 ? Math.min(100, (value / max) * 100) : 0
  return (
    <div style={{ flex: 1 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
        <span style={{ color: '#7d8590', fontSize: 11 }}>{label}</span>
        <span style={{ color: '#e6edf3', fontSize: 11, fontVariantNumeric: 'tabular-nums' }}>
          {value}/{max}
        </span>
      </div>
      <div style={{
        height: 6, borderRadius: 3, background: '#21262d', overflow: 'hidden',
      }}>
        <div style={{
          height: '100%',
          width: `${pct}%`,
          background: color,
          borderRadius: 3,
          transition: 'width 0.4s ease',
        }} />
      </div>
    </div>
  )
}

function BackpressureGauge({ pct }: { pct: number }) {
  const color = backpressureColor(pct)
  const label = pct >= 70 ? 'High' : pct >= 30 ? 'Moderate' : 'Normal'
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
      <div style={{
        flex: 1, height: 8, borderRadius: 4,
        background: '#21262d', overflow: 'hidden',
      }}>
        <div style={{
          height: '100%',
          width: `${Math.min(100, pct)}%`,
          background: `linear-gradient(90deg, #3fb950, ${pct >= 30 ? '#d29922' : '#3fb950'}, ${pct >= 70 ? '#f85149' : (pct >= 30 ? '#d29922' : '#3fb950')})`,
          borderRadius: 4,
          transition: 'width 0.4s ease',
        }} />
      </div>
      <div style={{
        fontSize: 12, fontWeight: 700, color,
        minWidth: 54, textAlign: 'right',
        fontVariantNumeric: 'tabular-nums',
      }}>
        {pct.toFixed(1)}% <span style={{ fontWeight: 400, color: '#7d8590' }}>{label}</span>
      </div>
    </div>
  )
}

const ZERO_METRICS: PodMetrics = { queue_depth: 0, queue_capacity: 50, backpressure_pct: 0, total_in_flight: 0 }

export function BackpressurePanel({ pods, metricsPerPod }: BackpressurePanelProps) {
  if (pods.length === 0) return null

  // Index by pod position so resolved.length always equals pods.length,
  // even when podNodes hasn't seeded yet (metricsPerPod may be shorter).
  const resolved = pods.map((_, i) => metricsPerPod[i] ?? ZERO_METRICS)

  const totalQueueDepth = resolved.reduce((s, m) => s + m.queue_depth, 0)
  const totalQueueCap = resolved.reduce((s, m) => s + m.queue_capacity, 0)
  const totalInFlight = resolved.reduce((s, m) => s + m.total_in_flight, 0)
  const avgBackpressure = resolved.reduce((s, m) => s + m.backpressure_pct, 0) / resolved.length

  return (
    <div style={{
      background: '#161b22',
      border: `1px solid ${avgBackpressure >= 70 ? '#f8514940' : avgBackpressure >= 30 ? '#d2992240' : '#21262d'}`,
      borderRadius: 10,
      padding: '14px 20px',
      marginBottom: 20,
      transition: 'border-color 0.4s',
    }}>
      <div style={{
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        marginBottom: 16,
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span style={{ fontWeight: 600, fontSize: 13 }}>Network Load</span>
          <span style={{ color: '#7d8590', fontSize: 12 }}>
            · queue + in-flight across {pods.length} region{pods.length !== 1 ? 's' : ''}
          </span>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 20 }}>
          <NetworkStat label="Queued" value={String(totalQueueDepth)} />
          <NetworkStat label="In-flight" value={String(totalInFlight)} />
          <NetworkStat label="Queue cap" value={String(totalQueueCap)} />
        </div>
      </div>

      {/* Overall backpressure gauge */}
      <div style={{ marginBottom: resolved.length > 1 ? 16 : 0 }}>
        <div style={{ color: '#7d8590', fontSize: 11, marginBottom: 6 }}>
          OVERALL BACKPRESSURE
        </div>
        <BackpressureGauge pct={avgBackpressure} />
      </div>

      {/* Per-pod breakdown when multiple pods */}
      {resolved.length > 1 && (
        <div style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))',
          gap: 16,
          paddingTop: 14,
          borderTop: '1px solid #21262d',
        }}>
          {pods.map((pod, i) => {
            const m = resolved[i]
            const bp = m.backpressure_pct
            const color = backpressureColor(bp)
            return (
              <div key={pod.pod_id}>
                <div style={{
                  display: 'flex', justifyContent: 'space-between',
                  alignItems: 'center', marginBottom: 8,
                }}>
                  <span style={{ fontSize: 12, color: '#e6edf3', fontWeight: 600 }}>
                    {pod.region_hint.toUpperCase()}
                  </span>
                  <span style={{
                    fontSize: 11, color,
                    background: `${color}18`,
                    border: `1px solid ${color}44`,
                    borderRadius: 4, padding: '1px 7px',
                  }}>
                    {bp.toFixed(1)}%
                  </span>
                </div>
                <div style={{ display: 'flex', gap: 12 }}>
                  <LoadBar value={m.queue_depth} max={Math.max(m.queue_capacity, 1)} color={color} label="Queue" />
                  <LoadBar value={m.total_in_flight} max={Math.max(m.total_in_flight + 1, 10)} color="#58a6ff" label="In-flight" />
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

function NetworkStat({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ textAlign: 'right' }}>
      <div style={{ color: '#7d8590', fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.06em' }}>
        {label}
      </div>
      <div style={{ color: '#e6edf3', fontSize: 14, fontWeight: 700, fontVariantNumeric: 'tabular-nums' }}>
        {value}
      </div>
    </div>
  )
}
