import { beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import ClineUpstreamRulesEditor from '../ClineUpstreamRulesEditor.vue'
import { applyClineUpstreamRules, clineRuleToPin, parseClineUpstreamRules, type ClineUpstreamRule } from '../clineUpstreamRules'

const { discover, verify, syncModels } = vi.hoisted(() => ({
  discover: vi.fn(), verify: vi.fn(), syncModels: vi.fn()
}))
vi.mock('@/api/admin/clineUpstreams', () => ({
  discoverClineUpstreams: discover, verifyClineUpstream: verify
}))
vi.mock('@/api/admin/accounts', () => ({ syncUpstreamModelsPreview: syncModels }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const model = 'cline-pass/deepseek-v4.1-flash-1017'
const connection = { account_id: 7, base_url: 'https://api.cline.bot/api/v1', proxy_id: 0 }
function row(overrides: Partial<ClineUpstreamRule> = {}): ClineUpstreamRule {
  return { model, mode: 'strict', upstream: 'deepseek', pipeline: 'planner', has: ['vision', 'implicit-caching'], ...overrides }
}
function editor(rows = reactive([row()]), discoverySnapshot?: unknown) {
  return mount(ClineUpstreamRulesEditor, {
    props: { rows, connection, discoverySnapshot },
    global: { stubs: { Select: true } }
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  discover.mockResolvedValue({
    model, pipeline: 'planner', actual_provider: 'deepseek', canonical_model: 'deepseek/deepseek-v4.1-flash',
    providers: [{ id: 'deepseek', name: 'DeepSeek' }], discovered_at: '2026-10-27T08:00:00Z'
  })
  verify.mockResolvedValue({
    model, pipeline: 'planner', target_provider: 'deepseek', actual_provider: 'deepseek',
    status: 'matched', completed: true, stream: true, has: ['vision', 'implicit-caching'], checked_at: '2026-10-27T08:00:00Z'
  })
  syncModels.mockResolvedValue({ models: [model] })
})

describe('Cline upstream settings', () => {
  it.each(['model with space', 'model\tname', 'model\u0085name', 'model\u0000name', 'model*', 'x'.repeat(257), '界'.repeat(86)])(
    'rejects an invalid model both when adding and saving: %j', async (invalidModel) => {
      const wrapper = editor(reactive([]))
      wrapper.getComponent('[data-testid="cline-model-select"]').vm.$emit('update:modelValue', invalidModel)
      await wrapper.vm.$nextTick()
      await wrapper.get('[data-testid="cline-add-model"]').trigger('click')
      expect(wrapper.emitted('update:rows')).toBeUndefined()
      expect(wrapper.get('[role="alert"]').text()).toContain('invalidModel')
      await wrapper.setProps({ rows: [row({ model: invalidModel })] })
      expect(wrapper.vm.validate()).toBe(false)
      wrapper.unmount()
    }
  )

  it('allows 100 rules and prevents adding or saving more', async () => {
    const rows = reactive(Array.from({ length: 100 }, (_, index) => row({ model: 'model-' + index })))
    const wrapper = editor(rows)
    expect(wrapper.vm.validate()).toBe(true)
    wrapper.getComponent('[data-testid="cline-model-select"]').vm.$emit('update:modelValue', 'new-model')
    await wrapper.vm.$nextTick()
    await wrapper.get('[data-testid="cline-add-model"]').trigger('click')
    expect(wrapper.emitted('update:rows')).toBeUndefined()
    expect(wrapper.get('[role="alert"]').text()).toContain('ruleLimit')
    await wrapper.setProps({ rows: [...rows, row({ model: 'overflow' })] })
    expect(wrapper.vm.validate()).toBe(false)
    wrapper.unmount()
  })

  it('ignores malformed saved discovery values and invalid provider entries', () => {
    const wrapper = editor(reactive([row(), row({ model: 'other-model' })]), {
      [model]: {
        model, pipeline: 'planner', discovered_at: '2026-10-27T08:00:00Z',
        providers: [null, 'bad', { id: null, name: 'bad' }, { id: '', name: 'bad' }, { id: 'deepseek', name: 'DeepSeek' }],
        warning: { invalid: true }
      },
      'other-model': { model: 'other-model', providers: [null], discovered_at: {} }
    })
    const providers = wrapper.findAllComponents('[data-testid="cline-provider"]')
    expect(providers[0].props('options')).toEqual([{ value: 'deepseek', label: 'DeepSeek · deepseek' }])
    expect(providers[1].props('options')).toEqual([{ value: 'deepseek', label: 'deepseek' }])
    expect(wrapper.text()).not.toContain('invalid')
    wrapper.unmount()
  })

  it('handles arbitrary model IDs without inheriting object properties', async () => {
    const wrapper = editor(reactive([row({ model: 'constructor', mode: 'auto', upstream: '', has: [] })]))
    await wrapper.get('[data-testid="cline-verify"]').trigger('click')
    await flushPromises()
    expect(verify.mock.calls[0]?.[0].model).toBe('constructor')
    expect(verify.mock.calls[0]?.[0].stream).toBe(true)
    wrapper.unmount()
  })

  it('keeps filters in advanced settings and clears them when switching to OpenRouter', async () => {
    const rows = reactive([row()])
    const wrapper = editor(rows)
    const capabilities = wrapper.get('[data-testid="cline-capabilities"]')
    expect(capabilities.element.closest('details')?.querySelector('summary')?.textContent).toContain('advanced')
    await wrapper.get('[data-testid="cline-pipeline"]').setValue('direct')
    expect(wrapper.find('[data-testid="cline-capabilities"]').exists()).toBe(false)
    expect(rows[0].has).toEqual([])
    expect(clineRuleToPin(rows[0])).toEqual({ pipeline: 'direct', upstream: 'deepseek' })
    await wrapper.get('[data-testid="cline-pipeline"]').setValue('planner')
    expect(rows[0].has).toEqual([])
    expect(wrapper.findAll<HTMLInputElement>('[data-capability]').every(input => !input.element.checked)).toBe(true)
    wrapper.unmount()
  })

  it('discovers without filters using the draft connection and clears filters for a detected direct pipeline', async () => {
    discover.mockResolvedValue({
      model, pipeline: 'direct', providers: [{ id: 'deepseek', name: 'DeepSeek' }], discovered_at: '2026-10-27T08:00:00Z'
    })
    const rows = reactive([row()])
    const wrapper = editor(rows)
    await wrapper.setProps({ connection: { ...connection, api_key: 'changed-test-key', proxy_id: 23 } })
    await wrapper.get('[data-testid="cline-discover"]').trigger('click')
    await flushPromises()
    expect(discover).toHaveBeenCalledWith({ ...connection, api_key: 'changed-test-key', proxy_id: 23, model }, expect.any(AbortSignal))
    expect(discover.mock.calls[0][0]).not.toHaveProperty('pin')
    expect(rows[0].pipeline).toBe('direct')
    expect(rows[0].has).toEqual([])
    expect(wrapper.find('[data-testid="cline-capabilities"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('verifies the complete rule and invalidates the result when its conditions change', async () => {
    const wrapper = editor()
    await wrapper.get('[data-testid="cline-verify"]').trigger('click')
    await flushPromises()
    expect(verify).toHaveBeenCalledWith({
      ...connection, model, pin: { pipeline: 'planner', upstream: 'deepseek', has: ['vision', 'implicit-caching'] }, stream: true
    }, expect.any(AbortSignal))
    expect(wrapper.get('[data-testid="cline-result"]').text()).toContain('result.matched')
    await wrapper.get('[data-capability="vision"]').setValue(false)
    expect(wrapper.find('[data-testid="cline-result"]').exists()).toBe(false)
    await wrapper.get('[data-testid="cline-test-mode"]').setValue('false')
    await wrapper.get('[data-testid="cline-verify"]').trigger('click')
    await flushPromises()
    expect(verify.mock.lastCall?.[0].stream).toBe(false)
    expect(verify.mock.lastCall?.[0].pin.has).toEqual(['implicit-caching'])
    wrapper.unmount()
  })

  it('does not apply stale responses after changing the connection', async () => {
    let resolve!: (result: unknown) => void
    discover.mockReturnValue(new Promise(done => { resolve = done }))
    const rows = reactive([row()])
    const wrapper = editor(rows)
    await wrapper.get('[data-testid="cline-discover"]').trigger('click')
    const signal = discover.mock.calls[0][1] as AbortSignal
    await wrapper.setProps({ connection: { ...connection, base_url: 'https://relay.example/v1' } })
    expect(signal.aborted).toBe(true)
    resolve({ model, pipeline: 'direct', providers: [], discovered_at: '2026-10-27T08:00:00Z' })
    await flushPromises()
    expect(rows[0].pipeline).toBe('planner')
    expect(rows[0].has).toHaveLength(2)
    wrapper.unmount()
  })

  it('refreshes a saved account model list without requiring its secret and retains existing rules', async () => {
    const rows = reactive([row()])
    const wrapper = editor(rows)
    syncModels.mockResolvedValue({ models: ['cline-pass/another-model'] })
    await wrapper.get('[data-testid="cline-refresh-models"]').trigger('click')
    await flushPromises()
    expect(syncModels).toHaveBeenCalledWith({
      ...connection, platform: 'cline', type: 'apikey', model_mapping: {}
    }, expect.any(AbortSignal))
    expect(rows).toHaveLength(1)
    expect(rows[0].model).toBe(model)
    expect(wrapper.get('[data-testid="cline-model-select"]').attributes('options')).toBeDefined()
    wrapper.unmount()
  })

  it('requires an upstream for strict rules and does not classify unknown routing as success', async () => {
    const wrapper = editor(reactive([row({ upstream: '' })]))
    expect(wrapper.vm.validate()).toBe(false)
    await wrapper.get('[data-testid="cline-verify"]').trigger('click')
    expect(verify).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="cline-mode"]').setValue('auto')
    verify.mockResolvedValue({ model, pipeline: 'planner', status: 'unknown', completed: true, stream: true, checked_at: '2026-10-27T08:00:00Z' })
    await wrapper.get('[data-testid="cline-verify"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="cline-result"]').text()).toContain('result.unknown')
    expect(wrapper.get('[data-testid="cline-result"]').classes()).not.toContain('bg-emerald-50')
    wrapper.unmount()
  })
})

describe('Cline credential serialization', () => {
  it('round-trips pins, strips direct capabilities, and preserves unrelated credentials', () => {
    const rows = parseClineUpstreamRules({
      [model]: { pipeline: 'planner', upstream: ' deepseek ', has: ['vision', 'vision', 'implicit-caching'] },
      'cline-pass/glm-5.3-flash': { pipeline: 'direct', upstream: 'z-ai', has: ['vision'] }
    })
    expect(rows[1].has).toEqual([])
    const credentials: Record<string, unknown> = { api_key: 'existing', base_url: 'https://api.cline.bot/api/v1', model_mapping: { alias: model } }
    applyClineUpstreamRules(credentials, rows)
    expect(credentials.cline_upstream_pins).toEqual({
      [model]: { pipeline: 'planner', upstream: 'deepseek', has: ['vision', 'implicit-caching'] },
      'cline-pass/glm-5.3-flash': { pipeline: 'direct', upstream: 'z-ai' }
    })
    applyClineUpstreamRules(credentials, [])
    expect(credentials).toEqual({ api_key: 'existing', base_url: 'https://api.cline.bot/api/v1', model_mapping: { alias: model } })
  })
})
