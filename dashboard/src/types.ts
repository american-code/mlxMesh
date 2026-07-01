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
}

export type NodeStatus = 'live' | 'degraded' | 'stale' | 'unreachable'
