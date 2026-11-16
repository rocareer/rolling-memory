<!-- plugin: ai v0.2.0 -->
<!--
  用量统计页（自取数，不走表格管家）。
  契约：
  - POST /admin/plugin/ai/usage/list → data={list,totals,filters{sources,biz_types}}；
    group=model|biz|source|biz_source|day（day 按天倒序，其余按 calls 降序），无分页、聚合行上限 100。
  - POST /admin/plugin/ai/usage-log/list → data={list,total}（分页；时间范围用显式 start/end 秒）。
  - POST /admin/plugin/ai/usage-log/detail → 尝试链（usage/detail.vue 承担）。
  时间范围必须用独立 el-date-picker（value-format='x'），不交给 comSearch 的 datetime/BETWEEN：
  时间列是 bigint 秒，comSearch 会发 'YYYY-MM-DD HH:mm:ss' 字符串，必然不命中。
-->
<template>
    <div class="default-main plugin-ai-usage">
        <el-alert class="usage-alert" type="info" :closable="false" show-icon :title="t('plugin.ai.usage.intro')" />

        <el-tabs v-model="activeTab" class="usage-tabs">
            <el-tab-pane :label="t('plugin.ai.usage.tabs.aggregate')" name="aggregate">
                <div class="filter-bar">
                    <el-input
                        v-model="aggFilters.model_name"
                        class="filter-input"
                        clearable
                        :placeholder="t('plugin.ai.usage.filters.model')"
                        @keyup.enter="loadAggregate"
                    />
                    <el-select v-model="aggFilters.biz_type" class="filter-select" clearable :placeholder="t('plugin.ai.usage.filters.bizType')">
                        <el-option v-for="item in filterOptions.biz_types" :key="item" :label="item" :value="item" />
                    </el-select>
                    <el-select v-model="aggFilters.source" class="filter-select" clearable :placeholder="t('plugin.ai.usage.filters.source')">
                        <el-option v-for="item in filterOptions.sources" :key="item" :label="item" :value="item" />
                    </el-select>
                    <el-select v-model="aggFilters.group" class="filter-select" :placeholder="t('plugin.ai.usage.filters.group')">
                        <el-option :label="t('plugin.ai.usage.group.model')" value="model" />
                        <el-option :label="t('plugin.ai.usage.group.biz')" value="biz" />
                        <el-option :label="t('plugin.ai.usage.group.source')" value="source" />
                        <el-option :label="t('plugin.ai.usage.group.bizSource')" value="biz_source" />
                        <el-option :label="t('plugin.ai.usage.group.day')" value="day" />
                    </el-select>
                    <el-date-picker
                        v-model="aggFilters.range"
                        class="filter-range"
                        type="datetimerange"
                        value-format="x"
                        :start-placeholder="t('plugin.ai.usage.filters.dateRange')"
                        :end-placeholder="t('plugin.ai.usage.filters.dateRange')"
                    />
                    <el-button v-blur type="primary" :loading="aggLoading" @click="loadAggregate">
                        {{ t('plugin.ai.usage.filters.query') }}
                    </el-button>
                    <el-button v-blur @click="resetAggregate">{{ t('plugin.ai.usage.filters.reset') }}</el-button>
                </div>

                <div class="summary-grid">
                    <div class="summary-card">
                        <div class="summary-label">{{ t('plugin.ai.usage.totals.calls') }}</div>
                        <div class="summary-value">{{ formatInt(totals.calls) }}</div>
                    </div>
                    <div class="summary-card success">
                        <div class="summary-label">{{ t('plugin.ai.usage.totals.success') }}</div>
                        <div class="summary-value">{{ formatInt(totals.success) }}</div>
                    </div>
                    <div class="summary-card danger">
                        <div class="summary-label">{{ t('plugin.ai.usage.totals.fail') }}</div>
                        <div class="summary-value">{{ formatInt(totals.fail) }}</div>
                    </div>
                    <div class="summary-card warning">
                        <div class="summary-label">{{ t('plugin.ai.usage.totals.timeout') }}</div>
                        <div class="summary-value">{{ formatInt(totals.timeout) }}</div>
                    </div>
                    <div class="summary-card">
                        <div class="summary-label">{{ t('plugin.ai.usage.totals.tokens') }}</div>
                        <div class="summary-value">{{ formatInt(totals.total_tokens) }}</div>
                    </div>
                    <div class="summary-card">
                        <div class="summary-label">{{ t('plugin.ai.usage.totals.cost') }}</div>
                        <div class="summary-value">{{ formatCost(totals.cost) }}</div>
                    </div>
                </div>

                <el-table v-loading="aggLoading" :data="aggRows" size="small" border stripe :empty-text="t('plugin.ai.usage.empty')">
                    <el-table-column :label="t(`plugin.ai.usage.groupColumn.${groupMeta.labelKey}`)" min-width="180" show-overflow-tooltip>
                        <template #default="scope">{{ groupValue(scope.row) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.columns.calls')" prop="calls" width="110" align="right">
                        <template #default="scope">{{ formatInt(scope.row.calls) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.columns.success')" prop="success" width="110" align="right">
                        <template #default="scope">{{ formatInt(scope.row.success) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.columns.fail')" prop="fail" width="110" align="right">
                        <template #default="scope">{{ formatInt(scope.row.fail) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.columns.timeout')" prop="timeout" width="110" align="right">
                        <template #default="scope">{{ formatInt(scope.row.timeout) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.columns.tokens')" prop="total_tokens" width="130" align="right">
                        <template #default="scope">{{ formatInt(scope.row.total_tokens) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.columns.cost')" prop="cost" width="130" align="right">
                        <template #default="scope">{{ formatCost(scope.row.cost) }}</template>
                    </el-table-column>
                </el-table>
            </el-tab-pane>

            <el-tab-pane v-if="canLogRead" :label="t('plugin.ai.usage.tabs.logs')" name="logs">
                <el-alert class="usage-alert" type="info" :closable="false" show-icon :title="t('plugin.ai.usage.log.intro')" />

                <div class="filter-bar">
                    <el-select v-model="logFilters.status" class="filter-select" clearable :placeholder="t('plugin.ai.usage.log.filters.status')">
                        <el-option :label="t('plugin.ai.usage.log.status.success')" value="success" />
                        <el-option :label="t('plugin.ai.usage.log.status.fail')" value="fail" />
                        <el-option :label="t('plugin.ai.usage.log.status.timeout')" value="timeout" />
                        <el-option :label="t('plugin.ai.usage.log.status.breaker')" value="breaker" />
                    </el-select>
                    <el-select v-model="logFilters.call_type" class="filter-select" clearable :placeholder="t('plugin.ai.usage.log.filters.callType')">
                        <el-option :label="t('plugin.ai.usage.log.callType.chat')" value="chat" />
                        <el-option :label="t('plugin.ai.usage.log.callType.embedding')" value="embedding" />
                        <el-option :label="t('plugin.ai.usage.log.callType.tts')" value="tts" />
                    </el-select>
                    <el-select v-model="logFilters.biz_type" class="filter-select" clearable :placeholder="t('plugin.ai.usage.log.filters.bizType')">
                        <el-option v-for="item in filterOptions.biz_types" :key="item" :label="item" :value="item" />
                    </el-select>
                    <el-select v-model="logFilters.source" class="filter-select" clearable :placeholder="t('plugin.ai.usage.log.filters.source')">
                        <el-option v-for="item in filterOptions.sources" :key="item" :label="item" :value="item" />
                    </el-select>
                    <el-select
                        v-model="logFilters.tokens_source"
                        class="filter-select"
                        clearable
                        :placeholder="t('plugin.ai.usage.log.filters.tokensSource')"
                    >
                        <el-option :label="t('plugin.ai.usage.log.tokensSource.usage')" value="usage" />
                        <el-option :label="t('plugin.ai.usage.log.tokensSource.estimate')" value="estimate" />
                    </el-select>
                    <el-input
                        v-model="logFilters.model_name"
                        class="filter-input"
                        clearable
                        :placeholder="t('plugin.ai.usage.log.filters.modelName')"
                        @keyup.enter="applyLogFilters"
                    />
                    <el-input
                        v-model="logFilters.channel_name"
                        class="filter-input"
                        clearable
                        :placeholder="t('plugin.ai.usage.log.filters.channelName')"
                        @keyup.enter="applyLogFilters"
                    />
                    <el-input
                        v-model="logFilters.request_id"
                        class="filter-input"
                        clearable
                        :placeholder="t('plugin.ai.usage.log.filters.requestId')"
                        @keyup.enter="applyLogFilters"
                    />
                    <el-date-picker
                        v-model="logFilters.range"
                        class="filter-range"
                        type="datetimerange"
                        value-format="x"
                        :start-placeholder="t('plugin.ai.usage.log.filters.dateRange')"
                        :end-placeholder="t('plugin.ai.usage.log.filters.dateRange')"
                    />
                    <el-button v-blur type="primary" :loading="logLoading" @click="applyLogFilters">
                        {{ t('plugin.ai.usage.log.filters.query') }}
                    </el-button>
                    <el-button v-blur @click="resetLogFilters">{{ t('plugin.ai.usage.log.filters.reset') }}</el-button>
                </div>

                <div class="log-legend">
                    <span class="legend-title">{{ t('plugin.ai.usage.log.legend.title') }}</span>
                    <el-tooltip :content="t('plugin.ai.usage.log.legend.tokensSourceTip')" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                        <span class="legend-item">{{ t('plugin.ai.usage.log.legend.tokensSource') }}</span>
                    </el-tooltip>
                    <el-tooltip :content="t('plugin.ai.usage.log.billedPartial.tip')" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                        <span class="legend-item">{{ t('plugin.ai.usage.log.legend.billedPartial') }}</span>
                    </el-tooltip>
                    <el-tooltip :content="t('plugin.ai.usage.log.legend.keySourceTip')" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                        <span class="legend-item">{{ t('plugin.ai.usage.log.legend.keySource') }}</span>
                    </el-tooltip>
                </div>

                <el-table v-loading="logLoading" :data="logs" size="small" border stripe :empty-text="t('plugin.ai.usage.log.empty')">
                    <el-table-column :label="t('plugin.ai.usage.log.columns.time')" width="160">
                        <template #default="scope">{{ formatSeconds(scope.row.create_time) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.log.columns.requestId')" min-width="240" show-overflow-tooltip>
                        <template #default="scope">
                            <span class="mono">{{ scope.row.request_id || '-' }}</span>
                        </template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.log.columns.model')" prop="model_name" min-width="140" show-overflow-tooltip />
                    <el-table-column :label="t('plugin.ai.usage.log.columns.channel')" prop="channel_name" min-width="120" show-overflow-tooltip />
                    <el-table-column :label="t('plugin.ai.usage.log.columns.callType')" width="100" align="center">
                        <template #default="scope">
                            <el-tag size="small" type="info">{{ callTypeLabel(scope.row.call_type) }}</el-tag>
                        </template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.log.columns.bizType')" prop="biz_type" width="110" show-overflow-tooltip />
                    <!-- source = 计量归因维度：key:<调用方> 是应用级 API Key（source 接缝写入），key:gateway 为插件全局密钥 -->
                    <el-table-column :label="t('plugin.ai.usage.log.columns.source')" prop="source" width="150" align="center">
                        <template #default="scope">
                            <el-tag v-if="scope.row.source" size="small" :type="sourceTagType(scope.row.source)" effect="plain">
                                {{ scope.row.source }}
                            </el-tag>
                            <span v-else>-</span>
                        </template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.log.columns.status')" width="95" align="center">
                        <template #default="scope">
                            <el-tag size="small" :type="statusTagType(scope.row.status)">{{ statusLabel(scope.row.status) }}</el-tag>
                        </template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.log.columns.promptTokens')" prop="prompt_tokens" width="115" align="right">
                        <template #default="scope">{{ formatInt(scope.row.prompt_tokens) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.log.columns.completionTokens')" prop="completion_tokens" width="115" align="right">
                        <template #default="scope">{{ formatInt(scope.row.completion_tokens) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.log.columns.totalTokens')" prop="total_tokens" width="120" align="right">
                        <template #default="scope">{{ formatInt(scope.row.total_tokens) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.log.columns.cost')" prop="cost" width="120" align="right">
                        <template #default="scope">{{ formatCost(scope.row.cost) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.log.columns.latency')" prop="latency_ms" width="115" align="right">
                        <template #default="scope">{{ formatInt(scope.row.latency_ms) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.log.columns.stream')" width="100" align="center">
                        <template #default="scope">
                            <el-tag size="small" :type="Number(scope.row.stream) === 1 ? 'success' : 'info'">
                                {{ Number(scope.row.stream) === 1 ? t('plugin.ai.usage.log.stream.on') : t('plugin.ai.usage.log.stream.off') }}
                            </el-tag>
                        </template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.log.columns.tokensSource')" width="130" align="center">
                        <template #default="scope">
                            <el-tooltip :content="t('plugin.ai.usage.log.legend.tokensSourceTip')" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                                <span>{{ tokensSourceLabel(scope.row.tokens_source) }}</span>
                            </el-tooltip>
                        </template>
                    </el-table-column>
                    <!-- 部分计费标记（billed_partial，迁移 2）：上游未收尾但已按已聚合内容计费 -->
                    <el-table-column
                        :label="t('plugin.ai.usage.log.columns.billedPartial')"
                        width="120"
                        align="center"
                        fixed="right"
                    >
                        <template #default="scope">
                            <el-tooltip
                                v-if="Number(scope.row.billed_partial) === 1"
                                :content="t('plugin.ai.usage.log.billedPartial.tip')"
                                placement="top"
                                :show-after="250"
                                :enterable="false"
                                :hide-after="0"
                            >
                                <el-tag size="small" type="warning" effect="dark">{{ t('plugin.ai.usage.log.billedPartial.yes') }}</el-tag>
                            </el-tooltip>
                            <el-tag v-else size="small" type="info" effect="plain">{{ t('plugin.ai.usage.log.billedPartial.no') }}</el-tag>
                        </template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.log.columns.errorClass')" width="110" align="center">
                        <template #default="scope">{{ errorClassLabel(scope.row.error_class) }}</template>
                    </el-table-column>
                    <el-table-column
                        :label="t('plugin.ai.usage.log.columns.errorMessage')"
                        prop="error_message"
                        min-width="220"
                        show-overflow-tooltip
                    />
                    <el-table-column
                        v-if="canLogDetail"
                        :label="t('plugin.ai.usage.log.columns.operate')"
                        width="90"
                        align="center"
                        fixed="right"
                    >
                        <template #default="scope">
                            <el-button link type="primary" size="small" @click="openDetail(scope.row)">
                                {{ t('plugin.ai.usage.log.detail') }}
                            </el-button>
                        </template>
                    </el-table-column>
                </el-table>

                <div class="log-pagination">
                    <el-pagination
                        background
                        :current-page="logPage"
                        :page-size="logLimit"
                        :page-sizes="[10, 20, 50, 100]"
                        :total="logTotal"
                        layout="total, sizes, prev, pager, next"
                        @current-change="onLogPageChange"
                        @size-change="onLogSizeChange"
                    />
                </div>
            </el-tab-pane>
        </el-tabs>

        <UsageDetail v-model="detailVisible" :request-id="detailRequestId" />
    </div>
