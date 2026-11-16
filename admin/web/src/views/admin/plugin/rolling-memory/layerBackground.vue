<!-- plugin: rolling-memory v0.0.58 -->
<template>
    <div class="default-main plugin-rolling-memory">
        <!-- 顶部提示条（参照 plugins/ai/web/views/channel/index.vue 标准） -->
        <el-alert
            class="rm-alert"
            type="info"
            :closable="false"
            show-icon
            :title="t('plugin.rolling-memory.background.pageTitle')"
            :description="`${t('plugin.rolling-memory.background.pageSub')} · ${t('plugin.rolling-memory.background.nativeHint')}`"
        />

        <!-- 4 维统计指标条 -->
        <div class="rm-stats-bar">
            <div class="stat-item primary">
                <span class="stat-label">{{ t('plugin.rolling-memory.background.dirsLabel') }}</span>
                <span class="stat-value">{{ dirs.length }}</span>
            </div>
            <div class="stat-item info">
                <span class="stat-label">{{ t('plugin.rolling-memory.background.filesLabel') }}</span>
                <span class="stat-value">{{ stats.background_file_count || files.length }}</span>
            </div>
            <div class="stat-item success">
                <span class="stat-label">{{ t('plugin.rolling-memory.background.chunksLabel') }}</span>
                <span class="stat-value">{{ stats.background_count || 0 }}</span>
            </div>
            <div class="stat-item" :class="!alignmentKnown ? 'info' : bgAlign.aligned ? 'success' : 'warning'">
                <span class="stat-label">{{
                    isLocalMode
                        ? t('plugin.rolling-memory.overview.localVaultLbl')
                        : `${t('plugin.rolling-memory.overview.localVaultLbl')} ⇄ ${t('plugin.rolling-memory.overview.remoteCloudLbl')}`
                }}</span>
                <span v-if="isLocalMode" class="stat-value">{{ bgAlign.local_vault_count ?? stats.background_count ?? 0 }}</span>
                <span v-else-if="!alignmentKnown" class="stat-value">{{ t('plugin.rolling-memory.overview.unknownStatus') }}</span>
                <span v-else class="stat-value"
                    >{{ bgAlign.local_vault_count }} / {{ bgAlign.remote_cloud_count }} ({{ bgAlign.alignment_pct }}%)</span
                >
            </div>
        </div>

        <!-- 挂载目录 ⇄ 目录内文件 双栏联动面板 -->
        <el-row :gutter="12" class="mb-10">
            <el-col :xs="24" :md="11">
                <div class="sub-table-box">
                    <div class="sub-table-header">
                        <div class="sub-table-title">
                            <Icon name="lucide-folder-tree" :size="15" />
                            <span>{{ t('plugin.rolling-memory.background.mountTitle') }}</span>
                        </div>
                        <div class="sub-table-actions">
                            <el-button
                                v-blur
                                type="primary"
                                size="small"
                                :loading="pickingDir || mountingDir"
                                data-testid="btn-add-bg-dir"
                                @click="handleAddDir"
                            >
                                <Icon v-if="!pickingDir && !mountingDir" name="lucide-folder-open" :size="13" class="btn-icon" />
                                {{
                                    pickingDir
                                        ? t('plugin.rolling-memory.background.waitingForPicker')
                                        : mountingDir
                                          ? `${t('plugin.rolling-memory.background.mounting')} (${mountDirPct}%)`
                                          : t('plugin.rolling-memory.background.selectAndMount')
                                }}
                            </el-button>
                            <el-button
                                v-blur
                                size="small"
                                :disabled="pickingDir || mountingDir"
                                data-testid="btn-manual-bg-dir"
                                @click="promptManualDirectory()"
                            >
                                <Icon name="lucide-pen-line" :size="13" class="btn-icon" />
                                {{ t('plugin.rolling-memory.background.manualBtn') }}
                            </el-button>
                        </div>
                    </div>
                    <div class="sub-table-hint">
                        {{ t('plugin.rolling-memory.background.formatPrefix') }}
                        <code>.docx</code> / <code>.xlsx</code> / <code>.pptx</code> / <code>.pdf</code> / <code>.md</code> / <code>.txt</code> /
                        <code>.json</code> / <code>.csv</code> /
                        {{ t('plugin.rolling-memory.background.formatSuffix') }}
                    </div>
                    <el-table
                        :data="dirs"
                        size="small"
                        border
                        stripe
                        class="ag-data-table"
                        header-cell-class-name="table-header-cell"
                        v-loading="loadingDirs || mountingDir"
                        :element-loading-text="dirsLoadingText"
                        :empty-text="t('plugin.rolling-memory.background.empty')"
                        max-height="220"
                        highlight-current-row
                        @row-click="selectDir"
                    >
                        <el-table-column
                            prop="root_path"
                            :label="t('plugin.rolling-memory.background.colPath')"
                            min-width="200"
                            show-overflow-tooltip
                        />
                        <el-table-column prop="file_count" :label="t('plugin.rolling-memory.background.colFileCount')" width="72" align="center" />
                        <el-table-column prop="chunk_count" :label="t('plugin.rolling-memory.background.colChunkCount')" width="72" align="center" />
                        <el-table-column :label="t('plugin.rolling-memory.background.colAction')" width="118" align="center">
                            <template #default="{ row }">
                                <el-button link type="primary" size="small" data-testid="btn-browse-bg-files" @click.stop="selectDir(row)">
                                    {{ t('plugin.rolling-memory.background.browseBtn') }}
                                </el-button>
                                <el-button link type="danger" size="small" data-testid="btn-remove-bg-dir" @click.stop="handleRemoveDir(row)">
                                    {{ t('plugin.rolling-memory.background.removeBtn') }}
                                </el-button>
                            </template>
                        </el-table-column>
                    </el-table>
                </div>
            </el-col>

            <el-col :xs="24" :md="13">
                <div class="sub-table-box">
                    <div class="sub-table-header">
                        <div class="sub-table-title">
                            <Icon name="lucide-file-stack" :size="15" />
                            <span>{{ t('plugin.rolling-memory.background.filesTitle') }}</span>
                            <el-tag
                                v-if="selectedDir"
                                size="small"
                                type="primary"
                                closable
                                data-testid="btn-clear-dir-filter"
                                @close="selectDir(null)"
                            >
                                {{ selectedDir.root_path }}
                            </el-tag>
                        </div>
                        <div class="sub-table-actions">
                            <el-input
                                v-model="fileKeyword"
                                size="small"
                                clearable
                                :placeholder="t('plugin.rolling-memory.filters.searchPlaceholder')"
                                style="width: 170px"
                                @input="onFileKeywordInput"
                                @clear="loadFiles"
                                @keyup.enter="loadFiles"
                            />
                        </div>
                    </div>
                    <div class="sub-table-hint">
                        {{ t('plugin.rolling-memory.background.manualHint') }}
                    </div>
                    <el-table
                        :data="pagedFiles"
                        size="small"
                        border
                        stripe
                        class="ag-data-table"
                        header-cell-class-name="table-header-cell"
                        v-loading="loadingFiles"
                        :element-loading-text="filesLoadingText"
                        :empty-text="t('plugin.rolling-memory.background.filesEmpty')"
                        max-height="220"
                        highlight-current-row
                        @row-click="focusFileChunks"
                    >
                        <el-table-column
                            prop="rel_path"
                            :label="t('plugin.rolling-memory.background.colRelPath')"
                            min-width="200"
                            show-overflow-tooltip
                        />
                        <el-table-column prop="size_text" :label="t('plugin.rolling-memory.background.colSize')" width="86" align="center" />
                        <el-table-column prop="chunk_count" :label="t('plugin.rolling-memory.background.colChunkCount')" width="76" align="center" />
                        <el-table-column prop="updated_text" :label="t('plugin.rolling-memory.background.colUpdatedAt')" width="148" align="center" />
                        <el-table-column :label="t('plugin.rolling-memory.background.colAction')" width="90" align="center">
                            <template #default="{ row }">
                                <el-button link type="primary" size="small" data-testid="btn-browse-bg-chunks" @click.stop="focusFileChunks(row)">
                                    {{ t('plugin.rolling-memory.background.chunksBtn') }}
                                </el-button>
                            </template>
                        </el-table-column>
                    </el-table>
                    <div v-if="files.length > filePageSize" class="files-pager">
                        <el-pagination
                            v-model:current-page="filePage"
                            :page-size="filePageSize"
                            :total="files.length"
                            layout="total, prev, pager, next"
                            size="small"
                        />
                    </div>
                </div>
            </el-col>
        </el-row>

        <!-- 背景分块/单元主表（100% 原生 BuildAdmin TableHeader + Table + ComSearch + 分页） -->
        <layerBrowser
            ref="browserRef"
            layer="background"
            :dir-id="selectedDirId"
            :rel-path="selectedRelPath"
            :title="t('plugin.rolling-memory.background.unitsTitle')"
            @loaded="onStatsLoaded"
        >
            <template #toolbarAppend>
                <el-switch
                    v-model="envHealth.auto_sync_enabled"
                    :disabled="typeof envHealth.auto_sync_enabled !== 'boolean'"
                    class="btns-ml-12"
                    :active-text="t('plugin.rolling-memory.overview.autoSyncModeLbl')"
                    :inactive-text="t('plugin.rolling-memory.overview.manualSyncModeLbl')"
                    @change="handleToggleAutoSync"
                />
                <el-button v-blur type="primary" class="table-header-operate btns-ml-12" :loading="pickingDir || mountingDir" @click="handleAddDir">
                    <Icon size="14" name="lucide-folder-plus" color="#ffffff" />
                    <span class="table-header-operate-text">
                        {{ t('plugin.rolling-memory.background.selectAndMount') }}
                        <template v-if="mountingDir"> ({{ mountDirPct }}%)</template>
                    </span>
                </el-button>
                <el-button
                    v-blur
                    type="success"
                    class="table-header-operate btns-ml-12"
                    :loading="syncing"
                    data-testid="btn-sync-all"
                    @click="handleSync"
                >
                    <Icon size="14" name="lucide-refresh-cw" color="#ffffff" />
                    <span class="table-header-operate-text">
                        {{ t('plugin.rolling-memory.background.syncBtn') }}
                        <template v-if="syncing"> ({{ bgSyncPct }}%)</template>
                    </span>
                </el-button>
                <el-button
                    v-blur
                    type="warning"
                    plain
                    class="table-header-operate btns-ml-12"
                    :loading="reconciling"
                    data-testid="btn-reconcile-bg"
                    @click="handleReconcileBg"
                >
                    <Icon size="14" name="lucide-refresh-cw" />
                    <span class="table-header-operate-text">
                        {{ t('plugin.rolling-memory.overview.reconcileLayerBtn') }}
                        <template v-if="reconciling"> ({{ bgReconcilePct }}%)</template>
                        <template v-else-if="bgAlign.total_pending_sync > 0"> ({{ bgAlign.total_pending_sync }})</template>
                    </span>
                </el-button>
            </template>

            <template #searchPrepend>
                <el-tag v-if="selectedRelPath" size="small" type="primary" closable class="filter-tag" @close="selectedRelPath = ''">
                    {{ t('plugin.rolling-memory.background.colRelPath') }}: {{ selectedRelPath }}
                </el-tag>
            </template>
        </layerBrowser>
    </div>
