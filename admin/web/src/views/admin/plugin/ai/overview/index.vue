<!-- plugin: ai v0.2.0 -->
<!--
  AI 总览（只读自取数页）。
  契约：POST /admin/plugin/ai/overview/index —— data 直出 h24 / d7 / d7_series / channels / models /
  top_models / recent_fails / recent_breakers / service / settings / time（字段名与源 Overview.php 对齐；
  d7_series 是本插件相对源的加项）。
  为什么在视图内直接 import request：插件的落位闭集只允许 web/views/** 与 web/lang/*.yaml，
  无法往宿主 web/src/api/** 加文件（internal/plugin/cli/plan.go 的 planCopyTarget）。
-->
<template>
    <div class="default-main plugin-ai-overview" v-loading="loading">
        <el-alert class="overview-alert" type="info" :closable="false" show-icon :title="t('plugin.ai.overview.intro')" />

        <div class="overview-toolbar">
            <div class="overview-toolbar-left">
                <el-tag size="small" effect="plain">{{ t('plugin.ai.overview.windowH24') }}</el-tag>
                <el-tag size="small" effect="plain" type="success">{{ t('plugin.ai.overview.windowD7') }}</el-tag>
                <span class="overview-updated">
                    {{ t('plugin.ai.overview.updatedAt') }}：{{ formatSeconds(data.time) }}
                </span>
            </div>
            <div class="overview-toolbar-right">
                <el-button v-blur :loading="loading" @click="load()">
                    <Icon name="lucide-refresh-cw" :size="14" />
                    <span class="btn-text">{{ t('plugin.ai.overview.refresh') }}</span>
                </el-button>
                <el-button v-if="canSettings" v-blur type="primary" @click="openSettings">
                    <Icon name="lucide-settings" :size="14" />
                    <span class="btn-text">{{ t('plugin.ai.overview.settings.open') }}</span>
                </el-button>
            </div>
        </div>

        <!-- KPI：近 24 小时（窗口 = now-86400，字段名照源 h24） -->
        <el-row :gutter="12" class="overview-kpi-row">
            <el-col v-for="card in kpiCards" :key="card.key" :xs="24" :sm="12" :lg="6">
                <el-card shadow="never" class="overview-kpi-card">
                    <div class="kpi-title">{{ card.title }}</div>
                    <div class="kpi-value">{{ card.value }}</div>
                    <div class="kpi-sub">{{ card.sub }}</div>
                </el-card>
            </el-col>
        </el-row>

        <el-card shadow="never" class="overview-card">
            <template #header>
                <div class="card-header">
                    <span>{{ t('plugin.ai.overview.service.title') }}</span>
                    <el-tag size="small" type="info">{{ formatSeconds(data.time) }}</el-tag>
                </div>
            </template>
            <el-descriptions :column="4" border size="small">
                <el-descriptions-item :label="t('plugin.ai.overview.service.db')">
                    <el-tag v-if="data.service" size="small" :type="dbOk ? 'success' : 'danger'">
                        {{ dbOk ? t('plugin.ai.overview.service.ok') : t('plugin.ai.overview.service.down') }}
                    </el-tag>
                    <span v-else>-</span>
                </el-descriptions-item>
                <el-descriptions-item :label="t('plugin.ai.overview.service.gatewayKey')">
                    <el-tag v-if="data.service" size="small" :type="gatewayKeySet ? 'success' : 'warning'">
                        {{ gatewayKeySet ? t('plugin.ai.overview.service.keySet') : t('plugin.ai.overview.service.keyMissing') }}
                    </el-tag>
                    <span v-else>-</span>
                </el-descriptions-item>
                <el-descriptions-item :label="t('plugin.ai.overview.service.time')">{{ formatSeconds(data.time) }}</el-descriptions-item>
                <el-descriptions-item :label="t('plugin.ai.overview.kpi.avgLatency')">
                    {{ formatLatency(h24.avg_latency_ms) }}
                </el-descriptions-item>
            </el-descriptions>
        </el-card>

        <el-row :gutter="12">
            <el-col :xs="24" :lg="14">
                <el-card shadow="never" class="overview-card">
                    <template #header>
                        <div class="card-header">
                            <span>{{ t('plugin.ai.overview.trend.title') }}</span>
                            <span class="card-hint">{{ t('plugin.ai.overview.windowD7') }}</span>
                        </div>
                    </template>
                    <div v-show="trendPoints.length" ref="trendChartRef" class="overview-chart"></div>
                    <el-empty v-if="!trendPoints.length" :description="t('plugin.ai.overview.trend.empty')" :image-size="60" />
                </el-card>
            </el-col>
            <el-col :xs="24" :lg="10">
                <el-card shadow="never" class="overview-card">
                    <template #header>
                        <div class="card-header">
                            <span>{{ t('plugin.ai.overview.channels.title') }}</span>
                            <el-tag size="small" type="info">
                                {{ t('plugin.ai.overview.models.total') }}：{{ models.total }}
                            </el-tag>
                        </div>
                    </template>
                    <div class="health-grid">
                        <div class="health-item">
                            <div class="health-label">{{ t('plugin.ai.overview.channels.total') }}</div>
                            <div class="health-value">{{ channels.total }}</div>
                        </div>
                        <div class="health-item">
                            <div class="health-label">{{ t('plugin.ai.overview.channels.enabled') }}</div>
                            <div class="health-value success">{{ channels.enabled }}</div>
                        </div>
                        <div class="health-item">
                            <div class="health-label">{{ t('plugin.ai.overview.channels.circuitOpen') }}</div>
                            <div class="health-value danger">{{ channels.circuit_open }}</div>
                        </div>
                        <div class="health-item">
                            <div class="health-label">{{ t('plugin.ai.overview.channels.circuitHalfOpen') }}</div>
                            <div class="health-value warning">{{ channels.circuit_half_open }}</div>
                        </div>
                        <div class="health-item">
                            <div class="health-label">{{ t('plugin.ai.overview.models.enabled') }}</div>
                            <div class="health-value">{{ models.enabled }}</div>
                        </div>
                    </div>
                    <div class="problems-title">{{ t('plugin.ai.overview.channels.problems') }}</div>
                    <div v-if="channelProblems.length" class="problems-list">
                        <div v-for="item in channelProblems" :key="String(item.id ?? item.name)" class="problem-item">
                            <el-tag
                                size="small"
                                :type="item.circuit === 'open' ? 'danger' : item.circuit === 'half_open' ? 'warning' : 'info'"
                            >
                                {{ circuitLabel(item.circuit) }}
                            </el-tag>
                            <span class="problem-name">{{ item.name || '-' }}</span>
                            <span class="problem-error" :title="item.last_error || ''">{{ item.last_error || '-' }}</span>
                        </div>
                    </div>
                    <el-empty v-else :description="t('plugin.ai.overview.channels.noProblems')" :image-size="50" />
                </el-card>
            </el-col>
        </el-row>

        <el-card shadow="never" class="overview-card">
            <template #header>
                <div class="card-header">
                    <span>{{ t('plugin.ai.overview.topModels.title') }}</span>
                    <span class="card-hint">{{ t('plugin.ai.overview.windowH24') }}</span>
                </div>
            </template>
            <el-table :data="topModels" size="small" border :empty-text="t('plugin.ai.overview.topModels.empty')">
                <el-table-column :label="t('plugin.ai.overview.topModels.model')" prop="model_name" min-width="180" show-overflow-tooltip>
                    <template #default="scope">{{ scope.row.model_name || scope.row.model || '-' }}</template>
                </el-table-column>
                <el-table-column :label="t('plugin.ai.overview.topModels.calls')" prop="calls" width="110" align="right">
                    <template #default="scope">{{ formatInt(scope.row.calls) }}</template>
                </el-table-column>
                <el-table-column :label="t('plugin.ai.overview.topModels.tokens')" prop="total_tokens" width="140" align="right">
                    <template #default="scope">{{ formatInt(scope.row.total_tokens ?? scope.row.tokens) }}</template>
                </el-table-column>
                <el-table-column :label="t('plugin.ai.overview.topModels.cost')" prop="cost" width="140" align="right">
                    <template #default="scope">{{ formatCost(scope.row.cost) }}</template>
                </el-table-column>
            </el-table>
        </el-card>

        <el-row :gutter="12">
            <el-col :xs="24" :lg="12">
                <el-card shadow="never" class="overview-card">
                    <template #header>
                        <div class="card-header">
                            <span>{{ t('plugin.ai.overview.recentFails.title') }}</span>
                        </div>
                    </template>
                    <el-table :data="recentFails" size="small" border :empty-text="t('plugin.ai.overview.recentFails.empty')">
                        <el-table-column :label="t('plugin.ai.overview.recentFails.time')" width="160">
                            <template #default="scope">{{ formatSeconds(scope.row.create_time) }}</template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.overview.recentFails.model')" prop="model_name" min-width="120" show-overflow-tooltip />
                        <el-table-column :label="t('plugin.ai.overview.recentFails.channel')" prop="channel_name" min-width="110" show-overflow-tooltip />
                        <el-table-column :label="t('plugin.ai.overview.recentFails.status')" width="90">
                            <template #default="scope">
                                <el-tag size="small" :type="scope.row.status === 'timeout' ? 'warning' : 'danger'">
                                    {{ usageStatusLabel(scope.row.status) }}
                                </el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column
                            :label="t('plugin.ai.overview.recentFails.error')"
                            prop="error_message"
                            min-width="180"
                            show-overflow-tooltip
                        />
                    </el-table>
                </el-card>
            </el-col>
            <el-col :xs="24" :lg="12">
                <el-card shadow="never" class="overview-card">
                    <template #header>
                        <div class="card-header">
                            <span>{{ t('plugin.ai.overview.recentBreakers.title') }}</span>
                        </div>
                    </template>
                    <el-table :data="recentBreakers" size="small" border :empty-text="t('plugin.ai.overview.recentBreakers.empty')">
                        <el-table-column :label="t('plugin.ai.overview.recentBreakers.time')" width="160">
                            <template #default="scope">{{ formatSeconds(scope.row.create_time) }}</template>
                        </el-table-column>
                        <el-table-column
                            :label="t('plugin.ai.overview.recentBreakers.channel')"
                            prop="channel_name"
                            min-width="110"
                            show-overflow-tooltip
                        />
                        <el-table-column :label="t('plugin.ai.overview.recentBreakers.event')" width="110">
                            <template #default="scope">
                                <el-tag size="small" :type="eventTagType(scope.row.event)">
                                    {{ eventLabel(scope.row.event) }}
                                </el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.overview.recentBreakers.failures')" prop="consecutive_failures" width="110" align="center" />
                        <el-table-column
                            :label="t('plugin.ai.overview.recentBreakers.error')"
                            prop="last_error"
                            min-width="180"
                            show-overflow-tooltip
                        />
                    </el-table>
                </el-card>
            </el-col>
        </el-row>

        <!-- 接入设置：插件自管（宿主配置页不会为 plugin.* 分组生成 tab） -->
        <el-dialog
            v-model="settingsVisible"
            :title="t('plugin.ai.overview.settings.title')"
            width="min(720px, 96vw)"
            :close-on-click-modal="false"
        >
            <el-alert class="settings-alert" type="info" :closable="false" show-icon :title="t('plugin.ai.overview.settings.hint')" />
            <div class="settings-scroll">
                <el-form :model="settingsForm" label-width="180px">
                <el-form-item :label="t('plugin.ai.overview.settings.apiKey')">
                    <el-input
                        v-model="settingsForm.api_key"
                        type="password"
                        show-password
                        autocomplete="new-password"
                        :placeholder="t('plugin.ai.overview.settings.apiKeyPlaceholder')"
                        @input="clearKey = false"
                    />
                    <div class="form-tip">
                        {{
                            maskedKey
                                ? t('plugin.ai.overview.settings.apiKeyConfigured', { masked: maskedKey })
                                : t('plugin.ai.overview.settings.apiKeyNotConfigured')
                        }}
                    </div>
                    <div class="key-actions">
                        <el-tag v-if="clearKey" size="small" type="warning">{{ t('plugin.ai.overview.settings.clearMarked') }}</el-tag>
                        <el-button v-blur size="small" type="danger" plain @click="markClearKey">
                            {{ t('plugin.ai.overview.settings.clearKey') }}
                        </el-button>
                    </div>
                </el-form-item>
                <el-form-item :label="t('plugin.ai.overview.settings.failThreshold')">
                    <el-input-number v-model="settingsForm.breaker_fail_threshold" class="w100" :min="0" :precision="0" controls-position="right" />
                </el-form-item>
                <el-form-item :label="t('plugin.ai.overview.settings.cooldownSeconds')">
                    <el-input-number v-model="settingsForm.breaker_cooldown_seconds" class="w100" :min="0" :precision="0" controls-position="right" />
                </el-form-item>
                <el-form-item :label="t('plugin.ai.overview.settings.requestTimeout')">
                    <el-input-number v-model="settingsForm.request_timeout" class="w100" :min="1" :precision="0" controls-position="right" />
                </el-form-item>
                <el-form-item :label="t('plugin.ai.overview.settings.streamTimeout')">
                    <el-input-number v-model="settingsForm.stream_timeout" class="w100" :min="1" :precision="0" controls-position="right" />
                </el-form-item>
                <el-form-item :label="t('plugin.ai.overview.settings.currency')">
                    <el-input v-model="settingsForm.currency" maxlength="10" />
                </el-form-item>
                <el-form-item :label="t('plugin.ai.overview.settings.allowPrivateHosts')">
                    <el-switch v-model="settingsForm.allow_private_hosts" :active-value="1" :inactive-value="0" />
                    <div class="form-tip">{{ t('plugin.ai.overview.settings.allowPrivateHostsTip') }}</div>
                </el-form-item>

                <!-- 流式上限：以 MiB 输入、按字节落库；回显当前生效值 -->
                <el-divider content-position="left">{{ t('plugin.ai.overview.settings.sseTitle') }}</el-divider>
                <el-form-item :label="t('plugin.ai.overview.settings.sseMaxLineBytes')">
                    <el-input-number
                        v-model="settingsForm.sse_max_line_mib"
                        class="w100"
                        :min="0"
                        :precision="2"
                        :step="0.5"
                        controls-position="right"
                    />
                    <div class="form-tip">{{ bytesHint('sse_max_line_bytes', SSE_FALLBACK.line) }}</div>
                </el-form-item>
                <el-form-item :label="t('plugin.ai.overview.settings.sseMaxFrameBytes')">
                    <el-input-number
                        v-model="settingsForm.sse_max_frame_mib"
                        class="w100"
                        :min="0"
                        :precision="2"
                        :step="0.5"
                        controls-position="right"
                    />
                    <div class="form-tip">{{ bytesHint('sse_max_frame_bytes', SSE_FALLBACK.frame) }}</div>
                </el-form-item>
                <el-form-item :label="t('plugin.ai.overview.settings.sseMaxAccumulatedBytes')">
                    <el-input-number
                        v-model="settingsForm.sse_max_accumulated_mib"
                        class="w100"
                        :min="0"
                        :precision="2"
                        :step="1"
                        controls-position="right"
                    />
                    <div class="form-tip">{{ bytesHint('sse_max_accumulated_bytes', SSE_FALLBACK.accumulated) }}</div>
                </el-form-item>
                <div class="form-tip group-tip">{{ t('plugin.ai.overview.settings.sseTip') }}</div>

                <!-- 公开面按 Key 限流：0 = 不限 -->
                <el-divider content-position="left">{{ t('plugin.ai.overview.settings.rateTitle') }}</el-divider>
                <el-form-item :label="t('plugin.ai.overview.settings.rateLimitRpm')">
                    <el-input-number v-model="settingsForm.rate_limit_rpm" class="w100" :min="0" :precision="0" controls-position="right" />
                </el-form-item>
                <el-form-item :label="t('plugin.ai.overview.settings.rateLimitBurst')">
                    <el-input-number v-model="settingsForm.rate_limit_burst" class="w100" :min="0" :precision="0" controls-position="right" />
                </el-form-item>
                <el-form-item :label="t('plugin.ai.overview.settings.rateLimitMaxInflight')">
                    <el-input-number
                        v-model="settingsForm.rate_limit_max_inflight"
                        class="w100"
                        :min="0"
                        :precision="0"
                        controls-position="right"
                    />
                </el-form-item>
                <div class="form-tip group-tip">{{ t('plugin.ai.overview.settings.rateTip') }}</div>

                <!-- 渠道 Gate 缺省：0 = 不限 -->
                <el-divider content-position="left">{{ t('plugin.ai.overview.settings.gateTitle') }}</el-divider>
                <el-form-item :label="t('plugin.ai.overview.settings.gateRpm')">
                    <el-input-number
                        v-model="settingsForm.channel_gate_rpm_default"
                        class="w100"
                        :min="0"
                        :precision="0"
                        controls-position="right"
                    />
                </el-form-item>
                <el-form-item :label="t('plugin.ai.overview.settings.gateTpm')">
                    <el-input-number
                        v-model="settingsForm.channel_gate_tpm_default"
                        class="w100"
                        :min="0"
                        :precision="0"
                        controls-position="right"
                    />
                </el-form-item>
                <div class="form-tip group-tip">{{ t('plugin.ai.overview.settings.gateTip') }}</div>

                <!-- 异步任务 worker：轮询下界 1 秒 -->
                <el-divider content-position="left">{{ t('plugin.ai.overview.settings.taskTitle') }}</el-divider>
                <el-form-item :label="t('plugin.ai.overview.settings.taskPollSeconds')">
                    <el-input-number v-model="settingsForm.task_poll_seconds" class="w100" :min="0" :precision="0" controls-position="right" />
                </el-form-item>
                <el-form-item :label="t('plugin.ai.overview.settings.taskMaxAttempts')">
                    <el-input-number v-model="settingsForm.task_max_attempts" class="w100" :min="0" :precision="0" controls-position="right" />
                </el-form-item>
                <div class="form-tip group-tip">{{ t('plugin.ai.overview.settings.taskTip') }}</div>
                </el-form>
            </div>
            <template #footer>
                <el-button @click="settingsVisible = false">{{ t('common.cancel') }}</el-button>
                <el-button v-blur type="primary" :loading="savingSettings" @click="saveSettings">
                    {{ t('plugin.ai.overview.settings.save') }}
                </el-button>
            </template>
        </el-dialog>
    </div>
</template>

<script setup lang="ts">
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { auth } from '@/utils/common'
import request from '@/utils/request'
import { dayjs, ElMessage, ElMessageBox } from 'element-plus'
import * as echarts from 'echarts'
import type { ECharts, EChartsOption } from 'echarts'
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, useTemplateRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'

defineOptions({
    name: 'plugin/ai/overview',
})

const { t } = useI18n()

// settings/save 的 pid 是分组目录 plugin/ai（authNode 键 = 前缀 + plugin/ai），不是本页菜单
const canSettings = auth({
    name: `${adminBaseRoutePath}/plugin/ai`,
    subNodeName: `${adminBaseRoutePath}/plugin/ai/settings/save`,
})

type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'

interface ChannelProblem {
    id?: number
    name?: string
    circuit?: string
    last_error?: string
}

interface TopModelRow {
    model_id?: number
    model_name?: string
    model?: string
    calls?: number
    total_tokens?: number
    tokens?: number
    cost?: string | number
}

interface UsageRow {
    request_id?: string
    attempt?: number
    model_name?: string
    channel_name?: string
    status?: string
    create_time?: number
    error_message?: string
}

interface BreakerEventRow {
    channel_name?: string
    event?: string
    consecutive_failures?: number
    create_time?: number
    last_error?: string
}

interface SeriesPoint {
    day?: string
    calls?: number
    total_tokens?: number
    tokens?: number
    cost?: string | number
}

interface OverviewData {
    h24?: {
        calls?: number
        success_calls?: number
        success?: number
        fail_calls?: number
        fail?: number
        timeout?: number
        total_tokens?: number
        cost?: string | number
        avg_latency_ms?: number
    }
    d7?: { calls?: number; total_tokens?: number; cost?: string | number }
    d7_series?: SeriesPoint[]
    channels?: { total?: number; enabled?: number; circuit_open?: number; circuit_half_open?: number; problems?: ChannelProblem[] }
    models?: { total?: number; enabled?: number }
    top_models?: TopModelRow[]
    recent_fails?: UsageRow[]
    recent_breakers?: BreakerEventRow[]
    service?: { db?: boolean | string | number; gateway_key_set?: boolean | string | number }
    settings?: Record<string, any>
    time?: number
}

const data = ref<OverviewData>({})
const loading = ref(false)
let inFlight = false

const h24 = computed(() => data.value.h24 ?? {})
const successCalls = computed(() => Number(h24.value.success_calls ?? h24.value.success ?? 0))
const failCalls = computed(() => Number(h24.value.fail_calls ?? h24.value.fail ?? 0))
const channels = computed(() => data.value.channels ?? {})
const models = computed(() => data.value.models ?? {})
const channelProblems = computed(() => (data.value.channels?.problems ?? []).slice(0, 5))
const topModels = computed(() => (data.value.top_models ?? []).slice(0, 5))
const recentFails = computed(() => (data.value.recent_fails ?? []).slice(0, 5))
const recentBreakers = computed(() => (data.value.recent_breakers ?? []).slice(0, 5))

const dbOk = computed(() => {
    const value = data.value.service?.db
    if (value === true || value === 1 || value === '1') return true
    return ['ok', 'up', 'healthy'].includes(String(value ?? '').toLowerCase())
})
const gatewayKeySet = computed(() => {
    const value = data.value.service?.gateway_key_set
    return value === true || value === 1 || value === '1' || value === 'true'
})

const formatSeconds = (value?: number | null) => (value ? dayjs.unix(Number(value)).format('YYYY-MM-DD HH:mm:ss') : '-')
const formatInt = (value?: number | null) => (value === null || value === undefined ? '-' : Number(value).toLocaleString())
const formatCost = (value?: string | number | null) => (value === null || value === undefined || value === '' ? '-' : String(value))
const formatLatency = (value?: number | null) => (value === null || value === undefined ? '-' : `${Number(value).toLocaleString()} ms`)
const formatPercent = (value: number) => (Number.isFinite(value) ? `${value.toFixed(1)}%` : '-')

const successRate = computed(() => {
    const calls = Number(h24.value.calls ?? 0)
    if (!calls) return '-'
    return formatPercent((successCalls.value / calls) * 100)
})

const kpiCards = computed(() => [
    {
        key: 'calls',
        title: t('plugin.ai.overview.kpi.calls'),
        value: formatInt(h24.value.calls),
        sub: t('plugin.ai.overview.kpi.callsHint', {
            success: formatInt(successCalls.value),
            fail: formatInt(failCalls.value),
        }),
    },
    {
        key: 'successRate',
        title: t('plugin.ai.overview.kpi.successRate'),
        value: successRate.value,
        sub: t('plugin.ai.overview.windowH24'),
    },
    {
        key: 'tokens',
        title: t('plugin.ai.overview.kpi.tokens'),
        value: formatInt(h24.value.total_tokens),
        sub: t('plugin.ai.overview.windowH24'),
    },
    {
        key: 'cost',
        title: t('plugin.ai.overview.kpi.cost'),
        value: formatCost(h24.value.cost),
        sub: currency.value,
    },
])

const currency = computed(() => String(data.value.settings?.currency || 'USD'))

const eventDict = computed<Record<string, string>>(() => ({
    open: t('plugin.ai.overview.events.open'),
    close: t('plugin.ai.overview.events.close'),
    manual_open: t('plugin.ai.overview.events.manualOpen'),
    manual_close: t('plugin.ai.overview.events.manualClose'),
}))
const eventLabel = (event?: string) => (event ? (eventDict.value[event] ?? event) : '-')
const eventTagType = (event?: string): TagType => {
    if (event === 'close' || event === 'manual_close') return 'success'
    return 'danger'
}
const circuitLabel = (circuit?: string) => {
    if (circuit === 'open') return t('plugin.ai.channel.circuit.open')
    if (circuit === 'half_open') return t('plugin.ai.channel.circuit.half_open')
    return t('plugin.ai.channel.circuit.closed')
}
const usageStatusLabel = (status?: string) => {
    if (!status) return '-'
    return t(`plugin.ai.usage.log.status.${status}`)
}

/* ---------------- 7 日趋势（echarts；d7_series 为相对源的加项） ---------------- */
const trendChartRef = useTemplateRef<HTMLDivElement>('trendChartRef')
let trendChart: ECharts | null = null

