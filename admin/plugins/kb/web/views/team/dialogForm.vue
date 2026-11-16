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
                    <!-- 组名按文本录入（用户组主体面在本部署不可用，故不做远端选择与存在性校验） -->
                    <el-form-item :label="t('plugin.kb.team.groupName')" prop="group_name">
                        <el-input v-model="formItems.group_name" :placeholder="t('common.pleaseEnter', { field: t('plugin.kb.team.groupName') })" />
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.team.displayName')" prop="display_name">
                        <el-input
                            v-model="formItems.display_name"
                            :placeholder="t('common.pleaseEnter', { field: t('plugin.kb.team.displayName') })"
                        />
                    </el-form-item>

                    <el-form-item :label="t('common.status')" prop="status">
                        <el-switch v-model="formItems.status" :active-value="1" :inactive-value="0" />
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
import { useTemplateRef } from 'vue'
import { useI18n } from 'vue-i18n'

interface Props {
    manager: TableManagerInstance
}

defineProps<Props>()
const formItems = defineModel<AnyObj>('formItems', { required: true })

const config = useConfig()
const formRef = useTemplateRef('formRef')

const { t } = useI18n()

const rules: Partial<Record<string, FormItemRule[]>> = {
    group_name: [buildValidatorRule({ name: 'required', title: t('plugin.kb.team.groupName') })],
}
</script>

<style scoped lang="scss"></style>
