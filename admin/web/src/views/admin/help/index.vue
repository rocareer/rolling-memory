<template>
    <div class="default-main rm-help-workbench" :class="{ 'is-zen-mode': zenMode }">
        <!-- ==================== 顶部极光命令与全局搜索横幅 ==================== -->
        <section class="rm-help-hero">
            <div class="hero-main-row">
                <div class="hero-brand-col">
                    <div class="hero-badge-row">
                        <span class="hero-pill-live">
                            <span class="live-dot"></span>
                            <span>rocareer/rolling-memory · v{{ releaseVersion }}</span>
                        </span>
                        <span class="hero-pill-stat">
                            <Icon name="lucide-file-text" :size="12" />
                            <span>{{ OPEN_SOURCE_DOCS.length }} {{ isEn ? 'Open-Source Docs' : '篇开源文档' }}</span>
                        </span>
                        <span class="hero-pill-stat">
                            <Icon name="lucide-text-align-start" :size="12" />
                            <span>{{ totalLinesCount.toLocaleString() }} {{ isEn ? 'Lines' : '行真实源码级正文' }}</span>
                        </span>
                        <span class="hero-pill-stat is-engine">
                            <Icon name="lucide-file-code" :size="12" />
                            <span>{{ t('help.mdEngineTag') }}</span>
                        </span>
                    </div>
                    <h1 class="hero-title">{{ t('help.title') }}</h1>
                    <p class="hero-subtitle">{{ t('help.subtitle') }}</p>
                </div>

                <div class="hero-controls-col">
                    <div class="hero-search-box">
                        <Icon name="lucide-search" :size="16" class="search-icon" />
                        <input
                            ref="searchInputRef"
                            v-model="keyword"
                            type="text"
                            class="search-native-input"
                            :placeholder="t('help.searchPlaceholder')"
                        />
                        <button v-if="keyword" type="button" class="search-clear-btn" @click="keyword = ''">×</button>
                        <kbd v-else class="search-kbd">⌘K</kbd>
                    </div>

                    <div class="hero-action-btns">
                        <el-tooltip
                            :content="
                                isEn
                                    ? 'Toggle Zen Reading Mode (hides left document tree and right TOC for distraction-free reading)'
                                    : '切换专注禅定阅读模式（隐藏左右侧边栏，沉浸式全宽阅读与在线编辑）'
                            "
                            placement="top"
                        >
                            <button type="button" class="hero-btn" :class="{ active: zenMode }" @click="zenMode = !zenMode">
                                <Icon :name="zenMode ? 'lucide-minimize-2' : 'lucide-maximize-2'" :size="14" />
                                <span>{{ zenMode ? t('help.exitZen') : t('help.enterZen') }}</span>
                            </button>
                        </el-tooltip>
                        <el-tooltip
                            :content="
                                isEn
                                    ? 'Open Repository Architecture, Author Declaration (DECLARATION.md), and License Matrix (LICENSE v1.0)'
                                    : '查看开源仓库拓扑、关于作者与诚实免责声明（DECLARATION.md）及开源许可协议矩阵（LICENSE v1.0）'
                            "
                            placement="top"
                        >
                            <button type="button" class="hero-btn" @click="goReposPage">
                                <Icon name="lucide-scale" :size="14" />
                                <span>{{ t('help.goReposPage') }}</span>
                            </button>
                        </el-tooltip>
                        <el-tooltip
                            :content="
                                isEn
                                    ? 'Open official GitHub primary repository in a new tab'
                                    : '在新标签页打开 GitHub 官方开源主仓 rocareer/rolling-memory'
                            "
                            placement="top"
                        >
                            <a :href="MAIN_REPO_URL" target="_blank" rel="noopener noreferrer" class="hero-btn hero-btn-primary">
                                <Icon name="lucide-git-branch" :size="14" />
                                <span>GitHub · rocareer/rolling-memory</span>
                                <span class="ext-arrow">↗</span>
                            </a>
                        </el-tooltip>
                    </div>
                </div>
            </div>

            <!-- 顶部分类胶囊快筛条 -->
            <div class="hero-filter-strip">
                <div class="scope-pills">
                    <button
                        v-for="tab in categoryTabs"
                        :key="tab.key"
                        type="button"
                        class="cat-pill"
                        :class="{ active: activeCategory === tab.key }"
                        @click="activeCategory = tab.key"
                    >
                        <Icon :name="tab.icon" :size="13" />
                        <span>{{ tab.label }}</span>
                        <em class="cat-count">{{ tab.count }}</em>
                    </button>
                </div>

                <div class="quick-doc-shortcuts">
                    <span class="shortcut-label">{{ isEn ? 'Pinned:' : '核心直达：' }}</span>
                    <button
                        v-for="pin in pinnedDocIds"
                        :key="pin.id"
                        type="button"
                        class="pin-chip"
                        :class="{ active: currentDoc?.id === pin.id }"
                        @click="selectDocById(pin.id)"
                    >
                        {{ pin.label }}
                    </button>
                </div>
            </div>
        </section>

        <!-- ==================== 主体三栏知识阅读工作台 ==================== -->
        <div class="rm-help-workspace">
            <!-- 左栏：分组文档导航树 (Sidebar Tree) -->
            <aside v-show="!zenMode" class="rm-help-sidebar">
                <div class="sidebar-inner">
                    <template v-for="group in groupedDocs" :key="group.key">
                        <div v-if="group.items.length" class="doc-group">
                            <div class="doc-group-header">
                                <span class="group-title">{{ group.title }}</span>
                                <span class="group-badge">{{ group.items.length }}</span>
                            </div>
                            <div class="doc-group-list">
                                <button
                                    v-for="doc in group.items"
                                    :key="doc.id"
                                    type="button"
                                    class="doc-tree-item"
                                    :class="{ 'is-active': currentDoc?.id === doc.id }"
                                    @click="selectDoc(doc)"
                                >
                                    <div class="tree-item-icon">
                                        <Icon :name="doc.icon" :size="15" />
                                    </div>
                                    <div class="tree-item-body">
                                        <div class="tree-item-top">
                                            <span class="tree-item-title">{{ isEn ? doc.shortTitleEn : doc.shortTitleZh }}</span>
                                            <span class="tree-item-badge">{{ isEn ? doc.badgeEn : doc.badgeZh }}</span>
                                        </div>
                                        <div class="tree-item-path">{{ doc.path }}</div>
                                    </div>
                                </button>
                            </div>
                        </div>
                    </template>

                    <div v-if="!filteredDocs.length" class="sidebar-empty">
                        <Icon name="lucide-search-x" :size="24" />
                        <p>{{ t('help.noMatch') }}</p>
                        <el-button size="small" link type="primary" @click="resetFilters">{{ t('help.resetFilter') }}</el-button>
                    </div>
                </div>
            </aside>

            <!-- 中栏：沉浸式富排版文档阅读器 (Main Document Reader) -->
            <main ref="readerContainerRef" class="rm-help-reader" @click="handleReaderClick">
                <article v-if="currentDoc" class="reader-paper">
                    <!-- 文档元信息顶栏 -->
                    <header class="reader-doc-header">
                        <div class="doc-breadcrumb-row">
                            <div class="doc-breadcrumb">
                                <span class="bc-repo">rocareer/rolling-memory</span>
                                <span class="bc-sep">/</span>
                                <span class="bc-path">{{ currentDoc.path }}</span>
                            </div>
                            <div class="doc-header-actions">
                                <el-tooltip
                                    :content="
                                        isEn
                                            ? 'Switch between formatted reader and live split-pane Markdown editor (powered by mdeditorv3 plugin)'
                                            : '切换「富排版阅读模式」与「mdeditorv3 双栏实时 Markdown 编辑预览模式」'
                                    "
                                    placement="top"
                                >
                                    <button type="button" class="doc-tool-btn is-editor-toggle" :class="{ active: editMode }" @click="toggleEditMode">
                                        <Icon :name="editMode ? 'lucide-book-open' : 'lucide-file-pen-line'" :size="13" />
                                        <span>{{ editMode ? t('help.exitEdit') : t('help.enterEdit') }}</span>
                                    </button>
                                </el-tooltip>
                                <el-tooltip
                                    v-if="editMode && isEdited"
                                    :content="
                                        isEn
                                            ? 'Discard local edits and restore original in-repo Markdown'
                                            : '放弃当前草稿改动，恢复为仓库原始 Markdown 正文'
                                    "
                                    placement="top"
                                >
                                    <button type="button" class="doc-tool-btn" @click="resetEditableContent">
                                        <Icon name="lucide-rotate-ccw" :size="13" />
                                        <span>{{ t('help.resetDraft') }}</span>
                                    </button>
                                </el-tooltip>
                                <el-tooltip
                                    :content="isEn ? 'Copy full raw Markdown source to clipboard' : '一键复制当前文档的完整 Markdown 源码到剪贴板'"
                                    placement="top"
                                >
                                    <button type="button" class="doc-tool-btn" @click="copyText(currentWorkingMarkdown)">
                                        <Icon name="lucide-copy" :size="13" />
                                        <span>{{ t('help.copyContent') }}</span>
                                    </button>
                                </el-tooltip>
                                <el-tooltip
                                    :content="isEn ? 'Copy remote repository URL of this document' : '复制该文档在 GitHub 仓库中的在线链接地址'"
                                    placement="top"
                                >
                                    <button type="button" class="doc-tool-btn" @click="copyText(currentDoc.url)">
                                        <Icon name="lucide-link" :size="13" />
                                        <span>{{ t('help.copyUrl') }}</span>
                                    </button>
                                </el-tooltip>
                                <a :href="currentDoc.url" target="_blank" rel="noopener noreferrer" class="doc-tool-btn is-primary">
                                    <Icon name="lucide-external-link" :size="13" />
                                    <span>{{ t('help.openGitHub') }}</span>
                                </a>
                            </div>
                        </div>

                        <div class="doc-title-block">
                            <div class="doc-title-line">
                                <span class="doc-cat-tag" :data-cat="currentDoc.category">
                                    {{ t(`help.categories.${currentDoc.category}`) }}
                                </span>
                                <span class="doc-stat-chip">
                                    <Icon name="lucide-code" :size="12" />
                                    {{ renderedDoc.lineCount }} {{ isEn ? 'lines' : '行' }}
                                </span>
                                <span class="doc-stat-chip">
                                    <Icon name="lucide-type" :size="12" />
                                    {{ renderedDoc.charCount.toLocaleString() }} {{ isEn ? 'chars' : '字' }}
                                </span>
                                <span class="doc-stat-chip">
                                    <Icon name="lucide-clock" :size="12" />
                                    ~{{ renderedDoc.readMinutes }} {{ isEn ? 'min read' : '分钟阅读' }}
                                </span>
                            </div>
                            <p class="doc-lead-summary">{{ isEn ? currentDoc.summaryEn : currentDoc.summaryZh }}</p>
                        </div>

                        <!-- 核心要点速览折叠条 -->
                        <div class="doc-key-takeaways">
                            <div class="takeaways-head">
                                <Icon name="lucide-sparkles" :size="14" />
                                <span>{{ t('help.outlineTitle') }}</span>
                            </div>
                            <div class="takeaways-grid">
                                <div v-for="(pt, idx) in isEn ? currentDoc.sectionsEn : currentDoc.sectionsZh" :key="idx" class="takeaway-item">
                                    <span class="takeaway-idx">{{ String(idx + 1).padStart(2, '0') }}</span>
                                    <span class="takeaway-text">{{ pt }}</span>
                                </div>
                            </div>
                        </div>
                    </header>

                    <!-- 在线编辑模式：直接调用 mdeditorv3 基础插件组件（双栏实时编辑 + 解析预览） -->
                    <div v-if="editMode" class="reader-mdeditor-wrap">
                        <MdEditorV3 v-model="editableContent" :preview="true" :previewOnly="false" no-katex no-mermaid no-echarts style="height: 680px" />
                    </div>

                    <!-- 阅读解析模式：真实 Markdown 富排版渲染区 -->
                    <div v-else class="reader-markdown-body" v-html="renderedDoc.html"></div>

                    <!-- 底部上一篇 / 下一篇无缝翻页卡片 -->
                    <footer class="reader-pager">
                        <button v-if="prevDoc" type="button" class="pager-card is-prev" @click="selectDoc(prevDoc)">
                            <span class="pager-dir">← {{ isEn ? 'Previous Document' : '上一篇文档' }}</span>
                            <span class="pager-title">{{ isEn ? prevDoc.shortTitleEn : prevDoc.shortTitleZh }}</span>
                            <span class="pager-path">{{ prevDoc.path }}</span>
                        </button>
                        <div v-else class="pager-spacer"></div>

                        <button v-if="nextDoc" type="button" class="pager-card is-next" @click="selectDoc(nextDoc)">
                            <span class="pager-dir">{{ isEn ? 'Next Document' : '下一篇文档' }} →</span>
                            <span class="pager-title">{{ isEn ? nextDoc.shortTitleEn : nextDoc.shortTitleZh }}</span>
                            <span class="pager-path">{{ nextDoc.path }}</span>
                        </button>
                    </footer>
                </article>
            </main>

            <!-- 右栏：Sticky 本页章节目录大纲 (On-Page TOC) 与快捷命令面板 -->
            <aside v-show="!zenMode" class="rm-help-toc">
                <div class="toc-inner">
                    <div class="toc-card">
                        <div class="toc-header">
                            <Icon name="lucide-list-tree" :size="14" />
                            <span>{{ isEn ? 'On This Page' : '本页章节目录' }}</span>
                            <span class="toc-count">{{ renderedDoc.toc.length }}</span>
                        </div>
                        <nav v-if="renderedDoc.toc.length" class="toc-nav">
                            <a
                                v-for="h in renderedDoc.toc"
                                :key="h.id"
                                href="javascript:void(0)"
                                class="toc-link"
                                :class="[`is-level-${h.level}`, { 'is-active': activeHeadingId === h.id }]"
                                @click.prevent="scrollToHeading(h.id)"
                            >
                                {{ h.text }}
                            </a>
                        </nav>
                        <div v-else class="toc-empty">{{ isEn ? 'No subsections' : '单节正文' }}</div>
                    </div>

                    <!-- 常用 CLI 速查与一键复制卡 -->
                    <div class="toc-cli-card">
                        <div class="toc-header">
                            <Icon name="lucide-terminal" :size="14" />
                            <span>{{ isEn ? 'Quick CLI Commands' : '常用命令一键复制' }}</span>
                        </div>
                        <div class="cli-snippet-list">
                            <div v-for="cmd in quickCommands" :key="cmd.cmd" class="cli-snippet-item" @click="copyText(cmd.cmd)">
                                <div class="cli-label">{{ cmd.label }}</div>
                                <code class="cli-code">{{ cmd.cmd }}</code>
                            </div>
                        </div>
                    </div>
                </div>
            </aside>
        </div>
    </div>
