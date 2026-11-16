<template>
    <div class="default-main plugin-index">
        <!-- 环境标识（后端判定，唯一权威）：生产环境禁止安装/卸载，开发环境允许。
             判定依据一并显示（显式配置 plugin.env / 自动判定的源码检出），不只在禁用时才说原因 -->
        <div v-if="env" class="plugin-env-line">
            <el-tag size="small" effect="dark" :type="env.lifecycle_allowed ? 'success' : 'danger'">{{ envNameText }}</el-tag>
            <span class="plugin-env-hint">{{ envHint }}</span>
        </div>

        <!-- 频道门禁：生产环境整频道封闭（弹窗不可关闭，唯一出口是「返回仪表盘」） -->
        <ChannelGate :env="env" />

        <!-- 信任等级如实标注（kind=code 的插件等价于把该段代码 git merge 进仓库，无隔离层） -->
        <el-alert class="plugin-alert" type="warning" :closable="false" show-icon :title="t('plugin.list.tips.trust')" />

        <!-- 运行时对照降级提示（注册文件不可读 / 接线悬空），有则显式报出，不静默 -->
        <el-alert v-if="runtimeAlert" class="plugin-alert" type="info" :closable="false" show-icon :title="runtimeAlert" />

        <TableHeader
            :manager="tableManager"
            v-model:com-search="tableManager.comSearch"
            :buttons="['refresh', 'comSearch', 'quickSearch', 'columnDisplay']"
            :quick-search-placeholder="t('plugin.list.searchPlaceholder')"
        />

        <Table :manager="tableManager" :empty-text="t('plugin.list.empty')" />

        <!-- 详情抽屉：manifest 摘要 + 实体 + 文件清单 + 动作审计（分页）；校验和取台账 checksum 列 -->
        <el-drawer v-model="detailVisible" :title="t('plugin.get.title')" size="min(920px, 100vw)" :destroy-on-close="true">
            <div v-loading="detailLoading" class="plugin-detail">
                <template v-if="detail">
                    <!-- 详情动作（与列表行同一份 API）：停用/启用 + 卸载。状态门禁（启用态不可直卸、
                         半装走强制）与环境门禁（生产禁止卸载）都在后端同一实现，这里按同一口径置灰
                         并显式给原因，不假装可执行 -->
                    <div class="plugin-drawer-ops">
                        <el-button
                            v-if="detail.row.status === 'enabled' && can('disable')"
                            type="warning"
                            :loading="statusChanging"
                            @click="drawerToggle('disable')"
                            >{{ t('plugin.get.actions.toggleOff') }}</el-button
                        >
                        <el-button
                            v-else-if="(detail.row.status === 'installed' || detail.row.status === 'disabled') && can('enable')"
                            type="success"
                            :loading="statusChanging"
                            @click="drawerToggle('enable')"
                            >{{ t('plugin.get.actions.toggleOn') }}</el-button
                        >
                        <el-tooltip v-if="can('uninstall')" :disabled="!uninstallDisabledTip" :content="uninstallDisabledTip" placement="top">
                            <span>
                                <el-button type="danger" :disabled="!!uninstallDisabledTip" @click="openUninstall">{{
                                    t('plugin.get.actions.uninstall')
                                }}</el-button>
                            </span>
                        </el-tooltip>
                        <span v-if="can('uninstall') && uninstallDisabledTip" class="plugin-hint plugin-drawer-ops-hint">{{ uninstallDisabledTip }}</span>
                    </div>

                    <el-descriptions :title="t('plugin.get.sections.manifest')" :column="2" border size="small">
                        <el-descriptions-item :label="t('plugin.get.fields.name')">{{ detail.row.name }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.get.fields.version')">{{ detail.row.version }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.get.fields.kind')">
                            <el-tag size="small" :type="kindCustom[detail.row.kind] ?? 'primary'">{{ kindDict[detail.row.kind] ?? detail.row.kind }}</el-tag>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.get.fields.status')">
                            <el-tag size="small" :type="statusCustom[detail.row.status] ?? 'primary'">{{ statusDict[detail.row.status] ?? detail.row.status }}</el-tag>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.get.fields.hostApi')">{{ detail.row.host_api || '-' }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.get.fields.hostVersion')">{{ detail.row.host_version || '-' }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.get.fields.frameworkReq')">{{ detail.row.framework_req || '-' }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.get.fields.requires')">{{ detail.row.requires || '-' }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.get.fields.checksum')" :span="2">
                            <span class="plugin-mono">{{ detail.row.checksum || '-' }}</span>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.get.fields.source')">{{ detail.row.source || '-' }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.get.fields.signedBy')">{{ detail.row.signed_by || '-' }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.get.fields.installedAt')">{{ formatTime(detail.row.installed_at) }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.get.fields.updatedAt')">{{ formatTime(detail.row.updated_at) }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.get.fields.remark')" :span="2">{{ detail.row.remark || '-' }}</el-descriptions-item>
                    </el-descriptions>

                    <div class="plugin-section-title">{{ t('plugin.get.sections.entities') }}</div>
                    <el-table :data="detail.entities" size="small" border>
                        <el-table-column prop="name" :label="t('plugin.get.fields.name')" min-width="140" show-overflow-tooltip />
                        <el-table-column prop="title_zh" :label="t('plugin.get.fields.titleZh')" min-width="120" show-overflow-tooltip />
                        <el-table-column prop="title_en" :label="t('plugin.get.fields.titleEn')" min-width="120" show-overflow-tooltip />
                        <el-table-column prop="table_name" :label="t('plugin.get.fields.tableName')" min-width="200" show-overflow-tooltip />
                    </el-table>

                    <div class="plugin-section-title">{{ t('plugin.get.sections.files') }}</div>
                    <el-alert v-if="!detail.files.available" type="info" :closable="false" :title="t('plugin.get.files.unavailable')" />
                    <el-table v-else :data="detail.files.list" size="small" border>
                        <el-table-column prop="target" :label="t('plugin.get.files.target')" min-width="240" show-overflow-tooltip />
                        <el-table-column prop="bytes" :label="t('plugin.get.files.bytes')" width="100" align="right" />
                        <el-table-column prop="sha256" :label="t('plugin.get.files.sha256')" min-width="180" show-overflow-tooltip />
                        <el-table-column :label="t('plugin.get.files.conflict')" min-width="180" show-overflow-tooltip>
                            <template #default="scope">{{ scope.row.conflict || t('plugin.get.files.added') }}</template>
                        </el-table-column>
                    </el-table>

                    <div class="plugin-section-title">{{ t('plugin.get.sections.actions') }}</div>
                    <div class="plugin-hint">{{ t('plugin.list.tips.operatorNote') }}</div>
                    <el-table :data="detail.audit.list" size="small" border>
                        <el-table-column prop="action" :label="t('plugin.get.audit.action')" width="100" />
                        <el-table-column prop="operator" :label="t('plugin.get.audit.operator')" width="130" show-overflow-tooltip />
                        <el-table-column prop="status" :label="t('plugin.get.audit.status')" width="100" />
                        <el-table-column :label="t('plugin.get.audit.createdAt')" width="160">
                            <template #default="scope">{{ formatTime(scope.row.created_at) }}</template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.get.audit.detail')" min-width="200" show-overflow-tooltip>
                            <template #default="scope">{{ formatDetail(scope.row.detail) }}</template>
                        </el-table-column>
                    </el-table>
                    <el-pagination
                        v-if="detail.audit.total"
                        class="plugin-audit-pagination"
                        background
                        :page-size="detail.audit.limit"
                        :current-page="detail.audit.page"
                        :page-sizes="[10, 20, 50, 100]"
                        layout="sizes, total, prev, pager, next"
                        :total="detail.audit.total"
                        @size-change="onAuditSizeChange"
                        @current-change="onAuditPageChange"
                    />
                </template>
                <el-empty v-else :description="t('plugin.get.empty')" />
            </div>
        </el-drawer>

        <!-- 卸载确认对话框：purge（彻底删除）与 keep-files（保留文件）为显式勾选，即界面的二次确认；
             adopt 装入（adopted_through>0）的插件 purge 由后端默认拒绝 ⇒ 界面禁选并说明去向；
             failed 半装走后端强制模式，文案显式标注 -->
        <el-dialog v-model="uninstallVisible" :title="t('plugin.get.uninstall.title')" width="min(560px, 92vw)" append-to-body>
            <el-alert type="warning" :closable="false" show-icon :title="t('plugin.get.uninstall.warning')" />
            <div class="plugin-uninstall-intro">
                {{ t('plugin.get.uninstall.intro', { name: detailName, status: detail ? (statusDict[detail.row.status] ?? detail.row.status) : '-' }) }}
            </div>
            <el-alert v-if="isEnabledDetail" class="plugin-uninstall-note" type="warning" :closable="false" :title="t('plugin.get.uninstall.enabledTip')" />
            <el-alert v-if="isFailedDetail" class="plugin-uninstall-note" type="error" :closable="false" :title="t('plugin.get.uninstall.failedTip')" />
            <div class="plugin-uninstall-option">
                <el-checkbox v-model="uninstallPurge" :disabled="purgeForbidden">{{ t('plugin.get.uninstall.purge') }}</el-checkbox>
                <div class="plugin-hint">{{ purgeForbidden ? t('plugin.get.uninstall.purgeAdopted') : t('plugin.get.uninstall.purgeTip') }}</div>
            </div>
            <div class="plugin-uninstall-option">
                <el-checkbox v-model="uninstallKeepFiles">{{ t('plugin.get.uninstall.keepFiles') }}</el-checkbox>
                <div class="plugin-hint">{{ t('plugin.get.uninstall.keepFilesTip') }}</div>
            </div>
            <template #footer>
                <el-button @click="uninstallVisible = false">{{ t('common.cancel') }}</el-button>
                <el-button type="danger" :loading="uninstalling" @click="doUninstall">{{
                    isEnabledDetail ? t('plugin.get.uninstall.confirmDisableFirst') : t('plugin.get.uninstall.confirm')
                }}</el-button>
            </template>
        </el-dialog>
    </div>
