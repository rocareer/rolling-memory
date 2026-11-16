<!-- plugin: rolling-memory v0.0.58 -->
<template>
    <div class="default-main plugin-rolling-memory">
        <div class="rm-hero">
            <div class="rm-hero-left">
                <div class="rm-hero-titles">
                    <div class="rm-hero-title-row">
                        <span class="rm-hero-title"
                            >{{ t('plugin.rolling-memory.title') }} · {{ t('plugin.rolling-memory.overview.heroTitleSuffix') }}</span
                        >
                        <el-tag v-if="envHealth.active_harness_label" size="small" type="primary" effect="dark" data-testid="badge-active-harness">
                            {{ t('plugin.rolling-memory.overview.envLabel') }}: {{ envHealth.active_harness_label }}
                        </el-tag>
                        <el-tag size="small" :type="healthTagType(envHealth.health_score)" effect="dark" data-testid="badge-memory-health">
                            {{ t('plugin.rolling-memory.overview.healthLabel') }}:
                            {{ healthKnown ? `${envHealth.health_score}%` : t('plugin.rolling-memory.overview.unknownStatus') }}
                        </el-tag>
                    </div>
                    <span class="rm-hero-sub">{{ t('plugin.rolling-memory.overview.heroSub') }}</span>
                </div>
            </div>
            <div class="rm-hero-right">
                <el-switch
                    v-model="envHealth.auto_sync_enabled"
                    :disabled="typeof envHealth.auto_sync_enabled !== 'boolean'"
                    :active-text="t('plugin.rolling-memory.overview.autoSyncModeLbl')"
                    :inactive-text="t('plugin.rolling-memory.overview.manualSyncModeLbl')"
                    @change="handleToggleAutoSync"
                />
                <el-button
                    type="success"
                    size="small"
                    :loading="reconcilingLayer === 'all'"
                    data-testid="btn-reconcile-all"
                    @click="handleReconcile('all')"
                >
                    {{ t('plugin.rolling-memory.overview.reconcileAllBtn') }}
                    <template v-if="reconcilingLayer === 'all'"> ({{ reconcilePct }}%)</template>
                    <template v-else-if="envHealth.total_pending_sync > 0"> ({{ envHealth.total_pending_sync }})</template>
                </el-button>
                <el-button size="small" :loading="checkingHealth" data-testid="btn-check-health" @click="refreshEnvHealth">
                    {{ t('plugin.rolling-memory.overview.checkHealthBtn') }}
                    <template v-if="checkingHealth"> ({{ progressPercent }}%)</template>
                </el-button>
                <el-button size="small" plain data-testid="btn-system-update" @click="showSystemUpdateDialog = true">
                    {{ t('plugin.rolling-memory.systemUpdate.openBtn') }}
                </el-button>
                <el-button type="primary" size="small" :loading="syncing" data-testid="btn-bg-sync" @click="handleSync">
                    {{ t('plugin.rolling-memory.overview.syncBtn') }}
                    <template v-if="syncing"> ({{ bgSyncPct }}%)</template>
                </el-button>
            </div>
        </div>

        <!-- 运行环境检测（Claude Code / ZCode / Codex 等）与五维记忆健康度面板；
             加载/刷新态由进度面板与页眉 Refreshing chip 表达，不再用整卡遮罩压暗已有数据。 -->
        <el-card shadow="never" class="mt-16 env-health-card">
            <!-- 首帧采集进度：阶段与计数来自引擎的实时进度事件（非估计动画） -->
            <div v-if="!healthKnown" class="env-progress" data-testid="env-health-progress">
                <div class="ep-head">
                    <span class="ep-title">{{ t('plugin.rolling-memory.overview.progressTitle') }}</span>
                    <span class="ep-elapsed"
                        >{{ progressPercent }}% · {{ t('plugin.rolling-memory.overview.progressElapsed', { s: progressElapsedSec }) }}</span
                    >
                </div>
                <el-progress
                    :percentage="progressPercent"
                    :indeterminate="progressIndeterminate"
                    :duration="1.6"
                    :stroke-width="8"
                    :show-text="false"
                />
                <div class="ep-steps">
                    <div v-for="st in progressSteps" :key="st.key" class="ep-step" :class="`is-${st.state}`">
                        <span class="ep-dot" aria-hidden="true" />
                        <span class="ep-label">{{ st.label }}</span>
                        <span v-if="st.detail" class="ep-detail">{{ st.detail }}</span>
                    </div>
                </div>
                <div class="ep-foot">
                    <span v-if="envProgress?.error" class="ep-error">{{ t('plugin.rolling-memory.overview.progressError') }}</span>
                    <span v-else class="ep-hint">{{ t('plugin.rolling-memory.overview.progressHint') }}</span>
                    <el-button v-if="envProgress?.error" size="small" @click="loadEnvHealth(true)">
                        {{ t('plugin.rolling-memory.overview.progressRetry') }}
                    </el-button>
                </div>
            </div>
            <template v-else>
                <!-- 数据就绪：展示上次采集用时；后台刷新时轻量提示（旧数据保持可见） -->
                <div class="env-meta-line" data-testid="env-health-meta">
                    <span class="em-updated">{{ lastFrameText }}</span>
                    <span v-if="envProgress?.running || checkingHealth" class="em-refreshing">
                        <span class="em-spin" aria-hidden="true" />
                        {{ progressPercent }}% · {{ t('plugin.rolling-memory.overview.progressRefreshing', { s: progressElapsedSec }) }}
                    </span>
                </div>
                <el-row :gutter="16">
                    <el-col :xs="24" :md="11">
                        <div class="sec-head">
                            <span class="sec-title">{{ t('plugin.rolling-memory.overview.harnessesTitle') }}</span>
                            <span v-if="envHealth.active_workspace" class="ws-chip">{{ envHealth.active_workspace }}</span>
                        </div>
                        <div class="harness-grid">
                            <div
                                v-for="h in envHealth.harnesses"
                                :key="h.id"
                                class="harness-pill"
                                :class="{ 'is-active': h.active, 'is-detected': h.detected }"
                                :data-testid="`harness-item-${h.id}`"
                            >
                                <div class="hp-top">
                                    <span class="hp-name">{{ h.label }}</span>
                                    <el-tag v-if="h.active" size="small" type="success" effect="dark">ACTIVE</el-tag>
                                    <el-tag v-else-if="h.detected" size="small" type="info" effect="plain">INSTALLED</el-tag>
                                </div>
                                <div class="hp-wiring">
                                    <span :class="{ ok: h.skill_wired }">Skill {{ h.skill_wired ? '✓' : '·' }}</span>
                                    <span :class="{ ok: h.mcp_wired }">MCP {{ h.mcp_wired ? '✓' : '·' }}</span>
                                    <span v-if="h.hooks_supported" :class="{ ok: h.hooks_wired }">Hooks {{ h.hooks_wired ? '✓' : '·' }}</span>
                                </div>
                                <!-- 两条能力分开显示：会话清单可发现（sessions_scan_supported）就显示
                                     会话数；轮次内容可读（turns_scan_supported）才显示 Turns。
                                     宿主能发现会话但读不了内容时（如 dsh 的 zstd 转录），
                                     隐藏 Turns 而不是把「未统计」写成 0/0。 -->
                                <div class="hp-stats" v-if="showsSessions(h) || showsTurns(h)">
                                    <span v-if="showsSessions(h)"
                                        >Sessions: <b>{{ h.sessions_count || 0 }}</b></span
                                    >
                                    <span v-if="showsTurns(h)"
                                        >Turns: <b>{{ h.synced_turns_count || 0 }}/{{ h.local_turns_count || 0 }}</b></span
                                    >
                                    <span v-if="showsTurns(h) && h.unsynced_turns_count > 0" class="pending-num">
                                        +{{ h.unsynced_turns_count }} {{ t('plugin.rolling-memory.overview.pendingSyncLbl') }}
                                    </span>
                                </div>
                                <!-- 单胜者的 ACTIVE 只标当前环境：其余宿主用最后活动时间如实呈现
                                     自己的活跃度，不引入任何时间窗阈值。 -->
                                <div class="hp-last-active" v-if="h.last_active_text && h.last_active_text !== '-'">
                                    Last active: <b>{{ h.last_active_text }}</b>
                                </div>
                            </div>
                        </div>
                    </el-col>
                    <el-col :xs="24" :md="13">
                        <div class="sec-head">
                            <span class="sec-title">{{
                                isLocalMode
                                    ? t('plugin.rolling-memory.overview.healthChecksTitleLocal')
                                    : t('plugin.rolling-memory.overview.healthChecksTitle')
                            }}</span>
                            <el-progress
                                v-if="healthKnown"
                                :percentage="envHealth.health_score"
                                :status="envHealth.health_score >= 85 ? 'success' : envHealth.health_score >= 60 ? 'warning' : 'exception'"
                                :stroke-width="10"
                                style="width: 160px"
                            />
                            <span v-else>{{ t('plugin.rolling-memory.overview.healthUnavailable') }}</span>
                        </div>
                        <div v-if="healthKnown" class="health-check-list">
                            <div v-for="chk in envHealth.health_checks" :key="chk.key" class="hc-row">
                                <div class="hc-left">
                                    <span class="hc-dot" :class="`st-${chk.status}`"></span>
                                    <span class="hc-label">{{ isEn ? chk.label_en : chk.label_zh }}</span>
                                </div>
                                <span class="hc-detail" :title="isEn ? chk.detail_en : chk.detail_zh">{{
                                    isEn ? chk.detail_en : chk.detail_zh
                                }}</span>
                                <span class="hc-score">{{ chk.score }}/{{ chk.max_score }}</span>
                            </div>
                        </div>
                    </el-col>
                </el-row>
            </template>
        </el-card>

        <!-- 三层认知记忆统计卡片（含本地 vs 远端对比与单层手动对齐同步按钮） -->
        <el-row :gutter="16" class="mt-16">
            <el-col :xs="24" :sm="8">
                <el-card
                    shadow="hover"
                    class="layer-card layer-bg"
                    data-testid="card-layer-background"
                    role="button"
                    tabindex="0"
                    @click="goLayer('background')"
                    @keydown.enter.prevent="goLayer('background')"
                    @keydown.space.prevent="goLayer('background')"
                >
                    <div class="layer-head">
                        <span class="layer-badge bg">{{ t('plugin.rolling-memory.background.badge') }}</span>
                        <el-tag size="small" :type="layerTagType('background')" effect="plain">
                            {{ layerStatus('background') }}
                        </el-tag>
                    </div>
                    <div class="stat-val">{{ statsLoaded ? fmtCount(stats.background_count) : '—' }}</div>
                    <div class="stat-meta">
                        <span
                            >{{ t('plugin.rolling-memory.background.dirsLabel') }}<b>{{ stats.background_dir_count }}</b
                            >{{ t('plugin.rolling-memory.background.dirsUnit') }}</span
                        >
                        <span
                            >{{ t('plugin.rolling-memory.overview.localVaultLbl') }}:
                            <b>{{ layerAlign('background').local_vault_count ?? stats.background_count ?? '—' }}</b></span
                        >
                        <span v-if="!isLocalMode"
                            >{{ t('plugin.rolling-memory.overview.remoteCloudLbl') }}:
                            <b>{{ alignmentKnown('background') ? layerAlign('background').remote_cloud_count : '—' }}</b></span
                        >
                    </div>
                    <div class="layer-foot">
                        <span class="jump-hint">{{ t('plugin.rolling-memory.overview.jumpHint') }}</span>
                        <el-button
                            size="small"
                            type="primary"
                            plain
                            :loading="reconcilingLayer === 'background'"
                            data-testid="btn-reconcile-background"
                            @click.stop="handleReconcile('background')"
                        >
                            {{ t('plugin.rolling-memory.overview.reconcileLayerBtn') }}
                            <template v-if="reconcilingLayer === 'background'"> ({{ reconcilePct }}%)</template>
                        </el-button>
                    </div>
                </el-card>
            </el-col>
            <el-col :xs="24" :sm="8">
                <el-card
                    shadow="hover"
                    class="layer-card layer-epi"
                    data-testid="card-layer-episodic"
                    role="button"
                    tabindex="0"
                    @click="goLayer('episodic')"
                    @keydown.enter.prevent="goLayer('episodic')"
                    @keydown.space.prevent="goLayer('episodic')"
                >
                    <div class="layer-head">
                        <span class="layer-badge epi">{{ t('plugin.rolling-memory.episodic.badge') }}</span>
                        <el-tag size="small" :type="layerTagType('episodic')" effect="plain">
                            {{ layerStatus('episodic') }}
                        </el-tag>
                    </div>
                    <div class="stat-val">{{ statsLoaded ? fmtCount(stats.episodic_count) : '—' }}</div>
                    <div class="stat-meta">
                        <span
                            >{{ t('plugin.rolling-memory.overview.localVaultLbl') }}:
                            <b>{{ layerAlign('episodic').local_vault_count ?? stats.episodic_count ?? '—' }}</b></span
                        >
                        <span v-if="!isLocalMode"
                            >{{ t('plugin.rolling-memory.overview.remoteCloudLbl') }}:
                            <b>{{ alignmentKnown('episodic') ? layerAlign('episodic').remote_cloud_count : '—' }}</b></span
                        >
                        <span v-if="layerAlign('episodic').harness_unsynced_turns > 0" class="pending-num">
                            {{ t('plugin.rolling-memory.overview.harnessUnsyncedLbl') }}: <b>{{ layerAlign('episodic').harness_unsynced_turns }}</b>
                        </span>
                        <span v-if="envHealth.episodic_index?.total_docs > 0">
                            {{ t('plugin.rolling-memory.episodic.indexCompatibleLbl') }}:
                            <b
                                >{{ fmtCount(envHealth.episodic_index.compatible || 0) }}/{{ fmtCount(envHealth.episodic_index.total_docs) }} ({{
                                    envHealth.episodic_index.coverage_pct ?? 0
                                }}%)</b
                            >
                        </span>
                    </div>
                    <div class="layer-foot">
                        <span class="jump-hint">{{ t('plugin.rolling-memory.overview.jumpHint') }}</span>
                        <div class="layer-foot-btns">
                            <el-button
                                v-if="envHealth.episodic_index?.total_docs > 0"
                                size="small"
                                :type="envHealth.episodic_index?.paused ? 'success' : 'warning'"
                                plain
                                :loading="togglingEpisodicPause"
                                data-testid="btn-overview-toggle-episodic-pause"
                                @click.stop="handleToggleEpisodicPause(!envHealth.episodic_index?.paused)"
                            >
                                {{
                                    envHealth.episodic_index?.paused
                                        ? t('plugin.rolling-memory.episodic.resumeSyncBtn')
                                        : t('plugin.rolling-memory.episodic.pauseSyncBtn')
                                }}
                            </el-button>
                            <el-button
                                size="small"
                                type="success"
                                plain
                                :loading="reconcilingLayer === 'episodic'"
                                data-testid="btn-reconcile-episodic"
                                @click.stop="handleReconcile('episodic')"
                            >
                                {{ t('plugin.rolling-memory.overview.reconcileLayerBtn') }}
                                <template v-if="reconcilingLayer === 'episodic'"> ({{ reconcilePct }}%)</template>
                            </el-button>
                        </div>
                    </div>
                </el-card>
            </el-col>
            <el-col :xs="24" :sm="8">
                <el-card
                    shadow="hover"
                    class="layer-card layer-noe"
                    data-testid="card-layer-noetic"
                    role="button"
                    tabindex="0"
                    @click="goLayer('noetic')"
                    @keydown.enter.prevent="goLayer('noetic')"
                    @keydown.space.prevent="goLayer('noetic')"
                >
                    <div class="layer-head">
                        <span class="layer-badge noe">{{ t('plugin.rolling-memory.noetic.badge') }}</span>
                        <el-tag size="small" :type="layerTagType('noetic')" effect="plain">
                            {{ layerStatus('noetic') }}
                        </el-tag>
                    </div>
                    <div class="stat-val">{{ statsLoaded ? fmtCount(stats.noetic_active_count) : '—' }}</div>
                    <div class="stat-meta">
                        <span
                            >{{ t('plugin.rolling-memory.overview.localVaultLbl') }}:
                            <b>{{ layerAlign('noetic').local_vault_count ?? stats.noetic_active_count ?? '—' }}</b></span
                        >
                        <span v-if="!isLocalMode"
                            >{{ t('plugin.rolling-memory.overview.remoteCloudLbl') }}:
                            <b>{{ alignmentKnown('noetic') ? layerAlign('noetic').remote_cloud_count : '—' }}</b></span
                        >
                        <span
                            >{{ t('plugin.rolling-memory.noetic.staleLabel') }}<b>{{ stats.noetic_stale_count }}</b
                            >{{ t('plugin.rolling-memory.noetic.staleUnit') }}</span
                        >
                    </div>
                    <div class="layer-foot">
                        <span class="jump-hint">{{ t('plugin.rolling-memory.overview.jumpHint') }}</span>
                        <el-button
                            size="small"
                            type="warning"
                            plain
                            :loading="reconcilingLayer === 'noetic'"
                            data-testid="btn-reconcile-noetic"
                            @click.stop="handleReconcile('noetic')"
                        >
                            {{ t('plugin.rolling-memory.overview.reconcileLayerBtn') }}
                            <template v-if="reconcilingLayer === 'noetic'"> ({{ reconcilePct }}%)</template>
                        </el-button>
                    </div>
                </el-card>
            </el-col>
        </el-row>

        <!-- 跨层聚合列表（全部记忆单元 · 标准 BuildAdmin TableHeader + Table + ComSearch）；
             「对齐全部 / 同步目录」主操作只在页眉（rm-hero）提供一处，表格工具栏不再重复同组按钮。 -->
        <div class="mt-16">
            <layerBrowser ref="browserRef" layer="all" :title="t('plugin.rolling-memory.overview.aggregateTitle')" @loaded="onStatsLoaded" />
        </div>

        <systemUpdateDialog v-model="showSystemUpdateDialog" @updated="refreshEnvHealth" />
    </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useAdminInfo } from '@/stores/adminInfo'
