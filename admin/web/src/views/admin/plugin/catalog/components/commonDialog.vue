<!--
  界面结构与组件用法移植自 BuildAdmin v2.3.9
  https://gitee.com/wonderful-code/buildadmin
  Apache-2.0，Copyright 妙码生花
  已修改：单弹窗按 loading/preflight/done/failed 切换内容；接入 catalog 预演/安装；
  删除了上传安装、覆盖/丢弃冲突、账号、支付与 BuildAdmin store 依赖。
-->
<template>
    <el-dialog
        v-model="visible"
        :title="title"
        width="min(780px, 96vw)"
        class="common-dialog"
        :close-on-click-modal="false"
        :close-on-press-escape="!installing"
        :show-close="!installing"
        :destroy-on-close="true"
    >
        <el-scrollbar height="560">
            <div v-if="state === 'loading'" v-loading="true" :element-loading-text="loadingText" class="common-loading"></div>

            <template v-else-if="state === 'preflight' && preflight">
                <el-alert v-if="!preflight.installable" class="preflight-alert" type="warning" :closable="false" show-icon :title="reasonMessage" />

                <el-descriptions :column="2" border size="small">
                    <el-descriptions-item :label="t('plugin.source.catalog.detail.uid')">{{ preflight.entry.uid }}</el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.source.catalog.detail.version')">{{ preflight.entry.version }}</el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.source.catalog.detail.source')">{{ sourceName || '-' }}</el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.source.catalog.detail.size')">{{
                        formatBytes(preflight.package.size)
                    }}</el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.source.catalog.detail.sha256')" :span="2">
                        <span class="mono">{{ preflight.package.sha256 }}</span>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.source.catalog.detail.signed')" :span="2">
                        {{ preflight.package.signed ? t('plugin.source.catalog.version.signedYes') : t('plugin.source.catalog.version.signedNo') }}
                    </el-descriptions-item>
                </el-descriptions>

                <!-- 签名三态必须分开：未签名 / 有签名但本机未配置公钥 / 公钥配置有误 -->
                <el-alert
                    v-if="preflight.trusted_keys_error"
                    class="preflight-alert"
                    type="error"
                    :closable="false"
                    show-icon
                    :title="t('plugin.source.catalog.signature.trustedKeysInvalid')"
                />
                <el-alert
                    v-else-if="!preflight.signature_present"
                    class="preflight-alert"
                    type="warning"
                    :closable="false"
                    show-icon
                    :title="t('plugin.source.catalog.signature.unsigned')"
                />
                <el-alert
                    v-else-if="!preflight.trusted_keys_configured"
                    class="preflight-alert"
                    type="warning"
                    :closable="false"
                    show-icon
                    :title="t('plugin.source.catalog.signature.noTrustedKeys')"
                />
                <el-alert
                    v-else-if="preflight.signature_verified"
                    class="preflight-alert"
                    type="success"
                    :closable="false"
                    show-icon
                    :title="t('plugin.source.catalog.signature.verified')"
                />
                <el-alert
                    v-else
                    class="preflight-alert"
                    type="warning"
                    :closable="false"
                    show-icon
                    :title="t('plugin.source.catalog.signature.unverified')"
                />

                <InstallConflict v-if="targets.length" :targets="targets" />
                <div v-if="conflictItems.length" class="conflict-block">
                    <div class="conflict-title">{{ t('plugin.source.catalog.conflict.items') }}</div>
                    <el-tag v-for="item in conflictItems" :key="item" type="danger" class="conflict-tag">{{ item }}</el-tag>
                    <div class="conflict-hint">{{ t('plugin.source.catalog.conflict.itemsHint') }}</div>
                </div>

                <div class="report-title">{{ t('plugin.source.catalog.preflight.report') }}</div>
                <pre class="report">{{ preflight.report || '-' }}</pre>

                <el-checkbox v-if="intent === 'install' && preflight.allow_unsigned_required" v-model="allowUnsigned" class="unsigned-check">
                    {{ unsignedText }}
                </el-checkbox>
            </template>

            <CommonDone v-else-if="state === 'done' && result" :result="result" @close="emit('close')" />

            <div v-else-if="state === 'failed'" class="failed-box">
                <el-result icon="error" :title="errorMessage" :sub-title="errorHint">
                    <template #extra>
                        <div v-if="error?.message" class="error-raw">{{ error.message }}</div>
                        <div class="error-hint">{{ t('plugin.source.catalog.failed.cleanup') }}</div>
                    </template>
                </el-result>
            </div>
        </el-scrollbar>

        <template #footer>
            <template v-if="state === 'preflight'">
                <el-button @click="emit('close')">{{ intent === 'install' ? t('common.cancel') : t('common.close') }}</el-button>
                <el-button
                    v-if="intent === 'install'"
                    type="primary"
                    :loading="installing"
                    :disabled="!preflight?.installable || (preflight?.allow_unsigned_required && !allowUnsigned)"
                    @click="emit('confirm', allowUnsigned)"
                >
                    {{ t('plugin.source.catalog.preflight.confirmInstall') }}
                </el-button>
            </template>
            <el-button v-else-if="(state === 'failed' || state === 'loading') && !installing" @click="emit('close')">{{
                t('common.close')
            }}</el-button>
        </template>
    </el-dialog>
