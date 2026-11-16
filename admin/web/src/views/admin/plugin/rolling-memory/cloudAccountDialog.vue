<!--
  BuildAdmin baAccount.vue 风格的「云端会员登录 + API Key 管理 + 云端记忆管理」一体化弹窗组件。
  1. 未登录态：云端模式采用申请制、逐步开放试用，在线注册已关闭；弹窗展示申请邮箱，已有账号可登录；
  2. 已登录态：展示会员信息（用户名/昵称/来源标签/专属命名空间 namespace）、默认 API Key 掩码与一键轮换复制、
     多设备独立 API Key 列表（新增/轮换/停用）、以及云端记忆管理（概览统计/近期记忆单元/在线检索/在线写入）。
  全部界面文案走 plugin.rolling-memory.cloudAccount.* 语言键（zh/en 同步）。
-->
<template>
    <el-dialog v-model="visible" :title="t('plugin.rolling-memory.cloudAccount.title')" width="min(780px, 95vw)" destroy-on-close append-to-body>
        <div v-loading="loading" :element-loading-text="cloudLoadingText" class="rm-cloud-account">
            <!-- 服务地址栏 -->
            <div class="ca-server-bar">
                <span class="ca-server-label">{{ t('plugin.rolling-memory.cloudAccount.serverLabel') }}</span>
                <el-input
                    v-model="form.base_url"
                    size="small"
                    :disabled="state.logged_in"
                    class="ca-server-input"
                />
                <el-tag v-if="state.logged_in" type="success" size="small" effect="dark">
                    {{ t('plugin.rolling-memory.cloudAccount.connected') }}
                </el-tag>
            </div>

            <!-- 未登录态：登录 Tab + 申请试用说明（云端模式采用申请制，在线注册已关闭） -->
            <template v-if="!state.logged_in">
                <el-alert type="warning" :closable="false" show-icon class="ca-alert" :title="t('plugin.rolling-memory.cloudAccount.applyIntro')" />

                <el-tabs v-model="authTab" class="ca-auth-tabs">
                    <el-tab-pane :label="t('plugin.rolling-memory.cloudAccount.tabLogin')" name="login">
                        <el-form label-position="top" @submit.prevent="onLogin">
                            <el-form-item :label="t('plugin.rolling-memory.cloudAccount.username')">
                                <el-input v-model="form.username" placeholder="alice_01" autocomplete="username" @keyup.enter="onLogin" />
                            </el-form-item>
                            <el-form-item :label="t('plugin.rolling-memory.cloudAccount.password')">
                                <el-input
                                    v-model="form.password"
                                    type="password"
                                    show-password
                                    placeholder="••••••••"
                                    autocomplete="current-password"
                                    @keyup.enter="onLogin"
                                />
                            </el-form-item>
                            <el-form-item>
                                <el-checkbox v-model="form.rotate_key">
                                    {{ t('plugin.rolling-memory.cloudAccount.rotateOnLogin') }}
                                </el-checkbox>
                            </el-form-item>
                            <el-button type="primary" class="ca-submit-btn" :loading="submitting" @click="onLogin">
                                {{ t('plugin.rolling-memory.cloudAccount.signInBtn') }}
                            </el-button>
                        </el-form>
                    </el-tab-pane>

                    <el-tab-pane :label="t('plugin.rolling-memory.cloudAccount.tabApply')" name="apply">
                        <p class="ca-apply-note">{{ t('plugin.rolling-memory.cloudAccount.applyNote') }}</p>
                        <a class="ca-apply-link" :href="applyMailto">{{ t('plugin.rolling-memory.cloudAccount.applyBtn') }}</a>
                        <p class="ca-apply-email">
                            {{ t('plugin.rolling-memory.cloudAccount.applyContact') }}
                            <a :href="applyMailto">pick@psychologylake.org.cn</a>
                        </p>
                        <p class="ca-apply-email">
                            {{ t('plugin.rolling-memory.cloudAccount.supportContact') }}
                            <a href="mailto:support@psychologylake.org.cn">support@psychologylake.org.cn</a>
                        </p>
                    </el-tab-pane>
                </el-tabs>
            </template>

            <!-- 已登录态：会员卡片 + API Key 管理 + 云端记忆管理 -->
            <template v-else>
                <div class="ca-profile-bar">
                    <div class="ca-profile-info">
                        <div class="ca-profile-name">
                            <strong>{{ state.user?.nickname || state.user?.username || state.namespace }}</strong>
                            <el-tag size="small" type="primary">{{ state.user?.source || 'Rolling-Memory-Plugin' }}</el-tag>
                            <el-tag size="small" type="info">namespace: {{ state.namespace }}</el-tag>
                        </div>
                        <div class="ca-profile-sub">
                            <span
                                >{{ t('plugin.rolling-memory.cloudAccount.keyMask') }} <code>{{ state.key_mask || '—' }}</code></span
                            >
                            <span
                                >{{ t('plugin.rolling-memory.cloudAccount.signedKeyMask') }} <code>{{ state.signed_key_mask || '—' }}</code></span
                            >
                            <span v-if="state.signed_key_expires_at">
                                {{ t('plugin.rolling-memory.cloudAccount.signedKeyExpires') }}
                                <code>{{ new Date(state.signed_key_expires_at * 1000).toLocaleString() }}</code>
                            </span>
                            <span v-if="state.key_file" class="ca-key-file">
                                {{ t('plugin.rolling-memory.cloudAccount.keyFile') }} <code>{{ state.key_file }}</code>
                            </span>
                        </div>
                    </div>
                    <div class="ca-profile-actions">
                        <el-button size="small" type="warning" plain :loading="submitting" @click="onRotateKey">
                            {{ t('plugin.rolling-memory.cloudAccount.rotateDefaultKey') }}
                        </el-button>
                        <el-button size="small" type="danger" plain :loading="submitting" @click="onLogout">
                            {{ t('plugin.rolling-memory.cloudAccount.signOut') }}
                        </el-button>
                    </div>
                </div>

                <!-- 数据面签名钥换取失败提示（端侧唯一记忆凭据，失败即无法上云） -->
                <el-alert
                    v-if="state.signed_key_error"
                    type="warning"
                    show-icon
                    class="ca-alert"
                    :title="t('plugin.rolling-memory.cloudAccount.signedKeyErrorTitle')"
                    :description="state.signed_key_error"
                />

                <!-- 一次性明文 Key 提示 -->
                <el-alert
                    v-if="freshPlainKey"
                    type="success"
                    show-icon
                    class="ca-alert"
                    :title="t('plugin.rolling-memory.cloudAccount.freshKeyTitle')"
                >
                    <div class="ca-fresh-key">
                        <code>{{ freshPlainKey }}</code>
                        <el-button size="small" type="primary" link @click="copyText(freshPlainKey)">
                            {{ t('plugin.rolling-memory.cloudAccount.copy') }}
                        </el-button>
                    </div>
                </el-alert>

                <el-tabs v-model="manageTab" class="ca-manage-tabs" @tab-change="onManageTabChange">
                    <!-- Tab 1: 云端记忆概览与在线读写 -->
                    <el-tab-pane :label="t('plugin.rolling-memory.cloudAccount.tabMemory')" name="memory">
                        <div class="ca-mem-stats" v-if="memStatus">
                            <el-statistic
                                :title="t('plugin.rolling-memory.cloudAccount.statActiveUnits')"
                                :value="memStatus.active_assertions || 0"
                            />
                            <el-statistic :title="t('plugin.rolling-memory.cloudAccount.statNodes')" :value="memStatus.nodes || 0" />
                            <el-statistic :title="t('plugin.rolling-memory.cloudAccount.statTraces')" :value="memStatus.traces || 0" />
                            <el-statistic :title="t('plugin.rolling-memory.cloudAccount.statConflicts')" :value="memStatus.conflicts_open || 0" />
                            <el-button size="small" @click="loadCloudMemoryStatusAndRecent">{{
                                t('plugin.rolling-memory.cloudAccount.refresh')
                            }}</el-button>
                        </div>

                        <div class="ca-mem-search">
                            <el-input
                                v-model="recallCue"
                                size="small"
                                clearable
                                :placeholder="t('plugin.rolling-memory.cloudAccount.recallPlaceholder')"
                                @keyup.enter="onCloudRecall"
                                @clear="recallItems = []"
                            >
                                <template #prepend>
                                    <span>Rolling Autonoetic Memory Engine</span>
                                </template>
                                <template #append>
                                    <el-button :loading="memBusy" @click="onCloudRecall">{{
                                        t('plugin.rolling-memory.cloudAccount.recallBtn')
                                    }}</el-button>
                                </template>
                            </el-input>
                        </div>

                        <!-- 在线快速新增云端记忆（S - X -> O） -->
                        <div class="ca-mem-write">
                            <el-input
                                v-model="writeForm.subject"
                                size="small"
                                :placeholder="t('plugin.rolling-memory.cloudAccount.subjectS')"
                                style="width: 140px"
                            />
                            <el-input
                                v-model="writeForm.rel"
                                size="small"
                                :placeholder="t('plugin.rolling-memory.cloudAccount.predicateX')"
                                style="width: 130px"
                            />
                            <el-input
                                v-model="writeForm.object"
                                size="small"
                                :placeholder="t('plugin.rolling-memory.cloudAccount.objectO')"
                                style="flex: 1"
                                @keyup.enter="onCloudRemember"
                            />
                            <el-button size="small" type="primary" plain :loading="memBusy" @click="onCloudRemember">
                                {{ t('plugin.rolling-memory.cloudAccount.addToCloud') }}
                            </el-button>
                        </div>

                        <div v-if="recallItems.length" class="ca-recall-box">
                            <div v-for="it in recallItems" :key="it.id" class="ca-recall-item">
                                <div class="ca-recall-head">
                                    <code>{{ it.id }}</code>
                                    <span>score: {{ Number(it.score || 0).toFixed(2) }}</span>
                                </div>
                                <MarkdownPreview :text="it.content" class="ca-recall-content" />
                            </div>
                        </div>

                        <el-table
                            v-else
                            :data="recentItems"
                            size="small"
                            border
                            stripe
                            class="ag-data-table"
                            header-cell-class-name="table-header-cell"
                            max-height="240"
                        >
                            <el-table-column prop="node_id" label="ID" width="95" show-overflow-tooltip />
                            <el-table-column prop="subject" :label="t('plugin.rolling-memory.cloudAccount.subjectS')" min-width="110" />
                            <el-table-column prop="rel" :label="t('plugin.rolling-memory.cloudAccount.predicateX')" min-width="100" />
                            <el-table-column :label="t('plugin.rolling-memory.cloudAccount.objectO')" min-width="150">
                                <template #default="{ row }">{{ row.object || row.object_literal || '—' }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.rolling-memory.cloudAccount.actions')" width="90" align="center">
                                <template #default="{ row }">
                                    <el-popconfirm
                                        :title="t('plugin.rolling-memory.cloudAccount.forgetConfirm')"
                                        @confirm="onCloudForget(row.node_id || row.id)"
                                    >
                                        <template #reference>
                                            <el-button size="small" link type="danger">
                                                {{ t('plugin.rolling-memory.cloudAccount.delete') }}
                                            </el-button>
                                        </template>
                                    </el-popconfirm>
                                </template>
                            </el-table-column>
                        </el-table>
                    </el-tab-pane>

                    <!-- Tab 2: 多设备 API Key 管理 -->
                    <el-tab-pane :label="t('plugin.rolling-memory.cloudAccount.tabDevices')" name="devices">
                        <div class="ca-dev-Create">
                            <el-input
                                v-model="newDeviceName"
                                size="small"
                                :placeholder="t('plugin.rolling-memory.cloudAccount.deviceNamePlaceholder')"
                                style="width: 240px"
                                @keyup.enter="onCreateDevice"
                            />
                            <el-select v-model="newDeviceKind" size="small" style="width: 120px">
                                <el-option label="Web / IDE" value="web" />
                                <el-option label="App" value="app" />
                                <el-option label="Hardware" value="hardware" />
                            </el-select>
                            <el-button size="small" type="primary" :loading="memBusy" @click="onCreateDevice">
                                {{ t('plugin.rolling-memory.cloudAccount.createKey') }}
                            </el-button>
                        </div>
                        <el-table
                            :data="devices"
                            size="small"
                            border
                            stripe
                            class="ag-data-table"
                            header-cell-class-name="table-header-cell"
                            max-height="260"
                        >
                            <el-table-column prop="id" label="ID" width="65" />
                            <el-table-column prop="name" :label="t('plugin.rolling-memory.cloudAccount.device')" min-width="120" />
                            <el-table-column prop="kind" :label="t('plugin.rolling-memory.cloudAccount.kind')" width="90" />
                            <el-table-column prop="mask" :label="t('plugin.rolling-memory.cloudAccount.colKeyMask')" min-width="150" />
                            <el-table-column :label="t('plugin.rolling-memory.cloudAccount.status')" width="80">
                                <template #default="{ row }">
                                    <el-tag size="small" :type="row.status === 1 ? 'success' : 'info'">
                                        {{
                                            row.status === 1
                                                ? t('plugin.rolling-memory.cloudAccount.statusActive')
                                                : t('plugin.rolling-memory.cloudAccount.statusRevoked')
                                        }}
                                    </el-tag>
                                </template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.rolling-memory.cloudAccount.actions')" width="150" align="center">
                                <template #default="{ row }">
                                    <el-button v-if="row.status === 1" size="small" link type="primary" :disabled="memBusy" @click="onRotateDevice(row.id)">
                                        {{ t('plugin.rolling-memory.cloudAccount.rotate') }}
                                    </el-button>
                                    <el-button v-if="row.status === 1" size="small" link type="danger" :disabled="memBusy" @click="onDisableDevice(row.id)">
                                        {{ t('plugin.rolling-memory.cloudAccount.revoke') }}
                                    </el-button>
                                </template>
                            </el-table-column>
                        </el-table>
                    </el-tab-pane>
                </el-tabs>
            </template>
        </div>
    </el-dialog>
