import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import FeishuCallbackView from '../FeishuCallbackView.vue'

const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>
}))

const replace = vi.fn()
const showSuccess = vi.fn()
const showError = vi.fn()
const setToken = vi.fn()
const setPendingAuthSession = vi.fn()
const clearPendingAuthSession = vi.fn()
const exchangePendingOAuthCompletion = vi.fn()
const login2FA = vi.fn()
const getPublicSettings = vi.fn()
const apiClientPost = vi.fn()

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => ({ replace })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key === 'auth.feishuProviderName' ? 'Mock Feishu' : key,
      te: () => false
    })
  }
})

vi.mock('@/stores', () => ({
  useAuthStore: () => ({
    setToken,
    setPendingAuthSession,
    clearPendingAuthSession
  }),
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    post: (...args: unknown[]) => apiClientPost(...args)
  }
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    exchangePendingOAuthCompletion: (...args: unknown[]) => exchangePendingOAuthCompletion(...args),
    login2FA: (...args: unknown[]) => login2FA(...args),
    getPublicSettings: (...args: unknown[]) => getPublicSettings(...args)
  }
})

function mountView() {
  return mount(FeishuCallbackView, {
    global: {
      stubs: {
        AuthLayout: { template: '<main><slot /></main>' },
        RouterLink: { template: '<a><slot /></a>' },
        TurnstileWidget: true
      }
    }
  })
}

