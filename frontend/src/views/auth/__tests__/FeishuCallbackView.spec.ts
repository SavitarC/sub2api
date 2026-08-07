import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import PendingOAuthCreateAccountForm from '@/components/auth/PendingOAuthCreateAccountForm.vue'
import { PROFILE_EMAIL_BINDING_PREFILL_KEY } from '@/utils/profileEmailBinding'
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
const authState = vi.hoisted(() => ({
  user: null as Record<string, unknown> | null
}))

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
    user: authState.user,
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
    authState.user = null
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

  it('does not accept a bearer fragment without a browser-bound pending exchange', async () => {
    window.location.hash =
      '#access_token=fragment-feishu-access&refresh_token=fragment-feishu-refresh&expires_in=3600&token_type=Bearer&redirect=%2Fmock-fast-path'
    exchangePendingOAuthCompletion.mockRejectedValue(
      Object.assign(new Error('pending session not found'), { status: 404 })
    )

    const wrapper = mountView()
    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenCalledWith()
    expect(setToken).not.toHaveBeenCalled()
    expect(localStorage.getItem('refresh_token')).toBeNull()
    expect(replace).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="feishu-callback-error"]').text()).toContain(
      'pending session not found'
    )
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

  it('retries a transient pending exchange without restarting OAuth', async () => {
    exchangePendingOAuthCompletion
      .mockRejectedValueOnce(
        Object.assign(new Error('Mock Feishu service unavailable'), {
          status: 503
        })
      )
      .mockResolvedValueOnce({
        access_token: 'retried-feishu-access-token',
        redirect: '/dashboard'
      })

    const wrapper = mountView()
    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenCalledTimes(1)
    expect(clearPendingAuthSession).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="feishu-callback-error"]').text()).toContain(
      'Mock Feishu service unavailable'
    )
    await wrapper.get('[data-testid="feishu-callback-retry"]').trigger('click')
    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenCalledTimes(2)
    expect(setToken).toHaveBeenCalledWith('retried-feishu-access-token')
    expect(replace).toHaveBeenCalledWith('/dashboard')
  })

  it('creates an account through the generic pending endpoint with mock Feishu profile data', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      auth_result: 'pending_session',
      provider: 'feishu',
      step: 'create_account_required',
      pending_email: 'account.default@example.com',
      suggested_email: 'mock.feishu@example.com',
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
      'account.default@example.com'
    )
    expect(wrapper.get<HTMLInputElement>('[data-testid="feishu-use-suggested-email"]').element.checked).toBe(false)
    expect(wrapper.get<HTMLInputElement>('[data-testid="feishu-adopt-display-name"]').element.checked).toBe(true)
    expect(wrapper.get<HTMLInputElement>('[data-testid="feishu-adopt-avatar"]').element.checked).toBe(true)

    await wrapper.get('[data-testid="feishu-use-suggested-email"]').setValue(true)
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
    expect(sessionStorage.getItem(PROFILE_EMAIL_BINDING_PREFILL_KEY)).toBeNull()
    expect(replace).toHaveBeenCalledWith('/dashboard')
  })

  it('forwards captcha proof when creating a pending Feishu account', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      auth_result: 'pending_session',
      provider: 'feishu',
      step: 'create_account_required',
      pending_email: 'captcha.feishu@example.com'
    })
    apiClientPost.mockResolvedValue({
      data: {
        access_token: 'captcha-feishu-access-token',
        redirect: '/dashboard'
      }
    })

    const wrapper = mountView()
    await flushPromises()
    wrapper.findComponent(PendingOAuthCreateAccountForm).vm.$emit('submit', {
      email: 'captcha.feishu@example.com',
      password: 'mock-secret',
      verifyCode: '123456',
      turnstileToken: 'mock-turnstile-token',
      tencentCaptchaTicket: 'mock-tencent-ticket',
      tencentCaptchaRandstr: 'mock-tencent-randstr',
      invitationCode: 'mock-invitation'
    })
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledWith('/auth/oauth/pending/create-account', {
      email: 'captcha.feishu@example.com',
      password: 'mock-secret',
      verify_code: '123456',
      turnstile_token: 'mock-turnstile-token',
      tencent_captcha_ticket: 'mock-tencent-ticket',
      tencent_captcha_randstr: 'mock-tencent-randstr',
      invitation_code: 'mock-invitation',
      adopt_display_name: false,
      adopt_avatar: false
    })
    expect(setToken).toHaveBeenCalledWith('captcha-feishu-access-token')
  })

  it('binds an existing account through the generic pending endpoint', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      auth_result: 'pending_session',
      provider: 'feishu',
      step: 'bind_login_required',
      existing_account_email: 'existing@example.com',
      adoption_required: true,
      suggested_email: 'existing@example.com',
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
    await wrapper.get('[data-testid="feishu-use-suggested-email"]').setValue(true)
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
    expect(sessionStorage.getItem(PROFILE_EMAIL_BINDING_PREFILL_KEY)).toBeNull()
    expect(replace).toHaveBeenCalledWith('/profile')
  })

  it('prefills existing-account login with the selected Feishu email from the choice step', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      auth_result: 'pending_session',
      provider: 'feishu',
      step: 'choice',
      adoption_required: true,
      suggested_email: 'choice.feishu@example.com',
      create_account_allowed: true
    })

    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="feishu-use-suggested-email"]').setValue(true)
    await wrapper.get('[data-testid="feishu-choose-bind-existing"]').trigger('click')

    expect(wrapper.get<HTMLInputElement>('[data-testid="feishu-bind-login-email"]').element.value).toBe(
      'choice.feishu@example.com'
    )
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

  it('keeps the adoption step available when its exchange fails transiently', async () => {
    exchangePendingOAuthCompletion
      .mockResolvedValueOnce({
        auth_result: 'pending_session',
        provider: 'feishu',
        adoption_required: true,
        suggested_display_name: 'Mock Feishu User',
        redirect: '/dashboard'
      })
      .mockRejectedValueOnce(
        Object.assign(new Error('Mock transient adoption failure'), {
          status: 503
        })
      )

    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="feishu-adoption-continue"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="feishu-adoption-continue"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('Mock transient adoption failure')
    expect(setToken).not.toHaveBeenCalled()
  })

  it('keeps the selected Feishu email out of adoption and opens profile verification', async () => {
    exchangePendingOAuthCompletion
      .mockResolvedValueOnce({
        auth_result: 'pending_session',
        provider: 'feishu',
        adoption_required: true,
        suggested_display_name: 'Mock Feishu User',
        suggested_avatar_url: 'https://example.com/mock-avatar.png',
        suggested_email: 'profile.feishu@example.com',
        redirect: '/dashboard'
      })
      .mockResolvedValueOnce({
        access_token: 'adopted-feishu-access-token',
        redirect: '/dashboard'
      })

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get<HTMLInputElement>('[data-testid="feishu-use-suggested-email"]').element.checked).toBe(false)
    await wrapper.get('[data-testid="feishu-adopt-display-name"]').setValue(false)
    await wrapper.get('[data-testid="feishu-use-suggested-email"]').setValue(true)
    await wrapper.get('[data-testid="feishu-adoption-continue"]').trigger('click')
    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenNthCalledWith(2, {
      adoptDisplayName: false,
      adoptAvatar: true
    })
    expect(setToken).toHaveBeenCalledWith('adopted-feishu-access-token')
    expect(sessionStorage.getItem(PROFILE_EMAIL_BINDING_PREFILL_KEY)).toBe(
      'profile.feishu@example.com'
    )
    expect(replace).toHaveBeenCalledWith('/profile')
  })

  it('finishes a current-account binding after profile adoption is confirmed', async () => {
    exchangePendingOAuthCompletion
      .mockResolvedValueOnce({
        auth_result: 'pending_session',
        provider: 'feishu',
        adoption_required: true,
        suggested_display_name: 'Mock Feishu User',
        suggested_email: 'bind.feishu@example.com',
        redirect: '/profile'
      })
      .mockResolvedValueOnce({
        auth_result: 'pending_session',
        provider: 'feishu',
        adoption_required: true,
        suggested_display_name: 'Mock Feishu User',
        redirect: '/profile'
      })

    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="feishu-use-suggested-email"]').setValue(true)
    await wrapper.get('[data-testid="feishu-adoption-continue"]').trigger('click')
    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenNthCalledWith(2, {
      adoptDisplayName: true,
      adoptAvatar: false
    })
    expect(clearPendingAuthSession).toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalledWith('profile.authBindings.bindSuccess')
    expect(sessionStorage.getItem(PROFILE_EMAIL_BINDING_PREFILL_KEY)).toBe(
      'bind.feishu@example.com'
    )
    expect(replace).toHaveBeenCalledWith('/profile')
  })

  it('does not reopen email binding after current-account bind when the same email is already bound', async () => {
    authState.user = {
      email: 'same.feishu@example.com',
      email_bound: true,
      auth_bindings: {
        email: { bound: true }
      }
    }
    exchangePendingOAuthCompletion
      .mockResolvedValueOnce({
        auth_result: 'pending_session',
        provider: 'feishu',
        adoption_required: true,
        suggested_display_name: 'Mock Feishu User',
        suggested_email: 'same.feishu@example.com',
        redirect: '/profile'
      })
      .mockResolvedValueOnce({
        auth_result: 'bound',
        redirect: '/profile'
      })

    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="feishu-use-suggested-email"]').setValue(true)
    await wrapper.get('[data-testid="feishu-adoption-continue"]').trigger('click')
    await flushPromises()

    expect(sessionStorage.getItem(PROFILE_EMAIL_BINDING_PREFILL_KEY)).toBeNull()
    expect(showSuccess).toHaveBeenCalledWith('profile.authBindings.bindSuccess')
    expect(replace).toHaveBeenCalledWith('/profile')
  })
})
