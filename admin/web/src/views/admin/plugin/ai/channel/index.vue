<!-- plugin: ai v0.2.0 -->
<!--
  渠道管理页（标准 CRUD 三件套 + 熔断统计条 + 熔断详情弹窗）。
  契约：POST /admin/plugin/ai/channel/list → data={list,total,stats}；stats 固定四字段
  circuit_open / circuit_half_open / problem / fail_1h（照源 Channel.php:216-231）。
  行数据新增四列：driver（驱动名，空 = 默认 OpenAI 兼容）、supplier_id（0 = 独立渠道）、
  rpm_limit / tpm_limit（0 = 跟随插件设置的渠道 Gate 缺省）。
  行内动作：编辑 / 启停 / 人工熔断 / 恢复 / 探活（显式 timeout=20s，服务端上限 15s）/ 熔断详情 / 删除。
  页面不出现「下次探活时间」类提示：probe_fail_streak 只记数，v1 不做退避调度。
-->
<template>
    <div class="default-main plugin-ai-channel">
        <el-alert class="channel-alert" type="info" :closable="false" show-icon :title="t('plugin.ai.channel.intro')" />

        <div class="channel-stats">
            <div class="stat-item danger">
                <span class="stat-label">{{ t('plugin.ai.channel.stats.circuitOpen') }}</span>
                <span class="stat-value">{{ stats.circuit_open }}</span>
            </div>
            <div class="stat-item warning">
                <span class="stat-label">{{ t('plugin.ai.channel.stats.circuitHalfOpen') }}</span>
                <span class="stat-value">{{ stats.circuit_half_open }}</span>
            </div>
            <div class="stat-item">
                <span class="stat-label">{{ t('plugin.ai.channel.stats.problem') }}</span>
                <span class="stat-value">{{ stats.problem }}</span>
            </div>
            <div class="stat-item">
                <span class="stat-label">{{ t('plugin.ai.channel.stats.fail1h') }}</span>
                <span class="stat-value">{{ stats.fail_1h }}</span>
            </div>
        </div>

        <div class="channel-filters">
            <el-select v-model="preset.status" class="filter-select" clearable :placeholder="t('plugin.ai.channel.filters.status')">
                <el-option :label="t('plugin.ai.channel.status.on')" :value="1" />
                <el-option :label="t('plugin.ai.channel.status.off')" :value="0" />
            </el-select>
            <el-select v-model="preset.vendor" class="filter-select" clearable :placeholder="t('plugin.ai.channel.filters.vendor')">
                <el-option v-for="item in vendorOptions" :key="item.value" :label="item.label" :value="item.value" />
            </el-select>
            <el-button v-blur type="primary" @click="applyPreset">{{ t('plugin.ai.channel.filters.query') }}</el-button>
            <el-button v-blur @click="resetPreset">{{ t('plugin.ai.channel.filters.reset') }}</el-button>
        </div>

        <TableHeader
            :manager="tableManager"
            v-model:com-search="tableManager.comSearch"
            :buttons="['refresh', 'add', 'delete', 'comSearch', 'quickSearch', 'columnDisplay']"
            :quick-search-placeholder="t('plugin.ai.channel.searchPlaceholder')"
        />

        <Table :manager="tableManager" :empty-text="t('plugin.ai.channel.empty')" />

        <ChannelForm
            :manager="tableManager"
            :suppliers="supplierOptions"
            :supplier-load-failed="supplierLoadFailed"
            v-model:form-items="tableManager.form.items!"
        />

        <!-- 熔断详情：状态 + 冷却剩余 + 累计统计 + 最近 20 条事件时间线 -->
        <el-dialog v-model="breakerVisible" :title="breakerTitle" width="min(760px, 96vw)" :destroy-on-close="true">
            <div v-loading="breakerLoading" class="breaker-body">
                <template v-if="breakerRow">
                    <el-descriptions :column="2" border size="small">
                        <el-descriptions-item :label="t('plugin.ai.channel.breaker.circuit')">
                            <el-tag size="small" :type="circuitCustom[breakerRow.circuit] ?? 'info'">
                                {{ circuitDict[breakerRow.circuit] ?? breakerRow.circuit }}
                            </el-tag>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.channel.breaker.openedAt')">
                            {{ formatSeconds(breakerRow.opened_at) }}
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.channel.breaker.cooldownRemaining')">
                            <template v-if="breakerRow.circuit === 'closed' || cooldownRemaining === null">-</template>
                            <template v-else-if="cooldownRemaining > 0">
                                {{ t('plugin.ai.channel.breaker.cooldownRemainingValue', { seconds: cooldownRemaining }) }}
                            </template>
                            <span v-else class="cooldown-expired">{{ t('plugin.ai.channel.breaker.cooldownExpired') }}</span>
                            <div v-if="Number(breakerRow.cooldown_seconds) === 0" class="form-tip">
                                {{ t('plugin.ai.channel.breaker.cooldownGlobalTip') }}
                            </div>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.channel.breaker.consecutive')">
                            {{ breakerRow.consecutive_failures }} / {{ effectiveThreshold }}
                            <span v-if="Number(breakerRow.fail_threshold) === 0" class="form-tip-inline">
                                ({{ t('plugin.ai.channel.breaker.globalDefault') }})
                            </span>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.channel.breaker.totalCalls')">{{ breakerRow.total_calls }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.channel.breaker.totalFailures')">
                            {{ breakerRow.total_failures }}
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.channel.breaker.totalTokens')">{{ breakerRow.total_tokens }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.channel.breaker.lastSuccess')">
                            {{ formatSeconds(breakerRow.last_success_at) }}
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.channel.breaker.lastError')" :span="2">
                            {{ breakerRow.last_error || '-' }}
                        </el-descriptions-item>
                    </el-descriptions>

                    <div class="breaker-events-title">{{ t('plugin.ai.channel.breaker.events') }}</div>
                    <el-timeline v-if="breakerEvents.length" class="breaker-timeline">
                        <el-timeline-item
                            v-for="item in breakerEvents"
                            :key="`${item.id}-${item.create_time}`"
                            :timestamp="formatSeconds(item.create_time)"
                            :type="eventTimelineType(item.event)"
                            placement="top"
                        >
                            <div class="event-head">
                                <el-tag size="small" :type="eventTimelineType(item.event)">
                                    {{ eventLabel(item.event) }}
                                </el-tag>
                                <span class="event-meta">
                                    {{ t('plugin.ai.channel.breaker.eventFailures') }}：{{ item.consecutive_failures }} /
                                    {{ t('plugin.ai.channel.breaker.eventThreshold') }}：{{ item.fail_threshold }} /
                                    {{ t('plugin.ai.channel.breaker.eventCooldown') }}：{{ item.cooldown_seconds }}
                                </span>
                            </div>
                            <div v-if="item.last_error" class="event-error">{{ item.last_error }}</div>
                        </el-timeline-item>
                    </el-timeline>
                    <el-empty v-else :description="t('plugin.ai.channel.breaker.eventsEmpty')" :image-size="50" />
                </template>
                <el-empty v-else :description="t('common.noData')" :image-size="50" />
            </div>
        </el-dialog>
    </div>
