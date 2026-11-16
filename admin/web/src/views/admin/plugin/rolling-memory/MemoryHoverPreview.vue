<!-- plugin: rolling-memory v0.0.58 -->
<!--
  MemoryHoverPreview —— 表格与列表长记忆单元格的富格式 Markdown 悬浮预览卡。
  · 懒挂载（Lazy-Render on Hover）：仅当用户悬停触发打开后才挂载内部 MarkdownPreview（MdPreview），
    列表渲染阶段零额外编辑器实例开销；
  · 鼠标可移入（enterable）+ 内部限高平滑滚动（max-height: 400px），彻底取代把换行与代码压扁的
    原生单行黑框 show-overflow-tooltip；
  · 顶部紧凑栏展示图层徽章、坐标锚点/标题与「复制原文」快捷操作。
-->
<template>
    <el-popover
        v-if="hasContent"
        trigger="hover"
        :placement="props.placement"
        :width="props.width"
        :show-after="props.showAfter"
        :hide-after="120"
        :persistent="false"
        popper-class="rm-md-hover-popper"
        @before-enter="everOpened = true"
    >
        <template #reference>
            <div class="rm-hover-ref">
                <slot />
            </div>
        </template>
        <div v-if="everOpened" class="rm-hover-card" data-testid="memory-hover-card">
            <div class="rm-hover-hdr">
                <div class="rm-hover-meta">
                    <el-tag v-if="layerBadge" size="small" :type="layerTagType" effect="dark" class="rm-hover-layer">
                        {{ layerBadge }}
                    </el-tag>
                    <code v-if="props.title" class="rm-hover-title">{{ props.title }}</code>
                    <span v-if="props.subtitle" class="rm-hover-sub">{{ props.subtitle }}</span>
                </div>
                <div class="rm-hover-actions">
                    <el-button link type="primary" size="small" data-testid="hover-preview-copy" @click.stop="copyRaw">
                        {{ t('plugin.rolling-memory.preview.copyRaw') }}
                    </el-button>
                </div>
            </div>
            <div v-if="props.question" class="rm-hover-question">
                <el-tag size="small" type="warning" effect="plain" class="rm-hover-q-badge">Q</el-tag>
                <span class="rm-hover-q-text">{{ props.question }}</span>
            </div>
            <div class="rm-hover-body">
                <MarkdownPreview :text="resolvedBodyText" :hint="props.hint" hide-toolbar compact />
            </div>
        </div>
    </el-popover>
    <slot v-else />
</template>

<script setup lang="ts">
import { copy } from '@/utils/common'
import { ElMessage } from 'element-plus'
import type { Placement } from 'element-plus'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import MarkdownPreview from './MarkdownPreview.vue'
import type { MemoryContentHint } from './memoryMarkdown'

const props = withDefaults(
    defineProps<{
        /** 待预览的完整正文（L1/L2 原文或 L3 回答）。 */
        text?: string
        /** L3 结晶或评测行的提问/线索（可选，置于正文上方结构化高亮）。 */
        question?: string
        /** 顶部显示的单元 ID 或分块坐标锚点。 */
        title?: string
        /** 顶部辅助说明（如时间、工作区）。 */
        subtitle?: string
        /** 正文形态线索（layer / relPath）。 */
        hint?: MemoryContentHint
        /** 悬浮窗宽度。 */
        width?: number | string
        /** 弹出方向。 */
        placement?: Placement
        /** 悬停延迟毫秒数（防鼠标划过表格时频繁闪烁）。 */
        showAfter?: number
    }>(),
    {
        text: '',
        question: '',
        title: '',
        subtitle: '',
        width: 'min(680px, 88vw)',
        placement: 'top-start',
        showAfter: 240,
    }
)

const { t } = useI18n()
const everOpened = ref(false)

const resolvedBodyText = computed(() => String(props.text || '').trim())
const hasContent = computed(() => Boolean(resolvedBodyText.value || String(props.question || '').trim()))

const layerBadge = computed(() => {
    const raw = String(props.hint?.layer || '').toLowerCase()
    if (raw === 'background' || raw === 'l1') return 'L1'
    if (raw === 'episodic' || raw === 'l2') return 'L2'
    if (raw === 'noetic' || raw === 'l3') return 'L3'
    return raw ? raw.toUpperCase() : ''
})

const layerTagType = computed(() => {
    const raw = String(props.hint?.layer || '').toLowerCase()
    if (raw === 'background' || raw === 'l1') return 'primary'
    if (raw === 'episodic' || raw === 'l2') return 'success'
    if (raw === 'noetic' || raw === 'l3') return 'warning'
    return 'info'
})

const copyPayload = computed(() => {
    const q = String(props.question || '').trim()
    const b = resolvedBodyText.value
    if (q && b) return `Q: ${q}\nA: ${b}`
    return b || q
})

const copyRaw = async () => {
    if (await copy(copyPayload.value)) {
        ElMessage.success(t('plugin.rolling-memory.preview.copied'))
    }
}
</script>

<style scoped lang="scss">
.rm-hover-ref {
    min-width: 0;
    width: 100%;
}

.rm-hover-card {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
}

.rm-hover-hdr {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding-bottom: 6px;
    border-bottom: 1px solid var(--el-border-color-lighter);
}

.rm-hover-meta {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    flex: 1;
    overflow: hidden;
}

.rm-hover-layer {
    flex-shrink: 0;
    height: 18px;
    padding: 0 5px;
    font-size: 10px;
    font-weight: 700;
}

.rm-hover-title {
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 11.5px;
    color: var(--el-text-color-primary);
    background: var(--el-fill-color-light);
    padding: 1px 6px;
    border-radius: 4px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.rm-hover-sub {
    font-size: 11px;
    color: var(--el-text-color-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.rm-hover-actions {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    gap: 6px;
}

.rm-hover-question {
    display: flex;
    align-items: flex-start;
    gap: 6px;
    padding: 6px 8px;
    border-radius: 6px;
    background: var(--el-fill-color-lighter);
    border-left: 3px solid var(--el-color-warning);
    font-size: 12.5px;
    line-height: 1.45;
    font-weight: 600;
    color: var(--el-text-color-primary);
    word-break: break-word;
}

.rm-hover-q-badge {
    flex-shrink: 0;
    height: 18px;
    padding: 0 5px;
    font-size: 10px;
    font-weight: 700;
    margin-top: 1px;
}

.rm-hover-q-text {
    flex: 1;
    min-width: 0;
}

.rm-hover-body {
    max-height: 380px;
    overflow-y: auto;
    overflow-x: auto;
    padding-right: 2px;
}
</style>

<style lang="scss">
.el-popover.el-popper.rm-md-hover-popper {
    padding: 10px 12px;
    border-radius: 10px;
    border: 1px solid var(--el-border-color-light);
    box-shadow: 0 12px 32px rgba(0, 0, 0, 0.16);
    background: var(--el-bg-color-overlay);
}
</style>