</template>

<script setup lang="ts">
import { disablePlugin, enablePlugin, getPlugin, uninstallPlugin, type PluginDetailData, type PluginEnv, type PluginRuntime } from '@/api/plugin'
import { TableManagerAPI } from '@/api/table'
import TableHeader from '@/components/table/header/index.vue'
import Table from '@/components/table/index.vue'
import { useTableManager } from '@/hooks/useTableManager'
import { auth, getCurrentRoutePath } from '@/utils/common'
import { dayjs, ElMessage, ElMessageBox, type TagProps } from 'element-plus'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import ChannelGate from './channelGate.vue'
import { usePluginEnv } from './usePluginEnv'

// 组件名 = 菜单规则名（keepalive 按组件名匹配；管理页菜单行的 name 取宿主保留字 `plugin`）
defineOptions({
    name: 'plugin',
})

const { t } = useI18n()

// 页面鉴权：节点名 = route.path + 节点名末段（assembleAuthNode 口径）——
// 菜单 path=plugin、四个节点 plugin/read|enable|disable|uninstall ⇒ authNode 键 /admin/plugin。
// 中间件层是第一道（节点不授予任何角色组），后端 handler 另有逐超管校验。
const pagePath = getCurrentRoutePath()
const can = (node: string) => auth({ name: pagePath, subNodeName: `${pagePath}/${node}` })