</template>

<script setup lang="ts">
import { useLoadingProgressText } from '@/utils/loadingProgress'
import request from '@/utils/request'
import { ElMessage } from 'element-plus'
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import MarkdownPreview from './MarkdownPreview.vue'

const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'changed'): void }>()

const { t } = useI18n()

const visible = computed({
    get: () => props.modelValue,
    set: (v: boolean) => emit('update:modelValue', v),
})

const loading = ref(false)
const submitting = ref(false)
const memBusy = ref(false)
const { text: cloudLoadingText } = useLoadingProgressText(loading)
const authTab = ref<'login' | 'apply'>('login')
const manageTab = ref<'memory' | 'devices'>('memory')
const freshPlainKey = ref('')

const applyMailto = `mailto:pick@psychologylake.org.cn?subject=${encodeURIComponent('Rolling Memory cloud trial application')}`

const form = reactive({
    base_url: '',
    username: '',
    password: '',
    rotate_key: false,
})

const state = reactive<Record<string, any>>({
    logged_in: false,
    base_url: '',
    namespace: '',
    key_mask: '',
    key_file: '',
    // 记忆面凭据 = 数据面直连签名钥（`mem2.*`）：只回掩码 / 到期时刻 / 换取错误原文
    has_signed_key: false,
    signed_key_mask: '',
    signed_key_expires_at: 0,
    signed_key_error: '',
    user: null,
})

