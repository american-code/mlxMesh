import { useState, useEffect, useCallback } from 'react'
import type { NodeDetection, NodeConfig, Schedule, DeviceStat } from '../types'
import { DEFAULT_SCHEDULE } from '../types'
import {
  fetchLocalDetect, saveLocalConfig,
  getLocalAgentURL, setLocalAgentURL, resetLocalAgentURL, DEFAULT_LOCAL_AGENT_URL,
} from '../api'

const WEEKDAYS: Array<{ key: string; label: string }> = [
  { key: 'mon', label: 'M' }, { key: 'tue', label: 'T' }, { key: 'wed', label: 'W' },
  { key: 'thu', label: 'T' }, { key: 'fri', label: 'F' }, { key: 'sat', label: 'S' }, { key: 'sun', label: 'S' },
]

// ── Helpers ───────────────────────────────────────────────────────────────────

function extractModelId(m: { id?: string; model_id?: string; [key: string]: unknown }): string {
  return (m.id ?? m.model_id ?? '') as string
}

function ramBar(used: number, total: number, committed: number) {
  const usedPct  = total > 0 ? (used / total) * 100 : 0
  const commPct  = total > 0 ? (committed / total) * 100 : 0
  return { usedPct, commPct }
}

// ── Not-running onboarding screen ────────────────────────────────────────────

function GettingStarted() {
  return (
    <div style={{ maxWidth: 760, margin: '40px auto', padding: '0 24px' }}>

      <div style={{ marginBottom: 32 }}>
        <div style={{ fontSize: 22, fontWeight: 700, marginBottom: 8 }}>
          Contribute your hardware to the mesh
        </div>
        <div style={{ color: '#7d8590', fontSize: 14, lineHeight: 1.6 }}>
          MeshAI routes background inference jobs to available nodes on the network.
          Your machine earns credits for every token it delivers.
        </div>
      </div>

      {/* Two-path cards */}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16, marginBottom: 32 }}>
        <RoleCard
          title="Using the network"
          badge="No setup required"
          badgeColor="#3fb950"
          items={[
            'Submit jobs via the OpenAI-compatible API',
            'No local software beyond an API key',
            'Credits purchased or earned from contribution',
          ]}
          cta={null}
        />
        <RoleCard
          title="Contributing a node"
          badge="Exo required"
          badgeColor="#d29922"
          items={[
            'Run Exo locally — it downloads and serves models',
            'Run oim node start to join the mesh',
            'Earn credits proportional to tokens delivered',
          ]}
          cta="Setup steps below"
        />
      </div>

      {/* Setup steps */}
      <div style={{
        background: '#161b22', border: '1px solid #21262d',
        borderRadius: 10, padding: '20px 24px', marginBottom: 20,
      }}>
        <div style={{ fontWeight: 700, fontSize: 14, marginBottom: 16 }}>
          Get started as a node contributor
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <Step n={1} title="Install Exo">
            <span style={{ color: '#7d8590', fontSize: 13 }}>
              Exo is the local model runner. Download a model via{' '}
              <code style={{ color: '#79c0ff', background: '#1c2128', padding: '1px 5px', borderRadius: 3 }}>
                exo run llama-3.2-3b
              </code>{' '}
              and keep it running.
            </span>
          </Step>
          <Step n={2} title="Start the node agent">
            <CodeBlock>oim node start --coordinator http://&lt;pod-url&gt; --region us</CodeBlock>
            <div style={{ color: '#7d8590', fontSize: 12, marginTop: 6 }}>
              The agent auto-detects your RAM, reads Exo's downloaded models, and registers with the pod coordinator.
            </div>
          </Step>
          <Step n={3} title="Return here to configure">
            <div style={{ color: '#7d8590', fontSize: 13 }}>
              Once the agent is running, this page auto-populates your machine specs and lets you control which models and how much memory to share.
            </div>
          </Step>
        </div>
      </div>

      <div style={{
        background: '#161b22', border: '1px solid #21262d',
        borderRadius: 10, padding: '16px 20px',
        display: 'flex', alignItems: 'flex-start', gap: 12,
      }}>
        <span style={{ fontSize: 18, lineHeight: 1 }}>ℹ</span>
        <div style={{ color: '#7d8590', fontSize: 13, lineHeight: 1.6 }}>
          <strong style={{ color: '#c9d1d9' }}>Why Exo?</strong> At Milestone 1, oim wraps Exo as its local inference backend.
          Future milestones will support Ollama, llama.cpp, and other backends — Exo will not be required indefinitely.
          If you are only submitting jobs via the API, you do not need Exo at all.
        </div>
      </div>
    </div>
  )
}

