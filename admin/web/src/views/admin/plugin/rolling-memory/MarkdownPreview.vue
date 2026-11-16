<!-- plugin: rolling-memory v0.0.58 -->
<!--
  MarkdownPreview —— 记忆正文的统一阅读面（三层浏览器 / 检索调试台 / ROI 证据抽屉 / 云端回忆共用）。

  渲染内核 = md-editor-v3 的 MdPreview（与帮助中心同一套组件、同一份样式，不再自维护渲染器）：
  · 正文先经 memoryMarkdown 自动格式化（结构化内容包代码围栏）再交给 MdPreview 渲染；
  · 分块坐标首行提升为围栏外的行首标题，代码行号随之校准到源文件真实行号（`#L<start>-<end>`）；
  · 正文里的原始 HTML 由 md-editor-v3 自带的 XSSPlugin 过滤：非白名单标签一律以字面文本呈现；
  · 代码高亮走本地 highlight.js 实例，离线可用、不请求任何 CDN（KaTeX/Mermaid 同步关闭）；
  · 「复制原文」复制未形变的原始字节，与格式化后的呈现互不影响。
-->
<template>
    <div class="rm-md-preview" :class="{ 'is-compact': props.compact }" :style="lineNumberStyle">
        <div v-if="!props.hideToolbar" class="rm-md-preview-bar">
            <el-button link type="primary" size="small" data-testid="md-preview-copy" @click="copyRawText">
                {{ t('plugin.rolling-memory.preview.copyRaw') }}
            </el-button>
        </div>
        <!-- 记忆正文以“打开即读”为目的：长代码块不默认折叠（收起态会保留大片布局空白，易被误读为空块）。 -->
        <MdPreview
            :id="editorId"
            :modelValue="markdown"
            :theme="resolvedTheme"
            :language="resolvedLanguage"
            previewTheme="github"
            codeTheme="atom"
            showCodeRowNumber
            :code-foldable="false"
            no-katex
            no-mermaid
            no-echarts
            data-testid="md-preview-body"
        />
    </div>
</template>

<script setup lang="ts">
import { useConfig } from '@/stores/config'
import { copy } from '@/utils/common'
import { uuid } from '@/utils/random'
import { ElMessage } from 'element-plus'
import { config as mdConfig, MdPreview, XSSPlugin, type MarkdownItConfigPlugin, type Themes } from 'md-editor-v3'
import 'md-editor-v3/lib/style.css'
import hljs from 'highlight.js/lib/common'
import 'highlight.js/styles/atom-one-dark.css'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { chunkAbsoluteLineStart, toMemoryMarkdown, type MemoryContentHint } from './memoryMarkdown'

// 全局注册与帮助中心 md-v3 包装器同构：XSSPlugin 净化正文里的原始 HTML；
// highlight 注入本地实例后 MdPreview 不再从 unpkg 拉取脚本与主题（纯本地控制台零 CDN 外呼）。
mdConfig({
    editorExtensions: {
        highlight: {
            instance: hljs,
        },
    },
    markdownItPlugins: (plugins: MarkdownItConfigPlugin[]) => [
        ...plugins.filter((p) => p.type !== 'xss'),
        { type: 'xss', plugin: XSSPlugin, options: {} },
    ],
})

const props = withDefaults(
    defineProps<{
        /** 待展示的记忆正文（格式化与「复制原文」共用同一字节源）。 */
        text?: string
        /** 正文形态线索（层级 / 来源文件），供自动格式化挑选呈现方式。 */
        hint?: MemoryContentHint
        /** 悬浮预览等外部已提供工具栏时隐藏内部顶部按钮栏。 */
        hideToolbar?: boolean
        /** 紧凑排版模式（用于表格行悬停卡片）。 */
        compact?: boolean
    }>(),
    {
        text: '',
        hideToolbar: false,
        compact: false,
    }
)

const { t } = useI18n()
const config = useConfig()
const editorId = `rm-md-preview-${uuid().slice(0, 8)}`

const markdown = computed(() => toMemoryMarkdown(String(props.text || ''), props.hint))
// 背景分块正文以源文件真实行号起算：仅当正文与锚点区间逐行 1:1 时把起始行交给 CSS 计数器，
// 否则保持 md-editor-v3 每块从 1 起算的相对行号，绝不显示会漂移的假绝对行号。
const lineNumberStyle = computed(() => {
    const start = chunkAbsoluteLineStart(String(props.text || ''))
    return start > 1 ? { '--rm-line-start': String(start - 1) } : undefined
})
const resolvedTheme = computed<Themes>(() => (config.layout.dark ? 'dark' : 'light'))
const resolvedLanguage = computed(() => (String(config.lang.active || 'zh-cn').toLowerCase().startsWith('en') ? 'en-US' : 'zh-CN'))

const copyRawText = async () => {
    if (await copy(String(props.text || ''))) {
        ElMessage.success(t('plugin.rolling-memory.preview.copied'))
    }
}
</script>

<style scoped lang="scss">
.rm-md-preview {
    min-width: 0;

    .rm-md-preview-bar {
        display: flex;
        align-items: center;
        justify-content: flex-end;
        gap: 8px;
        margin-bottom: 2px;
    }

    :deep(.md-editor-previewOnly) {
        background-color: transparent;
    }

    :deep(.md-editor-preview-wrapper) {
        padding: 0;
    }

    // 分块正文的渲染行号校准到源文件行号：覆盖 md-editor-v3 每个代码块从 1 起算的 CSS 计数器。
    :deep(.md-editor-scrn span[rn-wrapper]) {
        counter-reset: linenumber var(--rm-line-start, 0);
    }

    :deep(svg.md-editor-icon) {
        box-sizing: content-box;
    }

    &.is-compact {
        :deep(.md-editor-preview) {
            font-size: 12.5px;
            line-height: 1.55;
        }
        :deep(.md-editor-preview pre) {
            margin: 6px 0;
        }
        :deep(.md-editor-preview p) {
            margin: 4px 0;
        }
    }
}
</style>
