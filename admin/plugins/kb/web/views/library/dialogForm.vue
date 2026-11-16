<!-- plugin: kb v1.0.0 -->
<template>
    <!-- 对话框表单 -->
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
                {{ manager.form.operate == 'create' ? t('common.add') : t('common.edit') }}
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
                    <!-- 属主只有系统级一种：用户级主体面在本部署不可用（口径与后端一致），故不提供该选项 -->
                    <el-form-item :label="t('plugin.kb.library.ownerKind')" prop="owner_kind">
                        <el-radio-group v-model="formItems.owner_kind" :disabled="manager.form.operate == 'update'" class="ag-input-item-radio">
                            <el-radio value="system" :border="true">{{ t('plugin.kb.library.ownerKindSystem') }}</el-radio>
                        </el-radio-group>
                        <div v-if="manager.form.operate == 'update'" class="form-hint">{{ t('plugin.kb.library.ownerLocked') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.library.name')" prop="name">
                        <el-input
                            type="string"
                            v-model="formItems.name"
                            maxlength="128"
                            show-word-limit
                            :placeholder="t('common.pleaseEnter', { field: t('plugin.kb.library.name') })"
                        ></el-input>
                        <div class="form-hint">{{ t('plugin.kb.library.nameHint') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.library.description')" prop="description">
                        <el-input
                            type="textarea"
                            :rows="3"
                            v-model="formItems.description"
                            maxlength="512"
                            show-word-limit
                            :placeholder="t('common.pleaseEnter', { field: t('plugin.kb.library.description') })"
                        ></el-input>
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.library.chunkConfig')" prop="chunk_config">
                        <el-input
                            type="textarea"
                            :rows="6"
                            v-model="formItems.chunk_config"
                            :placeholder="t('plugin.kb.library.chunkConfigPlaceholder')"
                        ></el-input>
                        <div class="form-hint">{{ t('plugin.kb.library.chunkConfigHint') }}</div>
                    </el-form-item>

                    <el-form-item :label="t('common.status')" prop="status">
                        <el-switch
                            v-model="formItems.status"
                            :active-value="1"
                            :inactive-value="0"
                            :active-text="t('common.enable')"
                            :inactive-text="t('common.disable')"
                        />
                    </el-form-item>
                </el-form>
            </div>
        </el-scrollbar>
        <template #footer>
            <div :style="'width: calc(100% - ' + manager.form.labelWidth! / 1.8 + 'px)'">
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
import type { FormItemRule } from 'element-plus'
import { reactive, useTemplateRef } from 'vue'
import { useI18n } from 'vue-i18n'

interface Props {
    manager: TableManagerInstance
}

defineProps<Props>()
const { t } = useI18n()
const config = useConfig()
const formRef = useTemplateRef('formRef')
const formItems = defineModel<AnyObj>('formItems', { required: true })

// 切片配置：可选，留空 = 未配置；非空须为 JSON 对象（写入侧另有值域/未知键校验）
const validatorChunkConfig = (rule: any, val: string, callback: Function) => {
    if (!val || !String(val).trim()) {
        return callback()
    }
    try {
        const parsed = JSON.parse(String(val))
        if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
            return callback(new Error(t('plugin.kb.library.chunkConfigInvalid')))
        }
    } catch {
        return callback(new Error(t('plugin.kb.library.chunkConfigInvalid')))
    }
    return callback()
}

const rules: Partial<Record<string, FormItemRule[]>> = reactive({
    name: [buildValidatorRule({ name: 'required', title: t('plugin.kb.library.name') })],
    chunk_config: [
        {
            validator: validatorChunkConfig,
            trigger: 'blur',
        },
    ],
})
</script>

<style scoped lang="scss">
.form-hint {
    width: 100%;
    margin-top: 4px;
    color: var(--el-text-color-secondary);
    font-size: 12px;
    line-height: 18px;
}
</style>
