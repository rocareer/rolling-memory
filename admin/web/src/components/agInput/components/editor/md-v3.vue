<!-- plugin: mdeditorv3 v1.0.6 -->
<template>
    <div class="ba-editor md-editor-v3" :class="{ 'is-preview-only': previewOnly }">
        <MdPreview
            v-if="previewOnly"
            :id="editorId"
            :modelValue="modelValue"
            :theme="resolvedTheme"
            :language="resolvedLanguage"
            :previewTheme="previewTheme"
            :codeTheme="codeTheme"
            showCodeRowNumber
            v-bind="$attrs"
            @onGetCatalog="onGetCatalog"
        />
        <MdEditor
            v-else
            ref="editorRef"
            :id="editorId"
            :modelValue="modelValue"
            :theme="resolvedTheme"
            :preview="preview"
            :language="resolvedLanguage"
            :previewTheme="previewTheme"
            :codeTheme="codeTheme"
            :toolbarsExclude="state.toolbarsExclude"
            v-bind="$attrs"
            showCodeRowNumber
            v-loading="state.uploadLoading"
            element-loading-text="Uploading..."
            @update:modelValue="onUpdateModelValue"
            @onUploadImg="onUploadImg"
            @onGetCatalog="onGetCatalog"
        />
    </div>
</template>

<script setup lang="ts">
import { upload } from '@/api/common'
import { useConfig } from '@/stores/config'
import { uuid } from '@/utils/random'
import { ElMessage } from 'element-plus'
import { config as mdConfig, MdEditor, MdPreview, type ExposeParam, type HeadList, type Themes, type ToolbarNames } from 'md-editor-v3'
import 'md-editor-v3/lib/style.css'
import prettier from 'prettier'
import screenfull from 'screenfull'
import { computed, nextTick, reactive, ref } from 'vue'

interface Props {
    modelValue?: string
    theme?: Themes
    preview?: boolean
    previewOnly?: boolean
    previewTheme?: 'default' | 'github' | 'vuepress' | 'mk-cute' | 'smart-blue' | 'cyanosis'
    codeTheme?: 'atom' | 'a11y' | 'github' | 'gradient' | 'kimbie' | 'paraiso' | 'qtcreator' | 'stackoverflow'
    language?: string
    editorId?: string
    toolbarsExclude?: ToolbarNames[]
    fileForceLocal?: boolean
}

const props = withDefaults(defineProps<Props>(), {
    modelValue: '',
    preview: true,
    previewOnly: false,
    previewTheme: 'github',
    codeTheme: 'atom',
    language: '',
    editorId: () => `md-editor-${uuid().slice(0, 8)}`,
    toolbarsExclude: () => [],
    fileForceLocal: false,
})

const emit = defineEmits<{
    (e: 'update:modelValue', value: string): void
    (e: 'catalogChange', list: HeadList[]): void
}>()

const editorRef = ref<ExposeParam>()
const config = useConfig()

const excludeNames: ToolbarNames[] = [
    'sub',
    'sup',
    'mermaid',
    'katex',
    'revoke',
    'next',
    'save',
    'fullscreen',
    'task',
    '=',
    'codeRow',
    'htmlPreview',
    'catalog',
    'github',
]

const state: {
    toolbarsExclude: ToolbarNames[]
    uploadLoading: boolean
} = reactive({
    toolbarsExclude: props.toolbarsExclude.length ? props.toolbarsExclude : excludeNames,
    uploadLoading: false,
})

const resolvedTheme = computed<Themes>(() => {
    if (props.theme) {
        return props.theme
    }
    return config.layout.dark ? 'dark' : 'light'
})

const resolvedLanguage = computed<string>(() => {
    if (props.language) {
        return props.language
    }
    const activeLang = String(config.lang.active || config.lang.fallback || 'zh-cn').toLowerCase()
    return activeLang === 'zh-cn' ? 'zh-CN' : 'en-US'
})

mdConfig({
    editorExtensions: {
        screenfull: {
            instance: screenfull,
        },
        prettier: {
            prettierInstance: prettier,
        },
    },
})

const onUpdateModelValue = (val: string) => {
    emit('update:modelValue', val)
}

const onGetCatalog = (list: HeadList[]) => {
    emit('catalogChange', list)
}

const onUploadImg = async (files: File[], callback: (urls: string[]) => void) => {
    state.uploadLoading = true
    nextTick(async () => {
        try {
            const urls: string[] = []
            for (const file of files) {
                const res = await upload(
                    {
                        file,
                        topic: 'editor',
                        ...(props.fileForceLocal ? { driver: 'local' } : {}),
                    },
                    {
                        onUploadProgress: (evt: any) => {
                            const el = document.querySelector('.ba-editor .el-loading-mask .el-loading-text')
                            if (el && evt?.progress) {
                                el.innerHTML = (evt.progress * 100).toFixed(1) + '%'
                            }
                        },
                    }
                )
                const rawUrl = (res as any)?.data?.url || (res as any)?.data?.file?.full_url || ''
                if (rawUrl) {
                    urls.push(String(rawUrl).replace(/\(/g, '\\(').replace(/\)/g, '\\)'))
                }
            }
            callback(urls)
        } catch (err: any) {
            ElMessage.error(err?.message || 'Upload failed')
        } finally {
            state.uploadLoading = false
        }
    })
}

const getRef = () => {
    return editorRef.value
}

defineExpose({
    getRef,
})
</script>

<style scoped lang="scss">
.ba-editor.md-editor-v3 {
    width: 100%;

    :deep(svg.md-editor-icon) {
        box-sizing: content-box;
    }

    :deep(.md-editor-footer) {
        height: unset;
    }

    &.is-preview-only {
        :deep(.md-editor-previewOnly) {
            background-color: transparent;
        }
        :deep(.md-editor-preview-wrapper) {
            padding: 0;
        }
    }
}
</style>
