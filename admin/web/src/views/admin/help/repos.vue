<template>
    <div class="default-main rm-repos-dossier">
        <!-- ==================== 顶部旗舰头：单仓同源架构与快捷导航 ==================== -->
        <section class="dossier-hero">
            <div class="dossier-hero-top">
                <div class="dossier-title-col">
                    <div class="dossier-pills">
                        <span class="pill-org">
                            <Icon name="lucide-shield-check" :size="13" />
                            <span>GitHub · rocareer/rolling-memory</span>
                        </span>
                        <span class="pill-tag">{{ t('help.badge') }}</span>
                        <span class="pill-tag is-green">{{ isEn ? 'Single-Repo Co-Maintained' : '单仓同源 · 零外部拆仓' }}</span>
                    </div>
                    <h1 class="dossier-title">{{ t('help.reposPage.title') }}</h1>
                    <p class="dossier-subtitle">{{ t('help.reposPage.subtitle') }}</p>
                </div>

                <div class="dossier-actions">
                    <button type="button" class="dossier-btn" @click="goDocsPage()">
                        <Icon name="lucide-book-open" :size="14" />
                        <span>{{ t('help.goDocsPage') }}</span>
                    </button>
                    <a :href="MAIN_REPO_URL" target="_blank" rel="noopener noreferrer" class="dossier-btn is-primary">
                        <Icon name="lucide-git-branch" :size="14" />
                        <span>{{ t('help.openGitHub') }}</span>
                        <span>↗</span>
                    </a>
                </div>
            </div>

            <!-- 单仓同源 4 大模块架构流水条 -->
            <div class="single-repo-topology">
                <div class="topo-root-node">
                    <div class="topo-node-badge">GitHub Primary Repository</div>
                    <div class="topo-node-name">rocareer/rolling-memory</div>
                    <div class="topo-node-sub">{{ isEn ? 'Unified under the rocareer organization' : '统一归属 rocareer 组织' }}</div>
                </div>
                <div class="topo-connector">══▶</div>
                <div class="topo-pillars-grid">
                    <div v-for="pillar in repoPillars" :key="pillar.title" class="topo-pillar-card" @click="goDocsPage(pillar.docId)">
                        <div class="pillar-head">
                            <Icon :name="pillar.icon" :size="16" />
                            <span class="pillar-path">{{ pillar.path }}</span>
                        </div>
                        <div class="pillar-title">{{ pillar.title }}</div>
                        <div class="pillar-desc">{{ pillar.desc }}</div>
                    </div>
                </div>
            </div>
        </section>

        <!-- ==================== 双栏核心：作者立场声明卡 + 开源许可矩阵 ==================== -->
        <div class="dossier-dual-grid">
            <!-- 左卡：我们是谁 & 关于作者 & 四项诚实准则 (DECLARATION.md) -->
            <section class="dossier-panel is-author-panel">
                <header class="panel-head">
                    <div class="panel-head-left">
                        <Icon name="lucide-award" :size="18" />
                        <h2>{{ isEn ? 'Who We Are & About the Author (DECLARATION.md)' : '我们是谁 · 关于作者与诚实免责声明 (DECLARATION.md)' }}</h2>
                    </div>
                    <button type="button" class="panel-link-btn" @click="goDocsPage('root-declaration')">
                        <span>{{ isEn ? 'Read Full Declaration' : '阅读声明全文' }}</span>
                        <span>→</span>
                    </button>
                </header>

                <div class="author-profile-card">
                    <div class="author-avatar">A</div>
                    <div class="author-info">
                        <div class="author-name-row">
                            <span class="author-name">Albert</span>
                            <a href="mailto:albert@rocareer.com" class="author-email">albert@rocareer.com</a>
                            <span class="author-org-chip">Rocareer</span>
                            <span class="author-org-chip">Rolling-Memory Org</span>
                        </div>
                        <p class="author-role">
                            {{
                                isEn
                                    ? 'Founder of Rocareer · Co-Founder & Chief Architect of the Rolling-Memory Organization · Former Technical Partner / CTO at a leading AI enterprise'
                                    : 'Rocareer 组织创始人 · Rolling-Memory 组织联合创始人兼首席架构师 · 曾任人工智能头部企业技术合伙人 / CTO'
                            }}
                        </p>
                        <p class="author-domain">
                            {{
                                isEn
                                    ? 'Long-term researcher in Public Safety, Cryptography, and Trustworthy AI Memory Systems; currently focused on AI long-term memory architecture, Cambridge ATM-Bench & LoCoMo benchmark recomputation, structured graph retrieval, and the Rolling-CI crisis identification model.'
                                    : '长期深耕于公共安全、密码学研究与可信 AI 认知记忆系统等领域；现专注于 AI 长期记忆架构、记忆评测基准（剑桥 ATM-Bench 与 LoCoMo）复算、确定性结构化图检索研究及危机识别模型 Rolling-CI 研发，并带领大学生团队创新创业。'
                            }}
                        </p>
                    </div>
                </div>

                <!-- 四项诚实与免责准则 -->
                <div class="honesty-pillars">
                    <div v-for="item in honestyPrinciples" :key="item.title" class="honesty-item">
                        <div class="honesty-icon">
                            <Icon :name="item.icon" :size="15" />
                        </div>
                        <div class="honesty-body">
                            <div class="honesty-title">{{ item.title }}</div>
                            <div class="honesty-desc">{{ item.desc }}</div>
                        </div>
                    </div>
                </div>
            </section>

            <!-- 右卡：Apache License 2.0 许可与双组织联合权利人架构 (LICENSE & NOTICE) -->
            <section class="dossier-panel is-license-panel">
                <header class="panel-head">
                    <div class="panel-head-left">
                        <Icon name="lucide-scale" :size="18" />
                        <h2>
                            {{
                                isEn
                                    ? 'Apache License 2.0 & Joint Copyright Architecture (LICENSE)'
                                    : 'Apache License 2.0 开源许可与双组织联合权利人架构 (LICENSE)'
                            }}
                        </h2>
                    </div>
                    <button type="button" class="panel-link-btn" @click="goDocsPage('root-license')">
                        <span>{{ isEn ? 'Read Full License' : '阅读许可证全文' }}</span>
                        <span>→</span>
                    </button>
                </header>

                <div class="joint-licensor-box">
                    <div class="jl-label">{{ isEn ? 'Joint Copyright Holders (Licensor):' : '联合著作权人（Joint Licensor）：' }}</div>
                    <div class="jl-holders">
                        <span class="jl-chip">Rocareer Organization</span>
                        <span class="jl-plus">+</span>
                        <span class="jl-chip">Rolling-Memory Organization</span>
                        <span class="jl-plus">+</span>
                        <span class="jl-chip is-arch">Chief Architect Albert (albert@rocareer.com)</span>
                    </div>
                </div>

                <!-- 允许用途 vs 署名义务 对比矩阵 -->
                <div class="license-matrix">
                    <div class="matrix-col is-permitted">
                        <div class="matrix-col-head">
                            <Icon name="lucide-circle-check" :size="16" />
                            <span>{{ isEn ? 'Uses Permitted by Apache-2.0 (Commercial Use Included)' : 'Apache-2.0 允许的用途（含商业使用）' }}</span>
                        </div>
                        <ul class="matrix-list">
                            <li v-for="(item, idx) in permittedUses" :key="idx">{{ item }}</li>
                        </ul>
                    </div>

                    <div class="matrix-col is-boundaries">
                        <div class="matrix-col-head">
                            <Icon name="lucide-shield-alert" :size="16" />
                            <span>{{ isEn ? 'Apache-2.0 Attribution Duties & Brand Boundary' : 'Apache-2.0 署名义务与品牌边界' }}</span>
                        </div>
                        <ul class="matrix-list">
                            <li v-for="(item, idx) in licenseBoundaries" :key="idx">{{ item }}</li>
                        </ul>
                    </div>
                </div>

                <!-- 第三方合规归属摘要 -->
                <div class="third-party-bar" @click="goDocsPage('root-notice')">
                    <div class="tp-left">
                        <Icon name="lucide-file-check" :size="15" />
                        <span>{{ isEn ? 'Third-Party Open-Source Notices (NOTICE):' : '第三方开源组件合规归属（NOTICE）：' }}</span>
                    </div>
                    <div class="tp-badges">
                        <span>modernc.org/sqlite (BSD-3-Clause)</span>
                        <span>ai-go-admin / BuildAdmin (Apache-2.0)</span>
                        <span>Apache ECharts (Apache-2.0)</span>
                        <span>Vue 3 / Element Plus (MIT)</span>
                    </div>
                </div>
            </section>
        </div>

        <!-- ==================== 底部：随仓 13 篇公开文档与声明全景矩阵（卡片画廊 / 索引表格双视图） ==================== -->
        <section class="dossier-catalog-section">
            <div class="catalog-toolbar">
                <div class="catalog-title-group">
                    <h2>{{ t('help.reposPage.tableTitle') }}</h2>
                    <span class="catalog-sub-pill">
                        {{ OPEN_SOURCE_DOCS.length }} {{ isEn ? 'files co-maintained in repo' : '份随仓同源文档实时就绪' }}
                    </span>
                </div>

                <div class="catalog-view-switch">
                    <button type="button" class="view-btn" :class="{ active: viewMode === 'cards' }" @click="viewMode = 'cards'">
                        <Icon name="lucide-layout-grid" :size="14" />
                        <span>{{ isEn ? 'Card Gallery' : '卡片画廊' }}</span>
                    </button>
                    <button type="button" class="view-btn" :class="{ active: viewMode === 'table' }" @click="viewMode = 'table'">
                        <Icon name="lucide-table" :size="14" />
                        <span>{{ isEn ? 'Index Table' : '结构化表格' }}</span>
                    </button>
                </div>
            </div>

            <!-- 视图 A：高颜值卡片画廊 (Card Gallery) -->
            <div v-if="viewMode === 'cards'" class="catalog-cards-grid">
                <article v-for="doc in OPEN_SOURCE_DOCS" :key="doc.id" class="doc-gallery-card" @click="goDocsPage(doc.id)">
                    <div class="dgc-top">
                        <div class="dgc-icon">
                            <Icon :name="doc.icon" :size="18" />
                        </div>
                        <div class="dgc-meta">
                            <code class="dgc-path">{{ doc.path }}</code>
                            <span class="dgc-badge">{{ isEn ? doc.badgeEn : doc.badgeZh }}</span>
                        </div>
                    </div>

                    <h3 class="dgc-title">{{ isEn ? doc.shortTitleEn : doc.shortTitleZh }}</h3>
                    <p class="dgc-summary">{{ isEn ? doc.summaryEn : doc.summaryZh }}</p>

                    <div class="dgc-foot" @click.stop>
                        <span class="dgc-stats">
                            {{ doc.rawZh.split('\n').length }} {{ isEn ? 'lines' : '行' }} ·
                            {{ t(`help.categories.${doc.category}`) }}
                        </span>
                        <div class="dgc-actions">
                            <button type="button" class="dgc-btn" @click="goDocsPage(doc.id)">
                                {{ t('help.reposPage.previewBtn') }}
                            </button>
                            <a :href="doc.url" target="_blank" rel="noopener noreferrer" class="dgc-btn is-ext"> GitHub ↗ </a>
                        </div>
                    </div>
                </article>
            </div>

            <!-- 视图 B：结构化索引表格 (Index Table) -->
            <el-table v-else :data="OPEN_SOURCE_DOCS" border stripe size="default" class="catalog-el-table">
                <el-table-column :label="t('help.reposPage.colScope')" width="180">
                    <template #default="{ row }">
                        <el-tag size="small" :type="row.scope === 'root' ? 'primary' : 'success'">
                            {{ row.scope === 'root' ? t('help.scopeRoot') : t('help.scopeDocs') }}
                        </el-tag>
                    </template>
                </el-table-column>
                <el-table-column prop="path" :label="t('help.reposPage.colPath')" width="210">
                    <template #default="{ row }">
                        <code class="path-code">{{ row.path }}</code>
                    </template>
                </el-table-column>
                <el-table-column :label="t('help.reposPage.colCategory')" width="165">
                    <template #default="{ row }">
                        <el-tag size="small" type="info" effect="plain">
                            {{ t(`help.categories.${row.category}`) }}
                        </el-tag>
                    </template>
                </el-table-column>
                <el-table-column :label="t('help.reposPage.colTitle')" min-width="300">
                    <template #default="{ row }">
                        <div class="cell-title">{{ isEn ? row.titleEn : row.titleZh }}</div>
                        <div class="cell-sub">{{ isEn ? row.summaryEn : row.summaryZh }}</div>
                    </template>
                </el-table-column>
                <el-table-column :label="isEn ? 'Lines' : '行数'" width="100" align="right">
                    <template #default="{ row }">
                        <span class="mono-num">{{ row.rawZh.split('\n').length }}</span>
                    </template>
                </el-table-column>
                <el-table-column :label="t('help.reposPage.colAction')" width="190" align="center">
                    <template #default="{ row }">
                        <el-button size="small" type="primary" link @click="goDocsPage(row.id)">
                            {{ t('help.reposPage.previewBtn') }}
                        </el-button>
                        <el-button size="small" type="primary" link tag="a" :href="row.url" target="_blank" rel="noopener noreferrer">
                            {{ t('help.reposPage.openRemoteBtn') }}
                        </el-button>
                    </template>
                </el-table-column>
            </el-table>
        </section>
    </div>