</template>

<script setup lang="ts">
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { auth } from '@/utils/common'
import request from '@/utils/request'
import { dayjs, ElMessage } from 'element-plus'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import UsageDetail from './detail.vue'

defineOptions({
    name: 'plugin/ai/usage',
})

const { t } = useI18n()

// 调用日志节点的父菜单是 plugin/ai/usage-log（authNode 键 = 前缀 + 该菜单名）
const logPagePath = `${adminBaseRoutePath}/plugin/ai/usage-log`
const canLog = (node: string) => auth({ name: logPagePath, subNodeName: `${logPagePath}/${node}` })
const canLogRead = canLog('read')
const canLogDetail = canLog('detail')

type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'

interface AggregateRow {
    group?: string
    model_id?: number
    model_name?: string
    model?: string
    biz_type?: string
    source?: string
    biz_source?: string
    day?: string
    calls?: number
    success?: number
    fail?: number
    timeout?: number
    total_tokens?: number
    cost?: string | number
}

interface Totals {
    calls?: number
    success?: number
    fail?: number
    timeout?: number
    total_tokens?: number
    cost?: string | number
}

interface UsageData {
    list?: AggregateRow[]
    totals?: Totals
    filters?: { sources?: string[]; biz_types?: string[] }
}

interface UsageLogRow {
    request_id?: string
    attempt?: number
    model_name?: string
    channel_name?: string
    call_type?: string
    biz_type?: string
    source?: string
    status?: string
    prompt_tokens?: number
    completion_tokens?: number
    total_tokens?: number
    cost?: string | number
    latency_ms?: number
    stream?: number
    tokens_source?: string
    billed_partial?: number
    error_class?: string
    error_message?: string
    create_time?: number
}

