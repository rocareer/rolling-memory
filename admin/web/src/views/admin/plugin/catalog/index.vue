<!--
  插件清单页（插件清单方案）。
  页面装配与组件用法移植自 BuildAdmin v2.3.9 的 module/index.vue：
  TableHeader + Tabs + GoodsInfo + CommonDialog；来源页签是过滤器，卡片/详情/预演/安装对所有来源同一套代码。
  已修改：接入 ai-go-admin 的 catalog 接口、字段、权限节点与主题；删除了 BuildAdmin 账号、购买、支付、
  上传 ZIP、市场地址、覆盖式冲突与 store/types 依赖。
-->
<template>
    <div class="default-main plugin-catalog-index">
        <!-- 环境标识（后端判定，唯一权威）：生产环境禁止安装/卸载，按钮随之置灰；预演是只读的，不在此限 -->
        <div v-if="env" class="catalog-env-line">
            <el-tag size="small" effect="dark" :type="installAllowed ? 'success' : 'danger'">{{ envNameText }}</el-tag>
            <span class="catalog-env-hint">{{ envHint }}</span>
        </div>

        <!-- 频道门禁：生产环境整频道封闭（弹窗不可关闭，唯一出口是「返回仪表盘」） -->
        <ChannelGate :env="env" />

        <el-alert class="catalog-alert" type="warning" :closable="false" show-icon :title="t('plugin.source.catalog.tips.trust')" />

        <!-- 列表请求失败：区别于「没有来源」；保留重试入口，不把服务失败误报成空态 -->
        <el-alert v-if="loadError" class="catalog-alert" type="error" :closable="false" show-icon :title="loadErrorText.message">
            <div v-if="loadError.message" class="sync-error-line">{{ loadError.message }}</div>
            <div class="sync-error-line">{{ loadErrorText.hint }}</div>
            <el-button link type="primary" @click="loadData">{{ t('common.refresh') }}</el-button>
        </el-alert>

        <!-- 逐来源同步失败：错误 + 下一步；不伪造「全部成功」 -->
        <el-alert
            v-for="source in errorSources"
            :key="source.id"
            class="catalog-alert"
            type="error"
            :closable="false"
            show-icon
            :title="`${source.title || source.name}: ${source.last_sync_error || t('plugin.source.catalog.sourceErrorUnknown')}`"
        >
            <div class="sync-error-line">{{ t('plugin.source.catalog.sourceErrorHint') }}</div>
            <el-button link type="primary" @click="goSource(source.name)">{{ t('plugin.source.catalog.editSource') }}</el-button>
        </el-alert>

        <el-alert v-if="syncError" class="catalog-alert" type="error" :closable="false" show-icon :title="syncErrorText.message">
            <div v-if="syncError.message" class="sync-error-line">{{ syncError.message }}</div>
            <div class="sync-error-line">{{ syncErrorText.hint }}</div>
        </el-alert>

        <el-alert v-if="failedSyncs.length" class="catalog-alert" type="warning" :closable="true" @close="clearSyncResults">
            <template #title>{{ t('plugin.source.catalog.syncPartial', { failed: failedSyncs.length, total: syncResults.length }) }}</template>
            <div v-for="item in failedSyncs" :key="item.source_id" class="sync-error-line">
                {{ item.name || item.source_id }}: {{ item.error || '-' }}
            </div>
        </el-alert>

        <!-- 空态：没有来源 ⇒ 先去配置来源；有来源但没有条目 ⇒ 去同步 -->
        <el-empty v-if="!loading && !loadError && !sources.length" :description="t('plugin.source.catalog.empty.noSources')">
            <el-button type="primary" @click="goSource()">{{ t('plugin.source.catalog.empty.goSource') }}</el-button>
        </el-empty>

        <template v-else>
            <CatalogHeader
                :syncing="syncing"
                :can-sync="can('sync')"
                :placeholder="t('plugin.source.catalog.searchPlaceholder')"
                @refresh="loadData"
                @sync="onSync"
                @search="onSearch"
            />

            <Tabs v-model="activeTab" :sources="sources" :entries="entries" :loading="loading" @open="openDetail" />

            <!-- 有来源但没有条目：搜索无命中与「来源尚未同步」必须分开说，
                 否则页签空态里的「该来源暂无清单条目」会被读成「确实没有条目」 -->
            <div v-if="!loading && sources.length && !total" class="catalog-empty-actions">
                <div v-if="keyword" class="catalog-empty-hint">{{ t('plugin.source.catalog.empty.noMatch', { keyword }) }}</div>
                <el-button v-if="can('sync')" type="primary" @click="onSync">{{ t('plugin.source.catalog.empty.goSync') }}</el-button>
            </div>

            <el-pagination
                v-if="total > limit"
                class="catalog-pagination"
                background
                layout="total, prev, pager, next"
                :total="total"
                :page-size="limit"
                :current-page="page"
                @current-change="onPageChange"
            />
        </template>

        <GoodsInfo
            v-model="detailVisible"
            :entry="selected"
            :versions="versions"
            :enabled-source-ids="enabledSourceIds"
            :can-preflight="can('preflight')"
            :can-install="can('install')"
            :install-allowed="installAllowed"
            @preflight="onPreflight($event, 'preflight')"
            @install="onPreflight($event, 'install')"
        />

        <CommonDialog
            v-model="dialogVisible"
            :state="dialogState"
            :intent="dialogIntent"
            :loading-text="dialogLoadingText"
            :preflight="preflight"
            :result="installResult"
            :error="dialogError"
            :installing="installing"
            :source-name="selected?.source_name"
            @confirm="confirmInstall"
            @close="dialogVisible = false"
        />
    </div>