</template>

<script setup lang="ts">
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { MAIN_REPO_URL, OPEN_SOURCE_DOCS } from './docsData'

defineOptions({
    name: 'help/repos',
})

const { t, locale } = useI18n()
const router = useRouter()

const isEn = computed(() =>
    String(locale.value || '')
        .toLowerCase()
        .startsWith('en')
)
const viewMode = ref<'cards' | 'table'>('cards')

const repoPillars = computed(() => [
    {
        icon: 'lucide-cpu',
        path: 'cmd/ + internal/',
        title: isEn.value ? 'Pure Go Mechanical Core' : '纯 Go 单二进制记忆核',
        desc: isEn.value
            ? 'CGO_ENABLED=0 single-file SQLite + memory-unit graph + 6-leg recall'
            : '零外部依赖 SQLite + 记忆单元逐字锚 + 六腿 0-LLM 召回',
        docId: 'docs-design',
    },
    {
        icon: 'lucide-plug-zap',
        path: 'skills/ + hooks/',
        title: isEn.value ? 'Multi-Harness Plugin' : '全宿主开箱即用插件',
        desc: isEn.value ? 'ZCode / Claude Code / Codex / Cursor / Windsurf / Cline' : '一键接入 8 大 Coding Agents + 生命周期 Hooks',
        docId: 'docs-harness',
    },
    {
        icon: 'lucide-layout-dashboard',
        path: 'admin/ + ui/',
        title: isEn.value ? 'Web Console & Graph UI' : 'Web 管理控制台与图谱看板',
        desc: isEn.value ? 'Live console, Web Worker force graph, and Evaluation Lab' : '实时控制台、Web Worker 记忆图谱与评测实验室',
        docId: 'docs-plugin-console',
    },
    {
        icon: 'lucide-book-marked',
        path: 'docs/*.md',
        title: isEn.value ? 'Co-Maintained Docs Hub' : '开源文档中心（docs/ 同源）',
        desc: isEn.value ? 'Maintained directly in repo; zero drift with code' : '不拆外部独立文档仓，随代码 100% 同步演进',
        docId: 'root-readme',
    },
])

