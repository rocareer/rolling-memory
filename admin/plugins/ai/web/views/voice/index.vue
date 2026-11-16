<!-- plugin: ai v0.2.0 -->
<!--
  语音音色页（音色 CRUD + 试听）。
  契约（照已落地的 handler_voice.go / voice.go 逐字对齐）：
    POST /admin/plugin/ai/voice/list       → data={list,total}；行 = name/model/voice/format/status/remark/时间戳
    GET  /admin/plugin/ai/voice/get/:pk    → data={row}
    POST /admin/plugin/ai/voice/create     → data={id}
    POST /admin/plugin/ai/voice/update/:pk → 可改 name/model/voice/format/status/remark
    POST /admin/plugin/ai/voice/delete     → {pks}
    POST /admin/plugin/ai/voice/preview    → 音频流（Content-Type: audio/*，服务端不落盘、不返回上游 OSS 链接）
  试听入参 {id（必填）, text?, format?}：text 留空由后端取默认试听文本（页面预填一段可改的默认值）。
  试听走原始 axios：宿主 request 的响应拦截器按 body.code 判定，音频 blob 会被它当业务错误吞掉。
  后端失败时以 JSON 壳写回执（httpx 恒 HTTP 200）⇒ 响应 blob.type 含 json 就按错误文案提示、不播放。
  status 是 Voice.Status 的 int16 二值列（voice.go:54）⇒ 页面按 1/0 显示与提交。
  权限节点：plugin/ai/voice/{read,create,update,delete,preview}。
-->
<template>
    <div class="default-main plugin-ai-voice">
        <el-alert class="voice-alert" type="info" :closable="false" show-icon :title="t('plugin.ai.voice.intro')" />

        <TableHeader
            :manager="tableManager"
            v-model:com-search="tableManager.comSearch"
            :buttons="['refresh', 'add', 'delete', 'comSearch', 'quickSearch', 'columnDisplay']"
            :quick-search-placeholder="t('plugin.ai.voice.searchPlaceholder')"
        />

        <div class="voice-filters">
            <el-select v-model="preset.status" class="filter-select" clearable :placeholder="t('plugin.ai.voice.filters.status')">
                <el-option :label="t('plugin.ai.voice.status.on')" :value="1" />
                <el-option :label="t('plugin.ai.voice.status.off')" :value="0" />
            </el-select>
            <el-input
                v-model="preset.model"
                class="filter-input"
                clearable
                :placeholder="t('plugin.ai.voice.filters.model')"
                @keyup.enter="applyPreset"
            />
            <el-button v-blur type="primary" @click="applyPreset">{{ t('plugin.ai.voice.filters.query') }}</el-button>
            <el-button v-blur @click="resetPreset">{{ t('plugin.ai.voice.filters.reset') }}</el-button>
        </div>

        <Table :manager="tableManager" :empty-text="t('plugin.ai.voice.empty')" />

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
                    {{ tableManager.form.operate === 'create' ? t('plugin.ai.voice.form.add') : t('plugin.ai.voice.form.edit') }}
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
                        <el-form-item :label="t('plugin.ai.voice.form.name')" prop="name">
                            <el-input v-model="formItems.name" maxlength="50" :placeholder="t('plugin.ai.voice.form.namePlaceholder')" />
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.voice.form.model')" prop="model">
                            <el-input v-model="formItems.model" maxlength="100" :placeholder="t('plugin.ai.voice.form.modelPlaceholder')" />
                            <div class="form-tip">{{ t('plugin.ai.voice.form.modelTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.voice.form.voice')" prop="voice">
                            <el-input v-model="formItems.voice" maxlength="64" :placeholder="t('plugin.ai.voice.form.voicePlaceholder')" />
                            <div class="form-tip">{{ t('plugin.ai.voice.form.voiceTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.voice.form.format')" prop="format">
                            <el-select v-model="formItems.format" class="w100" filterable allow-create default-first-option :reserve-keyword="false">
                                <el-option v-for="item in formatOptions" :key="item" :label="formatDict[item]" :value="item" />
                            </el-select>
                            <div class="form-tip">{{ t('plugin.ai.voice.form.formatTip') }}</div>
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.voice.form.status')" prop="status">
                            <el-switch v-model="formItems.status" :active-value="1" :inactive-value="0" />
                        </el-form-item>

                        <el-form-item :label="t('plugin.ai.voice.form.remark')" prop="remark">
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

        <!-- 试听：文本可改，音频由后端经渠道实时合成（不落盘） -->
        <el-dialog v-model="previewVisible" :title="previewTitle" width="min(560px, 96vw)" :destroy-on-close="true" @close="stopAudio">
            <el-form label-width="120px">
                <el-form-item :label="t('plugin.ai.voice.preview.text')">
                    <el-input
                        v-model="previewText"
                        type="textarea"
                        :rows="4"
                        maxlength="500"
                        show-word-limit
                        :placeholder="t('plugin.ai.voice.preview.placeholder')"
                    />
                </el-form-item>
            </el-form>
            <div class="preview-actions">
                <el-button v-blur type="primary" :loading="previewLoading" @click="playPreview">
                    <Icon name="lucide-play" :size="14" />
                    <span class="btn-text">{{ previewLoading ? t('plugin.ai.voice.preview.playing') : t('plugin.ai.voice.preview.play') }}</span>
                </el-button>
                <el-button v-blur @click="stopAudio">
                    <Icon name="lucide-square" :size="14" />
                    <span class="btn-text">{{ t('plugin.ai.voice.preview.stop') }}</span>
                </el-button>
                <span class="preview-tip">{{ t('plugin.ai.voice.preview.tip') }}</span>
            </div>
            <template #footer>
                <el-button @click="previewVisible = false">{{ t('common.close') }}</el-button>
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
import { useConfig } from '@/stores/config'
import { auth, getCurrentRoutePath } from '@/utils/common'
import { getBaseURL } from '@/utils/request'
import { buildValidatorRule } from '@/utils/validate'
import axios from 'axios'
import { dayjs, ElMessage } from 'element-plus'
import type { FormInstance, FormItemRule } from 'element-plus'
import { computed, onMounted, onUnmounted, reactive, ref, useTemplateRef } from 'vue'
import { useI18n } from 'vue-i18n'

defineOptions({
    name: 'plugin/ai/voice',
})

const { t } = useI18n()
const config = useConfig()
const formRef = useTemplateRef<FormInstance>('formRef')

const pagePath = getCurrentRoutePath()
const canPreview = auth({ name: pagePath, subNodeName: `${pagePath}/preview` })

type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'

interface VoiceRow extends TableRow {
    id?: number
    name?: string
    model?: string
    voice?: string
    format?: string
    status?: number
    remark?: string
    create_time?: number
    update_time?: number
}

const statusDict: Record<string, string> = { 1: t('plugin.ai.voice.status.on'), 0: t('plugin.ai.voice.status.off') }
const statusCustom: Record<string, TagType> = { 1: 'success', 0: 'info' }
// 格式字典：内置四种给译名，注册表/上游新增的其它格式按原值展示（下拉允许自由输入）
const formatDict: Record<string, string> = {
    mp3: t('plugin.ai.voice.format.mp3'),
    wav: t('plugin.ai.voice.format.wav'),
    opus: t('plugin.ai.voice.format.opus'),
    pcm: t('plugin.ai.voice.format.pcm'),
}
const formatOptions = Object.keys(formatDict)
const formatLabel = (value?: string) => (value ? (formatDict[value] ?? value) : '-')
const formatSeconds = (value?: number | null) => (value ? dayjs.unix(Number(value)).format('YYYY-MM-DD HH:mm:ss') : '-')

const tableManager = useTableManager({
    api: new TableManagerAPI('/admin/plugin/ai/voice/'),
    table: {
        column: [
            { type: 'selection', align: 'center', operator: false },
            {
                label: t('plugin.ai.voice.columns.name'),
                prop: 'name',
                align: 'left',
                minWidth: 160,
                operator: 'ILIKE',
                quickSearch: true,
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.voice.columns.model'),
                prop: 'model',
                align: 'left',
                minWidth: 150,
                operator: 'ILIKE',
                quickSearch: true,
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.voice.columns.voice'),
                prop: 'voice',
                align: 'left',
                minWidth: 140,
                operator: 'ILIKE',
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.ai.voice.columns.format'),
                prop: 'format',
                align: 'center',
                width: 100,
                operator: 'eq',
                formatter: (row: TableRow) => formatLabel(row.format),
            },
            {
                label: t('plugin.ai.voice.columns.status'),
                prop: 'status',
                align: 'center',
                width: 100,
                operator: 'eq',
                render: 'tag',
                custom: statusCustom,
                dict: statusDict,
            },
            {
                label: t('plugin.ai.voice.columns.remark'),
                prop: 'remark',
                align: 'left',
                minWidth: 160,
                operator: 'ILIKE',
                showOverflowTooltip: true,
                show: false,
            },
            {
                label: t('plugin.ai.voice.columns.createdAt'),
                prop: 'create_time',
                align: 'center',
                width: 170,
                operator: false,
                formatter: (row: TableRow) => formatSeconds(row.create_time),
                show: false,
            },
            {
                label: t('plugin.ai.voice.columns.updatedAt'),
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
                width: 250,
                render: 'buttons',
                operator: false,
                buttons: [
                    {
                        render: 'tip',
                        name: 'preview',
                        title: t('plugin.ai.voice.actions.preview'),
                        type: 'primary',
                        icon: 'lucide-volume-2',
                        display: () => canPreview,
                        click: (row) => openPreview(row),
                    },
                    {
                        render: 'tip',
                        name: 'edit',
                        title: t('plugin.ai.voice.actions.edit'),
                        type: 'primary',
                        icon: 'lucide-pencil',
                        click: (row) => openEdit(row),
                    },
                    {
                        render: 'confirm',
                        name: 'delete',
                        title: t('plugin.ai.voice.actions.delete'),
                        type: 'danger',
                        icon: 'lucide-trash',
                        popconfirm: (ctx) => ({ title: t('plugin.ai.voice.actions.deleteConfirm', { name: ctx.row?.name ?? '' }) }),
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
            model: '',
            voice: '',
            format: 'mp3',
            status: 1,
            remark: '',
        },
    },
})

const formItems = computed<AnyObj>(() => tableManager.form.items ?? {})

const VOICE_EDITABLE_FIELDS = ['id', 'name', 'model', 'voice', 'format', 'status', 'remark']
tableManager.opts.after!.getEditData = () => {
    const row = tableManager.form.items ?? {}
    const items: AnyObj = {}
    for (const field of VOICE_EDITABLE_FIELDS) {
        items[field] = row[field]
    }
    items.status = Number(row.status ?? 1) === 1 ? 1 : 0
    items.format = row.format || 'mp3'
    tableManager.form.items = items
}

const rules = reactive<Partial<Record<string, FormItemRule[]>>>({
    name: [buildValidatorRule({ name: 'required', title: t('plugin.ai.voice.form.name') })],
    model: [buildValidatorRule({ name: 'required', title: t('plugin.ai.voice.form.model') })],
    voice: [buildValidatorRule({ name: 'required', title: t('plugin.ai.voice.form.voice') })],
})

/* ---------------- 顶部显式筛选（后端顶层 status/model，与 wheres AND 合并） ---------------- */
const preset = reactive<{ status: number | string; model: string }>({ status: '', model: '' })