describe('FeishuCallbackView', () => {
  beforeEach(() => {
    routeState.query = {}
    replace.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    setToken.mockReset().mockResolvedValue(undefined)
    setPendingAuthSession.mockReset()
    clearPendingAuthSession.mockReset()
    exchangePendingOAuthCompletion.mockReset()
    login2FA.mockReset()
    getPublicSettings.mockReset().mockResolvedValue({
      email_verify_enabled: false,
      invitation_code_enabled: false,
      turnstile_enabled: false,
      turnstile_site_key: ''
    })
    apiClientPost.mockReset()
    window.location.hash = ''
    localStorage.clear()
    sessionStorage.clear()
  })

  it('exchanges the pending Feishu session and completes login', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      access_token: 'mock-feishu-access-token',
      refresh_token: 'mock-feishu-refresh-token',
      expires_in: 3600,
      redirect: '/mock-dashboard'
    })

    mountView()
    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenCalledWith()
    expect(setToken).toHaveBeenCalledWith('mock-feishu-access-token')
    expect(localStorage.getItem('refresh_token')).toBe('mock-feishu-refresh-token')
    expect(clearPendingAuthSession).toHaveBeenCalled()
    expect(replace).toHaveBeenCalledWith('/mock-dashboard')
  })

  it('accepts the fast-path Feishu token fragment without a pending exchange', async () => {
    window.location.hash =
      '#access_token=fragment-feishu-access&refresh_token=fragment-feishu-refresh&expires_in=3600&token_type=Bearer&redirect=%2Fmock-fast-path'

    mountView()
    await flushPromises()

    expect(exchangePendingOAuthCompletion).not.toHaveBeenCalled()
    expect(setToken).toHaveBeenCalledWith('fragment-feishu-access')
    expect(localStorage.getItem('refresh_token')).toBe('fragment-feishu-refresh')
    expect(replace).toHaveBeenCalledWith('/mock-fast-path')
  })

  it('falls back safely when the callback has duplicate redirect query values', async () => {
    routeState.query = { redirect: ['/first', '/second'] }
    exchangePendingOAuthCompletion.mockResolvedValue({
      access_token: 'mock-duplicate-query-token'
    })

    mountView()
    await flushPromises()

    expect(setToken).toHaveBeenCalledWith('mock-duplicate-query-token')
    expect(replace).toHaveBeenCalledWith('/dashboard')
  })

  it('shows a Feishu provider error from the callback fragment without pending exchange', async () => {
    window.location.hash =
      '#error=token_exchange_failed&error_message=mock_exchange_failure&error_description=Mock%20Feishu%20token%20exchange%20failed'

    const wrapper = mountView()
    await flushPromises()

    expect(exchangePendingOAuthCompletion).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="feishu-callback-error"]').text()).toContain(
      'Mock Feishu token exchange failed'
    )
    expect(setToken).not.toHaveBeenCalled()
  })

  it('shows an error when the fast-path Feishu token cannot initialize the session', async () => {
    window.location.hash = '#access_token=invalid-mock-token&redirect=%2Fdashboard'
    setToken.mockRejectedValueOnce(new Error('Mock Feishu session initialization failed'))

    const wrapper = mountView()
    await flushPromises()

    expect(exchangePendingOAuthCompletion).not.toHaveBeenCalled()
    expect(clearPendingAuthSession).toHaveBeenCalled()
    expect(wrapper.get('[data-testid="feishu-callback-error"]').text()).toContain(
      'Mock Feishu session initialization failed'
    )
    expect(replace).not.toHaveBeenCalled()
  })

  it('creates an account through the generic pending endpoint with mock Feishu profile data', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      auth_result: 'pending_session',
      provider: 'feishu',
      step: 'create_account_required',
      pending_email: 'mock.feishu@example.com',
      adoption_required: true,
      suggested_display_name: 'Mock Feishu User',
      suggested_avatar_url: 'data:image/png;base64,bW9jay1mZWlzaHU=',
      redirect: '/dashboard'
    })
    apiClientPost.mockResolvedValue({
      data: {
        access_token: 'created-feishu-access-token',
        redirect: '/dashboard'
      }
    })

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('Mock Feishu User')
    expect(wrapper.get('[data-testid="feishu-create-account-email"]').element).toHaveProperty(
      'value',
      'mock.feishu@example.com'
    )
    await wrapper.get('[data-testid="feishu-create-account-password"]').setValue('mock-secret')
    await wrapper.get('[data-testid="feishu-create-account-submit"]').trigger('click')
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledWith('/auth/oauth/pending/create-account', {
      email: 'mock.feishu@example.com',
      password: 'mock-secret',
      verify_code: undefined,
      invitation_code: undefined,
      adopt_display_name: true,
      adopt_avatar: true
    })
    expect(setToken).toHaveBeenCalledWith('created-feishu-access-token')
  })

  it('binds an existing account through the generic pending endpoint', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      auth_result: 'pending_session',
      provider: 'feishu',
      step: 'bind_login_required',
      existing_account_email: 'existing@example.com',
      create_account_allowed: false
    })
    apiClientPost.mockResolvedValue({
      data: {
        auth_result: 'bound',
        redirect: '/profile'
      }
    })

    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="feishu-bind-create-account"]').exists()).toBe(false)
    await wrapper.get('[data-testid="feishu-bind-login-password"]').setValue('existing-secret')
    await wrapper.get('[data-testid="feishu-bind-login-submit"]').trigger('click')
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledWith('/auth/oauth/pending/bind-login', {
      email: 'existing@example.com',
      password: 'existing-secret',
      adopt_display_name: false,
      adopt_avatar: false
    })
    expect(setToken).not.toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalledWith('profile.authBindings.bindSuccess')
    expect(replace).toHaveBeenCalledWith('/profile')
  })

  it('does not prefill a synthetic Feishu address when registration is disabled', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      auth_result: 'pending_session',
      provider: 'feishu',
      step: 'bind_login_required',
      resolved_email: 'feishu-mock@feishu-connect.invalid',
      create_account_allowed: false
    })

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get<HTMLInputElement>('[data-testid="feishu-bind-login-email"]').element.value).toBe('')
    expect(wrapper.find('[data-testid="feishu-bind-create-account"]').exists()).toBe(false)
  })

  it('completes a two-factor challenge from a pending Feishu binding login', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      auth_result: 'pending_session',
      provider: 'feishu',
      requires_2fa: true,
      temp_token: 'mock-feishu-temp-token',
      user_email_masked: 'm***@example.com',
      redirect: '/dashboard'
    })
    login2FA.mockResolvedValue({ access_token: 'mock-2fa-access-token' })

    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="feishu-bind-login-totp"]').setValue('123456')
    await wrapper.get('[data-testid="feishu-bind-login-totp-submit"]').trigger('click')
    await flushPromises()

    expect(login2FA).toHaveBeenCalledWith({
      temp_token: 'mock-feishu-temp-token',
      totp_code: '123456'
    })
    expect(setToken).toHaveBeenCalledWith('mock-2fa-access-token')
    expect(replace).toHaveBeenCalledWith('/dashboard')
  })
})