const honestyPrinciples = computed(() => [
    {
        icon: 'lucide-check-check',
        title: isEn.value ? '1. Verbatim Provenance Honesty' : '1. 逐字证据锚诚实（Chain of Custody）',
        desc: isEn.value
            ? 'Every memory unit requires a verbatim span (trace:<hash>#start-end); missing knowledge returns absent=true truthfully.'
            : '每条记忆单元强制绑定原始文本逐字切片，交付即复核；本地不持有时如实返回 absent=true，绝不拿相似冒充事实。',
    },
    {
        icon: 'lucide-lock',
        title: isEn.value ? '2. Cryptographic Benchmark & Open Falsification' : '2. 评测密码学反作弊与随时开放质疑/证伪',
        desc: isEn.value
            ? 'Dataset SHA-256 and judge prompts SHA-1 are locked. Users can reproduce or falsify Rolling Autonoetic Memory Engine results anytime (0-Vector/0-LLM retrieval is $0; end-to-end QA judge uses your own LLM at your own cost).'
            : '题集（SHA-256）与判官提示词（三枚 SHA-1 钉）只读锁死；支持用户随时复现、质疑与证伪 Rolling Autonoetic Memory Engine 读数（0-Vector/0-LLM 检索 0 成本；端到端判分需自配 LLM 自付成本）。',
    },
    {
        icon: 'lucide-shield',
        title: isEn.value ? '3. Zero Plaintext Egress · Local Vault Stays Offline' : '3. 明文不出网 · 本地金库离线保管',
        desc: isEn.value
            ? 'Plaintext and the 256-bit key stay in the on-host SQLite vault, and CheckEgress blocks non-loopback traffic so memory data never leaves the host. Full Rolling Autonoetic Memory Engine capabilities require connecting to the /open/memory/v1/* API; the local vault stays available for offline inspection and export.'
            : '本地明文与 256-bit 密钥保存在本机 SQLite 金库；传输层 CheckEgress 物理阻断一切非 loopback 连接，记忆数据绝对不出网，private 记忆永不离开本机。完整洛灵自知记忆引擎能力需连接 /open/memory/v1/* 记忆服务接口；本地金库可离线查阅与导出。',
    },
    {
        icon: 'lucide-circle-alert',
        title: isEn.value ? '4. Honest Disclosure & "AS IS" Disclaimer' : '4. 能力局限坦诚披露与“按原样（AS IS）”免责',
        desc: isEn.value
            ? 'Human-defined invariants + AI-assisted implementation; experience is not omniscience. Provided "AS IS" without warranty.'
            : '采用「人类定义架构与验收门禁 + AI 辅助实现」模式；经验不等于全能，按“原样（AS IS）”提供，使用者须独立审查并自担风险。',
    },
])

