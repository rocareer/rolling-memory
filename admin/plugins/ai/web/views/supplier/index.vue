<!-- plugin: ai v0.2.0 -->
<!--
  厂商管理页（标准 CRUD 三件套）。
  契约：
    POST /admin/plugin/ai/supplier/list       → data={list,total}；行含 channel_count（引用渠道数）
    GET  /admin/plugin/ai/supplier/get/:pk    → data={row}（api_key 为掩码）
    POST /admin/plugin/ai/supplier/create     → data={id}
    POST /admin/plugin/ai/supplier/update/:pk → 可改全部字段（api_key 空串或含 * 不回写）
    POST /admin/plugin/ai/supplier/delete     → {pks}；仍有渠道引用（supplier_id>0）时后端拒绝并给可读文案
  继承语义：渠道的 base_url/api_key/driver/timeout 留空时继承厂商值（渠道显式填写时以渠道为准），
  所以厂商页的删除保护不是装饰：删掉继承来源会让渠道拿到空凭证。
  status 编码：插件自身的二值列口径（Supplier.Status int16，supplier.go:62），值域 0/1；
  筛选与表单一律发 0/1（列表顶层 status 走 optionalStatus，只收 0/1，字符串会被可读拒绝）。
  字段名与库列名一致（name/vendor/base_url/api_key/driver/timeout/status/remark/create_time/update_time），
  高级检索按 handler 的 wheres 白名单校验；channel_count 是聚合列，不参与筛选。
  权限节点：plugin/ai/supplier/{read,create,update,delete}。