</template>

<script setup lang="ts">
import TableHeader from '@/components/table/header/index.vue'
import Table from '@/components/table/index.vue'
import { TableManagerAPI } from '@/api/table'
import { useTableManager } from '@/hooks/useTableManager'
import { auth, getCurrentRoutePath } from '@/utils/common'
import request from '@/utils/request'
import { dayjs, ElMessage } from 'element-plus'
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import ChannelForm from './form.vue'

defineOptions({
    name: 'plugin/ai/channel',
})

const { t } = useI18n()

const pagePath = getCurrentRoutePath()
const can = (node: string) => auth({ name: pagePath, subNodeName: `${pagePath}/${node}` })

type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'

interface ChannelStats {
    circuit_open: number
    circuit_half_open: number
    problem: number
    fail_1h: number
}

interface BreakerEventRow extends TableRow {
    id?: number
    event?: string
    consecutive_failures?: number
    fail_threshold?: number
    cooldown_seconds?: number
    last_error?: string
    create_time?: number
}

interface ProbeResult {
    ok?: boolean
    latency_ms?: number
    http_status?: number
    error?: string
    models_count?: number
}

const vendorDict: Record<string, string> = {
    openai: t('plugin.ai.channel.vendor.openai'),
    deepseek: t('plugin.ai.channel.vendor.deepseek'),
    qwen: t('plugin.ai.channel.vendor.qwen'),
    moonshot: t('plugin.ai.channel.vendor.moonshot'),
    zhipu: t('plugin.ai.channel.vendor.zhipu'),
    ollama: t('plugin.ai.channel.vendor.ollama'),
    other: t('plugin.ai.channel.vendor.other'),
}
const statusDict: Record<string, string> = {
    1: t('plugin.ai.channel.status.on'),
    0: t('plugin.ai.channel.status.off'),
}
const statusCustom: Record<string, TagType> = { 1: 'success', 0: 'info' }
const circuitDict: Record<string, string> = {
    closed: t('plugin.ai.channel.circuit.closed'),
    open: t('plugin.ai.channel.circuit.open'),
    half_open: t('plugin.ai.channel.circuit.half_open'),
}
const circuitCustom: Record<string, TagType> = { closed: 'success', open: 'danger', half_open: 'warning' }
const probeDict: Record<string, string> = {
    1: t('plugin.ai.channel.probe.okShort'),
    0: t('plugin.ai.channel.probe.failShort'),
    never: t('plugin.ai.channel.probe.never'),
}
const probeCustom: Record<string, TagType> = { 1: 'success', 0: 'danger', never: 'info' }
// 空 driver = 默认驱动：tag 渲染器对空值直接不渲染，故用哨兵值 'default' 承载「空」这一态
const DRIVER_DEFAULT = 'default'
const driverDict: Record<string, string> = {
    [DRIVER_DEFAULT]: t('plugin.ai.channel.driver.inherit'),
    openai: t('plugin.ai.channel.driver.openai'),
    responses: t('plugin.ai.channel.driver.responses'),
    deepseek: t('plugin.ai.channel.driver.deepseek'),
    dashscope: t('plugin.ai.channel.driver.dashscope'),
    dashscope_tts: t('plugin.ai.channel.driver.dashscope_tts'),
}
const driverCustom: Record<string, TagType> = {
    [DRIVER_DEFAULT]: 'info',
    openai: 'success',
    responses: 'primary',
    deepseek: 'success',
    dashscope: 'warning',
    dashscope_tts: 'warning',
}
const eventDict: Record<string, string> = {
    open: t('plugin.ai.overview.events.open'),
    close: t('plugin.ai.overview.events.close'),
    manual_open: t('plugin.ai.overview.events.manualOpen'),
    manual_close: t('plugin.ai.overview.events.manualClose'),
}
const vendorOptions = Object.entries(vendorDict).map(([value, label]) => ({ value, label }))

