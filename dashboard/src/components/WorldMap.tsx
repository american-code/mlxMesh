import { ComposableMap, Geographies, Geography, Marker, Line } from 'react-simple-maps'
import type { NodeSnapshot } from '../types'
import { computeNodeStatus, statusColor, isExoNode } from '../utils'

const GEO_URL = 'https://cdn.jsdelivr.net/npm/world-atlas@2/countries-110m.json'

// Auto-fit projection to the actual node bounding box.
// Scale formula: empirically, scale=360 fits ~143° of longitude in 1080px.
// Extends naturally: wider data → lower scale → zooms out to full world.
function autoProjection(nodes: NodeSnapshot[]): { scale: number; center: [number, number] } {
  const geo = nodes.filter(n => n.geo_lat !== 0 || n.geo_lng !== 0)
  if (geo.length === 0) return { scale: 153, center: [0, 20] }

  const lats = geo.map(n => n.geo_lat)
  const lngs = geo.map(n => n.geo_lng)
  const minLat = Math.min(...lats), maxLat = Math.max(...lats)
  const minLng = Math.min(...lngs), maxLng = Math.max(...lngs)

  const centerLat = (minLat + maxLat) / 2
  const centerLng = (minLng + maxLng) / 2
  const spanLng   = Math.max(maxLng - minLng, 20)

  // scale=360 comfortably fits 143° longitude; scale inversely with padded span
  const scale = Math.min(420, Math.round(360 * 143 / (spanLng * 1.25)))

  return { scale: Math.max(scale, 130), center: [centerLng, centerLat] }
}

interface Props {
  nodes: NodeSnapshot[]
  selected: NodeSnapshot | null
  onNodeClick: (node: NodeSnapshot) => void
}

// ── KNN edge logic ─────────────────────────────────────────────────────────

function haversineKm(lat1: number, lng1: number, lat2: number, lng2: number): number {
  const R = 6371
  const φ1 = lat1 * Math.PI / 180, φ2 = lat2 * Math.PI / 180
  const Δφ = (lat2 - lat1) * Math.PI / 180, Δλ = (lng2 - lng1) * Math.PI / 180
  const a = Math.sin(Δφ / 2) ** 2 + Math.cos(φ1) * Math.cos(φ2) * Math.sin(Δλ / 2) ** 2
  return R * 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a))
}

interface KnnEdge { a: NodeSnapshot; b: NodeSnapshot }

function buildKnnEdges(nodes: NodeSnapshot[], k = 3): KnnEdge[] {
  const geo = nodes.filter(n => n.geo_lat !== 0 || n.geo_lng !== 0)
  const seen = new Set<string>()
  const edges: KnnEdge[] = []
  for (const node of geo) {
    const nearest = geo
      .filter(n => n.node_id !== node.node_id)
      .sort((a, b) =>
        haversineKm(node.geo_lat, node.geo_lng, a.geo_lat, a.geo_lng) -
        haversineKm(node.geo_lat, node.geo_lng, b.geo_lat, b.geo_lng))
      .slice(0, k)
    for (const neighbor of nearest) {
      const key = [node.node_id, neighbor.node_id].sort().join('|')
      if (!seen.has(key)) { seen.add(key); edges.push({ a: node, b: neighbor }) }
    }
  }
  return edges
}

function edgeColor(a: NodeSnapshot, b: NodeSnapshot): string {
  const ord: Record<string, number> = { live: 0, degraded: 1, stale: 2, unreachable: 3 }
  const sa = computeNodeStatus(a), sb = computeNodeStatus(b)
  return statusColor(ord[sa] >= ord[sb] ? sa : sb)
}

// ── Component ──────────────────────────────────────────────────────────────

export function WorldMap({ nodes, selected, onNodeClick }: Props) {
  const mapped = nodes.filter(n => n.geo_lat !== 0 || n.geo_lng !== 0)
  const edges  = buildKnnEdges(mapped, 3)
  const { scale, center } = autoProjection(mapped)

  return (
    <ComposableMap
      projection="geoNaturalEarth1"
      projectionConfig={{ scale, center }}
      width={1080}
      height={220}
      style={{ width: '100%', display: 'block' }}
    >
      <Geographies geography={GEO_URL}>
        {({ geographies }) =>
          geographies.map(geo => (
            <Geography
              key={geo.rsmKey}
              geography={geo}
              fill="#1c2128"
              stroke="#21262d"
              strokeWidth={0.5}
              style={{
                default: { outline: 'none' },
                hover:   { outline: 'none', fill: '#1e2535' },
                pressed: { outline: 'none' },
              }}
            />
          ))
        }
      </Geographies>

      {/* KNN proximity edges — drawn under markers */}
      {edges.map(({ a, b }) => (
        <Line
          key={`${a.node_id}|${b.node_id}`}
          from={[a.geo_lng, a.geo_lat]}
          to={[b.geo_lng, b.geo_lat]}
          stroke={edgeColor(a, b)}
          strokeWidth={1.2}
          strokeOpacity={0.35}
          strokeLinecap="round"
        />
      ))}

      {/* Node markers */}
      {mapped.map(node => {
        const status   = computeNodeStatus(node)
        const color    = statusColor(status)
        const isSel    = selected?.node_id === node.node_id
        const isExo    = isExoNode(node)
        const s        = isExo ? 7 : 5  // diamond half-size / circle radius

        return (
          <Marker
            key={node.node_id}
            coordinates={[node.geo_lng, node.geo_lat]}
            onClick={() => onNodeClick(node)}
          >
            {/* Pulse ring — always a circle for animation compatibility */}
            {status === 'live' && (
              <circle r={s + 5} fill="none" stroke={color} strokeOpacity={0.22} strokeWidth={1.5} className="pulse-ring" />
            )}
            {isSel && (
              <circle r={s + 7} fill="none" stroke={color} strokeOpacity={0.9} strokeWidth={2} />
            )}

            {isExo ? (
              <>
                {/* Diamond body */}
                <polygon
                  points={`0,${-s} ${s},0 0,${s} ${-s},0`}
                  fill={color}
                  fillOpacity={status === 'unreachable' ? 0.35 : 0.92}
                  stroke="#a371f7"
                  strokeWidth={1.8}
                  style={{ cursor: 'pointer' }}
                />
                {/* Memory tier badge */}
                <text y={s + 9} textAnchor="middle"
                  fill="#a371f7" fontSize={6} fontWeight={700}
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
                style={{ cursor: 'pointer' }}
              />
            )}
          </Marker>
        )
      })}
    </ComposableMap>
  )
}
