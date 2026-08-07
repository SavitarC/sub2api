import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import FeishuOAuthSection from '@/components/auth/FeishuOAuthSection.vue'

const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>
}))

vi.mock('vue-router', () => ({
  useRoute: () => routeState
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key === 'auth.feishu.signIn' ? 'Continue with Mock Feishu' : key
  })
}))

describe('FeishuOAuthSection', () => {
  beforeEach(() => {
    routeState.query = {
      redirect: '/billing?plan=mock',
      aff: 'mock-feishu-affiliate'
    }
    localStorage.clear()
    sessionStorage.clear()
  })

  it('starts Feishu OAuth with the redirect and persists the mock affiliate code', async () => {
    const wrapper = mount(FeishuOAuthSection)

    expect(wrapper.text()).toContain('Continue with Mock Feishu')
    await wrapper.get('[data-testid="feishu-oauth-login"]').trigger('click')

    expect(wrapper.emitted('start')).toEqual([[
      {
        provider: 'feishu',
        params: {
          redirect: '/billing?plan=mock',
          aff_code: 'mock-feishu-affiliate'
        }
      }
    ]])
    expect(sessionStorage.getItem('oauth_aff_code')).toBe('mock-feishu-affiliate')
  })
})