const memStatus = ref<Record<string, any> | null>(null)
const recentItems = ref<any[]>([])
const recallCue = ref('')
// 上游检索档参数（wire 值，仅透传，不在界面展示）
const recallTier = ref('zero')
const recallItems = ref<any[]>([])
const writeForm = reactive({
    subject: '',
    rel: '',
    object: '',
})
const devices = ref<any[]>([])
const newDeviceName = ref('')
const newDeviceKind = ref('web')

const postPlugin = async (path: string, data: Record<string, any> = {}) => {
    const res: any = await request({ url: `/admin/plugin/rolling-memory${path}`, method: 'post', data })
    return res?.data?.data ?? res?.data ?? {}
}

const loadState = async () => {
    loading.value = true
    try {
        const d = await postPlugin('/cloud/account/state')
        Object.assign(state, d)
        if (d.base_url) form.base_url = d.base_url
        if (state.logged_in) {
            await loadCloudMemoryStatusAndRecent()
        }
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        loading.value = false
    }
}

const onLogin = async () => {
    if (!form.username.trim() || !form.password) {
        return
    }
    submitting.value = true
    freshPlainKey.value = ''
    try {
        const d = await postPlugin('/cloud/account/login', form)
        Object.assign(state, d)
        form.password = ''
        if (d.api_key) freshPlainKey.value = d.api_key
        ElMessage.success(t('plugin.rolling-memory.cloudAccount.msgSignedIn'))
        emit('changed')
        await loadCloudMemoryStatusAndRecent()
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        submitting.value = false
    }
}