// 字典（字符串枚举一律 tag + custom + dict，禁用 switch 渲染器；数值/布尔域才用 switch）
type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'
const kindDict: Record<string, string> = {
    code: t('plugin.list.kind.code'),
    declarative: t('plugin.list.kind.declarative'),
    mixed: t('plugin.list.kind.mixed'),
}
const kindCustom: Record<string, TagType> = { code: 'primary', declarative: 'info', mixed: 'success' }
const statusDict: Record<string, string> = {
    failed: t('plugin.list.status.failed'),
    installed: t('plugin.list.status.installed'),
    enabled: t('plugin.list.status.enabled'),
    disabled: t('plugin.list.status.disabled'),
}
const statusCustom: Record<string, TagType> = { failed: 'danger', installed: 'info', enabled: 'success', disabled: 'warning' }
const wiredDict: Record<string, string> = {
    wired: t('plugin.list.wired.wired'),
    unwired: t('plugin.list.wired.unwired'),
    declarative: t('plugin.list.wired.declarative'),
}
const wiredCustom: Record<string, TagType> = { wired: 'success', unwired: 'warning', declarative: 'info' }
const driftDict: Record<string, string> = {
    unwired: t('plugin.list.wired.unwired'),
    missing_register: t('plugin.list.wired.missingRegister'),
    declarative_register: t('plugin.list.wired.declarativeRegister'),
    failed: t('plugin.list.status.failed'),
}
const driftCustom: Record<string, TagType> = { unwired: 'warning', missing_register: 'danger', declarative_register: 'danger', failed: 'danger' }

