<template>
  <AuthLayout>
    <div class="space-y-6">
      <div class="text-center">
        <h2 class="text-2xl font-bold text-gray-900 dark:text-white">
          {{ t('auth.feishu.callbackTitle') }}
        </h2>
        <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">
          {{ isProcessing ? t('auth.feishu.callbackProcessing') : t('auth.feishu.callbackHint') }}
        </p>
      </div>

      <div v-if="isProcessing" class="flex justify-center py-6" data-testid="feishu-callback-loading">
        <svg class="h-8 w-8 animate-spin text-primary-600" fill="none" viewBox="0 0 24 24">
          <circle
            class="opacity-25"
            cx="12"
            cy="12"
            r="10"
            stroke="currentColor"
            stroke-width="4"
          ></circle>
          <path
            class="opacity-75"
            fill="currentColor"
            d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
          ></path>
        </svg>
      </div>

      <div v-else-if="errorMessage" class="space-y-4" data-testid="feishu-callback-error">
        <div class="rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-950/30 dark:text-red-300">
          <p class="font-medium">{{ t('auth.feishu.error.title') }}</p>
          <p class="mt-1">{{ errorMessage }}</p>
        </div>
        <button
          v-if="canRetryPendingExchange"
          class="btn btn-primary w-full"
          data-testid="feishu-callback-retry"
          @click="retryPendingExchange"
        >
          {{ t('auth.feishu.retry') }}
        </button>
        <router-link to="/login" class="btn btn-secondary w-full">
          {{ t('auth.feishu.backToLogin') }}
        </router-link>
      </div>

      <div v-else class="space-y-4">
        <div
          v-if="adoptionRequired && (suggestedDisplayName || suggestedAvatarUrl)"
          class="rounded-xl border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-800/60"
          data-testid="feishu-profile-suggestion"
        >
          <div class="space-y-3">
            <div class="space-y-1">
              <p class="text-sm font-medium text-gray-900 dark:text-white">
                {{ t('auth.oauthFlow.profileDetailsTitle', { providerName }) }}
              </p>
              <p class="text-xs text-gray-500 dark:text-dark-400">
                {{ t('auth.oauthFlow.profileDetailsDescription', { providerName }) }}
              </p>
            </div>

            <label
              v-if="suggestedDisplayName"
              class="flex items-start gap-3 rounded-lg border border-gray-200 bg-white p-3 text-sm dark:border-dark-600 dark:bg-dark-900/50"
            >
              <input v-model="adoptDisplayName" type="checkbox" class="mt-1 h-4 w-4" />
              <span>
                <span class="block font-medium text-gray-900 dark:text-white">
                  {{ t('auth.oauthFlow.useDisplayName') }}
                </span>
                <span class="block text-gray-500 dark:text-dark-400">{{ suggestedDisplayName }}</span>
              </span>
            </label>

            <label
              v-if="suggestedAvatarUrl"
              class="flex items-start gap-3 rounded-lg border border-gray-200 bg-white p-3 text-sm dark:border-dark-600 dark:bg-dark-900/50"
            >
              <input v-model="adoptAvatar" type="checkbox" class="mt-1 h-4 w-4" />
              <img
                :src="suggestedAvatarUrl"
                :alt="t('auth.oauthFlow.avatarAlt', { providerName })"
                class="h-10 w-10 rounded-full border border-gray-200 object-cover dark:border-dark-600"
              />
              <span class="break-all text-gray-500 dark:text-dark-400">{{ suggestedAvatarUrl }}</span>
            </label>
          </div>
        </div>

        <template v-if="flowStep === 'adoption'">
          <p class="text-sm text-gray-700 dark:text-gray-300">
            {{ t('auth.oauthFlow.reviewProfileBeforeContinue', { providerName }) }}
          </p>
          <button
            class="btn btn-primary w-full"
            data-testid="feishu-adoption-continue"
            :disabled="isSubmitting"
            @click="continueAfterAdoption"
          >
            {{ isSubmitting ? t('common.processing') : t('auth.continue') }}
          </button>
          <p v-if="actionError" class="text-sm text-red-600 dark:text-red-400">{{ actionError }}</p>
        </template>

        <template v-else-if="flowStep === 'choose'">
          <div class="rounded-xl border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-800/60">
            <p class="text-sm font-medium text-gray-900 dark:text-white">
              {{ t('auth.oauthFlow.chooseHowToContinue') }}
            </p>
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
              {{
                pendingEmail
                  ? t('auth.oauthFlow.suggestedEmail', { email: pendingEmail })
                  : t('auth.oauthFlow.chooseAccountActionHint')
              }}
            </p>
            <div class="mt-4 grid gap-3 sm:grid-cols-2">
              <button class="btn btn-secondary w-full" @click="switchToBind()">
                {{ t('auth.oauthFlow.bindExistingAccount') }}
              </button>
              <button
                v-if="canCreateAccount"
                class="btn btn-primary w-full"
                data-testid="feishu-choose-create-account"
                @click="switchToCreate"
              >
                {{ t('auth.oauthFlow.createNewAccount') }}
              </button>
            </div>
          </div>
        </template>

        <template v-else-if="flowStep === 'create'">
          <p class="text-sm text-gray-700 dark:text-gray-300">
            {{ t('auth.oauthFlow.createAccountHint') }}
          </p>
          <PendingOAuthCreateAccountForm
            test-id-prefix="feishu"
            :initial-email="pendingEmail"
            :is-submitting="isSubmitting"
            :error-message="actionError"
            @submit="createAccount"
            @switch-to-bind="switchToBind"
          />
        </template>

        <template v-else-if="flowStep === 'bind'">
          <p class="text-sm text-gray-700 dark:text-gray-300">
            {{ t('auth.oauthFlow.bindLoginHint', { providerName }) }}
          </p>
          <div class="space-y-3">
            <input
              v-model.trim="bindEmail"
              data-testid="feishu-bind-login-email"
              type="email"
              class="input w-full"
              :placeholder="t('auth.emailPlaceholder')"
              :disabled="isSubmitting"
            />
            <input
              v-model="bindPassword"
              data-testid="feishu-bind-login-password"
              type="password"
              class="input w-full"
              :placeholder="t('auth.passwordPlaceholder')"
              :disabled="isSubmitting"
              @keyup.enter="bindExistingAccount"
            />
            <p v-if="actionError" class="text-sm text-red-600 dark:text-red-400">{{ actionError }}</p>
            <button
              data-testid="feishu-bind-login-submit"
              class="btn btn-primary w-full"
              :disabled="isSubmitting || !bindEmail || !bindPassword"
              @click="bindExistingAccount"
            >
              {{ isSubmitting ? t('common.processing') : t('auth.oauthFlow.logInAndBind') }}
            </button>
            <button
              v-if="canCreateAccount"
              class="btn btn-secondary w-full"
              data-testid="feishu-bind-create-account"
              :disabled="isSubmitting"
              @click="switchToCreate"
            >
              {{ t('auth.oauthFlow.createNewAccount') }}
            </button>
          </div>
        </template>

        <template v-else-if="flowStep === 'totp'">
          <p class="text-sm text-gray-700 dark:text-gray-300">
            {{
              t('auth.oauthFlow.totpHint', {
                providerName,
                account: totpEmailMasked || t('auth.oauthFlow.yourAccount')
              })
            }}
          </p>
          <div class="space-y-3">
            <input
              v-model.trim="totpCode"
              data-testid="feishu-bind-login-totp"
              type="text"
              inputmode="numeric"
              maxlength="6"
              class="input w-full"
              placeholder="123456"
              :disabled="isSubmitting"
              @keyup.enter="submitTotp"
            />
            <p v-if="actionError" class="text-sm text-red-600 dark:text-red-400">{{ actionError }}</p>
            <button
              data-testid="feishu-bind-login-totp-submit"
              class="btn btn-primary w-full"
              :disabled="isSubmitting || totpCode.length !== 6"
              @click="submitTotp"
            >
              {{ isSubmitting ? t('common.processing') : t('auth.oauthFlow.verifyAndContinue') }}
            </button>
          </div>
        </template>
      </div>
    </div>
  </AuthLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { AuthLayout } from '@/components/layout'
