<!-- plugin: rolling-memory v0.0.58 -->
<template>
    <div class="default-main plugin-rolling-memory">
        <!-- 顶部提示条（参照 plugins/ai/web/views/channel/index.vue 标准） -->
        <el-alert
            class="rm-alert"
            type="success"
            :closable="false"
            show-icon
            :title="t('plugin.rolling-memory.episodic.pageTitle')"
            :description="t('plugin.rolling-memory.episodic.pageSub')"
        />

        <!-- 4 维统计指标条 -->
        <div class="rm-stats-bar">
            <div class="stat-item success">
                <span class="stat-label">{{ t('plugin.rolling-memory.episodic.totalLabel') }}</span>
                <span class="stat-value">{{ stats.episodic_count || 0 }}</span>
            </div>
            <div class="stat-item primary">
                <span class="stat-label">{{ t('plugin.rolling-memory.episodic.cellsLabel') }}</span>
                <span class="stat-value">{{ stats.episodic_cell_count || 0 }}</span>
            </div>
            <div class="stat-item" :class="epiAlign.harness_unsynced_turns > 0 ? 'warning' : 'info'">
                <span class="stat-label"
                    >{{ t('plugin.rolling-memory.overview.envLabel') }} ({{
                        envHealth.active_harness_label || t('plugin.rolling-memory.overview.unknownStatus')
                    }})</span
                >
                <span class="stat-value">
                    <template v-if="!envHealth.layers?.episodic">
                        {{ t('plugin.rolling-memory.overview.unknownStatus') }}
                    </template>
                    <template v-else-if="epiAlign.harness_unsynced_turns > 0">
                        +{{ epiAlign.harness_unsynced_turns }} {{ t('plugin.rolling-memory.overview.pendingTag') }}
                    </template>
                    <template v-else>
                        {{ t('plugin.rolling-memory.overview.alignedTag') }}
                    </template>
                </span>
            </div>
            <div class="stat-item" :class="!alignmentKnown ? 'info' : epiAlign.aligned ? 'success' : 'warning'">
                <span class="stat-label">{{
                    isLocalMode
                        ? t('plugin.rolling-memory.overview.localVaultLbl')
                        : `${t('plugin.rolling-memory.overview.localVaultLbl')} ⇄ ${t('plugin.rolling-memory.overview.remoteCloudLbl')}`
                }}</span>
                <span v-if="isLocalMode" class="stat-value">{{ epiAlign.local_vault_count ?? stats.episodic_count ?? 0 }}</span>
                <span v-else-if="!alignmentKnown" class="stat-value">{{ t('plugin.rolling-memory.overview.unknownStatus') }}</span>
                <span v-else class="stat-value"
                    >{{ epiAlign.local_vault_count }} / {{ epiAlign.remote_cloud_count }} ({{ epiAlign.alignment_pct }}%)</span
                >
            </div>
        </div>

        <!-- 本机 Harness 会话扫描与同步工作台（ZCode / Claude Code / Codex / Cursor 等） -->
        <div class="harness-sync-box" v-loading="checkingHealth && !envHealth.harnesses?.length" :element-loading-text="healthLoadingText">
            <div class="harness-sync-head">
                <div class="harness-sync-title">
                    <Icon name="lucide-terminal" :size="15" />
                    <span>{{ t('plugin.rolling-memory.overview.harnessesTitle') }}</span>
                    <el-tag v-if="envHealth.active_harness_label" size="small" type="success" effect="dark">
                        {{ t('plugin.rolling-memory.overview.envLabel') }}: {{ envHealth.active_harness_label }}
                    </el-tag>
                    <code v-if="envHealth.active_workspace" class="ws-code">{{ envHealth.active_workspace }}</code>
                </div>
                <div class="harness-sync-actions">
                    <el-button
                        v-blur
                        type="success"
                        size="small"
                        :disabled="!envHealth.active_harness_id"
                        :loading="syncingHarness === envHealth.active_harness_id"
                        data-testid="btn-sync-active-harness"
                        @click="handleSyncHarness(envHealth.active_harness_id)"
                    >
                        <Icon size="13" name="lucide-download" color="#ffffff" class="btn-icon" />
                        {{
                            t('plugin.rolling-memory.episodic.syncActiveHarnessBtn', {
                                name: envHealth.active_harness_label || t('plugin.rolling-memory.overview.unknownStatus'),
                            })
                        }}
                        <template v-if="syncingHarness === envHealth.active_harness_id"> ({{ syncHarnessPct }}%)</template>
                    </el-button>
                    <el-button
                        v-blur
                        type="primary"
                        size="small"
                        plain
                        :loading="syncingHarness === 'all'"
                        data-testid="btn-sync-all-harnesses"
                        @click="handleSyncHarness('all')"
                    >
                        <Icon size="13" name="lucide-refresh-cw" class="btn-icon" />
                        {{ t('plugin.rolling-memory.episodic.syncAllHarnessesBtn') }}
                        <template v-if="syncingHarness === 'all'"> ({{ syncHarnessPct }}%)</template>
                    </el-button>
                    <el-button v-blur size="small" :loading="checkingHealth" data-testid="btn-rescan-harnesses" @click="refreshEnvHealth">
                        {{ t('plugin.rolling-memory.overview.checkHealthBtn') }}
                        <template v-if="checkingHealth && healthProgressPct > 0"> ({{ healthProgressPct }}%)</template>
                    </el-button>
                </div>
            </div>

            <!-- 历史指针索引回填与流式扫描实时进度条及暂停/继续/单步推进控制 -->
            <div v-if="epiIndex.total_docs > 0" class="index-progress-strip" data-testid="episodic-index-progress">
                <div class="ip-top">
                    <div class="ip-title-row">
                        <span class="ip-title">{{ t('plugin.rolling-memory.episodic.indexProgressTitle') }}</span>
                        <el-tag
                            size="small"
                            :type="epiIndex.paused ? 'warning' : epiIndex.running ? 'primary' : 'success'"
                            effect="dark"
                            data-testid="badge-episodic-index-state"
                        >
                            {{
                                epiIndex.paused
                                    ? t('plugin.rolling-memory.episodic.indexStatusPaused')
                                    : epiIndex.running
                                      ? t('plugin.rolling-memory.episodic.indexStatusRunning')
                                      : t('plugin.rolling-memory.episodic.indexStatusReady')
                            }}
                        </el-tag>
                        <span class="ip-counts">
                            {{ t('plugin.rolling-memory.episodic.indexCompatibleLbl') }}:
                            <b>{{ epiIndex.compatible || 0 }} / {{ epiIndex.total_docs || 0 }} ({{ epiIndex.coverage_pct ?? 0 }}%)</b>
                            · {{ t('plugin.rolling-memory.episodic.indexMissingLbl') }}: <b>{{ epiIndex.missing || 0 }}</b>
                        </span>
                    </div>
                    <div class="ip-actions">
                        <el-button
                            v-blur
                            size="small"
                            :type="epiIndex.paused ? 'success' : 'warning'"
                            :loading="togglingPause"
                            data-testid="btn-toggle-episodic-pause"
                            @click="handleTogglePause(!epiIndex.paused)"
                        >
                            {{
                                epiIndex.paused ? t('plugin.rolling-memory.episodic.resumeSyncBtn') : t('plugin.rolling-memory.episodic.pauseSyncBtn')
                            }}
                        </el-button>
                        <el-button
                            v-blur
                            size="small"
                            type="primary"
                            plain
                            :loading="steppingSync"
                            data-testid="btn-step-episodic-sync"
                            @click="handleStepSync"
                        >
                            {{ t('plugin.rolling-memory.episodic.stepSyncBtn') }}
                            <template v-if="steppingSync"> ({{ stepSyncPct }}%)</template>
                        </el-button>
                    </div>
                </div>
                <el-progress
                    :percentage="Math.min(100, Math.max(0, Number(epiIndex.coverage_pct || 0)))"
                    :status="epiIndex.paused ? 'warning' : 'success'"
                    :stroke-width="8"
                />
                <div v-if="epiIndex.backfill_cursor || epiIndex.scan_source" class="ip-cursors">
                    <span v-if="epiIndex.backfill_cursor">
                        {{ t('plugin.rolling-memory.episodic.indexBackfillCursorLbl') }}: <code>{{ epiIndex.backfill_cursor }}</code>
                    </span>
                    <span v-if="epiIndex.scan_source">
                        {{ t('plugin.rolling-memory.episodic.indexScanCursorLbl') }}:
                        <code>{{ epiIndex.scan_source }} #{{ epiIndex.scan_index || 0 }}</code>
                    </span>
                </div>
            </div>

            <div class="harness-cards-grid">
                <div
                    v-for="h in envHealth.harnesses"
                    :key="h.id"
                    class="harness-card"
                    :class="{ 'is-active': h.active, 'is-detected': h.detected }"
                    :data-testid="`episodic-harness-${h.id}`"
                >
                    <div class="hc-top">
                        <div class="hc-name-wrap">
                            <span class="hc-name">{{ h.label }}</span>
                            <el-tag v-if="h.active" size="small" type="success" effect="dark">ACTIVE</el-tag>
                            <el-tag v-else-if="h.detected" size="small" type="info" effect="plain">INSTALLED</el-tag>
                        </div>
                        <el-button
                            v-if="h.detected && h.turns_scan_supported !== false"
                            link
                            type="primary"
                            size="small"
                            :loading="syncingHarness === h.id"
                            :data-testid="`btn-sync-harness-${h.id}`"
                            @click="handleSyncHarness(h.id)"
                        >
                            {{ t('plugin.rolling-memory.episodic.syncThisBtn') }}
                        </el-button>
                    </div>
                    <div class="hc-wiring">
                        <span :class="{ ok: h.skill_wired }">Skill {{ h.skill_wired ? '✓' : '·' }}</span>
                        <span :class="{ ok: h.mcp_wired }">MCP {{ h.mcp_wired ? '✓' : '·' }}</span>
                        <span v-if="h.hooks_supported" :class="{ ok: h.hooks_wired }">Hooks {{ h.hooks_wired ? '✓' : '·' }}</span>
                    </div>
                    <!-- 会话清单与轮次内容是两条独立能力：能发现会话就显示计数，
                         能读内容才显示 Turns，读不到时不把「未统计」写成 0/0。 -->
                    <div class="hc-metrics" v-if="showsSessions(h) || showsTurns(h)">
                        <span v-if="showsSessions(h)"
                            >Sessions: <b>{{ h.sessions_count || 0 }}</b></span
                        >
                        <span v-if="showsTurns(h)"
                            >Turns: <b>{{ h.synced_turns_count || 0 }}/{{ h.local_turns_count || 0 }}</b></span
                        >
                        <span v-if="showsTurns(h) && h.unsynced_turns_count > 0" class="unsynced-pill">
                            +{{ h.unsynced_turns_count }} {{ t('plugin.rolling-memory.overview.pendingSyncLbl') }}
                        </span>
                    </div>
                    <!-- 单胜者的 ACTIVE 只标当前环境：其余宿主用最后活动时间如实呈现活跃度。 -->
                    <div class="hc-last-active" v-if="h.last_active_text && h.last_active_text !== '-'">
                        Last active: <b>{{ h.last_active_text }}</b>
                    </div>
                </div>
            </div>
        </div>

        <!-- 情景记忆单元主表（100% 原生 BuildAdmin TableHeader + Table + ComSearch + 分页） -->
        <layerBrowser ref="browserRef" layer="episodic" :title="t('plugin.rolling-memory.episodic.unitsTitle')" @loaded="onStatsLoaded">
            <template #toolbarAppend>
                <el-switch
                    v-model="envHealth.auto_sync_enabled"
                    :disabled="typeof envHealth.auto_sync_enabled !== 'boolean'"
                    class="btns-ml-12"
                    :active-text="t('plugin.rolling-memory.overview.autoSyncModeLbl')"
                    :inactive-text="t('plugin.rolling-memory.overview.manualSyncModeLbl')"
                    @change="handleToggleAutoSync"
                />
                <el-button
                    v-blur
                    type="success"
                    class="table-header-operate btns-ml-12"
                    :disabled="!envHealth.active_harness_id"
                    :loading="syncingHarness === envHealth.active_harness_id"
                    data-testid="btn-header-sync-active-harness"
                    @click="handleSyncHarness(envHealth.active_harness_id)"
                >
                    <Icon size="14" name="lucide-download" color="#ffffff" />
                    <span class="table-header-operate-text">
                        {{
                            t('plugin.rolling-memory.episodic.syncActiveHarnessBtn', {
                                name: envHealth.active_harness_label || t('plugin.rolling-memory.overview.unknownStatus'),
                            })
                        }}
                        <template v-if="epiAlign.harness_unsynced_turns > 0"> (+{{ epiAlign.harness_unsynced_turns }})</template>
                    </span>
                </el-button>
                <el-button
                    v-blur
                    type="primary"
                    plain
                    class="table-header-operate btns-ml-12"
                    :loading="syncingHarness === 'all'"
                    @click="handleSyncHarness('all')"
                >
                    <Icon size="14" name="lucide-refresh-cw" />
                    <span class="table-header-operate-text">{{ t('plugin.rolling-memory.episodic.syncAllHarnessesBtn') }}</span>
                </el-button>
            </template>
        </layerBrowser>
    </div>