const trendPoints = computed(() =>
    [...(data.value.d7_series ?? [])].sort((a, b) => String(a.day ?? '').localeCompare(String(b.day ?? ''))).slice(-7)
)

const renderTrendChart = () => {
    if (!trendChartRef.value) return
    if (!trendChart) {
        trendChart = echarts.init(trendChartRef.value)
    }
    const points = trendPoints.value
    const option: EChartsOption = {
        tooltip: { trigger: 'axis', appendToBody: true, extraCssText: 'pointer-events: none; z-index: 2050;' },
        legend: { data: [t('plugin.ai.overview.trend.calls'), t('plugin.ai.overview.trend.tokens')] },
        grid: { left: 52, right: 60, top: 42, bottom: 30 },
        xAxis: { type: 'category', boundaryGap: false, data: points.map((item) => item.day ?? '-') },
        yAxis: [
            { type: 'value', name: t('plugin.ai.overview.trend.calls'), minInterval: 1 },
            { type: 'value', name: t('plugin.ai.overview.trend.tokens'), minInterval: 1 },
        ],
        series: [
            {
                name: t('plugin.ai.overview.trend.calls'),
                type: 'line',
                smooth: true,
                showSymbol: true,
                areaStyle: { opacity: 0.08 },
                data: points.map((item) => Number(item.calls ?? 0)),
            },
            {
                name: t('plugin.ai.overview.trend.tokens'),
                type: 'line',
                smooth: true,
                showSymbol: true,
                yAxisIndex: 1,
                data: points.map((item) => Number(item.total_tokens ?? item.tokens ?? 0)),
            },
        ],
    }
    trendChart.setOption(option, true)
    // 图表在空态时以 v-show 隐藏，初始化尺寸可能为 0；数据到来后补一次 resize
    trendChart.resize()
}