import { useLoadingProgressText } from '@/utils/loadingProgress'
import { MonitorSocket } from '@/utils/monitorSocket'
import request from '@/utils/request'
import layerBrowser from './layerBrowser.vue'
import systemUpdateDialog from './systemUpdateDialog.vue'

defineOptions({
    name: 'plugin/rolling-memory/index',
})

const { t, locale } = useI18n()
const isEn = computed(() =>
    String(locale.value || '')
        .toLowerCase()
        .startsWith('en')
)
const router = useRouter()
const adminInfo = useAdminInfo()
const syncing = ref(false)
const checkingHealth = ref(false)
const showSystemUpdateDialog = ref(false)
const reconcilingLayer = ref('')
const togglingEpisodicPause = ref(false)
const { percent: reconcilePct } = useLoadingProgressText(() => Boolean(reconcilingLayer.value), undefined, 1200)
const { percent: bgSyncPct } = useLoadingProgressText(syncing, undefined, 1200)
const browserRef = ref<any>(null)
let envSocket: MonitorSocket | null = null

// 首帧占位：列表接口返回前大数字显示「—」而不是「0」（0 会被读成「库是空的」）。
const statsLoaded = ref(false)
// 计数千分位（56148 → 56,148）；非数值原样回退（缺数据时显示「—」）。
const fmtCount = (value: unknown) => (typeof value === 'number' ? value.toLocaleString() : (value ?? '—'))