const permittedUses = computed(() =>
    isEn.value
        ? [
              'Apache License 2.0 permits any purpose, including commercial use, modification, and redistribution, provided copyright, license, and NOTICE attributions are preserved. The scenarios below are typical examples, not an exhaustive list.',
              'Personal Non-Commercial Use on personal devices for coding, learning, and productivity.',
              'Non-Commercial Academic Research by students, faculty, and researchers at universities and research institutes (with official repository citation).',
              'Source inspection, local compilation, and Pull Request contributions back to the official repository.',
          ]
        : [
              'Apache License 2.0 允许包括商业使用在内的一切用途，并允许修改与再分发（须保留版权、许可证与 NOTICE 署名）；以下仅为典型使用场景，不构成用途限制。',
              '个人非商业自用：自然人在个人设备上用于日常编程辅助、学习与效率提升（不涉及商业利益或经营报酬）。',
              '非商业学术研究：高校、科研院所师生用于非商业科学研究、基准评测复算与学术论文发表（发表成果须明确引用本项目）。',
              '源码审阅、本地自编译运行，以及通过官方仓库提交 Pull Request 贡献改进。',
          ]
)

const licenseBoundaries = computed(() =>
    isEn.value
        ? [
              'Preserve Attribution (Apache-2.0 Section 4): any redistribution must keep the copyright notice, this license text, and the NOTICE file; modified files must carry prominent change notices.',
              'No Trademark or Endorsement Grant (Apache-2.0 Section 6, see TRADEMARK.md): redistributed copies, including listings on third-party plugin marketplaces, registries, or app stores, must not use the project names as their primary brand or imply official status.',
          ]
        : [
              '保留署名（Apache-2.0 第 4 条）：任何再分发均须保留版权声明、本许可证文本与 NOTICE 文件；修改过的文件须附显著的变更说明。',
              '无商标与背书授权（Apache-2.0 第 6 条，见 TRADEMARK.md）：再分发副本（含在第三方插件市场、应用商店或分发渠道上架）不得以本项目名称作为主品牌，也不得暗示官方身份或官方背书。',
          ]
)

