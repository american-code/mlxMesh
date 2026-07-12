import { useState, useEffect, useCallback } from 'react'
import { useTopology } from './hooks/useTopology'
import { useAllNodes } from './hooks/useAllNodes'
import { WorldMap } from './components/WorldMap'
import type { ActiveRoute, UserLocation } from './components/WorldMap'
import { NetworkGraph } from './components/NetworkGraph'
import { GeoNetworkGraph } from './components/GeoNetworkGraph'
import { NodeDetail } from './components/NodeDetail'
import { NodeSetupView } from './components/NodeSetupView'
import { AdminView } from './components/AdminView'
import { BackpressurePanel } from './components/BackpressurePanel'
import { runTestQueryWithAutoAuth } from './api'
import { getOrCreateUserId } from './identity'
import type { NodeSnapshot, PodHealthDigest } from './types'
import {
  computeNodeStatus, STATUS_COLORS, STATUS_LABELS,
  statusColor, formatTps, formatMem,
} from './utils'

type Tab = 'network' | 'node' | 'admin'
type GraphMode = 'hub' | 'geo'

// Marketing-mode deploys (e.g. the public seed's app.mlxmesh.net) show only the
// Network view — Account/Node Setup/Admin are for operators managing their own
// contribution (or the mesh itself), not visitors browsing the network. Off by
// default so the dashboard ships with full functionality everywhere else
// (local dev, self-hosted operators).
const MARKETING_MODE = import.meta.env.VITE_MARKETING_MODE === 'true'
// Runtime (not build-time) escape hatch for the operator: appending ?ops to
// the URL reveals the hidden tabs on an otherwise marketing-mode build,
// without needing a separate rebuild/deploy every time. Not a security
// boundary by itself — Admin still requires the real BDFL challenge/response
// signature — this only controls whether the TAB is visible to a casual
// visitor.
const OPS_OVERRIDE = typeof window !== 'undefined' && new URLSearchParams(window.location.search).has('ops')
const VISIBLE_TABS: Tab[] = (MARKETING_MODE && !OPS_OVERRIDE) ? ['network'] : ['network', 'node', 'admin']

