export const PROFILE_EMAIL_BINDING_PREFILL_KEY = 'sub2api_profile_email_binding_prefill'

function normalizeEmail(value: unknown): string {
  if (typeof value !== 'string') {
    return ''
  }

  const email = value.trim()
  if (
    !email ||
    email.length > 255 ||
    email.toLowerCase().endsWith('.invalid') ||
    !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)
  ) {
    return ''
  }
  return email
}

export function storeProfileEmailBindingPrefill(value: unknown): boolean {
  const email = normalizeEmail(value)
  if (!email || typeof window === 'undefined') {
    return false
  }

  try {
    window.sessionStorage.setItem(PROFILE_EMAIL_BINDING_PREFILL_KEY, email)
    return true
  } catch {
    return false
  }
}

export function consumeProfileEmailBindingPrefill(): string {
  if (typeof window === 'undefined') {
    return ''
  }

  try {
    const value = window.sessionStorage.getItem(PROFILE_EMAIL_BINDING_PREFILL_KEY)
    window.sessionStorage.removeItem(PROFILE_EMAIL_BINDING_PREFILL_KEY)
    return normalizeEmail(value)
  } catch {
    return ''
  }
}