const resizeTrendChart = () => trendChart?.resize()

/* ---------------- 取数 ---------------- */
const load = async (silent = false) => {
    if (inFlight) return
    inFlight = true
    if (!silent) loading.value = true
    try {
        const res = await request<OverviewData>(
            { url: '/admin/plugin/ai/overview/index', method: 'POST', data: {} },
            { cancelDuplicate: false }
        )
        data.value = res.data.data ?? {}
        await nextTick()
        renderTrendChart()
    } catch {
        // 失败文案由请求层统一提示；这里保留上一次成功数据，不把页面打空
    } finally {
        inFlight = false
        loading.value = false
    }
}

/* ---------------- 接入设置 ---------------- */
const settingsVisible = ref(false)
const savingSettings = ref(false)
const clearKey = ref(false)
const maskedKey = computed(() => {
    const value = data.value.settings?.api_key
    if (typeof value === 'string' && value) return value
    return data.value.settings?.api_key_set ? '****' : ''
})

const toNum = (value: unknown, fallback: number) => {
    const parsed = Number(value)
    return Number.isFinite(parsed) ? parsed : fallback
}

// 新增配置键（SSE 三上限 / 限流三参数 / 渠道 Gate 两缺省 / 任务 worker 两键）。
// 字节类以 MiB 输入、按字节落库；缺省值与 plugin.yaml 一致（1/8/32 MiB）。
const MIB = 1048576
const SSE_FALLBACK = { line: 1 * MIB, frame: 8 * MIB, accumulated: 32 * MIB }
const bytesToMib = (value: unknown, fallbackBytes: number) => {
    const parsed = Number(value)
    if (!Number.isFinite(parsed) || parsed <= 0) return Number((fallbackBytes / MIB).toFixed(2))
    return Number((parsed / MIB).toFixed(2))
}
const mibToBytes = (value: unknown, fallbackBytes: number) => {
    const parsed = Number(value)
    if (!Number.isFinite(parsed) || parsed <= 0) return fallbackBytes
    return Math.round(parsed * MIB)
}
// 回读生效值：直接读总览 settings 段（与服务端同一份），不读表单里的草稿值
const bytesHint = (key: string, fallbackBytes: number) => {
    const bytes = Number(data.value.settings?.[key] ?? fallbackBytes)
    const safe = Number.isFinite(bytes) && bytes > 0 ? bytes : fallbackBytes
    return t('plugin.ai.overview.settings.bytesValue', { bytes: formatInt(safe), mib: (safe / MIB).toFixed(2) })
}

