import type { NodeSnapshot } from '../types'
import { computeNodeStatus, statusColor, nodeLabel, isExoNode } from '../utils'

const W = 360, H = 260, PAD = 28

interface Props {
  nodes: NodeSnapshot[]
  selected: NodeSnapshot | null
  onNodeClick: (node: NodeSnapshot) => void
}

// ── KNN helpers (same logic as WorldMap) ────────────────────────────────────

function haversineKm(lat1: number, lng1: number, lat2: number, lng2: number): number {
  const R = 6371
  const φ1 = lat1 * Math.PI / 180, φ2 = lat2 * Math.PI / 180
  const Δφ = (lat2 - lat1) * Math.PI / 180, Δλ = (lng2 - lng1) * Math.PI / 180
  const a = Math.sin(Δφ / 2) ** 2 + Math.cos(φ1) * Math.cos(φ2) * Math.sin(Δλ / 2) ** 2
  return R * 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a))
}

interface KnnEdge { aId: string; bId: string }

function buildKnnEdges(nodes: NodeSnapshot[], k = 3): KnnEdge[] {
  const seen = new Set<string>()
  const edges: KnnEdge[] = []
  for (const node of nodes) {
    const nearest = nodes
      .filter(n => n.node_id !== node.node_id)
      .sort((a, b) =>
        haversineKm(node.geo_lat, node.geo_lng, a.geo_lat, a.geo_lng) -
        haversineKm(node.geo_lat, node.geo_lng, b.geo_lat, b.geo_lng))
      .slice(0, k)
    for (const neighbor of nearest) {
      const key = [node.node_id, neighbor.node_id].sort().join('|')
      if (!seen.has(key)) { seen.add(key); edges.push({ aId: node.node_id, bId: neighbor.node_id }) }
    }
  }
  return edges
}

// ── Linear lat/lng → SVG projection ─────────────────────────────────────────

function project(nodes: NodeSnapshot[]): Map<string, { x: number; y: number }> {
  const lats = nodes.map(n => n.geo_lat)
  const lngs = nodes.map(n => n.geo_lng)
  const minLat = Math.min(...lats), maxLat = Math.max(...lats)
  const minLng = Math.min(...lngs), maxLng = Math.max(...lngs)
  const latRange = Math.max(maxLat - minLat, 0.5)   // avoid div-by-0 for single-node case
  const lngRange = Math.max(maxLng - minLng, 0.5)
  const usableW = W - PAD * 2, usableH = H - PAD * 2

  const map = new Map<string, { x: number; y: number }>()
  for (const n of nodes) {
    map.set(n.node_id, {
      x: PAD + ((n.geo_lng - minLng) / lngRange) * usableW,
      y: PAD + ((maxLat - n.geo_lat) / latRange) * usableH,   // invert: lat↑ = y↓
    })
  }
  return map
}

// ── Component ─────────────────────────────────────────────────────────────

export function GeoNetworkGraph({ nodes, selected, onNodeClick }: Props) {
  const geo = nodes.filter(n => n.geo_lat !== 0 || n.geo_lng !== 0)
  const positions = project(geo)
  const edges = buildKnnEdges(geo, 3)
  const byId = new Map(geo.map(n => [n.node_id, n]))

  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      style={{ width: '100%', height: '100%', display: 'block' }}
      aria-label="Geographic KNN network graph"
    >
      {/* subtle grid hints */}
      <rect x={PAD} y={PAD} width={W - PAD * 2} height={H - PAD * 2}
        fill="none" stroke="#21262d" strokeWidth={0.5} rx={4} />

      {/* KNN edges */}
      {edges.map(({ aId, bId }) => {
        const a = byId.get(aId), b = byId.get(bId)
        if (!a || !b) return null
        const pa = positions.get(aId), pb = positions.get(bId)
        if (!pa || !pb) return null
        const ord: Record<string, number> = { live: 0, degraded: 1, stale: 2, unreachable: 3 }
        const sa = computeNodeStatus(a), sb = computeNodeStatus(b)
        const color = statusColor(ord[sa] >= ord[sb] ? sa : sb)
        return (
          <line
            key={`${aId}|${bId}`}
            x1={pa.x} y1={pa.y} x2={pb.x} y2={pb.y}
            stroke={color} strokeOpacity={0.35} strokeWidth={1.2}
            strokeLinecap="round"
          />
        )
      })}

      {/* Node circles / diamonds */}
      {geo.map(node => {
        const pos = positions.get(node.node_id)
        if (!pos) return null
        const status = computeNodeStatus(node)
        const color  = statusColor(status)
        const isSel  = selected?.node_id === node.node_id
        const isExo  = isExoNode(node)
        const s      = isExo ? 7 : 5

        return (
          <g
            key={node.node_id}
            transform={`translate(${pos.x},${pos.y})`}
            style={{ cursor: 'pointer' }}
            onClick={() => onNodeClick(node)}
          >
            {/* pulse ring */}
            {status === 'live' && (
              <circle r={s + 5} fill="none" stroke={color} strokeOpacity={0.2} strokeWidth={1.2}>
                <animate attributeName="r" values={`${s+2};${s+7};${s+2}`} dur="2.4s" repeatCount="indefinite" />
                <animate attributeName="stroke-opacity" values="0.28;0.05;0.28" dur="2.4s" repeatCount="indefinite" />
              </circle>
            )}
            {/* selection ring */}
            {isSel && <circle r={s + 7} fill="none" stroke={color} strokeOpacity={0.9} strokeWidth={2} />}

            {isExo ? (
              <>
                {/* Diamond body */}
                <polygon
                  points={`0,${-s} ${s},0 0,${s} ${-s},0`}
                  fill={color}
                  fillOpacity={status === 'unreachable' ? 0.35 : 0.92}
                  stroke="#a371f7"
                  strokeWidth={1.8}
                />
                {/* Memory badge */}
                <text y={s + 9} textAnchor="middle"
                  fontSize={6} fill="#a371f7" fontWeight={700}
                  style={{ pointerEvents: 'none', userSelect: 'none' }}>
                  {node.declared_memory_gb}G
                </text>
              </>
            ) : (
              <circle
                r={s}
                fill={color}
                fillOpacity={status === 'unreachable' ? 0.35 : 0.92}
                stroke="#0d1117"
                strokeWidth={1.2}
              />
            )}
            {/* label */}
            <text
              y={-(s + 4)}
              textAnchor="middle"
              fontSize={7}
              fill="#7d8590"
              style={{ pointerEvents: 'none', userSelect: 'none' }}
            >
              {nodeLabel(node)}
            </text>
          </g>
        )
      })}
    </svg>
  )
}