const goDocsPage = (docId?: string) => {
    const path = `${adminBaseRoutePath}/help/docs`
    if (docId) {
        router.push({ path, query: { doc: docId } })
    } else {
        router.push({ path })
    }
}
</script>

<style scoped lang="scss">
.rm-repos-dossier {
    display: flex;
    flex-direction: column;
    gap: 16px;
    padding-bottom: 28px;
}

/* ==================== 顶部旗舰头 ==================== */
.dossier-hero {
    padding: 22px 24px 20px;
    border-radius: 14px;
    background:
        radial-gradient(640px circle at 95% 10%, rgba(16, 185, 129, 0.12), transparent 65%),
        radial-gradient(540px circle at 5% 90%, rgba(37, 99, 235, 0.1), transparent 60%), var(--ag-bg-color-overlay);
    border: 1px solid var(--el-border-color-lighter);
}

.dossier-hero-top {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
    margin-bottom: 18px;
}

.dossier-pills {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: 8px;
}

.pill-org {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 3px 10px;
    border-radius: 999px;
    font-family: var(--el-font-family-monospace, monospace);
    font-size: 12px;
    font-weight: 600;
    color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
    border: 1px solid var(--el-color-primary-light-5);
}

.pill-tag {
    padding: 3px 10px;
    border-radius: 999px;
    font-size: 12px;
    font-weight: 500;
    color: var(--el-text-color-regular);
    background: var(--el-fill-color-light);

    &.is-green {
        color: #0f766e;
        background: rgba(16, 185, 129, 0.12);
    }
}

