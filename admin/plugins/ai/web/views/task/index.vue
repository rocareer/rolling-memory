<!-- plugin: ai v0.2.0 -->
<!--
  异步任务页（列表 + 提交 + 详情 + 导出）。
  契约（照已落地的 handler_task.go 逐字对齐）：
    POST /admin/plugin/ai/task/list → data={list,total}；行 = Task + {model, messages_count,
         inputs_count, summary, entries}（payload/result/locked_until 是 json:"-" ⇒ 列表行里没有）
    GET  /admin/plugin/ai/task/get/:pk → data={row, payload, result, entries, usage, usage_request_ids,
         usage_rows}（**payload/result 在 data 顶层，不在 row 里**）
    POST /admin/plugin/ai/task/submit → data={id,task_type,status,request_id,progress_total,created}
    GET  /admin/plugin/ai/task/export/:pk → application/x-ndjson 流（服务端不落盘）
  提交入参（taskSubmitRequest）：{task_type, model, messages|inputs（两套互斥）, biz_type 与 source（都必填，
  ValidateCallContext 与同步路径同一份校验；各最长 30 字符）, request_id（空 = 后端生成）}。
  列表筛选：顶层只认 status / task_type；keyword（表格右上角搜索框，经 quickSearchKeywords 传）命中
  request_id / source / biz_type / 模型名 ⇒ 列上不再挂 quickSearch，避免叠加 wheres 造成「交集为空」的假阴性。
  进度：progress_done / progress_total 是行上的真列（不是在 result 里）。
  导出：走原始 axios（宿主 request 的响应拦截器按 body.code 判定，二进制流会被它当错误吞掉），
  响应为 JSON 壳时按错误文案提示——httpx 恒 HTTP 200，业务码在 body.code。
  权限节点：plugin/ai/task/{read,submit,export}（list|get 由 read 回落覆盖）。