const settingsForm = reactive({
    api_key: '',
    breaker_fail_threshold: 5,
    breaker_cooldown_seconds: 60,
    request_timeout: 60,
    stream_timeout: 300,
    currency: 'USD',
    allow_private_hosts: 0,
    sse_max_line_mib: 1,
    sse_max_frame_mib: 8,
    sse_max_accumulated_mib: 32,
    rate_limit_rpm: 0,
    rate_limit_burst: 0,
    rate_limit_max_inflight: 0,
    channel_gate_rpm_default: 0,
    channel_gate_tpm_default: 0,
    task_poll_seconds: 5,
    task_max_attempts: 3,
})

const openSettings = () => {
    const settings = data.value.settings ?? {}
    settingsForm.api_key = ''
    settingsForm.breaker_fail_threshold = toNum(settings.breaker_fail_threshold, 5)
    settingsForm.breaker_cooldown_seconds = toNum(settings.breaker_cooldown_seconds, 60)
    settingsForm.request_timeout = toNum(settings.request_timeout, 60)
    settingsForm.stream_timeout = toNum(settings.stream_timeout, 300)
    settingsForm.currency = String(settings.currency || 'USD')
    settingsForm.allow_private_hosts = toNum(settings.allow_private_hosts, 0) ? 1 : 0
    settingsForm.sse_max_line_mib = bytesToMib(settings.sse_max_line_bytes, SSE_FALLBACK.line)
    settingsForm.sse_max_frame_mib = bytesToMib(settings.sse_max_frame_bytes, SSE_FALLBACK.frame)
    settingsForm.sse_max_accumulated_mib = bytesToMib(settings.sse_max_accumulated_bytes, SSE_FALLBACK.accumulated)
    settingsForm.rate_limit_rpm = toNum(settings.rate_limit_rpm, 0)
    settingsForm.rate_limit_burst = toNum(settings.rate_limit_burst, 0)
    settingsForm.rate_limit_max_inflight = toNum(settings.rate_limit_max_inflight, 0)
    settingsForm.channel_gate_rpm_default = toNum(settings.channel_gate_rpm_default, 0)
    settingsForm.channel_gate_tpm_default = toNum(settings.channel_gate_tpm_default, 0)
    settingsForm.task_poll_seconds = toNum(settings.task_poll_seconds, 5)
    settingsForm.task_max_attempts = toNum(settings.task_max_attempts, 3)
    clearKey.value = false
    settingsVisible.value = true
}

