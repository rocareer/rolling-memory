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
                    <el-form-item :label="t('plugin.kb.tag.rule.libraryId')" prop="library_id">
                        <RemoteSelect
                            v-model="formItems.library_id"
                            pk="id"
                            field="name"
                            remote-url="/admin/plugin/kb/library/list"
                            :placeholder="t('common.pleaseSelect', { field: t('plugin.kb.tag.rule.libraryId') })"
                            :empty-values="[null, 0]"
                            :value-on-clear="0"
                        />
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.tag.rule.name')" prop="name">
                        <el-input v-model="formItems.name" :placeholder="t('common.pleaseEnter', { field: t('plugin.kb.tag.rule.name') })" />
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.tag.rule.enabled')" prop="enabled">
                        <el-switch v-model="formItems.enabled" :active-value="1" :inactive-value="0" />
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.tag.rule.matchType')" prop="match_type">
                        <el-select
                            v-model="formItems.match_type"
                            :placeholder="t('common.pleaseSelect', { field: t('plugin.kb.tag.rule.matchType') })"
                            clearable
                        >
                            <el-option :label="t('plugin.kb.tag.rule.matchType path_glob')" value="path_glob" />
                            <el-option :label="t('plugin.kb.tag.rule.matchType filename_regex')" value="filename_regex" />
                            <el-option :label="t('plugin.kb.tag.rule.matchType suffix')" value="suffix" />
                            <el-option :label="t('plugin.kb.tag.rule.matchType meta_eq')" value="meta_eq" />
                        </el-select>
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.tag.rule.pattern')" prop="pattern">
                        <el-input v-model="formItems.pattern" :placeholder="t('common.pleaseEnter', { field: t('plugin.kb.tag.rule.pattern') })" />
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.tag.rule.tagId')" prop="tag_id">
                        <RemoteSelect
                            v-model="formItems.tag_id"
                            pk="id"
                            field="name"
                            remote-url="/admin/plugin/kb/tag/list"
                            :placeholder="t('common.pleaseSelect', { field: t('plugin.kb.tag.rule.tagId') })"
                            :empty-values="[null, 0]"
                            :value-on-clear="0"
                        />
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.tag.rule.priority')" prop="priority">
                        <el-input-number
                            class="w100"
                            controls-position="right"
                            v-model="formItems.priority"
                            :placeholder="t('common.pleaseEnter', { field: t('plugin.kb.tag.rule.priority') })"
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
    library_id: [buildValidatorRule({ name: 'required', title: t('plugin.kb.tag.rule.libraryId') })],
    name: [buildValidatorRule({ name: 'required', title: t('plugin.kb.tag.rule.name') })],
    match_type: [buildValidatorRule({ name: 'required', title: t('plugin.kb.tag.rule.matchType') })],
    pattern: [buildValidatorRule({ name: 'required', title: t('plugin.kb.tag.rule.pattern') })],
    tag_id: [buildValidatorRule({ name: 'required', title: t('plugin.kb.tag.rule.tagId') })],
}
</script>

<style scoped lang="scss"></style>