const stats = ref<any>({
    total_units: 0,
    background_count: 0,
    background_dir_count: 0,
    background_file_count: 0,
    background_chunk_count: 0,
    episodic_count: 0,
    episodic_cell_count: 0,
    noetic_active_count: 0,
    noetic_stale_count: 0,
    noetic_cited_links: 0,
})

const envHealth = ref<any>({ harnesses: [], health_checks: [], layers: {} })
// 会话清单与轮次内容是两个独立能力：DSH 的会话能被发现（计数如实），
// 但其 zstd 转录本构建尚不解析 ⇒ 只显示会话数，不显示 0/0 的 Turns。
const showsSessions = (h: any) => (h.sessions_scan_supported ?? h.turns_scan_supported ?? true) !== false
const showsTurns = (h: any) => h.turns_scan_supported !== false
const healthKnown = computed(
    () =>
        typeof envHealth.value.health_score === 'number' &&
        (envHealth.value.mode === 'local' || (envHealth.value.mode === 'cloud' && envHealth.value.cloud_online === true))
)

const handleToggleAutoSync = async (val: string | number | boolean) => {
    const enabled = Boolean(val)
    try {
        await request<any>({
            url: '/admin/plugin/rolling-memory/toggle',
            method: 'POST',
            data: { key: 'cloud_auto_sync', value: enabled ? 'true' : 'false' },
        })
        envHealth.value.auto_sync_enabled = enabled
        envHealth.value.sync_mode = enabled ? 'auto' : 'manual'
        ElMessage.success(
            enabled ? t('plugin.rolling-memory.overview.syncModeSwitchedAuto') : t('plugin.rolling-memory.overview.syncModeSwitchedManual')
        )
    } catch {
        envHealth.value.auto_sync_enabled = !enabled
    }
}

