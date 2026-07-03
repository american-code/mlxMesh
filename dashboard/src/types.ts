export interface ModelCapability {
  model_id: string
  quantization: string
  runtime: string
  max_context_tokens: number
  is_moe: boolean
}

export interface NodeSnapshot {
  node_id: string
  status: 'live' | 'stale' | 'unreachable'
  geographic_hint: string
  geo_lat: number
  geo_lng: number
  reachability_endpoint: string
  declared_memory_gb: number
  committed_memory_gb: number
  models: ModelCapability[]
  measured_toks_per_sec: number
  has_secure_enclave: boolean // self-declared by the node — informational only
  enclave_attested: boolean // coordinator-verified Secure Enclave proof — trust this, not has_secure_enclave
  is_cluster: boolean
  cluster_device_count?: number
  // One coarse chip family per cluster device (e.g. "Apple M1") — no hostnames,
  // no exact chip variant. Empty/absent for non-cluster nodes.
  cluster_chip_families?: string[]
  last_seen_at: string
  in_flight_jobs: number
}

export interface PodMetrics {
  queue_depth: number
  queue_capacity: number
  backpressure_pct: number
  total_in_flight: number
  nodes_live?: number
  nodes_total?: number
}

export interface PodHealthDigest {
  pod_id: string
  region_hint: string
  coordinator_endpoint: string
  servable_model_ids: string[]
  aggregate_health_score: number
  node_count_approx: number
  total_memory_gb: number
  aggregate_toks_per_sec: number
}

export interface TopologyResponse {
  pods: PodHealthDigest[]
  pod_count: number
  queried_at: string
}

// CoordinationParticipant is an iOS device acting as a security/coordination
// layer (hosts encrypted payload pointers). Not an inference node — rendered
// with a distinct icon and toggleable as a layer.
export interface CoordinationParticipant {
  device_id: string
  role: string
  is_mobile: boolean
  geographic_hint: string
  last_seen_at: string
}

export interface NodesResponse {
  pod_id: string
  region: string
  nodes: NodeSnapshot[]
  coordination_nodes?: CoordinationParticipant[]
  metrics?: PodMetrics
}

export type NodeStatus = 'live' | 'degraded' | 'stale' | 'unreachable'

export interface Balance {
  grant_balance: number
  earned_balance: number
  total: number
}

// Schedule controls when this node contributes to the mesh. Mode 'window'
// restricts sharing to a daily HH:MM-HH:MM local-time range (end < start
// means it crosses midnight, e.g. "22:00"-"07:00" = overnight only),
// optionally limited to specific weekdays. Empty/'always' mode shares
// whenever the agent process is running — the pre-scheduler default.
export interface Schedule {
  mode: 'always' | 'window' | ''
  daily_start: string // "HH:MM", 24-hour
  daily_end: string // "HH:MM", 24-hour
  days: string[] // lowercase 3-letter weekdays (mon..sun); empty = every day
}

export const DEFAULT_SCHEDULE: Schedule = { mode: 'always', daily_start: '22:00', daily_end: '07:00', days: [] }

export interface NodeConfig {
  exo_url: string
  memory_cap_pct: number
  geographic_hint: string
  reachability_endpoint: string
  pod_endpoint: string
  allowed_models: string[]
  sensitivity_cap: string
  schedule: Schedule
}

export interface DeviceStat {
  device_id: string
  friendly_name: string
  model_id: string // e.g. "Mac Studio", "MacBook Pro"
  chip_id: string // e.g. "Apple M1 Max"
  ram_total_gb: number
  ram_available_gb: number
  ram_used_pct: number
  gpu_usage_pct: number
  temp_c: number
  power_w: number
  connected_to: string[]
}

export interface DeviceTopology {
  devices: DeviceStat[]
}

export interface NodeDetection {
  node_id: string
  platform: string
  is_apple_silicon: boolean
  total_ram_gb: number
  available_ram_gb: number
  used_pct: number
  is_cluster: boolean
  cluster_device_count?: number
  cluster_chip_families?: string[]
  // Sum, across every cluster device (or this solo machine), of currently-free
  // memory minus a per-device safety reserve — the mesh will never actually be
  // given more than this, regardless of the memory-cap slider below. Devices
  // already low on free memory (e.g. one machine near-full while others in
  // the same cluster have headroom) contribute little or nothing here.
  safe_contributable_gb: number
  device_topology?: DeviceTopology | null
  has_secure_enclave: boolean
  is_foregrounded: boolean
  exo_healthy: boolean
  exo_url: string
  models: Array<{ id?: string; model_id?: string; [key: string]: unknown }>
  config: NodeConfig
  schedule_active: boolean
}