function RoleCard({ title, badge, badgeColor, items, cta }: {
  title: string
  badge: string
  badgeColor: string
  items: string[]
  cta: string | null
}) {
  return (
    <div style={{
      background: '#161b22', border: '1px solid #21262d',
      borderRadius: 10, padding: '20px 20px',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14 }}>
        <span style={{ fontWeight: 700, fontSize: 14 }}>{title}</span>
        <span style={{
          background: `${badgeColor}18`, border: `1px solid ${badgeColor}44`,
          color: badgeColor, fontSize: 10, fontWeight: 700,
          padding: '2px 7px', borderRadius: 4,
        }}>{badge}</span>
      </div>
      <ul style={{ margin: 0, padding: '0 0 0 16px', display: 'flex', flexDirection: 'column', gap: 7 }}>
        {items.map((item, i) => (
          <li key={i} style={{ color: '#7d8590', fontSize: 13, lineHeight: 1.5 }}>{item}</li>
        ))}
      </ul>
      {cta && (
        <div style={{ color: '#79c0ff', fontSize: 12, marginTop: 12 }}>↓ {cta}</div>
      )}
    </div>
  )
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', gap: 14 }}>
      <div style={{
        width: 24, height: 24, borderRadius: '50%', flexShrink: 0,
        background: '#2d333b', border: '1px solid #30363d',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        color: '#79c0ff', fontSize: 11, fontWeight: 700,
      }}>{n}</div>
      <div>
        <div style={{ fontWeight: 600, fontSize: 13, marginBottom: 5 }}>{title}</div>
        {children}
      </div>
    </div>
  )
}

function CodeBlock({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      background: '#0d1117', border: '1px solid #30363d', borderRadius: 6,
      padding: '8px 12px', fontFamily: 'monospace', fontSize: 12,
      color: '#e6edf3', userSelect: 'all',
    }}>{children}</div>
  )
}

// ── Main config view ──────────────────────────────────────────────────────────

