<!-- plugin: ai v0.2.0 -->
<!--
  渠道新增/编辑表单（独立 dialogForm，宿主惯例）。
  字段与默认值：status=1、timeout=60、priority=1、weight=1、fail_threshold=0、cooldown_seconds=0、
  vendor=openai、driver=''（空 = 默认 OpenAI 兼容，与历史渠道逐字一致）、supplier_id=0（0 = 独立渠道）、
  rpm_limit=0、tpm_limit=0（0 = 跟随插件设置的渠道 Gate 缺省）。
  api_key 留空或含 * 一律「不修改」（后端同口径）；base_url 写入期过 http(s) 校验，连接期另过出站护栏。
  厂商下拉：vendor 是展示标签，supplier 是继承来源（渠道字段留空时继承厂商的地址与凭证）。
  厂商列表由父页（index.vue）一次取回后传入，失败时降级为「独立渠道 + 手填 ID」，不阻塞表单。
  驱动下拉：候选取自驱动注册表的已注册名（driver_registry.go:49-52 的 openai/deepseek/dashscope/
  responses + driver_tts.go:42 的 dashscope_tts，即 GetSupported() 的返回集），另开
  filterable+allow-create，注册表新增驱动时可直接输入名称。渠道写入只做形状校验
  （handler_channel.go 的 normalizeDriverName：字母/数字/_ - .），未注册名要到调用/探活时才成
  local 错误 ⇒ 这里给候选而不是硬闭集。
