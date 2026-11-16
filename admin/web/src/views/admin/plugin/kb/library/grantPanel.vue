<!-- plugin: kb v1.0.0 -->
<template>
    <el-drawer v-model="visible" class="kb-grant-panel" size="min(880px, 100vw)" :destroy-on-close="true">
        <template #header>
            <div class="drawer-head">
                <div class="drawer-title">{{ t('plugin.kb.library.grantPanel') }}</div>
                <div class="drawer-sub">
                    <span class="drawer-name">{{ library?.name || '-' }}</span>
                    <el-tag size="small" type="info">ID {{ library?.id ?? '-' }}</el-tag>
                    <span class="drawer-owner">{{ ownerLabel }}</span>
                    <el-tag size="small" :type="library?.status ? 'success' : 'info'">
                        {{ library?.status ? t('common.enable') : t('common.disable') }}
                    </el-tag>
                </div>
            </div>
        </template>

        <!-- 授权表单：节点门 plugin/kb/grant/add -->
        <el-form v-if="canGrantAdd" class="grant-form" label-position="top" @submit.prevent>
            <div class="grant-form-row">
                <el-form-item :label="t('plugin.kb.library.grantSubjectKind')" class="grant-form-item grant-form-item-kind">
                    <el-radio-group v-model="form.subject_kind" @change="onKindChange">
                        <el-radio-button value="all">{{ t('plugin.kb.library.grantSubjectAll') }}</el-radio-button>
                        <el-radio-button value="team">{{ t('plugin.kb.library.grantSubjectTeam') }}</el-radio-button>
                    </el-radio-group>
                </el-form-item>
                <el-form-item
                    v-if="form.subject_kind !== 'all'"
                    :label="t('plugin.kb.library.grantSubjectId')"
                    class="grant-form-item grant-form-item-id"
                >
                    <RemoteSelect
                        v-model="form.subject_id"
                        field="group_name"
                        remote-url="/admin/plugin/kb/team/list"
                        :placeholder="t('common.pleaseSelect', { field: t('plugin.kb.library.grantSubjectTeam') })"
                        :pagination="false"
                        :empty-values="[null, 0]"
                        :value-on-clear="0"
                    />
                </el-form-item>
                <el-form-item :label="t('plugin.kb.library.grantPerm')" class="grant-form-item grant-form-item-perm">
                    <el-radio-group v-model="form.perm">
                        <el-radio-button value="read">{{ t('plugin.kb.library.grantPermRead') }}</el-radio-button>
                        <el-radio-button value="write">{{ t('plugin.kb.library.grantPermWrite') }}</el-radio-button>
                    </el-radio-group>
                </el-form-item>
                <el-form-item class="grant-form-item grant-form-item-submit">
                    <el-button v-blur type="primary" :loading="adding" @click="onAdd">{{ t('plugin.kb.library.grantAdd') }}</el-button>
                </el-form-item>
            </div>
            <div class="form-hint">{{ t('plugin.kb.library.grantHint') }}</div>
        </el-form>

        <el-alert v-if="loadError" :title="loadError" type="warning" :closable="false" show-icon class="grant-alert" />

        <!-- 授权列表：Table + useTableManager（行操作列走 buttons 渲染器 + OptButton；分页由 Table 内建） -->
        <Table :manager="grantManager" />

        <template #footer>
            <el-button @click="visible = false">{{ t('common.close') }}</el-button>
        </template>
    </el-drawer>
</template>

<script setup lang="ts">
import { addKbGrant, listKbGrants, removeKbGrant, type KbGrantPerm, type KbGrantRow, type KbGrantSubjectKind, type KbLibraryRow } from '../api'
import { TableManagerAPI } from '@/api/table'
import RemoteSelect from '@/components/agInput/components/remoteSelect.vue'
import Table from '@/components/table/index.vue'
import { useTableManager } from '@/hooks/useTableManager'
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { auth, getCurrentRoutePath } from '@/utils/common'
import type { AxiosResponse } from 'axios'
import { ElMessage } from 'element-plus'
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