export function NodeSetupView() {
  const [detection, setDetection] = useState<NodeDetection | null>(null)
  const [loading, setLoading]   = useState(true)
  const [agentRunning, setAgentRunning] = useState(false)

  // Form state — synced from detection.config on load
  const [capPct, setCapPct]     = useState(50)
  const [region, setRegion]     = useState('us')
  const [coordinator, setCoordinator] = useState('')
  const [reach, setReach]       = useState('')
  const [allowedModels, setAllowedModels] = useState<Set<string>>(new Set())
  const [sensitivityCap, setSensitivityCap] = useState('moderate')
  const [schedule, setSchedule] = useState<Schedule>(DEFAULT_SCHEDULE)

  const [saving, setSaving]     = useState(false)
  const [saveMsg, setSaveMsg]   = useState<string | null>(null)

  const [agentUrlDraft, setAgentUrlDraft] = useState(getLocalAgentURL())

  const detect = useCallback(async () => {
    setLoading(true)
    setSaveMsg(null)
    try {
      const d = await fetchLocalDetect()
      setDetection(d)
      setAgentRunning(true)
      // Populate form from saved config
      const c = d.config
      setCapPct(Math.round((c.memory_cap_pct ?? 0.5) * 100))
      setRegion(c.geographic_hint || 'us')
      setCoordinator(c.pod_endpoint || '')
      setReach(c.reachability_endpoint || '')
      setSensitivityCap(c.sensitivity_cap || 'moderate')
      if (c.schedule && c.schedule.mode) {
        setSchedule({ ...DEFAULT_SCHEDULE, ...c.schedule })
      }
      // Default: all models allowed (empty allowlist = all)
      const modelIds = (d.models ?? []).map(extractModelId).filter(Boolean)
      if (c.allowed_models && c.allowed_models.length > 0) {
        setAllowedModels(new Set(c.allowed_models))
      } else {
        setAllowedModels(new Set(modelIds))
      }
    } catch {
      setAgentRunning(false)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { detect() }, [detect])

  async function handleSave() {
    if (!detection) return
    setSaving(true)
    setSaveMsg(null)
    const cfg: NodeConfig = {
      exo_url: detection.exo_url,
      memory_cap_pct: capPct / 100,
      geographic_hint: region,
      reachability_endpoint: reach,
      pod_endpoint: coordinator,
      allowed_models: [...allowedModels],
      sensitivity_cap: sensitivityCap,
      schedule,
    }
    try {
      const r = await saveLocalConfig(cfg)
      setSaveMsg(`Saved to ${r.path}`)
    } catch (e) {
      setSaveMsg(`Error: ${(e as Error).message}`)
    } finally {
      setSaving(false)
    }
  }

  function toggleDay(day: string) {
    setSchedule(prev => {
      const has = prev.days.includes(day)
      return { ...prev, days: has ? prev.days.filter(d => d !== day) : [...prev.days, day] }
    })
  }

  function toggleModel(id: string) {
    setAllowedModels(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function handleAgentUrlApply() {
    setLocalAgentURL(agentUrlDraft)
    detect()
  }

  function handleAgentUrlReset() {
    resetLocalAgentURL()
    setAgentUrlDraft(DEFAULT_LOCAL_AGENT_URL)
    detect()
  }

  const agentUrlBar = (
    <LocalAgentBar
      value={agentUrlDraft}
      onChange={setAgentUrlDraft}
      onApply={handleAgentUrlApply}
      onReset={handleAgentUrlReset}
      isDefault={agentUrlDraft === DEFAULT_LOCAL_AGENT_URL}
    />
  )

  if (loading) {
    return (
      <div style={{ maxWidth: 1100, margin: '24px auto', padding: '0 24px' }}>
        {agentUrlBar}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: 260, color: '#7d8590', fontSize: 14 }}>
          Detecting local node…
        </div>
      </div>
    )
  }

  if (!agentRunning || !detection) {
    return (
      <div style={{ maxWidth: 1100, margin: '24px auto', padding: '0 24px' }}>
        {agentUrlBar}
        <GettingStarted />
      </div>
    )
  }

  const modelIds = (detection.models ?? []).map(extractModelId).filter(Boolean)
  const committedGB = (detection.total_ram_gb * capPct) / 100
  const usedGB = detection.total_ram_gb * detection.used_pct / 100
  const { usedPct, commPct } = ramBar(usedGB, detection.total_ram_gb, committedGB)

  const cliCmd = [
    'oim node start',
    coordinator ? `--coordinator ${coordinator}` : '',
    `--region ${region}`,
    `--cap ${(capPct / 100).toFixed(2)}`,
    reach ? `--reachability-endpoint ${reach}` : '',
    schedule.mode === 'window' ? `--schedule-mode window --schedule-start ${schedule.daily_start} --schedule-end ${schedule.daily_end}` : '',
    schedule.mode === 'window' && schedule.days.length > 0 ? `--schedule-days ${schedule.days.join(',')}` : '',
  ].filter(Boolean).join(' \\\n  ')

  const topologyDevices = detection.device_topology?.devices ?? []

  return (
    <div style={{ maxWidth: 1100, margin: '24px auto', padding: '0 24px' }}>

      {agentUrlBar}

      {topologyDevices.length > 0 && (
        <div style={{ marginBottom: 16 }}>
          <ClusterTopologySection devices={topologyDevices} />
        </div>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16, marginBottom: 16 }}>

        {/* ── Left: machine detection ── */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>

          {/* Machine specs */}
          <Section title="Detected hardware">
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, marginBottom: 16 }}>
              <DetectStat label="Node ID" value={detection.node_id.slice(0, 16) + '…'} mono />
              <DetectStat label="Platform" value={detection.platform} />
              {detection.is_apple_silicon && (
                <div style={{ gridColumn: '1 / -1' }}>
                  <span style={{
                    background: '#a371f718', border: '1px solid #a371f744',
                    color: '#a371f7', fontSize: 11, fontWeight: 700,
                    padding: '3px 9px', borderRadius: 5,
                  }}>Apple Silicon — Secure Enclave available</span>
                </div>
              )}
            </div>

            {/* RAM bar */}
            <div style={{ marginBottom: 4 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11, color: '#7d8590', marginBottom: 5 }}>
                <span>RAM usage</span>
                <span>{usedGB.toFixed(1)} / {detection.total_ram_gb.toFixed(1)} GB used</span>
              </div>
              <div style={{ height: 8, background: '#21262d', borderRadius: 4, overflow: 'hidden', position: 'relative' }}>
                <div style={{ position: 'absolute', left: 0, top: 0, height: '100%', width: `${usedPct}%`, background: '#d29922', borderRadius: 4 }} />
                <div style={{ position: 'absolute', left: 0, top: 0, height: '100%', width: `${commPct}%`, background: '#3fb95055', borderRadius: 4, border: '1px dashed #3fb950' }} />
              </div>
              <div style={{ display: 'flex', gap: 14, marginTop: 5, fontSize: 11, color: '#7d8590' }}>
                <span><span style={{ color: '#d29922' }}>■</span> Used</span>
                <span><span style={{ color: '#3fb950' }}>□</span> Your committed cap ({committedGB.toFixed(1)} GB)</span>
              </div>
            </div>
          </Section>

          {/* Exo status */}
          <Section title="Local Exo instance">
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 12 }}>
              <div style={{
                width: 9, height: 9, borderRadius: '50%',
                background: detection.exo_healthy ? '#3fb950' : '#f85149',
              }} />
              <span style={{ fontSize: 13, color: detection.exo_healthy ? '#3fb950' : '#f85149', fontWeight: 600 }}>
                {detection.exo_healthy ? 'Running' : 'Not reachable'}
              </span>
              <span style={{ color: '#484f58', fontSize: 12 }}>{detection.exo_url}</span>
            </div>
            {!detection.exo_healthy && (
              <div style={{ color: '#7d8590', fontSize: 12, background: '#f8514910', border: '1px solid #f8514930', borderRadius: 6, padding: '8px 12px' }}>
                Exo is not running. Start it first, then reload this page.
              </div>
            )}
            {detection.exo_healthy && modelIds.length === 0 && (
              <div style={{ color: '#7d8590', fontSize: 12 }}>
                No downloaded models found. Run <code style={{ color: '#79c0ff' }}>exo run llama-3.2-3b</code> to download one.
              </div>
            )}
          </Section>

          {/* Available models */}
          {modelIds.length > 0 && (
            <Section title={`Available models (${modelIds.length})`}>
              <div style={{ color: '#7d8590', fontSize: 11, marginBottom: 10 }}>
                Uncheck models you don't want to share with the mesh.
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 7 }}>
                {modelIds.map(id => (
                  <label key={id} style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer' }}>
                    <input
                      type="checkbox"
                      checked={allowedModels.has(id)}
                      onChange={() => toggleModel(id)}
                      style={{ accentColor: '#3fb950', width: 14, height: 14 }}
                    />
                    <span style={{
                      fontFamily: 'monospace', fontSize: 12,
                      color: allowedModels.has(id) ? '#e6edf3' : '#484f58',
                    }}>{id}</span>
                  </label>
                ))}
              </div>
            </Section>
          )}
        </div>

        {/* ── Right: configuration ── */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>

          <Section title="Contribution settings">

            {/* Memory cap slider */}
            <div style={{ marginBottom: 20 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 8 }}>
                <label style={{ fontSize: 13, fontWeight: 600 }}>Memory cap</label>
                <span style={{ fontFamily: 'monospace', fontSize: 13, color: '#79c0ff' }}>
                  {capPct}% — {committedGB.toFixed(1)} GB requested
                </span>
              </div>
              <input
                type="range" min={10} max={90} step={5} value={capPct}
                onChange={e => setCapPct(Number(e.target.value))}
                style={{ width: '100%', accentColor: '#3fb950' }}
              />
              <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 10, color: '#484f58', marginTop: 3 }}>
                <span>10% ({(detection.total_ram_gb * 0.1).toFixed(1)} GB)</span>
                <span style={{ color: '#7d8590', fontSize: 11 }}>
                  Never maxes out any one device — see below.
                </span>
                <span>90% ({(detection.total_ram_gb * 0.9).toFixed(1)} GB)</span>
              </div>
              {detection.safe_contributable_gb < committedGB - 0.5 ? (
                <div style={{
                  marginTop: 10, fontSize: 11, color: '#d29922',
                  background: '#d2992212', border: '1px solid #d2992240',
                  borderRadius: 6, padding: '7px 10px',
                }}>
                  Actually offered right now: <strong>{detection.safe_contributable_gb.toFixed(1)} GB</strong>, not {committedGB.toFixed(1)} GB —
                  {detection.is_cluster ? ' one or more devices in your cluster are' : ' this machine is'} low on free memory.
                  Each device always keeps its own safety margin; the mesh defers to whichever has headroom and this adjusts automatically as usage changes.
                </div>
              ) : (
                <div style={{ marginTop: 10, fontSize: 11, color: '#3fb950' }}>
                  ✓ Full {committedGB.toFixed(1)} GB is currently available to offer.
                </div>
              )}
            </div>

            {/* Region */}
            <FormRow label="Region">
              <select value={region} onChange={e => setRegion(e.target.value)} style={selectStyle}>
                <option value="us">us — North America</option>
                <option value="eu">eu — Europe</option>
                <option value="apac">apac — Asia Pacific</option>
              </select>
            </FormRow>

            {/* Max sensitivity */}
            <FormRow label="Max sensitivity tier">
              <select value={sensitivityCap} onChange={e => setSensitivityCap(e.target.value)} style={selectStyle}>
                <option value="low">low — embeddings &amp; classification only</option>
                <option value="moderate">moderate — general chat (default)</option>
                <option value="high_requires_attestation">high — PII / confidential (Secure Enclave required)</option>
              </select>
              {sensitivityCap === 'high_requires_attestation' && !detection.has_secure_enclave && (
                <div style={{ color: '#f85149', fontSize: 11, marginTop: 5 }}>
                  Secure Enclave not detected on this machine — high-sensitivity jobs will not be routed here.
                </div>
              )}
            </FormRow>

            {/* Coordinator URL */}
            <FormRow label="Coordinator URL">
              <input
                type="text" value={coordinator} placeholder="http://pod.example.com:9000"
                onChange={e => setCoordinator(e.target.value)} style={inputStyle}
              />
            </FormRow>

            {/* Reachability endpoint */}
            <FormRow label="Reachability endpoint">
              <input
                type="text" value={reach} placeholder="http://YOUR_IP:8765 (auto-derived if empty)"
                onChange={e => setReach(e.target.value)} style={inputStyle}
              />
              <div style={{ color: '#484f58', fontSize: 11, marginTop: 4 }}>
                How the coordinator sends jobs back to you. Leave blank for LAN; set to your public IP if behind NAT.
              </div>
            </FormRow>
          </Section>

          {/* Contribution schedule */}
          <Section title="Contribution schedule">
            <div style={{ display: 'flex', gap: 8, marginBottom: schedule.mode === 'window' ? 16 : 4 }}>
              <ModeButton active={schedule.mode !== 'window'} onClick={() => setSchedule(s => ({ ...s, mode: 'always' }))}>
                Always share
              </ModeButton>
              <ModeButton active={schedule.mode === 'window'} onClick={() => setSchedule(s => ({ ...s, mode: 'window' }))}>
                Only during a window
              </ModeButton>
            </div>

            {schedule.mode === 'window' ? (
              <>
                <div style={{ display: 'flex', gap: 12, marginBottom: 14 }}>
                  <FormRow label="Window start">
                    <input
                      type="time" value={schedule.daily_start}
                      onChange={e => setSchedule(s => ({ ...s, daily_start: e.target.value }))}
                      style={inputStyle}
                    />
                  </FormRow>
                  <FormRow label="Window end">
                    <input
                      type="time" value={schedule.daily_end}
                      onChange={e => setSchedule(s => ({ ...s, daily_end: e.target.value }))}
                      style={inputStyle}
                    />
                  </FormRow>
                </div>
                <div style={{ color: '#7d8590', fontSize: 11, marginBottom: 14 }}>
                  {schedule.daily_end < schedule.daily_start
                    ? `Overnight window — active ${schedule.daily_start} until ${schedule.daily_end} the next morning.`
                    : `Active daily from ${schedule.daily_start} to ${schedule.daily_end}.`}
                  {' '}Outside this window your machine is fully yours; the mesh will not use it.
                </div>
                <div style={{ marginBottom: 4 }}>
                  <label style={{ fontSize: 13, fontWeight: 600, display: 'block', marginBottom: 8 }}>
                    Days ({schedule.days.length === 0 ? 'every day' : schedule.days.join(', ')})
                  </label>
                  <div style={{ display: 'flex', gap: 6 }}>
                    {WEEKDAYS.map(({ key, label }) => (
                      <button
                        key={key}
                        onClick={() => toggleDay(key)}
                        title={key}
                        style={{
                          width: 30, height: 30, borderRadius: 6,
                          background: schedule.days.includes(key) ? '#3fb95022' : '#0d1117',
                          border: `1px solid ${schedule.days.includes(key) ? '#3fb950' : '#30363d'}`,
                          color: schedule.days.includes(key) ? '#3fb950' : '#7d8590',
                          fontSize: 12, fontWeight: 700, cursor: 'pointer',
                        }}
                      >{label}</button>
                    ))}
                  </div>
                </div>
              </>
            ) : (
              <div style={{ color: '#7d8590', fontSize: 12 }}>
                Your machine shares with the mesh anytime the node agent is running.
              </div>
            )}

            {detection.config.schedule?.mode === schedule.mode
              && detection.config.schedule?.daily_start === schedule.daily_start
              && detection.config.schedule?.daily_end === schedule.daily_end && (
              <div style={{
                marginTop: 14, display: 'flex', alignItems: 'center', gap: 8,
                fontSize: 12, color: detection.schedule_active ? '#3fb950' : '#d29922',
              }}>
                <div style={{
                  width: 7, height: 7, borderRadius: '50%',
                  background: detection.schedule_active ? '#3fb950' : '#d29922',
                }} />
                {detection.schedule_active ? 'Currently active — sharing with the mesh' : 'Currently paused — outside your scheduled window'}
              </div>
            )}
          </Section>

          {/* Generated CLI command */}
          <Section title="Equivalent CLI command">
            <div style={{ color: '#7d8590', fontSize: 11, marginBottom: 8 }}>
              Config above is saved to <code style={{ color: '#79c0ff' }}>~/.config/oim/config.json</code> and applied on next start. You can also pass flags directly:
            </div>
            <div style={{
              background: '#0d1117', border: '1px solid #30363d', borderRadius: 6,
              padding: '10px 14px', fontFamily: 'monospace', fontSize: 12,
              color: '#e6edf3', whiteSpace: 'pre', userSelect: 'all',
              lineHeight: 1.7,
            }}>{cliCmd}</div>
          </Section>

          {/* Save button */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            <button
              onClick={handleSave}
              disabled={saving}
              style={{
                background: saving ? '#1c2128' : '#238636',
                border: `1px solid ${saving ? '#30363d' : '#2ea043'}`,
                color: saving ? '#484f58' : '#ffffff',
                borderRadius: 6, padding: '8px 20px',
                cursor: saving ? 'not-allowed' : 'pointer',
                fontSize: 13, fontWeight: 600,
                transition: 'all 0.15s',
              }}
            >
              {saving ? 'Saving…' : 'Save config'}
            </button>
            <button onClick={detect} style={{
              background: '#1c2128', border: '1px solid #30363d',
              color: '#e6edf3', borderRadius: 6, padding: '8px 14px',
              cursor: 'pointer', fontSize: 13,
            }}>↺ Re-detect</button>
            {saveMsg && (
              <span style={{
                fontSize: 12,
                color: saveMsg.startsWith('Error') ? '#f85149' : '#3fb950',
              }}>{saveMsg}</span>
            )}
          </div>

        </div>
      </div>
    </div>
  )
}