-->
<template>
    <div class="default-main plugin-ai-supplier">
        <el-alert class="supplier-alert" type="info" :closable="false" show-icon :title="t('plugin.ai.supplier.intro')" />

        <div class="supplier-filters">
            <el-select v-model="preset.status" class="filter-select" clearable :placeholder="t('plugin.ai.supplier.filters.status')">
                <el-option v-for="item in statusOptions" :key="String(item.value)" :label="item.label" :value="item.value" />
            </el-select>
            <el-button v-blur type="primary" @click="applyPreset">{{ t('plugin.ai.supplier.filters.query') }}</el-button>
            <el-button v-blur @click="resetPreset">{{ t('plugin.ai.supplier.filters.reset') }}</el-button>
        </div>

        <TableHeader
            :manager="tableManager"
            v-model:com-search="tableManager.comSearch"
            :buttons="['refresh', 'add', 'delete', 'comSearch', 'quickSearch', 'columnDisplay']"
            :quick-search-placeholder="t('plugin.ai.supplier.searchPlaceholder')"
        />

        <Table :manager="tableManager" :empty-text="t('plugin.ai.supplier.empty')" />

        <!-- 新增 / 编辑：就地独立弹窗 -->
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
                    {{
                        tableManager.form.operate === 'create'
                            ? t('plugin.ai.supplier.form.add')
                            : t('plugin.ai.supplier.form.edit')
                    }}
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
                        <el-form-item :label="t('plugin.ai.supplier.form.name')" prop="name">
                            <el-input v-model="formItems.name" maxlength="50" :placeholder="t('plugin.ai.supplier.form.namePlaceholder')" />
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.supplier.form.vendor')" prop="vendor">
                            <el-select v-model="formItems.vendor" class="w100" filterable allow-create default-first-option :reserve-keyword="false">
                                <el-option v-for="item in vendorOptions" :key="item.value" :label="item.label" :value="item.value" />
                            </el-select>
                            <div class="form-tip">{{ t('plugin.ai.supplier.form.vendorTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.supplier.form.driver')" prop="driver">
                            <el-select v-model="formItems.driver" class="w100" filterable allow-create default-first-option :reserve-keyword="false">
                                <el-option v-for="item in driverOptions" :key="item.value" :label="item.label" :value="item.value" />
                            </el-select>
                            <div class="form-tip">{{ t('plugin.ai.supplier.form.driverTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.supplier.form.baseUrl')" prop="base_url">
                            <el-input v-model="formItems.base_url" maxlength="255" :placeholder="t('plugin.ai.supplier.form.baseUrlPlaceholder')" />
                            <div class="form-tip">{{ t('plugin.ai.supplier.form.baseUrlTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.supplier.form.apiKey')" prop="api_key">
                            <el-input
                                v-model="formItems.api_key"
                                type="password"
                                show-password
                                autocomplete="new-password"
                                maxlength="255"
                                :placeholder="t('plugin.ai.supplier.form.apiKeyPlaceholder')"
                            />
                            <div class="form-tip">{{ t('plugin.ai.supplier.form.apiKeyTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.supplier.form.timeout')" prop="timeout">
                            <el-input-number v-model="formItems.timeout" class="w100" :min="0" :max="600" :precision="0" controls-position="right" />
                            <div class="form-tip">{{ t('plugin.ai.supplier.form.timeoutTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.supplier.form.status')" prop="status">
                            <el-radio-group v-model="formItems.status">
                                <el-radio v-for="item in statusOptions" :key="String(item.value)" :value="item.value" :border="true">
                                    {{ item.label }}
                                </el-radio>
                            </el-radio-group>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.supplier.form.remark')" prop="remark">
                            <el-input
                                @keyup.enter.stop=""
                                @keyup.ctrl.enter="tableManager.submitForm(formRef)"
                                v-model="formItems.remark"
                                type="textarea"
                                :rows="3"
                                maxlength="255"
                                show-word-limit
                            />
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
    </div>
</template>

<script setup lang="ts">
import TableHeader from '@/components/table/header/index.vue'
import Table from '@/components/table/index.vue'
import { TableManagerAPI } from '@/api/table'
import { useTableManager } from '@/hooks/useTableManager'
import { useConfig } from '@/stores/config'
import { auth, getCurrentRoutePath } from '@/utils/common'
import { buildValidatorRule } from '@/utils/validate'
import { dayjs } from 'element-plus'
import type { FormInstance, FormItemRule } from 'element-plus'
import { computed, onMounted, reactive, useTemplateRef } from 'vue'
import { useI18n } from 'vue-i18n'

defineOptions({
    name: 'plugin/ai/supplier',
})

const { t } = useI18n()
const config = useConfig()
const formRef = useTemplateRef<FormInstance>('formRef')

const pagePath = getCurrentRoutePath()
const canNode = (node: string) => auth({ name: pagePath, subNodeName: `${pagePath}/${node}` })

type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'

const statusDict: Record<string, string> = {
    1: t('plugin.ai.supplier.status.on'),
    0: t('plugin.ai.supplier.status.off'),
}
const statusCustom: Record<string, TagType> = { 1: 'success', 0: 'info' }

// status 是 Supplier.Status 的 int16 二值列（supplier.go:62 `json:"status"`、supplierListRequest.Status
// 走 optionalStatus 只收 0/1）⇒ 筛选与表单一律 1/0，不发字符串（发了会被可读拒绝）。
const statusOptions = [
    { value: 1, label: t('plugin.ai.supplier.status.on') },
    { value: 0, label: t('plugin.ai.supplier.status.off') },
]

const formatSeconds = (value?: number | null) => (value ? dayjs.unix(Number(value)).format('YYYY-MM-DD HH:mm:ss') : '-')

const DRIVER_CANDIDATES = ['openai', 'responses', 'deepseek', 'dashscope', 'dashscope_tts']
const driverOptions = computed(() => [
    { value: '', label: t('plugin.ai.channel.driver.inherit') },
    ...DRIVER_CANDIDATES.map((value) => ({ value, label: t(`plugin.ai.channel.driver.${value}`) })),
])
const vendorOptions = computed(() =>
    ['openai', 'deepseek', 'qwen', 'moonshot', 'zhipu', 'ollama', 'other'].map((value) => ({
        value,
        label: t(`plugin.ai.channel.vendor.${value}`),
    }))
)

const tableManager = useTableManager({
    api: new TableManagerAPI('/admin/plugin/ai/supplier/'),
    table: {
        column: [
            { type: 'selection', align: 'center', operator: false },
            {
                label: t('plugin.ai.supplier.columns.name'),
                prop: 'name',
                align: 'left',
                minWidth: 160,
                operator: 'ILIKE',
                quickSearch: true,
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.supplier.columns.vendor'),
                prop: 'vendor',
                align: 'center',
                width: 120,
                operator: 'ILIKE',
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.supplier.columns.driver'),
                prop: 'driver',
                align: 'center',
                width: 140,
                operator: 'eq',
                formatter: (row: TableRow) => row.driver || t('plugin.ai.channel.driver.inherit'),
            },
            {
                label: t('plugin.ai.supplier.columns.baseUrl'),
                prop: 'base_url',
                align: 'left',
                minWidth: 220,
                operator: 'ILIKE',
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.supplier.columns.apiKey'),
                prop: 'api_key',
                align: 'center',
                width: 170,
                operator: false,
                showOverflowTooltip: true,
                formatter: (row: TableRow) => row.api_key || '-',
            },
            { label: t('plugin.ai.supplier.columns.timeout'), prop: 'timeout', align: 'center', width: 110, operator: 'BETWEEN' },
            {
                label: t('plugin.ai.supplier.columns.channelCount'),
                prop: 'channel_count',
                align: 'center',
                width: 110,
                operator: false,
                formatter: (row: TableRow) => Number(row.channel_count ?? 0),
            },
            {
                label: t('plugin.ai.supplier.columns.status'),
                prop: 'status',
                align: 'center',
                width: 100,
                operator: 'eq',
                render: 'tag',
                custom: statusCustom,
                dict: statusDict,
            },
            {
                label: t('plugin.ai.supplier.columns.remark'),
                prop: 'remark',
                align: 'left',
                minWidth: 160,
                operator: 'ILIKE',
                showOverflowTooltip: true,
                show: false,
            },
            {
                label: t('plugin.ai.supplier.columns.createdAt'),
                prop: 'create_time',
                align: 'center',
                width: 170,
                operator: false,
                formatter: (row: TableRow) => formatSeconds(row.create_time),
                show: false,
            },
            {
                label: t('plugin.ai.supplier.columns.updatedAt'),
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
                        name: 'edit',
                        title: t('plugin.ai.supplier.actions.edit'),
                        type: 'primary',
                        icon: 'lucide-pencil',
                        display: () => canNode('update'),
                        click: (row) => openEdit(row),
                    },
                    {
                        render: 'confirm',
                        name: 'delete',
                        title: t('plugin.ai.supplier.actions.delete'),
                        type: 'danger',
                        icon: 'lucide-trash',
                        display: () => canNode('delete'),
                        popconfirm: (ctx) => ({ title: t('plugin.ai.supplier.actions.deleteConfirm', { name: ctx.row?.name ?? '' }) }),
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
            vendor: 'openai',
            driver: '',
            base_url: '',
            api_key: '',
            timeout: 60,
            status: 1,
            remark: '',
        },
    },
})

const formItems = computed<AnyObj>(() => tableManager.form.items ?? {})

// 编辑回填：只保留可编辑字段；status 原样回填（不归一编码，见文件头）
const SUPPLIER_EDITABLE_FIELDS = ['id', 'name', 'vendor', 'driver', 'base_url', 'api_key', 'timeout', 'status', 'remark']
tableManager.opts.after!.getEditData = () => {
    const row = tableManager.form.items ?? {}
    const items: AnyObj = {}
    for (const field of SUPPLIER_EDITABLE_FIELDS) {
        items[field] = row[field]
    }
    items.driver = row.driver == null ? '' : String(row.driver)
    items.timeout = Number(row.timeout ?? 60)
    items.status = Number(row.status ?? 1) === 1 ? 1 : 0
    items.api_key = row.api_key ?? ''
    tableManager.form.items = items
}

// 提交前归一：timeout 非负整数；status 走 0/1 二值；api_key 掩码形态不回写（后端同口径，这里只保证不发无意义值）
tableManager.opts.before!.submitForm = ({ items }: { items: AnyObj }) => {
    if (!items) return
    const timeout = Math.trunc(Number(items.timeout ?? 60))
    items.timeout = Number.isFinite(timeout) && timeout > 0 ? timeout : 0
    items.status = Number(items.status ?? 1) === 1 ? 1 : 0
    items.driver = items.driver == null ? '' : String(items.driver).trim()
}

/* ---------------- 顶部显式筛选（后端顶层 status，与 wheres AND 合并） ---------------- */
const preset = reactive<{ status: string | number }>({ status: '' })

const applyPreset = () => {
    const filter = tableManager.table.filter!
    if (preset.status === '' || preset.status === null || preset.status === undefined) {
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
        // 请求层已提示（仍有渠道引用时是后端给的可读拒绝）；保持列表不变
    }
}

const validateBaseUrl = (_rule: unknown, value: string, callback: (error?: Error) => void) => {
    if (!value) {
        callback()
        return
    }
    if (!/^https?:\/\/[^\s]+$/i.test(value)) {
        callback(new Error(t('plugin.ai.supplier.form.rules.baseUrlInvalid')))
        return
    }
    callback()
}

const validateTimeout = (_rule: unknown, value: number, callback: (error?: Error) => void) => {
    const timeout = Number(value)
    if (!Number.isFinite(timeout) || timeout < 0 || timeout > 600) {
        callback(new Error(t('plugin.ai.supplier.form.rules.timeoutRange')))
        return
    }
    callback()
}

const rules = reactive<Partial<Record<string, FormItemRule[]>>>({
    name: [buildValidatorRule({ name: 'required', title: t('plugin.ai.supplier.form.name') })],
    base_url: [{ validator: validateBaseUrl, trigger: 'blur' }],
    timeout: [{ validator: validateTimeout, trigger: 'change' }],
})

onMounted(() => {
    tableManager.initCtx()
    tableManager.getData()
})
</script>

<style scoped lang="scss">
.plugin-ai-supplier {
    .supplier-alert {
        margin-bottom: 10px;
    }

    .supplier-filters {
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
}
</style>