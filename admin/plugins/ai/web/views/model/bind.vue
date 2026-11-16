<!-- plugin: ai v0.2.0 -->
<!--
  绑定渠道弹窗（model/bind 全量替换）。
  拉启用渠道：POST /admin/plugin/ai/channel/list，显式顶层筛选 {status:1, limit:100}（不是 wheres）。
  已绑定回填：GET /admin/plugin/ai/model/get/:pk 的 row.channels（channel_id / priority / weight / status）。
  保存：POST /admin/plugin/ai/model/bind，binds 全量替换；priority/weight 缺省 1，enabled 缺省 false。
-->
<template>
    <el-dialog
        v-model="visible"
        :title="t('plugin.ai.model.bind.title', { name: modelName || '-' })"
        width="min(760px, 96vw)"
        :close-on-click-modal="false"
        :destroy-on-close="true"
    >
        <div v-loading="loading" class="bind-body">
            <el-alert class="bind-alert" type="info" :closable="false" show-icon :title="t('plugin.ai.model.bind.intro')" />
            <el-empty v-if="!loading && !channels.length" :description="t('plugin.ai.model.bind.noChannels')" :image-size="60" />
            <div v-else class="bind-list">
                <div v-for="channel in channels" :key="channel.id" class="bind-item">
                    <div class="bind-head">
                        <el-checkbox :model-value="!!binds[channel.id]" @change="(checked) => toggle(channel, Boolean(checked))">
                            <span class="bind-name">{{ channel.name || `#${channel.id}` }}</span>
                            <el-tag v-if="Number(channel.status) === 0" size="small" type="info">
                                {{ t('plugin.ai.model.bind.disabled') }}
                            </el-tag>
                        </el-checkbox>
                    </div>
                    <div v-if="binds[channel.id]" class="bind-fields">
                        <div class="bind-field">
                            <span class="bind-label">{{ t('plugin.ai.model.bind.priority') }}</span>
                            <el-input-number
                                v-model="binds[channel.id].priority"
                                size="small"
                                :min="0"
                                :precision="0"
                                controls-position="right"
                            />
                        </div>
                        <div class="bind-field">
                            <span class="bind-label">{{ t('plugin.ai.model.bind.weight') }}</span>
                            <el-input-number
                                v-model="binds[channel.id].weight"
                                size="small"
                                :min="0"
                                :precision="0"
                                controls-position="right"
                            />
                        </div>
                        <div class="bind-field">
                            <span class="bind-label">{{ t('plugin.ai.model.bind.enabled') }}</span>
                            <el-switch v-model="binds[channel.id].enabled" />
                        </div>
                    </div>
                </div>
            </div>
        </div>
        <template #footer>
            <el-button @click="visible = false">{{ t('common.cancel') }}</el-button>
            <el-button v-blur type="primary" :loading="saving" :disabled="!modelId || loading" @click="save">
                {{ t('plugin.ai.model.bind.save') }}
            </el-button>
        </template>
    </el-dialog>
</template>

<script setup lang="ts">
import request from '@/utils/request'
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

interface Props {
    modelId: number | null
    modelName: string
}

const props = defineProps<Props>()
const emit = defineEmits<{ (e: 'saved'): void }>()
const visible = defineModel<boolean>({ default: false })
const { t } = useI18n()

interface ChannelRow {
    id: number
    name?: string
    status?: number
}

interface ModelChannelBinding {
    channel_id?: number
    id?: number
    channel_name?: string
    name?: string
    priority?: number
    weight?: number
    status?: number
}

interface ModelDetailRow {
    channels?: ModelChannelBinding[]
}

interface BindEntry {
    channel_id: number
    priority: number
    weight: number
    enabled: boolean
}

const loading = ref(false)
const saving = ref(false)
const channels = ref<ChannelRow[]>([])
const binds = reactive<Record<number, BindEntry>>({})

const clearBinds = () => {
    for (const key of Object.keys(binds)) {
        Reflect.deleteProperty(binds, String(key))
    }
}

const load = async () => {
    if (!props.modelId) return
    loading.value = true
    try {
        const [detailRes, listRes] = await Promise.all([
            request<{ row?: ModelDetailRow }>(
                { url: `/admin/plugin/ai/model/get/${props.modelId}`, method: 'GET' },
                { cancelDuplicate: false }
            ),
            request<{ list?: ChannelRow[] }>(
                {
                    url: '/admin/plugin/ai/channel/list',
                    method: 'POST',
                    data: { status: 1, limit: 100 },
                },
                { cancelDuplicate: false }
            ),
        ])
        const row = detailRes.data.data?.row ?? {}
        const enabledChannels = (listRes.data.data?.list ?? []).map((item) => ({
            id: Number(item.id),
            name: item.name,
            status: Number(item.status ?? 1),
        }))
        const merged: ChannelRow[] = [...enabledChannels]
        const seen = new Set(merged.map((item) => item.id))
        clearBinds()
        for (const binding of row.channels ?? []) {
            const channelId = Number(binding.channel_id ?? binding.id)
            if (!channelId) continue
            binds[channelId] = {
                channel_id: channelId,
                priority: Number(binding.priority ?? 1),
                weight: Number(binding.weight ?? 1),
                enabled: Number(binding.status ?? 1) === 1,
            }
            if (!seen.has(channelId)) {
                merged.push({
                    id: channelId,
                    name: binding.channel_name || binding.name || t('plugin.ai.model.bind.unknownChannel', { id: channelId }),
                    status: Number(binding.status ?? 0),
                })
                seen.add(channelId)
            }
        }
        channels.value = merged
    } catch {
        // 请求层已提示；保持空列表，不清掉已有绑定（下一次打开重试）
    } finally {
        loading.value = false
    }
}

const toggle = (channel: ChannelRow, checked: boolean) => {
    const channelId = Number(channel.id)
    if (!channelId) return
    if (checked) {
        binds[channelId] = {
            channel_id: channelId,
            priority: 1,
            weight: 1,
            enabled: true,
        }
    } else {
        Reflect.deleteProperty(binds, String(channelId))
    }
}

const save = async () => {
    if (!props.modelId) return
    const payload = {
        model_id: props.modelId,
        binds: Object.values(binds).map((item) => ({
            channel_id: item.channel_id,
            priority: Number(item.priority ?? 1),
            weight: Number(item.weight ?? 1),
            enabled: Boolean(item.enabled),
        })),
    }
    saving.value = true
    try {
        await request(
            { url: '/admin/plugin/ai/model/bind', method: 'POST', data: payload },
            { showSuccessMessage: true }
        )
        emit('saved')
        visible.value = false
    } catch {
        // 请求层已提示；保留弹窗供修正
    } finally {
        saving.value = false
    }
}

watch(visible, (value) => {
    if (value) {
        load()
    }
})
</script>

<style scoped lang="scss">
.bind-body {
    min-height: 140px;
}

.bind-alert {
    margin-bottom: 10px;
}

.bind-list {
    max-height: 55vh;
    overflow-y: auto;
    padding-right: 4px;
}

.bind-item {
    padding: 8px 10px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 6px;
    margin-bottom: 8px;
}

.bind-head {
    display: flex;
    align-items: center;
    gap: 8px;
}

.bind-name {
    margin-right: 6px;
    font-weight: 600;
}

.bind-fields {
    display: flex;
    align-items: center;
    gap: 18px;
    flex-wrap: wrap;
    margin: 8px 0 0 24px;
}

.bind-field {
    display: flex;
    align-items: center;
    gap: 6px;
}

.bind-label {
    font-size: 12px;
    color: var(--el-text-color-secondary);
}
</style>