</template>

<script setup lang="ts">
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { ElMessage } from 'element-plus'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import MdEditorV3 from '@/views/admin/plugin/mdeditorv3/editor.vue'
import { MAIN_REPO_URL, OPEN_SOURCE_DOCS, type DocCategoryKey, type OpenSourceDocItem } from './docsData'
import { renderMarkdownDocument } from './markdown'
import { config as mdConfig } from 'md-editor-v3'

// 构建期注入的发布版本（单一真源 = 仓根 VERSION，经 setup 常量桥接进模板）。
const releaseVersion = __RM_VERSION__
import hljs from 'highlight.js/lib/common'
import 'highlight.js/styles/atom-one-dark.css'

mdConfig({
    editorExtensions: {
        highlight: {
            instance: hljs,
        },
    },
})

defineOptions({
    name: 'help/docs',
})

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()

const isEn = computed(() =>
    String(locale.value || '')
        .toLowerCase()
        .startsWith('en')
)

const activeCategory = ref<'all' | DocCategoryKey>('all')
const keyword = ref('')
const zenMode = ref(false)
const editMode = ref(false)
const editedMap = ref<Record<string, string>>({})
const selectedId = ref<string>(OPEN_SOURCE_DOCS[0]?.id || 'root-readme')
const activeHeadingId = ref<string>('')

