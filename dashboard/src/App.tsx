import { useState } from 'react'
import { useTopology } from './hooks/useTopology'
import { useAllNodes } from './hooks/useAllNodes'
import { WorldMap } from './components/WorldMap'
import { NetworkGraph } from './components/NetworkGraph'
import { GeoNetworkGraph } from './components/GeoNetworkGraph'
import { NodeDetail } from './components/NodeDetail'
import { AccountView } from './components/AccountView'
import { NodeSetupView } from './components/NodeSetupView'
import type { NodeSnapshot, PodHealthDigest } from './types'
import {
  computeNodeStatus, STATUS_COLORS, STATUS_LABELS,
  statusColor, formatTps, formatMem,
} from './utils'

type Tab = 'network' | 'account' | 'node'
type GraphMode = 'hub' | 'geo'

export default function App() {
  const { data: topology, error: topoError, lastUpdated, refresh } = useTopology()
  const [selected, setSelected] = useState<NodeSnapshot | null>(null)
  const [tab, setTab] = useState<Tab>('network')

  const pods = topology?.pods ?? []

  // ── Dynamic: works for any number of pods/regions ──
  const podNodes = useAllNodes(pods)
  const allNodes = podNodes.flatMap(p => p.nodes)

  const liveCount     = allNodes.filter(n => computeNodeStatus(n) === 'live').length
  const degradedCount = allNodes.filter(n => computeNodeStatus(n) === 'degraded').length
  const offlineCount  = allNodes.filter(n => ['stale', 'unreachable'].includes(computeNodeStatus(n))).length
  const totalTps      = allNodes.reduce((s, n) => s + n.measured_toks_per_sec, 0)
  const totalMem      = allNodes.reduce((s, n) => s + n.committed_memory_gb, 0)

  return (
    <div style={{ minHeight: '100vh', background: '#0d1117' }}>

      {/* ── Header ── */}
      <header style={{
        borderBottom: '1px solid #21262d',
        padding: '0 24px',
        height: 54,
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        position: 'sticky', top: 0, background: '#0d1117', zIndex: 50,
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <div className="header-live" style={{
            width: 9, height: 9, borderRadius: '50%', background: '#3fb950',
          }} />
          <span style={{ fontWeight: 700, fontSize: 15, letterSpacing: '-0.01em' }}>
            OIM Control Center
          </span>
          <div style={{
            display: 'flex', background: '#1c2128',
            border: '1px solid #30363d', borderRadius: 7,
            padding: 2, gap: 2, marginLeft: 8,
          }}>
            {(['network', 'account', 'node'] as Tab[]).map(t => (
              <button key={t} onClick={() => setTab(t)} style={{
                background: tab === t ? '#2d333b' : 'transparent',
                border: 'none', borderRadius: 5,
                color: tab === t ? '#e6edf3' : '#7d8590',
                padding: '3px 12px', cursor: 'pointer', fontSize: 12,
                fontWeight: tab === t ? 600 : 400,
                transition: 'all 0.15s',
              }}>
                {t === 'network' ? 'Network' : t === 'account' ? 'Account' : 'Node Setup'}
              </button>
            ))}
          </div>
          {topoError && (
            <span style={{
              color: '#f85149', fontSize: 12,
              background: '#f8514918', border: '1px solid #f8514940',
              padding: '2px 9px', borderRadius: 5,
            }}>
              {topoError}
            </span>
          )}
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 28 }}>
          <HeaderStat label="Live"       value={String(liveCount)} color="#3fb950" />
          {degradedCount > 0 && <HeaderStat label="Degraded" value={String(degradedCount)} color="#d29922" />}
          {offlineCount  > 0 && <HeaderStat label="Offline"  value={String(offlineCount)}  color="#f85149" />}
          <div style={{ width: 1, height: 20, background: '#30363d' }} />
          <HeaderStat label="Throughput" value={`${Math.round(totalTps).toLocaleString()} t/s`} />
          <HeaderStat label="Committed"  value={formatMem(totalMem)} />
          <HeaderStat label="Regions"    value={String(pods.length)} />
          <div style={{ width: 1, height: 20, background: '#30363d' }} />
          <button onClick={refresh} style={{
            background: '#1c2128', border: '1px solid #30363d', color: '#e6edf3',
            borderRadius: 6, padding: '5px 14px', cursor: 'pointer', fontSize: 13,
          }}>↺</button>
          {lastUpdated && (
            <span style={{ color: '#7d8590', fontSize: 12, fontVariantNumeric: 'tabular-nums' }}>
              {lastUpdated.toLocaleTimeString()}
            </span>
          )}
        </div>
      </header>

      {tab === 'account' && (
        <AccountView coordinatorURL={pods[0]?.coordinator_endpoint ?? null} />
      )}

      {tab === 'node' && <NodeSetupView />}

      <main style={{ padding: '20px 24px', maxWidth: 1440, margin: '0 auto', display: tab === 'network' ? 'block' : 'none' }}>

        {/* ── World map — projection auto-fits wherever nodes actually are ── */}
        <section style={{
          background: '#161b22', border: '1px solid #21262d',
          borderRadius: 10, marginBottom: 20, overflow: 'hidden',
        }}>
          <div style={{
            padding: '11px 16px', borderBottom: '1px solid #21262d',
            display: 'flex', justifyContent: 'space-between', alignItems: 'center',
          }}>
            <span style={{ fontWeight: 600, fontSize: 13 }}>
              Global Distribution
              {pods.length > 0 && (
                <span style={{ color: '#7d8590', fontWeight: 400, marginLeft: 8 }}>
                  {pods.length} region{pods.length !== 1 ? 's' : ''} · {allNodes.length} nodes
                </span>
              )}
            </span>
            <StatusLegend />
          </div>
          {allNodes.length === 0 ? (
            <div style={{ height: 220, display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#7d8590', fontSize: 14 }}>
              Connecting to network…
            </div>
          ) : (
            <WorldMap nodes={allNodes} selected={selected} onNodeClick={setSelected} />
          )}
        </section>

        {/* ── Pod summary cards — one per coordinator, any count ── */}
        {podNodes.length > 0 && (
          <div style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))',
            gap: 16, marginBottom: 20,
          }}>
            {podNodes.map(({ pod, nodes }) => (
              <PodCard key={pod.pod_id} pod={pod} nodes={nodes} />
            ))}
          </div>
        )}

        {/* ── Per-region network graphs — one per coordinator, any count ── */}
        <div style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fit, minmax(400px, 1fr))',
          gap: 16,
        }}>
          {podNodes
            .filter(p => p.nodes.length > 0)
            .map(({ pod, nodes }) => (
              <GraphCard
                key={pod.pod_id}
                title={`${pod.region_hint.toUpperCase()} Region`}
                podId={pod.pod_id}
                nodes={nodes}
                region={pod.region_hint}
                selected={selected}
                onNodeClick={setSelected}
              />
            ))}
        </div>
      </main>

      {selected && <NodeDetail node={selected} onClose={() => setSelected(null)} />}
    </div>
  )
}