// 差异码 → hover 提示（failed 给清理入口，其余给处置口径）
const driftTip = (code: string) => {
    switch (code) {
        case 'unwired':
            return t('plugin.list.wired.unwiredTip')
        case 'missing_register':
            return t('plugin.list.wired.missingRegisterTip')
        case 'declarative_register':
            return t('plugin.list.wired.declarativeRegisterTip')
        case 'failed':
            return t('plugin.list.tips.failedCleanup')
        default:
            return ''
    }
}

const formatTime = (value?: string | null) => (value ? dayjs(value).format('YYYY-MM-DD HH:mm:ss') : '-')
const formatDetail = (value: unknown) => (value === null || value === undefined || value === '' ? '-' : JSON.stringify(value))

// 接线列：声明式 / 已接线 / 待重编译 三态；hover 提示按差异给出处置口径
const wiredValue = (row: TableRow) => (row.kind === 'declarative' ? 'declarative' : row.wired ? 'wired' : 'unwired')
const wiredTip = (row?: TableRow) => {
    if (!row || row.kind === 'declarative') {
        return t('plugin.list.wired.declarativeTip')
    }
    if (row.wired) {
        return ''
    }
    return row.registered === false ? t('plugin.list.wired.missingRegisterTip') : t('plugin.list.wired.unwiredTip')
}

// tagTitle tag 渲染器的悬浮提示属性：title 是 HTML 原生属性（经 v-bind 透传到 el-tag 根元素），
// 不在 ElTag 的 TagProps 声明里 ⇒ 断言收窄（对象字面量直传会触发 TS 多余属性检查）；
// 空提示返回 {}：不给「已接线」等正常行挂 title="" 的空气泡
const tagTitle = (tip: string): Partial<TagProps> => (tip ? ({ title: tip } as Partial<TagProps>) : {})

// 详情抽屉
const detailVisible = ref(false)
const detailLoading = ref(false)
const detail = ref<PluginDetailData | null>(null)
const detailName = ref('')
const auditPage = ref(1)
const auditLimit = ref(20)

const loadDetail = () => {
    if (!detailName.value) {
        return
    }
    detailLoading.value = true
    getPlugin({ name: detailName.value, page: auditPage.value, limit: auditLimit.value })
        .then((res) => {
            detail.value = res.data.data
        })
        .catch(() => {
            detail.value = null
        })
        .finally(() => {
            detailLoading.value = false
        })
}

const openDetail = (name: string) => {
    detailName.value = name
    detail.value = null
    auditPage.value = 1
    detailVisible.value = true
    loadDetail()
}

const onAuditPageChange = (page: number) => {
    auditPage.value = page
    loadDetail()
}

const onAuditSizeChange = (size: number) => {
    auditLimit.value = size
    auditPage.value = 1
    loadDetail()
}

// 运行时对照降级提示（页面级）
const runtime = ref<PluginRuntime | null>(null)
const runtimeAlert = computed(() => {
    const parts: string[] = []
    if (runtime.value && !runtime.value.registry_file) {
        parts.push(t('plugin.list.runtime.registryUnavailable'))
    }
    if (runtime.value && runtime.value.orphans.length) {
        parts.push(`${t('plugin.list.wired.orphan')}：${runtime.value.orphans.join('、')}（${t('plugin.list.wired.orphanTip')}）`)
    }
    return parts.join('；')
})