const layerAlign = (layer: string) => envHealth.value?.layers?.[layer] || {}
// 运行模式口径与后端 isLocalModeForHome 一致：只有显式 cloud 才算云端，缺省/未加载/未知一律按本地处置。
// 否则环境面板（单次约 9s）尚在途中时，本地模式的页面会先闪出一条「远端云端: —」。
const isLocalMode = computed(() => envHealth.value?.mode !== 'cloud')
const alignmentKnown = (layer: string) =>
    Boolean(envHealth.value?.layers?.[layer] && (isLocalMode.value || (envHealth.value?.mode === 'cloud' && envHealth.value?.cloud_online === true)))
const layerTagType = (layer: string) => (!alignmentKnown(layer) ? 'info' : layerAlign(layer).aligned ? 'success' : 'warning')
const layerStatus = (layer: string) => {
    if (!alignmentKnown(layer)) return t('plugin.rolling-memory.overview.unknownStatus')
    return layerAlign(layer).aligned
        ? t('plugin.rolling-memory.overview.alignedTag')
        : `${t('plugin.rolling-memory.overview.pendingTag')} (${layerAlign(layer).total_pending_sync})`
}

const healthTagType = (score: number) => {
    if (!healthKnown.value) return 'info'
    if (score >= 85) return 'success'
    if (score >= 60) return 'warning'
    return 'danger'
}