interface Props {
    // 被授权库（列表行原样传入：id/name/owner_kind/owner_id/status）
    library: KbLibraryRow | null
}

const props = defineProps<Props>()
const visible = defineModel<boolean>({ required: true })
const { t } = useI18n()

// 节点门口径：授权节点（plugin/kb/grant/*）与库管理页挂在同一个菜单下，authNode 的键 = 本页路由 path，
// 叶子条目 = 前缀 + 节点 name 全名（utils/router.ts 的 assembleAuthNode）⇒ 一律取「前缀 + plugin/kb/ + 节点路径」。
const pagePath = getCurrentRoutePath()
const canKb = (node: string) => auth({ name: pagePath, subNodeName: `${adminBaseRoutePath}/plugin/kb/${node}` })
const canGrantAdd = canKb('grant/add')
const canGrantRemove = canKb('grant/remove')

// 面板内状态：列表数据/分页/加载/错误态由 useTableManager 承担，这里只留表单与在途动作
const loadError = ref('')
const adding = ref(false)
const removingId = ref(0)

const form = reactive<{ subject_kind: KbGrantSubjectKind; subject_id: number; perm: KbGrantPerm }>({
    subject_kind: 'all',
    subject_id: 0,
    perm: 'read',
})

const ownerLabel = computed(() => {
    if (!props.library) return '-'
    const kind = props.library.owner_kind === 'user' ? t('plugin.kb.library.ownerKindUser') : t('plugin.kb.library.ownerKindSystem')
    return props.library.owner_kind === 'user' ? `${kind} #${props.library.owner_id}` : kind
})

// 显示字典与色板（值域闭集，与 KbGrantRow 同源）：主体类型 / 权限轴
// （user 已在授权表单里下线——用户级主体面不可用；字典保留 user 一行只为回显历史授权行）
const subjectKindDict: Record<string, string> = {
    all: t('plugin.kb.library.grantSubjectAll'),
    team: t('plugin.kb.library.grantSubjectTeam'),
    user: t('plugin.kb.library.grantSubjectUser'),
}
const subjectKindTone: Record<string, string> = { all: 'success', team: 'warning', user: 'primary' }
const permDict: Record<string, string> = { read: t('plugin.kb.library.grantPermRead'), write: t('plugin.kb.library.grantPermWrite') }
const permTone: Record<string, string> = { read: 'success', write: 'warning' }

// 表格行类型为 element-plus 的 DefaultRow，具名取值/提交前统一按 KbGrantRow 收窄
const asGrant = (row: TableRow) => row as unknown as KbGrantRow
const grantedByLabel = (row: KbGrantRow) => `${row.granted_by_kind || '-'} #${row.granted_by_id}`

// 请求被重复请求取消器 abort 属正常路径，不当作加载失败
const isCanceled = (err: any) => err?.code === 'ERR_CANCELED' || err?.name === 'CanceledError'

// 乱序保护：并发/切换库时旧响应不得写进当前面板
let loadSeq = 0

/** 过期响应（库已切换 / 无目标库）：按取消路径忽略，不当作加载失败 */
class StaleResponse extends Error {}

/**
 * 非 CRUD 数据源：授权列表接口为 {library_id, page, limit}、撤权入参为 grant_id
 * （非生成器式 {pks}）⇒ 继承 TableManagerAPI 覆写 list，仍走同一套 Table 与操作列。
 * 泛型签名与基类对齐；数据源是既有 API 函数、形状由本插件的 api.ts 定，
 * 故此处对响应做一次显式收窄（list/total 由 getData 消费）。
 */
class KbGrantAPI extends TableManagerAPI {
    constructor() {
        super('/admin/plugin/kb/grant/')
    }

