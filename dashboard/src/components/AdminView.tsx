import { useState, useEffect, useCallback } from 'react'
import type { NodeSnapshot, Balance, ReconciliationReport, AdminAction } from '../types'
import {
  requestAdminChallenge, authenticateAdmin, fetchTreasuryBalance,
  fetchReconcileReport, fetchAuditLog, postTreasuryCredit, deregisterNode,
} from '../api'
import { signChallenge, getAdminSessionToken, setAdminSessionToken, clearAdminSessionToken } from '../adminAuth'

interface AdminViewProps {
  coordinatorURL: string | null
  nodes: NodeSnapshot[]
  onNodesChanged?: () => void
}

export function AdminView({ coordinatorURL, nodes, onNodesChanged }: AdminViewProps) {
  const [sessionToken, setSessionToken] = useState<string | null>(() => getAdminSessionToken())
  // Bumped after a successful treasury credit so the reconcile/audit-log
  // panels — each fetched independently on mount — pick up the new totals
  // and audit row without waiting for a full page reload.
  const [dataVersion, setDataVersion] = useState(0)

  const signOut = useCallback(() => {
    clearAdminSessionToken()
    setSessionToken(null)
  }, [])

  if (!coordinatorURL) {
    return (
      <div style={{ padding: '40px 24px', textAlign: 'center', color: '#7d8590' }}>
        No coordinator connected yet.
      </div>
    )
  }

  if (!sessionToken) {
    return (
      <div style={{ maxWidth: 480, margin: '60px auto', padding: '0 24px' }}>
        <LoginForm
          coordinatorURL={coordinatorURL}
          onAuthenticated={token => { setAdminSessionToken(token); setSessionToken(token) }}
        />
      </div>
    )
  }

  return (
    <div style={{ maxWidth: 1000, margin: '0 auto', padding: '20px 24px', display: 'flex', flexDirection: 'column', gap: 16 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span style={{ fontWeight: 700, fontSize: 15 }}>Admin panel</span>
        <button onClick={signOut} style={{
          background: '#1c2128', border: '1px solid #30363d', color: '#7d8590',
          borderRadius: 6, padding: '5px 14px', cursor: 'pointer', fontSize: 12,
        }}>
          Sign out
        </button>
      </div>

      <TreasuryPanel
        coordinatorURL={coordinatorURL}
        sessionToken={sessionToken}
        onAuthExpired={signOut}
        onCredited={() => setDataVersion(v => v + 1)}
      />
      <ReconcilePanel coordinatorURL={coordinatorURL} sessionToken={sessionToken} onAuthExpired={signOut} dataVersion={dataVersion} />
      <AuditLogPanel coordinatorURL={coordinatorURL} sessionToken={sessionToken} onAuthExpired={signOut} dataVersion={dataVersion} />
      <NodeManagementPanel
        coordinatorURL={coordinatorURL}
        sessionToken={sessionToken}
        nodes={nodes}
        onAuthExpired={signOut}
        onNodesChanged={onNodesChanged}
      />
    </div>
  )
}

// ── Login ────────────────────────────────────────────────────────────────

function LoginForm({
  coordinatorURL, onAuthenticated,
}: { coordinatorURL: string; onAuthenticated: (token: string) => void }) {
  const [privateKey, setPrivateKey] = useState('')
  const [signingIn, setSigningIn] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSignIn() {
    if (!privateKey.trim() || signingIn) return
    setSigningIn(true)
    setError(null)
    try {
      const { nonce } = await requestAdminChallenge(coordinatorURL)
      const signature = await signChallenge(privateKey, nonce)
      const { session_token } = await authenticateAdmin(coordinatorURL, nonce, signature)
      onAuthenticated(session_token)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      // The pasted key only ever lived in this component's state — clear it
      // regardless of outcome so it doesn't linger in memory longer than needed.
      setPrivateKey('')
      setSigningIn(false)
    }
  }

  return (
    <div style={{
      background: '#161b22', border: '1px solid #21262d', borderRadius: 10,
      padding: '24px 28px', display: 'flex', flexDirection: 'column', gap: 12,
    }}>
      <div>
        <div style={{ fontWeight: 700, fontSize: 15, marginBottom: 4 }}>Admin sign-in</div>
        <div style={{ color: '#7d8590', fontSize: 12, lineHeight: 1.5 }}>
          Paste the BDFL private key printed by <code>oim admin keygen</code>. It signs the
          coordinator's challenge in this tab and is never stored or transmitted — only the
          signature is sent.
        </div>
      </div>
      <input
        type="password"
        value={privateKey}
        onChange={e => setPrivateKey(e.target.value)}
        onKeyDown={e => { if (e.key === 'Enter') handleSignIn() }}
        placeholder="Private key (hex)"
        autoComplete="off"
        style={{
          background: '#0d1117', border: '1px solid #30363d', borderRadius: 6,
          padding: '8px 12px', color: '#e6edf3', fontSize: 13, fontFamily: 'monospace',
        }}
      />
      <button
        onClick={handleSignIn}
        disabled={!privateKey.trim() || signingIn}
        style={{
          background: '#238636', border: '1px solid #2ea043', color: '#e6edf3',
          borderRadius: 6, padding: '8px 16px', cursor: signingIn ? 'not-allowed' : 'pointer',
          fontSize: 13, fontWeight: 600,
        }}
      >
        {signingIn ? 'Signing in…' : 'Sign in'}
      </button>
      {error && <span style={{ color: '#f85149', fontSize: 12 }}>{error}</span>}
    </div>
  )
}

// ── Shared panel chrome ──────────────────────────────────────────────────

function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ background: '#161b22', border: '1px solid #21262d', borderRadius: 10, overflow: 'hidden' }}>
      <div style={{
        padding: '11px 16px', borderBottom: '1px solid #21262d',
        fontWeight: 600, fontSize: 13,
      }}>
        {title}
      </div>
      <div style={{ padding: '16px 20px' }}>{children}</div>
    </div>
  )
}