// force=true 只在用户显式「重新检测 / 重试」路径使用：服务端 TTL 复用（默认 30s）对
// 首屏与页面切换是纯收益，但用户手动要求现算时不得复用旧读数。
const loadEnvHealth = async (force = false) => {
    checkingHealth.value = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/env/health',
            method: 'POST',
            data: { force },
            timeout: 0,
        })
        envHealth.value = res.data?.data || { harnesses: [], health_checks: [], layers: {} }
    } catch {
        envHealth.value = { harnesses: [], health_checks: [], layers: {} }
    } finally {
        checkingHealth.value = false
    }
}

// 「重新检测」：有实时面订阅时排入共享流——引擎的进度帧与最终数据帧走同一条 SSE，
// 页面凭既有进度面板 / 页眉 chip 全程可见；服务端在无订阅时直接同步采集并把载荷带回。
// 端点不可用（旧服务端）或流未连时退回一次性取值，行为与历史一致。
const refreshEnvHealth = async () => {
    if (checkingHealth.value) return
    checkingHealth.value = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/env/health/refresh',
            method: 'POST',
            data: {},
            timeout: 0,
        })
        const data = res.data?.data
        if (data && (data.harnesses || data.layers)) {
            // 无实时面订阅时的同步回退载荷：直接落地并收尾。
            envHealth.value = data
            checkingHealth.value = false
        }
        // queued：等待 SSE 的进度帧与最终数据帧，onmessage 负责收尾。
    } catch {
        await loadEnvHealth(true)
    }
}

// 采集进度（env_health_progress 瞬态帧）：引擎在采集期逐步上报阶段与计数，
// 首帧等待据此显示真实进度而不是无名转圈；帧只描述「正在做什么」，不承载状态。
type EnvProgress = {
    running?: boolean
    stage?: string
    status?: string
    harness?: string
    phase?: string
    sessions_done?: number
    sessions_total?: number
    turns?: number
    turns_cap?: number
    elapsed_ms?: number
    started_at?: number
    error?: string
}
const envProgress = ref<EnvProgress | null>(null)
const progressDoneStages = ref<string[]>([])
const progressNow = ref(Date.now())
let progressTicker: number | null = null

const progressStageMeta: { key: string; labelKey: string }[] = [
    { key: 'vault', labelKey: 'plugin.rolling-memory.overview.progressStageVault' },
    { key: 'wiring', labelKey: 'plugin.rolling-memory.overview.progressStageWiring' },
    { key: 'scan', labelKey: 'plugin.rolling-memory.overview.progressStageScan' },
    { key: 'index', labelKey: 'plugin.rolling-memory.overview.progressStageIndex' },
]

const startProgressTicker = () => {
    if (progressTicker != null) return
    progressTicker = window.setInterval(() => {
        progressNow.value = Date.now()
    }, 250)
}
const stopProgressTicker = () => {
    if (progressTicker != null) {
        window.clearInterval(progressTicker)
        progressTicker = null
    }
}

const applyEnvProgress = (d: EnvProgress) => {
    const prev = envProgress.value || {}
    envProgress.value = { ...prev, ...d }
    if (d.status === 'done' && d.stage && d.stage !== 'all' && !progressDoneStages.value.includes(d.stage)) {
        progressDoneStages.value = [...progressDoneStages.value, d.stage]
    }
    const rawPct = computeRawProgressPct(envProgress.value, progressDoneStages.value)
    if (rawPct > progressPeakPct.value) {
        progressPeakPct.value = rawPct
    }
    if (d.running) {
        startProgressTicker()
        return
    }
    if (d.running === false && !d.error) {
        stopProgressTicker()
        if (checkingHealth.value) {
            checkingHealth.value = false
        }
        // 收尾帧后紧跟着就是数据帧：留一小段淡出窗口，避免面板闪跳。
        window.setTimeout(() => {
            if (envProgress.value && envProgress.value.running === false && !envProgress.value.error) {
                envProgress.value = null
            }
        }, 1500)
    }
    if (d.error) {
        stopProgressTicker()
        progressDoneStages.value = []
        progressPeakPct.value = 8
        // 显式刷新在已有数据时走页眉 chip 而不再展示面板：失败须以轻提示收尾，避免按钮一直转圈。
        if (checkingHealth.value) {
            checkingHealth.value = false
        }
        if (healthKnown.value) {
            ElMessage.warning(t('plugin.rolling-memory.overview.refreshFailed'))
        }
    }
}