</template>

<script setup lang="ts">
import {
    catalogErrorKey,
    errorInfo,
    installCatalog,
    listCatalog,
    preflightCatalog,
    syncCatalog,
    type CatalogEntry,
    type CatalogInstallData,
    type CatalogPreflightData,
    type CatalogSourceSummary,
    type CatalogSyncResult,
    type ErrorInfo,
} from '@/api/plugin/source'
import type { PluginEnv } from '@/api/plugin'
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { auth, getCurrentRoutePath } from '@/utils/common'
import { isCancel } from 'axios'
import { ElMessage } from 'element-plus'
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import CatalogHeader from './components/catalogHeader.vue'
import CommonDialog from './components/commonDialog.vue'
import GoodsInfo from './components/goodsInfo.vue'
import Tabs from './components/tabs.vue'
import ChannelGate from '../channelGate.vue'
import { usePluginEnv } from '../usePluginEnv'

// keepalive 按组件名匹配；菜单行 name=plugin/catalog（数据库迁移）
defineOptions({
    name: 'plugin/catalog',
})

const { t, te } = useI18n()
const router = useRouter()

const pagePath = getCurrentRoutePath()
const can = (node: string) => auth({ name: pagePath, subNodeName: `${pagePath}/${node}` })

const sources = ref<CatalogSourceSummary[]>([])
const entries = ref<CatalogEntry[]>([])
const total = ref(0)
const page = ref(1)
const limit = 40
const keyword = ref('')
const loading = ref(false)
const syncing = ref(false)
const activeTab = ref('all')
const loadError = ref<ErrorInfo | null>(null)
const loadErrorKey = computed(() => catalogErrorKey(loadError.value?.code, te))
const loadErrorText = computed(() => ({
    message: t(`plugin.source.errors.${loadErrorKey.value}.message`),
    hint: t(`plugin.source.errors.${loadErrorKey.value}.hint`),
}))

const enabledSources = computed(() => sources.value.filter((source) => source.enabled === 1))
const enabledSourceIds = computed(() => enabledSources.value.map((source) => source.id))
const errorSources = computed(() => sources.value.filter((source) => source.last_sync_status === 'error'))
const activeSourceId = computed(() => (activeTab.value === 'all' ? undefined : Number(activeTab.value)))

// 环境标识（后端判定，见 /admin/plugin/catalog/list 的 env 段）：生产环境禁止安装/卸载。
// 后端门禁才是权威，这里只把它翻译成界面状态：
//   - 拿不到 env 段（旧后端 / 列表失败）⇒ 不置灰（安装会被后端按真实环境裁决，不会因此绕过门禁）；
//   - 拿得到 ⇒ 一律以后端的 lifecycle_allowed 为准，不在前端复述「name === 'dev'」这条规则。
// 环境名/依据文案与门禁态派生单点 = usePluginEnv（与插件管理页、来源页共用一份 plugin.envGate.* 文案）。
const env = ref<PluginEnv | null>(null)
const installAllowed = computed(() => env.value?.lifecycle_allowed ?? true)
const { envNameText, envHint } = usePluginEnv(env)

