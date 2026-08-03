import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import FeishuOAuthSection from '@/components/auth/FeishuOAuthSection.vue'

const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>
}))

const locationState = vi.hoisted(() => ({
  current: { href: 'http://localhost/login' } as { href: string }
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
    locationState.current = { href: 'http://localhost/login' }
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: locationState.current
    })
    localStorage.clear()
    sessionStorage.clear()
  })

  it('starts Feishu OAuth with the redirect and persists the mock affiliate code', async () => {
    const wrapper = mount(FeishuOAuthSection)

    expect(wrapper.text()).toContain('Continue with Mock Feishu')
    await wrapper.get('[data-testid="feishu-oauth-login"]').trigger('click')

    expect(locationState.current.href).toBe(
      '/api/v1/auth/oauth/feishu/start?redirect=%2Fbilling%3Fplan%3Dmock'
    )
    expect(sessionStorage.getItem('oauth_aff_code')).toBe('mock-feishu-affiliate')
  })
})