const formatSeconds = (value?: number | null) => (value ? dayjs.unix(Number(value)).format('YYYY-MM-DD HH:mm:ss') : '-')
const eventLabel = (event?: string) => (event ? (eventDict[event] ?? event) : '-')
const eventTimelineType = (event?: string): TagType => (event === 'close' || event === 'manual_close' ? 'success' : 'danger')
const probeValue = (row: TableRow) => (row.last_probe_ok === null || row.last_probe_ok === undefined ? 'never' : Number(row.last_probe_ok))

const stats = ref<ChannelStats>({ circuit_open: 0, circuit_half_open: 0, problem: 0, fail_1h: 0 })

const tableManager = useTableManager({
    api: new TableManagerAPI('/admin/plugin/ai/channel/'),
    table: {
        column: [
            { type: 'selection', align: 'center', operator: false },
            {
                label: t('plugin.ai.channel.columns.name'),
                prop: 'name',
                align: 'left',
                minWidth: 150,
                operator: 'ILIKE',
                quickSearch: true,
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.channel.columns.vendor'),
                prop: 'vendor',
                align: 'center',
                width: 110,
                operator: false,
                render: 'tag',
                custom: { openai: 'primary', deepseek: 'success', qwen: 'warning', moonshot: 'info', zhipu: 'info', ollama: 'success', other: 'info' },
                dict: vendorDict,
            },
            {
                label: t('plugin.ai.channel.columns.driver'),
                prop: 'driver',
                align: 'center',
                width: 130,
                operator: false,
                render: 'tag',
                custom: driverCustom,
                dict: driverDict,
                formatter: (row: TableRow) => (row.driver ? String(row.driver) : DRIVER_DEFAULT),
            },
            {
                label: t('plugin.ai.channel.columns.supplier'),
                prop: 'supplier_id',
                align: 'center',
                width: 140,
                operator: false,
                formatter: (row: TableRow) => supplierLabel(row.supplier_id),
            },
            {
                label: t('plugin.ai.channel.columns.baseUrl'),
                prop: 'base_url',
                align: 'left',
                minWidth: 220,
                operator: 'ILIKE',
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.channel.columns.model'),
                prop: 'model',
                align: 'left',
                minWidth: 140,
                operator: 'ILIKE',
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.channel.columns.apiKey'),
                prop: 'api_key',
                align: 'center',
                width: 170,
                operator: false,
                showOverflowTooltip: true,
                formatter: (row: TableRow) => row.api_key || '-',
            },
            {
                label: t('plugin.ai.channel.columns.status'),
                prop: 'status',
                align: 'center',
                width: 90,
                operator: false,
                render: 'tag',
                custom: statusCustom,
                dict: statusDict,
            },
            {
                label: t('plugin.ai.channel.columns.circuit'),
                prop: 'circuit',
                align: 'center',
                width: 110,
                operator: 'eq',
                render: 'tag',
                custom: circuitCustom,
                dict: circuitDict,
            },
            {
                label: t('plugin.ai.channel.columns.lastProbe'),
                prop: 'last_probe_at',
                align: 'center',
                width: 160,
                operator: false,
                formatter: (row: TableRow) => formatSeconds(row.last_probe_at),
            },
            {
                label: t('plugin.ai.channel.columns.lastProbeResult'),
                prop: 'last_probe_ok',
                align: 'center',
                width: 110,
                operator: false,
                render: 'tag',
                custom: probeCustom,
                dict: probeDict,
                formatter: probeValue,
            },
            { label: t('plugin.ai.channel.columns.timeout'), prop: 'timeout', align: 'center', width: 95, operator: 'BETWEEN' },
            { label: t('plugin.ai.channel.columns.priority'), prop: 'priority', align: 'center', width: 95, operator: 'BETWEEN' },
            { label: t('plugin.ai.channel.columns.weight'), prop: 'weight', align: 'center', width: 90, operator: 'BETWEEN' },
            // rpm/tpm：渠道级准入配额（0 = 跟随插件设置的渠道 Gate 缺省）。字段名按库列名，
            // 是否可作筛选由 handler 的 wheres 白名单决定；此处列改为仅展示（operator: false），
            // 避免把「未声明的新列」塞进筛选条件。
            { label: t('plugin.ai.channel.columns.rpm'), prop: 'rpm_limit', align: 'center', width: 105, operator: false, show: false },
            { label: t('plugin.ai.channel.columns.tpm'), prop: 'tpm_limit', align: 'center', width: 105, operator: false, show: false },
            {
                label: t('plugin.ai.channel.columns.failThreshold'),
                prop: 'fail_threshold',
                align: 'center',
                width: 110,
                operator: 'BETWEEN',
                show: false,
            },
            {
                label: t('plugin.ai.channel.columns.cooldown'),
                prop: 'cooldown_seconds',
                align: 'center',
                width: 110,
                operator: 'BETWEEN',
                show: false,
            },
            {
                label: t('plugin.ai.channel.columns.consecutiveFailures'),
                prop: 'consecutive_failures',
                align: 'center',
                width: 120,
                operator: 'BETWEEN',
                show: false,
            },
            {
                label: t('plugin.ai.channel.columns.totalCalls'),
                prop: 'total_calls',
                align: 'center',
                width: 110,
                operator: 'BETWEEN',
                show: false,
            },
            {
                label: t('plugin.ai.channel.columns.totalFailures'),
                prop: 'total_failures',
                align: 'center',
                width: 110,
                operator: 'BETWEEN',
                show: false,
            },
            {
                label: t('plugin.ai.channel.columns.totalTokens'),
                prop: 'total_tokens',
                align: 'center',
                width: 120,
                operator: 'BETWEEN',
                show: false,
            },
            {
                label: t('plugin.ai.channel.columns.lastSuccess'),
                prop: 'last_success_at',
                align: 'center',
                width: 160,
                operator: false,
                formatter: (row: TableRow) => formatSeconds(row.last_success_at),
                show: false,
            },
            {
                label: t('plugin.ai.channel.columns.lastProbeLatency'),
                prop: 'last_probe_latency',
                align: 'center',
                width: 130,
                operator: 'BETWEEN',
                show: false,
            },
            {
                label: t('plugin.ai.channel.columns.lastProbeError'),
                prop: 'last_probe_error',
                align: 'left',
                minWidth: 200,
                operator: 'ILIKE',
                showOverflowTooltip: true,
                show: false,
            },
            {
                label: t('plugin.ai.channel.columns.probeFailStreak'),
                prop: 'probe_fail_streak',
                align: 'center',
                width: 130,
                operator: 'BETWEEN',
                show: false,
            },
            {
                label: t('plugin.ai.channel.columns.lastError'),
                prop: 'last_error',
                align: 'left',
                minWidth: 220,
                operator: 'ILIKE',
                showOverflowTooltip: true,
                show: false,
            },
            {
                label: t('plugin.ai.channel.columns.remark'),
                prop: 'remark',
                align: 'left',
                minWidth: 160,
                operator: 'ILIKE',
                show: false,
            },
            {
                label: t('plugin.ai.channel.columns.createdAt'),
                prop: 'create_time',
                align: 'center',
                width: 160,
                operator: false,
                formatter: (row: TableRow) => formatSeconds(row.create_time),
                show: false,
            },
            {
                label: t('plugin.ai.channel.columns.updatedAt'),
                prop: 'update_time',
                align: 'center',
                width: 160,
                operator: false,
                formatter: (row: TableRow) => formatSeconds(row.update_time),
                show: false,
            },
            {
                label: t('common.operate'),
                align: 'center',
                width: 360,
                render: 'buttons',
                operator: false,
                buttons: [
                    {
                        render: 'tip',
                        name: 'edit',
                        title: t('plugin.ai.channel.actions.edit'),
                        type: 'primary',
                        icon: 'lucide-pencil',
                        display: () => can('update'),
                        click: (row) => openEdit(row),
                    },
                    {
                        render: 'confirm',
                        name: 'toggle-status',
                        title: t('plugin.ai.channel.actions.toggle'),
                        type: 'warning',
                        icon: 'lucide-power',
                        display: () => can('switch'),
                        popconfirm: (ctx) => ({
                            title:
                                Number(ctx.row?.status) === 1
                                    ? t('plugin.ai.channel.actions.disableConfirm', { name: ctx.row?.name ?? '' })
                                    : t('plugin.ai.channel.actions.enableConfirm', { name: ctx.row?.name ?? '' }),
                        }),
                        click: (row) => toggleStatus(row),
                    },
                    {
                        render: 'confirm',
                        name: 'breaker-open',
                        title: t('plugin.ai.channel.actions.breakerOpen'),
                        type: 'danger',
                        icon: 'lucide-octagon-x',
                        display: (row) => can('breaker') && row.circuit !== 'open',
                        popconfirm: (ctx) => ({ title: t('plugin.ai.channel.actions.openConfirm', { name: ctx.row?.name ?? '' }) }),
                        click: (row) => setBreaker(row, 'open'),
                    },
                    {
                        render: 'confirm',
                        name: 'breaker-close',
                        title: t('plugin.ai.channel.actions.breakerClose'),
                        type: 'success',
                        icon: 'lucide-rotate-ccw',
                        display: (row) => can('breaker') && row.circuit !== 'closed',
                        popconfirm: (ctx) => ({ title: t('plugin.ai.channel.actions.closeConfirm', { name: ctx.row?.name ?? '' }) }),
                        click: (row) => setBreaker(row, 'close'),
                    },
                    {
                        render: 'tip',
                        name: 'probe',
                        title: t('plugin.ai.channel.actions.probe'),
                        type: 'info',
                        icon: 'lucide-activity',
                        display: () => can('probe'),
                        loading: (row) => isProbing(row.id),
                        click: (row) => probeRow(row),
                    },
                    {
                        render: 'tip',
                        name: 'breaker-detail',
                        title: t('plugin.ai.channel.actions.breakerDetail'),
                        type: 'info',
                        icon: 'lucide-list-tree',
                        display: () => can('events'),
                        click: (row) => openBreaker(row),
                    },
                    {
                        render: 'confirm',
                        name: 'delete',
                        title: t('plugin.ai.channel.actions.delete'),
                        type: 'danger',
                        icon: 'lucide-trash',
                        display: () => can('delete'),
                        popconfirm: (ctx) => ({ title: t('plugin.ai.channel.actions.deleteConfirm', { name: ctx.row?.name ?? '' }) }),
                        click: (row) => removeRow(row),
                    },
                ],
            },
        ],
        filter: {
            sort: 'priority',
            order: 'asc',
        },
        dblClickNotEditColumn: ['all'],
    },
    form: {
        labelWidth: 170,
        defaultItems: {
            name: '',
            vendor: 'openai',
            driver: '',
            supplier_id: 0,
            base_url: '',
            api_key: '',
            model: '',
            timeout: 60,
            priority: 1,
            weight: 1,
            rpm_limit: 0,
            tpm_limit: 0,
            status: 1,
            fail_threshold: 0,
            cooldown_seconds: 0,
            remark: '',
        },
    },
})