const searchInputRef = ref<HTMLInputElement | null>(null)
const readerContainerRef = ref<HTMLElement | null>(null)

const totalLinesCount = computed(() => OPEN_SOURCE_DOCS.reduce((sum, item) => sum + item.rawZh.split('\n').length, 0))

const categoryTabs = computed(() => {
    const countOf = (cat: DocCategoryKey) => OPEN_SOURCE_DOCS.filter((d) => d.category === cat).length
    return [
        { key: 'all' as const, icon: 'lucide-layers', label: t('help.catAll'), count: OPEN_SOURCE_DOCS.length },
        { key: 'overview' as const, icon: 'lucide-compass', label: t('help.categories.overview'), count: countOf('overview') },
        { key: 'architecture' as const, icon: 'lucide-cpu', label: t('help.categories.architecture'), count: countOf('architecture') },
        { key: 'guide' as const, icon: 'lucide-book-open-check', label: t('help.categories.guide'), count: countOf('guide') },
        { key: 'legal' as const, icon: 'lucide-scale', label: t('help.categories.legal'), count: countOf('legal') },
    ]
})

const pinnedDocIds = computed(() => [
    { id: 'root-readme', label: 'README.md' },
    { id: 'docs-faq', label: 'docs/faq.md' },
    { id: 'root-declaration', label: 'DECLARATION.md' },
    { id: 'root-license', label: 'LICENSE' },
    { id: 'docs-plugin-console', label: 'docs/plugin-console.md' },
    { id: 'docs-harness', label: 'docs/harness.md' },
    { id: 'docs-benchmarks', label: 'docs/benchmarks.md' },
])