// ── Local agent URL bar ───────────────────────────────────────────────────────
// Lets the operator point Node Setup at wherever their real `oim node start`
// process is actually listening. The default (8765) collides with this repo's
// own Docker simulation stack (node-us-1 owns that port), so anyone running a
// real agent alongside the simulation — or on any other custom --listen port —
// needs this to ever see their real hardware instead of the sim node.

function LocalAgentBar({ value, onChange, onApply, onReset, isDefault }: {
  value: string
  onChange: (v: string) => void
  onApply: () => void
  onReset: () => void
  isDefault: boolean
}) {
  return (
    <div style={{
      display: 'flex', alignItems: 'center', gap: 10,
      background: '#161b22', border: '1px solid #21262d',
      borderRadius: 8, padding: '8px 14px', marginBottom: 16,
    }}>
      <span style={{ color: '#7d8590', fontSize: 12, fontWeight: 600, whiteSpace: 'nowrap' }}>Local agent</span>
      <input
        value={value}
        onChange={e => onChange(e.target.value)}
        onKeyDown={e => { if (e.key === 'Enter') onApply() }}
        placeholder="http://localhost:8765"
        style={{
          flex: 1, background: '#0d1117', border: '1px solid #30363d',
          borderRadius: 6, padding: '5px 10px', color: '#e6edf3',
          fontSize: 12, fontFamily: 'monospace',
        }}
      />
      <button onClick={onApply} style={{
        background: '#1c2128', border: '1px solid #30363d', color: '#e6edf3',
        borderRadius: 6, padding: '5px 12px', cursor: 'pointer', fontSize: 12,
      }}>Connect</button>
      {!isDefault && (
        <button onClick={onReset} style={{
          background: 'transparent', border: 'none', color: '#7d8590',
          cursor: 'pointer', fontSize: 11, textDecoration: 'underline',
        }}>Reset to default</button>
      )}
    </div>
  )
}

