<!-- plugin: rolling-memory v0.0.58 -->
<template>
    <div class="default-main plugin-rolling-memory">
        <!-- 顶部提示条（参照 plugins/ai/web/views/channel/index.vue 标准） -->
        <el-alert
            class="rm-alert"
            type="warning"
            :closable="false"
            show-icon
            :title="t('plugin.rolling-memory.noetic.pageTitle')"
            :description="t('plugin.rolling-memory.noetic.pageSub')"
        />

        <!-- 4 维统计指标条 -->
        <div class="rm-stats-bar">
            <div class="stat-item warning">
                <span class="stat-label">{{ t('plugin.rolling-memory.browser.statusActive') }}</span>
                <span class="stat-value">{{ stats.noetic_active_count || 0 }}</span>
            </div>
            <div class="stat-item info">
                <span class="stat-label">{{ t('plugin.rolling-memory.browser.statusStale') }}</span>
                <span class="stat-value">{{ stats.noetic_stale_count || 0 }}</span>
            </div>
            <div class="stat-item primary">
                <span class="stat-label">{{ t('plugin.rolling-memory.noetic.citedLabel') }}</span>
                <span class="stat-value">{{ stats.noetic_cited_links || 0 }}</span>
            </div>
            <div class="stat-item" :class="!alignmentKnown ? 'info' : noeAlign.aligned ? 'success' : 'warning'">
                <span class="stat-label">{{
                    isLocalMode
                        ? t('plugin.rolling-memory.overview.localVaultLbl')
                        : `${t('plugin.rolling-memory.overview.localVaultLbl')} ⇄ ${t('plugin.rolling-memory.overview.remoteCloudLbl')}`
                }}</span>
                <span v-if="isLocalMode" class="stat-value">{{ noeAlign.local_vault_count ?? stats.noetic_active_count ?? 0 }}</span>
                <span v-else-if="!alignmentKnown" class="stat-value">{{ t('plugin.rolling-memory.overview.unknownStatus') }}</span>
                <span v-else class="stat-value"
                    >{{ noeAlign.local_vault_count }} / {{ noeAlign.remote_cloud_count }} ({{ noeAlign.alignment_pct }}%)</span
                >
            </div>
        </div>

        <!-- 认知记忆结晶主表（100% 原生 BuildAdmin TableHeader + Table + ComSearch + 分页） -->
        <layerBrowser
            ref="browserRef"
            layer="noetic"
            :show-status="true"
            :title="t('plugin.rolling-memory.noetic.unitsTitle')"
            @loaded="onStatsLoaded"
        >
            <template #toolbarAppend>
                <el-button
                    v-blur
                    type="warning"
                    class="table-header-operate btns-ml-12"
                    :loading="reconciling"
                    data-testid="btn-reconcile-noetic"
                    @click="handleReconcileNoetic"
                >
                    <Icon size="14" name="lucide-refresh-cw" color="#ffffff" />
                    <span class="table-header-operate-text">
                        {{ t('plugin.rolling-memory.overview.reconcileLayerBtn') }}
                        <template v-if="reconciling"> ({{ noeticReconcilePct }}%)</template>
                        <template v-else-if="noeAlign.total_pending_sync > 0"> ({{ noeAlign.total_pending_sync }})</template>
                    </span>
                </el-button>
                <el-popconfirm
                    v-if="(stats.noetic_stale_count || 0) > 0"
                    :title="t('plugin.rolling-memory.noetic.purgeStaleConfirm', { n: stats.noetic_stale_count || 0 })"
                    :confirm-button-text="t('common.delete')"
                    :cancel-button-text="t('common.cancel')"
                    confirm-button-type="danger"
                    @confirm="handlePurgeStale"
                >
                    <template #reference>
                        <div class="btns-ml-12">
                            <el-button
                                v-blur
                                type="danger"
                                plain
                                class="table-header-operate"
                                :loading="purging"
                                data-testid="btn-purge-stale-noetic"
                            >
                                <Icon size="14" name="lucide-eraser" />
                                <span class="table-header-operate-text">
                                    {{ t('plugin.rolling-memory.noetic.purgeStaleBtn', { n: stats.noetic_stale_count || 0 }) }}
                                    <template v-if="purging"> ({{ purgePct }}%)</template>
                                </span>
                            </el-button>
                        </div>
                    </template>
                </el-popconfirm>
                <el-button
                    v-blur
                    class="table-header-operate btns-ml-12"
                    data-testid="btn-go-recall-from-noetic"
                    @click="router.push({ path: '/admin/plugin/rolling-memory/recall' })"
                >
                    <Icon size="14" name="lucide-search" />
                    <span class="table-header-operate-text">{{ t('plugin.rolling-memory.noetic.goRecallBtn') }}</span>
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
import { useRouter } from 'vue-router'
import layerBrowser from './layerBrowser.vue'

