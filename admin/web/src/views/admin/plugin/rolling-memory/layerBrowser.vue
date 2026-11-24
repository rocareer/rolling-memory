<!-- plugin: rolling-memory v0.0.58 -->
<template>
    <div class="rm-ba-table-wrap">
        <TableHeader
            :manager="tableManager"
            v-model:com-search="tableManager.comSearch"
            :buttons="headerButtons"
            :quick-search-placeholder="t('plugin.rolling-memory.filters.searchPlaceholder')"
        >
            <template #refreshAppend>
                <slot name="toolbarAppend"></slot>
            </template>

            <template #quickSearchPrepend>
                <slot name="searchPrepend"></slot>
                <el-radio-group
                    v-if="props.layer === 'all'"
                    v-model="subLayerFilter"
                    size="small"
                    class="header-filter-group"
                    data-testid="browser-layer-filter"
                    @change="onPresetChange"
                >
                    <el-radio-button value="all">{{ t('plugin.rolling-memory.filters.all', { n: lastStats.total_units || 0 }) }}</el-radio-button>
                    <el-radio-button value="background">L1 ({{ lastStats.background_count || 0 }})</el-radio-button>
                    <el-radio-button value="episodic">L2 ({{ lastStats.episodic_count || 0 }})</el-radio-button>
                    <el-radio-button value="noetic">L3 ({{ lastStats.noetic_active_count || 0 }})</el-radio-button>
                </el-radio-group>

                <el-radio-group
                    v-if="props.showStatus"
                    v-model="statusFilter"
                    size="small"
                    class="header-filter-group"
                    data-testid="browser-status-filter"
                    @change="onPresetChange"
                >
                    <el-radio-button value="">{{ t('plugin.rolling-memory.browser.statusAll') }}</el-radio-button>
                    <el-radio-button value="active"
                        >{{ t('plugin.rolling-memory.browser.statusActive') }} ({{ lastStats.noetic_active_count || 0 }})</el-radio-button
                    >
                    <el-radio-button value="stale"
                        >{{ t('plugin.rolling-memory.browser.statusStale') }} ({{ lastStats.noetic_stale_count || 0 }})</el-radio-button
                    >
                </el-radio-group>
            </template>
        </TableHeader>

        <Table
            ref="tableRef"
            :manager="tableManager"
            :pagination="true"
            :empty-text="emptyText"
            :element-loading-text="deletingUnits ? deletingLoadingText : undefined"
        >
            <!-- 自定义内容列（区分 L3 Q&A 结晶与 L1/L2 明文证据，悬停展示富格式 Markdown 预览） -->
            <template #contentSlot>
                <el-table-column :label="t('plugin.rolling-memory.filters.colContent')" prop="content" min-width="280">
                    <template #default="{ row }">
                        <MemoryHoverPreview
                            :text="row.layer === 'noetic' ? row.answer_text || row.content : row.content"
                            :question="row.layer === 'noetic' ? row.title : ''"
                            :title="row.rel_path ? `${row.rel_path}#${row.anchor || '0'}` : row.id"
                            :subtitle="row.time_text || ''"
                            :hint="{ layer: row.layer, relPath: row.rel_path }"
                        >
                            <div v-if="row.layer === 'noetic'" class="qa-cell" @click="openInfoDialog(row)">
                                <div class="qa-line qa-q">
                                    <el-tag size="small" type="warning" effect="plain" class="qa-badge">Q</el-tag>
                                    {{ row.title }}
                                    <el-tag
                                        v-if="row.adoption_outcome === 'adopted_tool' || row.adoption_outcome === 'adopted_context'"
                                        size="small"
                                        :type="row.adoption_outcome === 'adopted_tool' ? 'success' : 'primary'"
                                        effect="plain"
                                        style="margin-left: 6px"
                                    >
                                        {{
                                            row.adoption_outcome === 'adopted_tool'
                                                ? String(locale).toLowerCase().startsWith('en')
                                                    ? '🎯 Tool-input Match'
                                                    : '🎯 工具输入匹配'
                                                : String(locale).toLowerCase().startsWith('en')
                                                  ? '✅ Reply Match'
                                                  : '✅ 回复匹配'
                                        }}
                                    </el-tag>
                                    <span
                                        v-if="row.assistant_id && row.assistant_id !== 'default'"
                                        style="margin-left: 6px; font-size: 11px; color: var(--el-color-primary)"
                                    >
                                        · {{ row.assistant_id }}
                                    </span>
                                </div>
                                <div class="qa-line qa-a">
                                    <el-tag size="small" type="success" effect="plain" class="qa-badge">A</el-tag>
                                    {{ row.answer_text || '-' }}
                                </div>
                            </div>
                            <div v-else class="content-cell" @click="openInfoDialog(row)">
                                <span v-if="row.rel_path" class="rel-badge">{{ row.rel_path }}#{{ row.anchor }}</span>
                                <span>{{ row.content }}</span>
                            </div>
                        </MemoryHoverPreview>
                    </template>
                </el-table-column>
            </template>
        </Table>

        <!-- BuildAdmin 标准查看详情弹窗 (.ag-operate-dialog) -->
        <el-dialog class="ag-operate-dialog" v-model="infoVisible" width="min(780px, 95vw)" destroy-on-close>
            <template #header>
                <div class="title" style="display: flex; align-items: center; justify-content: space-between; padding-right: 28px">
                    <span>{{ t('plugin.rolling-memory.manage.drawerTitle') }} · {{ infoRow?.id || '' }}</span>
                    <div style="display: flex; align-items: center; gap: 8px">
                        <el-button
                            v-if="infoHistory.length > 0"
                            size="small"
                            plain
                            data-testid="btn-dialog-back-unit"
                            @click="popInfoHistory"
                        >
                            ← {{ String(locale).toLowerCase().startsWith('en') ? 'Back' : '返回上一条' }}
                        </el-button>
                        <el-button
                            v-if="infoRow?.layer === 'noetic'"
                            type="success"
                            size="small"
                            plain
                            data-testid="btn-dialog-replay-noetic"
                            @click="
                                infoVisible = false;
                                replayInDebugger(infoRow);
                            "
                        >
                            {{ t('plugin.rolling-memory.noetic.replayBtn') }}
                        </el-button>
                        <el-popconfirm
                            v-if="infoRow?.id && tableManager.auth('delete')"
                            :title="t('common.deleteSelectedRecords')"
                            :confirm-button-text="t('common.delete')"
                            :cancel-button-text="t('common.cancel')"
                            confirm-button-type="danger"
                            @confirm="executeDeleteUnits([infoRow.id])"
                        >
                            <template #reference>
                                <el-button
                                    type="danger"
                                    size="small"
                                    plain
                                    :loading="deletingUnits"
                                    data-testid="btn-dialog-delete-unit"
                                >
                                    {{ t('common.delete') }}
                                </el-button>
                            </template>
                        </el-popconfirm>
                    </div>
                </div>
            </template>
            <el-scrollbar v-loading="unitLoading" :element-loading-text="unitLoadingText" class="ag-table-form-scrollbar">
                <div v-if="infoRow" class="ag-operate-form ag-info-form">
                    <el-descriptions :column="2" border size="small">
                        <el-descriptions-item :label="t('plugin.rolling-memory.filters.colUnit')">
                            <code>{{ infoRow.id }}</code>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.rolling-memory.filters.colLayer')">
                            <MemoryLayerBadge :layer="infoRow.layer" />
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.rolling-memory.browser.colStatus')">
                            <el-tag :type="infoRow.status === 'active' ? 'success' : 'info'" size="small">
                                {{ infoRow.status }}
                            </el-tag>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.rolling-memory.filters.colTime')">
                            {{ infoRow.time_text || '-' }}
                        </el-descriptions-item>
                        <el-descriptions-item v-if="infoRow.address_uri" :span="2" :label="t('plugin.rolling-memory.filters.cellAddress')">
                            <code>{{ infoRow.address_uri }}</code>
                        </el-descriptions-item>
                        <el-descriptions-item
                            v-if="infoRow.layer === 'noetic' && infoRow.adoption_outcome"
                            :span="2"
                            :label="String(locale).toLowerCase().startsWith('en') ? 'Evidence Reuse Audit' : '事后证据复用审计'"
                        >
                            <div style="display: flex; align-items: center; flex-wrap: wrap; gap: 6px">
                                <el-tag
                                    size="small"
                                    :type="
                                        infoRow.adoption_outcome === 'adopted_tool'
                                            ? 'success'
                                            : infoRow.adoption_outcome === 'adopted_context'
                                              ? 'primary'
                                              : 'info'
                                    "
                                    effect="plain"
                                >
                                    {{
                                        infoRow.adoption_outcome === 'adopted_tool'
                                            ? String(locale).toLowerCase().startsWith('en')
                                                ? '🎯 Tool-input Match'
                                                : '🎯 工具输入匹配'
                                            : infoRow.adoption_outcome === 'adopted_context'
                                              ? String(locale).toLowerCase().startsWith('en')
                                                  ? '✅ Reply Match'
                                                  : '✅ 回复匹配'
                                              : infoRow.adoption_outcome === 'unadopted'
                                                ? String(locale).toLowerCase().startsWith('en')
                                                    ? 'No Match'
                                                    : '未匹配'
                                                : infoRow.adoption_outcome === 'pending'
                                                  ? String(locale).toLowerCase().startsWith('en')
                                                      ? 'Pending'
                                                      : '待结算'
                                                  : infoRow.adoption_outcome
                                    }}
                                </el-tag>
                                <span v-if="infoRow.assistant_id && infoRow.assistant_id !== 'default'" style="font-size: 12px; color: var(--el-color-primary)">
                                    ({{ infoRow.assistant_id }})
                                </span>
                                <el-tag
                                    v-for="(ma, idx) in infoRow.matched_anchors || []"
                                    :key="idx"
                                    size="small"
                                    type="success"
                                    effect="plain"
                                >
                                    <code>{{ ma }}</code>
                                </el-tag>
                            </div>
                        </el-descriptions-item>
                        <el-descriptions-item v-if="infoRow.rel_path" :span="2" :label="t('plugin.rolling-memory.browser.colFileChunk')">
                            <code>{{ infoRow.rel_path }}#{{ infoRow.anchor }}</code>
                        </el-descriptions-item>
                        <el-descriptions-item
                            v-if="infoRow.cited_unit_ids?.length"
                            :span="2"
                            :label="t('plugin.rolling-memory.filters.citedHandles')"
                        >
                            <div class="cited-tags">
                                <el-button v-for="cid in infoRow.cited_unit_ids" :key="cid" link type="primary" size="small" @click="openUnit(cid)">
                                    {{ cid }}
                                </el-button>
                            </div>
                        </el-descriptions-item>
                    </el-descriptions>

                    <el-alert
                        v-if="infoRow.layer === 'background' && infoRow.rel_path && infoRow.body_unavailable"
                        type="info"
                        :closable="false"
                        show-icon
                        :title="
                            String(locale).toLowerCase().startsWith('en')
                                ? 'The chunk text is currently unavailable (the source file may have been moved or deleted, or the local reading engine is not ready). Only the source location is shown below.'
                                : '当前无法读取该分块的原始正文（源文件可能已被移动或删除，或本地读取引擎暂未就绪）。下方只显示来源位置。'
                        "
                    >
                        <el-button
                            link
                            type="primary"
                            size="small"
                            :loading="unitLoading"
                            data-testid="btn-dialog-reload-body"
                            @click="openUnit(infoRow.id)"
                        >
                            {{ t('plugin.rolling-memory.browser.reloadBody') }}
                        </el-button>
                    </el-alert>
                    <template v-if="infoRow.content">
                        <div class="info-sec-title">{{ t('plugin.rolling-memory.browser.contentTitle') }}</div>
                        <MarkdownPreview :text="infoRow.content" :hint="{ layer: infoRow.layer, relPath: infoRow.rel_path }" />
                    </template>
                </div>
            </el-scrollbar>
        </el-dialog>
    </div>