// 列表响应里的 stats 段（与 list/total 同层）
tableManager.opts.after!.getData = ({ res }: { res: ApiResponse }) => {
    const payload = res.data.data as { stats?: Partial<ChannelStats> } | undefined
    const next = payload?.stats
    if (next) {
        stats.value = {
            circuit_open: Number(next.circuit_open ?? 0),
            circuit_half_open: Number(next.circuit_half_open ?? 0),
            problem: Number(next.problem ?? 0),
            fail_1h: Number(next.fail_1h ?? 0),
        }
    }
}

// 编辑回填只保留可编辑列：行里还有累计统计与探活快照，原样回传没有意义
const EDITABLE_FIELDS = [
    'id',
    'name',
    'vendor',
    'driver',
    'supplier_id',
    'base_url',
    'api_key',
    'model',
    'timeout',
    'priority',
    'weight',
    'rpm_limit',
    'tpm_limit',
    'status',
    'fail_threshold',
    'cooldown_seconds',
    'remark',
]
tableManager.opts.after!.getEditData = () => {
    const row = tableManager.form.items ?? {}
    const items: AnyObj = {}
    for (const field of EDITABLE_FIELDS) {
        items[field] = row[field]
    }
    items.timeout = Number(items.timeout ?? 60)
    items.priority = Number(items.priority ?? 1)
    items.weight = Number(items.weight ?? 1)
    items.status = Number(items.status ?? 1)
    items.fail_threshold = Number(items.fail_threshold ?? 0)
    items.cooldown_seconds = Number(items.cooldown_seconds ?? 0)
    // driver 空串是「默认驱动」的合法取值（不可归一为 undefined：那会让下拉显示为空选项）
    items.driver = row.driver == null ? '' : String(row.driver)
    items.supplier_id = Number(row.supplier_id ?? 0)
    items.rpm_limit = Number(row.rpm_limit ?? 0)
    items.tpm_limit = Number(row.tpm_limit ?? 0)
    items.api_key = row.api_key ?? ''
    tableManager.form.items = items
}

