import { useState } from 'react'
import type { NodeSnapshot } from '../types'
import {
  computeNodeStatus, statusColor, STATUS_LABELS,
  formatTps, formatMem, formatTime, nodeLabel,
} from '../utils'
import { warmModel } from '../api'
import { getOrCreateUserId } from '../identity'

// Summarizes cluster_chip_families into counted groups, e.g.
// ["Apple M1", "Apple M1", "Apple M1 Pro"] -> "2× Apple M1, 1× Apple M1 Pro".
// Coarse by design — the backend already strips hostnames and exact chip
// variants before this ever reaches the dashboard.
function summarizeChipFamilies(families: string[] | undefined): string | null {
  if (!families || families.length === 0) return null
  const counts = new Map<string, number>()
  for (const f of families) counts.set(f, (counts.get(f) ?? 0) + 1)
  return [...counts.entries()].map(([family, n]) => `${n}× ${family}`).join(', ')
}

// enclave_attested is coordinator-VERIFIED (a Secure Enclave-backed key
// actually signed a fresh challenge); has_secure_enclave is self-declared by
// the node and proves nothing on its own — surface both so a "claimed but
// unverified" node reads differently from a genuinely attested one.
function secureEnclaveMetaRow(node: NodeSnapshot): { value: string; valueColor?: string } {
  if (node.enclave_attested) return { value: '✓ Verified', valueColor: '#3fb950' }
  if (node.has_secure_enclave) return { value: 'Claimed, unverified', valueColor: '#d29922' }
  return { value: 'Not available' }
}

interface Props {
  node: NodeSnapshot
  coordinatorURL: string | null
  onClose: () => void
}

export function NodeDetail({ node, coordinatorURL, onClose }: Props) {
  const status = computeNodeStatus(node)
  const color = statusColor(status)
  const chipSummary = summarizeChipFamilies(node.cluster_chip_families)

  // Client-side only — no new server state needed. The next node-list refresh
  // (SSE/poll, already running) naturally flips a model's badge once Exo
  // actually reports it loaded; this just covers the "request is in flight"
  // moment (see plan: "Deliberately out of scope — a dedicated loading state").
  const [warmingModelIds, setWarmingModelIds] = useState<Set<string>>(new Set())
  const [warmError, setWarmError] = useState<string | null>(null)

  async function handleLoadModel(modelId: string) {
    if (!coordinatorURL || warmingModelIds.has(modelId)) return
    setWarmError(null)
    setWarmingModelIds(prev => new Set(prev).add(modelId))
    try {
      await warmModel(coordinatorURL, node.node_id, modelId, getOrCreateUserId())
    } catch (e) {
      setWarmError((e as Error).message)
    } finally {
      setWarmingModelIds(prev => {
        const next = new Set(prev)
        next.delete(modelId)
        return next
      })
    }
  }

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
      <div style={{ display: 'flex', gap: 8, marginBottom: 20, flexWrap: 'wrap' }}>
        <div style={{
          display: 'inline-flex', alignItems: 'center', gap: 7,
          background: `${color}18`, border: `1px solid ${color}44`,
          borderRadius: 6, padding: '5px 12px',
        }}>
          <div style={{ width: 8, height: 8, borderRadius: '50%', background: color }} />
          <span style={{ color, fontSize: 13, fontWeight: 600 }}>{STATUS_LABELS[status]}</span>
        </div>
        {node.simulated && (
          <div style={{
            display: 'inline-flex', alignItems: 'center',
            background: 'transparent', border: '1px dashed #7d8590',
            borderRadius: 6, padding: '5px 12px',
          }} title="Decorative seed capacity, not a real operator's node">
            <span style={{ color: '#7d8590', fontSize: 13, fontWeight: 600 }}>DEMO</span>
          </div>
        )}
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
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <div style={{ color: '#e6edf3', fontSize: 13, fontWeight: 500 }}>{m.model_id}</div>
                <span style={{
                  fontSize: 10, fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.04em',
                  padding: '2px 6px', borderRadius: 4,
                  color: m.loaded ? '#3fb950' : '#d29922',
                  background: m.loaded ? '#3fb95022' : '#d2992222',
                  border: `1px solid ${m.loaded ? '#3fb95055' : '#d2992255'}`,
                }}>
                  {m.loaded ? 'Loaded' : 'Cold'}
                </span>
                {!m.loaded && (
                  <button
                    onClick={() => handleLoadModel(m.model_id)}
                    disabled={!coordinatorURL || warmingModelIds.has(m.model_id)}
                    style={{
                      marginLeft: 'auto', fontSize: 10, fontWeight: 600,
                      padding: '2px 8px', borderRadius: 4,
                      background: '#1f6feb22', border: '1px solid #1f6feb55', color: '#58a6ff',
                      cursor: warmingModelIds.has(m.model_id) ? 'default' : 'pointer',
                      opacity: warmingModelIds.has(m.model_id) ? 0.6 : 1,
                    }}
                  >
                    {warmingModelIds.has(m.model_id) ? 'Loading…' : 'Load'}
                  </button>
                )}
              </div>
              <div style={{ color: '#7d8590', fontSize: 11, marginTop: 3 }}>
                {m.quantization} · {m.runtime} · {m.max_context_tokens.toLocaleString()} ctx
                {m.is_moe && ' · MoE'}
              </div>
            </div>
          ))}
          {warmError && (
            <div style={{ color: '#f85149', fontSize: 11, marginTop: 4 }}>
              Load failed: {warmError}
            </div>
          )}
        </div>
      )}

      {/* Metadata rows */}
      <div style={{ borderTop: '1px solid #21262d', paddingTop: 16, display: 'flex', flexDirection: 'column', gap: 12 }}>
        <MetaRow label="Secure Enclave" {...secureEnclaveMetaRow(node)} />
        <MetaRow
          label="Cluster"
          value={node.is_cluster
            ? `Yes · ${node.cluster_device_count} devices${chipSummary ? ` (${chipSummary})` : ''}`
            : 'Single device'}
        />
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