const onLogout = async () => {
    submitting.value = true
    try {
        await postPlugin('/cloud/account/logout')
        state.logged_in = false
        freshPlainKey.value = ''
        form.password = ''
        memStatus.value = null
        recentItems.value = []
        recallItems.value = []
        devices.value = []
        ElMessage.success(t('plugin.rolling-memory.cloudAccount.msgSignedOut'))
        emit('changed')
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        submitting.value = false
    }
}

const onRotateKey = async () => {
    submitting.value = true
    try {
        const d = await postPlugin('/cloud/account/rotate-key')
        if (d.api_key) freshPlainKey.value = d.api_key
        if (d.key_mask) state.key_mask = d.key_mask
        ElMessage.success(t('plugin.rolling-memory.cloudAccount.msgKeyRotated'))
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        submitting.value = false
    }
}

const loadCloudMemoryStatusAndRecent = async () => {
    memBusy.value = true
    try {
        memStatus.value = await postPlugin('/cloud/account/memory', { action: 'status' })
        const rec = await postPlugin('/cloud/account/memory', { action: 'recent', limit: 20 })
        recentItems.value = rec.items || []
    } catch {
        // ignore transient network errors on initial open
    } finally {
        memBusy.value = false
    }
}

const onCloudRecall = async () => {
    if (!recallCue.value.trim()) {
        recallItems.value = []
        return
    }
    memBusy.value = true
    try {
        const d = await postPlugin('/cloud/account/memory', { action: 'recall', cue: recallCue.value, tier: recallTier.value, top_k: 12 })
        recallItems.value = d.items || []
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        memBusy.value = false
    }
}

