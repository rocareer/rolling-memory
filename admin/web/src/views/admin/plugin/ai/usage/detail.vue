<!-- plugin: ai v0.2.0 -->
<!--
  尝试链详情（按 request_id 还原）。
  契约：POST /admin/plugin/ai/usage-log/detail {request_id} → data={rows,summary}。
  summary 定义：success + fail + timeout = calls，fail 不含 timeout（对账时用 fail + timeout）。
  request_id 为空 ⇒ 本地早退，不发请求（渠道直连调试不记账）。
-->
<template>
    <el-dialog v-model="visible" :title="t('plugin.ai.usage.detail.title')" width="min(1080px, 97vw)" :destroy-on-close="true">
        <div v-loading="loading" class="detail-body">
            <el-alert class="detail-alert" type="info" :closable="false" show-icon :title="t('plugin.ai.usage.detail.intro')" />

            <el-alert
                v-if="noRequestId"
                class="detail-alert"
                type="warning"
                :closable="false"
                show-icon
                :title="t('plugin.ai.usage.detail.noRequestId')"
            />

            <template v-else>
                <div v-if="summary" class="summary-row">
                    <el-tag type="info">{{ t('plugin.ai.usage.detail.summary.calls') }}：{{ formatInt(summary.calls) }}</el-tag>
                    <el-tag type="success">{{ t('plugin.ai.usage.detail.summary.success') }}：{{ formatInt(summary.success) }}</el-tag>
                    <el-tag type="danger">{{ t('plugin.ai.usage.detail.summary.fail') }}：{{ formatInt(summary.fail) }}</el-tag>
                    <el-tag type="warning">{{ t('plugin.ai.usage.detail.summary.timeout') }}：{{ formatInt(summary.timeout) }}</el-tag>
                    <el-tag type="primary">{{ t('plugin.ai.usage.detail.summary.tokens') }}：{{ formatInt(summary.total_tokens) }}</el-tag>
                    <el-tag type="info">{{ t('plugin.ai.usage.detail.summary.cost') }}：{{ formatCost(summary.cost) }}</el-tag>
                </div>

                <el-table :data="rows" size="small" border stripe :empty-text="t('plugin.ai.usage.detail.empty')">
                    <el-table-column :label="t('plugin.ai.usage.detail.columns.attempt')" prop="attempt" width="80" align="center" />
                    <el-table-column :label="t('plugin.ai.usage.detail.columns.time')" width="160">
                        <template #default="scope">{{ formatSeconds(scope.row.create_time) }}</template>
                    </el-table-column>
                    <el-table-column
                        :label="t('plugin.ai.usage.detail.columns.model')"
                        prop="model_name"
                        min-width="140"
                        show-overflow-tooltip
                    />
                    <el-table-column
                        :label="t('plugin.ai.usage.detail.columns.channel')"
                        prop="channel_name"
                        min-width="120"
                        show-overflow-tooltip
                    />
                    <el-table-column :label="t('plugin.ai.usage.detail.columns.callType')" width="90" align="center">
                        <template #default="scope">
                            <el-tag size="small" type="info">{{ callTypeLabel(scope.row.call_type) }}</el-tag>
                        </template>
                    </el-table-column>
                    <!-- 计量归因维度：key:<调用方> = 应用级 API Key；key:gateway = 插件全局密钥 -->
                    <el-table-column :label="t('plugin.ai.usage.detail.columns.source')" prop="source" width="140" align="center">
                        <template #default="scope">
                            <el-tag v-if="scope.row.source" size="small" :type="sourceTagType(scope.row.source)" effect="plain">
                                {{ scope.row.source }}
                            </el-tag>
                            <span v-else>-</span>
                        </template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.detail.columns.status')" width="90" align="center">
                        <template #default="scope">
                            <el-tag size="small" :type="statusTagType(scope.row.status)">{{ statusLabel(scope.row.status) }}</el-tag>
                        </template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.detail.columns.promptTokens')" prop="prompt_tokens" width="110" align="right">
                        <template #default="scope">{{ formatInt(scope.row.prompt_tokens) }}</template>
                    </el-table-column>
                    <el-table-column
                        :label="t('plugin.ai.usage.detail.columns.completionTokens')"
                        prop="completion_tokens"
                        width="110"
                        align="right"
                    >
                        <template #default="scope">{{ formatInt(scope.row.completion_tokens) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.detail.columns.totalTokens')" prop="total_tokens" width="120" align="right">
                        <template #default="scope">{{ formatInt(scope.row.total_tokens) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.detail.columns.cost')" prop="cost" width="120" align="right">
                        <template #default="scope">{{ formatCost(scope.row.cost) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.detail.columns.latency')" prop="latency_ms" width="110" align="right">
                        <template #default="scope">{{ formatInt(scope.row.latency_ms) }}</template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.detail.columns.stream')" width="100" align="center">
                        <template #default="scope">
                            <el-tag size="small" :type="Number(scope.row.stream) === 1 ? 'success' : 'info'">
                                {{ Number(scope.row.stream) === 1 ? t('plugin.ai.usage.log.stream.on') : t('plugin.ai.usage.log.stream.off') }}
                            </el-tag>
                        </template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.detail.columns.tokensSource')" width="120" align="center">
                        <template #default="scope">
                            <el-tooltip :content="t('plugin.ai.usage.log.legend.tokensSourceTip')" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                                <span>{{ tokensSourceLabel(scope.row.tokens_source) }}</span>
                            </el-tooltip>
                        </template>
                    </el-table-column>
                    <!-- 部分计费标记（billed_partial，迁移 2）：上游未收尾但已按已聚合内容计费 -->
                    <el-table-column :label="t('plugin.ai.usage.detail.columns.billedPartial')" width="110" align="center">
                        <template #default="scope">
                            <el-tooltip
                                v-if="Number(scope.row.billed_partial) === 1"
                                :content="t('plugin.ai.usage.log.billedPartial.tip')"
                                placement="top"
                                :show-after="250"
                                :enterable="false"
                                :hide-after="0"
                            >
                                <el-tag size="small" type="warning" effect="dark">{{ t('plugin.ai.usage.log.billedPartial.yes') }}</el-tag>
                            </el-tooltip>
                            <el-tag v-else size="small" type="info" effect="plain">{{ t('plugin.ai.usage.log.billedPartial.no') }}</el-tag>
                        </template>
                    </el-table-column>
                    <el-table-column :label="t('plugin.ai.usage.detail.columns.errorClass')" width="110" align="center">
                        <template #default="scope">{{ errorClassLabel(scope.row.error_class) }}</template>
                    </el-table-column>
                    <el-table-column
                        :label="t('plugin.ai.usage.detail.columns.errorMessage')"
                        prop="error_message"
                        min-width="220"
                        show-overflow-tooltip
                    />
                </el-table>
            </template>
        </div>
        <template #footer>
            <el-button @click="visible = false">{{ t('common.close') }}</el-button>
        </template>
    </el-dialog>