interface UsageLogData {
    list?: UsageLogRow[]
    total?: number
}

const activeTab = ref<string>('aggregate')

const formatSeconds = (value?: number | null) => (value ? dayjs.unix(Number(value)).format('YYYY-MM-DD HH:mm:ss') : '-')
const formatInt = (value?: number | null) => (value === null || value === undefined ? '-' : Number(value).toLocaleString())
const formatCost = (value?: string | number | null) => (value === null || value === undefined || value === '' ? '-' : String(value))

const statusLabel = (status?: string) => (status ? t(`plugin.ai.usage.log.status.${status}`) : '-')
const statusTagType = (status?: string): TagType => {
    if (status === 'success') return 'success'
    if (status === 'timeout') return 'warning'
    if (status === 'breaker') return 'info'
    return 'danger'
}
const callTypeLabel = (callType?: string) => (callType ? t(`plugin.ai.usage.log.callType.${callType}`) : '-')
const tokensSourceLabel = (source?: string) => {
    if (!source) return t('plugin.ai.usage.log.tokensSource.none')
    return t(`plugin.ai.usage.log.tokensSource.${source}`)
}
const errorClassLabel = (value?: string) => (value ? t(`plugin.ai.usage.log.errorClass.${value}`) : '-')
// 按 Key 归因：'key:gateway' = 插件全局密钥；'key:<调用方>' = 应用级 API Key（source 接缝写入）
const sourceTagType = (source?: string): TagType => {
    if (source === 'key:gateway') return 'primary'
    if (source && source.startsWith('key:')) return 'warning'
    return 'info'
}