</template>

<script setup lang="ts">
import { useAdminInfo } from '@/stores/adminInfo'
import { useLoadingProgressText } from '@/utils/loadingProgress'
import { MonitorSocket } from '@/utils/monitorSocket'
import request from '@/utils/request'
import { ElMessage, ElMessageBox } from 'element-plus'
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import layerBrowser from './layerBrowser.vue'

defineOptions({
    name: 'plugin/rolling-memory/background',
})

const { t } = useI18n()
const adminInfo = useAdminInfo()
let envSocket: MonitorSocket | null = null

const loadingDirs = ref(false)
const loadingFiles = ref(false)
const syncing = ref(false)
const reconciling = ref(false)
const pickingDir = ref(false)
const mountingDir = ref(false)
const { text: dirsLoadingText } = useLoadingProgressText(
    () => loadingDirs.value || mountingDir.value,
    () => (mountingDir.value ? t('plugin.rolling-memory.background.mounting') : undefined)
)
const { text: filesLoadingText } = useLoadingProgressText(loadingFiles)
const { percent: mountDirPct } = useLoadingProgressText(mountingDir, undefined, 1200)
const { percent: bgSyncPct } = useLoadingProgressText(syncing, undefined, 1200)
const { percent: bgReconcilePct } = useLoadingProgressText(reconciling, undefined, 1200)
const dirs = ref<any[]>([])
const files = ref<any[]>([])
const filePage = ref(1)
const filePageSize = 20
const pagedFiles = computed(() => {
    const start = (filePage.value - 1) * filePageSize
    return files.value.slice(start, start + filePageSize)
})
const fileKeyword = ref('')
const selectedDir = ref<any>(null)
const selectedRelPath = ref<string>('')
const stats = ref<any>({})
const envHealth = ref<any>({})
const browserRef = ref<any>(null)

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

