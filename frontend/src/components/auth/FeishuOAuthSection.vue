<template>
  <div class="space-y-4">
    <button
      type="button"
      :disabled="disabled"
      class="btn btn-secondary w-full"
      data-testid="feishu-oauth-login"
      @click="startLogin"
    >
      <svg
        class="icon mr-2"
        viewBox="0 0 24 24"
        xmlns="http://www.w3.org/2000/svg"
        width="20"
        height="20"
        aria-hidden="true"
        style="flex-shrink: 0"
      >
        <circle cx="12" cy="12" r="12" fill="#3370FF" />
        <text
          x="12"
          y="17"
          font-family="sans-serif"
          font-size="13"
          font-weight="bold"
          fill="white"
          text-anchor="middle"
        >F</text>
      </svg>
      {{ t('auth.feishu.signIn') }}
    </button>

    <div v-if="showDivider" class="flex items-center gap-3">
      <div class="h-px flex-1 bg-gray-200 dark:bg-dark-700"></div>
      <span class="text-xs text-gray-500 dark:text-dark-400">
        {{ t('auth.oauthOrContinue') }}
      </span>
      <div class="h-px flex-1 bg-gray-200 dark:bg-dark-700"></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import type { OAuthLoginStart } from '@/api/auth'
import { resolveAffiliateReferralCode, storeOAuthAffiliateCode } from '@/utils/oauthAffiliate'

const props = withDefaults(defineProps<{
  disabled?: boolean
  affCode?: string
  showDivider?: boolean
}>(), {
  showDivider: true
})
const emit = defineEmits<{
  start: [request: OAuthLoginStart]
}>()

const route = useRoute()
const { t } = useI18n()

function startLogin(): void {
  const redirectTo = (route.query.redirect as string) || '/dashboard'
  const affiliateCode = resolveAffiliateReferralCode(props.affCode, route.query.aff, route.query.aff_code)
  storeOAuthAffiliateCode(affiliateCode)
  const params: Record<string, string> = { redirect: redirectTo }
  if (affiliateCode) {
    params.aff_code = affiliateCode
  }
  emit('start', { provider: 'feishu', params })
}
</script>