import PendingOAuthCreateAccountForm, {
  type PendingOAuthCreateAccountPayload
} from '@/components/auth/PendingOAuthCreateAccountForm.vue'
import { apiClient } from '@/api/client'
import {
  exchangePendingOAuthCompletion,
  getOAuthCompletionKind,
  isOAuthLoginCompletion,
  login2FA,
  persistOAuthTokenContext,
  type OAuthAdoptionDecision,
  type PendingOAuthExchangeResponse
} from '@/api/auth'
import { useAppStore, useAuthStore } from '@/stores'
import {
  clearAllAffiliateReferralCodes,
  loadOAuthAffiliateCode,
  oauthAffiliatePayload
} from '@/utils/oauthAffiliate'

type FlowStep = 'none' | 'adoption' | 'choose' | 'create' | 'bind' | 'totp'

type FeishuPendingCompletion = PendingOAuthExchangeResponse & {
  provider?: string
  step?: string
  pending_email?: string
  resolved_email?: string
  existing_account_email?: string
  compat_email?: string
  email?: string
  suggested_email?: string
  intent?: string
  create_account_allowed?: boolean
}

const route = useRoute()
const router = useRouter()
const { t, te } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()

const providerName = t('auth.feishuProviderName')
const isProcessing = ref(true)
const isSubmitting = ref(false)
const errorMessage = ref('')
const actionError = ref('')
const canRetryPendingExchange = ref(false)
const canCreateAccount = ref(true)
const flowStep = ref<FlowStep>('none')
const redirectTo = ref('/dashboard')
const pendingEmail = ref('')
const bindEmail = ref('')
const bindPassword = ref('')
const adoptionRequired = ref(false)
const suggestedDisplayName = ref('')
const suggestedAvatarUrl = ref('')
const adoptDisplayName = ref(true)
const adoptAvatar = ref(true)
const totpTempToken = ref('')
const totpCode = ref('')
const totpEmailMasked = ref('')

