import type { ClineCapability, ClinePipeline, ClineUpstreamPin } from '@/api/admin/clineUpstreams'

export const CLINE_UPSTREAM_PINS_KEY = 'cline_upstream_pins'
export const CLINE_UPSTREAM_RULE_LIMIT = 100

export function isValidClineModelID(value: string): boolean {
  const model = value.trim()
  return model.length > 0 && new TextEncoder().encode(model).length <= 256 && !/[\p{White_Space}\p{Cc}*]/u.test(model)
}

export interface ClineUpstreamRule {
  model: string
  mode: 'auto' | 'strict'
  upstream: string
  pipeline: ClinePipeline
  has: ClineCapability[]
}

export function parseClineUpstreamRules(raw: unknown): ClineUpstreamRule[] {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return []
  return Object.entries(raw).flatMap(([model, value]) => {
    if (!model.trim() || !value || typeof value !== 'object' || Array.isArray(value)) return []
    const pin = value as Record<string, unknown>
    const pipeline = pin.pipeline === 'planner' || pin.pipeline === 'direct' ? pin.pipeline : 'unknown'
    const upstream = typeof pin.upstream === 'string' ? pin.upstream.trim() : ''
    return [{
      model: model.trim(), upstream, pipeline,
      mode: upstream ? 'strict' : 'auto',
      has: pipeline === 'direct' ? [] : normalizeClineCapabilities(pin.has)
    }]
  })
}

function normalizeClineCapabilities(value: unknown): ClineCapability[] {
  return Array.isArray(value)
    ? [...new Set(value.filter((item): item is ClineCapability => item === 'vision' || item === 'implicit-caching'))]
    : []
}

export function clineRuleToPin(rule: ClineUpstreamRule): ClineUpstreamPin {
  const has = rule.pipeline === 'direct' ? [] : normalizeClineCapabilities(rule.has)
  return {
    pipeline: rule.pipeline,
    ...(rule.mode === 'strict' ? { upstream: rule.upstream.trim() } : {}),
    ...(has.length ? { has } : {})
  }
}

export function applyClineUpstreamRules(credentials: Record<string, unknown>, rules: ClineUpstreamRule[]): void {
  if (!rules.length) {
    delete credentials[CLINE_UPSTREAM_PINS_KEY]
    return
  }
  credentials[CLINE_UPSTREAM_PINS_KEY] = Object.fromEntries(
    rules.map(rule => [rule.model.trim(), clineRuleToPin(rule)])
  )
}
