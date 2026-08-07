import { beforeEach, describe, expect, it } from 'vitest'
import {
  consumeProfileEmailBindingPrefill,
  PROFILE_EMAIL_BINDING_PREFILL_KEY,
  storeProfileEmailBindingPrefill,
} from '@/utils/profileEmailBinding'

describe('profileEmailBinding', () => {
  beforeEach(() => {
    sessionStorage.clear()
  })

  it('stores a valid email in session storage and consumes it once', () => {
    expect(storeProfileEmailBindingPrefill('  feishu.user@example.com  ')).toBe(true)
    expect(sessionStorage.getItem(PROFILE_EMAIL_BINDING_PREFILL_KEY)).toBe(
      'feishu.user@example.com'
    )
    expect(consumeProfileEmailBindingPrefill()).toBe('feishu.user@example.com')
    expect(consumeProfileEmailBindingPrefill()).toBe('')
  })

  it('rejects synthetic and malformed email values', () => {
    expect(storeProfileEmailBindingPrefill('user@feishu-connect.invalid')).toBe(false)
    expect(storeProfileEmailBindingPrefill('not-an-email')).toBe(false)
    expect(sessionStorage.getItem(PROFILE_EMAIL_BINDING_PREFILL_KEY)).toBeNull()
  })
})