const markClearKey = () => {
    ElMessageBox.confirm(t('plugin.ai.overview.settings.clearConfirm'), t('common.reminder'), { type: 'warning' })
        .then(() => {
            settingsForm.api_key = ''
            clearKey.value = true
            ElMessage.warning(t('plugin.ai.overview.settings.clearMarked'))
        })
        .catch(() => {
            // 用户取消：不改动当前输入
        })
}

const saveSettings = async () => {
    const payload: AnyObj = {
        breaker_fail_threshold: Number(settingsForm.breaker_fail_threshold),
        breaker_cooldown_seconds: Number(settingsForm.breaker_cooldown_seconds),
        request_timeout: Number(settingsForm.request_timeout),
        stream_timeout: Number(settingsForm.stream_timeout),
        currency: settingsForm.currency.trim() || 'USD',
        allow_private_hosts: Number(settingsForm.allow_private_hosts) ? 1 : 0,
        // MiB → 字节（后端按字节校验与落库；0 视为「沿用缺省」由后端 clamp）
        sse_max_line_bytes: mibToBytes(settingsForm.sse_max_line_mib, SSE_FALLBACK.line),
        sse_max_frame_bytes: mibToBytes(settingsForm.sse_max_frame_mib, SSE_FALLBACK.frame),
        sse_max_accumulated_bytes: mibToBytes(settingsForm.sse_max_accumulated_mib, SSE_FALLBACK.accumulated),
        rate_limit_rpm: Number(settingsForm.rate_limit_rpm),
        rate_limit_burst: Number(settingsForm.rate_limit_burst),
        rate_limit_max_inflight: Number(settingsForm.rate_limit_max_inflight),
        channel_gate_rpm_default: Number(settingsForm.channel_gate_rpm_default),
        channel_gate_tpm_default: Number(settingsForm.channel_gate_tpm_default),
        task_poll_seconds: Number(settingsForm.task_poll_seconds),
        task_max_attempts: Number(settingsForm.task_max_attempts),
    }
    // api_key 三态：字段缺省 = 不修改；"" = 显式清空；非空 = 设置
    if (settingsForm.api_key.trim()) {
        payload.api_key = settingsForm.api_key.trim()
    } else if (clearKey.value) {
        payload.api_key = ''
    }
    savingSettings.value = true
    try {
        await request({ url: '/admin/plugin/ai/settings/save', method: 'POST', data: payload })
        ElMessage.success(t('plugin.ai.overview.settings.saveSuccess'))
        settingsVisible.value = false
        await load(true)
    } catch {
        // 请求层已提示；保留弹窗供修正
    } finally {
        savingSettings.value = false
    }
}