</template>

<script setup lang="ts">
import { TableManagerAPI } from '@/api/table'
import MemoryLayerBadge from '@/components/memory/MemoryLayerBadge.vue'
import TableHeader from '@/components/table/header/index.vue'
import Table from '@/components/table/index.vue'
import { useTableManager } from '@/hooks/useTableManager'
import { auth } from '@/utils/common'
import { useLoadingProgressText } from '@/utils/loadingProgress'
import request from '@/utils/request'
import { ElMessage } from 'element-plus'
import { computed, markRaw, onActivated, onDeactivated, onMounted, ref, useTemplateRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import MarkdownPreview from './MarkdownPreview.vue'
import MemoryHoverPreview from './MemoryHoverPreview.vue'

const props = withDefaults(
    defineProps<{
        layer?: 'all' | 'background' | 'episodic' | 'noetic'
        dirId?: string
        relPath?: string
        showStatus?: boolean
        limit?: number
        title?: string
    }>(),
    {
        layer: 'all',
        dirId: '',
        relPath: '',
        showStatus: false,
        limit: 20,
        title: '',
    }
)

const emit = defineEmits<{
    (e: 'loaded', payload: any): void
    (e: 'deleted', payload: { pks: string[]; deleted: number }): void
}>()

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const tableRef = useTemplateRef('tableRef')

const subLayerFilter = ref<string>('all')
const statusFilter = ref<string>('')
const lastStats = ref<any>({})
// 列表请求失败（超时 / 后端未响应）时给出可区分的空态提示：与「确实没有数据」分开，
// 否则用户看到的「暂无数据」会把加载失败说成库是空的。
const loadFailed = ref(false)
const emptyText = computed(() => {
    if (loadFailed.value) return t('plugin.rolling-memory.browser.emptyLoadFailed')
    // 关键词检索的空结果与「库本身为空」分开表述，避免把「没有匹配」读成「没有记忆」。
    // 框架快速搜索把输入落在 `filter.quickSearchKeywords`（字符串），`keyword` 是其它调用面的等价字段。
    const keyword = String(tableManager.table.filter?.quickSearchKeywords || tableManager.table.filter?.keyword || '').trim()
    if (keyword) return t('plugin.rolling-memory.browser.emptySearch')
    return t('plugin.rolling-memory.browser.empty')
})

const infoVisible = ref(false)
const infoRow = ref<any>(null)
const infoHistory = ref<any[]>([])
const unitLoading = ref(false)
const deletingUnits = ref(false)
const { text: unitLoadingText } = useLoadingProgressText(unitLoading)
const { text: deletingLoadingText } = useLoadingProgressText(
    deletingUnits,
    () => t('plugin.rolling-memory.browser.deletingLoading'),
    900
)

const headerButtons: TableHeaderOptButton[] = ['refresh', 'delete', 'comSearch', 'quickSearch', 'columnDisplay']

let detailRequest = 0
const openInfoDialog = (row: any) => {
    if (!row) return
    detailRequest++
    unitLoading.value = false
    infoHistory.value = []
    infoRow.value = row
    infoVisible.value = true
    // L1 背景分块：先用列表行即时开窗，再向 /layers/get 取一次带分块正文的规范详情
    // （列表批量预热未覆盖、或读取引擎刚就绪时由这里补上新鲜正文）。
    if (row.layer === 'background' && row.rel_path) {
        void openUnit(row.id)
    }
}

const popInfoHistory = () => {
    if (!infoHistory.value.length) return
    detailRequest++
    unitLoading.value = false
    infoRow.value = infoHistory.value.pop()
}

const openUnit = async (id: string): Promise<boolean> => {
    const requestID = ++detailRequest
    const prevRow = infoVisible.value ? infoRow.value : null
    infoVisible.value = true
    unitLoading.value = true
    const cleanID = id.replace(/^unit:/, '')
    try {
        const res = await request<{ row: any }>(
            {
                url: '/admin/plugin/rolling-memory/layers/get',
                method: 'POST',
                data: { id: cleanID },
            },
            { cancelDuplicate: false, showErrorMessage: !prevRow }
        )
        if (requestID === detailRequest && res.data.data?.row) {
            if (prevRow && prevRow.id !== res.data.data.row.id) {
                infoHistory.value.push(prevRow)
            }
            infoRow.value = res.data.data.row
            infoVisible.value = true
            return true
        }
        if (requestID === detailRequest && prevRow) {
            ElMessage.warning(
                String(locale.value).toLowerCase().startsWith('en')
                    ? `Cited source unit (${cleanID}) has been deleted or is no longer in the vault.`
                    : `该引证的底层记忆单元（${cleanID}）已被删除或不在当前金库中。`
            )
        }
        return false
    } catch {
        if (requestID === detailRequest) {
            if (!prevRow) {
                infoVisible.value = false
            } else {
                ElMessage.warning(
                    String(locale.value).toLowerCase().startsWith('en')
                        ? `Cited source unit (${cleanID}) has been deleted or is no longer in the vault.`
                        : `该引证的底层记忆单元（${cleanID}）已被删除或不在当前金库中。`
                )
            }
        }
        return false
    } finally {
        if (requestID === detailRequest) {
            unitLoading.value = false
        }
    }
}

// 正文面统一走 MarkdownPreview 的自动格式化（结构化内容自动包代码围栏），不再区分默认展示面。
const layerDict: Record<string, string> = {
    background: t('plugin.rolling-memory.layers.background'),
    episodic: t('plugin.rolling-memory.layers.episodic'),
    noetic: t('plugin.rolling-memory.layers.noetic'),
}

const statusDict: Record<string, string> = {
    active: 'Active',
    stale: 'Stale',
}
const statusCustom: Record<string, string> = {
    active: 'success',
    stale: 'info',
}

// L3 行的线索与工作区锚交给调试台，按当前参数重新查询。
const replayInDebugger = (row: TableRow) => {
    const cue = String(row.title || '').trim()
    if (!cue) return
    const anchored = cue.match(/^\s*\[([^[\]\r\n]{1,64})\]\s*/)
    router.push({
        path: '/admin/plugin/rolling-memory/recall',
        query: anchored ? { q: cue.slice(anchored[0].length).trim(), workspace: anchored[1].trim() } : { q: cue },
    })
}

const optButtons: OptButton[] = [
    {
        render: 'tip',
        name: 'replay',
        title: String(locale.value).toLowerCase().startsWith('en') ? 'Query in debugger with current settings' : '按当前参数在调试台重新查询',
        text: '',
        type: 'success',
        icon: 'lucide-search',
        class: 'table-row-replay',
        display: (row: TableRow) => row.layer === 'noetic',
        click: (row: TableRow) => replayInDebugger(row),
    },
    {
        render: 'tip',
        name: 'info',
        title: t('plugin.rolling-memory.manage.detailBtn'),
        text: '',
        type: 'primary',
        icon: 'lucide-eye',
        class: 'table-row-info',
        click: (row: TableRow) => openInfoDialog(row),
    },
    {
        render: 'confirm',
        name: 'delete',
        title: t('common.delete'),
        text: '',
        type: 'danger',
        icon: 'lucide-trash',
        class: 'table-row-delete',
        popconfirm: {
            confirmButtonText: t('common.delete'),
            cancelButtonText: t('common.cancel'),
            confirmButtonType: 'danger',
            title: t('common.deleteSelectedRecords'),
        },
    },
]

const tableManager = useTableManager({
    auth: (action: string) => {
        // layers/read 与 layers/delete 权限节点挂在 index 菜单下（authNode 的 key 是该菜单路径），
        // 不存在 /admin/plugin/rolling-memory/layers 这个菜单节点，误用会让行内操作按钮恒不渲染。
        if (action === 'delete') {
            return auth({ name: '/admin/plugin/rolling-memory/index', subNodeName: '/admin/plugin/rolling-memory/layers/delete' })
        }
        return auth({ name: '/admin/plugin/rolling-memory/index', subNodeName: '/admin/plugin/rolling-memory/layers/read' })
    },
    api: new TableManagerAPI('/admin/plugin/rolling-memory/layers/'),
    table: {
        pk: 'id',
        column: [
            { type: 'selection', align: 'center', operator: false, width: 48 },
            {
                label: t('plugin.rolling-memory.filters.colLayer'),
                prop: 'layer',
                align: 'center',
                width: 125,
                render: 'customRender',
                customRender: markRaw(MemoryLayerBadge),
                operator: props.layer === 'all' ? 'eq' : false,
                comSearchRender: 'select',
                dict: layerDict,
                sortable: 'custom',
                show: props.layer === 'all',
            },
            {
                label: t('plugin.rolling-memory.filters.colUnit'),
                prop: 'id',
                align: 'left',
                width: 180,
                operator: 'ILIKE',
                quickSearch: true,
                sortable: 'custom',
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.rolling-memory.browser.colStatus'),
                prop: 'status',
                align: 'center',
                width: 96,
                render: 'tag',
                operator: 'eq',
                comSearchRender: 'select',
                dict: statusDict,
                custom: statusCustom,
                sortable: 'custom',
                show: props.layer === 'noetic',
            },
            {
                label: t('plugin.rolling-memory.filters.colContent'),
                prop: 'content',
                render: 'slot',
                slotName: 'contentSlot',
                operator: 'ILIKE',
                quickSearch: true,
            },
            {
                label: t('plugin.rolling-memory.browser.colFileChunk'),
                prop: 'rel_path',
                align: 'left',
                width: 195,
                operator: props.layer === 'background' ? 'ILIKE' : false,
                quickSearch: true,
                sortable: 'custom',
                showOverflowTooltip: true,
                show: props.layer === 'background',
                formatter: (row: TableRow) => (row.rel_path ? `${row.rel_path}#${row.anchor || '0'}` : row.address_uri || '-'),
            },
            {
                label: t('plugin.rolling-memory.filters.colAddr'),
                prop: 'address_uri',
                align: 'left',
                width: 210,
                operator: 'ILIKE',
                quickSearch: true,
                sortable: 'custom',
                showOverflowTooltip: true,
                formatter: (row: TableRow) => {
                    if (row.layer === 'noetic' && Array.isArray(row.cited_unit_ids) && row.cited_unit_ids.length) {
                        return `${t('plugin.rolling-memory.filters.citedPrefix')} ${row.cited_unit_ids.join(', ')}`
                    }
                    return row.address_uri || '-'
                },
            },
            {
                label: t('plugin.rolling-memory.filters.colTime'),
                prop: 'time_text',
                align: 'center',
                width: 165,
                sortable: 'custom',
                operator: 'BETWEEN',
                comSearchRender: 'datetime',
            },
            {
                label: t('common.operate'),
                align: 'center',
                width: 130,
                render: 'buttons',
                buttons: optButtons,
                operator: false,
            },
        ],
        filter: {
            page: 1,
            limit: props.limit || 20,
            sort: 'occurred_at',
            order: 'desc',
        },
        dblClickNotEditColumn: ['all'],
    },
})

const syncFilterProps = () => {
    const f = tableManager.table.filter!
    if (props.layer === 'all') {
        f.layer = subLayerFilter.value || 'all'
    } else {
        f.layer = props.layer
    }
    if (props.showStatus && statusFilter.value) {
        f.status = statusFilter.value
    } else {
        delete f.status
    }
    if (props.dirId) {
        f.dir_id = props.dirId
    } else {
        delete f.dir_id
    }
    if (props.relPath) {
        f.rel_path = props.relPath
    } else {
        delete f.rel_path
    }
}

tableManager.opts.before!.getData = () => {
    syncFilterProps()
}

tableManager.opts.after!.getData = ({ res }: { res?: ApiResponse }) => {
    // 只有真正拿到载荷才更新读数：请求失败/超时（res 为空）时保留上一次的计数，
    // 不要把「统计」清成 0 或空 —— 否则页面看起来像「库是空的」，与事实不符。
    const payload = res?.data?.data
    if (!payload) {
        loadFailed.value = true
        return
    }
    loadFailed.value = false
    lastStats.value = payload
    emit('loaded', payload)
}

tableManager.opts.before!.columnDblclick = ({ row }) => {
    openInfoDialog(row)
    return false
}

const executeDeleteUnits = async (pks: string[]) => {
    const cleanPKs = (pks || []).map((s) => String(s || '').trim()).filter(Boolean)
    if (!cleanPKs.length || deletingUnits.value) return
    deletingUnits.value = true
    tableManager.table.loading = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/layers/delete',
            method: 'POST',
            data: { pks: cleanPKs },
            timeout: 0,
        })
        const deletedCount = Number(res.data?.data?.deleted ?? cleanPKs.length)
        ElMessage.success(t('plugin.rolling-memory.browser.deleteSuccess', { count: deletedCount }))
        if (infoVisible.value && infoRow.value?.id && cleanPKs.includes(String(infoRow.value.id))) {
            infoVisible.value = false
        }
        tableManager.table.selections = []
        await tableManager.getData()
        emit('deleted', { pks: cleanPKs, deleted: deletedCount })
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        deletingUnits.value = false
        tableManager.table.loading = false
    }
}