/* ---------------- 厂商候选（渠道可挂厂商，留空即继承） ---------------- */
interface SupplierRow {
    id?: number
    name?: string
}

const supplierRows = ref<SupplierRow[]>([])
const supplierLoadFailed = ref(false)
const supplierOptions = computed(() =>
    supplierRows.value
        .filter((item) => item.id !== null && item.id !== undefined)
        .map((item) => {
            const value = Number(item.id)
            return { value, label: item.name ? `${item.name}（#${value}）` : `#${value}` }
        })
)
const supplierLabel = (id?: unknown) => {
    const value = Number(id ?? 0)
    if (!value) return t('plugin.ai.channel.supplierPick.none')
    const hit = supplierOptions.value.find((item) => item.value === value)
    return hit ? hit.label : t('plugin.ai.channel.supplierPick.unknown', { id: value })
}

// 厂商列表是渠道表单的便利项，不是必需项：无厂商读权限时静默降级（不弹红字，改为表单内提示 + 手填 ID）
const loadSuppliers = async () => {
    try {
        const res = await request<{ list?: SupplierRow[] }>(
            { url: '/admin/plugin/ai/supplier/list', method: 'POST', data: { page: 1, limit: 100 } },
            { cancelDuplicate: false, showErrorMessage: false, showNetworkErrorMessage: false }
        )
        supplierRows.value = res.data.data?.list ?? []
        supplierLoadFailed.value = false
    } catch {
        supplierRows.value = []
        supplierLoadFailed.value = true
    }
}

