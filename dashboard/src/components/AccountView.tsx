import { useState, useEffect, useCallback } from 'react'
import type { Balance, PodHealthDigest } from '../types'
import { fetchBalanceAllPods, claimStartupGrant, generateApiKey, checkApiKeyExists, revokeApiKey } from '../api'
import { getOrCreateUserId } from '../identity'

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
  // Primary coordinator — used for actions that must target one pod (claiming
  // the startup grant, managing the API key). Balance display instead queries
  // every pod in `pods` (see fetchBalanceAllPods) since ledgers aren't
  // federated yet and the primary pod may not be the one holding this wallet's
  // credits.
  coordinatorURL: string | null
  pods: PodHealthDigest[]
}

// ── API Key panel ─────────────────────────────────────────────────────────────

const API_KEY_STORAGE = 'oim_api_key'

function ApiKeyPanel({ coordinatorURL, userId }: { coordinatorURL: string | null; userId: string }) {
  const [storedKey, setStoredKey]   = useState<string | null>(() => localStorage.getItem(API_KEY_STORAGE))
  const [serverHas, setServerHas]   = useState<boolean | null>(null)
  const [generating, setGenerating] = useState(false)
  const [revoking, setRevoking]     = useState(false)
  const [copied, setCopied]         = useState(false)
  const [msg, setMsg]               = useState<string | null>(null)
  const [revealed, setRevealed]     = useState(false)

  const checkServer = useCallback(async () => {
    if (!coordinatorURL) return
    try {
      const r = await checkApiKeyExists(coordinatorURL, userId)
      setServerHas(r.exists)
      // If server no longer has a key but we have one stored, clear it
      if (!r.exists) {
        localStorage.removeItem(API_KEY_STORAGE)
        setStoredKey(null)
      }
    } catch { /* coordinator may be offline */ }
  }, [coordinatorURL, userId])

  useEffect(() => { checkServer() }, [checkServer])

  async function handleGenerate() {
    if (!coordinatorURL) return
    setGenerating(true)
    setMsg(null)
    try {
      const r = await generateApiKey(coordinatorURL, userId)
      localStorage.setItem(API_KEY_STORAGE, r.api_key)
      setStoredKey(r.api_key)
      setServerHas(true)
      setRevealed(true)
      setMsg('Key generated — copy it now, it will be masked after you leave this page.')
    } catch (e) {
      setMsg(`Error: ${(e as Error).message}`)
    } finally {
      setGenerating(false)
    }
  }

  async function handleRevoke() {
    if (!coordinatorURL) return
    setRevoking(true)
    setMsg(null)
    try {
      await revokeApiKey(coordinatorURL, userId)
      localStorage.removeItem(API_KEY_STORAGE)
      setStoredKey(null)
      setServerHas(false)
      setRevealed(false)
      setMsg('Key revoked.')
    } catch (e) {
      setMsg(`Error: ${(e as Error).message}`)
    } finally {
      setRevoking(false)
    }
  }

  async function handleCopy() {
    if (!storedKey) return
    await navigator.clipboard.writeText(storedKey)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  const displayKey = storedKey
    ? (revealed ? storedKey : storedKey.slice(0, 8) + '••••••••••••••••••••••••••••••••')
    : null

  return (
    <div style={{
      background: '#161b22', border: '1px solid #21262d',
      borderRadius: 10, padding: '20px 24px', marginBottom: 20,
    }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 16 }}>
        <div>
          <div style={{ fontWeight: 700, fontSize: 15, marginBottom: 4 }}>API Key</div>
          <div style={{ color: '#7d8590', fontSize: 12 }}>
            Use with{' '}
            <code style={{ color: '#79c0ff', background: '#1c2128', padding: '1px 5px', borderRadius: 3 }}>
              Authorization: Bearer oim_xxx
            </code>
            {' '}to submit inference jobs
          </div>
        </div>
        {serverHas !== null && (
          <span style={{
            background: serverHas ? '#3fb95018' : '#f8514918',
            border: `1px solid ${serverHas ? '#3fb95040' : '#f8514940'}`,
            color: serverHas ? '#3fb950' : '#7d8590',
            fontSize: 11, fontWeight: 700, padding: '3px 9px', borderRadius: 5,
          }}>
            {serverHas ? 'Active' : 'No key'}
          </span>
        )}
      </div>

      {displayKey && (
        <div style={{ marginBottom: 14 }}>
          <div style={{
            display: 'flex', alignItems: 'center', gap: 8,
            background: '#0d1117', border: '1px solid #30363d',
            borderRadius: 6, padding: '8px 12px',
          }}>
            <code style={{
              flex: 1, fontFamily: 'monospace', fontSize: 12,
              color: '#e6edf3', wordBreak: 'break-all', letterSpacing: '0.02em',
            }}>
              {displayKey}
            </code>
            <button onClick={() => setRevealed(v => !v)} style={microBtn}>
              {revealed ? 'Hide' : 'Show'}
            </button>
            <button onClick={handleCopy} style={{ ...microBtn, color: copied ? '#3fb950' : '#e6edf3' }}>
              {copied ? 'Copied!' : 'Copy'}
            </button>
          </div>
          {!revealed && (
            <div style={{ color: '#484f58', fontSize: 11, marginTop: 5 }}>
              Key is stored in localStorage. Click Show to reveal, or generate a new one (replaces this key).
            </div>
          )}
        </div>
      )}

      <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', marginBottom: msg ? 10 : 0 }}>
        <button
          onClick={handleGenerate}
          disabled={generating || !coordinatorURL}
          style={btnStyle('#238636', '#2ea043', generating || !coordinatorURL)}
        >
          {generating ? 'Generating…' : serverHas ? 'Regenerate key' : 'Generate API key'}
        </button>
        {serverHas && (
          <button
            onClick={handleRevoke}
            disabled={revoking}
            style={btnStyle('#f8514918', '#f8514944', revoking)}
          >
            {revoking ? 'Revoking…' : 'Revoke'}
          </button>
        )}
      </div>

      {msg && (
        <div style={{
          marginTop: 10, padding: '8px 12px', borderRadius: 6, fontSize: 12,
          background: msg.startsWith('Error') ? '#f8514918' : '#3fb95018',
          border: `1px solid ${msg.startsWith('Error') ? '#f8514940' : '#3fb95040'}`,
          color: msg.startsWith('Error') ? '#f85149' : '#3fb950',
        }}>{msg}</div>
      )}

      {!coordinatorURL && (
        <div style={{ color: '#7d8590', fontSize: 12, marginTop: 10 }}>
          Connect to a coordinator to manage API keys.
        </div>
      )}

      <div style={{ marginTop: 16, borderTop: '1px solid #21262d', paddingTop: 14 }}>
        <div style={{ color: '#7d8590', fontSize: 12, marginBottom: 8, fontWeight: 600 }}>Usage</div>
        <pre style={{
          background: '#0d1117', border: '1px solid #30363d', borderRadius: 6,
          padding: '10px 14px', fontSize: 11, color: '#e6edf3', margin: 0, overflowX: 'auto',
        }}>{`curl https://<coordinator>/v1/chat/completions \\
  -H "Authorization: Bearer ${storedKey ?? 'oim_your_key_here'}" \\
  -H "Content-Type: application/json" \\
  -d '{"model": "llama-3.2-3b", "messages": [{"role": "user", "content": "Hello"}]}'`}</pre>
        <div style={{ color: '#484f58', fontSize: 11, marginTop: 8 }}>
          Your user ID is included automatically — no X-OIM-User-ID header needed when using an API key.
        </div>
      </div>
    </div>
  )
}