const progressElapsedSec = computed(() => {
    const p = envProgress.value
    if (!p) return '0.0'
    if (p.running && typeof p.started_at === 'number') {
        return Math.max(0, (progressNow.value - p.started_at) / 1000).toFixed(1)
    }
    if (typeof p.elapsed_ms === 'number') {
        return (p.elapsed_ms / 1000).toFixed(1)
    }
    return '0.0'
})

// 阶段权重连续单调映射（vault 0..20% → wiring 20..35% → scan 35..88% → index 88..98%），不倒退。
const progressPeakPct = ref(8)
const computeRawProgressPct = (p: EnvProgress | null, done: string[]): number => {
    let raw = 8
    if (done.includes('index') || p?.stage === 'index') {
        raw = done.includes('index') ? 98 : 92
    } else if (done.includes('scan') || p?.stage === 'scan') {
        let ratio = 0.25
        if (typeof p?.turns_cap === 'number' && p.turns_cap > 0) {
            ratio = Math.max(ratio, Math.min(1, (p.turns || 0) / p.turns_cap))
        }
        if (typeof p?.sessions_total === 'number' && p.sessions_total > 0) {
            ratio = Math.max(ratio, Math.min(1, (p.sessions_done || 0) / p.sessions_total))
        }
        raw = Math.round(35 + ratio * 50)
    } else if (done.includes('wiring') || p?.stage === 'wiring') {
        raw = done.includes('wiring') ? 35 : 28
    } else if (done.includes('vault') || p?.stage === 'vault') {
        raw = done.includes('vault') ? 20 : 12
    }
    return raw
}
const progressIndeterminate = computed(() => !envProgress.value?.stage && progressDoneStages.value.length === 0)
const progressPercent = computed(() => {
    const raw = computeRawProgressPct(envProgress.value, progressDoneStages.value)
    return Math.min(99, Math.max(5, Math.max(raw, progressPeakPct.value)))
})

const progressSteps = computed(() => {
    const p = envProgress.value
    const steps = progressStageMeta.map(({ key, labelKey }) => {
        let state: 'done' | 'active' | 'idle' = 'idle'
        if (progressDoneStages.value.includes(key)) {
            state = 'done'
        } else if (p?.running && p?.stage === key) {
            state = 'active'
        }
        let detail = ''
        if (key === 'scan' && p?.harness) {
            const parts = [p.harness]
            if (typeof p.sessions_done === 'number' && p.sessions_done > 0) {
                parts.push(t('plugin.rolling-memory.overview.progressSessions', { done: p.sessions_done }))
            }
            if (typeof p.turns_cap === 'number' && p.turns_cap > 0) {
                parts.push(t('plugin.rolling-memory.overview.progressTurns', { turns: p.turns || 0, cap: p.turns_cap }))
            }
            detail = parts.join(' · ')
        }
        return { key, label: t(labelKey), state, detail }
    })
    // 阶段单调化：进度帧在突发时限流可能被丢弃，'后续阶段已开始' 蕴含 '前序阶段已完成'，
    // 据此补齐错过的完成状态，时间线不因单个丢帧而回显为待办。
    let advanced = false
    for (let i = steps.length - 1; i >= 0; i--) {
        if (steps[i].state === 'idle' && advanced) {
            steps[i].state = 'done'
        }
        if (steps[i].state !== 'idle') {
            advanced = true
        }
    }
    return steps
})

// 数据帧时间线：上次采集完成时刻与用时（progress 帧的收尾 elapsed_ms）。
const lastFrameAt = ref<number | null>(null)
const lastFrameElapsedMs = ref<number | null>(null)
const lastFrameText = computed(() => {
    if (!lastFrameAt.value) return ''
    const time = new Date(lastFrameAt.value).toLocaleTimeString(isEn.value ? 'en-US' : 'zh-CN', { hour12: false })
    const s = lastFrameElapsedMs.value != null ? Math.max(0.1, lastFrameElapsedMs.value / 1000).toFixed(1) : '0.1'
    return t('plugin.rolling-memory.overview.progressUpdatedAt', { time, s })
})

// 实时面走同源 MonitorSocket WebSocket 共享房间（rm/env_health）：服务端 watcher 在配置/金库/宿主会话库
// 指纹变化时推帧，脱离浏览器 HTTP/1.1 6 连接池限制。
const startEnvHealthStream = () => {
    stopEnvHealthStream()
    const token = adminInfo.token
    if (!token) return
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
    envSocket = new MonitorSocket({
        token,
        room: 'rm/env_health',
        onMessage: (env: any) => {
            if (!env?.event || env.data == null) return
            const d = env.data
            if (env.event === 'env_health_progress') {
                applyEnvProgress(d as EnvProgress)
                return
            }
            if (env.event !== 'env_health') return
            if (d && (d.harnesses || d.layers)) {
                envHealth.value = d
            }
            checkingHealth.value = false
            lastFrameAt.value = Date.now()
            lastFrameElapsedMs.value = typeof envProgress.value?.elapsed_ms === 'number' ? envProgress.value.elapsed_ms : null
            progressDoneStages.value = []
            progressPeakPct.value = 8
        },
    })
    envSocket.start()
}