let suppressTabReload = false
// 请求序号：快速切页签/改搜索词时，不同参数的请求不会被 cancelDuplicate 取消；
// 只接受最后一次请求的响应，避免早发出晚返回覆盖当前页签。
let loadSeq = 0
watch(activeTab, () => {
    if (suppressTabReload) {
        suppressTabReload = false
        return
    }
    page.value = 1
    loadData()
})

const loadData = async () => {
    const seq = ++loadSeq
    loading.value = true
    try {
        const res = await listCatalog({
            source_id: activeSourceId.value,
            keyword: keyword.value || undefined,
            page: page.value,
            limit,
        })
        if (seq !== loadSeq) return
        const data = res.data.data
        loadError.value = null
        sources.value = data.sources ?? []
        entries.value = data.list ?? []
        total.value = data.total ?? 0
        env.value = data.env ?? null

        // 当前页签的来源被停用/删除 ⇒ 回到「全部」并重取（停用来源的快照仍可在“全部”只读查看）
        if (activeTab.value !== 'all' && !enabledSources.value.some((source) => String(source.id) === activeTab.value)) {
            suppressTabReload = true
            activeTab.value = 'all'
            page.value = 1
            await loadData()
        }
    } catch (err) {
        // 重复请求被 abort（axios cancel）不是列表失败；过期响应也不写入错误态
        if (seq !== loadSeq || isCancel(err)) return
        // 列表失败 ≠ 没有来源：保留页面错误态与重试入口，不落入空态文案
        loadError.value = errorInfo(err)
        entries.value = []
        total.value = 0
    } finally {
        if (seq === loadSeq) loading.value = false
    }
}

const onSearch = (keywords: string) => {
    keyword.value = keywords
    page.value = 1
    loadData()
}

const onPageChange = (target: number) => {
    page.value = target
    loadData()
}

/* ---------------- 同步清单 ---------------- */
const syncResults = ref<CatalogSyncResult[]>([])
const syncError = ref<ErrorInfo | null>(null)
const failedSyncs = computed(() => syncResults.value.filter((item) => item.status === 'error'))
const clearSyncResults = () => {
    syncResults.value = []
}
const syncErrorKey = computed(() => catalogErrorKey(syncError.value?.code, te))
const syncErrorText = computed(() => ({
    message: t(`plugin.source.errors.${syncErrorKey.value}.message`),
    hint: t(`plugin.source.errors.${syncErrorKey.value}.hint`),
}))

const onSync = async () => {
    if (!can('sync') || syncing.value) return
    syncing.value = true
    syncResults.value = []
    syncError.value = null
    try {
        const res = await syncCatalog({})
        const data = res.data.data
        syncResults.value = data.results ?? []
        if (failedSyncs.value.length) {
            ElMessage.warning(t('plugin.source.catalog.syncPartial', { failed: failedSyncs.value.length, total: syncResults.value.length }))
        } else {
            ElMessage.success(t('plugin.source.catalog.syncOk', { total: syncResults.value.length }))
        }
        page.value = 1
        await loadData()
    } catch (err) {
        if (isCancel(err)) return
        syncError.value = errorInfo(err)
    } finally {
        syncing.value = false
    }
}

/* ---------------- 详情 / 预演 / 安装 ---------------- */
const detailVisible = ref(false)
const selected = ref<CatalogEntry | null>(null)
const versions = ref<CatalogEntry[]>([])
// 详情版本表的请求序号：快速连开不同卡片时只接受最后一次响应
let versionSeq = 0