-->
<template>
    <el-dialog
        class="ag-operate-dialog"
        :close-on-click-modal="false"
        :model-value="['create', 'update'].includes(manager.form.operate!)"
        @close="manager.toggleForm"
        :destroy-on-close="true"
        :draggable="true"
    >
        <template #header>
            <div class="title">
                {{ manager.form.operate === 'create' ? t('plugin.ai.channel.form.add') : t('plugin.ai.channel.form.edit') }}
            </div>
        </template>
        <el-scrollbar v-loading="manager.form.loading" class="ag-table-form-scrollbar">
            <div
                class="ag-operate-form"
                :class="'ag-' + manager.form.operate + '-form'"
                :style="config.layout.shrink ? '' : 'width: calc(100% - ' + manager.form.labelWidth! / 2 + 'px)'"
            >
                <el-form
                    ref="formRef"
                    @submit.prevent=""
                    @keyup.enter="manager.submitForm(formRef)"
                    :model="formItems"
                    :label-position="config.layout.shrink ? 'top' : 'right'"
                    :label-width="manager.form.labelWidth + 'px'"
                    :rules="rules"
                    v-if="!manager.form.loading"
                >
                    <el-form-item :label="t('plugin.ai.channel.form.name')" prop="name">
                        <el-input v-model="formItems.name" maxlength="50" :placeholder="t('plugin.ai.channel.form.namePlaceholder')" />
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.vendor')" prop="vendor">
                        <el-select v-model="formItems.vendor" class="w100">
                            <el-option v-for="item in vendorOptions" :key="item.value" :label="item.label" :value="item.value" />
                        </el-select>
                        <div class="form-tip">{{ t('plugin.ai.channel.form.vendorTip') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.driver')" prop="driver">
                        <el-select
                            v-model="formItems.driver"
                            class="w100"
                            filterable
                            allow-create
                            default-first-option
                            :reserve-keyword="false"
                        >
                            <el-option v-for="item in driverOptions" :key="item.value" :label="item.label" :value="item.value" />
                        </el-select>
                        <div class="form-tip">{{ t('plugin.ai.channel.form.driverTip') }}</div>
                        <div class="form-tip">{{ t('plugin.ai.channel.driver.freeInputTip') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.supplier')" prop="supplier_id">
                        <el-select
                            v-model="formItems.supplier_id"
                            class="w100"
                            clearable
                            filterable
                            allow-create
                            default-first-option
                            :reserve-keyword="false"
                            :placeholder="t('plugin.ai.channel.supplierPick.placeholder')"
                        >
                            <el-option :label="t('plugin.ai.channel.supplierPick.none')" :value="0" />
                            <el-option
                            v-for="item in supplierOptions"
                            :key="item.value"
                            :label="item.label"
                            :value="item.value"
                            />
                        </el-select>
                        <div class="form-tip">{{ t('plugin.ai.channel.supplierPick.inheritTip') }}</div>
                        <div v-if="supplierLoadFailed" class="form-tip warn">{{ t('plugin.ai.channel.supplierPick.loadFailed') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.baseUrl')" prop="base_url">
                        <el-input v-model="formItems.base_url" maxlength="255" :placeholder="t('plugin.ai.channel.form.baseUrlPlaceholder')" />
                        <div class="form-tip">{{ t('plugin.ai.channel.form.baseUrlTip') }}</div>
                        <div v-if="hasSupplier" class="form-tip">{{ t('plugin.ai.channel.form.baseUrlInheritTip') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.apiKey')" prop="api_key">
                        <el-input
                            v-model="formItems.api_key"
                            type="password"
                            show-password
                            autocomplete="new-password"
                            maxlength="255"
                            :placeholder="t('plugin.ai.channel.form.apiKeyPlaceholder')"
                        />
                        <div class="form-tip">{{ t('plugin.ai.channel.form.apiKeyTip') }}</div>
                        <div v-if="hasSupplier" class="form-tip">{{ t('plugin.ai.channel.form.apiKeyInheritTip') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.model')" prop="model">
                        <el-input v-model="formItems.model" maxlength="100" :placeholder="t('plugin.ai.channel.form.modelPlaceholder')" />
                        <div class="form-tip">{{ t('plugin.ai.channel.form.modelTip') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.timeout')" prop="timeout">
                        <el-input-number v-model="formItems.timeout" class="w100" :min="0" :max="600" :precision="0" controls-position="right" />
                        <div class="form-tip">{{ t('plugin.ai.channel.form.timeoutTip') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.priority')" prop="priority">
                        <el-input-number v-model="formItems.priority" class="w100" :min="0" :precision="0" controls-position="right" />
                        <div class="form-tip">{{ t('plugin.ai.channel.form.priorityTip') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.weight')" prop="weight">
                        <el-input-number v-model="formItems.weight" class="w100" :min="0" :precision="0" controls-position="right" />
                        <div class="form-tip">{{ t('plugin.ai.channel.form.weightTip') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.rpm')" prop="rpm_limit">
                        <el-input-number v-model="formItems.rpm_limit" class="w100" :min="0" :precision="0" controls-position="right" />
                        <div class="form-tip">{{ t('plugin.ai.channel.form.rpmTip') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.tpm')" prop="tpm_limit">
                        <el-input-number v-model="formItems.tpm_limit" class="w100" :min="0" :precision="0" controls-position="right" />
                        <div class="form-tip">{{ t('plugin.ai.channel.form.tpmTip') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.failThreshold')" prop="fail_threshold">
                        <el-input-number v-model="formItems.fail_threshold" class="w100" :min="0" :precision="0" controls-position="right" />
                        <div class="form-tip">{{ t('plugin.ai.channel.form.failThresholdTip') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.cooldown')" prop="cooldown_seconds">
                        <el-input-number v-model="formItems.cooldown_seconds" class="w100" :min="0" :precision="0" controls-position="right" />
                        <div class="form-tip">{{ t('plugin.ai.channel.form.cooldownTip') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.status')" prop="status">
                        <el-switch v-model="formItems.status" :active-value="1" :inactive-value="0" />
                    </el-form-item>

                    <el-form-item :label="t('plugin.ai.channel.form.remark')" prop="remark">
                        <el-input
                            @keyup.enter.stop=""
                            @keyup.ctrl.enter="manager.submitForm(formRef)"
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
                <el-button v-blur @click="manager.toggleForm()">{{ t('common.cancel') }}</el-button>
                <el-button v-blur :loading="manager.form.submitLoading" @click="manager.submitForm(formRef)" type="primary">
                    {{ manager.form.operatePKs && manager.form.operatePKs.length > 1 ? t('common.saveAndContinue') : t('common.save') }}
                </el-button>
            </div>
        </template>
    </el-dialog>
</template>

<script setup lang="ts">
import { useConfig } from '@/stores/config'
import { buildValidatorRule } from '@/utils/validate'
import type { FormInstance, FormItemRule } from 'element-plus'
import { computed, reactive, useTemplateRef } from 'vue'
import { useI18n } from 'vue-i18n'

interface Props {
    manager: TableManagerInstance
    // 厂商候选（父页一次取回；失败时为空数组 + supplierLoadFailed）
    suppliers?: SupplierOption[]
    supplierLoadFailed?: boolean
}

interface SupplierOption {
    value: number
    label: string
}

const props = withDefaults(defineProps<Props>(), {
    suppliers: () => [],
    supplierLoadFailed: false,
})
const { t } = useI18n()
const config = useConfig()
const formRef = useTemplateRef<FormInstance>('formRef')
const formItems = defineModel<AnyObj>('formItems', { required: true })

const supplierOptions = computed(() => props.suppliers)
// 挂了厂商才提示「可留空继承」（此时后端按厂商字段回落）
const hasSupplier = computed(() => Number(formItems.value.supplier_id ?? 0) > 0)

// 驱动候选：驱动注册表的已注册名（driver_registry.go:49-52 + driver_tts.go:42 = GetSupported() 的集合）
// + 自由输入（filterable + allow-create；注册表可扩展，这里不把闭集钉死在前端）
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

const validateBaseUrl = (_rule: unknown, value: string, callback: (error?: Error) => void) => {
    // 挂厂商时可留空：运行时继承厂商地址——与后端 handler_channel.go 的准入逐字同口径
    // （挂厂商 supplier_id>0 时可留空 / 未挂厂商必填）。hasSupplier 在这里**动态**读，
    // 因此「先留空、再选厂商」也能通过提交前的整体校验（submitForm 会再 validate 一遍）。
    if (!value) {
        if (!hasSupplier.value) {
            callback(new Error(t('plugin.ai.channel.form.rules.baseUrlRequired')))
            return
        }
        callback()
        return
    }
    if (!/^https?:\/\/[^\s]+$/i.test(value)) {
        callback(new Error(t('plugin.ai.channel.form.rules.baseUrlInvalid')))
        return
    }
    callback()
}

const validateTimeout = (_rule: unknown, value: number, callback: (error?: Error) => void) => {
    const timeout = Number(value)
    if (!Number.isFinite(timeout) || timeout < 0 || timeout > 600) {
        callback(new Error(t('plugin.ai.channel.form.rules.timeoutRange')))
        return
    }
    callback()
}

const rules = reactive<Partial<Record<string, FormItemRule[]>>>({
    name: [buildValidatorRule({ name: 'required', title: t('plugin.ai.channel.form.name') })],
    base_url: [{ validator: validateBaseUrl, trigger: 'blur' }],
    model: [buildValidatorRule({ name: 'required', title: t('plugin.ai.channel.form.model') })],
    timeout: [{ validator: validateTimeout, trigger: 'change' }],
})
</script>

<style scoped lang="scss">
.form-tip {
    width: 100%;
    margin-top: 2px;
    font-size: 12px;
    line-height: 18px;
    color: var(--el-text-color-secondary);

    &.warn {
        color: var(--el-color-warning);
    }
}

.dialog-footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
}
</style>