.dossier-title {
    margin: 0 0 6px;
    font-size: 21px;
    font-weight: 700;
    color: var(--el-text-color-primary);
}

.dossier-subtitle {
    margin: 0;
    font-size: 13px;
    color: var(--el-text-color-secondary);
    line-height: 1.55;
    max-width: 820px;
}

.dossier-actions {
    display: flex;
    align-items: center;
    gap: 8px;
}

.dossier-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 34px;
    padding: 0 14px;
    border-radius: 8px;
    font-size: 12.5px;
    font-weight: 600;
    color: var(--el-text-color-regular);
    background: var(--el-fill-color-blank);
    border: 1px solid var(--el-border-color);
    cursor: pointer;
    text-decoration: none;
    transition: all 0.15s ease;

    &:hover {
        color: var(--el-color-primary);
        border-color: var(--el-color-primary);
    }

    &.is-primary {
        color: #ffffff;
        background: linear-gradient(135deg, #2563eb, #0284c7);
        border-color: transparent;

        &:hover {
            opacity: 0.94;
            color: #ffffff;
        }
    }
}

/* 单仓同源拓扑图 */
.single-repo-topology {
    display: flex;
    align-items: stretch;
    gap: 12px;
    padding: 14px;
    border-radius: 12px;
    background: var(--el-fill-color-extra-light);
    border: 1px solid var(--el-border-color-lighter);
    flex-wrap: wrap;
}

.topo-root-node {
    min-width: 230px;
    padding: 14px 16px;
    border-radius: 10px;
    background: linear-gradient(135deg, #0f172a, #1e293b);
    color: #f8fafc;
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 4px;

    .topo-node-badge {
        font-size: 10.5px;
        text-transform: uppercase;
        letter-spacing: 0.06em;
        color: #38bdf8;
        font-weight: 700;
    }

    .topo-node-name {
        font-family: var(--el-font-family-monospace, monospace);
        font-size: 15px;
        font-weight: 700;
    }

    .topo-node-sub {
        font-size: 11.5px;
        color: #94a3b8;
    }
}

.topo-connector {
    display: flex;
    align-items: center;
    font-family: var(--el-font-family-monospace, monospace);
    font-weight: 700;
    color: var(--el-color-primary);
    padding: 0 4px;
}

.topo-pillars-grid {
    flex: 1;
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
    gap: 10px;
}

.topo-pillar-card {
    padding: 11px 13px;
    border-radius: 9px;
    background: var(--ag-bg-color-overlay);
    border: 1px solid var(--el-border-color-lighter);
    cursor: pointer;
    transition: all 0.15s ease;

    &:hover {
        border-color: var(--el-color-primary);
        transform: translateY(-1px);
    }

    .pillar-head {
        display: flex;
        align-items: center;
        justify-content: space-between;
        color: var(--el-color-primary);
        margin-bottom: 5px;

        .pillar-path {
            font-family: var(--el-font-family-monospace, monospace);
            font-size: 11px;
            color: var(--el-text-color-secondary);
        }
    }

    .pillar-title {
        font-size: 13px;
        font-weight: 700;
        color: var(--el-text-color-primary);
        margin-bottom: 3px;
    }

    .pillar-desc {
        font-size: 11.5px;
        line-height: 1.45;
        color: var(--el-text-color-secondary);
    }
}

/* ==================== 中部双栏：作者立场卡 + 开源许可矩阵（小屏/笔记本自动上下全宽舒展排布） ==================== */
.dossier-dual-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 16px;
}

@media (max-width: 1520px) {
    .dossier-dual-grid {
        grid-template-columns: 1fr;
    }
}

.dossier-panel {
    background: var(--ag-bg-color-overlay);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 14px;
    padding: 20px 22px;
    display: flex;
    flex-direction: column;
    gap: 14px;
}

.panel-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding-bottom: 12px;
    border-bottom: 1px solid var(--el-border-color-lighter);

    .panel-head-left {
        display: flex;
        align-items: center;
        gap: 8px;
        color: var(--el-color-primary);

        h2 {
            margin: 0;
            font-size: 15.5px;
            font-weight: 700;
            color: var(--el-text-color-primary);
        }
    }

    .panel-link-btn {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        border: none;
        background: var(--el-color-primary-light-9);
        color: var(--el-color-primary);
        font-size: 12px;
        font-weight: 600;
        padding: 4px 10px;
        border-radius: 6px;
        cursor: pointer;

        &:hover {
            background: var(--el-color-primary);
            color: #ffffff;
        }
    }
}