defineOptions({
    name: 'plugin/rolling-memory/noetic',
})

const { t } = useI18n()
const router = useRouter()
const adminInfo = useAdminInfo()
const stats = ref<any>({})
const envHealth = ref<any>({})
const reconciling = ref(false)
const purging = ref(false)
const { percent: noeticReconcilePct } = useLoadingProgressText(reconciling, undefined, 900)
const { percent: purgePct } = useLoadingProgressText(purging, undefined, 800)
const browserRef = ref<any>(null)
let envSocket: MonitorSocket | null = null

const noeAlign = computed(() => envHealth.value?.layers?.noetic || {})
const isLocalMode = computed(() => envHealth.value?.mode !== 'cloud')
const alignmentKnown = computed(() =>
    Boolean(envHealth.value?.layers?.noetic && (isLocalMode.value || (envHealth.value?.mode === 'cloud' && envHealth.value?.cloud_online === true)))
)

const onStatsLoaded = (payload: any) => {
    if (payload) {
        stats.value = payload
    }
}

const loadEnvHealth = async () => {
    try {
        const res = await request<any>(
            { url: '/admin/plugin/rolling-memory/env/health', method: 'POST', data: {}, timeout: 0 },
            { cancelDuplicate: false, showErrorMessage: false }
        )
        envHealth.value = res.data?.data || {}
    } catch {
        envHealth.value = {}
    }
}

// 动作后刷新：页面已订阅实时流，强制采集排入共享流（数据帧走同一条 SSE）；
// 端点不可用（旧服务端）或流未连时回退一次性取值。
const refreshEnvHealth = async () => {
    try {
        const res = await request<any>(
            { url: '/admin/plugin/rolling-memory/env/health/refresh', method: 'POST', data: {}, timeout: 0 },
            { cancelDuplicate: false, showErrorMessage: false }
        )
        const data = res.data?.data
        if (data && (data.harnesses || data.layers)) {
            envHealth.value = data
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
    envSocket = new MonitorSocket({
        token,
        room: 'rm/env_health',
        onMessage: (env: any) => {
            if (env?.event !== 'env_health' || !env.data) return
            const d = env.data
            if (d && (d.harnesses || d.layers)) {
                envHealth.value = d
            }
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

const handleReconcileNoetic = async () => {
    if (reconciling.value) return
    reconciling.value = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/sync/reconcile',
            method: 'POST',
            data: { layer: 'noetic', limit: 80 },
            timeout: 0,
        })
        const rep = res.data?.data
        if (rep) {
            const summary = isLocalMode.value
                ? t('plugin.rolling-memory.noetic.reconcileSuccessLocal')
                : t('plugin.rolling-memory.noetic.reconcileResult', { noe: rep.noetic_pushed_to_remote ?? 0 })
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
        reconciling.value = false
    }
}

const handlePurgeStale = async () => {
    if (purging.value) return
    purging.value = true
    try {
        await request<any>({
            url: '/admin/plugin/rolling-memory/layers/delete',
            method: 'POST',
            data: { purge_stale: true },
        })
        ElMessage.success(t('plugin.rolling-memory.noetic.purgeStaleSuccess'))
        browserRef.value?.reload()
        refreshEnvHealth()
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        purging.value = false
    }
}

onMounted(() => {
    // 首屏只订阅实时流：服务端共享 watcher 推帧即达；显式全量诊断保留给动作后的 loadEnvHealth。
    startEnvHealthStream()
})

let noeticDeactivated = false
onActivated(() => {
    if (!noeticDeactivated) return
    noeticDeactivated = false
    startEnvHealthStream()
})

onDeactivated(() => {
    noeticDeactivated = true
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
.btns-ml-12 {
    margin-left: 12px;
}
.table-header-operate-text {
    margin-left: 6px;
}
</style>
