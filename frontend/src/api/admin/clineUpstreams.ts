import { apiClient } from '../client'

export type ClinePipeline = 'planner' | 'direct' | 'unknown'
export type ClineCapability = 'vision' | 'implicit-caching'

export interface ClineUpstreamPin {
  pipeline: ClinePipeline
  upstream?: string
  has?: ClineCapability[]
}

export interface ClineConnection {
  account_id?: number
  api_key?: string
  base_url?: string
  proxy_id?: number
}

export interface ClineUpstreamDiscovery {
  model: string
  pipeline: ClinePipeline
  canonical_model?: string
  actual_provider?: string
  providers: Array<{ id: string; name: string }>
  discovered_at: string
  warning?: string
}

export interface ClineUpstreamVerification {
  model: string
  pipeline: ClinePipeline
  canonical_model?: string
  actual_provider?: string
  target_provider?: string
  status: 'matched' | 'mismatch' | 'unknown' | 'unavailable'
  stream: boolean
  completed: boolean
  checked_at: string
  message?: string
  has?: ClineCapability[]
}

export async function discoverClineUpstreams(
  params: ClineConnection & { model: string },
  signal?: AbortSignal
): Promise<ClineUpstreamDiscovery> {
  const { data } = await apiClient.post<ClineUpstreamDiscovery>(
    '/admin/accounts/cline/upstreams/discover', params, { signal, timeout: 120000 }
  )
  return data
}

export async function verifyClineUpstream(
  params: ClineConnection & { model: string; pin: ClineUpstreamPin; stream: boolean },
  signal?: AbortSignal
): Promise<ClineUpstreamVerification> {
  const { data } = await apiClient.post<ClineUpstreamVerification>(
    '/admin/accounts/cline/upstreams/verify', params, { signal, timeout: 120000 }
  )
  return data
}