</template>

<script setup lang="ts">
import { useAdminInfo } from '@/stores/adminInfo'
import { useLoadingProgressText } from '@/utils/loadingProgress'
import { MonitorSocket } from '@/utils/monitorSocket'
import request from '@/utils/request'
import { ElMessage } from 'element-plus'
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import layerBrowser from './layerBrowser.vue'

defineOptions({
    name: 'plugin/rolling-memory/episodic',
})

const { t } = useI18n()
const adminInfo = useAdminInfo()
const stats = ref<any>({})
const checkingHealth = ref(false)
const syncingHarness = ref('')
const togglingPause = ref(false)
const steppingSync = ref(false)
const { percent: syncHarnessPct } = useLoadingProgressText(() => Boolean(syncingHarness.value), undefined, 1200)
const { percent: stepSyncPct } = useLoadingProgressText(steppingSync, undefined, 900)
const browserRef = ref<any>(null)
let envSocket: MonitorSocket | null = null
const healthProgressPct = ref(0)
const healthLoadingText = computed(() =>
    healthProgressPct.value > 0
        ? `${t('plugin.rolling-memory.overview.progressTitle')} (${healthProgressPct.value}%)`
        : `${t('plugin.rolling-memory.overview.progressTitle')}...`
)

const envHealth = ref<any>({ harnesses: [], layers: {}, episodic_index: {} })
// 会话清单与轮次内容是两个独立能力：DSH 的会话能被发现（计数如实），
// 但其 zstd 转录本构建尚不解析 ⇒ 只显示会话数，不显示 0/0 的 Turns。
const showsSessions = (h: any) => (h.sessions_scan_supported ?? h.turns_scan_supported ?? true) !== false
const showsTurns = (h: any) => h.turns_scan_supported !== false
const epiIndex = computed(() => envHealth.value?.episodic_index || {})

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

