<!-- plugin: ai v0.2.0 -->
<!--
  API Key 管理页（标准 CRUD 三件套 + 一次性明文回执 + 重置 / 启停）。
  契约：
    POST /admin/plugin/ai/apikey/list        → data={list,total}；行含 key_prefix（掩码），key_hash 永不外返
    GET  /admin/plugin/ai/apikey/get/:pk     → data={row}（source 固定不可改）
    POST /admin/plugin/ai/apikey/create      → data={id, api_key 明文}（明文仅此一次）
    POST /admin/plugin/ai/apikey/update/:pk  → 可改 name / model_scope / rate_limit / expire_at / remark
    POST /admin/plugin/ai/apikey/delete      → {pks}（宿主表格管家的批量删除契约）
    POST /admin/plugin/ai/apikey/reset/:pk   → data={id, api_key 明文}（旧 Key 立即失效）
    POST /admin/plugin/ai/apikey/switch      → {id, status}（目标状态用行自身的编码，见下）
  明文承载：apiKeyIssued（handler_apikey.go）返回 {id, api_key（明文）, key_prefix, source}；页面按
  data.api_key → data.key → data.row.api_key 依次兜底读取；除这段时间窗，任何出口都只有 key_prefix。
  status：APIKey.Status 是 int16 二值列（apikey.go:64，默认 1）；列表顶层 status 走 optionalStatus，
  只收 0/1（字符串会被可读拒绝「status 只能是 0（停用）或 1（启用）」）⇒ 页面显示、筛选与启停一律 1/0。
  source 最长 24 字符（apikey.go:61 的 varchar(24)，'key:'+source 要落在 usage.source 的 30 内）。
  expire_at / last_used_at 是 unix 秒（bigint，可空）；日期选择器用 value-format="X"（秒），不交给
  comSearch 的 datetime（那会发 'YYYY-MM-DD HH:mm:ss' 字符串，必然不命中）。
  权限节点：plugin/ai/apikey/{read,create,update,delete,reset,switch}。
