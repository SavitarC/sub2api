import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { getPublicSettingsMock } = vi.hoisted(() => ({
  getPublicSettingsMock: vi.fn()
}))

const publicSettings = {
  turnstile_enabled: false,
  turnstile_site_key: '',
  linuxdo_oauth_enabled: false,
  dingtalk_oauth_enabled: false,
  feishu_oauth_enabled: false,
  wechat_oauth_enabled: false,
  backend_mode_enabled: false,
  oidc_oauth_enabled: false,
  oidc_oauth_provider_name: 'OIDC',
  github_oauth_enabled: false,
  google_oauth_enabled: false,
  password_reset_enabled: false,
  passkey_enabled: false,
  login_agreement_enabled: false,
  login_agreement_documents: []
}

vi.mock('vue-router', () => ({
  useRouter: () => ({
    currentRoute: { value: { query: {} } },
    push: vi.fn()
  })
}))

vi.mock('vue-i18n', () => ({
  createI18n: () => ({
    global: {
      t: (key: string) => key
    }
  }),
  useI18n: () => ({
    t: (key: string) => key
  })
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({
    login: vi.fn(),
    login2FA: vi.fn(),
    loginWithPasskey: vi.fn()
  }),
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showWarning: vi.fn()
  })
}))

vi.mock('@/api/auth', () => ({
  getPublicSettings: (...args: unknown[]) => getPublicSettingsMock(...args),
  isTotp2FARequired: () => false,
  isWeChatWebOAuthEnabled: (settings: { wechat_oauth_enabled?: boolean }) =>
    settings.wechat_oauth_enabled === true
}))

async function mountLogin() {
  const { default: LoginView } = await import('@/views/auth/LoginView.vue')
  return mount(LoginView, {
    global: {
      stubs: {
        AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
        Icon: true,
        TurnstileWidget: true,
        LoginAgreementPrompt: true,
        EmailOAuthButtons: true,
        LinuxDoOAuthSection: true,
        DingTalkOAuthSection: true,
        FeishuOAuthSection: { template: '<button data-testid="mock-feishu-login-entry" />' },
        WechatOAuthSection: true,
        OidcOAuthSection: true,
        TotpLoginModal: true,
        RouterLink: true,
        transition: false
      }
    }
  })
}

describe('LoginView OAuth entries', () => {
  beforeEach(() => {
    getPublicSettingsMock.mockReset()
    getPublicSettingsMock.mockResolvedValue(publicSettings)
    localStorage.clear()
    sessionStorage.clear()
  })

  it('shows the Feishu login entry only when public settings enable it', async () => {
    const disabledWrapper = await mountLogin()
    await flushPromises()

    expect(disabledWrapper.find('[data-testid="mock-feishu-login-entry"]').exists()).toBe(false)
    disabledWrapper.unmount()

    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      feishu_oauth_enabled: true
    })
    const enabledWrapper = await mountLogin()
    await flushPromises()

    expect(enabledWrapper.get('[data-testid="mock-feishu-login-entry"]').exists()).toBe(true)
  })
})