const stopEnvHealthStream = () => {
    if (envSocket) {
        envSocket.stop()
        envSocket = null
    }
}

const handleReconcile = async (layer: 'all' | 'episodic' | 'background' | 'noetic') => {
    if (reconcilingLayer.value) return
    reconcilingLayer.value = layer
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/sync/reconcile',
            method: 'POST',
            data: { layer, limit: 80 },
            timeout: 0,
        })
        const rep = res.data?.data
        if (rep) {
            let summary = ''
            if (isLocalMode.value) {
                if (layer === 'noetic') {
                    summary = t('plugin.rolling-memory.noetic.reconcileSuccessLocal')
                } else if (layer === 'background') {
                    summary = t('plugin.rolling-memory.background.reconcileSuccessLocal', { bg: rep.background_synced_files ?? 0 })
                } else if (layer === 'episodic') {
                    summary = t('plugin.rolling-memory.episodic.reconcileSuccessLocal', { har: rep.episodic_imported_from_harness ?? 0 })
                } else {
                    summary = t('plugin.rolling-memory.overview.reconcileSuccessLocal', {
                        har: rep.episodic_imported_from_harness ?? 0,
                        bg: rep.background_synced_files ?? 0,
                        score: rep.status?.health_score ?? envHealth.value?.health_score ?? 100,
                    })
                }
            } else {
                summary = t('plugin.rolling-memory.overview.reconcileResult', {
                    har: rep.episodic_imported_from_harness ?? 0,
                    files: rep.background_synced_files ?? 0,
                    epi: rep.episodic_pushed_to_remote ?? 0,
                    bg: rep.background_pushed_to_remote ?? 0,
                    noe: rep.noetic_pushed_to_remote ?? 0,
                })
            }
            const actionableWarnings = (rep.warnings || []).filter(
                (w: string) => !String(w).includes('episodic index backfill covers only the scanned harness window')
            )
            if (actionableWarnings.length) {
                ElMessage.warning({
                    message: `${summary}\n${t('plugin.rolling-memory.overview.reconcileWarnings')} ${actionableWarnings.join('; ')}`,
                    duration: 0,
                    showClose: true,
                })
            } else {
                ElMessage.success(summary)
            }
            if (rep.status && (rep.status.harnesses || rep.status.layers)) {
                envHealth.value = { ...envHealth.value, ...rep.status }
                checkingHealth.value = false
                lastFrameAt.value = Date.now()
            } else {
                await refreshEnvHealth()
            }
            browserRef.value?.reload()
        } else {
            ElMessage.warning(t('plugin.rolling-memory.overview.reconcileNoReport'))
            await refreshEnvHealth()
        }
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        reconcilingLayer.value = ''
    }
}

const onStatsLoaded = (payload: any) => {
    if (payload) {
        stats.value = payload
        statsLoaded.value = true
    }
}

const goLayer = (layer: 'background' | 'episodic' | 'noetic') => {
    router.push({ path: `/admin/plugin/rolling-memory/${layer}` })
}

const handleToggleEpisodicPause = async (pause: boolean) => {
    if (togglingEpisodicPause.value) return
    togglingEpisodicPause.value = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/sync/reconcile',
            method: 'POST',
            data: { action: pause ? 'pause' : 'resume' },
            timeout: 0,
        })
        const data = res.data?.data
        if (data?.episodic_index) {
            envHealth.value.episodic_index = data.episodic_index
        } else {
            await refreshEnvHealth()
        }
        ElMessage.success(pause ? t('plugin.rolling-memory.episodic.syncPausedToast') : t('plugin.rolling-memory.episodic.syncResumedToast'))
    } catch {
        // 业务与网络错误由请求拦截器统一提示；暂停态以 envHealth 回显为准，不做乐观回滚。
    } finally {
        togglingEpisodicPause.value = false
    }
}

const handleSync = async () => {
    syncing.value = true
    try {
        const res = await request<any>({ url: '/admin/plugin/rolling-memory/background/sync', method: 'POST', data: {}, timeout: 0 })
        const rep = res.data?.data
        if (rep) {
            ElMessage.success(
                t('plugin.rolling-memory.background.syncSummary', {
                    scanned: rep.scanned_files,
                    ingested: rep.ingested_files,
                    unchanged: rep.unchanged_files,
                })
            )
        } else {
            ElMessage.warning(t('plugin.rolling-memory.overview.reconcileNoReport'))
        }
        browserRef.value?.reload()
        refreshEnvHealth()
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        syncing.value = false
    }
}

onMounted(() => {
    checkingHealth.value = true
    startEnvHealthStream()
})

// KeepAlive 切入后台时释放实时房间订阅，回到前台时重新订阅（首次挂载已由 onMounted 启动，跳过首次 onActivated 避免 leave+join 双发）。
let indexDeactivated = false
onActivated(() => {
    if (!indexDeactivated) return
    indexDeactivated = false
    startEnvHealthStream()
})

onDeactivated(() => {
    indexDeactivated = true
    stopEnvHealthStream()
    stopProgressTicker()
})

onBeforeUnmount(() => {
    stopEnvHealthStream()
    stopProgressTicker()
})
</script>