function parseFragmentParams(): URLSearchParams {
  const raw = typeof window !== 'undefined' ? window.location.hash : ''
  return new URLSearchParams(raw.startsWith('#') ? raw.slice(1) : raw)
}

function sanitizeRedirectPath(value: unknown, fallback = '/dashboard'): string {
  if (
    typeof value !== 'string' ||
    !value.startsWith('/') ||
    value.startsWith('//') ||
    value.includes('://')
  ) {
    return fallback
  }
  if (value.includes('\n') || value.includes('\r')) {
    return fallback
  }
  return value
}

function currentAdoptionDecision(): OAuthAdoptionDecision {
  return {
    adoptDisplayName: adoptDisplayName.value,
    adoptAvatar: adoptAvatar.value
  }
}

function serializeAdoptionDecision(decision: OAuthAdoptionDecision): Record<string, boolean> {
  return {
    ...(typeof decision.adoptDisplayName === 'boolean'
      ? { adopt_display_name: decision.adoptDisplayName }
      : {}),
    ...(typeof decision.adoptAvatar === 'boolean' ? { adopt_avatar: decision.adoptAvatar } : {})
  }
}

function setProfileSuggestion(completion: FeishuPendingCompletion): void {
  adoptionRequired.value = completion.adoption_required === true
  suggestedDisplayName.value = completion.suggested_display_name?.trim() || ''
  suggestedAvatarUrl.value = completion.suggested_avatar_url?.trim() || ''
  adoptDisplayName.value = Boolean(suggestedDisplayName.value)
  adoptAvatar.value = Boolean(suggestedAvatarUrl.value)
}

function extractPendingEmail(completion: FeishuPendingCompletion): string {
  const candidates = [
    completion.existing_account_email,
    completion.compat_email,
    completion.pending_email,
    completion.resolved_email,
    completion.email,
    completion.suggested_email
  ]
  for (const candidate of candidates) {
    const email = candidate?.trim() || ''
    if (email && !email.toLowerCase().endsWith('.invalid')) {
      return email
    }
  }
  return ''
}

function resolvePendingStep(completion: FeishuPendingCompletion): FlowStep {
  const value = (completion.step || completion.error || completion.intent || '').trim().toLowerCase()
  if (['choice', 'choose_account_action_required', 'choose_account_action', 'choose_account', 'choose'].includes(value)) {
    return 'choose'
  }
  if (['invitation_required', 'email_required', 'create_account_required', 'create_account'].includes(value)) {
    return 'create'
  }
  if (
    [
      'bind_login_required',
      'bind_login',
      'existing_account_binding_required',
      'existing_account_required',
      'adopt_existing_user_by_email'
    ].includes(value)
  ) {
    return 'bind'
  }
  return 'none'
}