// 提交前把下拉/自由输入的可能形状归一为后端契约：三个数值列非负整数、driver 为字符串
const toNonNegativeInt = (value: unknown) => {
    const parsed = Math.trunc(Number(value))
    return Number.isFinite(parsed) && parsed > 0 ? parsed : 0
}
tableManager.opts.before!.submitForm = ({ items }: { items: AnyObj }) => {
    if (!items) return
    items.supplier_id = toNonNegativeInt(items.supplier_id)
    items.rpm_limit = toNonNegativeInt(items.rpm_limit)
    items.tpm_limit = toNonNegativeInt(items.tpm_limit)
    items.driver = items.driver == null ? '' : String(items.driver).trim()
    items.timeout = toNonNegativeInt(items.timeout)
    items.priority = toNonNegativeInt(items.priority)
    items.status = Number(items.status) === 1 ? 1 : 0
}

/* ---------------- 顶部显式筛选（后端顶层 status/vendor，与 wheres AND 合并） ---------------- */
const preset = reactive<{ status: number | string; vendor: string }>({ status: '', vendor: '' })

const applyPreset = () => {
    const filter = tableManager.table.filter!
    if (preset.status === '' || preset.status === null || preset.status === undefined) {
        delete filter.status
    } else {
        filter.status = preset.status
    }
    if (!preset.vendor) {
        delete filter.vendor
    } else {
        filter.vendor = preset.vendor
    }
    filter.page = 1
    tableManager.getData()
}