const onCloudRemember = async () => {
    const s = writeForm.subject.trim()
    const x = writeForm.rel.trim()
    const o = writeForm.object.trim()
    if (!s || !x || !o) {
        ElMessage.warning(t('plugin.rolling-memory.cloudAccount.msgFillAll'))
        return
    }
    memBusy.value = true
    try {
        await postPlugin('/cloud/account/memory', {
            action: 'remember',
            subject: s,
            rel: x,
            object: o,
            content: `${s} ${x} ${o}`,
        })
        writeForm.subject = ''
        writeForm.rel = ''
        writeForm.object = ''
        ElMessage.success(t('plugin.rolling-memory.cloudAccount.msgSaved'))
        await loadCloudMemoryStatusAndRecent()
        emit('changed')
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        memBusy.value = false
    }
}

const onCloudForget = async (nodeId: string) => {
    if (!nodeId) return
    memBusy.value = true
    try {
        await postPlugin('/cloud/account/memory', {
            action: 'forget',
            node_id: nodeId,
            reason: 'retracted via local cloud console',
        })
        ElMessage.success(t('plugin.rolling-memory.cloudAccount.msgRetracted'))
        await loadCloudMemoryStatusAndRecent()
        emit('changed')
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        memBusy.value = false
    }
}

const loadDevices = async () => {
    memBusy.value = true
    try {
        const d = await postPlugin('/cloud/account/devices', { action: 'list' })
        devices.value = d.items || []
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        memBusy.value = false
    }
}

