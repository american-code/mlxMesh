import { useState, useEffect, useCallback } from 'react'
import type { Balance } from '../types'
import { fetchBalance, claimStartupGrant } from '../api'

// ── Persistent anonymous user ID ───────────────────────────────────────────

const USER_ID_KEY = 'oim_user_id'

function getOrCreateUserId(): string {
  let id = localStorage.getItem(USER_ID_KEY)
  if (!id) {
    id = crypto.randomUUID()
    localStorage.setItem(USER_ID_KEY, id)
  }
  return id
}

// ── Gauge SVG ─────────────────────────────────────────────────────────────
// 260° sweep arc (bottom notch), two-segment: earned (green) then grant (amber)

const GCX = 110, GCY = 95, GR = 72, STROKE = 12
const START_DEG = 140, SWEEP = 260   // 140° → 400° (= 40°)

function degToRad(d: number) { return d * Math.PI / 180 }

function arcPath(cx: number, cy: number, r: number, startDeg: number, endDeg: number): string {
  const s = degToRad(startDeg), e = degToRad(endDeg)
  const x1 = cx + r * Math.cos(s), y1 = cy + r * Math.sin(s)
  const x2 = cx + r * Math.cos(e), y2 = cy + r * Math.sin(e)
  const large = (endDeg - startDeg) > 180 ? 1 : 0
  return `M ${x1.toFixed(2)} ${y1.toFixed(2)} A ${r} ${r} 0 ${large} 1 ${x2.toFixed(2)} ${y2.toFixed(2)}`
}

interface GaugeProps {
  earned: number
  grant: number
}

function CreditGauge({ earned, grant }: GaugeProps) {
  const total = earned + grant
  const earnedFrac = total > 0 ? earned / total : 0
  const grantFrac  = total > 0 ? grant  / total : 0

  const earnedEnd = START_DEG + SWEEP * earnedFrac
  const grantEnd  = earnedEnd + SWEEP * grantFrac

  return (
    <svg viewBox="0 0 220 140" style={{ width: 220, height: 140, overflow: 'visible' }}>
      {/* Track */}
      <path
        d={arcPath(GCX, GCY, GR, START_DEG, START_DEG + SWEEP)}
        fill="none" stroke="#21262d" strokeWidth={STROKE} strokeLinecap="round"
      />

      {/* Earned segment (green) */}
      {earnedFrac > 0 && (
        <path
          d={arcPath(GCX, GCY, GR, START_DEG, earnedEnd)}
          fill="none" stroke="#3fb950" strokeWidth={STROKE} strokeLinecap="round"
        />
      )}

      {/* Grant segment (amber) */}
      {grantFrac > 0 && (
        <path
          d={arcPath(GCX, GCY, GR, earnedEnd, grantEnd)}
          fill="none" stroke="#d29922" strokeWidth={STROKE} strokeLinecap="round"
        />
      )}

      {/* Center total */}
      <text x={GCX} y={GCY - 6} textAnchor="middle"
        fill="#e6edf3" fontSize={22} fontWeight={700} fontFamily="monospace">
        {total.toFixed(1)}
      </text>
      <text x={GCX} y={GCY + 12} textAnchor="middle"
        fill="#7d8590" fontSize={10} letterSpacing="0.05em">
        CREDITS
      </text>

      {/* Legend ticks */}
      <text x={GCX - GR - 8} y={GCY + GR * 0.65} textAnchor="end"
        fill="#3fb950" fontSize={8} fontWeight={600}>
        EARNED
      </text>
      <text x={GCX + GR + 8} y={GCY + GR * 0.65} textAnchor="start"
        fill="#d29922" fontSize={8} fontWeight={600}>
        GRANT
      </text>
    </svg>
  )
}

// ── Account View ───────────────────────────────────────────────────────────

interface Props {
  coordinatorURL: string | null
}