const resetPreset = () => {
    preset.status = ''
    preset.vendor = ''
    applyPreset()
}

/* ---------------- 行内动作 ---------------- */
const openEdit = (row: TableRow) => {
    tableManager.toggleForm('update', [String(row.id)])
}

const removeRow = async (row: TableRow) => {
    try {
        await tableManager.api.delete([String(row.id)])
        tableManager.getData()
    } catch {
        // 请求层已提示；保持列表不变
    }
}

const toggleStatus = async (row: TableRow) => {
    try {
        await request(
            {
                url: '/admin/plugin/ai/channel/switch',
                method: 'POST',
                data: { id: Number(row.id), status: Number(row.status) === 1 ? 0 : 1 },
            },
            { showSuccessMessage: true }
        )
        tableManager.getData()
    } catch {
        // 请求层已提示
    }
}

const setBreaker = async (row: TableRow, state: 'open' | 'close') => {
    try {
        await request(
            {
                url: '/admin/plugin/ai/channel/breaker',
                method: 'POST',
                data: { id: Number(row.id), state },
            },
            { showSuccessMessage: true }
        )
        tableManager.getData()
    } catch {
        // 请求层已提示
    }
}

const probing = reactive(new Set<number>())
const isProbing = (id: unknown) => probing.has(Number(id))

const probeRow = async (row: TableRow) => {
    const id = Number(row.id)
    if (probing.has(id)) return
    probing.add(id)
    try {
        // 服务端探活超时 clamp(渠道 timeout, 3, 15) 秒；前端显式 20s，避免被宿主默认 10s 打断
        const res = await request<ProbeResult>(
            { url: '/admin/plugin/ai/channel/probe', method: 'POST', data: { id }, timeout: 20000 },
            { cancelDuplicate: false, showSuccessMessage: false }
        )
        const result = res.data.data ?? {}
        if (result.ok) {
            ElMessage.success(
                t('plugin.ai.channel.probe.ok', { latency: Number(result.latency_ms ?? 0), models: Number(result.models_count ?? 0) })
            )
        } else {
            ElMessage.warning(t('plugin.ai.channel.probe.fail', { reason: result.error || '-' }))
        }
        tableManager.getData()
    } catch {
        // 请求层已提示（网络错误 / 403 / 回执 code!=0）
    } finally {
        probing.delete(id)
    }
}

/* ---------------- 熔断详情 ---------------- */
const breakerVisible = ref(false)
const breakerLoading = ref(false)
const breakerRow = ref<TableRow | null>(null)
const breakerEvents = ref<BreakerEventRow[]>([])
const nowSec = ref(Math.floor(Date.now() / 1000))
let nowTimer: ReturnType<typeof setInterval> | null = null