const quickCommands = computed(() => [
    { label: isEn.value ? 'Open local web console (:9006 + :8008)' : '打开本地管理控制台 (:9006 + :8008)', cmd: 'rolling-memory panel' },
    { label: isEn.value ? 'Check for updates (SHA-256 verified)' : '检查最新版本与 SHA-256 摘要', cmd: 'rolling-memory update --check' },
    { label: isEn.value ? 'Check harness & MCP health' : '检查多宿主接入与 MCP 握手', cmd: 'rolling-memory install --check' },
    { label: isEn.value ? 'Diagnose mode, permissions & vault health' : '诊断运行模式、权限与金库健康度', cmd: 'rolling-memory doctor' },
    { label: isEn.value ? 'Inspect local memory store status' : '查看本地记忆库规模与状态', cmd: 'rolling-memory status' },
    {
        label: isEn.value ? 'Run Cambridge ATM-Bench Hard-31 eval' : '运行剑桥 ATM-Bench Hard-31 检索评测',
        cmd: 'rmem eval --suite atm --tier hard --pulse-mode local_plain --top-k 50 --answerer none --judge off',
    },
])

const filteredDocs = computed(() => {
    const q = keyword.value.trim().toLowerCase()
    return OPEN_SOURCE_DOCS.filter((item) => {
        if (activeCategory.value !== 'all' && item.category !== activeCategory.value) {
            return false
        }
        if (!q) {
            return true
        }
        const haystack = [item.path, item.titleZh, item.titleEn, item.summaryZh, item.summaryEn, item.rawZh, item.rawEn].join('\n').toLowerCase()
        return haystack.includes(q)
    })
})

const groupedDocs = computed(() => {
    const list = filteredDocs.value
    return [
        {
            key: 'root',
            title: isEn.value ? '01 · Root Overview & Declarations' : '01 · 根级总览、立场与许可证',
            items: list.filter((d) => d.scope === 'root'),
        },
        {
            key: 'arch',
            title: isEn.value ? '02 · Core Architecture & Contracts' : '02 · 核心架构设计与协议契约',
            items: list.filter((d) => d.scope === 'docs' && d.category === 'architecture'),
        },
        {
            key: 'guide',
            title: isEn.value ? '03 · Guides, Evaluation & Privacy' : '03 · 宿主接入、评测与隐私边界',
            items: list.filter((d) => d.scope === 'docs' && d.category !== 'architecture'),
        },
    ]
})

const currentDoc = computed<OpenSourceDocItem | undefined>(() => {
    const found = filteredDocs.value.find((d) => d.id === selectedId.value)
    if (found) {
        return found
    }
    return filteredDocs.value[0] || OPEN_SOURCE_DOCS[0]
})

const currentDocIndex = computed(() => {
    if (!currentDoc.value) return -1
    return filteredDocs.value.findIndex((d) => d.id === currentDoc.value?.id)
})

const prevDoc = computed(() => (currentDocIndex.value > 0 ? filteredDocs.value[currentDocIndex.value - 1] : null))
const nextDoc = computed(() =>
    currentDocIndex.value >= 0 && currentDocIndex.value < filteredDocs.value.length - 1 ? filteredDocs.value[currentDocIndex.value + 1] : null
)

const activeRawMarkdown = computed(() => {
    if (!currentDoc.value) return ''
    return isEn.value ? currentDoc.value.rawEn : currentDoc.value.rawZh
})

const editKey = computed(() => `${currentDoc.value?.id || 'none'}:${isEn.value ? 'en' : 'zh'}`)

const currentWorkingMarkdown = computed(() => {
    const custom = editedMap.value[editKey.value]
    return custom !== undefined ? custom : activeRawMarkdown.value
})

const editableContent = computed({
    get: () => currentWorkingMarkdown.value,
    set: (val: string) => {
        editedMap.value[editKey.value] = val
    },
})

const isEdited = computed(() => editedMap.value[editKey.value] !== undefined && editedMap.value[editKey.value] !== activeRawMarkdown.value)

const toggleEditMode = () => {
    editMode.value = !editMode.value
}

const resetEditableContent = () => {
    delete editedMap.value[editKey.value]
}

const renderedDoc = computed(() => {
    if (!currentDoc.value) {
        return { html: '', toc: [], lineCount: 0, charCount: 0, readMinutes: 1 }
    }
    return renderMarkdownDocument(currentWorkingMarkdown.value, !!currentDoc.value.isPlainLicense && !isEdited.value)
})

const selectDoc = (doc: OpenSourceDocItem) => {
    selectedId.value = doc.id
    activeHeadingId.value = ''
    nextTick(() => {
        const el = document.querySelector('.rm-help-reader .reader-paper')
        el?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    })
}

const selectDocById = (id: string) => {
    activeCategory.value = 'all'
    keyword.value = ''
    const target = OPEN_SOURCE_DOCS.find((d) => d.id === id)
    if (target) {
        selectDoc(target)
    }
}

const resetFilters = () => {
    activeCategory.value = 'all'
    keyword.value = ''
}

const scrollToHeading = (id: string) => {
    activeHeadingId.value = id
    const target = document.getElementById(id)
    if (target) {
        target.scrollIntoView({ behavior: 'smooth', block: 'start' })
    }
}

