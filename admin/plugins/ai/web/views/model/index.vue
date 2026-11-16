<!-- plugin: ai v0.2.0 -->
<!--
  模型管理页（标准 CRUD 三件套 + 绑定渠道业务弹窗）。
  契约：POST /admin/plugin/ai/model/list（行附 channels / channel_names / bound_count）、
  GET /model/get/:pk（row.channels 回填绑定）、POST /model/bind（{model_id,binds[]} 全量替换）。
  capabilities 由后端接受数组或逗号串；这里固定为字符串数组。
-->
<template>
    <div class="default-main plugin-ai-model">
        <el-alert class="model-alert" type="info" :closable="false" show-icon :title="t('plugin.ai.model.intro')" />

        <div class="model-filters">
            <el-select v-model="preset.type" class="filter-select" clearable :placeholder="t('plugin.ai.model.filters.type')">
                <el-option :label="t('plugin.ai.model.type.chat')" value="chat" />
                <el-option :label="t('plugin.ai.model.type.embedding')" value="embedding" />
                <el-option :label="t('plugin.ai.model.type.tts')" value="tts" />
            </el-select>
            <el-select v-model="preset.status" class="filter-select" clearable :placeholder="t('plugin.ai.model.filters.status')">
                <el-option :label="t('plugin.ai.model.status.on')" :value="1" />
                <el-option :label="t('plugin.ai.model.status.off')" :value="0" />
            </el-select>
            <el-button v-blur type="primary" @click="applyPreset">{{ t('plugin.ai.model.filters.query') }}</el-button>
            <el-button v-blur @click="resetPreset">{{ t('plugin.ai.model.filters.reset') }}</el-button>
        </div>

        <TableHeader
            :manager="tableManager"
            v-model:com-search="tableManager.comSearch"
            :buttons="['refresh', 'add', 'delete', 'comSearch', 'quickSearch', 'columnDisplay']"
            :quick-search-placeholder="t('plugin.ai.model.searchPlaceholder')"
        />

        <Table :manager="tableManager" :empty-text="t('plugin.ai.model.empty')" />

        <!-- 新增 / 编辑：就地独立弹窗（本页未拆 dialogForm，避免与绑定弹窗交叉） -->
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
                    {{ tableManager.form.operate === 'create' ? t('plugin.ai.model.form.add') : t('plugin.ai.model.form.edit') }}
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
                        <el-form-item :label="t('plugin.ai.model.form.name')" prop="name">
                            <el-input v-model="formItems.name" maxlength="100" :placeholder="t('plugin.ai.model.form.namePlaceholder')" />
                            <div class="form-tip">{{ t('plugin.ai.model.form.nameTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.model.form.type')" prop="type">
                            <el-radio-group v-model="formItems.type">
                                <el-radio value="chat" :border="true">{{ t('plugin.ai.model.type.chat') }}</el-radio>
                                <el-radio value="embedding" :border="true">{{ t('plugin.ai.model.type.embedding') }}</el-radio>
                                <el-radio value="tts" :border="true">{{ t('plugin.ai.model.type.tts') }}</el-radio>
                            </el-radio-group>
                            <div class="form-tip">{{ t('plugin.ai.model.form.typeTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.model.form.strategy')" prop="strategy">
                            <el-radio-group v-model="formItems.strategy">
                                <el-radio value="priority" :border="true">{{ t('plugin.ai.model.strategy.priority') }}</el-radio>
                                <el-radio value="weighted" :border="true">{{ t('plugin.ai.model.strategy.weighted') }}</el-radio>
                            </el-radio-group>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.model.form.priceInput')" prop="price_input">
                            <el-input-number
                                v-model="formItems.price_input"
                                class="w100"
                                :min="0"
                                :precision="6"
                                :step="0.01"
                                controls-position="right"
                            />
                            <div class="form-tip">{{ t('plugin.ai.model.form.priceUnit') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.model.form.priceOutput')" prop="price_output">
                            <el-input-number
                                v-model="formItems.price_output"
                                class="w100"
                                :min="0"
                                :precision="6"
                                :step="0.01"
                                controls-position="right"
                            />
                            <div class="form-tip">{{ t('plugin.ai.model.form.priceUnit') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.model.form.encoding')" prop="encoding">
                            <el-select v-model="formItems.encoding" class="w100">
                                <el-option :label="t('plugin.ai.model.encoding.off')" value="" />
                                <el-option label="tokenz-r" value="tokenz-r" />
                            </el-select>
                            <div class="form-tip">{{ t('plugin.ai.model.form.encodingTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.model.form.contextWindow')" prop="context_window">
                            <el-input-number
                                v-model="formItems.context_window"
                                class="w100"
                                :min="0"
                                :precision="0"
                                controls-position="right"
                            />
                            <div class="form-tip">{{ t('plugin.ai.model.form.contextWindowTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.model.form.capabilities')" prop="capabilities">
                            <el-checkbox-group v-model="formItems.capabilities">
                                <el-checkbox value="chat">{{ t('plugin.ai.model.capability.chat') }}</el-checkbox>
                                <el-checkbox value="function_calling">{{ t('plugin.ai.model.capability.function_calling') }}</el-checkbox>
                                <el-checkbox value="code">{{ t('plugin.ai.model.capability.code') }}</el-checkbox>
                            </el-checkbox-group>
                            <div class="form-tip">{{ t('plugin.ai.model.form.capabilitiesTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.model.form.status')" prop="status">
                            <el-switch v-model="formItems.status" :active-value="1" :inactive-value="0" />
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.model.form.description')" prop="description">
                            <el-input
                                @keyup.enter.stop=""
                                @keyup.ctrl.enter="tableManager.submitForm(formRef)"
                                v-model="formItems.description"
                                type="textarea"
                                :rows="3"
                                maxlength="255"
                                show-word-limit
                                :placeholder="t('plugin.ai.model.form.descriptionPlaceholder')"
                            />
                        </el-form-item>
                    </el-form>
                </div>
            </el-scrollbar>
            <template #footer>
                <div class="dialog-footer">
                    <el-button v-blur @click="tableManager.toggleForm()">{{ t('common.cancel') }}</el-button>
                    <el-button v-blur :loading="tableManager.form.submitLoading" @click="tableManager.submitForm(formRef)" type="primary">
                        {{ tableManager.form.operatePKs && tableManager.form.operatePKs.length > 1 ? t('common.saveAndContinue') : t('common.save') }}
                    </el-button>
                </div>
            </template>
        </el-dialog>

        <BindDialog v-model="bindVisible" :model-id="bindModelId" :model-name="bindModelName" @saved="tableManager.getData()" />
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
import type { FormInstance, FormItemRule } from 'element-plus'
import { computed, onMounted, reactive, ref, useTemplateRef } from 'vue'
import { useI18n } from 'vue-i18n'
import BindDialog from './bind.vue'

defineOptions({
    name: 'plugin/ai/model',
})

const { t } = useI18n()
const config = useConfig()
const formRef = useTemplateRef<FormInstance>('formRef')

const pagePath = getCurrentRoutePath()
const can = (node: string) => auth({ name: pagePath, subNodeName: `${pagePath}/${node}` })

type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'

const typeDict: Record<string, string> = {
    chat: t('plugin.ai.model.type.chat'),
    embedding: t('plugin.ai.model.type.embedding'),
    tts: t('plugin.ai.model.type.tts'),
}
const typeCustom: Record<string, TagType> = { chat: 'primary', embedding: 'success', tts: 'warning' }
const strategyDict: Record<string, string> = {
    priority: t('plugin.ai.model.strategy.priority'),
    weighted: t('plugin.ai.model.strategy.weighted'),
}
const strategyCustom: Record<string, TagType> = { priority: 'info', weighted: 'warning' }
const encodingDict: Record<string, string> = {
    off: t('plugin.ai.model.encoding.off'),
    'tokenz-r': 'tokenz-r',
}
const encodingCustom: Record<string, TagType> = { off: 'info', 'tokenz-r': 'success' }
const capabilityDict: Record<string, string> = {
    chat: t('plugin.ai.model.capability.chat'),
    function_calling: t('plugin.ai.model.capability.function_calling'),
    code: t('plugin.ai.model.capability.code'),
}
const statusDict: Record<string, string> = {
    1: t('plugin.ai.model.status.on'),
    0: t('plugin.ai.model.status.off'),
}
const statusCustom: Record<string, TagType> = { 1: 'success', 0: 'info' }

const capabilityValue = (row: TableRow): string[] => {
    const value = row.capabilities
    if (Array.isArray(value)) return value.map(String)
    if (typeof value === 'string' && value.trim()) {
        return value
            .split(',')
            .map((item) => item.trim())
            .filter(Boolean)
    }
    return []
}

const tableManager = useTableManager({
    api: new TableManagerAPI('/admin/plugin/ai/model/'),
    table: {
        column: [
            { type: 'selection', align: 'center', operator: false },
            {
                label: t('plugin.ai.model.columns.name'),
                prop: 'name',
                align: 'left',
                minWidth: 180,
                operator: 'ILIKE',
                quickSearch: true,
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.model.columns.type'),
                prop: 'type',
                align: 'center',
                width: 100,
                operator: false,
                render: 'tag',
                custom: typeCustom,
                dict: typeDict,
            },
            {
                label: t('plugin.ai.model.columns.strategy'),
                prop: 'strategy',
                align: 'center',
                width: 100,
                operator: 'eq',
                render: 'tag',
                custom: strategyCustom,
                dict: strategyDict,
            },
            {
                label: t('plugin.ai.model.columns.priceInput'),
                prop: 'price_input',
                align: 'right',
                width: 130,
                operator: 'BETWEEN',
                formatter: (row: TableRow) => String(row.price_input ?? '-'),
            },
            {
                label: t('plugin.ai.model.columns.priceOutput'),
                prop: 'price_output',
                align: 'right',
                width: 130,
                operator: 'BETWEEN',
                formatter: (row: TableRow) => String(row.price_output ?? '-'),
            },
            {
                label: t('plugin.ai.model.columns.encoding'),
                prop: 'encoding',
                align: 'center',
                width: 120,
                operator: 'eq',
                render: 'tag',
                custom: encodingCustom,
                dict: encodingDict,
                formatter: (row: TableRow) => row.encoding || 'off',
            },
            {
                label: t('plugin.ai.model.columns.contextWindow'),
                prop: 'context_window',
                align: 'center',
                width: 120,
                operator: 'BETWEEN',
            },
            {
                label: t('plugin.ai.model.columns.capabilities'),
                prop: 'capabilities',
                align: 'left',
                minWidth: 160,
                operator: false,
                render: 'tags',
                dict: capabilityDict,
                formatter: (row: TableRow) => capabilityValue(row),
            },
            {
                label: t('plugin.ai.model.columns.status'),
                prop: 'status',
                align: 'center',
                width: 90,
                operator: false,
                render: 'tag',
                custom: statusCustom,
                dict: statusDict,
            },
            {
                label: t('plugin.ai.model.columns.boundChannels'),
                prop: 'channel_names',
                align: 'left',
                minWidth: 180,
                operator: false,
                render: 'tags',
            },
            {
                label: t('plugin.ai.model.columns.boundCount'),
                prop: 'bound_count',
                align: 'center',
                width: 90,
                operator: false,
            },
            {
                label: t('plugin.ai.model.columns.description'),
                prop: 'description',
                align: 'left',
                minWidth: 180,
                operator: 'ILIKE',
                showOverflowTooltip: true,
                show: false,
            },
            {
                label: t('plugin.ai.model.columns.createdAt'),
                prop: 'create_time',
                align: 'center',
                width: 160,
                operator: false,
                show: false,
            },
            {
                label: t('plugin.ai.model.columns.updatedAt'),
                prop: 'update_time',
                align: 'center',
                width: 160,
                operator: false,
                show: false,
            },
            {
                label: t('common.operate'),
                align: 'center',
                width: 220,
                render: 'buttons',
                operator: false,
                buttons: [
                    {
                        render: 'tip',
                        name: 'edit',
                        title: t('plugin.ai.model.actions.edit'),
                        type: 'primary',
                        icon: 'lucide-pencil',
                        display: () => can('update'),
                        click: (row) => tableManager.toggleForm('update', [String(row.id)]),
                    },
                    {
                        render: 'tip',
                        name: 'bind',
                        title: t('plugin.ai.model.actions.bind'),
                        type: 'success',
                        icon: 'lucide-link',
                        display: () => can('bind'),
                        click: (row) => openBind(row),
                    },
                    {
                        render: 'confirm',
                        name: 'delete',
                        title: t('plugin.ai.model.actions.delete'),
                        type: 'danger',
                        icon: 'lucide-trash',
                        display: () => can('delete'),
                        popconfirm: (ctx) => ({ title: t('plugin.ai.model.actions.deleteConfirm', { name: ctx.row?.name ?? '' }) }),
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
        labelWidth: 160,
        defaultItems: {
            name: '',
            type: 'chat',
            strategy: 'priority',
            description: '',
            status: 1,
            price_input: 0,
            price_output: 0,
            encoding: '',
            context_window: 0,
            capabilities: [],
        },
    },
})

const formItems = computed<AnyObj>(() => tableManager.form.items ?? {})

// 编辑回填只保留可编辑列，并把 numeric/JSON 列归一为表单控件可用的类型
const MODEL_EDITABLE_FIELDS = [
    'id',
    'name',
    'type',
    'strategy',
    'description',
    'status',
    'price_input',
    'price_output',
    'encoding',
    'context_window',
    'capabilities',
]
tableManager.opts.after!.getEditData = () => {
    const row = tableManager.form.items ?? {}
    const items: AnyObj = {}
    for (const field of MODEL_EDITABLE_FIELDS) {
        items[field] = row[field]
    }
    items.status = Number(items.status ?? 1)
    items.price_input = Number(items.price_input ?? 0)
    items.price_output = Number(items.price_output ?? 0)
    items.context_window = Number(items.context_window ?? 0)
    items.encoding = items.encoding ?? ''
    items.capabilities = capabilityValue(row)
    tableManager.form.items = items
}

const rules = reactive<Partial<Record<string, FormItemRule[]>>>({
    name: [buildValidatorRule({ name: 'required', title: t('plugin.ai.model.form.name') })],
})

// 金额入口一律转 6 位小数字符串（与后端 numeric(12,6) 的字符串承载口径一致）
const priceString = (value: unknown) => {
    const parsed = Number(value)
    return Number.isFinite(parsed) ? parsed.toFixed(6) : '0.000000'
}
tableManager.opts.before!.submitForm = ({ items }: { items: AnyObj }) => {
    if (!items) return
    items.price_input = priceString(items.price_input)
    items.price_output = priceString(items.price_output)
}

/* ---------------- 顶部显式筛选（后端顶层 type/status，与 wheres AND 合并） ---------------- */
const preset = reactive<{ type: string; status: number | string }>({ type: '', status: '' })

const applyPreset = () => {
    const filter = tableManager.table.filter!
    if (!preset.type) {
        delete filter.type
    } else {
        filter.type = preset.type
    }
    if (preset.status === '' || preset.status === null || preset.status === undefined) {
        delete filter.status
    } else {
        filter.status = preset.status
    }
    filter.page = 1
    tableManager.getData()
}

const resetPreset = () => {
    preset.type = ''
    preset.status = ''
    applyPreset()
}

/* ---------------- 行内动作 ---------------- */
const removeRow = async (row: TableRow) => {
    try {
        await tableManager.api.delete([String(row.id)])
        tableManager.getData()
    } catch {
        // 请求层已提示；保持列表不变
    }
}

/* ---------------- 绑定渠道 ---------------- */
const bindVisible = ref(false)
const bindModelId = ref<number | null>(null)
const bindModelName = ref('')

const openBind = (row: TableRow) => {
    bindModelId.value = Number(row.id)
    bindModelName.value = String(row.name ?? '')
    bindVisible.value = true
}

onMounted(() => {
    tableManager.initCtx()
    tableManager.getData()
})
</script>

<style scoped lang="scss">
.plugin-ai-model {
    .model-alert {
        margin-bottom: 10px;
    }

    .model-filters {
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
