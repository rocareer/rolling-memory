<!-- plugin: kb v1.0.0 -->
<template>
    <el-dialog
        class="ag-operate-dialog"
        :close-on-click-modal="false"
        :model-value="visible"
        @close="close"
        :destroy-on-close="true"
        :draggable="true"
    >
        <template #header>
            <div class="title">{{ t('plugin.kb.library.importDoc') }}</div>
        </template>
        <el-scrollbar v-loading="importing" class="ag-table-form-scrollbar">
            <div class="ag-operate-form import-form">
                <el-form
                    ref="formRef"
                    @submit.prevent=""
                    :model="form"
                    :label-position="config.layout.shrink ? 'top' : 'right'"
                    label-width="120px"
                    :rules="rules"
                >
                    <!-- 目标库 = 库列表当前行（只读回显，不提供切换） -->
                    <el-form-item :label="t('plugin.kb.library.importLibrary')">
                        <div class="import-library">
                            <span class="import-library-name">{{ library?.name || '-' }}</span>
                            <el-tag size="small" type="info">ID {{ library?.id ?? '-' }}</el-tag>
                        </div>
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.library.importName')" prop="name">
                        <el-input
                            v-model="form.name"
                            maxlength="128"
                            show-word-limit
                            :placeholder="t('common.pleaseEnter', { field: t('plugin.kb.library.importName') })"
                        ></el-input>
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.library.importText')" prop="text">
                        <el-input
                            type="textarea"
                            :rows="8"
                            v-model="form.text"
                            :placeholder="t('plugin.kb.library.importTextPlaceholder')"
                        ></el-input>
                    </el-form-item>

                    <el-form-item :label="t('plugin.kb.library.importFile')">
                        <!-- 文件只借 el-upload 做本地选择（auto-upload=false）：字节由本插件的导入端点
                             以 multipart 直接接收（契约见 views/api.ts 的 importKbDocument），
                             不经宿主 /common/upload 的「先落宿主存储、再回 URL」形态。 -->
                        <el-upload :auto-upload="false" v-model:file-list="fileList" :on-change="onFileChange" :on-remove="onFileRemove">
                            <el-button v-blur type="primary">{{ t('plugin.kb.library.importPick') }}</el-button>
                        </el-upload>
                        <div class="form-hint">{{ t('plugin.kb.library.importFileHint') }}</div>
                    </el-form-item>
                </el-form>
                <div class="form-hint import-hint">{{ t('plugin.kb.library.importHint') }}</div>
            </div>
        </el-scrollbar>
        <template #footer>
            <el-button v-blur @click="close()">{{ t('common.cancel') }}</el-button>
            <el-button v-blur :loading="importing" @click="onImport" type="primary">{{ t('common.confirm') }}</el-button>
        </template>
    </el-dialog>
</template>

<script setup lang="ts">
import { importKbDocument, type KbLibraryRow } from '../api'
import { useConfig } from '@/stores/config'
import { buildValidatorRule } from '@/utils/validate'
import { ElMessage, type FormInstance, type FormItemRule, type UploadFile, type UploadUserFile } from 'element-plus'
import { reactive, ref, useTemplateRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'

interface Props {
    // 被导入的库（列表行原样传入：id/name/owner_kind/owner_id/status）
    library: KbLibraryRow | null
}

const props = defineProps<Props>()
const visible = defineModel<boolean>({ required: true })
const emits = defineEmits<{
    (e: 'success'): void
}>()

const { t } = useI18n()
const config = useConfig()
const formRef = useTemplateRef<FormInstance>('formRef')

// 面板内状态：表单字段 + 选中的文件（字节随 multipart 走）+ 在途动作
const form = reactive({ name: '', text: '' })
const rules: Partial<Record<string, FormItemRule[]>> = {
    name: [buildValidatorRule({ name: 'required', title: t('plugin.kb.library.importName') })],
}
const fileList = ref<UploadUserFile[]>([])
const picked = ref<File | null>(null)
const importing = ref(false)

// 选文件：只保留最新一次选择（一次导入一份文档），名称留空时取文件名
const onFileChange = (file: UploadFile) => {
    fileList.value = [file]
    picked.value = (file.raw as File) || null
    if (!form.name.trim() && file.name) {
        form.name = file.name
    }
}

const onFileRemove = () => {
    fileList.value = []
    picked.value = null
}

const close = () => {
    visible.value = false
}

const onImport = async () => {
    const lib = props.library
    if (!lib || !formRef.value) {
        return
    }

    // 名称必填（表单行内校验）；内容 = 文本与文件至少一项（二选一由后端同一条下游路径收口）
    const valid = await formRef.value.validate().catch(() => false)
    if (!valid) {
        return
    }
    if (!picked.value && !form.text.trim()) {
        ElMessage.warning(t('plugin.kb.library.importContentRequired'))
        return
    }

    importing.value = true
    try {
        const res = await importKbDocument({
            library_id: lib.id,
            name: form.name.trim(),
            text: form.text,
            file: picked.value,
        })
        // 幂等回执：created=false 表示同库同内容已有活的文档（未重复建库）
        const result = res.data?.data
        ElMessage.success(
            result?.created === false
                ? t('plugin.kb.library.importDuplicated', { id: result?.document_id })
                : t('plugin.kb.library.importSucceeded', { id: result?.document_id })
        )
        emits('success')
        close()
    } catch {
        // 业务/网络错误已由请求拦截器统一提示；对话框保留现场供改后重试
    } finally {
        importing.value = false
    }
}

// 打开（或切换目标库）时重置：上一轮的文本与文件不得带进下一次导入
watch(
    () => [visible.value, props.library?.id],
    ([open]) => {
        if (!open) {
            return
        }
        form.name = ''
        form.text = ''
        fileList.value = []
        picked.value = null
        importing.value = false
    }
)
</script>

<style scoped lang="scss">
.import-library {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
}

.import-library-name {
    font-weight: 600;
    color: var(--el-text-color-primary);
    word-break: break-all;
}

.form-hint {
    width: 100%;
    margin-top: 4px;
    color: var(--el-text-color-secondary);
    font-size: 12px;
    line-height: 18px;
}

.import-hint {
    margin-top: 0;
    margin-bottom: 8px;
}
</style>