// isAuthError distinguishes an expired/invalid session (which should bounce
// back to the login form) from any other fetch failure (network blip,
// validation error) — those should just show inline, not sign the operator out.
function isAuthError(e: unknown): boolean {
  const msg = (e as Error).message ?? ''
  return msg.includes('401')
}

// ── Treasury ─────────────────────────────────────────────────────────────

function TreasuryPanel({
  coordinatorURL, sessionToken, onAuthExpired, onCredited,
}: { coordinatorURL: string; sessionToken: string; onAuthExpired: () => void; onCredited: () => void }) {
  const [balance, setBalance] = useState<Balance | null>(null)
  const [amount, setAmount] = useState('')
  const [reason, setReason] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setBalance(await fetchTreasuryBalance(coordinatorURL, sessionToken))
    } catch (e) {
      if (isAuthError(e)) onAuthExpired()
    }
  }, [coordinatorURL, sessionToken, onAuthExpired])

  useEffect(() => { load() }, [load])

  async function handleCredit() {
    const amt = parseFloat(amount)
    if (!amt || amt <= 0 || !reason.trim() || submitting) return
    setSubmitting(true)
    setError(null)
    try {
      const updated = await postTreasuryCredit(coordinatorURL, sessionToken, amt, reason.trim())
      setBalance(updated)
      setAmount('')
      setReason('')
      onCredited()
    } catch (e) {
      if (isAuthError(e)) { onAuthExpired(); return }
      setError((e as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Panel title="Treasury">
      <div style={{ display: 'flex', gap: 28, marginBottom: 16 }}>
        <Stat label="Total" value={balance ? balance.total.toFixed(4) : '—'} />
        <Stat label="Grant" value={balance ? balance.grant_balance.toFixed(4) : '—'} />
        <Stat label="Earned" value={balance ? balance.earned_balance.toFixed(4) : '—'} />
      </div>
      <div style={{ display: 'flex', gap: 8 }}>
        <input
          value={amount}
          onChange={e => setAmount(e.target.value)}
          placeholder="Amount"
          type="number"
          style={{
            width: 120, background: '#0d1117', border: '1px solid #30363d', borderRadius: 6,
            padding: '7px 10px', color: '#e6edf3', fontSize: 13,
          }}
        />
        <input
          value={reason}
          onChange={e => setReason(e.target.value)}
          placeholder="Reason (required, audited)"
          style={{
            flex: 1, background: '#0d1117', border: '1px solid #30363d', borderRadius: 6,
            padding: '7px 10px', color: '#e6edf3', fontSize: 13,
          }}
        />
        <button
          onClick={handleCredit}
          disabled={!amount || parseFloat(amount) <= 0 || !reason.trim() || submitting}
          style={{
            background: '#238636', border: '1px solid #2ea043', color: '#e6edf3',
            borderRadius: 6, padding: '7px 16px', cursor: submitting ? 'not-allowed' : 'pointer',
            fontSize: 13, fontWeight: 600, whiteSpace: 'nowrap',
          }}
        >
          {submitting ? 'Crediting…' : 'Credit treasury'}
        </button>
      </div>
      {error && <div style={{ color: '#f85149', fontSize: 12, marginTop: 8 }}>{error}</div>}
    </Panel>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
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

// ── Reconciliation ───────────────────────────────────────────────────────

function ReconcilePanel({
  coordinatorURL, sessionToken, onAuthExpired, dataVersion,
}: { coordinatorURL: string; sessionToken: string; onAuthExpired: () => void; dataVersion: number }) {
  const [report, setReport] = useState<ReconciliationReport | null>(null)

  useEffect(() => {
    fetchReconcileReport(coordinatorURL, sessionToken)
      .then(setReport)
      .catch(e => { if (isAuthError(e)) onAuthExpired() })
  }, [coordinatorURL, sessionToken, onAuthExpired, dataVersion])

  if (!report) return <Panel title="Ledger reconciliation">Loading…</Panel>

  const color = report.consistent ? '#3fb950' : '#f85149'
  return (
    <Panel title="Ledger reconciliation">
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 16 }}>
        <div style={{
          background: `${color}18`, border: `1px solid ${color}44`, borderRadius: 6,
          padding: '4px 10px', color, fontSize: 13, fontWeight: 700,
        }}>
          {report.consistent ? 'Consistent' : `${report.anomalies?.length ?? 0} anomalies`}
        </div>
        <span style={{ color: '#7d8590', fontSize: 12 }}>{report.user_count} accounts</span>
      </div>
      <div style={{ display: 'flex', gap: 28, marginBottom: report.anomalies?.length ? 16 : 0 }}>
        <Stat label="Total credits" value={report.total_credits.toFixed(4)} />
        <Stat label="Total debits" value={report.total_debits.toFixed(4)} />
        <Stat label="Outstanding" value={report.total_outstanding.toFixed(4)} />
      </div>
      {report.anomalies && report.anomalies.length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          {report.anomalies.map(a => (
            <div key={a.user_id} style={{
              background: '#0d1117', border: '1px solid #f8514940', borderRadius: 6,
              padding: '8px 12px', fontSize: 12,
            }}>
              <span style={{ color: '#f85149', fontWeight: 600 }}>{a.kind}</span>
              {' — '}
              <span style={{ fontFamily: 'monospace', color: '#e6edf3' }}>{a.user_id}</span>
              {': '}<span style={{ color: '#7d8590' }}>{a.detail}</span>
            </div>
          ))}
        </div>
      )}
    </Panel>
  )
}

// ── Audit log ────────────────────────────────────────────────────────────

function AuditLogPanel({
  coordinatorURL, sessionToken, onAuthExpired, dataVersion,
}: { coordinatorURL: string; sessionToken: string; onAuthExpired: () => void; dataVersion: number }) {
  const [actions, setActions] = useState<AdminAction[] | null>(null)

  useEffect(() => {
    fetchAuditLog(coordinatorURL, sessionToken, 50)
      .then(setActions)
      .catch(e => { if (isAuthError(e)) onAuthExpired() })
  }, [coordinatorURL, sessionToken, onAuthExpired, dataVersion])

  return (
    <Panel title="Audit log">
      {!actions || actions.length === 0 ? (
        <div style={{ color: '#7d8590', fontSize: 12 }}>No admin actions recorded yet.</div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          {actions.map((a, i) => (
            <div key={i} style={{
              display: 'flex', justifyContent: 'space-between', alignItems: 'center',
              background: '#0d1117', border: '1px solid #21262d', borderRadius: 6,
              padding: '8px 12px', fontSize: 12,
            }}>
              <div>
                <span style={{ color: '#e6edf3', fontWeight: 600 }}>{a.action}</span>
                <span style={{ color: '#7d8590' }}> · {a.detail}</span>
              </div>
              <div style={{ display: 'flex', gap: 14, alignItems: 'center' }}>
                <span style={{ color: '#3fb950', fontVariantNumeric: 'tabular-nums' }}>+{a.amount}</span>
                <span style={{ color: '#7d8590', fontVariantNumeric: 'tabular-nums' }}>
                  {new Date(a.performed_at).toLocaleString()}
                </span>
              </div>
            </div>
          ))}
        </div>
      )}
    </Panel>
  )
}

// ── Node management ──────────────────────────────────────────────────────

function NodeManagementPanel({
  coordinatorURL, sessionToken, nodes, onAuthExpired, onNodesChanged,
}: {
  coordinatorURL: string
  sessionToken: string
  nodes: NodeSnapshot[]
  onAuthExpired: () => void
  onNodesChanged?: () => void
}) {
  const [pending, setPending] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  async function handleDeregister(nodeId: string) {
    setPending(nodeId)
    setError(null)
    try {
      await deregisterNode(coordinatorURL, sessionToken, nodeId)
      onNodesChanged?.()
    } catch (e) {
      if (isAuthError(e)) { onAuthExpired(); return }
      setError((e as Error).message)
    } finally {
      setPending(null)
    }
  }

  return (
    <Panel title={`Nodes (${nodes.length})`}>
      {error && <div style={{ color: '#f85149', fontSize: 12, marginBottom: 10 }}>{error}</div>}
      {nodes.length === 0 ? (
        <div style={{ color: '#7d8590', fontSize: 12 }}>No nodes registered.</div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          {nodes.map(n => (
            <div key={n.node_id} style={{
              display: 'flex', justifyContent: 'space-between', alignItems: 'center',
              background: '#0d1117', border: '1px solid #21262d', borderRadius: 6,
              padding: '8px 12px', fontSize: 12,
            }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0 }}>
                <span style={{ fontFamily: 'monospace', color: '#e6edf3' }}>{n.node_id.slice(0, 16)}…</span>
                <span style={{ color: '#7d8590' }}>{n.status} · {n.geographic_hint || '—'}</span>
              </div>
              <button
                onClick={() => handleDeregister(n.node_id)}
                disabled={pending === n.node_id}
                style={{
                  background: '#21262d', border: '1px solid #f8514944', color: '#f85149',
                  borderRadius: 6, padding: '3px 10px', cursor: pending === n.node_id ? 'not-allowed' : 'pointer',
                  fontSize: 11, fontWeight: 600,
                }}
              >
                {pending === n.node_id ? 'Deregistering…' : 'Deregister'}
              </button>
            </div>
          ))}
        </div>
      )}
    </Panel>
  )
}