tableManager.opts.before!.delete = ({ pks }: { pks: string[] }) => {
    void executeDeleteUnits(pks)
    return false
}

const onPresetChange = () => {
    tableManager.table.filter!.page = 1
    tableManager.getData()
}

watch(locale, () => {
    layerDict.background = t('plugin.rolling-memory.layers.background')
    layerDict.episodic = t('plugin.rolling-memory.layers.episodic')
    layerDict.noetic = t('plugin.rolling-memory.layers.noetic')
    const isEnNow = String(locale.value).toLowerCase().startsWith('en')
    if (optButtons[0]) optButtons[0].title = isEnNow ? 'Query in debugger with current settings' : '按当前参数在调试台重新查询'
    if (optButtons[1]) optButtons[1].title = t('plugin.rolling-memory.manage.detailBtn')
    if (optButtons[2]) optButtons[2].title = t('common.delete')
    const cols = tableManager.table.column
    if (cols[1]) cols[1].label = t('plugin.rolling-memory.filters.colLayer')
    if (cols[2]) cols[2].label = t('plugin.rolling-memory.filters.colUnit')
    if (cols[3]) cols[3].label = t('plugin.rolling-memory.browser.colStatus')
    if (cols[4]) cols[4].label = t('plugin.rolling-memory.filters.colContent')
    if (cols[5]) cols[5].label = t('plugin.rolling-memory.browser.colFileChunk')
    if (cols[6]) cols[6].label = t('plugin.rolling-memory.filters.colAddr')
    if (cols[7]) cols[7].label = t('plugin.rolling-memory.filters.colTime')
    if (cols[8]) cols[8].label = t('common.operate')
})