.author-profile-card {
    display: flex;
    align-items: flex-start;
    gap: 14px;
    padding: 14px;
    border-radius: 10px;
    background: var(--el-fill-color-extra-light);
    border: 1px solid var(--el-border-color-lighter);

    .author-avatar {
        width: 44px;
        height: 44px;
        border-radius: 10px;
        background: linear-gradient(135deg, #2563eb, #0ea5e9);
        color: #ffffff;
        font-size: 20px;
        font-weight: 800;
        display: flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
    }

    .author-name-row {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 8px;
        margin-bottom: 5px;

        .author-name {
            font-size: 15px;
            font-weight: 700;
            color: var(--el-text-color-primary);
        }

        .author-email {
            font-family: var(--el-font-family-monospace, monospace);
            font-size: 12px;
            color: var(--el-color-primary);
            text-decoration: none;
        }

        .author-org-chip {
            font-size: 11px;
            padding: 1px 7px;
            border-radius: 4px;
            background: var(--el-fill-color);
            color: var(--el-text-color-regular);
            font-weight: 600;
        }
    }

    .author-role {
        margin: 0 0 5px;
        font-size: 12.5px;
        font-weight: 600;
        color: var(--el-text-color-primary);
        line-height: 1.5;
    }

    .author-domain {
        margin: 0;
        font-size: 12px;
        color: var(--el-text-color-regular);
        line-height: 1.55;
    }
}

.honesty-pillars {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px;
}

@media (max-width: 640px) {
    .honesty-pillars {
        grid-template-columns: 1fr;
    }
}

.honesty-item {
    display: flex;
    align-items: flex-start;
    gap: 9px;
    padding: 10px 12px;
    border-radius: 8px;
    background: var(--el-fill-color-blank);
    border: 1px solid var(--el-border-color-lighter);

    .honesty-icon {
        color: var(--el-color-primary);
        margin-top: 2px;
        flex-shrink: 0;
    }

    .honesty-title {
        font-size: 12.5px;
        font-weight: 700;
        color: var(--el-text-color-primary);
        margin-bottom: 3px;
    }

    .honesty-desc {
        font-size: 11.5px;
        line-height: 1.5;
        color: var(--el-text-color-secondary);
    }
}

.joint-licensor-box {
    padding: 11px 14px;
    border-radius: 9px;
    background: var(--el-fill-color-extra-light);
    border: 1px solid var(--el-border-color-lighter);

    .jl-label {
        font-size: 11.5px;
        color: var(--el-text-color-secondary);
        margin-bottom: 6px;
    }

    .jl-holders {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 6px;
    }

    .jl-chip {
        padding: 3px 9px;
        border-radius: 6px;
        font-size: 12px;
        font-weight: 600;
        background: var(--ag-bg-color-overlay);
        border: 1px solid var(--el-border-color);
        color: var(--el-text-color-primary);

        &.is-arch {
            color: var(--el-color-primary);
            border-color: var(--el-color-primary-light-5);
            background: var(--el-color-primary-light-9);
        }
    }

    .jl-plus {
        font-weight: 700;
        color: var(--el-text-color-secondary);
    }
}

.license-matrix {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 12px;
}

@media (max-width: 640px) {
    .license-matrix {
        grid-template-columns: 1fr;
    }
}

.matrix-col {
    padding: 12px 14px;
    border-radius: 10px;
    border: 1px solid transparent;

    .matrix-col-head {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 13px;
        font-weight: 700;
        margin-bottom: 8px;
    }

    .matrix-list {
        margin: 0;
        padding-left: 18px;
        font-size: 12px;
        line-height: 1.6;
        color: var(--el-text-color-regular);

        li {
            margin: 5px 0;
        }
    }

    &.is-permitted {
        background: rgba(16, 185, 129, 0.06);
        border-color: rgba(16, 185, 129, 0.25);

        .matrix-col-head {
            color: #059669;
        }
    }

    &.is-boundaries {
        background: rgba(239, 68, 68, 0.06);
        border-color: rgba(239, 68, 68, 0.25);

        .matrix-col-head {
            color: #dc2626;
        }
    }
}

.third-party-bar {
    margin-top: auto;
    padding: 10px 12px;
    border-radius: 8px;
    background: var(--el-fill-color-light);
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;

    .tp-left {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 12px;
        font-weight: 600;
        color: var(--el-text-color-primary);
    }

    .tp-badges {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 6px;

        span {
            font-family: var(--el-font-family-monospace, monospace);
            font-size: 11px;
            padding: 2px 7px;
            border-radius: 4px;
            background: var(--ag-bg-color-overlay);
            color: var(--el-text-color-regular);
        }
    }
}

/* ==================== 底部文档全景画廊 ==================== */
.dossier-catalog-section {
    background: var(--ag-bg-color-overlay);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 14px;
    padding: 20px 22px;
}

.catalog-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 12px;
    margin-bottom: 16px;
}