export default function App() {
  const { data: topology, error: topoError, lastUpdated, refresh } = useTopology()
  const [selected, setSelected] = useState<NodeSnapshot | null>(null)
  const [tab, setTab] = useState<Tab>('network')
  const [showCoordination, setShowCoordination] = useState(true)

  // "You" marker — approximate browser geolocation, requested once on mount.
  // Silently absent if denied/unavailable; nothing else depends on it.
  const [userLocation, setUserLocation] = useState<UserLocation | null>(null)
  useEffect(() => {
    if (!navigator.geolocation) return
    navigator.geolocation.getCurrentPosition(
      pos => setUserLocation({ lat: pos.coords.latitude, lng: pos.coords.longitude }),
      () => { /* denied or unavailable — no marker, route still works via nearest pod */ },
      { timeout: 4000, maximumAge: 600_000 },
    )
  }, [])

  // Active route — lights up only in response to a query THIS browser session
  // submitted. Cleared automatically once its fade-out animation finishes.
  const [activeRoute, setActiveRoute] = useState<ActiveRoute | null>(null)

  const pods = topology?.pods ?? []

  // ── Dynamic: works for any number of pods/regions ──
  const podNodes = useAllNodes(pods)
  const allNodes = podNodes.flatMap(p => p.nodes)
  // iOS security/coordination participants (distinct from inference nodes),
  // tagged with the region they announced through.
  const allCoordination = podNodes.flatMap(p =>
    p.coordination.map(c => ({ ...c, region: p.pod.region_hint })))

  const liveCount     = allNodes.filter(n => computeNodeStatus(n) === 'live').length
  const degradedCount = allNodes.filter(n => computeNodeStatus(n) === 'degraded').length
  const offlineCount  = allNodes.filter(n => ['stale', 'unreachable'].includes(computeNodeStatus(n))).length
  const totalTps      = allNodes.reduce((s, n) => s + n.measured_toks_per_sec, 0)
  const totalMem      = allNodes.reduce((s, n) => s + n.committed_memory_gb, 0)
  const simulatedCount = allNodes.filter(n => n.simulated).length

  // Resolve a served node_id + this browser's own location (falling back to the
  // nearest live node as a rough origin when geolocation was denied) into an
  // ActiveRoute, and schedule it to clear once the fade-out animation finishes.
  const lightUpRoute = useCallback((servedByNodeId: string | null, lane: 'fast' | 'background' | null) => {
    if (!servedByNodeId) return
    const servedNode = allNodes.find(n => n.node_id === servedByNodeId)
    if (!servedNode || (servedNode.geo_lat === 0 && servedNode.geo_lng === 0)) return

    // Without geolocation, approximate "where the query entered the mesh" as the
    // centroid of the OTHER live nodes in the serving node's own pod/region —
    // never the served node's own coordinates, which would draw a zero-length
    // (invisible) line straight onto itself.
    let origin = userLocation
    if (!origin) {
      const podGroup = podNodes.find(p => p.nodes.some(n => n.node_id === servedByNodeId))
      const others = (podGroup?.nodes ?? [])
        .filter(n => n.node_id !== servedByNodeId && (n.geo_lat !== 0 || n.geo_lng !== 0))
      origin = others.length > 0
        ? {
            lat: others.reduce((s, n) => s + n.geo_lat, 0) / others.length,
            lng: others.reduce((s, n) => s + n.geo_lng, 0) / others.length,
          }
        : { lat: servedNode.geo_lat + 3, lng: servedNode.geo_lng + 3 } // lone node in pod — small offset so the route is still visible
    }
    const route: ActiveRoute = {
      key: `${servedByNodeId}-${Date.now()}`,
      fromLat: origin.lat,
      fromLng: origin.lng,
      toLat: servedNode.geo_lat,
      toLng: servedNode.geo_lng,
      lane: lane === 'background' ? 'background' : 'fast',
    }
    setActiveRoute(route)
    const fadeMs = route.lane === 'background' ? 6000 : 4000
    setTimeout(() => {
      setActiveRoute(current => (current?.key === route.key ? null : current))
    }, fadeMs)
  }, [allNodes, podNodes, userLocation])

  const metricsPerPod = podNodes.map(p => p.metrics)
  const validMetrics  = metricsPerPod.filter(m => m !== null)
  const totalQueued   = validMetrics.reduce((s, m) => s + m!.queue_depth, 0)
  const totalInFlight = validMetrics.reduce((s, m) => s + m!.total_in_flight, 0)
  const avgBackpressure = validMetrics.length > 0
    ? validMetrics.reduce((s, m) => s + m!.backpressure_pct, 0) / validMetrics.length
    : 0

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
            mlxMesh
          </span>
          {VISIBLE_TABS.length > 1 && (
            <div style={{
              display: 'flex', background: '#1c2128',
              border: '1px solid #30363d', borderRadius: 7,
              padding: 2, gap: 2, marginLeft: 8,
            }}>
              {VISIBLE_TABS.map(t => (
                <button key={t} onClick={() => setTab(t)} style={{
                  background: tab === t ? '#2d333b' : 'transparent',
                  border: 'none', borderRadius: 5,
                  color: tab === t ? '#e6edf3' : '#7d8590',
                  padding: '3px 12px', cursor: 'pointer', fontSize: 12,
                  fontWeight: tab === t ? 600 : 400,
                  transition: 'all 0.15s',
                }}>
                  {t === 'network' ? 'Network' : t === 'node' ? 'Node Setup' : 'Admin'}
                </button>
              ))}
            </div>
          )}
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
          {validMetrics.length > 0 && (
            <>
              <div style={{ width: 1, height: 20, background: '#30363d' }} />
              <HeaderStat
                label="Queued"
                value={String(totalQueued)}
                color={totalQueued > 0 ? '#d29922' : '#7d8590'}
              />
              <HeaderStat
                label="In-flight"
                value={String(totalInFlight)}
                color={totalInFlight > 0 ? '#58a6ff' : '#7d8590'}
              />
              <BackpressurePill pct={avgBackpressure} />
            </>
          )}
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

      {tab === 'node' && <NodeSetupView />}

      {tab === 'admin' && (
        <AdminView coordinatorURL={pods[0]?.coordinator_endpoint ?? null} nodes={allNodes} onNodesChanged={refresh} />
      )}

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
                  {simulatedCount > 0 && ` (${simulatedCount} demo)`}
                </span>
              )}
            </span>
            <div style={{ display: 'flex', alignItems: 'center', gap: 20 }}>
              <RouteLegend />
              <StatusLegend />
            </div>
          </div>
          {allNodes.length === 0 ? (
            <div style={{ height: 220, display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#7d8590', fontSize: 14 }}>
              No nodes yet — be the first to contribute
            </div>
          ) : (
            <WorldMap
              nodes={allNodes}
              selected={selected}
              onNodeClick={setSelected}
              userLocation={userLocation}
              activeRoute={activeRoute}
            />
          )}
          <TryTheMesh
            coordinatorURL={pods[0]?.coordinator_endpoint ?? null}
            nodes={allNodes}
            onServed={lightUpRoute}
          />
        </section>

        {/* ── Security / coordination layer — iOS devices hosting encrypted pointers ── */}
        <SecurityLayerPanel
          participants={allCoordination}
          show={showCoordination}
          onToggle={() => setShowCoordination(v => !v)}
        />

        {/* ── Backpressure panel — queue depth and in-flight across all coordinators ── */}
        {pods.length > 0 && (
          <BackpressurePanel
            pods={pods}
            metricsPerPod={metricsPerPod}
          />
        )}

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

      {selected && (
        <NodeDetail
          node={selected}
          coordinatorURL={podNodes.find(p => p.nodes.some(n => n.node_id === selected.node_id))?.pod.coordinator_endpoint ?? null}
          onClose={() => setSelected(null)}
        />
      )}
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

function BackpressurePill({ pct }: { pct: number }) {
  const color = pct >= 70 ? '#f85149' : pct >= 30 ? '#d29922' : '#3fb950'
  const label = pct >= 70 ? 'High load' : pct >= 30 ? 'Moderate' : 'Normal'
  return (
    <div style={{
      display: 'flex', alignItems: 'center', gap: 6,
      background: `${color}12`,
      border: `1px solid ${color}40`,
      borderRadius: 6, padding: '3px 10px',
    }}>
      <div style={{ width: 7, height: 7, borderRadius: '50%', background: color }} />
      <span style={{ color, fontSize: 12, fontWeight: 600 }}>{label}</span>
      <span style={{ color: `${color}99`, fontSize: 11, fontVariantNumeric: 'tabular-nums' }}>
        {pct.toFixed(0)}%
      </span>
    </div>
  )
}

function RouteLegend() {
  return (
    <div style={{ display: 'flex', gap: 14, alignItems: 'center' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <div style={{ width: 14, height: 2, background: '#58a6ff', borderRadius: 1 }} />
        <span style={{ color: '#7d8590', fontSize: 12 }}>Fast query</span>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <div style={{ width: 14, height: 2, background: '#d29922', borderRadius: 1 }} />
        <span style={{ color: '#7d8590', fontSize: 12 }}>Background job</span>
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

// ── Try the mesh — submits a real query and lights up the route it took ────
// Only ever shows THIS browser's own request/response. onServed receives the
// serving node_id + lane straight from the coordinator's response so App can
// draw the route — nobody else's traffic is ever visible here.

// SIM_FLEET_MODEL is served by every simulated node — a guaranteed-dispatchable
// fallback distinct from a real node's own (often exclusively-hosted) model.
const SIM_FLEET_MODEL = 'llama-3.2-3b'

// pickDemoModel prefers a live, real (non-simulated) node's own model over the
// simulated fleet's shared SIM_FLEET_MODEL — so when an actual contributor
// machine is online and can serve the request, the demo shows real hardware
// responding instead of always landing on the sim fleet (which every request
// is otherwise guaranteed to hit, since SIM_FLEET_MODEL is a sim-only model no
// real contributor happens to host). Ties (multiple real live nodes) break on
// declared throughput — the routing layer's own eligibility/scoring still has
// the final say on which one actually serves it.
function pickDemoModel(nodes: NodeSnapshot[]): string {
  const realLive = nodes
    .filter(n => !n.simulated && n.status === 'live' && n.models.length > 0)
    .sort((a, b) => b.measured_toks_per_sec - a.measured_toks_per_sec)
  return realLive[0]?.models[0]?.model_id ?? SIM_FLEET_MODEL
}

function TryTheMesh({
  coordinatorURL, nodes, onServed,
}: {
  coordinatorURL: string | null
  nodes: NodeSnapshot[]
  onServed: (servedByNodeId: string | null, lane: 'fast' | 'background' | null) => void
}) {
  const [prompt, setPrompt] = useState('What can this network do?')
  const [sending, setSending] = useState(false)
  const [reply, setReply] = useState<string | null>(null)
  const [stats, setStats] = useState<{ tokensPerSec: number | null; latencyMs: number | null } | null>(null)
  const [error, setError] = useState<string | null>(null)

  async function handleSend() {
    if (!coordinatorURL || !prompt.trim() || sending) return
    setSending(true)
    setError(null)
    setReply(null)
    setStats(null)
    try {
      // The coordinator requires an authenticated caller for /v1/chat/completions
      // (it's the metered path) — transparently provision this device's demo
      // wallet (API key + startup grant) so a first-time visitor doesn't have
      // to visit Account first just to see the mesh respond. Retries once with
      // a freshly minted key if the stored one turns out to be stale.
      // Prefer a live real node's own model over the sim fleet's shared
      // fallback — see pickDemoModel. A node the topology snapshot reported as
      // "live" can still turn out to be undispatchable at the exact moment of
      // the request (stale reachability info, a brief network hiccup) — and
      // since it may be the ONLY node hosting its own model, that failure has
      // no fallback candidate for the coordinator to try on its own. Retry
      // once against the sim fleet's guaranteed-servable model rather than
      // surfacing a hard "no eligible nodes" error to the user.
      const preferred = pickDemoModel(nodes)
      const userId = getOrCreateUserId()
      let result
      try {
        result = await runTestQueryWithAutoAuth(coordinatorURL, prompt.trim(), preferred, userId)
      } catch (e) {
        if (preferred === SIM_FLEET_MODEL) throw e
        result = await runTestQueryWithAutoAuth(coordinatorURL, prompt.trim(), SIM_FLEET_MODEL, userId)
      }
      setReply(result.content || '(empty response)')
      setStats({ tokensPerSec: result.tokensPerSec, latencyMs: result.latencyMs })
      onServed(result.servedByNodeId, result.lane)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setSending(false)
    }
  }

  return (
    <div style={{
      padding: '12px 16px', borderTop: '1px solid #21262d',
      display: 'flex', flexDirection: 'column', gap: 8,
    }}>
      <div style={{ display: 'flex', gap: 8 }}>
        <input
          value={prompt}
          onChange={e => setPrompt(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') handleSend() }}
          placeholder="Ask the mesh something…"
          disabled={!coordinatorURL}
          style={{
            flex: 1, background: '#0d1117', border: '1px solid #30363d',
            borderRadius: 6, padding: '7px 12px', color: '#e6edf3', fontSize: 13,
          }}
        />
        <button
          onClick={handleSend}
          disabled={!coordinatorURL || sending || !prompt.trim()}
          style={{
            background: '#238636', border: '1px solid #2ea043',
            color: '#e6edf3', borderRadius: 6, padding: '7px 16px',
            cursor: sending || !coordinatorURL ? 'not-allowed' : 'pointer',
            fontSize: 13, fontWeight: 600, whiteSpace: 'nowrap',
          }}
        >
          {sending ? 'Sending…' : 'Try the mesh →'}
        </button>
      </div>
      {!coordinatorURL && (
        <span style={{ color: '#7d8590', fontSize: 12 }}>Connect to a coordinator to try a live query.</span>
      )}
      {error && <span style={{ color: '#f85149', fontSize: 12 }}>Error: {error}</span>}
      {reply && (
        <div style={{
          background: '#0d1117', border: '1px solid #30363d', borderRadius: 6,
          padding: '8px 12px', color: '#c9d1d9', fontSize: 12.5, lineHeight: 1.5,
        }}>
          {reply}
          {stats && (stats.tokensPerSec !== null || stats.latencyMs !== null) && (
            <div style={{
              display: 'flex', gap: 14, marginTop: 8, paddingTop: 8,
              borderTop: '1px solid #21262d', fontSize: 11, color: '#7d8590',
            }}>
              {stats.tokensPerSec !== null && (
                <span>
                  <span style={{ color: '#3fb950', fontWeight: 700, fontVariantNumeric: 'tabular-nums' }}>
                    {stats.tokensPerSec.toFixed(1)} t/s
                  </span>{' '}measured this request
                </span>
              )}
              {stats.latencyMs !== null && (
                <span>
                  <span style={{ color: '#79c0ff', fontWeight: 700, fontVariantNumeric: 'tabular-nums' }}>
                    {stats.latencyMs}ms
                  </span>{' '}latency
                </span>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// SecurityLayerPanel renders iOS coordination participants with a distinct
// shield icon and a show/hide toggle for the whole layer. Renders even at zero
// participants (so the layer is discoverable) but stays compact then.
function SecurityLayerPanel({
  participants, show, onToggle,
}: {
  participants: Array<{ device_id: string; role: string; is_mobile: boolean; geographic_hint: string; region: string }>
  show: boolean
  onToggle: () => void
}) {
  const count = participants.length
  return (
    <div style={{
      background: '#161b22', border: '1px solid #a371f733',
      borderRadius: 10, padding: '12px 20px', marginBottom: 20,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <span style={{ fontSize: 16 }}>🛡️</span>
          <span style={{ fontWeight: 600, fontSize: 13 }}>Security & coordination layer</span>
          <span style={{
            color: '#a371f7', background: '#a371f718', border: '1px solid #a371f744',
            borderRadius: 20, padding: '1px 9px', fontSize: 12, fontWeight: 700,
          }}>{count} device{count !== 1 ? 's' : ''}</span>
          <span style={{ color: '#7d8590', fontSize: 12 }}>
            iOS devices classifying on-device & hosting encrypted pointers — additive, mesh routes normally without them
          </span>
        </div>
        <button onClick={onToggle} style={{
          background: show ? '#a371f722' : '#1c2128',
          border: `1px solid ${show ? '#a371f7' : '#30363d'}`,
          color: show ? '#a371f7' : '#7d8590',
          borderRadius: 6, padding: '4px 12px', cursor: 'pointer', fontSize: 12, fontWeight: 600,
        }}>{show ? 'Layer on' : 'Layer off'}</button>
      </div>

      {show && count > 0 && (
        <div style={{
          display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))',
          gap: 10, marginTop: 12,
        }}>
          {participants.map(p => (
            <div key={p.device_id} style={{
              display: 'flex', alignItems: 'center', gap: 10,
              background: '#0d1117', border: '1px solid #21262d',
              borderRadius: 8, padding: '8px 12px',
            }}>
              <span style={{ fontSize: 18 }}>{p.is_mobile ? '📱' : '🛡️'}</span>
              <div style={{ minWidth: 0 }}>
                <div style={{ fontFamily: 'monospace', fontSize: 12, color: '#e6edf3', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {p.device_id.slice(0, 12)}…
                </div>
                <div style={{ color: '#7d8590', fontSize: 11 }}>
                  {(p.region || p.geographic_hint || '—').toUpperCase()} · {p.role.replace(/_/g, ' ')}
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
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