const epiAlign = computed(() => envHealth.value?.layers?.episodic || {})
const isLocalMode = computed(() => envHealth.value?.mode !== 'cloud')
const alignmentKnown = computed(() =>
    Boolean(envHealth.value?.layers?.episodic && (isLocalMode.value || (envHealth.value?.mode === 'cloud' && envHealth.value?.cloud_online === true)))
)

const onStatsLoaded = (payload: any) => {
    if (payload) {
        stats.value = payload
    }
}

const loadEnvHealth = async () => {
    checkingHealth.value = true
    try {
        const res = await request<any>(
            { url: '/admin/plugin/rolling-memory/env/health', method: 'POST', data: {}, timeout: 0 },
            { cancelDuplicate: false, showErrorMessage: false }
        )
        envHealth.value = res.data?.data || { harnesses: [], layers: {} }
    } catch {
        envHealth.value = { harnesses: [], layers: {} }
    } finally {
        checkingHealth.value = false
    }
}

// 「重新检测」与动作后刷新：页面已订阅实时流，强制采集排入共享流——引擎进度帧与最终数据帧
// 走同一条 SSE（等待态由数据帧或进度收尾帧清除）；端点不可用（旧服务端）时回退一次性取值。
const refreshEnvHealth = async () => {
    if (checkingHealth.value) return
    checkingHealth.value = true
    try {
        const res = await request<any>(
            { url: '/admin/plugin/rolling-memory/env/health/refresh', method: 'POST', data: {}, timeout: 0 },
            { cancelDuplicate: false, showErrorMessage: false }
        )
        const data = res.data?.data
        if (data && (data.harnesses || data.layers)) {
            envHealth.value = data
            checkingHealth.value = false
        }
    } catch {
        await loadEnvHealth()
    }
}