.catalog-title-group {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;

    h2 {
        margin: 0;
        font-size: 16px;
        font-weight: 700;
        color: var(--el-text-color-primary);
    }

    .catalog-sub-pill {
        font-size: 12px;
        padding: 2px 10px;
        border-radius: 999px;
        background: var(--el-fill-color-light);
        color: var(--el-text-color-secondary);
    }
}

.catalog-view-switch {
    display: inline-flex;
    padding: 3px;
    border-radius: 8px;
    background: var(--el-fill-color-light);
    border: 1px solid var(--el-border-color-lighter);

    .view-btn {
        display: inline-flex;
        align-items: center;
        gap: 5px;
        padding: 4px 11px;
        border-radius: 6px;
        border: none;
        background: transparent;
        font-size: 12px;
        font-weight: 600;
        color: var(--el-text-color-regular);
        cursor: pointer;

        &.active {
            background: var(--ag-bg-color-overlay);
            color: var(--el-color-primary);
            box-shadow: 0 1px 4px rgba(0, 0, 0, 0.06);
        }
    }
}

.catalog-cards-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(310px, 1fr));
    gap: 14px;
}

.doc-gallery-card {
    padding: 16px;
    border-radius: 11px;
    border: 1px solid var(--el-border-color-lighter);
    background: var(--el-fill-color-blank);
    cursor: pointer;
    display: flex;
    flex-direction: column;
    gap: 10px;
    transition: all 0.16s ease;

    &:hover {
        border-color: var(--el-color-primary);
        box-shadow: 0 6px 18px rgba(37, 99, 235, 0.08);
        transform: translateY(-2px);
    }

    .dgc-top {
        display: flex;
        align-items: center;
        gap: 10px;
    }

    .dgc-icon {
        width: 34px;
        height: 34px;
        border-radius: 8px;
        background: var(--el-color-primary-light-9);
        color: var(--el-color-primary);
        display: flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
    }

    .dgc-meta {
        flex: 1;
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 6px;

        .dgc-path {
            font-family: var(--el-font-family-monospace, monospace);
            font-size: 12px;
            font-weight: 600;
            color: var(--el-text-color-primary);
        }

        .dgc-badge {
            font-size: 11px;
            padding: 1px 7px;
            border-radius: 4px;
            background: var(--el-fill-color-light);
            color: var(--el-text-color-secondary);
        }
    }

    .dgc-title {
        margin: 0;
        font-size: 14.5px;
        font-weight: 700;
        color: var(--el-text-color-primary);
    }

    .dgc-summary {
        margin: 0;
        font-size: 12.5px;
        line-height: 1.55;
        color: var(--el-text-color-regular);
        flex: 1;
    }

    .dgc-foot {
        padding-top: 10px;
        border-top: 1px dashed var(--el-border-color-lighter);
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 8px;

        .dgc-stats {
            font-size: 11.5px;
            color: var(--el-text-color-secondary);
        }

        .dgc-actions {
            display: flex;
            align-items: center;
            gap: 6px;
        }

        .dgc-btn {
            padding: 3px 9px;
            border-radius: 5px;
            font-size: 11.5px;
            font-weight: 600;
            border: 1px solid var(--el-color-primary-light-5);
            background: var(--el-color-primary-light-9);
            color: var(--el-color-primary);
            cursor: pointer;
            text-decoration: none;

            &.is-ext {
                border-color: var(--el-border-color);
                background: var(--el-fill-color-blank);
                color: var(--el-text-color-regular);
            }
        }
    }
}

.path-code {
    font-family: var(--el-font-family-monospace, monospace);
    font-size: 12px;
}

.cell-title {
    font-weight: 600;
    font-size: 13px;
    color: var(--el-text-color-primary);
}

.cell-sub {
    margin-top: 2px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
}

.mono-num {
    font-family: var(--el-font-family-monospace, monospace);
    font-size: 12.5px;
}
</style>