-->
<template>
    <div class="default-main plugin-ai-apikey">
        <el-alert class="apikey-alert" type="info" :closable="false" show-icon :title="t('plugin.ai.apikey.intro')" />

        <div class="apikey-filters">
            <el-select v-model="preset.status" class="filter-select" clearable :placeholder="t('plugin.ai.apikey.filters.status')">
                <el-option :label="t('plugin.ai.apikey.status.on')" :value="1" />
                <el-option :label="t('plugin.ai.apikey.status.off')" :value="0" />
            </el-select>
            <el-button v-blur type="primary" @click="applyPreset">{{ t('plugin.ai.apikey.filters.query') }}</el-button>
            <el-button v-blur @click="resetPreset">{{ t('plugin.ai.apikey.filters.reset') }}</el-button>
        </div>

        <TableHeader
            :manager="tableManager"
            v-model:com-search="tableManager.comSearch"
            :buttons="['refresh', 'add', 'delete', 'comSearch', 'quickSearch', 'columnDisplay']"
            :quick-search-placeholder="t('plugin.ai.apikey.searchPlaceholder')"
        />

        <Table :manager="tableManager" :empty-text="t('plugin.ai.apikey.empty')" />

        <!-- 新增 / 编辑：就地独立弹窗（与模型页同写法的「本页未拆 dialogForm」形态） -->
        <el-dialog
            class="ag-operate-dialog"
            :close-on-click-modal="false"
            :model-value="['create', 'update'].includes(tableManager.form.operate!)"
            @close="tableManager.toggleForm"
            :destroy-on-close="true"
            :draggable="true"
        >
            <template #header>
                <div class="title">
                    {{ tableManager.form.operate === 'create' ? t('plugin.ai.apikey.form.add') : t('plugin.ai.apikey.form.edit') }}
                </div>
            </template>
            <el-scrollbar v-loading="tableManager.form.loading" class="ag-table-form-scrollbar">
                <div
                    class="ag-operate-form"
                    :class="'ag-' + tableManager.form.operate + '-form'"
                    :style="config.layout.shrink ? '' : 'width: calc(100% - ' + tableManager.form.labelWidth! / 2 + 'px)'"
                >
                    <el-form
                        ref="formRef"
                        @submit.prevent=""
                        @keyup.enter="tableManager.submitForm(formRef)"
                        :model="formItems"
                        :label-position="config.layout.shrink ? 'top' : 'right'"
                        :label-width="tableManager.form.labelWidth + 'px'"
                        :rules="rules"
                        v-if="!tableManager.form.loading"
                    >
                        <el-form-item :label="t('plugin.ai.apikey.form.name')" prop="name">
                            <el-input v-model="formItems.name" maxlength="50" :placeholder="t('plugin.ai.apikey.form.namePlaceholder')" />
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.apikey.form.source')" prop="source">
                            <el-input
                                v-model="formItems.source"
                                maxlength="24"
                                :disabled="tableManager.form.operate === 'update'"
                                :placeholder="t('plugin.ai.apikey.form.sourcePlaceholder')"
                            />
                            <div class="form-tip">
                                {{
                                    tableManager.form.operate === 'update'
                                        ? t('plugin.ai.apikey.form.sourceLocked')
                                        : t('plugin.ai.apikey.form.sourceTip')
                                }}
                            </div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.apikey.form.modelScope')" prop="model_scope_text">
                            <el-input
                                v-model="formItems.model_scope_text"
                                type="textarea"
                                :rows="4"
                                maxlength="2000"
                                show-word-limit
                                :placeholder="t('plugin.ai.apikey.form.modelScopePlaceholder')"
                            />
                            <div class="form-tip">{{ t('plugin.ai.apikey.form.modelScopeTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.apikey.form.rateLimit')" prop="rate_limit">
                            <el-input-number v-model="formItems.rate_limit" class="w100" :min="0" :precision="0" controls-position="right" />
                            <div class="form-tip">{{ t('plugin.ai.apikey.form.rateLimitTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.apikey.form.expireAt')" prop="expire_at">
                            <el-date-picker
                                v-model="formItems.expire_at"
                                class="w100"
                                type="datetime"
                                value-format="X"
                                clearable
                                :placeholder="t('plugin.ai.apikey.form.expireAt')"
                            />
                            <div class="form-tip">{{ t('plugin.ai.apikey.form.expireAtTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.apikey.form.remark')" prop="remark">
                            <el-input
                                @keyup.enter.stop=""
                                @keyup.ctrl.enter="tableManager.submitForm(formRef)"
                                v-model="formItems.remark"
                                type="textarea"
                                :rows="3"
                                maxlength="255"
                                show-word-limit
                            />
                            <div class="form-tip">{{ t('plugin.ai.apikey.form.statusTip') }}</div>
                        </el-form-item>
                    </el-form>
                </div>
            </el-scrollbar>
            <template #footer>
                <div class="dialog-footer">
                    <el-button v-blur @click="tableManager.toggleForm()">{{ t('common.cancel') }}</el-button>
                    <el-button v-blur :loading="tableManager.form.submitLoading" @click="tableManager.submitForm(formRef)" type="primary">
                        {{ t('common.save') }}
                    </el-button>
                </div>
            </template>
        </el-dialog>

        <!-- 一次性明文回执：关闭即不可再查（库里只有 sha256） -->
        <el-dialog v-model="secretVisible" :title="t('plugin.ai.apikey.secret.title')" width="min(560px, 96vw)" :close-on-click-modal="false">
            <el-alert class="apikey-alert" type="warning" :closable="false" show-icon :title="t('plugin.ai.apikey.secret.intro')" />
            <div class="secret-marked">{{ secretMarked }}</div>
            <el-form label-width="120px">
                <el-form-item :label="t('plugin.ai.apikey.secret.label')">
                    <el-input :model-value="secretValue" readonly>
                        <template #append>
                            <el-button v-blur @click="copySecret">{{ t('plugin.ai.apikey.secret.copy') }}</el-button>
                        </template>
                    </el-input>
                </el-form-item>
            </el-form>
            <template #footer>
                <el-button v-blur type="primary" @click="secretVisible = false">{{ t('plugin.ai.apikey.secret.close') }}</el-button>
            </template>
        </el-dialog>
    </div>
</template>

<script setup lang="ts">
import TableHeader from '@/components/table/header/index.vue'
import Table from '@/components/table/index.vue'
import { TableManagerAPI } from '@/api/table'
import { useTableManager } from '@/hooks/useTableManager'
import { useConfig } from '@/stores/config'
import { auth, copy, getCurrentRoutePath } from '@/utils/common'
import request from '@/utils/request'
import { dayjs, ElMessage } from 'element-plus'
import type { FormInstance, FormItemRule } from 'element-plus'
import { computed, onMounted, reactive, ref, useTemplateRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { buildValidatorRule } from '@/utils/validate'

defineOptions({
    name: 'plugin/ai/apikey',
})

const { t } = useI18n()
const config = useConfig()
const formRef = useTemplateRef<FormInstance>('formRef')

const pagePath = getCurrentRoutePath()
const can = (node: string) => auth({ name: pagePath, subNodeName: `${pagePath}/${node}` })

type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'

interface ApiKeyRow extends TableRow {
    id?: number
    name?: string
    source?: string
    key_prefix?: string
    model_scope?: string[] | string
    rate_limit?: number
    status?: string | number
    expire_at?: number | string | null
    last_used_at?: number | string | null
    remark?: string
    create_time?: number
    update_time?: number
}

// status 是 APIKey.Status 的 int16 二值列（handler_apikey.go 的 optionalStatus / apiKeySaveRequest.Status
// 都只收 0/1，字符串会被拒）⇒ 显示与筛选一律 1/0；下面是搞错编码时也不至于显示空白的兜底。
const ENABLED_VALUES = [1, '1', 'enabled', true]
const isEnabled = (value: unknown) => ENABLED_VALUES.includes(value as string | number | boolean)
const statusDict: Record<string, string> = {
    1: t('plugin.ai.apikey.status.on'),
    0: t('plugin.ai.apikey.status.off'),
}
const statusCustom: Record<string, TagType> = { 1: 'success', 0: 'info' }

const formatSeconds = (value?: number | string | null) => {
    const parsed = Number(value ?? 0)
    return parsed > 0 ? dayjs.unix(parsed).format('YYYY-MM-DD HH:mm:ss') : '-'
}
const scopeList = (value: unknown): string[] => {
    if (Array.isArray(value)) return value.map((item) => String(item)).filter((item) => item !== '')
    if (typeof value === 'string' && value.trim() !== '') {
        // 后端理论上回数组；字符串形态（JSON 或逗号串）一并兜住
        const text = value.trim()
        if (text.startsWith('[')) {
            try {
                const parsed = JSON.parse(text)
                return Array.isArray(parsed) ? parsed.map((item) => String(item)) : []
            } catch {
                return []
            }
        }
        return text.split(',').map((item) => item.trim()).filter((item) => item !== '')
    }
    return []
}
const scopeText = (value: unknown) => {
    const items = scopeList(value)
    return items.length ? items.join(', ') : t('plugin.ai.apikey.scope.all')
}
const rateLimitText = (value: unknown) => {
    const parsed = Number(value ?? 0)
    return parsed > 0 ? String(parsed) : t('plugin.ai.apikey.rateLimitFollow')
}
const expireText = (value?: number | string | null) => {
    const parsed = Number(value ?? 0)
    return parsed > 0 ? formatSeconds(parsed) : t('plugin.ai.apikey.neverExpires')
}

const tableManager = useTableManager({
    api: new TableManagerAPI('/admin/plugin/ai/apikey/'),
    table: {
        column: [
            { type: 'selection', align: 'center', operator: false },
            {
                label: t('plugin.ai.apikey.columns.name'),
                prop: 'name',
                align: 'left',
                minWidth: 160,
                operator: 'ILIKE',
                quickSearch: true,
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.apikey.columns.source'),
                prop: 'source',
                align: 'left',
                minWidth: 160,
                operator: 'ILIKE',
                quickSearch: true,
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.apikey.columns.keyPrefix'),
                prop: 'key_prefix',
                align: 'center',
                width: 150,
                operator: false,
                formatter: (row: TableRow) => row.key_prefix || '-',
            },
            {
                label: t('plugin.ai.apikey.columns.modelScope'),
                prop: 'model_scope',
                align: 'left',
                minWidth: 180,
                operator: false,
                showOverflowTooltip: true,
                formatter: (row: TableRow) => scopeText(row.model_scope),
            },
            {
                label: t('plugin.ai.apikey.columns.rateLimit'),
                prop: 'rate_limit',
                align: 'center',
                width: 130,
                operator: 'BETWEEN',
                formatter: (row: TableRow) => rateLimitText(row.rate_limit),
            },
            {
                label: t('plugin.ai.apikey.columns.status'),
                prop: 'status',
                align: 'center',
                width: 100,
                operator: 'eq',
                render: 'tag',
                custom: statusCustom,
                dict: statusDict,
            },
            {
                label: t('plugin.ai.apikey.columns.expireAt'),
                prop: 'expire_at',
                align: 'center',
                width: 170,
                operator: false,
                formatter: (row: TableRow) => expireText(row.expire_at),
            },
            {
                label: t('plugin.ai.apikey.columns.lastUsedAt'),
                prop: 'last_used_at',
                align: 'center',
                width: 170,
                operator: false,
                formatter: (row: TableRow) => formatSeconds(row.last_used_at),
                show: false,
            },
            {
                label: t('plugin.ai.apikey.columns.remark'),
                prop: 'remark',
                align: 'left',
                minWidth: 160,
                operator: 'ILIKE',
                showOverflowTooltip: true,
                show: false,
            },
            {
                label: t('plugin.ai.apikey.columns.createdAt'),
                prop: 'create_time',
                align: 'center',
                width: 170,
                operator: false,
                formatter: (row: TableRow) => formatSeconds(row.create_time),
                show: false,
            },
            {
                label: t('plugin.ai.apikey.columns.updatedAt'),
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
                width: 330,
                render: 'buttons',
                operator: false,
                buttons: [
                    {
                        render: 'tip',
                        name: 'edit',
                        title: t('plugin.ai.apikey.actions.edit'),
                        type: 'primary',
                        icon: 'lucide-pencil',
                        display: () => can('update'),
                        click: (row) => openEdit(row),
                    },
                    {
                        render: 'confirm',
                        name: 'reset',
                        title: t('plugin.ai.apikey.actions.reset'),
                        type: 'warning',
                        icon: 'lucide-key-round',
                        display: () => can('reset'),
                        popconfirm: (ctx) => ({ title: t('plugin.ai.apikey.actions.resetConfirm', { name: ctx.row?.name ?? '' }) }),
                        click: (row) => resetRow(row),
                    },
                    {
                        render: 'confirm',
                        name: 'toggle-status',
                        title: t('plugin.ai.apikey.actions.toggle'),
                        type: 'info',
                        icon: 'lucide-power',
                        display: () => can('switch'),
                        popconfirm: (ctx) => ({ title: t('plugin.ai.apikey.actions.toggleConfirm', { name: ctx.row?.name ?? '' }) }),
                        click: (row) => toggleStatus(row),
                    },
                    {
                        render: 'confirm',
                        name: 'delete',
                        title: t('plugin.ai.apikey.actions.delete'),
                        type: 'danger',
                        icon: 'lucide-trash',
                        display: () => can('delete'),
                        popconfirm: (ctx) => ({ title: t('plugin.ai.apikey.actions.deleteConfirm', { name: ctx.row?.name ?? '' }) }),
                        click: (row) => removeRow(row),
                    },
                ],
            },
        ],
        filter: {
            sort: 'id',
            order: 'desc',
        },
        dblClickNotEditColumn: ['all'],
    },
    form: {
        labelWidth: 150,
        defaultItems: {
            name: '',
            source: '',
            model_scope_text: '',
            rate_limit: 0,
            expire_at: '',
            remark: '',
        },
    },
})

// 表单字段直连 manager.form.items（与模型页同写法：computed 持有同一 reactive 对象，
// v-model 写的是它上面的属性，不替换对象本身）
const formItems = computed<AnyObj>(() => tableManager.form.items ?? {})

// 编辑回填：只保留可编辑字段；scope 数组 → 文本；expire_at 秒 → 日期选择器可读的字符串
tableManager.opts.after!.getEditData = () => {
    const row = tableManager.form.items ?? {}
    tableManager.form.items = {
        id: row.id,
        name: row.name ?? '',
        source: row.source ?? '',
        model_scope_text: scopeList(row.model_scope).join('\n'),
        rate_limit: Number(row.rate_limit ?? 0),
        expire_at: Number(row.expire_at ?? 0) > 0 ? String(row.expire_at) : '',
        remark: row.remark ?? '',
    }
}

// 提交前归一：模型白名单文本 → 字符串数组；过期时间 → unix 秒（空 = 不过期）。
// 同时记录本次操作类型：submitForm 的 after 钩子在 toggleForm() 之后才跑，那时 form.operate 已被清空。
const submittedOperate = ref('')
tableManager.opts.before!.submitForm = ({ operate, items }: { operate?: string; items: AnyObj }) => {
    submittedOperate.value = String(operate ?? '')
    if (!items) return
    const scope = String(items.model_scope_text ?? '')
        .split(/[\n,，]/)
        .map((item) => item.trim())
        .filter((item) => item !== '')
    items.model_scope = Array.from(new Set(scope))
    delete items.model_scope_text
    const expire = Number(items.expire_at ?? 0)
    items.expire_at = Number.isFinite(expire) && expire > 0 ? Math.trunc(expire) : 0
    items.rate_limit = Math.max(0, Math.trunc(Number(items.rate_limit ?? 0)) || 0)
}

/* ---------------- 一次性明文回执 ---------------- */
const secretVisible = ref(false)
const secretValue = ref('')
const secretMarked = ref('')

const openSecret = (payload: unknown, marked: string) => {
    const data = (payload ?? {}) as AnyObj
    const row = (data.row ?? {}) as AnyObj
    const plain = data.api_key ?? data.key ?? data.plain_key ?? row.api_key ?? ''
    if (typeof plain !== 'string' || plain === '') return
    secretValue.value = plain
    secretMarked.value = marked
    secretVisible.value = true
}

const copySecret = async () => {
    const ok = await copy(secretValue.value)
    if (ok) {
        ElMessage.success(t('plugin.ai.apikey.secret.copied'))
    } else {
        ElMessage.warning(t('plugin.ai.apikey.secret.copyFailed'))
    }
}

// create 的回执（明文）由 submitForm 钩子读取；update 不回明文，弹窗不出现
tableManager.opts.after!.submitForm = ({ res }: { res: ApiResponse }) => {
    if (submittedOperate.value !== 'create') return
    const marked = t('plugin.ai.apikey.secret.createMarked', { name: String(formItems.value.name ?? '') })
    openSecret(res?.data?.data, marked)
}

/* ---------------- 顶部显式筛选（后端顶层 status，与 wheres AND 合并） ---------------- */
const preset = reactive<{ status: string }>({ status: '' })

const applyPreset = () => {
    const filter = tableManager.table.filter!
    if (!preset.status) {
        delete filter.status
    } else {
        filter.status = preset.status
    }
    filter.page = 1
    tableManager.getData()
}

const resetPreset = () => {
    preset.status = ''
    applyPreset()
}

/* ---------------- 行内动作 ---------------- */
const openEdit = (row: TableRow) => {
    tableManager.toggleForm('update', [String(row.id)])
}

const removeRow = async (row: TableRow) => {
    try {
        await tableManager.api.delete([String(row.id)])
        tableManager.getData()
    } catch {
        // 请求层已提示；保持列表不变
    }
}

const resetRow = async (row: TableRow) => {
    try {
        const res = await request<AnyObj>(
            { url: `/admin/plugin/ai/apikey/reset/${Number(row.id)}`, method: 'POST', data: {} },
            { cancelDuplicate: false }
        )
        const marked = t('plugin.ai.apikey.secret.resetMarked', { name: String(row.name ?? '') })
        openSecret(res.data.data, marked)
        tableManager.getData()
    } catch {
        // 请求层已提示
    }
}

// 目标状态 1/0（apiKeyActionRequest.Status 只收 0/1）；已是启用则发 0 停用，反之发 1 启用
const toggleStatus = async (row: TableRow) => {
    const target = isEnabled(row.status) ? 0 : 1
    try {
        await request(
            {
                url: '/admin/plugin/ai/apikey/switch',
                method: 'POST',
                data: { id: Number(row.id), status: target },
            },
            { showSuccessMessage: true }
        )
        tableManager.getData()
    } catch {
        // 请求层已提示
    }
}

const rules = reactive<Partial<Record<string, FormItemRule[]>>>({
    name: [buildValidatorRule({ name: 'required', title: t('plugin.ai.apikey.form.name') })],
    source: [
        buildValidatorRule({ name: 'required', title: t('plugin.ai.apikey.form.source') }),
        {
            validator: (_rule: unknown, value: string, callback: (error?: Error) => void) => {
                if (!value) {
                    callback()
                    return
                }
                if (!/^[a-zA-Z0-9_:.-]{1,24}$/.test(value)) {
                    callback(new Error(t('plugin.ai.apikey.rules.sourceInvalid')))
                    return
                }
                callback()
            },
            trigger: 'blur',
        },
    ],
})

onMounted(() => {
    tableManager.initCtx()
    tableManager.getData()
})
</script>

<style scoped lang="scss">
.plugin-ai-apikey {
    .apikey-alert {
        margin-bottom: 10px;
    }

    .apikey-filters {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
        margin-bottom: 10px;
    }

    .filter-select {
        width: 170px;
    }

    .form-tip {
        width: 100%;
        margin-top: 2px;
        font-size: 12px;
        line-height: 18px;
        color: var(--el-text-color-secondary);
    }

    .dialog-footer {
        display: flex;
        justify-content: flex-end;
        gap: 8px;
    }

    .secret-marked {
        margin: 12px 0 8px;
        font-weight: 600;
    }
}
</style>