    list<T = TableManagerListAPIDefaultData>(filter: TableInterface['filter'] = {}) {
        const lib = props.library
        const seq = ++loadSeq
        if (!lib) {
            return Promise.reject(new StaleResponse('grant list without library'))
        }
        return listKbGrants({ library_id: lib.id, page: filter.page, limit: filter.limit }).then((res) => {
            if (seq !== loadSeq || props.library?.id !== lib.id) {
                throw new StaleResponse('stale grant list response')
            }
            return res as unknown as AxiosResponse<ApiResponse<T>>
        })
    }
}

// 行操作：撤权（软删即时生效、不可逆）⇒ confirm 渲染器承载原 popconfirm；
// 节点门 plugin/kb/grant/remove 决定显隐（列与按钮双门，与「无节点整列隐藏」同口径）
const optButtons: OptButton[] = [
    {
        render: 'confirm',
        name: 'remove',
        title: t('plugin.kb.library.grantRemove'),
        type: 'danger',
        icon: 'lucide-trash',
        display: () => canGrantRemove,
        loading: (row) => removingId.value === asGrant(row).id,
        popconfirm: {
            title: t('plugin.kb.library.grantRemoveConfirm'),
            confirmButtonText: t('plugin.kb.library.grantRemove'),
            cancelButtonText: t('common.cancel'),
            confirmButtonType: 'danger',
        },
        click: (row) => void onRemove(asGrant(row)),
    },
]

const grantManager = useTableManager({
    api: new KbGrantAPI(),
    table: {
        column: [
            { label: 'ID', prop: 'id', width: 70, align: 'center', operator: false },
            {
                label: t('plugin.kb.library.grantSubjectKind'),
                prop: 'subject_kind',
                width: 100,
                align: 'center',
                render: 'tag',
                dict: subjectKindDict,
                custom: subjectKindTone,
                customRenderAttr: { tag: { size: 'small' } },
                operator: false,
            },
            {
                label: t('plugin.kb.library.grantSubjectId'),
                width: 130,
                align: 'center',
                // 公开 = 主体 all 一行，subject_id 恒 0 哨兵（服务层同口径）⇒ 不展示哨兵值
                formatter: (row: TableRow) => (row.subject_kind === 'all' ? '—' : `#${row.subject_id}`),
                operator: false,
            },
            {
                label: t('plugin.kb.library.grantPerm'),
                prop: 'perm',
                width: 100,
                align: 'center',
                render: 'tag',
                dict: permDict,
                custom: permTone,
                customRenderAttr: { tag: { effect: 'plain', size: 'small' } },
                operator: false,
            },
            {
                label: t('plugin.kb.library.grantedBy'),
                minWidth: 120,
                align: 'center',
                formatter: (row: TableRow) => grantedByLabel(asGrant(row)),
                operator: false,
            },
            {
                label: t('common.createdAt'),
                prop: 'created_at',
                width: 160,
                align: 'center',
                render: 'datetime',
                operator: false,
            },
            // 撤权列：节点门 plugin/kb/grant/remove（无节点则整列隐藏）
            {
                label: t('common.operate'),
                align: 'center',
                width: 90,
                render: 'buttons',
                buttons: optButtons,
                operator: false,
                show: canGrantRemove,
            },
        ],
        filter: {
            page: 1,
            limit: 20,
        },
        // 面板无编辑表单：双击行不得触发框架默认编辑（否则会打不存在的 get 接口）
        dblClickNotEditColumn: ['all'],
    },
})

// 未打开抽屉 / 无目标库时不发请求；每轮加载前清掉上一轮错误态
grantManager.opts.before!.getData = () => {
    if (!props.library || !visible.value) return false
    loadError.value = ''
    return true
}

// 列表失败回显：服务端拒绝（如无 plugin/kb/grant/read 节点）与网络失败同型；取消 / 过期响应不算失败
grantManager.opts.after!.getData = (payload: { res?: ApiResponse; err?: any }) => {
    const err = payload.err
    if (!err || err instanceof StaleResponse || isCanceled(err)) return
    loadError.value = err?.data?.message || t('plugin.kb.library.grantLoadFailed')
    grantManager.table.data = []
    grantManager.table.total = 0
}