const selectedDirId = computed(() => selectedDir.value?.dir_id || '')

const bgAlign = computed(() => envHealth.value?.layers?.background || {})
const isLocalMode = computed(() => envHealth.value?.mode !== 'cloud')
const alignmentKnown = computed(() =>
    Boolean(
        envHealth.value?.layers?.background && (isLocalMode.value || (envHealth.value?.mode === 'cloud' && envHealth.value?.cloud_online === true))
    )
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

const loadDirs = async () => {
    loadingDirs.value = true
    try {
        const res = await request<any>({ url: '/admin/plugin/rolling-memory/background/dirs', method: 'POST', data: {} })
        dirs.value = res.data?.data || []
        if (selectedDir.value) {
            const still = dirs.value.find((d) => d.dir_id === selectedDir.value.dir_id)
            selectedDir.value = still || null
        }
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        loadingDirs.value = false
    }
}

let fileKeywordTimer: ReturnType<typeof setTimeout> | null = null
const onFileKeywordInput = () => {
    if (fileKeywordTimer) clearTimeout(fileKeywordTimer)
    fileKeywordTimer = setTimeout(() => {
        loadFiles()
    }, 280)
}

const loadFiles = async () => {
    loadingFiles.value = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/background/files',
            method: 'POST',
            data: { dir_id: selectedDirId.value, keyword: fileKeyword.value, limit: 300 },
        })
        files.value = res.data?.data || []
        filePage.value = 1
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        loadingFiles.value = false
    }
}