// 环境标识（后端判定，见 /admin/plugin/list 的 env 段）；拿不到 env 段（旧后端/列表失败）时不显示——
// 宁可少一行提示，也不在前端自行猜环境（判定与门禁的权威都在后端）。
// 环境名/依据文案与门禁态派生单点 = usePluginEnv（与清单页、来源页共用一份 plugin.envGate.* 文案）。
const env = ref<PluginEnv | null>(null)
const { envNameText, envHint } = usePluginEnv(env)

// 详情抽屉动作：启停（复用列表同一 API）与卸载（管理面唯一卸载入口）。
// 状态门禁（启用态不可直卸、半装才认 force）与环境门禁（生产禁止卸载）都在后端同一实现；
// 这里只按后端回载的 status/env 置灰并显式给原因，不假装可执行、不在前端复述规则。
const statusChanging = ref(false)
const uninstallVisible = ref(false)
const uninstalling = ref(false)
const uninstallPurge = ref(false)
const uninstallKeepFiles = ref(false)

// 环境以详情的 env 段为准（抽屉自包含）；详情未回载时退用列表的 env（两处同一后端判定）
const detailEnv = computed(() => detail.value?.env ?? env.value)
const uninstallAllowed = computed(() => detailEnv.value?.lifecycle_allowed === true)
// adopt 装入（adopted_through>0）的插件：--purge 需 CLI 三重确认，界面禁选
const purgeForbidden = computed(() => (detail.value?.row.adopted_through ?? 0) > 0)
const isFailedDetail = computed(() => detail.value?.row.status === 'failed')
// 启用态：卸载不置灰——确认对话框改为「停用并卸载」两步（先禁用后卸载仍是硬门禁，
// 由界面把两步串起来且两步各自写审计），不再让用户先自行停用再回来点卸载
const isEnabledDetail = computed(() => detail.value?.row.status === 'enabled')
// 卸载按钮的置灰原因（空 = 可卸载）：只剩环境门禁（生产禁止）；状态门禁改由对话框承接
const uninstallDisabledTip = computed(() => {
    if (!uninstallAllowed.value) {
        return t('plugin.get.actions.lifecycleForbidden')
    }
    return ''
})

const drawerToggle = (operate: 'enable' | 'disable') => {
    const current = detail.value
    if (!current) {
        return
    }
    statusChanging.value = true
    const call = operate === 'enable' ? enablePlugin : disablePlugin
    call({ name: current.row.name })
        .then((res) => {
            const effect = res.data.data.effect
            if (effect === 'rebuild_required') {
                ElMessage.warning(t('plugin.list.effect.rebuildRequired', { name: current.row.name }))
            } else if (effect === 'declarative') {
                ElMessage.info(t('plugin.list.effect.declarative', { name: current.row.name }))
            }
            loadDetail()
            tableManager.getData()
        })
        .catch(() => {
            // 失败文案由请求层统一提示（后端错误原样返回）；这里不残留乐观态（未调 loadDetail 即保持原样）
        })
        .finally(() => {
            statusChanging.value = false
        })
}

const openUninstall = () => {
    uninstallPurge.value = false
    uninstallKeepFiles.value = false
    uninstallVisible.value = true
}

// 卸载提交：启用态先停用（同一条硬门禁的两步、各自留审计），停用失败即中止卸载；
// 「彻底删除」勾选即界面的二次确认（服务端把 purge 与 CLI 的 --yes 强制成对）；半装走 force
const doUninstall = () => {
    const current = detail.value
    if (!current) {
        return
    }
    const name = current.row.name
    uninstalling.value = true
    const preStep =
        current.row.status === 'enabled'
            ? disablePlugin({ name }).then(() => undefined)
            : Promise.resolve(undefined)
    preStep
        .then(() =>
            uninstallPlugin({ name, purge: uninstallPurge.value, keep_files: uninstallKeepFiles.value, force: isFailedDetail.value })
        )
        .then((res) => {
            // 成功回执两段：执行回执（CLI 同源输出，含逐段删/保留清单与计数）+ effect 口径；
            // 弹窗展示（不可逆动作要给全留痕）后收尾：关对话框与抽屉、刷新列表
            const effectText =
                res.data.data.effect === 'rebuild_required'
                    ? t('plugin.get.uninstall.effect.rebuildRequired')
                    : t('plugin.get.uninstall.effect.gateTtl')
            const report = (res.data.data.report || '').trim()
            const body = report ? `${report}\n\n${effectText}` : effectText
            ElMessageBox.alert(body, t('plugin.get.uninstall.done', { name }), {
                confirmButtonText: t('common.confirm'),
                customClass: 'plugin-uninstall-report',
            }).finally(() => {
                uninstallVisible.value = false
                detailVisible.value = false
                detail.value = null
                tableManager.getData()
            })
        })
        .catch(() => {
            // 同上：失败文案由请求层提示（如「处于 enabled：先 disable」「adopt 装入的 --purge 默认拒绝」）
        })
        .finally(() => {
            uninstalling.value = false
        })
}

