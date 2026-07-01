import { ComposableMap, Geographies, Geography, Marker } from 'react-simple-maps'
import type { NodeSnapshot } from '../types'
import { computeNodeStatus, statusColor } from '../utils'

const GEO_URL = 'https://cdn.jsdelivr.net/npm/world-atlas@2/countries-110m.json'

interface Props {
  nodes: NodeSnapshot[]
  selected: NodeSnapshot | null
  onNodeClick: (node: NodeSnapshot) => void
}

export function WorldMap({ nodes, selected, onNodeClick }: Props) {
  const mapped = nodes.filter(n => n.geo_lat !== 0 || n.geo_lng !== 0)

  return (
    <ComposableMap
      projection="geoNaturalEarth1"
      projectionConfig={{ scale: 153, center: [10, 25] }}
      style={{ width: '100%', height: 280, display: 'block' }}
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
                hover: { outline: 'none', fill: '#1e2535' },
                pressed: { outline: 'none' },
              }}
            />
          ))
        }
      </Geographies>

      {mapped.map(node => {
        const status = computeNodeStatus(node)
        const color = statusColor(status)
        const isSelected = selected?.node_id === node.node_id

        return (
          <Marker
            key={node.node_id}
            coordinates={[node.geo_lng, node.geo_lat]}
            onClick={() => onNodeClick(node)}
          >
            {/* Outer pulse for live nodes */}
            {status === 'live' && (
              <circle r={11} fill="none" stroke={color} strokeOpacity={0.2} strokeWidth={1.5} className="pulse-ring" />
            )}
            {/* Selection ring */}
            {isSelected && (
              <circle r={13} fill="none" stroke={color} strokeOpacity={0.9} strokeWidth={2} />
            )}
            <circle
              r={5.5}
              fill={color}
              fillOpacity={status === 'unreachable' ? 0.35 : 0.9}
              stroke="#0d1117"
              strokeWidth={1.5}
              style={{ cursor: 'pointer' }}
            />
          </Marker>
        )
      })}
    </ComposableMap>
  )
}