// 处理阅读器内部事件委托：代码块一键复制、锚点跳转、文档间互跳
const handleReaderClick = (ev: MouseEvent) => {
    const target = ev.target as HTMLElement | null
    if (!target) return

    const copyBtn = target.closest('[data-copy-code]') as HTMLElement | null
    if (copyBtn) {
        const encoded = copyBtn.getAttribute('data-copy-code') || ''
        if (encoded) {
            copyText(decodeURIComponent(encoded))
        }
        return
    }

    const scrollAnchor = target.closest('[data-scroll-to]') as HTMLElement | null
    if (scrollAnchor) {
        const id = scrollAnchor.getAttribute('data-scroll-to') || ''
        if (id) {
            scrollToHeading(id)
        }
        return
    }

    const intLink = target.closest('[data-internal-href]') as HTMLElement | null
    if (intLink) {
        const rawHref = (intLink.getAttribute('data-internal-href') || '').trim()
        if (!rawHref) return
        if (rawHref.startsWith('#')) {
            return
        }
        const pathOnly = rawHref.split('#')[0]
        const matched = OPEN_SOURCE_DOCS.find(
            (d) =>
                d.path.toLowerCase() === pathOnly.toLowerCase() ||
                d.path.toLowerCase() === `docs/${pathOnly.toLowerCase()}` ||
                (pathOnly.toLowerCase() === 'readme.en.md' && d.id === 'root-readme')
        )
        if (matched) {
            activeCategory.value = 'all'
            keyword.value = ''
            selectDoc(matched)
        }
    }
}

const applyQueryDoc = () => {
    const docParam = typeof route.query.doc === 'string' ? route.query.doc.trim() : ''
    if (!docParam) return
    const target = OPEN_SOURCE_DOCS.find((d) => d.id === docParam || d.path === docParam)
    if (target) {
        activeCategory.value = 'all'
        selectedId.value = target.id
    }
}

const onKeydown = (ev: KeyboardEvent) => {
    if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === 'k') {
        ev.preventDefault()
        searchInputRef.value?.focus()
    }
}

onMounted(() => {
    applyQueryDoc()
    window.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
    window.removeEventListener('keydown', onKeydown)
})

watch(() => route.query.doc, applyQueryDoc)

const goReposPage = () => {
    router.push(`${adminBaseRoutePath}/help/repos`)
}

const copyText = async (text: string) => {
    try {
        await navigator.clipboard.writeText(text)
        ElMessage.success(t('help.copied'))
    } catch {
        ElMessage.info(text.slice(0, 80))
    }
}
</script>

<style scoped lang="scss">
.rm-help-workbench {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding-bottom: 24px;
}

/* ==================== 顶部极光命令头 ==================== */
.rm-help-hero {
    position: relative;
    padding: 20px 24px 16px;
    border-radius: 14px;
    background:
        radial-gradient(620px circle at 92% 12%, rgba(14, 165, 233, 0.14), transparent 65%),
        radial-gradient(520px circle at 8% 85%, rgba(37, 99, 235, 0.1), transparent 60%), var(--ag-bg-color-overlay);
    border: 1px solid var(--el-border-color-lighter);
    box-shadow: 0 4px 20px rgba(15, 23, 42, 0.04);
}

.hero-main-row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 20px;
    flex-wrap: wrap;
}

.hero-brand-col {
    flex: 1;
    min-width: 300px;
}

.hero-badge-row {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    margin-bottom: 10px;
}

.hero-pill-live {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 3px 10px;
    border-radius: 999px;
    font-family: var(--el-font-family-monospace, monospace);
    font-size: 12px;
    font-weight: 600;
    color: #0f766e;
    background: rgba(16, 185, 129, 0.12);
    border: 1px solid rgba(16, 185, 129, 0.28);

    .live-dot {
        width: 7px;
        height: 7px;
        border-radius: 50%;
        background: #10b981;
        box-shadow: 0 0 0 3px rgba(16, 185, 129, 0.22);
    }
}

.hero-pill-stat {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 3px 10px;
    border-radius: 999px;
    font-size: 12px;
    color: var(--el-text-color-regular);
    background: var(--el-fill-color-light);
    border: 1px solid var(--el-border-color-lighter);
}

.hero-title {
    margin: 0 0 6px;
    font-size: 22px;
    font-weight: 700;
    letter-spacing: -0.02em;
    color: var(--el-text-color-primary);
}

.hero-subtitle {
    margin: 0;
    font-size: 13px;
    line-height: 1.6;
    color: var(--el-text-color-secondary);
    max-width: 780px;
}

.hero-controls-col {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 10px;
    min-width: 340px;
}

.hero-search-box {
    position: relative;
    width: 100%;
    max-width: 420px;
    display: flex;
    align-items: center;
    background: var(--el-fill-color-blank);
    border: 1px solid var(--el-border-color);
    border-radius: 10px;
    padding: 0 10px 0 12px;
    height: 38px;
    transition: all 0.18s ease;

    &:focus-within {
        border-color: var(--el-color-primary);
        box-shadow: 0 0 0 3px rgba(37, 99, 235, 0.12);
    }

    .search-icon {
        color: var(--el-text-color-secondary);
        flex-shrink: 0;
    }

    .search-native-input {
        flex: 1;
        border: none;
        outline: none;
        background: transparent;
        padding: 0 10px;
        font-size: 13px;
        color: var(--el-text-color-primary);
    }

    .search-kbd {
        font-family: var(--el-font-family-monospace, monospace);
        font-size: 11px;
        padding: 2px 6px;
        border-radius: 5px;
        color: var(--el-text-color-secondary);
        background: var(--el-fill-color-light);
        border: 1px solid var(--el-border-color-lighter);
    }

    .search-clear-btn {
        border: none;
        background: var(--el-fill-color);
        color: var(--el-text-color-regular);
        width: 20px;
        height: 20px;
        border-radius: 50%;
        cursor: pointer;
        font-size: 14px;
        line-height: 1;
    }
}

.hero-action-btns {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}