function persistPendingSession(): void {
  authStore.setPendingAuthSession({
    token: '',
    token_field: 'pending_oauth_token',
    provider: 'feishu',
    redirect: redirectTo.value,
    adoption_required: adoptionRequired.value,
    suggested_display_name: suggestedDisplayName.value || undefined,
    suggested_avatar_url: suggestedAvatarUrl.value || undefined
  })
}

function requestErrorMessage(error: unknown, fallback: string): string {
  const typed = error as {
    message?: string
    response?: { data?: { detail?: string; message?: string; error?: string } }
  }
  return (
    typed.response?.data?.detail ||
    typed.response?.data?.message ||
    typed.response?.data?.error ||
    typed.message ||
    fallback
  )
}

function isRetryablePendingExchangeError(error: unknown): boolean {
  const typed = error as {
    status?: number
    code?: string
    isAxiosError?: boolean
    response?: { status?: number }
  }
  const status = typed?.status ?? typed?.response?.status
  if (typeof status !== 'number') {
    return typed?.isAxiosError === true && typed?.code !== 'ERR_CANCELED'
  }
  return status === 0 || status === 408 || status === 425 || status === 429 || status >= 500
}

function localizedCallbackError(code: string, description?: string): string {
  const normalized = code.trim().toLowerCase()
  const key = `auth.feishu.error.${normalized}`
  if (te(key)) {
    return t(key)
  }
  return description?.trim() || code || t('auth.loginFailed')
}

async function finalizeCompletion(completion: FeishuPendingCompletion): Promise<void> {
  if (getOAuthCompletionKind(completion) === 'bind') {
    authStore.clearPendingAuthSession()
    clearAllAffiliateReferralCodes()
    appStore.showSuccess(t('profile.authBindings.bindSuccess'))
    await router.replace(sanitizeRedirectPath(completion.redirect, '/profile'))
    return
  }

  if (!isOAuthLoginCompletion(completion)) {
    throw new Error(t('auth.feishu.callbackMissingToken'))
  }

  persistOAuthTokenContext(completion)
  await authStore.setToken(completion.access_token)
  authStore.clearPendingAuthSession()
  clearAllAffiliateReferralCodes()
  appStore.showSuccess(t('auth.loginSuccess'))
  await router.replace(redirectTo.value)
}

async function applyCompletion(
  completion: FeishuPendingCompletion,
  adoptionHandled = false
): Promise<void> {
  setProfileSuggestion(completion)
  canCreateAccount.value = completion.create_account_allowed !== false
  redirectTo.value = sanitizeRedirectPath(completion.redirect, redirectTo.value)

  if (completion.requires_2fa === true && completion.temp_token) {
    flowStep.value = 'totp'
    totpTempToken.value = completion.temp_token
    totpEmailMasked.value = completion.user_email_masked?.trim() || ''
    persistPendingSession()
    isProcessing.value = false
    return
  }

  const nextStep = resolvePendingStep(completion)
  if (nextStep !== 'none') {
    pendingEmail.value = extractPendingEmail(completion)
    bindEmail.value = pendingEmail.value
    flowStep.value = nextStep
    persistPendingSession()
    isProcessing.value = false
    return
  }

  if (completion.auth_result === 'pending_session') {
    if (
      !adoptionHandled &&
      adoptionRequired.value &&
      (suggestedDisplayName.value || suggestedAvatarUrl.value)
    ) {
      flowStep.value = 'adoption'
      persistPendingSession()
      isProcessing.value = false
      return
    }
    if (!adoptionHandled) {
      throw new Error(t('auth.feishu.callbackMissingToken'))
    }
  }

  if (completion.error) {
    throw new Error(localizedCallbackError(completion.error))
  }

  if (
    !adoptionHandled &&
    adoptionRequired.value &&
    (suggestedDisplayName.value || suggestedAvatarUrl.value)
  ) {
    flowStep.value = 'adoption'
    persistPendingSession()
    isProcessing.value = false
    return
  }

  await finalizeCompletion(completion)
}

function switchToBind(email?: string): void {
  bindEmail.value = email?.trim() || bindEmail.value || pendingEmail.value
  bindPassword.value = ''
  actionError.value = ''
  flowStep.value = 'bind'
}

function switchToCreate(): void {
  if (!canCreateAccount.value) return
  pendingEmail.value = pendingEmail.value || bindEmail.value
  actionError.value = ''
  flowStep.value = 'create'
}