</template>

<script setup lang="ts">
import { catalogErrorKey, formatBytes, type CatalogInstallData, type CatalogPreflightData, type ErrorInfo } from '@/api/plugin/source'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import CommonDone from './commonDone.vue'
import InstallConflict from './installConflict.vue'

type CatalogDialogState = 'loading' | 'preflight' | 'done' | 'failed'

interface Props {
    state: CatalogDialogState
    intent: 'preflight' | 'install'
    loadingText?: string
    preflight?: CatalogPreflightData | null
    result?: CatalogInstallData | null
    error?: ErrorInfo | null
    installing?: boolean
    /** 预演回执的 entry 不含来源名；由页面从当前 catalog 行带入 */
    sourceName?: string
}

const props = withDefaults(defineProps<Props>(), {
    loadingText: '',
    preflight: null,
    result: null,
    error: null,
    installing: false,
    sourceName: '',
})

const emit = defineEmits<{
    confirm: [allowUnsigned: boolean]
    close: []
}>()

const visible = defineModel<boolean>({ required: true })
const { t, te } = useI18n()

const title = computed(() => {
    if (props.state === 'loading') {
        return props.intent === 'install' ? t('plugin.source.catalog.dialog.installingTitle') : t('plugin.source.catalog.dialog.preflightTitle')
    }
    if (props.state === 'preflight') return t('plugin.source.catalog.dialog.preflightTitle')
    if (props.state === 'done') return t('plugin.source.catalog.dialog.doneTitle')
    return t('plugin.source.catalog.dialog.failedTitle')
})

const errorKey = computed(() => catalogErrorKey(props.error?.code, te))
const errorMessage = computed(() => t(`plugin.source.errors.${errorKey.value}.message`))
const errorHint = computed(() => t(`plugin.source.errors.${errorKey.value}.hint`))

// 预演失败但 code=0 时，精确分支码在 error_code；reason 只用于“为何不可安装”
const reasonKey = computed(() => catalogErrorKey(props.preflight?.error_code || props.preflight?.reason, te))
const reasonMessage = computed(() => t(`plugin.source.errors.${reasonKey.value}.message`))

const allowUnsigned = ref(false)
watch(
    () => props.preflight,
    () => {
        allowUnsigned.value = false
    }
)

const unsignedText = computed(() =>
    props.preflight?.signature_present
        ? t('plugin.source.catalog.preflight.allowUnsignedUnverified')
        : t('plugin.source.catalog.preflight.allowUnsigned')
)

const targets = computed(() => props.preflight?.existing_targets ?? [])
// 后端 conflicts 是 []string（规则名/路径、实体表、目标依次追加）；目标已在 InstallConflict 单列
const conflictItems = computed(() => {
    const targetSet = new Set(targets.value)
    return (props.preflight?.conflicts ?? []).filter((item) => !targetSet.has(item))
})
</script>

<style scoped lang="scss">
.common-loading {
    height: 400px;
}
.preflight-alert {
    margin: 10px 0;
}
.conflict-block {
    margin: 10px 0;
}
.conflict-title {
    margin-bottom: 6px;
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);
}
.conflict-hint {
    margin-top: 6px;
    font-size: 12px;
    line-height: 18px;
    color: var(--el-text-color-secondary);
}
.conflict-tag {
    margin: 0 6px 6px 0;
}
.report-title {
    margin-top: 12px;
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);
}
.report {
    max-height: 220px;
    overflow: auto;
    padding: 10px;
    margin: 6px 0 0;
    font-family: var(--el-font-family-mono, monospace);
    font-size: 12px;
    line-height: 18px;
    white-space: pre-wrap;
    word-break: break-all;
    background-color: var(--el-fill-color-light);
    border-radius: var(--el-border-radius-base);
}
.unsigned-check {
    margin-top: 12px;
}
.failed-box {
    padding-top: 20px;
}
.error-raw {
    margin-bottom: 8px;
    font-size: 12px;
    word-break: break-all;
    color: var(--el-text-color-secondary);
}
.error-hint {
    font-size: 12px;
    line-height: 20px;
    color: var(--el-text-color-secondary);
}
.mono {
    font-family: var(--el-font-family-mono, monospace);
    word-break: break-all;
}
</style>