.hero-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 32px;
    padding: 0 12px;
    border-radius: 8px;
    font-size: 12.5px;
    font-weight: 500;
    color: var(--el-text-color-regular);
    background: var(--el-fill-color-blank);
    border: 1px solid var(--el-border-color);
    cursor: pointer;
    text-decoration: none;
    transition: all 0.15s ease;

    &:hover,
    &.active {
        color: var(--el-color-primary);
        border-color: var(--el-color-primary-light-5);
        background: var(--el-color-primary-light-9);
    }

    &.hero-btn-primary {
        color: #ffffff;
        background: linear-gradient(135deg, #2563eb, #0284c7);
        border-color: transparent;

        &:hover {
            opacity: 0.94;
            color: #ffffff;
        }
    }

    .ext-arrow {
        font-size: 11px;
        opacity: 0.85;
    }
}

.hero-filter-strip {
    margin-top: 14px;
    padding-top: 12px;
    border-top: 1px dashed var(--el-border-color-lighter);
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
}

.scope-pills {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
}

.cat-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 5px 12px;
    border-radius: 8px;
    font-size: 12.5px;
    font-weight: 500;
    color: var(--el-text-color-regular);
    background: var(--el-fill-color-light);
    border: 1px solid transparent;
    cursor: pointer;
    transition: all 0.15s ease;

    .cat-count {
        font-style: normal;
        font-size: 11px;
        padding: 0 6px;
        border-radius: 999px;
        background: var(--el-fill-color-blank);
        color: var(--el-text-color-secondary);
    }

    &:hover {
        color: var(--el-color-primary);
    }

    &.active {
        color: #ffffff;
        background: var(--el-color-primary);

        .cat-count {
            background: rgba(255, 255, 255, 0.22);
            color: #ffffff;
        }
    }
}

.quick-doc-shortcuts {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;

    .shortcut-label {
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }
}

.pin-chip {
    font-family: var(--el-font-family-monospace, monospace);
    font-size: 11.5px;
    padding: 3px 9px;
    border-radius: 6px;
    border: 1px solid var(--el-border-color-lighter);
    background: var(--el-fill-color-blank);
    color: var(--el-text-color-regular);
    cursor: pointer;
    transition: all 0.15s ease;

    &:hover,
    &.active {
        color: var(--el-color-primary);
        border-color: var(--el-color-primary);
        background: var(--el-color-primary-light-9);
    }
}

/* ==================== 三栏知识工作区 ==================== */
.rm-help-workspace {
    display: grid;
    grid-template-columns: 280px minmax(0, 1fr) 240px;
    gap: 16px;
    align-items: start;
}

.rm-help-workbench.is-zen-mode .rm-help-workspace {
    grid-template-columns: minmax(0, 960px);
    justify-content: center;
}

@media (max-width: 1540px) {
    .rm-help-workspace {
        grid-template-columns: 250px minmax(0, 1fr);
    }
    .rm-help-toc {
        display: none;
    }
}

@media (max-width: 960px) {
    .rm-help-workspace {
        grid-template-columns: 1fr;
    }
}

/* 左栏：分组文档导航树 */
.rm-help-sidebar {
    position: sticky;
    top: 12px;
}

.sidebar-inner {
    background: var(--ag-bg-color-overlay);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 12px;
    padding: 12px;
    max-height: calc(100vh - 120px);
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 14px;
}

.doc-group-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 4px 8px 6px;
    font-size: 11.5px;
    font-weight: 700;
    color: var(--el-text-color-secondary);
    letter-spacing: 0.02em;

    .group-badge {
        font-size: 11px;
        padding: 1px 6px;
        border-radius: 999px;
        background: var(--el-fill-color-light);
    }
}

.doc-group-list {
    display: flex;
    flex-direction: column;
    gap: 4px;
}

.doc-tree-item {
    width: 100%;
    text-align: left;
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 9px 10px;
    border-radius: 9px;
    border: 1px solid transparent;
    background: transparent;
    cursor: pointer;
    transition: all 0.15s ease;

    .tree-item-icon {
        width: 28px;
        height: 28px;
        border-radius: 7px;
        background: var(--el-fill-color-light);
        color: var(--el-text-color-regular);
        display: flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
        margin-top: 1px;
    }

    .tree-item-body {
        flex: 1;
        min-width: 0;
    }

    .tree-item-top {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 6px;
    }

    .tree-item-title {
        font-size: 13px;
        font-weight: 600;
        color: var(--el-text-color-primary);
        line-height: 1.35;
    }

    .tree-item-badge {
        font-size: 10.5px;
        padding: 1px 6px;
        border-radius: 4px;
        background: var(--el-fill-color);
        color: var(--el-text-color-secondary);
        white-space: nowrap;
        flex-shrink: 0;
    }

    .tree-item-path {
        margin-top: 3px;
        font-family: var(--el-font-family-monospace, monospace);
        font-size: 11px;
        color: var(--el-text-color-secondary);
    }

    &:hover {
        background: var(--el-fill-color-light);
    }

    &.is-active {
        background: var(--el-color-primary-light-9);
        border-color: var(--el-color-primary-light-5);

        .tree-item-icon {
            background: var(--el-color-primary);
            color: #ffffff;
        }

        .tree-item-title {
            color: var(--el-color-primary);
        }

        .tree-item-badge {
            background: rgba(37, 99, 235, 0.14);
            color: var(--el-color-primary);
        }
    }
}

.sidebar-empty {
    padding: 28px 12px;
    text-align: center;
    color: var(--el-text-color-secondary);
    font-size: 13px;
}

/* ==================== 中栏：沉浸式文档阅读器 ==================== */
.reader-paper {
    background: var(--ag-bg-color-overlay);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 14px;
    padding: 26px 32px 32px;
    box-shadow: 0 4px 24px rgba(15, 23, 42, 0.03);
}

.reader-doc-header {
    padding-bottom: 20px;
    margin-bottom: 22px;
    border-bottom: 1px solid var(--el-border-color-lighter);
}

.doc-breadcrumb-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
    margin-bottom: 14px;
}