// 切换主体类型时清掉旧主体 id（避免把上一次选的主体 id 带进新主体）
const onKindChange = () => {
    form.subject_id = 0
}

const onAdd = async () => {
    const lib = props.library
    if (!lib) return
    if (form.subject_kind !== 'all' && !form.subject_id) {
        ElMessage.warning(t('plugin.kb.library.grantSubjectRequired'))
        return
    }
    adding.value = true
    try {
        await addKbGrant({
            library_id: lib.id,
            subject_kind: form.subject_kind,
            // 公开 = 主体 all 一行，subject_id 恒 0 哨兵（服务层同口径）
            subject_id: form.subject_kind === 'all' ? 0 : Number(form.subject_id),
            perm: form.perm,
        })
        // 服务端固定 id DESC 托底序：新授权行落第一页
        grantManager.table.filter!.page = 1
        await grantManager.getData()
    } catch {
        // 业务/网络错误已由请求拦截器统一提示（重复请求取消无需提示）
    } finally {
        adding.value = false
    }
}

const onRemove = async (row: KbGrantRow) => {
    const lib = props.library
    if (!lib) return
    removingId.value = row.id
    try {
        await removeKbGrant({ library_id: lib.id, grant_id: row.id })
        // 删后当页可能空：回退一页再取（空页不留在界面上）
        const filter = grantManager.table.filter!
        const page = filter.page || 1
        if ((grantManager.table.data || []).length === 1 && page > 1) {
            filter.page = page - 1
        }
        await grantManager.getData()
    } catch {
        // 业务/网络错误已由请求拦截器统一提示
    } finally {
        removingId.value = 0
    }
}

// 打开抽屉（或切换库）时重置并加载
watch(
    () => [visible.value, props.library?.id],
    ([open, id], old) => {
        const [oldOpen, oldId] = old || [false, undefined]
        if (!open) return
        if (oldOpen && id === oldId) return
        const filter = grantManager.table.filter!
        grantManager.table.data = []
        grantManager.table.total = 0
        filter.page = 1
        filter.limit = 20
        loadError.value = ''
        form.subject_kind = 'all'
        form.subject_id = 0
        form.perm = 'read'
        void grantManager.getData()
    },
    { immediate: true }
)
</script>

<style scoped lang="scss">
.kb-grant-panel {
    :deep(.el-drawer__header) {
        margin-bottom: 0;
        padding: 18px 20px 14px;
        border-bottom: 1px solid var(--el-border-color-lighter);
    }

    :deep(.el-drawer__body) {
        padding: 12px 20px 0;
    }

    :deep(.el-drawer__footer) {
        border-top: 1px solid var(--el-border-color-lighter);
        padding: 12px 20px;
    }
}

.drawer-head {
    min-width: 0;
}

.drawer-title {
    font-size: 18px;
    font-weight: 600;
    color: var(--el-text-color-primary);
    line-height: 26px;
}

.drawer-sub {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    margin-top: 4px;
}

.drawer-name {
    color: var(--el-text-color-secondary);
    font-size: 13px;
}

.drawer-owner {
    color: var(--el-text-color-placeholder);
    font-size: 12px;
}

.grant-form {
    padding: 12px 14px 4px;
    margin-bottom: 14px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: var(--el-border-radius-base);
    background-color: var(--el-fill-color-lighter);
}

.grant-form-row {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 0 14px;
}

.grant-form-item {
    margin-bottom: 10px;
}

.grant-form-item-kind {
    flex: none;
}

.grant-form-item-id {
    flex: none;
    width: 200px;
}

.grant-form-item-perm {
    flex: none;
}

.grant-form-item-submit {
    flex: none;
}

.form-hint {
    width: 100%;
    margin-bottom: 8px;
    color: var(--el-text-color-secondary);
    font-size: 12px;
    line-height: 18px;
}

.grant-alert {
    margin: 0 0 14px;
}
</style>
