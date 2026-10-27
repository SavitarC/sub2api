<template>
  <section class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="cline-upstream-editor">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div>
        <h3 class="input-label mb-0">{{ tr('title') }}</h3>
        <p class="input-hint">{{ tr('hint') }}</p>
      </div>
      <button type="button" class="btn btn-secondary text-sm" :disabled="syncingModels || !canConnect" data-testid="cline-refresh-models" @click="refreshModels">
        {{ syncingModels ? tr('loadingModels') : tr('refreshModels') }}
      </button>
    </div>
    <p v-if="catalogStatus" class="input-hint" role="status">{{ catalogStatus }}</p>
    <div class="flex flex-col gap-2 sm:flex-row">
      <Select
        v-model="selectedModel" class="min-w-0 flex-1"
        :options="modelOptions" searchable creatable
        :aria-label="tr('selectModel')" :placeholder="tr('selectModel')"
        :creatable-prefix="tr('manualModel')" data-testid="cline-model-select"
      />
      <button type="button" class="btn btn-secondary shrink-0 text-sm" data-testid="cline-add-model" @click="addRule">{{ tr('addModel') }}</button>
    </div>
    <p v-if="error" class="text-sm text-red-600 dark:text-red-400" role="alert">{{ error }}</p>
    <p v-if="!rows.length" class="input-hint rounded-lg bg-gray-50 p-3 dark:bg-dark-800">{{ tr('empty') }}</p>
    <details
      v-for="row in rows" :key="row.model"
      class="rounded-xl border border-gray-200 dark:border-dark-600"
      :open="expanded.has(row.model)" :data-model="row.model"
      @toggle="setExpanded(row.model, ($event.target as HTMLDetailsElement).open)"
    >
      <summary class="cursor-pointer break-words px-4 py-3">
        <span class="font-mono text-sm">{{ row.model }}</span>
        <span class="ml-2 text-xs text-gray-500 dark:text-gray-400">{{ row.mode === 'strict' ? row.upstream || tr('selectProvider') : tr('auto') }}</span>
      </summary>
      <div class="space-y-3 border-t border-gray-100 p-4 dark:border-dark-700">
        <div class="grid gap-3 sm:grid-cols-[10rem_minmax(0,1fr)]">
          <label>
            <span class="input-label">{{ tr('mode') }}</span>
            <select class="input" :value="row.mode" data-testid="cline-mode" @change="changeMode(row, ($event.target as HTMLSelectElement).value)">
              <option value="auto">{{ tr('auto') }}</option>
              <option value="strict">{{ tr('strict') }}</option>
            </select>
          </label>
          <div v-if="row.mode === 'strict'">
            <label class="input-label">{{ tr('provider') }}</label>
            <Select
              :model-value="row.upstream" :options="providerOptions(row)" searchable creatable
              :placeholder="tr('selectProvider')" :aria-label="tr('provider')"
              :creatable-prefix="tr('manualProvider')" data-testid="cline-provider"
              @update:model-value="patchRule(row, { upstream: String($event ?? '').trim() })"
            />
          </div>
          <p v-else class="input-hint self-end pb-2">{{ tr('autoHint') }}</p>
        </div>
        <div class="flex flex-wrap items-center justify-between gap-2 text-xs">
          <span class="text-gray-500 dark:text-gray-400">
            {{ stateFor(row).discovery ? tr('candidates', { count: stateFor(row).discovery!.providers.length }) : tr('noSnapshot') }}
            <span v-if="stateFor(row).discovery"> · {{ formatDate(stateFor(row).discovery!.discovered_at) }}</span>
          </span>
          <button type="button" class="text-primary-600 hover:text-primary-700 disabled:opacity-50 dark:text-primary-400" :disabled="!!stateFor(row).busy || !canConnect" data-testid="cline-discover" @click="discover(row)">
            {{ stateFor(row).busy === 'discover' ? tr('discovering') : tr('discover') }}
          </button>
        </div>
        <p v-if="stateFor(row).discovery?.warning" class="text-xs text-amber-700 dark:text-amber-300">{{ stateFor(row).discovery!.warning }}</p>
        <p v-if="row.pipeline === 'unknown'" class="rounded-lg bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-900/20 dark:text-amber-300">{{ tr('unknownHint') }}</p>
        <details class="text-sm">
          <summary class="cursor-pointer text-gray-600 dark:text-gray-400">{{ tr('advanced') }} · {{ pipelineLabel(row.pipeline) }}</summary>
          <div class="mt-3 space-y-3">
            <label class="block">
              <span class="input-label">{{ tr('pipeline') }}</span>
              <select class="input" :value="row.pipeline" data-testid="cline-pipeline" @change="setPipeline(row, ($event.target as HTMLSelectElement).value as ClinePipeline)">
                <option value="unknown">{{ tr('unknown') }}</option>
                <option value="planner">Vercel AI Gateway</option>
                <option value="direct">OpenRouter</option>
              </select>
            </label>
            <fieldset v-if="row.pipeline !== 'direct'" class="space-y-2" data-testid="cline-capabilities">
              <legend class="input-label">{{ tr('capabilities') }}</legend>
              <div class="grid gap-2 sm:grid-cols-2">
                <label v-for="capability in capabilities" :key="capability" class="flex cursor-pointer items-start gap-2 rounded-lg border border-gray-200 bg-gray-50 p-3 dark:border-dark-600 dark:bg-dark-800">
                  <input type="checkbox" class="mt-1" :checked="row.has.includes(capability)" :data-capability="capability" @change="toggleCapability(row, capability, ($event.target as HTMLInputElement).checked)">
                  <span>
                    <span class="block">{{ tr(capability === 'vision' ? 'vision' : 'caching') }}</span>
                    <span class="input-hint">{{ tr(capability === 'vision' ? 'visionHint' : 'cachingHint') }}</span>
                  </span>
                </label>
              </div>
              <p class="input-hint">{{ tr('capabilitiesHint') }}</p>
            </fieldset>
            <button type="button" class="text-xs text-red-600 hover:text-red-700 dark:text-red-400" data-testid="cline-remove" @click="removeRule(row)">{{ tr('remove') }}</button>
          </div>
        </details>
        <div class="flex flex-wrap items-end justify-between gap-3">
          <label>
            <span class="input-label">{{ tr('verificationMode') }}</span>
            <select v-model="stateFor(row).stream" class="input" data-testid="cline-test-mode" @change="invalidate(row)">
              <option :value="true">{{ tr('stream') }}</option>
              <option :value="false">{{ tr('nonstream') }}</option>
            </select>
          </label>
          <button type="button" class="btn btn-secondary text-sm" :disabled="!!stateFor(row).busy || !canConnect" data-testid="cline-verify" @click="verify(row)">
            {{ stateFor(row).busy === 'verify' ? tr('verifying') : tr('verify') }}
          </button>
        </div>
        <p v-if="stateFor(row).error" class="text-sm text-red-600 dark:text-red-400" role="alert">{{ stateFor(row).error }}</p>
        <div
          v-if="stateFor(row).result" class="rounded-lg p-3 text-sm" aria-live="polite" data-testid="cline-result"
          :class="resultClass(stateFor(row).result!)"
        >
          <p class="font-medium">{{ tr('result.' + stateFor(row).result!.status) }}</p>
          <p class="mt-1 text-xs">{{ tr('target') }}: {{ stateFor(row).result!.target_provider || tr('auto') }} · {{ tr('actual') }}: {{ stateFor(row).result!.actual_provider || tr('unknown') }}</p>
          <p class="mt-1 text-xs">{{ tr(stateFor(row).result!.completed ? 'completed' : 'incomplete') }} · {{ formatDate(stateFor(row).result!.checked_at) }}</p>
          <p v-if="stateFor(row).result!.message" class="mt-1 break-words text-xs">{{ stateFor(row).result!.message }}</p>
          <p v-if="stateFor(row).result!.has?.length" class="mt-1 text-xs">{{ tr('capabilityVerificationHint') }}</p>
        </div>
      </div>
    </details>
    <p class="input-hint">{{ tr('scopeHint') }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import { syncUpstreamModelsPreview } from '@/api/admin/accounts'
import {
  discoverClineUpstreams, verifyClineUpstream,
  type ClineCapability, type ClineConnection, type ClinePipeline,
  type ClineUpstreamDiscovery, type ClineUpstreamVerification
} from '@/api/admin/clineUpstreams'
import { CLINE_UPSTREAM_RULE_LIMIT, clineRuleToPin, isValidClineModelID, type ClineUpstreamRule } from './clineUpstreamRules'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{
  rows: ClineUpstreamRule[]
  connection: ClineConnection
  modelMapping?: Record<string, unknown>
  discoverySnapshot?: unknown
}>()
const emit = defineEmits<{ (event: 'update:rows', rows: ClineUpstreamRule[]): void }>()
const { t } = useI18n()
const tr = (key: string, params: Record<string, string | number> = {}) => t('admin.accounts.clineRouting.' + key, params)
const capabilities: ClineCapability[] = ['vision', 'implicit-caching']
const selectedModel = ref<string | number | boolean | null>('')
const catalog = ref<string[]>([])
const catalogStatus = ref('')
const syncingModels = ref(false)
const error = ref('')
const expanded = ref(new Set(props.rows.slice(0, 1).map(row => row.model)))
interface RowState {
  stream: boolean
  busy?: 'discover' | 'verify'
  discovery?: ClineUpstreamDiscovery
  result?: ClineUpstreamVerification
  error?: string
}
const states = ref<Record<string, RowState>>(Object.create(null))
const controllers = new Map<string, AbortController>()
let modelController: AbortController | undefined
let connectionVersion = 0

const stateFor = (row: ClineUpstreamRule) => states.value[row.model] ||= { stream: true }
const canConnect = computed(() => !!props.connection.account_id || !!props.connection.api_key?.trim())
const modelOptions = computed(() => [...new Set([
  ...catalog.value, ...props.rows.map(row => row.model),
  ...Object.values(props.modelMapping ?? {}).filter((value): value is string => typeof value === 'string' && !value.includes('*'))
])].sort().map(model => ({
  value: model,
  label: model + ' · ' + tr(model.startsWith('cline-pass/') ? 'subscription' : model.startsWith('cline-free/') ? 'free' : 'credits')
})))
const pipelineLabel = (pipeline: ClinePipeline) => pipeline === 'planner' ? 'Vercel AI Gateway' : pipeline === 'direct' ? 'OpenRouter' : tr('unknown')
const formatDate = (value: string) => {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString()
}
const providerOptions = (row: ClineUpstreamRule) => {
  const options = (stateFor(row).discovery?.providers ?? []).map(provider => ({ value: provider.id, label: provider.name + ' · ' + provider.id }))
  if (row.upstream && !options.some(option => option.value === row.upstream)) options.push({ value: row.upstream, label: row.upstream })
  return options
}
const setExpanded = (model: string, open: boolean) => {
  if (open) expanded.value.add(model)
  else expanded.value.delete(model)
}
function invalidate(row: ClineUpstreamRule) {
  controllers.get(row.model)?.abort()
  controllers.delete(row.model)
  const state = stateFor(row)
  delete state.result
  delete state.error
  delete state.busy
}
function patchRule(row: ClineUpstreamRule, patch: Partial<ClineUpstreamRule>) {
  invalidate(row)
  Object.assign(row, patch)
  if (row.pipeline === 'direct') row.has = []
  error.value = ''
}
function changeMode(row: ClineUpstreamRule, mode: string) {
  patchRule(row, { mode: mode === 'strict' ? 'strict' : 'auto', upstream: mode === 'strict' ? row.upstream : '' })
}
function setPipeline(row: ClineUpstreamRule, pipeline: ClinePipeline) {
  patchRule(row, { pipeline })
}
function toggleCapability(row: ClineUpstreamRule, capability: ClineCapability, checked: boolean) {
  patchRule(row, { has: checked ? [...new Set([...row.has, capability])] : row.has.filter(value => value !== capability) })
}
function addRule() {
  const model = String(selectedModel.value ?? '').trim()
  if (!isValidClineModelID(model)) { error.value = tr('invalidModel'); return }
  if (props.rows.some(row => row.model.trim() === model)) { error.value = tr('duplicate'); return }
  if (props.rows.length >= CLINE_UPSTREAM_RULE_LIMIT) { error.value = tr('ruleLimit'); return }
  error.value = ''
  expanded.value.add(model)
  emit('update:rows', [...props.rows, { model, mode: 'auto', pipeline: 'unknown', upstream: '', has: [] }])
  selectedModel.value = ''
}
function removeRule(row: ClineUpstreamRule) {
  invalidate(row)
  delete states.value[row.model]
  expanded.value.delete(row.model)
  emit('update:rows', props.rows.filter(value => value !== row))
}
function validate(): boolean {
  if (props.rows.length > CLINE_UPSTREAM_RULE_LIMIT) {
    error.value = tr('ruleLimit')
    return false
  }
  const seen = new Set<string>()
  for (const row of props.rows) {
    const model = row.model.trim()
    const invalid = !isValidClineModelID(model) ? 'invalidModel'
      : seen.has(model) ? 'duplicate'
        : row.mode === 'strict' && !row.upstream.trim() ? 'providerRequired'
          : row.upstream && !/^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,127}$/.test(row.upstream.trim()) ? 'invalidProvider' : ''
    if (invalid) {
      error.value = tr(invalid)
      expanded.value.add(row.model)
      return false
    }
    seen.add(model)
  }
  error.value = ''
  return true
}
async function refreshModels() {
  if (!canConnect.value) return
  modelController?.abort()
  const controller = new AbortController()
  modelController = controller
  syncingModels.value = true
  error.value = ''
  try {
    const result = await syncUpstreamModelsPreview({
      ...props.connection, platform: 'cline', type: 'apikey',
      model_mapping: Object.fromEntries(Object.entries(props.modelMapping ?? {}).filter((entry): entry is [string, string] => typeof entry[1] === 'string'))
    }, controller.signal)
    if (controller.signal.aborted) return
    catalog.value = result.models
    catalogStatus.value = tr('modelsLoaded', { count: result.models.length })
  } catch (err) {
    if (!controller.signal.aborted) error.value = extractApiErrorMessage(err, tr('modelsFailed'))
  } finally {
    if (modelController === controller) { syncingModels.value = false; modelController = undefined }
  }
}
async function probe(row: ClineUpstreamRule, kind: 'discover' | 'verify') {
  if (!canConnect.value) return
  if (kind === 'verify' && row.mode === 'strict' && !row.upstream.trim()) {
    stateFor(row).error = tr('providerRequired')
    return
  }
  invalidate(row)
  const state = stateFor(row)
  const controller = new AbortController()
  const version = connectionVersion
  controllers.set(row.model, controller)
  state.busy = kind
  try {
    const params = { ...props.connection, model: row.model.trim() }
    if (kind === 'discover') {
      const result = await discoverClineUpstreams(params, controller.signal)
      if (controller.signal.aborted || version !== connectionVersion || !props.rows.includes(row)) return
      state.discovery = result
      row.pipeline = result.pipeline
      if (row.pipeline === 'direct') row.has = []
    } else {
      const result = await verifyClineUpstream({ ...params, pin: clineRuleToPin(row), stream: state.stream }, controller.signal)
      if (controller.signal.aborted || version !== connectionVersion || !props.rows.includes(row)) return
      state.result = result
    }
  } catch (err) {
    if (!controller.signal.aborted && version === connectionVersion) state.error = extractApiErrorMessage(err, tr('probeFailed'))
  } finally {
    if (controllers.get(row.model) === controller) {
      controllers.delete(row.model)
      delete state.busy
    }
  }
}
const discover = (row: ClineUpstreamRule) => probe(row, 'discover')
const verify = (row: ClineUpstreamRule) => probe(row, 'verify')
const resultClass = (result: ClineUpstreamVerification) => {
  if (result.status === 'matched' && result.completed) return 'bg-emerald-50 text-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-300'
  if (result.status === 'mismatch' || result.status === 'unavailable') return 'bg-red-50 text-red-700 dark:bg-red-900/20 dark:text-red-300'
  return 'bg-amber-50 text-amber-800 dark:bg-amber-900/20 dark:text-amber-300'
}
function cancelAll() {
  connectionVersion++
  controllers.forEach(controller => controller.abort())
  controllers.clear()
  modelController?.abort()
  modelController = undefined
  syncingModels.value = false
}
watch(() => JSON.stringify(props.connection), () => {
  cancelAll()
  states.value = Object.create(null)
  catalog.value = []
  catalogStatus.value = ''
  error.value = ''
})
if (props.discoverySnapshot && typeof props.discoverySnapshot === 'object' && !Array.isArray(props.discoverySnapshot)) {
  for (const [model, value] of Object.entries(props.discoverySnapshot)) {
    if (!isValidClineModelID(model) || !value || typeof value !== 'object' || Array.isArray(value)) continue
    const snapshot = value as Partial<ClineUpstreamDiscovery>
    if (snapshot.model !== model || !Array.isArray(snapshot.providers) || typeof snapshot.discovered_at !== 'string') continue
    if (snapshot.pipeline !== 'planner' && snapshot.pipeline !== 'direct' && snapshot.pipeline !== 'unknown') continue
    states.value[model] = {
      stream: true,
      discovery: {
        model,
        pipeline: snapshot.pipeline,
        discovered_at: snapshot.discovered_at,
        providers: snapshot.providers.filter(provider =>
          provider && typeof provider.id === 'string' && provider.id.trim() && typeof provider.name === 'string'
        ),
        ...(typeof snapshot.warning === 'string' ? { warning: snapshot.warning } : {})
      }
    }
  }
}
onBeforeUnmount(cancelAll)
defineExpose({ validate })
</script>
