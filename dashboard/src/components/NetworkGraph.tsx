import { useState } from 'react'
import type { NodeSnapshot } from '../types'
import {
  computeNodeStatus, statusColor, memToRadius,
  nodeLabel, formatTps, formatMem,
} from '../utils'

interface Props {
  nodes: NodeSnapshot[]
  podId: string
  region: string
  selected: NodeSnapshot | null
  onNodeClick: (node: NodeSnapshot) => void
}

const W = 560
const H = 440
const CX = W / 2
const CY = H / 2
const INNER_R = 148
const OUTER_R = 218
const INNER_CAP = 8

interface PlacedNode {
  node: NodeSnapshot
  x: number
  y: number
}

function placeNodes(nodes: NodeSnapshot[]): PlacedNode[] {
  const inner = nodes.slice(0, INNER_CAP)
  const outer = nodes.slice(INNER_CAP)

  const place = (arr: NodeSnapshot[], radius: number): PlacedNode[] =>
    arr.map((node, i) => {
      const angle = (2 * Math.PI * i / arr.length) - Math.PI / 2
      return { node, x: CX + radius * Math.cos(angle), y: CY + radius * Math.sin(angle) }
    })

  return [...place(inner, INNER_R), ...place(outer, OUTER_R)]
}

interface Tooltip { node: NodeSnapshot; clientX: number; clientY: number }

export function NetworkGraph({ nodes, podId, region, selected, onNodeClick }: Props) {
  const [tooltip, setTooltip] = useState<Tooltip | null>(null)
  const placed = placeNodes(nodes)
  const uid = `graph-${region}`

  return (
    <div style={{ position: 'relative' }}>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        style={{ width: '100%', height: 400, display: 'block', overflow: 'visible' }}
      >
        <defs>
          <pattern id={`${uid}-grid`} width={40} height={40} patternUnits="userSpaceOnUse">
            <path d="M 40 0 L 0 0 0 40" fill="none" stroke="#1a1f2e" strokeWidth={1} />
          </pattern>
          <radialGradient id={`${uid}-glow`} cx="50%" cy="50%" r="50%">
            <stop offset="0%" stopColor="#6e76ae" stopOpacity={0.25} />
            <stop offset="100%" stopColor="#6e76ae" stopOpacity={0} />
          </radialGradient>
        </defs>

        {/* Grid background */}
        <rect width={W} height={H} fill={`url(#${uid}-grid)`} />

        {/* Coordinator glow */}
        <circle cx={CX} cy={CY} r={64} fill={`url(#${uid}-glow)`} />

        {/* Ring guides */}
        {[INNER_R, OUTER_R].map(r => (
          <circle key={r} cx={CX} cy={CY} r={r}
            fill="none" stroke="#1e2535" strokeWidth={1} strokeDasharray="5 10" />
        ))}

        {/* Edges */}
        {placed.map(({ node, x, y }) => (
          <line key={`e-${node.node_id}`}
            x1={CX} y1={CY} x2={x} y2={y}
            stroke={statusColor(computeNodeStatus(node))}
            strokeOpacity={0.18} strokeWidth={1.5}
          />
        ))}

        {/* Coordinator */}
        <circle cx={CX} cy={CY} r={22} fill="#4e5a8c" stroke="#6e76ae" strokeWidth={2} />
        <text x={CX} y={CY + 1} textAnchor="middle" dominantBaseline="middle"
          fill="white" fontSize={13} fontWeight={700} style={{ pointerEvents: 'none', userSelect: 'none' }}>
          {region.toUpperCase()}
        </text>
        <text x={CX} y={CY + 35} textAnchor="middle"
          fill="#7d8590" fontSize={10} style={{ pointerEvents: 'none', userSelect: 'none' }}>
          {podId}
        </text>

        {/* Nodes */}
        {placed.map(({ node, x, y }) => {
          const status = computeNodeStatus(node)
          const color = statusColor(status)
          const r = memToRadius(node.declared_memory_gb)
          const isSelected = selected?.node_id === node.node_id
          const isStale = status === 'stale'
          const isDead = status === 'unreachable'

          return (
            <g key={node.node_id}
              style={{ cursor: 'pointer' }}
              onClick={() => onNodeClick(node)}
              onMouseEnter={e => setTooltip({ node, clientX: e.clientX, clientY: e.clientY })}
              onMouseMove={e => setTooltip(t => t ? { ...t, clientX: e.clientX, clientY: e.clientY } : null)}
              onMouseLeave={() => setTooltip(null)}
            >
              {/* Live pulse ring */}
              {status === 'live' && (
                <circle cx={x} cy={y} r={r + 6}
                  fill="none" stroke={color} strokeOpacity={0.2} strokeWidth={1.5}
                  className="pulse-ring" />
              )}
              {/* Selection ring */}
              {isSelected && (
                <circle cx={x} cy={y} r={r + 9}
                  fill="none" stroke={color} strokeOpacity={0.85} strokeWidth={2} />
              )}
              {/* Node body */}
              <circle cx={x} cy={y} r={r}
                fill={color}
                fillOpacity={isDead ? 0.2 : 0.85}
                stroke={color}
                strokeWidth={isDead || isStale ? 2 : 0}
                strokeDasharray={isStale ? '3 3' : undefined}
              />
              {/* Label below node */}
              <text x={x} y={y + r + 12} textAnchor="middle"
                fill="#7d8590" fontSize={9}
                style={{ pointerEvents: 'none', userSelect: 'none' }}>
                {nodeLabel(node)}
              </text>
            </g>
          )
        })}
      </svg>

      {/* Floating tooltip */}
      {tooltip && (
        <NodeTooltip
          node={tooltip.node}
          clientX={tooltip.clientX}
          clientY={tooltip.clientY}
        />
      )}
    </div>
  )
}

function NodeTooltip({ node, clientX, clientY }: { node: NodeSnapshot; clientX: number; clientY: number }) {
  const status = computeNodeStatus(node)
  const color = statusColor(status)

  return (
    <div style={{
      position: 'fixed',
      left: clientX + 14,
      top: clientY - 10,
      background: '#1c2128',
      border: `1px solid ${color}55`,
      borderRadius: 8,
      padding: '10px 14px',
      pointerEvents: 'none',
      zIndex: 200,
      minWidth: 170,
      boxShadow: '0 6px 24px rgba(0,0,0,0.6)',
    }}>
      <div style={{ color, fontWeight: 700, fontSize: 13, marginBottom: 6 }}>
        {nodeLabel(node)}
      </div>
      <div style={{ color: '#7d8590', fontSize: 12, lineHeight: 1.7 }}>
        <div>{formatMem(node.declared_memory_gb)} · <span style={{ color: '#e6edf3' }}>{formatTps(node.measured_toks_per_sec)}</span></div>
        {(node.models ?? []).slice(0, 2).map(m => (
          <div key={m.model_id} style={{ color: '#7d8590' }}>{m.model_id}</div>
        ))}
      </div>
    </div>
  )
}