const selectDir = (row: any | null) => {
    selectedDir.value = row
    selectedRelPath.value = ''
    loadFiles()
}

const focusFileChunks = (row: any) => {
    if (!row?.rel_path) return
    selectedRelPath.value = row.rel_path
}

const reloadAll = () => {
    loadDirs()
    loadFiles()
    refreshEnvHealth()
    browserRef.value?.reload()
}

const mountDirectory = async (path: string) => {
    if (!path.trim() || mountingDir.value) return
    mountingDir.value = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/background/add',
            method: 'POST',
            data: { path: path.trim() },
            timeout: 0,
        })
        if (res.data?.code === 0) {
            ElMessage.success(t('plugin.rolling-memory.background.mountSuccess'))
            if (res.data?.data?.notice) {
                ElMessage.info(res.data.data.notice)
            }
            reloadAll()
        } else {
            ElMessage.error(res.data?.message || t('plugin.rolling-memory.background.mountFailed'))
        }
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        mountingDir.value = false
    }
}

const promptManualDirectory = async (detail = '') => {
    const fallback = t('plugin.rolling-memory.background.manualFallback')
    if (detail) {
        ElMessage.warning({
            message: `${fallback}: ${detail}`,
            duration: 6000,
            showClose: true,
        })
    }
    try {
        const { value } = await ElMessageBox.prompt(
            t('plugin.rolling-memory.background.manualPrompt'),
            t('plugin.rolling-memory.background.manualTitle'),
            {
                confirmButtonText: t('plugin.rolling-memory.background.mountAndSync'),
                cancelButtonText: t('common.cancel'),
                inputPlaceholder: t('plugin.rolling-memory.background.manualPlaceholder'),
            }
        )
        if (value?.trim()) await mountDirectory(value.trim())
    } catch {
        // 取消手输与取消系统目录选择一样，不产生错误。
    }
}