export function AccountView({ coordinatorURL }: Props) {
  const [userId] = useState(getOrCreateUserId)
  const [balance, setBalance] = useState<Balance | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [claiming, setClaiming] = useState(false)
  const [claimMsg, setClaimMsg] = useState<string | null>(null)

  const loadBalance = useCallback(async () => {
    if (!coordinatorURL) return
    setLoading(true)
    setError(null)
    try {
      setBalance(await fetchBalance(coordinatorURL, userId))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [coordinatorURL, userId])

  useEffect(() => { loadBalance() }, [loadBalance])

  async function handleClaim() {
    if (!coordinatorURL) return
    setClaiming(true)
    setClaimMsg(null)
    try {
      const result = await claimStartupGrant(coordinatorURL, userId)
      if (result.status === 'already_claimed') {
        setClaimMsg('Grant already claimed for this session')
      } else {
        setClaimMsg(`Granted ${result.amount.toFixed(2)} credits`)
      }
      await loadBalance()
    } catch (e) {
      setClaimMsg(`Error: ${(e as Error).message}`)
    } finally {
      setClaiming(false)
    }
  }

  const b = balance ?? { grant_balance: 0, earned_balance: 0, total: 0 }

  return (
    <div style={{ maxWidth: 680, margin: '32px auto', padding: '0 24px' }}>

      {/* ── Identity ── */}
      <div style={{
        background: '#161b22', border: '1px solid #21262d',
        borderRadius: 10, padding: '20px 24px', marginBottom: 20,
      }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
          <div>
            <div style={{ fontWeight: 700, fontSize: 15, marginBottom: 4 }}>Node Identity</div>
            <div style={{ color: '#7d8590', fontSize: 12 }}>
              Anonymous · proof-of-node
            </div>
          </div>
          <div style={{
            background: '#3fb95018', border: '1px solid #3fb95040',
            borderRadius: 6, padding: '3px 10px',
            color: '#3fb950', fontSize: 12, fontWeight: 600,
          }}>
            M5 Preview
          </div>
        </div>
        <div style={{
          background: '#0d1117', border: '1px solid #30363d',
          borderRadius: 6, padding: '10px 14px',
          fontFamily: 'monospace', fontSize: 12, color: '#7d8590',
          wordBreak: 'break-all', letterSpacing: '0.02em',
        }}>
          {userId}
        </div>
        <div style={{ color: '#484f58', fontSize: 11, marginTop: 8 }}>
          Stored locally · never sent to a third party · tied to your contributing node
        </div>
      </div>

      {/* ── Credit Gauge ── */}
      <div style={{
        background: '#161b22', border: '1px solid #21262d',
        borderRadius: 10, padding: '24px', marginBottom: 20,
      }}>
        <div style={{ fontWeight: 700, fontSize: 15, marginBottom: 20 }}>Credit Balance</div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 32, flexWrap: 'wrap' }}>
          <CreditGauge earned={b.earned_balance} grant={b.grant_balance} />

          <div style={{ flex: 1, minWidth: 160 }}>
            <CreditRow
              label="Earned from contribution"
              value={b.earned_balance}
              color="#3fb950"
              subtitle="From verified inference work"
            />
            <div style={{ height: 1, background: '#21262d', margin: '14px 0' }} />
            <CreditRow
              label="Startup grant"
              value={b.grant_balance}
              color="#d29922"
              subtitle="One-time bootstrap allocation"
            />
            <div style={{ height: 1, background: '#21262d', margin: '14px 0' }} />
            <CreditRow
              label="Total available"
              value={b.total}
              color="#e6edf3"
              subtitle="Spendable on inference jobs"
              bold
            />
          </div>
        </div>

        {error && (
          <div style={{
            marginTop: 16, padding: '8px 12px',
            background: '#f8514918', border: '1px solid #f8514940',
            borderRadius: 6, color: '#f85149', fontSize: 12,
          }}>
            {error} · {coordinatorURL ? 'Coordinator may not be running' : 'No coordinator connected'}
          </div>
        )}

        <div style={{ display: 'flex', gap: 10, marginTop: 20 }}>
          <button
            onClick={loadBalance}
            disabled={loading || !coordinatorURL}
            style={btnStyle('#1c2128', '#30363d', loading)}
          >
            {loading ? 'Refreshing…' : '↺ Refresh'}
          </button>
          <button
            onClick={handleClaim}
            disabled={claiming || !coordinatorURL || b.grant_balance > 0}
            style={btnStyle('#d2992218', '#d2992244', claiming || b.grant_balance > 0)}
          >
            {claiming ? 'Claiming…' : b.grant_balance > 0 ? 'Grant claimed ✓' : 'Claim startup grant'}
          </button>
          {claimMsg && (
            <span style={{ color: '#7d8590', fontSize: 12, alignSelf: 'center' }}>{claimMsg}</span>
          )}
        </div>
      </div>

      {/* ── How credits work ── */}
      <div style={{
        background: '#161b22', border: '1px solid #21262d',
        borderRadius: 10, padding: '20px 24px',
      }}>
        <div style={{ fontWeight: 700, fontSize: 14, marginBottom: 14 }}>How credits work</div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <InfoRow icon="⚡" text="Run inference on the mesh → earn credits proportional to tokens delivered and verified" />
          <InfoRow icon="🔒" text="Measurement is cryptographically signed — credits reflect actual throughput, not self-declared specs" />
          <InfoRow icon="🎁" text="Startup grant lets you use the network before you've contributed — sized to real job costs, not an arbitrary number" />
          <InfoRow icon="⚖️" text="Spend credits to submit your own jobs. No native token. No external conversion needed." />
        </div>
      </div>
    </div>
  )
}

// ── Small helpers ──────────────────────────────────────────────────────────

function CreditRow({ label, value, color, subtitle, bold }: {
  label: string; value: number; color: string; subtitle: string; bold?: boolean
}) {
  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
        <span style={{ color: '#c9d1d9', fontSize: 13, fontWeight: bold ? 700 : 400 }}>{label}</span>
        <span style={{
          color, fontSize: bold ? 18 : 15, fontWeight: 700,
          fontVariantNumeric: 'tabular-nums', fontFamily: 'monospace',
        }}>
          {value.toFixed(2)}
        </span>
      </div>
      <div style={{ color: '#484f58', fontSize: 11, marginTop: 2 }}>{subtitle}</div>
    </div>
  )
}

function InfoRow({ icon, text }: { icon: string; text: string }) {
  return (
    <div style={{ display: 'flex', gap: 10, alignItems: 'flex-start' }}>
      <span style={{ fontSize: 14, lineHeight: 1.5 }}>{icon}</span>
      <span style={{ color: '#7d8590', fontSize: 13, lineHeight: 1.5 }}>{text}</span>
    </div>
  )
}

function btnStyle(bg: string, border: string, disabled: boolean): React.CSSProperties {
  return {
    background: bg, border: `1px solid ${border}`,
    color: disabled ? '#484f58' : '#e6edf3',
    borderRadius: 6, padding: '6px 14px', cursor: disabled ? 'not-allowed' : 'pointer',
    fontSize: 13, transition: 'opacity 0.15s',
  }
}