const openDetail = async (entry: CatalogEntry) => {
    const seq = ++versionSeq
    selected.value = entry
    versions.value = [entry]
    detailVisible.value = true
    try {
        // 版本表按 uid 单独请求，不依赖当前卡片页的 40 条
        const res = await listCatalog({ uid: entry.uid, page: 1, limit: 100 })
        if (seq !== versionSeq) return
        versions.value = (res.data.data.list ?? []).filter((item) => item.uid === entry.uid)
    } catch (err) {
        // 重复请求被 abort 或已切到别的卡片：不弹假错误；保留单行兜底
        if (seq !== versionSeq || isCancel(err)) return
        ElMessage.warning(t('plugin.source.catalog.detail.versionsLoadFailed'))
    }
}

const dialogVisible = ref(false)
const dialogState = ref<'loading' | 'preflight' | 'done' | 'failed'>('loading')
const dialogIntent = ref<'preflight' | 'install'>('install')
const dialogLoadingText = ref('')
const preflight = ref<CatalogPreflightData | null>(null)
const installResult = ref<CatalogInstallData | null>(null)
const dialogError = ref<ErrorInfo | null>(null)
const installing = ref(false)
// 预演请求序号：连续打开不同条目时只接受最后一次回执
let preflightSeq = 0

const onPreflight = async (entry: CatalogEntry, intent: 'preflight' | 'install') => {
    if (intent === 'install' ? !can('install') || !installAllowed.value : !can('preflight')) {
        return
    }
    const seq = ++preflightSeq
    selected.value = entry
    detailVisible.value = false
    dialogIntent.value = intent
    dialogState.value = 'loading'
    dialogLoadingText.value = t('plugin.source.catalog.dialog.preflightLoading')
    preflight.value = null
    installResult.value = null
    dialogError.value = null
    dialogVisible.value = true
    try {
        const res = await preflightCatalog({ source_id: entry.source_id, uid: entry.uid, version: entry.version })
        if (seq !== preflightSeq) return
        preflight.value = res.data.data
        dialogState.value = 'preflight'
    } catch (err) {
        if (seq !== preflightSeq || isCancel(err)) return
        dialogError.value = errorInfo(err)
        dialogState.value = 'failed'
    }
}

const confirmInstall = async (allowUnsigned: boolean) => {
    const data = preflight.value
    // 预演回执的 entry 是清单条目（PluginEntry，无 source_id）；来源取当前选中行
    const sourceId = selected.value?.source_id
    if (!data || !data.installable) return
    // 环境门禁（与按钮置灰同源）：生产环境不放行——即使对话框已经用预演结果打开了
    if (!installAllowed.value) return
    if (!sourceId) {
        dialogError.value = { code: 'source_not_found', message: '' }
        dialogState.value = 'failed'
        return
    }
    installing.value = true
    dialogState.value = 'loading'
    dialogLoadingText.value = t('plugin.source.catalog.dialog.installLoading')
    try {
        const res = await installCatalog({
            source_id: sourceId,
            uid: data.entry.uid,
            version: data.entry.version,
            allow_unsigned: allowUnsigned,
            confirm_sha256: data.package.sha256,
        })
        installResult.value = res.data.data
        dialogState.value = 'done'
        page.value = 1
        await loadData()
    } catch (err) {
        dialogError.value = errorInfo(err)
        dialogState.value = 'failed'
    } finally {
        installing.value = false
    }
}

const goSource = (name?: string) => {
    // 来源页路由由菜单下发（数据库迁移）；未注册时直接 push 会落到空白区，先提示再中止
    if (!router.hasRoute('plugin/source')) {
        ElMessage.warning(t('plugin.source.routeMissing'))
        return
    }
    router.push({ path: `${adminBaseRoutePath}/plugin/source`, query: name ? { source: name } : {} })
}

onMounted(() => {
    loadData()
})
</script>

<style scoped lang="scss">
.plugin-catalog-index {
    .catalog-alert {
        margin-bottom: 10px;
    }

    .catalog-env-line {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-bottom: 10px;
    }

    .catalog-env-hint {
        font-size: 13px;
        color: var(--el-text-color-secondary);
    }

    .sync-error-line {
        font-size: 12px;
        line-height: 20px;
        word-break: break-all;
    }

    .catalog-empty-actions {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 10px;
        padding-bottom: 20px;
    }

    .catalog-empty-hint {
        font-size: 13px;
        color: var(--el-text-color-secondary);
    }

    .catalog-pagination {
        margin-top: 10px;
        justify-content: flex-end;
    }
}
</style>