// 启停：确认框走 OptButton 的 confirm 渲染器；回执按 effect 码如实提示（不假装已生效）
const setStatus = (operate: 'enable' | 'disable', name: string) => {
    const call = operate === 'enable' ? enablePlugin : disablePlugin
    call({ name })
        .then((res) => {
            const effect = res.data.data.effect
            if (effect === 'rebuild_required') {
                ElMessage.warning(t('plugin.list.effect.rebuildRequired', { name }))
            } else if (effect === 'declarative') {
                ElMessage.info(t('plugin.list.effect.declarative', { name }))
            }
            tableManager.getData()
        })
        .catch(() => {
            // 失败文案由请求层统一提示（后端错误原样返回），这里只保证列表不残留乐观态
        })
}

const optButtons: OptButton[] = [
    {
        render: 'tip',
        name: 'detail',
        title: t('plugin.list.actions.detail'),
        type: 'info',
        icon: 'lucide-search',
        display: () => can('read'),
        click: (row) => openDetail(String(row.name)),
    },
    {
        render: 'confirm',
        name: 'enable',
        title: t('plugin.list.actions.enable'),
        type: 'success',
        icon: 'lucide-play',
        popconfirm: (ctx) => ({ title: t('plugin.list.actions.enableConfirm', { name: ctx.row?.name ?? '' }) }),
        display: (row) => can('enable') && (row.status === 'installed' || row.status === 'disabled'),
        click: (row) => setStatus('enable', String(row.name)),
    },
    {
        render: 'confirm',
        name: 'disable',
        title: t('plugin.list.actions.disable'),
        type: 'warning',
        icon: 'lucide-pause',
        popconfirm: (ctx) => ({ title: t('plugin.list.actions.disableConfirm', { name: ctx.row?.name ?? '' }) }),
        display: (row) => can('disable') && row.status === 'enabled',
        click: (row) => setStatus('disable', String(row.name)),
    },
]

const tableManager = useTableManager({
    api: new TableManagerAPI('/admin/plugin/'),
    table: {
        column: [
            { label: t('plugin.list.columns.name'), prop: 'name', align: 'left', width: 180, operator: 'ILIKE', quickSearch: true, showOverflowTooltip: true },
            { label: t('plugin.list.columns.title'), prop: 'title', align: 'left', minWidth: 160, operator: 'ILIKE', quickSearch: true, showOverflowTooltip: true },
            { label: t('plugin.list.columns.version'), prop: 'version', align: 'center', width: 100, operator: 'eq' },
            {
                label: t('plugin.list.columns.kind'),
                prop: 'kind',
                align: 'center',
                width: 110,
                operator: 'eq',
                render: 'tag',
                custom: kindCustom,
                dict: kindDict,
            },
            {
                label: t('plugin.list.columns.status'),
                prop: 'status',
                align: 'center',
                width: 110,
                operator: 'eq',
                render: 'tag',
                custom: statusCustom,
                dict: statusDict,
            },
            { label: t('plugin.list.columns.entities'), prop: 'entities', align: 'center', width: 90, operator: false },
            {
                label: t('plugin.list.columns.wired'),
                prop: 'wired',
                align: 'center',
                width: 120,
                operator: false,
                render: 'tag',
                formatter: (row: TableRow) => wiredValue(row),
                custom: wiredCustom,
                dict: wiredDict,
                customRenderAttr: {
                    tag: (ctx: TableContextData) => tagTitle(wiredTip(ctx.row)),
                },
            },
            {
                label: t('plugin.list.columns.drift'),
                prop: 'drift',
                align: 'left',
                minWidth: 200,
                operator: false,
                render: 'tags',
                custom: driftCustom,
                dict: driftDict,
                customRenderAttr: {
                    tag: (ctx: TableContextData) => tagTitle(driftTip(String(ctx.cellValue ?? ''))),
                },
            },
            {
                label: t('plugin.list.columns.installedAt'),
                prop: 'installed_at',
                align: 'center',
                width: 160,
                render: 'datetime',
                comSearchRender: 'datetime',
                sortable: 'custom',
                operator: 'BETWEEN',
            },
            {
                label: t('common.operate'),
                align: 'center',
                width: 140,
                render: 'buttons',
                buttons: optButtons,
                operator: false,
                // 固定在右侧：本表 10 列总宽超常见视口，不固定时操作按钮落在横向滚动区外、
                // 用户看不到也点不到（「无法操作」的直接来源）；固定后始终可见
                fixed: 'right',
            },
        ],
        filter: {
            // id DESC ≈ 安装先后；installed_at 可能为 NULL（failed 行），排序托底交给主键
            sort: 'id',
            order: 'desc',
        },
        dblClickNotEditColumn: ['all'],
    },
})