const breakerTitle = computed(() => t('plugin.ai.channel.breaker.title', { name: breakerRow.value?.name ?? '-' }))
const effectiveCooldown = computed(() => {
    const effective = Number(breakerRow.value?.effective_cooldown_seconds ?? 0)
    if (effective > 0) return effective
    const raw = Number(breakerRow.value?.cooldown_seconds ?? 0)
    return raw > 0 ? raw : 60
})
const effectiveThreshold = computed(() => {
    const effective = Number(breakerRow.value?.effective_fail_threshold ?? 0)
    if (effective > 0) return effective
    const raw = Number(breakerRow.value?.fail_threshold ?? 0)
    return raw > 0 ? raw : 5
})
const cooldownRemaining = computed(() => {
    const row = breakerRow.value
    if (!row || row.circuit === 'closed') return null
    const opened = Number(row.opened_at ?? 0)
    if (!opened) return null
    return Math.max(0, opened + effectiveCooldown.value - nowSec.value)
})

const startNowTimer = () => {
    stopNowTimer()
    nowSec.value = Math.floor(Date.now() / 1000)
    nowTimer = setInterval(() => {
        nowSec.value = Math.floor(Date.now() / 1000)
    }, 1000)
}
const stopNowTimer = () => {
    if (nowTimer) {
        clearInterval(nowTimer)
        nowTimer = null
    }
}

const openBreaker = async (row: TableRow) => {
    breakerRow.value = row
    breakerEvents.value = []
    breakerVisible.value = true
    startNowTimer()
    breakerLoading.value = true
    const id = Number(row.id)
    const [detailRes, eventsRes] = await Promise.allSettled([
        request<{ row?: TableRow }>({ url: `/admin/plugin/ai/channel/get/${id}`, method: 'GET' }, { cancelDuplicate: false }),
        request<{ list?: BreakerEventRow[] }>(
            { url: '/admin/plugin/ai/channel/events', method: 'POST', data: { id } },
            { cancelDuplicate: false }
        ),
    ])
    if (detailRes.status === 'fulfilled' && detailRes.value.data.data?.row) {
        breakerRow.value = detailRes.value.data.data.row
    }
    if (eventsRes.status === 'fulfilled') {
        breakerEvents.value = eventsRes.value.data.data?.list ?? []
    }
    breakerLoading.value = false
}

watch(breakerVisible, (visible) => {
    if (!visible) {
        stopNowTimer()
        breakerRow.value = null
        breakerEvents.value = []
    }
})

onMounted(() => {
    tableManager.initCtx()
    tableManager.getData()
    loadSuppliers()
})

onUnmounted(() => {
    stopNowTimer()
})
</script>

<style scoped lang="scss">
.plugin-ai-channel {
    .channel-alert {
        margin-bottom: 10px;
    }

    .channel-stats {
        display: grid;
        grid-template-columns: repeat(4, minmax(0, 1fr));
        gap: 10px;
        margin-bottom: 10px;
    }

    .stat-item {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 10px 14px;
        border: 1px solid var(--el-border-color-lighter);
        border-left: 3px solid var(--el-color-primary);
        border-radius: 6px;
        background: var(--el-bg-color-overlay);

        &.danger {
            border-left-color: var(--el-color-danger);
        }

        &.warning {
            border-left-color: var(--el-color-warning);
        }
    }

    .stat-label {
        font-size: 13px;
        color: var(--el-text-color-secondary);
    }

    .stat-value {
        font-size: 20px;
        font-weight: 600;
    }

    .channel-filters {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
        margin-bottom: 10px;
    }

    .filter-select {
        width: 170px;
    }

    .breaker-body {
        min-height: 160px;
    }

    .cooldown-expired {
        color: var(--el-color-success);
    }

    .form-tip {
        width: 100%;
        margin-top: 2px;
        font-size: 12px;
        line-height: 18px;
        color: var(--el-text-color-secondary);
    }

    .form-tip-inline {
        margin-left: 4px;
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .breaker-events-title {
        margin: 16px 0 8px;
        font-weight: 600;
    }

    .breaker-timeline {
        padding-left: 4px;
    }

    .event-head {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
    }

    .event-meta {
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .event-error {
        margin-top: 4px;
        font-size: 12px;
        color: var(--el-color-danger);
        word-break: break-all;
    }
}
</style>
