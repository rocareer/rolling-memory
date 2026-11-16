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
                    <el-form-item :label="t('plugin.kb.tag.groupId')" prop="group_id">
                        <RemoteSelect
                            v-model="formItems.group_id"
                            pk="id"
                            field="name"
                            remote-url="/admin/plugin/kb/tag_group/list"
                            :placeholder="t('common.pleaseSelect', { field: t('plugin.kb.tag.groupId') })"
                            :empty-values="[null, 0]"
                            :value-on-clear="0"
                        />
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.tag.name')" prop="name">
                        <el-input v-model="formItems.name" :placeholder="t('common.pleaseEnter', { field: t('plugin.kb.tag.name') })" />
                    </el-form-item>

                    <el-form-item :label="t('common.weigh')" prop="weigh">
                        <el-input-number
                            class="w100"
                            controls-position="right"
                            v-model="formItems.weigh"
                            :placeholder="t('common.pleaseEnter', { field: t('common.weigh') })"
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
import RemoteSelect from '@/components/agInput/components/remoteSelect.vue'
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
    group_id: [buildValidatorRule({ name: 'required', title: t('plugin.kb.tag.groupId') })],
    name: [buildValidatorRule({ name: 'required', title: t('plugin.kb.tag.name') })],
}
</script>

<style scoped lang="scss"></style>
