import type { NodeSnapshot } from '../types'
import {
  computeNodeStatus, statusColor, STATUS_LABELS,
  formatTps, formatMem, formatTime, nodeLabel,
} from '../utils'

interface Props {
  node: NodeSnapshot
  onClose: () => void
}

export function NodeDetail({ node, onClose }: Props) {
  const status = computeNodeStatus(node)
  const color = statusColor(status)

  return (
    <div style={{
      position: 'fixed', right: 0, top: 0, bottom: 0, width: 360,
      background: '#161b22', borderLeft: '1px solid #30363d',
      padding: 24, zIndex: 100, overflowY: 'auto',
      fontFamily: 'inherit', animation: 'slideIn 0.18s ease-out',
    }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 20 }}>
        <span style={{ color: '#e6edf3', fontWeight: 700, fontSize: 15 }}>
          {nodeLabel(node)}
        </span>
        <button onClick={onClose} style={{
          background: 'none', border: 'none', color: '#7d8590',
          cursor: 'pointer', fontSize: 22, lineHeight: 1, padding: '0 4px',
        }}>×</button>
      </div>

      {/* Status badge */}
      <div style={{
        display: 'inline-flex', alignItems: 'center', gap: 7,
        background: `${color}18`, border: `1px solid ${color}44`,
        borderRadius: 6, padding: '5px 12px', marginBottom: 20,
      }}>
        <div style={{ width: 8, height: 8, borderRadius: '50%', background: color }} />
        <span style={{ color, fontSize: 13, fontWeight: 600 }}>{STATUS_LABELS[status]}</span>
      </div>

      {/* Stat grid */}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10, marginBottom: 20 }}>
        <StatBox label="Declared Mem" value={formatMem(node.declared_memory_gb)} />
        <StatBox label="Committed" value={formatMem(node.committed_memory_gb)} />
        <StatBox label="Throughput" value={formatTps(node.measured_toks_per_sec)} color={color} />
        <StatBox label="Region" value={node.geographic_hint.toUpperCase()} />
        <StatBox label="Lat" value={node.geo_lat.toFixed(3)} />
        <StatBox label="Lng" value={node.geo_lng.toFixed(3)} />
      </div>

      {/* Models */}
      {(node.models ?? []).length > 0 && (
        <div style={{ marginBottom: 20 }}>
          <div style={{ color: '#7d8590', fontSize: 11, textTransform: 'uppercase', letterSpacing: '0.06em', marginBottom: 8 }}>
            Models
          </div>
          {node.models.map(m => (
            <div key={m.model_id} style={{
              background: '#1c2128', border: '1px solid #30363d',
              borderRadius: 6, padding: '9px 12px', marginBottom: 6,
            }}>
              <div style={{ color: '#e6edf3', fontSize: 13, fontWeight: 500 }}>{m.model_id}</div>
              <div style={{ color: '#7d8590', fontSize: 11, marginTop: 3 }}>
                {m.quantization} · {m.runtime} · {m.max_context_tokens.toLocaleString()} ctx
                {m.is_moe && ' · MoE'}
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Metadata rows */}
      <div style={{ borderTop: '1px solid #21262d', paddingTop: 16, display: 'flex', flexDirection: 'column', gap: 12 }}>
        <MetaRow label="Secure Enclave" value={node.has_secure_enclave ? '✓ Available' : 'Not available'} valueColor={node.has_secure_enclave ? '#3fb950' : undefined} />
        <MetaRow label="Cluster" value={node.is_cluster ? `Yes · ${node.cluster_device_count} devices` : 'Single device'} />
        <MetaRow label="Last seen" value={formatTime(node.last_seen_at)} />
        <MetaRow label="Endpoint" value={node.reachability_endpoint} mono />
        <MetaRow label="Node ID" value={node.node_id} mono small />
      </div>
    </div>
  )
}

function StatBox({ label, value, color }: { label: string; value: string; color?: string }) {
  return (
    <div style={{
      background: '#1c2128', border: '1px solid #30363d',
      borderRadius: 8, padding: '10px 12px',
    }}>
      <div style={{ color: '#7d8590', fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.06em', marginBottom: 5 }}>
        {label}
      </div>
      <div style={{ color: color ?? '#e6edf3', fontSize: 19, fontWeight: 700, fontVariantNumeric: 'tabular-nums' }}>
        {value}
      </div>
    </div>
  )
}

function MetaRow({ label, value, mono, small, valueColor }: {
  label: string; value: string; mono?: boolean; small?: boolean; valueColor?: string
}) {
  return (
    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 12, alignItems: 'flex-start' }}>
      <span style={{ color: '#7d8590', fontSize: 12, flexShrink: 0 }}>{label}</span>
      <span style={{
        color: valueColor ?? '#e6edf3',
        fontSize: small ? 10 : mono ? 11 : 13,
        fontFamily: mono ? 'ui-monospace, monospace' : 'inherit',
        wordBreak: 'break-all', textAlign: 'right',
      }}>{value}</span>
    </div>
  )
}