// 环境与记忆健康实时流：走同源 MonitorSocket WebSocket 共享房间（rm/env_health）。
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
                if (d?.stage === 'vault') healthProgressPct.value = Math.max(healthProgressPct.value, 18)
                else if (d?.stage === 'wiring') healthProgressPct.value = Math.max(healthProgressPct.value, 32)
                else if (d?.stage === 'scan') {
                    let ratio = 0.25
                    if (typeof d?.turns_cap === 'number' && d.turns_cap > 0) {
                        ratio = Math.max(ratio, Math.min(1, (d.turns || 0) / d.turns_cap))
                    }
                    if (typeof d?.sessions_total === 'number' && d.sessions_total > 0) {
                        ratio = Math.max(ratio, Math.min(1, (d.sessions_done || 0) / d.sessions_total))
                    }
                    healthProgressPct.value = Math.max(healthProgressPct.value, Math.round(35 + ratio * 50))
                } else if (d?.stage === 'index') {
                    healthProgressPct.value = Math.max(healthProgressPct.value, 94)
                }
                if (d && d.running === false) {
                    checkingHealth.value = false
                    healthProgressPct.value = 0
                    if (d.error) {
                        ElMessage.warning(t('plugin.rolling-memory.overview.refreshFailed'))
                    }
                }
                return
            }
            if (env.event !== 'env_health') return
            if (d && (d.harnesses || d.layers)) {
                envHealth.value = d
            }
            checkingHealth.value = false
            healthProgressPct.value = 0
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