const applyPreset = () => {
    const filter = tableManager.table.filter!
    if (preset.status === '' || preset.status === null || preset.status === undefined) {
        delete filter.status
    } else {
        filter.status = preset.status
    }
    const model = preset.model.trim()
    if (!model) {
        delete filter.model
    } else {
        filter.model = model
    }
    filter.page = 1
    tableManager.getData()
}

const resetPreset = () => {
    preset.status = ''
    preset.model = ''
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

/* ---------------- 试听（音频 blob，走原始 axios） ---------------- */
const previewVisible = ref(false)
const previewLoading = ref(false)
const previewText = ref('')
const previewRow = ref<VoiceRow | null>(null)
const previewTitle = computed(() => t('plugin.ai.voice.preview.title', { name: String(previewRow.value?.name ?? '-') }))

let audio: HTMLAudioElement | null = null
let audioUrl = ''

const adminPrefixed = (url: string) => (adminBaseRoutePath !== '/admin' ? url.replace(/^\/admin\//, `${adminBaseRoutePath}/`) : url)

const stopAudio = () => {
    if (audio) {
        audio.pause()
        audio.onended = null
        audio = null
    }
    if (audioUrl) {
        window.URL.revokeObjectURL(audioUrl)
        audioUrl = ''
    }
}

const openPreview = (row: TableRow) => {
    stopAudio()
    previewRow.value = row as VoiceRow
    // 预填一段默认试听文本（可改）：后端 text 为空时用自己的默认文本，这里给人看得见的默认值
    previewText.value = t('plugin.ai.voice.preview.sample')
    previewVisible.value = true
}

const playPreview = async () => {
    const id = Number(previewRow.value?.id ?? 0)
    if (!id) {
        ElMessage.warning(t('plugin.ai.voice.preview.empty'))
        return
    }
    previewLoading.value = true
    try {
        const res = await axios.post(
            adminPrefixed('/admin/plugin/ai/voice/preview'),
            // 入参照 voicePreviewRequest：{id 必填, text? 可覆盖, format? 可覆盖}；text 留空由后端取默认文本
            { id, text: previewText.value.trim() },
            {
                baseURL: getBaseURL(),
                responseType: 'blob',
                timeout: 60000,
                headers: { Authorization: `Bearer ${useAdminInfo().token}` },
            }
        )
        const blob = res.data as Blob
        // httpx 恒 HTTP 200：拒绝以 JSON 壳写回执 ⇒ 不把错误体当音频播
        if (blob.type.includes('json')) {
            const text = await blob.text()
            let message = t('plugin.ai.voice.preview.failed')
            try {
                message = JSON.parse(text)?.message || message
            } catch {
                message = text || message
            }
            ElMessage.error(message)
            return
        }
        stopAudio()
        audioUrl = window.URL.createObjectURL(blob)
        audio = new Audio(audioUrl)
        audio.onended = () => stopAudio()
        await audio.play()
    } catch (error) {
        const status = (error as AnyObj)?.response?.status
        const message = (error as AnyObj)?.response?.data?.message ?? (error as Error)?.message ?? t('plugin.ai.voice.preview.failed')
        ElMessage.error(status ? `${message}（HTTP ${status}）` : String(message))
    } finally {
        previewLoading.value = false
    }
}

onMounted(() => {
    tableManager.initCtx()
    tableManager.getData()
})

onUnmounted(() => {
    stopAudio()
})
</script>

<style scoped lang="scss">
.plugin-ai-voice {
    .voice-alert {
        margin-bottom: 10px;
    }

    .voice-filters {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
        margin: 10px 0;
    }

    .filter-select {
        width: 150px;
    }

    .filter-input {
        width: 190px;
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

    .preview-actions {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
    }

    .preview-tip {
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .btn-text {
        margin-left: 4px;
    }
}
</style>