const rangePayload = (range: string[]) => {
    if (!Array.isArray(range) || range.length < 2 || !range[0] || !range[1]) return {}
    const start = Math.floor(Number(range[0]) / 1000)
    const end = Math.floor(Number(range[1]) / 1000)
    if (!Number.isFinite(start) || !Number.isFinite(end)) return {}
    return { start, end }
}

/* ---------------- 聚合统计 ---------------- */
const aggLoading = ref(false)
const aggRows = ref<AggregateRow[]>([])
const totals = ref<Totals>({})
const filterOptions = reactive<{ sources: string[]; biz_types: string[] }>({ sources: [], biz_types: [] })
const aggFilters = reactive({
    model_name: '',
    biz_type: '',
    source: '',
    group: 'model',
    range: [] as string[],
})

const GROUP_META: Record<string, { labelKey: string }> = {
    model: { labelKey: 'model' },
    biz: { labelKey: 'biz' },
    source: { labelKey: 'source' },
    biz_source: { labelKey: 'bizSource' },
    day: { labelKey: 'day' },
}
const appliedGroup = ref('model')
const groupMeta = computed(() => GROUP_META[appliedGroup.value] ?? GROUP_META.model)

const groupValue = (row: AggregateRow) => {
    if (appliedGroup.value === 'model') return row.model_name || row.model || '-'
    if (appliedGroup.value === 'biz') return row.biz_type || row.group || '-'
    if (appliedGroup.value === 'source') return row.source || row.group || '-'
    if (appliedGroup.value === 'biz_source') return row.group || `${row.biz_type ?? '-'} | ${row.source ?? '-'}`
    if (appliedGroup.value === 'day') return row.day || row.group || '-'
    return '-'
}