const handleSyncHarness = async (harnessId: string) => {
    if (syncingHarness.value) return
    syncingHarness.value = harnessId
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/sync/reconcile',
            method: 'POST',
            data: { layer: 'episodic', harness: harnessId, limit: 80 },
            timeout: 0,
        })
        const rep = res.data?.data
        if (rep) {
            const summary = isLocalMode.value
                ? t('plugin.rolling-memory.episodic.reconcileSuccessLocal', {
                      har: rep.episodic_imported_from_harness ?? 0,
                  })
                : t('plugin.rolling-memory.episodic.reconcileResult', {
                      har: rep.episodic_imported_from_harness ?? 0,
                      epi: rep.episodic_pushed_to_remote ?? 0,
                  })
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
        syncingHarness.value = ''
    }
}

const handleTogglePause = async (pause: boolean) => {
    if (togglingPause.value) return
    togglingPause.value = true
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
            await loadEnvHealth()
        }
        ElMessage.success(pause ? t('plugin.rolling-memory.episodic.syncPausedToast') : t('plugin.rolling-memory.episodic.syncResumedToast'))
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        togglingPause.value = false
    }
}

const handleStepSync = async () => {
    if (steppingSync.value) return
    steppingSync.value = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/sync/reconcile',
            method: 'POST',
            data: { action: 'step' },
            timeout: 0,
        })
        const data = res.data?.data
        if (data?.episodic_index) {
            envHealth.value.episodic_index = data.episodic_index
        } else {
            await loadEnvHealth()
        }
        ElMessage.success(
            t('plugin.rolling-memory.episodic.syncSteppedToast', {
                indexed: data?.backfill?.Indexed ?? 0,
                scanned: data?.backfill?.Scanned ?? 0,
            })
        )
        browserRef.value?.reload()
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        steppingSync.value = false
    }
}