const reload = () => {
    tableManager.getData()
}

const setKeyword = (value: string) => {
    tableManager.table.filter!.keyword = value
    tableManager.table.filter!.page = 1
    tableManager.getData()
}

watch(
    () => [props.layer, props.dirId, props.relPath, props.limit],
    () => {
        tableManager.table.filter!.page = 1
        tableManager.getData()
    }
)

// 本实例已自动开窗过的深链键（unit_id + ref）：keep-alive 的标签页切换与路由过渡都会
// 反复触发 onActivated/onMounted，只有键变化才自动开窗，避免来回切页反复弹窗。
let appliedUnitKey = ''

const applyUnitQuery = () => {
    if (props.layer !== 'all' || !route.path.endsWith('/plugin/rolling-memory/index')) return
    if (typeof route.query.unit_id !== 'string' || !route.query.unit_id.trim()) return
    const id = route.query.unit_id.trim()
    // 深链键带上 ref：检索调试台每次「详情溯源」跳转都会附一个新的 ref 时间戳，同一单元
    // 再点一次也能重新开窗；而标签页切走再回来（键不变）不会重复弹窗。地址栏参数不做剥离，
    // 因为建标签页时应用会先挂一个瞬时实例再换成持久实例，剥离会被瞬时实例抢先执行、
    // 持久实例激活时已无参可放（弹窗永远开不出来）；键去重让两个实例各自取件，互不依赖。
    const key = `${id}|${String(route.query.ref || '')}`
    if (key === appliedUnitKey) return
    appliedUnitKey = key
    void openUnit(id)
}