const microBtn: React.CSSProperties = {
  background: '#1c2128', border: '1px solid #30363d',
  color: '#e6edf3', borderRadius: 5, padding: '3px 9px',
  cursor: 'pointer', fontSize: 11, whiteSpace: 'nowrap',
}

// ── Account View ───────────────────────────────────────────────────────────────

export function AccountView({ coordinatorURL, pods }: Props) {
  const [userId] = useState(getOrCreateUserId)
  const [balance, setBalance] = useState<Balance | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [claiming, setClaiming] = useState(false)
  const [claimMsg, setClaimMsg] = useState<string | null>(null)

  const loadBalance = useCallback(async () => {
    if (pods.length === 0) return
    setLoading(true)
    setError(null)
    try {
      setBalance(await fetchBalanceAllPods(pods, userId))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [pods, userId])

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
    <div style={{ maxWidth: 780, margin: '32px auto', padding: '0 24px' }}>

      {/* ── Identity ── */}
      <div style={{
        background: '#161b22', border: '1px solid #21262d',
        borderRadius: 10, padding: '20px 24px', marginBottom: 20,
      }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 12 }}>
          <div>
            <div style={{ fontWeight: 700, fontSize: 15, marginBottom: 4 }}>Account Identity</div>
            <div style={{ color: '#7d8590', fontSize: 12 }}>
              Anonymous · device-local UUID · used for credit accounting and API key binding
            </div>
          </div>
        </div>
        <div style={{
          background: '#0d1117', border: '1px solid #30363d',
          borderRadius: 6, padding: '10px 14px',
          fontFamily: 'monospace', fontSize: 13, color: '#e6edf3',
          wordBreak: 'break-all', letterSpacing: '0.02em', userSelect: 'all',
        }}>
          {userId}
        </div>
        <div style={{ color: '#484f58', fontSize: 11, marginTop: 8 }}>
          Stored in localStorage · pass as{' '}
          <code style={{ color: '#79c0ff' }}>--user-id {userId.slice(0, 8)}…</code>
          {' '}when running{' '}
          <code style={{ color: '#79c0ff' }}>oim node start</code>
          {' '}to attribute earnings to this account
        </div>
      </div>

      {/* ── API Key ── */}
      <ApiKeyPanel coordinatorURL={coordinatorURL} userId={userId} />

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
            {error} · {pods.length > 0 ? 'One or more coordinators may not be running' : 'No coordinator connected'}
          </div>
        )}

        <div style={{ display: 'flex', gap: 10, marginTop: 20 }}>
          <button
            onClick={loadBalance}
            disabled={loading || pods.length === 0}
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
          <InfoRow icon="🔑" text="Generate an API key above — use it as Authorization: Bearer oim_xxx when submitting jobs. Your user ID is implicit; no extra header needed." />
          <InfoRow icon="🖥" text={`Link your node to this account: oim node start --user-id ${userId.slice(0,8)}… — every token served credits this balance`} />
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
    fontSize: 13, transition: 'all 0.15s', fontWeight: 500,
  }
}