.doc-breadcrumb {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 12px;
    border-radius: 7px;
    background: var(--el-fill-color-light);
    font-family: var(--el-font-family-monospace, monospace);
    font-size: 12px;

    .bc-repo {
        color: var(--el-text-color-secondary);
    }

    .bc-sep {
        color: var(--el-text-color-placeholder);
    }

    .bc-path {
        font-weight: 600;
        color: var(--el-color-primary);
    }
}

.doc-header-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}

.doc-tool-btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 30px;
    padding: 0 11px;
    border-radius: 7px;
    font-size: 12px;
    font-weight: 500;
    color: var(--el-text-color-regular);
    background: var(--el-fill-color-blank);
    border: 1px solid var(--el-border-color);
    cursor: pointer;
    text-decoration: none;
    transition: all 0.15s ease;

    &:hover {
        color: var(--el-color-primary);
        border-color: var(--el-color-primary-light-5);
    }

    &.is-primary {
        color: var(--el-color-primary);
        background: var(--el-color-primary-light-9);
        border-color: var(--el-color-primary-light-5);
    }

    &.is-editor-toggle.active {
        color: #ffffff;
        background: #059669;
        border-color: #059669;
    }
}

.reader-mdeditor-wrap {
    margin: 10px 0 18px;
    border-radius: 10px;
    overflow: hidden;
    border: 1px solid var(--el-border-color-lighter);
}

.doc-title-line {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: 10px;
}

.doc-cat-tag {
    padding: 2px 9px;
    border-radius: 5px;
    font-size: 11.5px;
    font-weight: 600;
    color: #2563eb;
    background: rgba(37, 99, 235, 0.1);
}

.doc-stat-chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
}

.doc-lead-summary {
    margin: 0;
    font-size: 14px;
    line-height: 1.65;
    color: var(--el-text-color-regular);
}

.doc-key-takeaways {
    margin-top: 16px;
    padding: 14px 16px;
    border-radius: 10px;
    background: linear-gradient(135deg, rgba(37, 99, 235, 0.05), rgba(14, 165, 233, 0.04));
    border: 1px solid rgba(37, 99, 235, 0.16);
}

.takeaways-head {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12.5px;
    font-weight: 700;
    color: var(--el-color-primary);
    margin-bottom: 10px;
}

.takeaways-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
    gap: 8px 14px;
}

.takeaway-item {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    font-size: 12.5px;
    line-height: 1.5;
    color: var(--el-text-color-primary);

    .takeaway-idx {
        font-family: var(--el-font-family-monospace, monospace);
        font-size: 11px;
        font-weight: 700;
        color: var(--el-color-primary);
        background: rgba(37, 99, 235, 0.12);
        padding: 1px 6px;
        border-radius: 4px;
        flex-shrink: 0;
        margin-top: 1px;
    }
}

/* ==================== 富排版 Markdown 样式 ==================== */
.reader-markdown-body {
    font-size: 14.5px;
    line-height: 1.78;
    color: var(--el-text-color-primary);

    :deep(.md-heading) {
        position: relative;
        scroll-margin-top: 20px;
        font-weight: 700;
        color: var(--el-text-color-primary);
        line-height: 1.35;

        .md-anchor-mark {
            color: var(--el-color-primary);
            opacity: 0.35;
            text-decoration: none;
            margin-right: 7px;
            font-family: var(--el-font-family-monospace, monospace);
            transition: opacity 0.15s ease;
        }

        &:hover .md-anchor-mark {
            opacity: 1;
        }
    }

    :deep(.md-h1) {
        font-size: 22px;
        margin: 8px 0 16px;
        padding-bottom: 10px;
        border-bottom: 1px solid var(--el-border-color-lighter);
    }

    :deep(.md-h2) {
        font-size: 18px;
        margin: 28px 0 12px;
        padding-bottom: 6px;
        border-bottom: 1px dashed var(--el-border-color-lighter);
    }

    :deep(.md-h3) {
        font-size: 15.5px;
        margin: 20px 0 8px;
    }

    :deep(.md-h4) {
        font-size: 14.5px;
        margin: 16px 0 6px;
    }

    :deep(.md-p) {
        margin: 10px 0 14px;
        color: var(--el-text-color-regular);
    }

    :deep(.md-list) {
        margin: 8px 0 16px;
        padding-left: 22px;
        color: var(--el-text-color-regular);

        li {
            margin: 5px 0;
            line-height: 1.7;
        }
    }

    :deep(.md-inline-code) {
        font-family: var(--el-font-family-monospace, monospace);
        font-size: 12.5px;
        padding: 2px 6px;
        border-radius: 5px;
        background: var(--el-fill-color);
        color: var(--el-color-primary);
        border: 1px solid var(--el-border-color-lighter);
    }

    :deep(.md-blockquote) {
        margin: 14px 0;
        padding: 10px 16px;
        border-left: 3px solid var(--el-color-primary);
        background: var(--el-fill-color-light);
        border-radius: 0 8px 8px 0;

        p {
            margin: 4px 0;
            font-size: 13.5px;
            color: var(--el-text-color-regular);
        }
    }

    :deep(.md-hr) {
        border: none;
        border-top: 1px solid var(--el-border-color-lighter);
        margin: 24px 0;
    }

    :deep(.md-link) {
        color: var(--el-color-primary);
        text-decoration: none;
        font-weight: 500;
        border-bottom: 1px dashed rgba(37, 99, 235, 0.4);

        &:hover {
            border-bottom-style: solid;
        }

        .md-ext-arrow {
            font-size: 11px;
            margin-left: 2px;
        }
    }

    :deep(.md-code-block) {
        margin: 14px 0 18px;
        border-radius: 10px;
        overflow: hidden;
        border: 1px solid #1e293b;
        background: #0f172a;
    }

    :deep(.md-code-bar) {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 7px 14px;
        background: #1e293b;
        border-bottom: 1px solid #334155;

        .md-code-dots {
            display: inline-flex;
            gap: 6px;

            i {
                width: 9px;
                height: 9px;
                border-radius: 50%;
                display: inline-block;

                &:nth-child(1) {
                    background: #ef4444;
                }
                &:nth-child(2) {
                    background: #f59e0b;
                }
                &:nth-child(3) {
                    background: #10b981;
                }
            }
        }

        .md-code-lang {
            font-family: var(--el-font-family-monospace, monospace);
            font-size: 11px;
            font-weight: 600;
            color: #94a3b8;
            letter-spacing: 0.05em;
        }

        .md-code-copy-btn {
            border: 1px solid #475569;
            background: rgba(255, 255, 255, 0.06);
            color: #e2e8f0;
            font-size: 11.5px;
            padding: 2px 9px;
            border-radius: 5px;
            cursor: pointer;
            transition: all 0.15s ease;

            &:hover {
                background: #2563eb;
                border-color: #2563eb;
                color: #ffffff;
            }
        }
    }

    :deep(.md-code-pre) {
        margin: 0;
        padding: 14px 16px;
        overflow-x: auto;
        font-family: var(--el-font-family-monospace, monospace);
        font-size: 12.5px;
        line-height: 1.65;
        color: #e2e8f0;
    }

    :deep(.md-table-wrap) {
        margin: 14px 0 18px;
        overflow-x: auto;
        border-radius: 9px;
        border: 1px solid var(--el-border-color-lighter);
    }

    :deep(.md-table) {
        width: 100%;
        border-collapse: collapse;
        font-size: 13px;

        th,
        td {
            padding: 9px 12px;
            text-align: left;
            border-bottom: 1px solid var(--el-border-color-lighter);
            line-height: 1.55;
        }

        th {
            background: var(--el-fill-color-light);
            font-weight: 700;
            color: var(--el-text-color-primary);
        }

        tr:last-child td {
            border-bottom: none;
        }

        tbody tr:hover {
            background: var(--el-fill-color-extra-light);
        }
    }

    :deep(.md-license-sheet) {
        padding: 12px 18px;
        border-radius: 10px;
        background: var(--el-fill-color-extra-light);
        border: 1px solid var(--el-border-color-lighter);

        .md-license-line {
            margin: 4px 0;
            font-size: 13.5px;
            line-height: 1.7;
            color: var(--el-text-color-regular);
        }

        .md-license-gap {
            height: 10px;
        }
    }
}