</template>

<script setup lang="ts">
import request from '@/utils/request'
import { dayjs } from 'element-plus'
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

interface Props {
    requestId: string
}

const props = defineProps<Props>()
const visible = defineModel<boolean>({ default: false })
const { t } = useI18n()

type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'

interface UsageRow {
    request_id?: string
    attempt?: number
    model_name?: string
    channel_name?: string
    call_type?: string
    source?: string
    status?: string
    prompt_tokens?: number
    completion_tokens?: number
    total_tokens?: number
    cost?: string | number
    latency_ms?: number
    stream?: number
    tokens_source?: string
    billed_partial?: number
    error_class?: string
    error_message?: string
    create_time?: number
}

interface Summary {
    calls?: number
    success?: number
    fail?: number
    timeout?: number
    total_tokens?: number
    cost?: string | number
}

interface DetailData {
    rows?: UsageRow[]
    summary?: Summary
}

const loading = ref(false)
const noRequestId = ref(false)
const rows = ref<UsageRow[]>([])
const summary = ref<Summary | null>(null)

const formatSeconds = (value?: number | null) => (value ? dayjs.unix(Number(value)).format('YYYY-MM-DD HH:mm:ss') : '-')
const formatInt = (value?: number | null) => (value === null || value === undefined ? '-' : Number(value).toLocaleString())
const formatCost = (value?: string | number | null) => (value === null || value === undefined || value === '' ? '-' : String(value))

const statusLabel = (status?: string) => (status ? t(`plugin.ai.usage.log.status.${status}`) : '-')
const statusTagType = (status?: string): TagType => {
    if (status === 'success') return 'success'
    if (status === 'timeout') return 'warning'
    if (status === 'breaker') return 'info'
    return 'danger'
}
const callTypeLabel = (callType?: string) => (callType ? t(`plugin.ai.usage.log.callType.${callType}`) : '-')
const tokensSourceLabel = (source?: string) => {
    if (!source) return t('plugin.ai.usage.log.tokensSource.none')
    return t(`plugin.ai.usage.log.tokensSource.${source}`)
}
const errorClassLabel = (value?: string) => (value ? t(`plugin.ai.usage.log.errorClass.${value}`) : '-')
// 按 Key 归因：'key:gateway' = 插件全局密钥；'key:<调用方>' = 应用级 API Key（source 接缝写入）
const sourceTagType = (source?: string): TagType => {
    if (source === 'key:gateway') return 'primary'
    if (source && source.startsWith('key:')) return 'warning'
    return 'info'
}

const reset = () => {
    rows.value = []
    summary.value = null
    noRequestId.value = false
}

const load = async (requestId: string) => {
    loading.value = true
    try {
        const res = await request<DetailData>(
            { url: '/admin/plugin/ai/usage-log/detail', method: 'POST', data: { request_id: requestId } },
            { cancelDuplicate: false }
        )
        const payload = res.data.data ?? {}
        rows.value = [...(payload.rows ?? [])].sort((a, b) => Number(a.attempt ?? 0) - Number(b.attempt ?? 0))
        summary.value = payload.summary ?? null
    } catch {
        // 请求层已提示；保持空态，不残留上一次请求的数据
        rows.value = []
        summary.value = null
    } finally {
        loading.value = false
    }
}

watch(
    [visible, () => props.requestId],
    ([isVisible, requestId]) => {
        if (!isVisible) {
            reset()
            return
        }
        reset()
        if (!requestId) {
            noRequestId.value = true
            return
        }
        load(requestId)
    },
    { immediate: false }
)
</script>

<style scoped lang="scss">
.detail-body {
    min-height: 160px;
}

.detail-alert {
    margin-bottom: 10px;
}

.summary-row {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: 12px;
}
</style>