// ── Cluster topology diagram ─────────────────────────────────────────────────

// loadColor buckets a device's live temp/GPU reading into a status color —
// same thresholds a heat-conscious operator would eyeball: idle/cool devices
// read gray, warming-up ones amber, and actively-serving ones red.
function loadColor(d: DeviceStat): string {
  if (d.temp_c >= 55 || d.gpu_usage_pct >= 15) return '#f85149'
  if (d.temp_c >= 35 || d.gpu_usage_pct >= 2) return '#d29922'
  return '#7d8590'
}

function ClusterTopologyDiagram({ devices }: { devices: DeviceStat[] }) {
  const width = 640
  const height = 340
  const cx = width / 2
  const cy = height / 2
  const radius = devices.length <= 1 ? 0 : 120
  const boxW = 108
  const boxH = 58

  const positions = new Map<string, { x: number; y: number }>()
  devices.forEach((d, i) => {
    const angle = (-90 + i * (360 / devices.length)) * (Math.PI / 180)
    positions.set(d.device_id, {
      x: cx + radius * Math.cos(angle),
      y: cy + radius * Math.sin(angle),
    })
  })

  // Dedupe undirected connection pairs — Exo reports links per-device, so
  // each real link appears twice (A->B and B->A) unless only one side is up.
  const edges: Array<[string, string]> = []
  const seenPairs = new Set<string>()
  devices.forEach(d => {
    d.connected_to.forEach(peerId => {
      if (!positions.has(peerId)) return
      const key = [d.device_id, peerId].sort().join('|')
      if (seenPairs.has(key)) return
      seenPairs.add(key)
      edges.push([d.device_id, peerId])
    })
  })

  return (
    <svg viewBox={`0 0 ${width} ${height}`} style={{ width: '100%', height: 'auto', display: 'block' }}>
      {edges.map(([a, b], i) => {
        const pa = positions.get(a)!
        const pb = positions.get(b)!
        return (
          <line key={i} x1={pa.x} y1={pa.y} x2={pb.x} y2={pb.y}
            stroke="#30363d" strokeWidth={1.5} strokeDasharray="4 4" />
        )
      })}
      {devices.map(d => {
        const p = positions.get(d.device_id)!
        const color = loadColor(d)
        const usedGB = Math.max(0, d.ram_total_gb - d.ram_available_gb)
        return (
          <g key={d.device_id} transform={`translate(${p.x - boxW / 2}, ${p.y - boxH / 2})`}>
            {/* device chassis */}
            <rect x={0} y={0} width={boxW} height={boxH} rx={6} fill="#2d2410" stroke="#d29922" strokeWidth={1.5} />
            <rect x={6} y={6} width={boxW - 12} height={boxH - 24} rx={3} fill="#3a2f14" />
            <circle cx={14} cy={boxH - 10} r={2} fill="#8a6d1f" />
            <circle cx={22} cy={boxH - 10} r={2} fill="#8a6d1f" />

            <text x={boxW / 2} y={-10} textAnchor="middle" fill="#d29922" fontSize={11} fontWeight={700} fontFamily="monospace">
              {d.friendly_name}
            </text>
            <text x={boxW / 2} y={boxH + 16} textAnchor="middle" fill="#e6edf3" fontSize={10} fontFamily="monospace">
              {usedGB.toFixed(1)}GB/{d.ram_total_gb.toFixed(0)}GB ({d.ram_used_pct.toFixed(0)}%)
            </text>

            <g transform={`translate(${boxW + 8}, 4)`}>
              <rect x={0} y={0} width={40} height={boxH - 8} rx={4} fill={`${color}18`} stroke={`${color}55`} />
              <text x={20} y={15} textAnchor="middle" fill={color} fontSize={9} fontWeight={700}>{d.gpu_usage_pct.toFixed(0)}%</text>
              <text x={20} y={28} textAnchor="middle" fill={color} fontSize={9} fontWeight={700}>{d.temp_c.toFixed(0)}°C</text>
              <text x={20} y={41} textAnchor="middle" fill={color} fontSize={9} fontWeight={700}>{d.power_w.toFixed(0)}W</text>
            </g>
          </g>
        )
      })}
    </svg>
  )
}