const onResize = () => resizeTrendChart()

onMounted(() => {
    load()
    window.addEventListener('resize', onResize)
})

onUnmounted(() => {
    window.removeEventListener('resize', onResize)
    trendChart?.dispose()
    trendChart = null
})
</script>

<style scoped lang="scss">
.plugin-ai-overview {
    .overview-alert {
        margin-bottom: 10px;
    }

    .overview-toolbar {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
        flex-wrap: wrap;
        margin-bottom: 12px;
    }

    .overview-toolbar-left,
    .overview-toolbar-right {
        display: flex;
        align-items: center;
        gap: 10px;
        flex-wrap: wrap;
    }

    .overview-updated {
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .btn-text {
        margin-left: 4px;
    }

    .overview-kpi-row {
        margin-bottom: 12px;
    }

    .overview-kpi-card {
        margin-bottom: 12px;

        .kpi-title {
            font-size: 13px;
            color: var(--el-text-color-secondary);
        }

        .kpi-value {
            margin: 8px 0 6px;
            font-size: 26px;
            font-weight: 600;
            line-height: 1.2;
            color: var(--el-text-color-primary);
            word-break: break-all;
        }

        .kpi-sub {
            font-size: 12px;
            color: var(--el-text-color-secondary);
        }
    }

    .overview-card {
        margin-bottom: 12px;
    }

    .card-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
        font-weight: 600;
    }

    .card-hint {
        font-size: 12px;
        font-weight: 400;
        color: var(--el-text-color-secondary);
    }

    .overview-chart {
        width: 100%;
        height: 300px;
    }

    .health-grid {
        display: grid;
        grid-template-columns: repeat(3, minmax(0, 1fr));
        gap: 10px;
        margin-bottom: 12px;
    }

    .health-item {
        padding: 10px;
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 6px;
    }

    .health-label {
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .health-value {
        margin-top: 4px;
        font-size: 22px;
        font-weight: 600;

        &.success {
            color: var(--el-color-success);
        }

        &.danger {
            color: var(--el-color-danger);
        }

        &.warning {
            color: var(--el-color-warning);
        }
    }

    .problems-title {
        margin: 6px 0;
        font-size: 13px;
        font-weight: 600;
    }

    .problem-item {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 4px 0;
        font-size: 12px;
    }

    .problem-name {
        flex: 0 0 auto;
        font-weight: 600;
    }

    .problem-error {
        overflow: hidden;
        min-width: 0;
        color: var(--el-text-color-secondary);
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .settings-alert {
        margin-bottom: 12px;
    }

    .settings-scroll {
        max-height: 62vh;
        overflow-y: auto;
        padding-right: 6px;
    }

    .group-tip {
        margin: 0 0 10px;
    }

    .form-tip {
        width: 100%;
        margin-top: 2px;
        font-size: 12px;
        line-height: 18px;
        color: var(--el-text-color-secondary);
    }

    .key-actions {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-top: 6px;
    }
}
</style>