// ── Sub-components ─────────────────────────────────────────────────────────

function HeaderStat({ label, value, color }: { label: string; value: string; color?: string }) {
  return (
    <div style={{ textAlign: 'right' }}>
      <div style={{ color: '#7d8590', fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.06em' }}>
        {label}
      </div>
      <div style={{ color: color ?? '#e6edf3', fontSize: 15, fontWeight: 700, fontVariantNumeric: 'tabular-nums', lineHeight: 1.2 }}>
        {value}
      </div>
    </div>
  )
}

function StatusLegend() {
  return (
    <div style={{ display: 'flex', gap: 16, alignItems: 'center' }}>
      {(Object.entries(STATUS_LABELS) as [keyof typeof STATUS_COLORS, string][]).map(([status, label]) => (
        <div key={status} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <div style={{ width: 8, height: 8, borderRadius: '50%', background: STATUS_COLORS[status] }} />
          <span style={{ color: '#7d8590', fontSize: 12 }}>{label}</span>
        </div>
      ))}
    </div>
  )
}

function PodCard({ pod, nodes }: { pod: PodHealthDigest; nodes: NodeSnapshot[] }) {
  const statusCounts = nodes.reduce<Record<string, number>>((acc, n) => {
    const s = computeNodeStatus(n)
    acc[s] = (acc[s] ?? 0) + 1
    return acc
  }, {})

  const liveNodes  = nodes.filter(n => computeNodeStatus(n) === 'live').length
  const healthPct  = Math.round(pod.aggregate_health_score * 100)

  return (
    <div style={{
      background: '#161b22', border: '1px solid #21262d',
      borderRadius: 10, padding: '16px 20px',
    }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 16 }}>
        <div>
          <div style={{ fontWeight: 700, fontSize: 15 }}>{pod.pod_id}</div>
          <div style={{ color: '#7d8590', fontSize: 12, marginTop: 2 }}>
            {pod.region_hint.toUpperCase()} · {pod.coordinator_endpoint}
          </div>
        </div>
        <HealthBadge score={healthPct} />
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 12, marginBottom: 14 }}>
        <MiniStat label="Nodes"  value={`${liveNodes}/${nodes.length}`} />
        <MiniStat label="Memory" value={formatMem(pod.total_memory_gb)} />
        <MiniStat label="Tok/s"  value={formatTps(pod.aggregate_toks_per_sec)} />
        <MiniStat label="Models" value={String(pod.servable_model_ids?.length ?? 0)} />
      </div>

      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        {Object.entries(statusCounts).map(([s, count]) => (
          <div key={s} style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
            <div style={{ width: 7, height: 7, borderRadius: '50%', background: statusColor(s as never) }} />
            <span style={{ color: '#7d8590', fontSize: 12 }}>{count} {STATUS_LABELS[s as keyof typeof STATUS_LABELS]}</span>
          </div>
        ))}
      </div>
    </div>
  )
}