onMounted(() => {
    // 首屏只订阅实时流：服务端共享 watcher 在指纹变化时推帧，首帧由「按当前 Harness 过滤」的轻量采集产出。
    // 全量诊断（跨全部 Harness 的完整扫描）改由面板的「重新检测」按钮与同步动作后的显式刷新承担，
    // 不在挂载路径上默认触发。
    checkingHealth.value = true
    startEnvHealthStream()
})

let epDeactivated = false
onActivated(() => {
    if (!epDeactivated) return
    epDeactivated = false
    startEnvHealthStream()
})

onDeactivated(() => {
    epDeactivated = true
    stopEnvHealthStream()
})

onBeforeUnmount(() => {
    stopEnvHealthStream()
})
</script>

<style scoped lang="scss">
.rm-alert {
    margin-bottom: 10px;
}
.rm-stats-bar {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 10px;
    margin-bottom: 10px;
}
@media (max-width: 960px) {
    .rm-stats-bar {
        grid-template-columns: repeat(2, minmax(0, 1fr));
    }
}
.stat-item {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 10px 14px;
    border: 1px solid var(--el-border-color-lighter);
    border-left: 3px solid var(--el-color-primary);
    border-radius: 6px;
    background: var(--ag-bg-color-overlay);
    &.success {
        border-left-color: var(--el-color-success);
    }
    &.warning {
        border-left-color: var(--el-color-warning);
    }
    &.info {
        border-left-color: var(--el-color-info);
    }
}
.stat-label {
    font-size: 12px;
    color: var(--el-text-color-secondary);
}
.stat-value {
    font-size: 16px;
    font-weight: 700;
    color: var(--el-text-color-primary);
}
.harness-sync-box {
    background-color: var(--ag-bg-color-overlay);
    border: 1px solid var(--ag-border-color);
    border-radius: var(--el-border-radius-base);
    padding: 12px 14px;
    margin-bottom: 10px;
}
.harness-sync-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    flex-wrap: wrap;
    gap: 10px;
    margin-bottom: 10px;
}
.harness-sync-title {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);
}
.ws-code {
    font-size: 11px;
    padding: 2px 6px;
    border-radius: 4px;
    background: var(--el-fill-color-light);
    color: var(--el-text-color-secondary);
}
.harness-sync-actions {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
}
.harness-cards-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 8px;
}
.harness-card {
    padding: 9px 12px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 6px;
    background: var(--el-fill-color-blank);
    transition: border-color 0.2s;
    &.is-active {
        border-color: var(--el-color-success);
        background: rgba(16, 185, 129, 0.04);
    }
    &:not(.is-detected) {
        opacity: 0.55;
    }
}
.hc-top {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 5px;
}
.hc-name-wrap {
    display: flex;
    align-items: center;
    gap: 6px;
}
.hc-name {
    font-size: 13px;
    font-weight: 600;
}
.hc-wiring {
    display: flex;
    gap: 10px;
    font-size: 11px;
    color: var(--el-text-color-secondary);
    margin-bottom: 5px;
    .ok {
        color: var(--el-color-success);
        font-weight: 600;
    }
}
.hc-metrics {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    font-size: 11px;
    color: var(--el-text-color-secondary);
}
.unsynced-pill {
    color: var(--el-color-warning);
    font-weight: 600;
}
.hc-last-active {
    margin-top: 3px;
    font-size: 10px;
    color: var(--el-text-color-placeholder);
}
.btn-icon {
    margin-right: 4px;
}
.btns-ml-12 {
    margin-left: 12px;
}
.table-header-operate-text {
    margin-left: 6px;
}
.index-progress-strip {
    margin-bottom: 10px;
    padding: 10px 12px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 6px;
    background: var(--el-fill-color-blank);
    display: flex;
    flex-direction: column;
    gap: 8px;
}
.ip-top {
    display: flex;
    justify-content: space-between;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
}
.ip-title-row {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    font-size: 12px;
}
.ip-title {
    font-weight: 600;
    color: var(--el-text-color-primary);
}
.ip-counts {
    color: var(--el-text-color-secondary);
}
.ip-actions {
    display: flex;
    align-items: center;
    gap: 6px;
}
.ip-cursors {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    font-size: 11px;
    color: var(--el-text-color-secondary);
}
</style>