// 列表响应随带的运行时段（注册文件可读性 / 接线悬空）——在 **body.data** 层级：
// 后端是 httpx.WithData(gin.H{list, total, runtime, env})（internal/handler/admin/plugin/plugin.go），
// 与 useTableManager 读 res.data.data.list/total 同一层；读错一层会让页面级提示恒不显示。
// （形参口径沿用 table.d.ts 的 after.getData 声明 = ApiResponse 默认泛型 any；`res.data.data` 才是响应载荷。）
// 失败分支只带 { err }（useTableManager 的 .catch）：没有 res 时直接返回，别让钩子自己抛错
// （钩子在 .catch 里抛错会让 getData() 的 promise 变拒绝，同 render/index.vue 的写法）。
tableManager.opts.after!.getData = ({ res }: { res?: ApiResponse }) => {
    if (!res) {
        return
    }
    const payload = res.data.data as { runtime?: PluginRuntime; env?: PluginEnv } | undefined
    runtime.value = payload?.runtime ?? null
    env.value = payload?.env ?? null
}

tableManager.initCtx()
tableManager.getData()
</script>

<style scoped lang="scss">
.plugin-index {
    .plugin-alert {
        margin-bottom: 10px;
    }

    .plugin-env-line {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-bottom: 10px;
    }

    .plugin-env-hint {
        font-size: 13px;
        color: var(--el-text-color-secondary);
    }

    .plugin-detail {
        min-height: 200px;
    }

    .plugin-drawer-ops {
        display: flex;
        align-items: center;
        gap: 10px;
        flex-wrap: wrap;
        margin-bottom: 14px;
    }

    .plugin-drawer-ops-hint {
        margin-bottom: 0;
    }

    .plugin-uninstall-intro {
        margin: 12px 0 8px;
        font-weight: 600;
    }

    .plugin-uninstall-note {
        margin-bottom: 10px;
    }

    .plugin-uninstall-option {
        margin-top: 6px;
    }

    .plugin-section-title {
        margin: 18px 0 8px;
        font-weight: 600;
        color: var(--el-text-color-primary);
    }

    .plugin-hint {
        margin-bottom: 8px;
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .plugin-audit-pagination {
        margin-top: 10px;
        justify-content: flex-end;
    }

    .plugin-mono {
        font-family: var(--el-font-family-mono, monospace);
        word-break: break-all;
    }
}
</style>

<style lang="scss">
/* 卸载回执弹窗：CLI 输出按纯文本呈现（保留换行），弹窗挂在 body 上，scoped 样式够不着 */
.plugin-uninstall-report {
    .el-message-box__message p {
        white-space: pre-wrap;
        word-break: break-all;
        font-family: var(--el-font-family-mono, monospace);
        font-size: 12px;
        line-height: 1.6;
        max-height: 50vh;
        overflow: auto;
    }
}
</style>
