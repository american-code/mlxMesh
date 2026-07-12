import { ComposableMap, Geographies, Geography, Marker, Line } from 'react-simple-maps'
import type { NodeSnapshot } from '../types'
import { computeNodeStatus, statusColor, isExoNode, worseStatus, worstStatus, groupMarkers } from '../utils'

const GEO_URL = 'https://cdn.jsdelivr.net/npm/world-atlas@2/countries-110m.json'

// Auto-fit projection to the actual node bounding box.
// Scale formula: empirically, scale=360 fits ~143° of longitude in 1080px.
// Extends naturally: wider data → lower scale → zooms out to full world.
function autoProjection(points: { geo_lat: number; geo_lng: number }[]): { scale: number; center: [number, number] } {
  const geo = points.filter(n => n.geo_lat !== 0 || n.geo_lng !== 0)
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

// ActiveRoute is rendered on top of the static topology when a query the
// current browser session submitted gets a response. lane controls animation
// speed/color — the visible distinction between fast and background traffic.
// key must change per-request so React remounts the element and restarts the
// travel/fade animation instead of reusing a finished one.
export interface ActiveRoute {
  key: string
  fromLat: number
  fromLng: number
  toLat: number
  toLng: number
  lane: 'fast' | 'background'
}

export interface UserLocation {
  lat: number
  lng: number
}

interface Props {
  nodes: NodeSnapshot[]
  selected: NodeSnapshot | null
  onNodeClick: (node: NodeSnapshot) => void
  userLocation?: UserLocation | null
  activeRoute?: ActiveRoute | null
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
  return statusColor(worseStatus(computeNodeStatus(a), computeNodeStatus(b)))
}

// ── Component ──────────────────────────────────────────────────────────────

export function WorldMap({ nodes, selected, onNodeClick, userLocation, activeRoute }: Props) {
  const mapped = nodes.filter(n => n.geo_lat !== 0 || n.geo_lng !== 0)
  const markerGroups = groupMarkers(mapped)
  // One representative node per merged marker for KNN edges/projection bounds
  // — otherwise every device in a cluster draws its own near-duplicate set of
  // proximity edges, stacked on top of each other exactly like the markers
  // were before groupMarkers.
  const deduped = markerGroups.map(g => g.nodes[0])
  const edges  = buildKnnEdges(deduped, 3)
  const projectionPoints = userLocation
    ? [...deduped, { geo_lat: userLocation.lat, geo_lng: userLocation.lng }]
    : deduped
  const { scale, center } = autoProjection(projectionPoints)

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

      {/* "You" marker — the current browser's approximate location, shown only
          when geolocation was granted. Never represents any other user. */}
      {userLocation && (
        <Marker coordinates={[userLocation.lng, userLocation.lat]}>
          <circle r={9} fill="none" stroke="#e6edf3" strokeOpacity={0.5} strokeWidth={1.5} className="you-marker" />
          <circle r={4} fill="#e6edf3" stroke="#0d1117" strokeWidth={1.2} />
          <text y={-13} textAnchor="middle" fill="#e6edf3" fontSize={8} fontWeight={700}
            style={{ pointerEvents: 'none', userSelect: 'none' }}>
            YOU
          </text>
        </Marker>
      )}

      {/* Node markers — grouped so a multi-device cluster (every device
          registers its own node entry at the SAME location) draws one marker
          with a device-count badge instead of N overlapping circles. */}
      {markerGroups.map(group => {
        const node     = group.nodes[0] // representative for click/detail
        const status   = worstStatus(group.nodes)
        const color    = statusColor(status)
        const isSel    = group.nodes.some(n => selected?.node_id === n.node_id)
        const isExo    = isExoNode(node)
        const s        = isExo ? 7 : 5  // diamond half-size / circle radius
        const count    = node.is_cluster ? (node.cluster_device_count ?? group.nodes.length) : 1

        return (
          <Marker
            key={group.key}
            coordinates={[group.lng, group.lat]}
            onClick={() => onNodeClick(node)}
          >
            {/* Pulse ring — always a circle for animation compatibility */}
            {status === 'live' && (
              <circle r={s + 5} fill="none" stroke={color} strokeOpacity={0.22} strokeWidth={1.5} className="pulse-ring" />
            )}
            {isSel && (
              <circle r={s + 7} fill="none" stroke={color} strokeOpacity={0.9} strokeWidth={2} />
            )}
            {/* Dashed ring — decorative/seed capacity, not a real operator's node */}
            {node.simulated && (
              <circle r={s + 4} fill="none" stroke="#7d8590" strokeOpacity={0.8} strokeWidth={1} strokeDasharray="2,2" />
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
            {/* Device-count badge — only shown when this marker represents
                more than one merged cluster-member registration. */}
            {count > 1 && (
              <text y={-(s + 8)} textAnchor="middle"
                fill="#e6edf3" fontSize={7} fontWeight={700}
                style={{ pointerEvents: 'none', userSelect: 'none' }}>
                ×{count}
              </text>
            )}
          </Marker>
        )
      })}

      {/* Active route — drawn last so it renders on top of everything else.
          Shows THIS browser's own most recent query only — never anyone else's
          traffic; the coordinator only tells a requester which node served ITS
          OWN request (privacy split, proposal §7.1). Fast lane travels quickly
          and fades fast; background lane travels slowly and lingers — that
          speed difference is the visible fast-vs-background distinction, since
          any node can serve either lane. Glow layer underneath for visibility
          against the dense static topology. */}
      {activeRoute && (
        <>
          <Line
            from={[activeRoute.fromLng, activeRoute.fromLat]}
            to={[activeRoute.toLng, activeRoute.toLat]}
            stroke={activeRoute.lane === 'background' ? '#d29922' : '#58a6ff'}
            strokeWidth={6}
            strokeOpacity={0.25}
            strokeLinecap="round"
          />
          <Line
            key={activeRoute.key}
            from={[activeRoute.fromLng, activeRoute.fromLat]}
            to={[activeRoute.toLng, activeRoute.toLat]}
            stroke={activeRoute.lane === 'background' ? '#d29922' : '#58a6ff'}
            strokeWidth={2.6}
            strokeLinecap="round"
            strokeDasharray="6 6"
            className={activeRoute.lane === 'background' ? 'route-background' : 'route-fast'}
          />
        </>
      )}
    </ComposableMap>
  )
}