function HealthBadge({ score }: { score: number }) {
  const color = score >= 70 ? '#3fb950' : score >= 40 ? '#d29922' : '#f85149'
  return (
    <div style={{
      background: `${color}18`, border: `1px solid ${color}44`,
      borderRadius: 6, padding: '4px 10px',
      color, fontSize: 13, fontWeight: 700,
    }}>
      {score}%
    </div>
  )
}

function MiniStat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div style={{ color: '#7d8590', fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.06em', marginBottom: 4 }}>
        {label}
      </div>
      <div style={{ color: '#e6edf3', fontSize: 17, fontWeight: 700, fontVariantNumeric: 'tabular-nums' }}>
        {value}
      </div>
    </div>
  )
}

function GraphCard({
  title, podId, nodes, region, selected, onNodeClick,
}: {
  title: string
  podId: string
  nodes: NodeSnapshot[]
  region: string
  selected: NodeSnapshot | null
  onNodeClick: (n: NodeSnapshot) => void
}) {
  const [mode, setMode] = useState<GraphMode>('hub')

  // Hub and geo graphs show only active nodes. Stale/unreachable nodes remain
  // visible on the world map (as orange/red dots) for geographic awareness.
  const activeNodes = nodes.filter(n => ['live', 'degraded'].includes(computeNodeStatus(n)))
  const offlineCount = nodes.length - activeNodes.length

  return (
    <div style={{ background: '#161b22', border: '1px solid #21262d', borderRadius: 10, overflow: 'hidden' }}>
      <div style={{
        padding: '11px 16px', borderBottom: '1px solid #21262d',
        display: 'flex', justifyContent: 'space-between', alignItems: 'center',
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span style={{ fontWeight: 600, fontSize: 13 }}>{title}</span>
          {offlineCount > 0 && (
            <span style={{
              fontSize: 10, fontWeight: 600,
              color: '#f85149', background: '#f8514918',
              border: '1px solid #f8514940',
              padding: '1px 6px', borderRadius: 4,
            }}>
              {offlineCount} offline
            </span>
          )}
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <div style={{
            display: 'flex', background: '#0d1117',
            border: '1px solid #30363d', borderRadius: 6,
            padding: 2, gap: 1,
          }}>
            {(['hub', 'geo'] as GraphMode[]).map(m => (
              <button
                key={m}
                onClick={() => setMode(m)}
                title={m === 'hub' ? 'Snowflake layout' : 'Geographic KNN layout'}
                style={{
                  background: mode === m ? '#2d333b' : 'transparent',
                  border: 'none', borderRadius: 4,
                  color: mode === m ? '#e6edf3' : '#7d8590',
                  padding: '2px 9px', cursor: 'pointer', fontSize: 11,
                  fontWeight: mode === m ? 600 : 400,
                  transition: 'all 0.12s',
                }}
              >
                {m === 'hub' ? '⬡ Hub' : '⊕ Geo'}
              </button>
            ))}
          </div>
          <span style={{ color: '#7d8590', fontSize: 11 }}>{podId}</span>
        </div>
      </div>
      <div style={{ padding: '8px 12px' }}>
        {mode === 'hub' ? (
          <NetworkGraph
            nodes={activeNodes}
            podId={podId}
            region={region}
            selected={selected}
            onNodeClick={onNodeClick}
          />
        ) : (
          <GeoNetworkGraph
            nodes={activeNodes}
            selected={selected}
            onNodeClick={onNodeClick}
          />
        )}
      </div>
    </div>
  )
}