const loadAggregate = async () => {
    const requestedGroup = aggFilters.group
    const payload: AnyObj = { group: requestedGroup, ...rangePayload(aggFilters.range) }
    if (aggFilters.model_name.trim()) payload.model_name = aggFilters.model_name.trim()
    if (aggFilters.biz_type) payload.biz_type = aggFilters.biz_type
    if (aggFilters.source) payload.source = aggFilters.source
    aggLoading.value = true
    try {
        const res = await request<UsageData>(
            { url: '/admin/plugin/ai/usage/list', method: 'POST', data: payload },
            { cancelDuplicate: false }
        )
        const data = res.data.data ?? {}
        aggRows.value = data.list ?? []
        totals.value = data.totals ?? {}
        appliedGroup.value = requestedGroup
        filterOptions.sources = data.filters?.sources ?? filterOptions.sources
        filterOptions.biz_types = data.filters?.biz_types ?? filterOptions.biz_types
    } catch {
        // 请求层已提示；保留上一次成功结果（维度标签也保持与旧数据一致）
    } finally {
        aggLoading.value = false
    }
}

const resetAggregate = () => {
    aggFilters.model_name = ''
    aggFilters.biz_type = ''
    aggFilters.source = ''
    aggFilters.group = 'model'
    aggFilters.range = []
    loadAggregate()
}

