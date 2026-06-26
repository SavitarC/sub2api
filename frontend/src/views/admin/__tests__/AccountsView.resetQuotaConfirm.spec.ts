import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  getBatchTodayStats,
  getAllProxies,
  getAllGroups,
  resetAccountQuota,
  showSuccess
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  resetAccountQuota: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getBatchTodayStats,
      resetAccountQuota,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    proxies: {
      getAll: getAllProxies
    },
    groups: {
      getAll: getAllGroups
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess,
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    token: 'test-token'
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => params?.name ? `${key}:${params.name}` : key
    })
  }
})

const quotaAccount = {
  id: 42,
  name: 'quota-account',
  platform: 'anthropic',
  type: 'apikey',
  status: 'active',
  schedulable: true,
  quota_limit: 10,
  quota_used: 5,
  created_at: '2026-03-07T10:00:00Z',
  updated_at: '2026-03-07T10:00:00Z'
}

const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div data-test="data-table">
      <div v-for="row in data" :key="row.id">
        <slot name="cell-actions" :row="row" />
      </div>
    </div>
  `
}

const AccountActionMenuStub = {
  props: ['show', 'account'],
  emits: ['reset-quota'],
  template: `
    <button
      v-if="show && account"
      data-test="reset-quota-menu-item"
      @click="$emit('reset-quota', account)"
    >
      reset quota
    </button>
  `
}

const ConfirmDialogStub = {
  props: ['show', 'title', 'message', 'confirmText', 'cancelText'],
  emits: ['confirm', 'cancel'],
  template: `
    <div v-if="show" data-test="confirm-dialog">
      <p data-test="confirm-title">{{ title }}</p>
      <p data-test="confirm-message">{{ message }}</p>
      <button data-test="confirm-reset-quota" @click="$emit('confirm')">{{ confirmText }}</button>
      <button data-test="cancel-reset-quota" @click="$emit('cancel')">{{ cancelText }}</button>
    </div>
  `
}

function mountView() {
  return mount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: DataTableStub,
        Pagination: true,
        ConfirmDialog: ConfirmDialogStub,
        AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
        AccountTableFilters: { template: '<div></div>' },
        AccountBulkActionsBar: true,
        AccountActionMenu: AccountActionMenuStub,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: true,
        AccountStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: true,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: true,
        Icon: true
      }
    }
  })
}

describe('admin AccountsView reset quota confirmation', () => {
  beforeEach(() => {
    localStorage.clear()

    listAccounts.mockReset()
    listWithEtag.mockReset()
    getBatchTodayStats.mockReset()
    getAllProxies.mockReset()
    getAllGroups.mockReset()
    resetAccountQuota.mockReset()
    showSuccess.mockReset()

    listAccounts.mockResolvedValue({
      items: [quotaAccount],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    listWithEtag.mockResolvedValue({
      notModified: true,
      etag: null,
      data: null
    })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
    resetAccountQuota.mockResolvedValue({
      ...quotaAccount,
      quota_used: 0,
      updated_at: '2026-03-07T10:01:00Z'
    })
  })

  it('asks for confirmation before resetting a single account quota', async () => {
    const wrapper = mountView()
    await flushPromises()

    const actionButtons = wrapper.findAll('[data-test="data-table"] button')
    await actionButtons[2].trigger('click')
    await wrapper.get('[data-test="reset-quota-menu-item"]').trigger('click')
    await flushPromises()

    expect(resetAccountQuota).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="confirm-title"]').text()).toBe('admin.accounts.resetQuota')
    expect(wrapper.get('[data-test="confirm-message"]').text()).toBe('admin.accounts.resetQuotaConfirm:quota-account')

    await wrapper.get('[data-test="cancel-reset-quota"]').trigger('click')
    await flushPromises()

    expect(resetAccountQuota).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="confirm-dialog"]').exists()).toBe(false)

    await actionButtons[2].trigger('click')
    await wrapper.get('[data-test="reset-quota-menu-item"]').trigger('click')
    await flushPromises()

    await wrapper.get('[data-test="confirm-reset-quota"]').trigger('click')
    await flushPromises()

    expect(resetAccountQuota).toHaveBeenCalledTimes(1)
    expect(resetAccountQuota).toHaveBeenCalledWith(42)
    expect(showSuccess).toHaveBeenCalledWith('common.success')
  })
})