const onCreateDevice = async () => {
    memBusy.value = true
    try {
        const d = await postPlugin('/cloud/account/devices', {
            action: 'create',
            name: newDeviceName.value || 'Rolling-Memory-Client',
            kind: newDeviceKind.value,
        })
        if (d.key) freshPlainKey.value = d.key
        newDeviceName.value = ''
        await loadDevices()
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        memBusy.value = false
    }
}

const onRotateDevice = async (id: number) => {
    memBusy.value = true
    try {
        const d = await postPlugin('/cloud/account/devices', { action: 'rotate', id })
        if (d.key) freshPlainKey.value = d.key
        await loadDevices()
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        memBusy.value = false
    }
}

const onDisableDevice = async (id: number) => {
    memBusy.value = true
    try {
        await postPlugin('/cloud/account/devices', { action: 'disable', id })
        await loadDevices()
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        memBusy.value = false
    }
}

const onManageTabChange = (tab: any) => {
    if (tab === 'devices') loadDevices()
    else loadCloudMemoryStatusAndRecent()
}

const copyText = async (txt: string) => {
    try {
        await navigator.clipboard?.writeText(txt)
        ElMessage.success(t('plugin.rolling-memory.cloudAccount.msgCopied'))
    } catch {
        // ignore
    }
}

watch(visible, (v) => {
    if (v) {
        loadState()
    } else {
        form.password = ''
        freshPlainKey.value = ''
    }
})
</script>

<style scoped lang="scss">
.ca-server-bar {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 12px;
}
.ca-server-label {
    font-size: 13px;
    white-space: nowrap;
    color: var(--el-text-color-regular);
}
.ca-server-input {
    flex: 1;
}
.ca-alert {
    margin-bottom: 12px;
}
.ca-submit-btn {
    width: 100%;
    margin-top: 6px;
}
.ca-apply-note {
    margin: 0 0 12px;
    color: var(--el-text-color-regular);
    line-height: 1.7;
}
.ca-apply-link {
    display: inline-block;
    padding: 8px 16px;
    border-radius: 6px;
    background: var(--el-color-primary);
    color: #fff;
    text-decoration: none;
}
.ca-apply-email {
    margin: 14px 0 0;
    color: var(--el-text-color-secondary);
}
.ca-apply-email a {
    color: var(--el-color-primary);
}
.ca-profile-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 12px;
    background: var(--el-fill-color-light);
    border-radius: 6px;
    margin-bottom: 12px;
    flex-wrap: wrap;
}
.ca-profile-name {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    font-size: 15px;
}
.ca-profile-sub {
    margin-top: 4px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
    display: flex;
    gap: 14px;
    flex-wrap: wrap;
}
.ca-fresh-key {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-top: 4px;
    word-break: break-all;
}
.ca-mem-stats {
    display: flex;
    align-items: center;
    justify-content: space-around;
    padding: 8px 0 14px;
    border-bottom: 1px solid var(--el-border-color-lighter);
    margin-bottom: 12px;
}
.ca-mem-search {
    margin-bottom: 10px;
}
.ca-mem-write {
    display: flex;
    gap: 8px;
    margin-bottom: 12px;
    align-items: center;
}
.ca-recall-box {
    max-height: 240px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 8px;
}
.ca-recall-item {
    padding: 8px 10px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 4px;
    background: var(--el-fill-color-blank);
}
.ca-recall-head {
    display: flex;
    justify-content: space-between;
    font-size: 12px;
    color: var(--el-text-color-secondary);
    margin-bottom: 4px;
}
.ca-recall-content {
    margin: 0;
    font-size: 12px;
    line-height: 1.45;
}
.ca-dev-Create {
    display: flex;
    gap: 8px;
    margin-bottom: 10px;
    align-items: center;
}
</style>