/* ---------------- 调用日志 ---------------- */
const logLoading = ref(false)
const logs = ref<UsageLogRow[]>([])
const logTotal = ref(0)
const logPage = ref(1)
const logLimit = ref(20)
const logLoaded = ref(false)
const logFilters = reactive({
    status: '',
    call_type: '',
    biz_type: '',
    source: '',
    tokens_source: '',
    request_id: '',
    model_name: '',
    channel_name: '',
    range: [] as string[],
})

const loadLogs = async () => {
    const payload: AnyObj = { page: logPage.value, limit: logLimit.value, ...rangePayload(logFilters.range) }
    for (const key of ['status', 'call_type', 'biz_type', 'source', 'tokens_source'] as const) {
        if (logFilters[key]) payload[key] = logFilters[key]
    }
    for (const key of ['request_id', 'model_name', 'channel_name'] as const) {
        const value = logFilters[key].trim()
        if (value) payload[key] = value
    }
    logLoading.value = true
    try {
        const res = await request<UsageLogData>(
            { url: '/admin/plugin/ai/usage-log/list', method: 'POST', data: payload },
            { cancelDuplicate: false }
        )
        const data = res.data.data ?? {}
        logs.value = data.list ?? []
        logTotal.value = Number(data.total ?? 0)
        logLoaded.value = true
    } catch {
        // 请求层已提示；保留上一次结果
    } finally {
        logLoading.value = false
    }
}

const applyLogFilters = () => {
    logPage.value = 1
    loadLogs()
}

const resetLogFilters = () => {
    logFilters.status = ''
    logFilters.call_type = ''
    logFilters.biz_type = ''
    logFilters.source = ''
    logFilters.tokens_source = ''
    logFilters.request_id = ''
    logFilters.model_name = ''
    logFilters.channel_name = ''
    logFilters.range = []
    logPage.value = 1
    loadLogs()
}

const onLogPageChange = (page: number) => {
    logPage.value = page
    loadLogs()
}

const onLogSizeChange = (size: number) => {
    logLimit.value = size
    logPage.value = 1
    loadLogs()
}

watch(activeTab, (tab) => {
    if (tab === 'logs' && !logLoaded.value) {
        loadLogs()
    }
})

/* ---------------- 尝试链详情 ---------------- */
const detailVisible = ref(false)
const detailRequestId = ref('')

const openDetail = (row: UsageLogRow) => {
    if (!row.request_id) {
        ElMessage.warning(t('plugin.ai.usage.log.noRequestId'))
        return
    }
    detailRequestId.value = row.request_id
    detailVisible.value = true
}

onMounted(() => {
    loadAggregate()
})
</script>

<style scoped lang="scss">
.plugin-ai-usage {
    .usage-alert {
        margin-bottom: 10px;
    }

    .usage-tabs {
        :deep(.el-tabs__content) {
            overflow: visible;
        }
    }

    .filter-bar {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
        margin-bottom: 12px;
    }

    .filter-select {
        width: 150px;
    }

    .filter-input {
        width: 170px;
    }

    .filter-range {
        max-width: 380px;
    }

    .summary-grid {
        display: grid;
        grid-template-columns: repeat(6, minmax(0, 1fr));
        gap: 10px;
        margin-bottom: 12px;
    }

    .summary-card {
        padding: 12px 14px;
        border: 1px solid var(--el-border-color-lighter);
        border-left: 3px solid var(--el-color-primary);
        border-radius: 6px;
        background: var(--el-bg-color-overlay);

        &.success {
            border-left-color: var(--el-color-success);
        }

        &.danger {
            border-left-color: var(--el-color-danger);
        }

        &.warning {
            border-left-color: var(--el-color-warning);
        }
    }

    .summary-label {
        font-size: 13px;
        color: var(--el-text-color-secondary);
    }

    .summary-value {
        margin-top: 6px;
        font-size: 22px;
        font-weight: 600;
        word-break: break-all;
    }

    .mono {
        font-family: var(--el-font-family-mono, monospace);
        font-size: 12px;
    }

    .log-legend {
        display: flex;
        align-items: center;
        gap: 14px;
        flex-wrap: wrap;
        margin-bottom: 8px;
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .legend-title {
        font-weight: 600;
        color: var(--el-text-color-primary);
    }

    .legend-item {
        cursor: help;
        border-bottom: 1px dashed var(--el-border-color-darker);
    }

    .log-pagination {
        display: flex;
        justify-content: flex-end;
        margin-top: 12px;
    }
}

@media (max-width: 1200px) {
    .plugin-ai-usage .summary-grid {
        grid-template-columns: repeat(2, minmax(0, 1fr));
    }
}
</style>