/* 底部上一篇 / 下一篇翻页卡 */
.reader-pager {
    margin-top: 32px;
    padding-top: 20px;
    border-top: 1px solid var(--el-border-color-lighter);
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 14px;
}

.pager-card {
    padding: 14px 16px;
    border-radius: 10px;
    border: 1px solid var(--el-border-color-lighter);
    background: var(--el-fill-color-blank);
    cursor: pointer;
    display: flex;
    flex-direction: column;
    gap: 4px;
    transition: all 0.16s ease;

    &.is-prev {
        text-align: left;
    }

    &.is-next {
        text-align: right;
    }

    .pager-dir {
        font-size: 11.5px;
        color: var(--el-text-color-secondary);
    }

    .pager-title {
        font-size: 14px;
        font-weight: 600;
        color: var(--el-color-primary);
    }

    .pager-path {
        font-family: var(--el-font-family-monospace, monospace);
        font-size: 11.5px;
        color: var(--el-text-color-secondary);
    }

    &:hover {
        border-color: var(--el-color-primary);
        background: var(--el-color-primary-light-9);
    }
}

/* ==================== 右栏：Sticky TOC 与快捷命令卡 ==================== */
.rm-help-toc {
    position: sticky;
    top: 12px;
}

.toc-inner {
    display: flex;
    flex-direction: column;
    gap: 12px;
}

.toc-card,
.toc-cli-card {
    background: var(--ag-bg-color-overlay);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 12px;
    padding: 14px;
}

.toc-header {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12.5px;
    font-weight: 700;
    color: var(--el-text-color-primary);
    margin-bottom: 10px;

    .toc-count {
        margin-left: auto;
        font-size: 11px;
        padding: 1px 6px;
        border-radius: 999px;
        background: var(--el-fill-color-light);
        color: var(--el-text-color-secondary);
    }
}

.toc-nav {
    display: flex;
    flex-direction: column;
    gap: 3px;
    max-height: 400px;
    overflow-y: auto;
    border-left: 2px solid var(--el-border-color-lighter);
}

.toc-link {
    display: block;
    padding: 4px 8px 4px 12px;
    font-size: 12px;
    line-height: 1.45;
    color: var(--el-text-color-regular);
    text-decoration: none;
    border-left: 2px solid transparent;
    margin-left: -2px;
    transition: all 0.14s ease;

    &.is-level-3 {
        padding-left: 22px;
        font-size: 11.5px;
        color: var(--el-text-color-secondary);
    }

    &:hover,
    &.is-active {
        color: var(--el-color-primary);
        border-left-color: var(--el-color-primary);
        background: var(--el-color-primary-light-9);
        font-weight: 600;
    }
}

.toc-empty {
    font-size: 12px;
    color: var(--el-text-color-secondary);
}

.cli-snippet-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
}

.cli-snippet-item {
    padding: 8px 10px;
    border-radius: 8px;
    background: var(--el-fill-color-light);
    border: 1px solid var(--el-border-color-lighter);
    cursor: pointer;
    transition: all 0.15s ease;

    &:hover {
        border-color: var(--el-color-primary);
        background: var(--el-color-primary-light-9);
    }

    .cli-label {
        font-size: 11px;
        color: var(--el-text-color-secondary);
        margin-bottom: 3px;
    }

    .cli-code {
        font-family: var(--el-font-family-monospace, monospace);
        font-size: 11.5px;
        font-weight: 600;
        color: var(--el-text-color-primary);
        display: block;
        word-break: break-all;
    }
}
</style>