async function continueAfterAdoption(): Promise<void> {
  isSubmitting.value = true
  actionError.value = ''
  try {
    await applyCompletion(
      await exchangePendingOAuthCompletion(currentAdoptionDecision()),
      true
    )
  } catch (error) {
    const message = requestErrorMessage(error, t('auth.loginFailed'))
    if (isRetryablePendingExchangeError(error)) {
      actionError.value = message
    } else {
      authStore.clearPendingAuthSession()
      errorMessage.value = message
    }
  } finally {
    isSubmitting.value = false
  }
}

async function exchangeBrowserBoundCompletion(): Promise<void> {
  isProcessing.value = true
  errorMessage.value = ''
  canRetryPendingExchange.value = false
  try {
    await applyCompletion(await exchangePendingOAuthCompletion() as FeishuPendingCompletion)
  } catch (error) {
    const retryable = isRetryablePendingExchangeError(error)
    if (!retryable) {
      authStore.clearPendingAuthSession()
    }
    canRetryPendingExchange.value = retryable
    errorMessage.value = requestErrorMessage(error, t('auth.loginFailed'))
    isProcessing.value = false
  }
}

async function retryPendingExchange(): Promise<void> {
  await exchangeBrowserBoundCompletion()
}

async function createAccount(payload: PendingOAuthCreateAccountPayload): Promise<void> {
  if (!payload.email || !payload.password) return
  isSubmitting.value = true
  actionError.value = ''
  try {
    const { data } = await apiClient.post<FeishuPendingCompletion>(
      '/auth/oauth/pending/create-account',
      {
        email: payload.email.trim(),
        password: payload.password,
        verify_code: payload.verifyCode || undefined,
        ...(payload.turnstileToken ? { turnstile_token: payload.turnstileToken } : {}),
        ...(payload.tencentCaptchaTicket
          ? {
              tencent_captcha_ticket: payload.tencentCaptchaTicket,
              tencent_captcha_randstr: payload.tencentCaptchaRandstr
            }
          : {}),
        invitation_code: payload.invitationCode || undefined,
        ...oauthAffiliatePayload(loadOAuthAffiliateCode()),
        ...serializeAdoptionDecision(currentAdoptionDecision())
      }
    )
    await applyCompletion(data)
  } catch (error) {
    actionError.value = requestErrorMessage(error, t('auth.feishu.completeRegistrationFailed'))
  } finally {
    isSubmitting.value = false
  }
}

async function bindExistingAccount(): Promise<void> {
  if (!bindEmail.value || !bindPassword.value) return
  isSubmitting.value = true
  actionError.value = ''
  try {
    const { data } = await apiClient.post<FeishuPendingCompletion>('/auth/oauth/pending/bind-login', {
      email: bindEmail.value,
      password: bindPassword.value,
      ...serializeAdoptionDecision(currentAdoptionDecision())
    })
    await applyCompletion(data)
  } catch (error) {
    actionError.value = requestErrorMessage(error, t('auth.loginFailed'))
  } finally {
    isSubmitting.value = false
  }
}

async function submitTotp(): Promise<void> {
  if (!totpTempToken.value || totpCode.value.length !== 6) return
  isSubmitting.value = true
  actionError.value = ''
  try {
    const completion = await login2FA({
      temp_token: totpTempToken.value,
      totp_code: totpCode.value
    })
    persistOAuthTokenContext(completion)
    await authStore.setToken(completion.access_token)
    authStore.clearPendingAuthSession()
    clearAllAffiliateReferralCodes()
    appStore.showSuccess(t('auth.loginSuccess'))
    await router.replace(redirectTo.value)
  } catch (error) {
    actionError.value = requestErrorMessage(error, t('auth.loginFailed'))
  } finally {
    isSubmitting.value = false
  }
}

onMounted(async () => {
  const fragment = parseFragmentParams()
  redirectTo.value = sanitizeRedirectPath(
    fragment.get('redirect') || (route.query.redirect as string | undefined)
  )
  const callbackError =
    fragment.get('error') || (typeof route.query.error === 'string' ? route.query.error : '')
  const callbackDescription =
    fragment.get('error_description') ||
    fragment.get('error_message') ||
    (typeof route.query.error_description === 'string' ? route.query.error_description : '')

  if (callbackError) {
    errorMessage.value = localizedCallbackError(callbackError, callbackDescription)
    isProcessing.value = false
    return
  }

  await exchangeBrowserBoundCompletion()
})
</script>