function ClusterTopologySection({ devices }: { devices: DeviceStat[] }) {
  return (
    <Section title={`Cluster topology (${devices.length} device${devices.length === 1 ? '' : 's'})`}>
      <div style={{ color: '#7d8590', fontSize: 11, marginBottom: 10 }}>
        Live from Exo — RAM, GPU load, temperature, and power per device, updated on every re-detect.
      </div>
      <ClusterTopologyDiagram devices={devices} />
      <div style={{ display: 'flex', gap: 16, marginTop: 4, fontSize: 11, color: '#7d8590' }}>
        <span><span style={{ color: '#7d8590' }}>■</span> Idle</span>
        <span><span style={{ color: '#d29922' }}>■</span> Warming up</span>
        <span><span style={{ color: '#f85149' }}>■</span> Actively serving</span>
      </div>
    </Section>
  )
}

// ── Sub-components ─────────────────────────────────────────────────────────────

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{
      background: '#161b22', border: '1px solid #21262d',
      borderRadius: 10, padding: '16px 20px',
    }}>
      <div style={{ fontWeight: 700, fontSize: 13, marginBottom: 14, color: '#c9d1d9' }}>{title}</div>
      {children}
    </div>
  )
}

function DetectStat({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <div style={{ color: '#7d8590', fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.06em', marginBottom: 3 }}>
        {label}
      </div>
      <div style={{
        color: '#e6edf3', fontSize: 13, fontWeight: 600,
        fontFamily: mono ? 'monospace' : undefined,
      }}>{value}</div>
    </div>
  )
}

function ModeButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      onClick={onClick}
      style={{
        flex: 1, padding: '8px 12px', borderRadius: 6, fontSize: 12, fontWeight: 600,
        cursor: 'pointer',
        background: active ? '#3fb95022' : '#0d1117',
        border: `1px solid ${active ? '#3fb950' : '#30363d'}`,
        color: active ? '#3fb950' : '#7d8590',
      }}
    >{children}</button>
  )
}

function FormRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 16 }}>
      <label style={{ fontSize: 13, fontWeight: 600, display: 'block', marginBottom: 6 }}>{label}</label>
      {children}
    </div>
  )
}

const selectStyle: React.CSSProperties = {
  width: '100%', background: '#0d1117', border: '1px solid #30363d',
  color: '#e6edf3', borderRadius: 6, padding: '7px 10px', fontSize: 13,
  cursor: 'pointer',
}

const inputStyle: React.CSSProperties = {
  width: '100%', background: '#0d1117', border: '1px solid #30363d',
  color: '#e6edf3', borderRadius: 6, padding: '7px 10px', fontSize: 13,
  boxSizing: 'border-box',
}