-->
<template>
    <div class="default-main plugin-ai-task">
        <el-alert class="task-alert" type="info" :closable="false" show-icon :title="t('plugin.ai.task.intro')" />

        <TableHeader
            :manager="tableManager"
            v-model:com-search="tableManager.comSearch"
            :buttons="['refresh', 'comSearch', 'quickSearch', 'columnDisplay']"
            :quick-search-placeholder="t('plugin.ai.task.searchPlaceholder')"
        >
            <template #refreshAppend>
                <el-button v-if="canSubmit" v-blur class="btns-ml-12" type="primary" @click="openSubmit">
                    <Icon name="lucide-plus" :size="16" :stroke-width="3" color="#ffffff" />
                    <span class="btn-text">{{ t('plugin.ai.task.submit.open') }}</span>
                </el-button>
            </template>
        </TableHeader>

        <div class="task-filters">
            <el-select v-model="preset.task_type" class="filter-select" clearable :placeholder="t('plugin.ai.task.filters.taskType')">
                <el-option :label="t('plugin.ai.task.taskType.chat')" value="chat" />
                <el-option :label="t('plugin.ai.task.taskType.embedding')" value="embedding" />
            </el-select>
            <el-select v-model="preset.status" class="filter-select" clearable :placeholder="t('plugin.ai.task.filters.status')">
                <el-option :label="t('plugin.ai.task.status.pending')" value="pending" />
                <el-option :label="t('plugin.ai.task.status.processing')" value="processing" />
                <el-option :label="t('plugin.ai.task.status.success')" value="success" />
                <el-option :label="t('plugin.ai.task.status.failed')" value="failed" />
            </el-select>
            <el-input
                v-model="preset.keyword"
                class="filter-input"
                clearable
                :placeholder="t('plugin.ai.task.filters.keyword')"
                @keyup.enter="applyPreset"
            />
            <el-button v-blur type="primary" @click="applyPreset">{{ t('plugin.ai.task.filters.query') }}</el-button>
            <el-button v-blur @click="resetPreset">{{ t('plugin.ai.task.filters.reset') }}</el-button>
        </div>

        <Table :manager="tableManager" :empty-text="t('plugin.ai.task.empty')" />

        <!-- 提交任务 -->
        <el-dialog v-model="submitVisible" :title="t('plugin.ai.task.submit.title')" width="min(680px, 96vw)" :close-on-click-modal="false">
            <el-form :model="submitForm" label-width="140px">
                <el-form-item :label="t('plugin.ai.task.submit.taskType')">
                    <el-radio-group v-model="submitForm.task_type">
                        <el-radio value="chat" :border="true">{{ t('plugin.ai.task.taskType.chat') }}</el-radio>
                        <el-radio value="embedding" :border="true">{{ t('plugin.ai.task.taskType.embedding') }}</el-radio>
                    </el-radio-group>
                </el-form-item>
                <el-form-item :label="t('plugin.ai.task.submit.model')">
                    <el-input v-model="submitForm.model" maxlength="100" :placeholder="t('plugin.ai.task.submit.modelPlaceholder')" />
                </el-form-item>
                <el-form-item :label="t('plugin.ai.task.submit.bizType')">
                    <el-input v-model="submitForm.biz_type" maxlength="30" :placeholder="t('plugin.ai.task.submit.bizTypePlaceholder')" />
                </el-form-item>
                <el-form-item :label="t('plugin.ai.task.submit.source')">
                    <el-input v-model="submitForm.source" maxlength="30" :placeholder="t('plugin.ai.task.submit.sourcePlaceholder')" />
                    <div class="form-tip">{{ t('plugin.ai.task.submit.contextTip') }}</div>
                </el-form-item>
                <el-form-item v-if="submitForm.task_type === 'chat'" :label="t('plugin.ai.task.submit.chatTitle')">
                    <el-input v-model="submitForm.chat_text" type="textarea" :rows="6" :placeholder="t('plugin.ai.task.submit.chatPlaceholder')" />
                    <div class="form-tip">{{ t('plugin.ai.task.submit.chatTip') }}</div>
                </el-form-item>
                <el-form-item v-else :label="t('plugin.ai.task.submit.embeddingTitle')">
                    <el-input
                        v-model="submitForm.embedding_text"
                        type="textarea"
                        :rows="8"
                        :placeholder="t('plugin.ai.task.submit.embeddingPlaceholder')"
                    />
                    <div class="form-tip">{{ t('plugin.ai.task.submit.embeddingTip') }}</div>
                    <div class="form-tip">{{ t('plugin.ai.task.submit.count', { count: embedInputs.length }) }}</div>
                </el-form-item>
            </el-form>
            <template #footer>
                <el-button v-blur @click="submitVisible = false">{{ t('common.cancel') }}</el-button>
                <el-button v-blur type="primary" :loading="submitting" @click="submitTask">{{ t('plugin.ai.task.submit.submit') }}</el-button>
            </template>
        </el-dialog>

        <!-- 任务详情：payload / result 原文 + 条目数 + 累计用量（进度是行上的 progress_done/total 真列） -->
        <el-dialog v-model="detailVisible" :title="detailTitle" width="min(860px, 96vw)" :destroy-on-close="true">
            <div v-loading="detailLoading" class="task-detail">
                <template v-if="detailRow">
                    <el-descriptions :column="2" border size="small" class="detail-meta">
                        <el-descriptions-item :label="t('plugin.ai.task.columns.taskType')">{{ taskTypeLabel(detailRow.task_type) }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.task.columns.status')">{{ statusLabel(detailRow.status) }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.task.columns.model')">{{ detailModel || '-' }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.task.columns.source')">{{ detailRow.source || '-' }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.task.columns.bizType')">{{ detailRow.biz_type || '-' }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.task.columns.requestId')">
                            <span class="mono">{{ detailRow.request_id || '-' }}</span>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.task.columns.attempt')">{{ detailRow.attempt ?? '-' }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.task.columns.progress')">{{ progressText(detailRow) }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.task.detail.entries')">{{ detailEntries ?? '-' }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.ai.task.detail.usage')">
                            {{
                                t('plugin.ai.task.detail.usageValue', {
                                    calls: Number(detailUsage.calls ?? 0),
                                    tokens: Number(detailUsage.total_tokens ?? 0),
                                    cost: String(detailUsage.cost ?? '-'),
                                })
                            }}
                        </el-descriptions-item>
                    </el-descriptions>
                    <div v-if="detailRow.error" class="detail-error">{{ detailRow.error }}</div>
                    <div class="detail-title">{{ t('plugin.ai.task.detail.payload') }}</div>
                    <pre class="detail-json">{{ prettyJson(detailPayload) }}</pre>
                    <div class="detail-title">{{ t('plugin.ai.task.detail.result') }}</div>
                    <pre class="detail-json">{{ prettyJson(detailResult) }}</pre>
                </template>
                <el-empty v-else :description="t('common.noData')" :image-size="50" />
            </div>
            <template #footer>
                <el-button @click="detailVisible = false">{{ t('common.close') }}</el-button>
            </template>
        </el-dialog>
    </div>
</template>

<script setup lang="ts">
import TableHeader from '@/components/table/header/index.vue'
import Table from '@/components/table/index.vue'
import { TableManagerAPI } from '@/api/table'
import { useTableManager } from '@/hooks/useTableManager'
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { useAdminInfo } from '@/stores/adminInfo'
import { auth, getCurrentRoutePath } from '@/utils/common'
import request, { getBaseURL } from '@/utils/request'
import axios from 'axios'
import { dayjs, ElMessage } from 'element-plus'
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'

defineOptions({
    name: 'plugin/ai/task',
})

const { t } = useI18n()

const pagePath = getCurrentRoutePath()
const canSubmit = auth({ name: pagePath, subNodeName: `${pagePath}/submit` })
const canExport = auth({ name: pagePath, subNodeName: `${pagePath}/export` })

type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'

interface TaskRow extends TableRow {
    id?: number
    task_type?: string
    status?: string
    error?: string
    source?: string
    biz_type?: string
    request_id?: string
    attempt?: number
    progress_done?: number
    progress_total?: number
    // 列表行附加段（handler_task.go 的 taskRow：内嵌 Task + 下列五项）
    model?: string
    messages_count?: number
    inputs_count?: number
    summary?: string
    entries?: number
    create_time?: number
    update_time?: number
}

const taskTypeDict: Record<string, string> = {
    chat: t('plugin.ai.task.taskType.chat'),
    embedding: t('plugin.ai.task.taskType.embedding'),
}
const taskTypeCustom: Record<string, TagType> = { chat: 'primary', embedding: 'success' }
const statusDict: Record<string, string> = {
    pending: t('plugin.ai.task.status.pending'),
    processing: t('plugin.ai.task.status.processing'),
    success: t('plugin.ai.task.status.success'),
    failed: t('plugin.ai.task.status.failed'),
}
const statusCustom: Record<string, TagType> = { pending: 'info', processing: 'warning', success: 'success', failed: 'danger' }

const taskTypeLabel = (value?: string) => (value ? (taskTypeDict[value] ?? value) : '-')
const statusLabel = (value?: string) => (value ? (statusDict[value] ?? value) : '-')
const formatSeconds = (value?: number | null) => (value ? dayjs.unix(Number(value)).format('YYYY-MM-DD HH:mm:ss') : '-')

// jsonb 列可能回对象，也可能回 JSON 字符串：两种都兜住（展示用）
const asObject = (value: unknown): AnyObj => {
    if (value && typeof value === 'object' && !Array.isArray(value)) return value as AnyObj
    if (typeof value === 'string' && value.trim().startsWith('{')) {
        try {
            const parsed = JSON.parse(value)
            return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {}
        } catch {
            return {}
        }
    }
    return {}
}
const prettyJson = (value: unknown) => {
    if (value === null || value === undefined || value === '') return '-'
    if (typeof value === 'string') {
        try {
            return JSON.stringify(JSON.parse(value), null, 2)
        } catch {
            return value
        }
    }
    return JSON.stringify(value, null, 2)
}

// 分片进度：progress_done / progress_total 是行上的真列（表定义见 task.go:118-119）
const progressOf = (row: TableRow): { done: number; total: number } => {
    const done = Number(row.progress_done ?? 0)
    const total = Number(row.progress_total ?? 0)
    return {
        done: Number.isFinite(done) ? done : 0,
        total: Number.isFinite(total) ? total : 0,
    }
}
const progressText = (row: TableRow) => {
    const { done, total } = progressOf(row)
    if (total <= 0) return t('plugin.ai.task.progress.none')
    return t('plugin.ai.task.progress.value', { done, total })
}
// 模型名由 taskRow.Model 直出（列表行里没有 payload 列，后端已解析好）
const modelOf = (row: TableRow) => row.model || '-'

const tableManager = useTableManager({
    api: new TableManagerAPI('/admin/plugin/ai/task/'),
    table: {
        column: [
            { type: 'selection', align: 'center', operator: false },
            { label: t('plugin.ai.task.columns.id'), prop: 'id', align: 'center', width: 100, operator: 'BETWEEN' },
            {
                label: t('plugin.ai.task.columns.taskType'),
                prop: 'task_type',
                align: 'center',
                width: 110,
                operator: 'eq',
                render: 'tag',
                custom: taskTypeCustom,
                dict: taskTypeDict,
            },
            {
                label: t('plugin.ai.task.columns.status'),
                prop: 'status',
                align: 'center',
                width: 110,
                operator: 'eq',
                render: 'tag',
                custom: statusCustom,
                dict: statusDict,
            },
            {
                label: t('plugin.ai.task.columns.model'),
                prop: 'model',
                align: 'left',
                minWidth: 150,
                operator: false,
                showOverflowTooltip: true,
                formatter: (row: TableRow) => modelOf(row),
            },
            {
                label: t('plugin.ai.task.columns.progress'),
                prop: 'progress_total',
                align: 'center',
                width: 130,
                operator: 'BETWEEN',
                formatter: (row: TableRow) => progressText(row),
            },
            {
                label: t('plugin.ai.task.columns.summary'),
                prop: 'summary',
                align: 'left',
                minWidth: 180,
                operator: false,
                showOverflowTooltip: true,
                formatter: (row: TableRow) => row.summary || '-',
            },
            {
                label: t('plugin.ai.task.columns.source'),
                prop: 'source',
                align: 'center',
                width: 120,
                operator: 'ILIKE',
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.task.columns.bizType'),
                prop: 'biz_type',
                align: 'center',
                width: 110,
                operator: 'ILIKE',
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.task.columns.requestId'),
                prop: 'request_id',
                align: 'left',
                minWidth: 200,
                operator: 'ILIKE',
                showOverflowTooltip: true,
            },
            { label: t('plugin.ai.task.columns.attempt'), prop: 'attempt', align: 'center', width: 90, operator: 'BETWEEN' },
            {
                label: t('plugin.ai.task.columns.error'),
                prop: 'error',
                align: 'left',
                minWidth: 220,
                operator: 'ILIKE',
                showOverflowTooltip: true,
                show: false,
            },
            {
                label: t('plugin.ai.task.columns.createdAt'),
                prop: 'create_time',
                align: 'center',
                width: 170,
                operator: false,
                formatter: (row: TableRow) => formatSeconds(row.create_time),
                show: false,
            },
            {
                label: t('plugin.ai.task.columns.updatedAt'),
                prop: 'update_time',
                align: 'center',
                width: 170,
                operator: false,
                formatter: (row: TableRow) => formatSeconds(row.update_time),
                show: false,
            },
            {
                label: t('common.operate'),
                align: 'center',
                width: 180,
                render: 'buttons',
                operator: false,
                buttons: [
                    {
                        render: 'tip',
                        name: 'detail',
                        title: t('plugin.ai.task.actions.detail'),
                        type: 'info',
                        icon: 'lucide-list-tree',
                        click: (row) => openDetail(row),
                    },
                    {
                        render: 'tip',
                        name: 'export',
                        title: t('plugin.ai.task.actions.exportHint'),
                        type: 'primary',
                        icon: 'lucide-download',
                        display: () => canExport,
                        loading: (row) => exporting.has(Number(row.id)),
                        click: (row) => exportRow(row),
                    },
                ],
            },
        ],
        filter: {
            sort: 'id',
            order: 'desc',
            limit: 20,
        },
        dblClickNotEditColumn: ['all'],
    },
    form: {
        labelWidth: 160,
    },
})

/* ---------------- 顶部显式筛选（后端顶层 task_type/status + keyword，与 wheres AND 合并） ---------------- */
const preset = reactive<{ task_type: string; status: string; keyword: string }>({ task_type: '', status: '', keyword: '' })

const applyPreset = () => {
    const filter = tableManager.table.filter!
    for (const key of ['task_type', 'status', 'keyword'] as const) {
        const value = String(preset[key] ?? '').trim()
        if (!value) {
            delete filter[key]
        } else {
            filter[key] = value
        }
    }
    filter.page = 1
    tableManager.getData()
}

const resetPreset = () => {
    preset.task_type = ''
    preset.status = ''
    preset.keyword = ''
    applyPreset()
}

/* ---------------- 提交任务 ---------------- */
const submitVisible = ref(false)
const submitting = ref(false)
const submitForm = reactive({
    task_type: 'chat',
    model: '',
    biz_type: '',
    source: '',
    chat_text: '',
    embedding_text: '',
})

const openSubmit = () => {
    submitForm.task_type = 'chat'
    submitForm.model = ''
    submitForm.biz_type = ''
    submitForm.source = ''
    submitForm.chat_text = ''
    submitForm.embedding_text = ''
    submitVisible.value = true
}

// 对话文本：每行 role:content（无冒号的行按 user 消息处理）
const parseChatMessages = (text: string) =>
    text
        .split('\n')
        .map((line) => line.trim())
        .filter((line) => line !== '')
        .map((line) => {
            const index = line.indexOf(':')
            if (index <= 0) return { role: 'user', content: line }
            return { role: line.slice(0, index).trim() || 'user', content: line.slice(index + 1).trim() }
        })

const embedInputs = computed(() =>
    submitForm.embedding_text
        .split('\n')
        .map((line) => line.trim())
        .filter((line) => line !== '')
)

const submitTask = async () => {
    if (!submitForm.model.trim() || !submitForm.biz_type.trim() || !submitForm.source.trim()) {
        ElMessage.warning(t('plugin.ai.task.submit.empty'))
        return
    }
    const isChat = submitForm.task_type === 'chat'
    const messages = parseChatMessages(submitForm.chat_text)
    const inputs = embedInputs.value
    if (isChat && messages.length === 0) {
        ElMessage.warning(t('plugin.ai.task.submit.empty'))
        return
    }
    if (!isChat && inputs.length === 0) {
        ElMessage.warning(t('plugin.ai.task.submit.empty'))
        return
    }
    // 入参照 taskSubmitRequest：messages/inputs 互斥、biz_type 与 source 都必填、request_id 空 = 服务端生成
    const body: AnyObj = {
        task_type: submitForm.task_type,
        model: submitForm.model.trim(),
        biz_type: submitForm.biz_type.trim(),
        source: submitForm.source.trim(),
        request_id: '',
    }
    if (isChat) {
        body.messages = messages
    } else {
        body.inputs = inputs
    }
    submitting.value = true
    try {
        // 提交是普通 JSON 往返 ⇒ 走宿主 request（错误码与提示由拦截器统一处理）
        const res = await request<AnyObj>(
            { url: '/admin/plugin/ai/task/submit', method: 'POST', data: body },
            { cancelDuplicate: false }
        )
        const payload = res.data.data ?? {}
        // submit 出参是扁平形状（data={id,task_type,status,request_id,progress_total,created}）
        const row = (payload.row ?? payload) as AnyObj
        ElMessage.success(
            t('plugin.ai.task.submit.success', {
                id: String(row.id ?? '-'),
                status: statusLabel(String(row.status ?? 'pending')),
            })
        )
        submitVisible.value = false
        tableManager.getData()
    } catch {
        // 请求层已提示（含 403 / code!=0）；保留弹窗供修正
    } finally {
        submitting.value = false
    }
}

/* ---------------- 详情 ---------------- */
const detailVisible = ref(false)
const detailLoading = ref(false)
const detailRow = ref<TaskRow | null>(null)
const detailPayload = ref<unknown>(null)
const detailResult = ref<unknown>(null)
const detailEntries = ref<number | null>(null)
const detailUsage = ref<AnyObj>({})
const detailModel = ref('')
const detailTitle = computed(() => t('plugin.ai.task.detail.title', { id: String(detailRow.value?.id ?? '-') }))

const openDetail = async (row: TableRow) => {
    detailRow.value = row as TaskRow
    detailPayload.value = null
    detailResult.value = null
    detailEntries.value = null
    detailUsage.value = {}
    detailModel.value = modelOf(row)
    detailVisible.value = true
    detailLoading.value = true
    try {
        const res = await tableManager.api.get(String(row.id))
        const data = (res.data.data ?? {}) as AnyObj
        if (data.row) detailRow.value = data.row as TaskRow
        // payload/result 在 data 顶层（row 里是 json:"-"），照 handler_task.go 的 taskGet 出口读
        detailPayload.value = data.payload ?? null
        detailResult.value = data.result ?? null
        detailEntries.value = data.entries === undefined || data.entries === null ? null : Number(data.entries)
        detailUsage.value = (data.usage ?? {}) as AnyObj
        const payloadModel = asObject(data.payload).model
        if (typeof payloadModel === 'string' && payloadModel !== '') detailModel.value = payloadModel
    } catch {
        // 请求层已提示；保留列表行里的快照
    } finally {
        detailLoading.value = false
    }
}

/* ---------------- 导出（NDJSON 流） ---------------- */
const exporting = reactive(new Set<number>())

// 宿主 request 的响应拦截器按 body.code 判定成败，二进制流会被它当业务错误吞掉 ⇒ 导出走原始 axios
const adminPrefixed = (url: string) => (adminBaseRoutePath !== '/admin' ? url.replace(/^\/admin\//, `${adminBaseRoutePath}/`) : url)

const saveBlob = (blob: Blob, filename: string) => {
    const objectUrl = window.URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = objectUrl
    link.download = filename
    document.body.appendChild(link)
    link.click()
    document.body.removeChild(link)
    window.URL.revokeObjectURL(objectUrl)
}

const exportRow = async (row: TableRow) => {
    const id = Number(row.id)
    if (exporting.has(id)) return
    exporting.add(id)
    try {
        const res = await axios.get(adminPrefixed(`/admin/plugin/ai/task/export/${id}`), {
            baseURL: getBaseURL(),
            responseType: 'blob',
            timeout: 60000,
            headers: { Authorization: `Bearer ${useAdminInfo().token}` },
        })
        const blob = res.data as Blob
        // httpx 恒 HTTP 200，拒绝写的是 JSON 壳 ⇒ blob 是 JSON 时按错误文案提示，不下载错误体
        if (blob.type.includes('json')) {
            const text = await blob.text()
            let message = t('plugin.ai.task.export.failed')
            try {
                message = JSON.parse(text)?.message || message
            } catch {
                message = text || message
            }
            ElMessage.error(message)
            return
        }
        saveBlob(blob, `plugin-ai-task-${id}.ndjson`)
        ElMessage.success(t('plugin.ai.task.export.success'))
    } catch (error) {
        const status = (error as AnyObj)?.response?.status
        const message = (error as AnyObj)?.response?.data?.message ?? (error as Error)?.message ?? t('plugin.ai.task.export.failed')
        ElMessage.error(status ? `${message}（HTTP ${status}）` : String(message))
    } finally {
        exporting.delete(id)
    }
}

onMounted(() => {
    tableManager.initCtx()
    tableManager.getData()
})
</script>

<style scoped lang="scss">
.plugin-ai-task {
    .task-alert {
        margin-bottom: 10px;
    }

    .task-filters {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
        margin: 10px 0;
    }

    .filter-select {
        width: 160px;
    }

    .filter-input {
        width: 190px;
    }

    .btn-text {
        margin-left: 4px;
    }

    .form-tip {
        width: 100%;
        margin-top: 2px;
        font-size: 12px;
        line-height: 18px;
        color: var(--el-text-color-secondary);
    }

    .task-detail {
        min-height: 160px;
    }

    .detail-meta {
        margin-bottom: 12px;
    }

    .detail-error {
        margin-bottom: 12px;
        font-size: 12px;
        color: var(--el-color-danger);
        word-break: break-all;
    }

    .detail-title {
        margin: 10px 0 6px;
        font-weight: 600;
    }

    .detail-json {
        max-height: 260px;
        margin: 0;
        padding: 10px;
        overflow: auto;
        font-size: 12px;
        line-height: 18px;
        background: var(--el-fill-color-light);
        border-radius: 6px;
        white-space: pre-wrap;
        word-break: break-all;
    }

    .mono {
        font-family: var(--el-font-family-mono, monospace);
        font-size: 12px;
    }
}
</style>