let wasDeactivated = false
watch(
    () => [route.path, route.query.unit_id],
    () => {
        if (!wasDeactivated) applyUnitQuery()
    }
)

onMounted(() => {
    tableManager.table.ref = tableRef.value
    tableManager.initCtx()
    tableManager.getData()
    applyUnitQuery()
})

onActivated(() => {
    if (!wasDeactivated) return
    wasDeactivated = false
    applyUnitQuery()
})

onDeactivated(() => {
    // 只记录停用标记，作废在飞请求交由 openUnit / openInfoDialog 自身的序号自增完成：
    // 深链过渡会在标签页落位时把瞬时实例换成持久实例，若这里也自增序号，
    // 深链在飞的取件响应会被误判为过期而丢弃，弹窗永远开不出来。
    wasDeactivated = true
})

defineExpose({ reload, setKeyword, tableManager })
</script>

<style scoped lang="scss">
.rm-ba-table-wrap {
    margin-top: 0;
}
.info-sec-title {
    margin: 12px 0 8px;
    font-size: 13px;
    font-weight: 600;
}
.header-filter-group {
    margin-right: 10px;
}
.content-cell {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    font-size: 12px;
    line-height: 1.5;
    cursor: pointer;
}
.rel-badge {
    display: inline-block;
    margin-right: 6px;
    padding: 0 5px;
    border-radius: 3px;
    background: var(--el-color-primary-light-9);
    color: var(--el-color-primary);
    font-family: monospace;
    font-size: 11px;
}
.qa-cell {
    display: flex;
    flex-direction: column;
    gap: 3px;
    cursor: pointer;
}
.qa-line {
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: flex;
    align-items: center;
    gap: 6px;
}
.qa-badge {
    height: 18px;
    padding: 0 5px;
    font-size: 10px;
    font-weight: 700;
}
.qa-q {
    font-weight: 600;
    color: var(--el-text-color-primary);
}
.qa-a {
    color: var(--el-text-color-secondary);
}
.cited-tags {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
}
</style>