const handleAddDir = async () => {
    if (pickingDir.value || mountingDir.value) return
    pickingDir.value = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/background/select',
            method: 'POST',
            data: {},
            timeout: 0,
        })
        const selection = res.data?.data
        switch (selection?.status) {
            case 'selected':
                pickingDir.value = false
                await mountDirectory(selection.path || '')
                break
            case 'cancelled':
                ElMessage.info(t('plugin.rolling-memory.background.cancelled'))
                break
            case 'busy':
                ElMessage.warning(t('plugin.rolling-memory.background.pickerBusy'))
                break
            default:
                pickingDir.value = false
                await promptManualDirectory(selection?.detail || '')
                break
        }
    } catch {
        // 鉴权、业务与网络错误由请求拦截器统一提示。
    } finally {
        pickingDir.value = false
    }
}

const handleRemoveDir = async (row: any) => {
    try {
        await ElMessageBox.confirm(
            t('plugin.rolling-memory.background.removeConfirm', { path: row.root_path }),
            t('plugin.rolling-memory.background.removeConfirmTitle'),
            {
                type: 'warning',
                confirmButtonText: t('plugin.rolling-memory.background.removeBtn'),
                cancelButtonText: t('common.cancel'),
            }
        )
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/background/remove',
            method: 'POST',
            data: { dir_id: row.dir_id, path: row.root_path },
            timeout: 0,
        })
        ElMessage.success(t('plugin.rolling-memory.background.removeSuccess'))
        const keptFiles = Number(res.data?.data?.kept_files || 0)
        if (keptFiles > 0) {
            ElMessage.info(t('plugin.rolling-memory.background.removeKeptFiles', { count: keptFiles }))
        }
        reloadAll()
    } catch {}
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
        reloadAll()
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        syncing.value = false
    }
}

const handleReconcileBg = async () => {
    if (reconciling.value) return
    reconciling.value = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/sync/reconcile',
            method: 'POST',
            data: { layer: 'background', limit: 100 },
            timeout: 0,
        })
        const rep = res.data?.data
        if (rep) {
            const summary = isLocalMode.value
                ? t('plugin.rolling-memory.background.reconcileSuccessLocal', {
                      bg: rep.background_synced_files ?? 0,
                  })
                : t('plugin.rolling-memory.background.reconcileResult', {
                      files: rep.background_synced_files ?? 0,
                      chunks: rep.background_pushed_to_remote ?? 0,
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
                loadDirs()
                loadFiles()
                browserRef.value?.reload()
            } else {
                reloadAll()
            }
        } else {
            ElMessage.warning(t('plugin.rolling-memory.overview.reconcileNoReport'))
            refreshEnvHealth()
        }
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        reconciling.value = false
    }
}

onMounted(() => {
    loadDirs()
    loadFiles()
    // 首屏只订阅实时流：服务端共享 watcher 推帧即达；显式全量诊断保留给动作后的 loadEnvHealth。
    startEnvHealthStream()
})

let bgDeactivated = false
onActivated(() => {
    if (!bgDeactivated) return
    bgDeactivated = false
    startEnvHealthStream()
})

onDeactivated(() => {
    bgDeactivated = true
    if (fileKeywordTimer) {
        clearTimeout(fileKeywordTimer)
        fileKeywordTimer = null
    }
    stopEnvHealthStream()
})

onBeforeUnmount(() => {
    if (fileKeywordTimer) {
        clearTimeout(fileKeywordTimer)
        fileKeywordTimer = null
    }
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
.mb-10 {
    margin-bottom: 10px;
}
.sub-table-box {
    background-color: var(--ag-bg-color-overlay);
    border: 1px solid var(--ag-border-color);
    border-radius: var(--el-border-radius-base);
    padding: 12px;
    height: 100%;
}
.sub-table-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    margin-bottom: 6px;
}
.sub-table-title {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);
}
.sub-table-actions {
    display: flex;
    align-items: center;
    gap: 6px;
}
.sub-table-hint {
    font-size: 12px;
    color: var(--el-text-color-secondary);
    margin-bottom: 8px;
    line-height: 1.4;
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
.filter-tag {
    margin-right: 4px;
}
.files-pager {
    display: flex;
    justify-content: flex-end;
    padding-top: 6px;
}
</style>
