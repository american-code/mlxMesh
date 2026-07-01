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
  has_secure_enclave: boolean
  is_cluster: boolean
  cluster_device_count?: number
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

export interface NodesResponse {
  pod_id: string
  region: string
  nodes: NodeSnapshot[]
  metrics?: PodMetrics
}

export type NodeStatus = 'live' | 'degraded' | 'stale' | 'unreachable'

export interface Balance {
  grant_balance: number
  earned_balance: number
  total: number
}

export interface NodeConfig {
  exo_url: string
  memory_cap_pct: number
  geographic_hint: string
  reachability_endpoint: string
  pod_endpoint: string
  allowed_models: string[]
  sensitivity_cap: string
}

export interface NodeDetection {
  node_id: string
  platform: string
  is_apple_silicon: boolean
  total_ram_gb: number
  available_ram_gb: number
  used_pct: number
  has_secure_enclave: boolean
  is_foregrounded: boolean
  exo_healthy: boolean
  exo_url: string
  models: Array<{ id?: string; model_id?: string; [key: string]: unknown }>
  config: NodeConfig
}