<style scoped>
.mt-16 {
    margin-top: 16px;
}
.rm-hero {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 14px 18px;
    background: var(--el-bg-color-overlay);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
    flex-wrap: wrap;
    gap: 10px;
}
.rm-hero-titles {
    display: flex;
    flex-direction: column;
    gap: 4px;
}
.rm-hero-title-row {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}
.rm-hero-title {
    font-size: 16px;
    font-weight: 600;
}
.rm-hero-sub {
    font-size: 12px;
    color: var(--el-text-color-secondary);
}
.rm-hero-right {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}
.sec-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 10px;
}
.sec-title {
    font-size: 13px;
    font-weight: 600;
}
.ws-chip {
    font-size: 11px;
    font-family: monospace;
    padding: 2px 6px;
    border-radius: 4px;
    background: var(--el-fill-color-light);
    color: var(--el-text-color-regular);
    white-space: nowrap;
}
.env-progress {
    padding: 6px 2px 2px;
}
.ep-head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    margin-bottom: 10px;
}
.ep-title {
    font-size: 13px;
    font-weight: 600;
}
.ep-elapsed {
    font-size: 12px;
    font-family: monospace;
    color: var(--el-text-color-secondary);
}
.ep-steps {
    margin-top: 12px;
    display: flex;
    flex-direction: column;
    gap: 6px;
}
.ep-step {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
}
.ep-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--el-border-color);
    flex: none;
}
.ep-step.is-active {
    color: var(--el-color-primary);
}
.ep-step.is-active .ep-dot {
    background: var(--el-color-primary);
    animation: ep-pulse 1.2s ease-in-out infinite;
}
.ep-step.is-done {
    color: var(--el-text-color-regular);
}
.ep-step.is-done .ep-dot {
    background: var(--el-color-success, #67c23a);
}
.ep-label {
    font-weight: 500;
}
.ep-detail {
    font-family: monospace;
    color: var(--el-text-color-secondary);
}
.ep-foot {
    margin-top: 10px;
    display: flex;
    align-items: center;
    gap: 10px;
}
.ep-hint {
    font-size: 12px;
    color: var(--el-text-color-placeholder);
}
.ep-error {
    font-size: 12px;
    color: var(--el-color-danger);
}
@keyframes ep-pulse {
    0%,
    100% {
        opacity: 1;
        transform: scale(1);
    }
    50% {
        opacity: 0.45;
        transform: scale(0.8);
    }
}
.env-meta-line {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 10px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
}
.em-updated {
    font-family: monospace;
}
.em-refreshing {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--el-color-primary);
}
.em-spin {
    width: 10px;
    height: 10px;
    border: 2px solid var(--el-color-primary-light-5, #a0cfff);
    border-top-color: var(--el-color-primary);
    border-radius: 50%;
    animation: ep-rotate 0.8s linear infinite;
}
@keyframes ep-rotate {
    to {
        transform: rotate(360deg);
    }
}
.harness-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(175px, 1fr));
    gap: 8px;
}
.harness-pill {
    padding: 8px 10px;
    border-radius: 6px;
    border: 1px solid var(--el-border-color-lighter);
    background: var(--el-fill-color-blank);
    font-size: 12px;
}
.harness-pill.is-active {
    border-color: var(--el-color-success);
    background: var(--el-color-success-light-9);
}
.hp-top {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 4px;
}
.hp-name {
    font-weight: 600;
}
.hp-wiring {
    display: flex;
    gap: 8px;
    font-size: 11px;
    color: var(--el-text-color-placeholder);
    margin-bottom: 4px;
}
.hp-wiring span.ok {
    color: var(--el-color-success);
    font-weight: 600;
}
.hp-stats {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    font-size: 11px;
    color: var(--el-text-color-secondary);
}
.pending-num {
    color: var(--el-color-warning);
    font-weight: 600;
}
.hp-last-active {
    margin-top: 3px;
    font-size: 10px;
    color: var(--el-text-color-placeholder);
}
.health-check-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
}
.hc-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    font-size: 12px;
    padding: 5px 8px;
    border-radius: 4px;
    background: var(--el-fill-color-lighter);
}
.hc-left {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 150px;
}
.hc-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--el-color-info);
}
.hc-dot.st-healthy {
    background: var(--el-color-success);
}
.hc-dot.st-warning {
    background: var(--el-color-warning);
}
.hc-dot.st-error {
    background: var(--el-color-danger);
}
.hc-label {
    font-weight: 600;
}
.hc-detail {
    flex: 1;
    color: var(--el-text-color-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.hc-score {
    font-family: monospace;
    font-weight: 600;
}
.layer-card {
    cursor: pointer;
    transition:
        border-color 0.2s,
        transform 0.2s;
}
.layer-card:hover {
    border-color: var(--el-color-primary);
    transform: translateY(-1px);
}
.layer-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 8px;
}
.layer-badge {
    font-size: 13px;
    font-weight: 600;
}
.stat-val {
    font-size: 28px;
    font-weight: 700;
    color: var(--el-color-primary);
    line-height: 1.2;
}
.stat-meta {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
    margin-top: 8px;
}
.layer-foot {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 10px;
    margin-top: 10px;
    padding-top: 8px;
    border-top: 1px dashed var(--el-border-color-lighter);
    color: var(--el-text-color-secondary);
}
.jump-hint {
    flex: 1 1 auto;
    min-width: 0;
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
/* L2 卡同时挂「暂停/继续」与「对齐」两个按钮：容器不换行，提示文字优先让位（ellipsis）。 */
.layer-foot-btns {
    display: flex;
    flex: none;
    gap: 8px;
}
</style>
