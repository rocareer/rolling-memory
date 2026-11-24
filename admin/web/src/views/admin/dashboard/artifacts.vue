<template>
    <div class="default-main rm-artifacts-console">
        <!-- 顶部标题栏与 WebSocket 实时流状态 -->
        <div class="console-hero">
            <div class="hero-left">
                <span class="hero-title">
                    {{
                        isEn
                            ? 'Memory Console · Rolling Autonoetic Memory Engine (Vector-Free & LLM-Free)'
                            : '记忆控制台 · Rolling Autonoetic Memory Engine（洛灵自知记忆引擎 · 向量无关 & LLM 无关）'
                    }}
                </span>
                <el-tooltip :content="heroHint" placement="bottom" :show-after="250" :enterable="false" :hide-after="0">
                    <span class="term-hint">?</span>
                </el-tooltip>
                <el-tag size="small" :type="status.engine_online ? 'success' : 'danger'">
                    {{ status.engine_online ? (isEn ? 'Engine Online' : '引擎在线') : isEn ? 'Offline' : '离线' }}
                </el-tag>
                <el-tag size="small" type="primary"> ROLLING MEMORY · 0-VECTOR · 0-LLM </el-tag>
                <el-tag size="small" :type="wsConnected ? 'success' : 'info'" effect="plain" class="sse-badge">
                    <span class="sse-dot" :class="{ 'is-live': wsConnected }"></span>
                    {{
                        wsConnected
                            ? isEn
                                ? '⚡ WS Live Stream (0-Poll)'
                                : '⚡ WS 实时流已连接 (零轮询)'
                            : isEn
                              ? 'Reconnecting WS...'
                              : 'WS 重连中...'
                    }}
                </el-tag>
                <el-tooltip :content="wsHint" placement="bottom" :show-after="250" :enterable="false" :hide-after="0">
                    <span class="term-hint">?</span>
                </el-tooltip>
            </div>
            <div class="hero-right">
                <el-button size="small" plain data-testid="btn-artifacts-system-update" @click="showSystemUpdateDialog = true">
                    {{ isEn ? 'Version & Update' : '版本与在线更新' }}
                </el-button>
                <el-button
                    size="small"
                    plain
                    data-testid="btn-artifacts-system-service"
                    @click="router.push('/admin/plugin/rolling-memory/system')"
                >
                    {{ isEn ? 'System Service' : '系统管理' }}
                </el-button>
                <el-button size="small" plain @click="router.push('/admin/dashboard')">
                    {{ isEn ? 'Live Console' : '实时控制台' }}
                </el-button>
                <el-button size="small" plain @click="router.push('/admin/dashboard/roi')">
                    {{ isEn ? 'ROI Monitor' : 'ROI 监控屏' }}
                </el-button>
                <el-button size="small" plain @click="router.push('/admin/dashboard/graph')">
                    {{ isEn ? 'Memory Graph' : '记忆图谱' }}
                </el-button>
                <el-button size="small" type="primary" plain @click="router.push('/admin/plugin/rolling-memory/index')">
                    {{ isEn ? 'Manage All Memories' : '管理所有记忆' }}
                </el-button>
                <el-button size="small" type="success" plain @click="router.push('/admin/plugin/rolling-memory/recall')">
                    {{ isEn ? 'Recall Debugger' : '检索调试' }}
                </el-button>
            </div>
        </div>

        <!-- 实时数据流拓扑带（Vector-Free & LLM-Free Pipeline） -->
        <div class="arch-pipeline-strip">
            <div class="pipe-stage is-enhanced">
                <div class="pipe-eyebrow">{{ isEn ? '① LOCAL PLAINTEXT VAULT' : '① 本地 SQLite 明文金库' }}</div>
                <div class="pipe-main">
                    <strong>{{
                        isEn ? 'SQLite Plaintext Vault (Shannon Gain · Cluster Kernel)' : '本地 SQLite 明文金库（香农自信息增益 · 簇检索内核）'
                    }}</strong>
                    <span class="pipe-main-right">
                        <el-tag size="small" type="success" effect="plain">local_plain · 0 Egress</el-tag>
                        <el-tooltip :content="vaultStageHint" placement="bottom" :show-after="250" :enterable="false" :hide-after="0">
                            <span class="term-hint">?</span>
                        </el-tooltip>
                    </span>
                </div>
            </div>
            <div class="pipe-arrow">→</div>
            <div class="pipe-stage is-enhanced">
                <div class="pipe-eyebrow">
                    {{
                        isEn ? '② LOCAL RECALL (Local Vault · local_plain)' : '② 本地检索（SQLite 明文金库 · local_plain）'
                    }}
                </div>
                <div class="pipe-main">
                    <strong>{{ status.artifacts?.upstream_endpoint || 'local://vault' }}</strong>
                    <span class="pipe-main-right">
                        <el-tag size="small" type="success" effect="plain">
                            {{ isEn ? 'Local Vault Only' : '仅本地金库' }}
                        </el-tag>
                        <el-tooltip :content="recallStageHint" placement="bottom" :show-after="250" :enterable="false" :hide-after="0">
                            <span class="term-hint">?</span>
                        </el-tooltip>
                    </span>
                </div>
            </div>
            <div class="pipe-arrow">→</div>
            <div class="pipe-stage is-wide is-enhanced">
                <div class="pipe-eyebrow">
                    {{
                        isEn
                            ? '③ THREE-LAYER MEMORY PIPELINE (BACKGROUND × SESSIONS × RECALL TRAIL)'
                            : '③ 三层记忆协同管线（背景知识 × 会话记录 × 检索沉淀）'
                    }}
                    <el-tooltip :content="pipelineHint" placement="bottom" :show-after="250" :enterable="false" :hide-after="0">
                        <span class="term-hint">?</span>
                    </el-tooltip>
                </div>
                <div class="pipe-chips">
                    <el-tooltip :content="layerHints.l1" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                        <MemoryLayerBadge layer="background" :label="isEn ? 'L1 Background (Docs & Code)' : 'L1 文档与代码'" size="default" />
                    </el-tooltip>
                    <span class="pipe-plus">×</span>
                    <el-tooltip :content="layerHints.l2" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                        <MemoryLayerBadge layer="episodic" :label="isEn ? 'L2 Sessions (Episodic)' : 'L2 会话记录'" size="default" />
                    </el-tooltip>
                    <span class="pipe-plus">×</span>
                    <el-tooltip :content="layerHints.l3" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                        <MemoryLayerBadge layer="noetic" :label="isEn ? 'L3 Recall Trail (Noetic)' : 'L3 检索沉淀'" size="default" />
                    </el-tooltip>
                </div>
            </div>
        </div>

        <!-- 第一部分：运行期辅助开关与接口绑定状态 -->
        <div class="section-card">
            <div class="section-head">
                <div class="section-title">
                    {{ isEn ? '1. Engine Binding & Runtime Switches' : '1. 引擎连接状态与运行开关' }}
                    <el-tooltip :content="sectionOneHint" placement="bottom" :show-after="250" :enterable="false" :hide-after="0">
                        <span class="term-hint">?</span>
                    </el-tooltip>
                </div>
                <div class="storage-switch-group">
                    <el-tag size="small" type="success" effect="plain">
                        {{
                            isEn
                                ? 'Engine: Rolling Autonoetic Memory Engine'
                                : '引擎：Rolling Autonoetic Memory Engine（洛灵自知记忆引擎）'
                        }}
                    </el-tag>
                    <el-tag size="small" type="primary" effect="plain">
                        {{ isEn ? 'Memory Scope: Local Plaintext Vault (local_plain)' : '记忆域：本地明文金库 (local_plain)' }}
                    </el-tag>
                    <el-tooltip :content="scopeHint" placement="bottom" :show-after="250" :enterable="false" :hide-after="0">
                        <span class="term-hint">?</span>
                    </el-tooltip>
                </div>
            </div>

            <div class="orthogonal-bar">
                <div class="ortho-left">
                    <span class="ortho-title">{{ isEn ? '⚡ Pure-Math Rolling Autonoetic Memory Engine:' : '⚡ 纯数学洛灵自知记忆引擎：' }}</span>
                    <el-tooltip :content="engineHint" placement="bottom" :show-after="250" :enterable="false" :hide-after="0">
                        <span class="term-hint">?</span>
                    </el-tooltip>
                    <el-tag size="small" type="success" effect="plain">
                        {{
                            isEn
                                ? '100% Vector-Free · 100% LLM-Free · Zero Model Config Needed'
                                : '100% 向量无关 · 100% LLM 无关 · 无需配置任何向量或大模型'
                        }}
                    </el-tag>
                </div>
                <div class="ortho-right">
                    <span class="switch-item">
                        <el-checkbox
                            :model-value="Boolean(status.switches?.ledger)"
                            :disabled="togglingKey !== ''"
                            @change="(val: string | number | boolean) => toggle('ledger', String(Boolean(val)))"
                        >
                            {{ isEn ? 'Recall call ledger (ledger)' : '召回调用账本 (ledger)' }}
                        </el-checkbox>
                        <el-tooltip :content="switchHints.ledger" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                            <span class="term-hint">?</span>
                        </el-tooltip>
                    </span>
                </div>
            </div>
        </div>

        <!-- 第二部分：📊 记忆产物与洛灵自知记忆引擎明文金库实时统计台 -->
        <div class="section-card">
            <div class="section-head">
                <div class="section-title">
                    {{ isEn ? '2. Memory Store Telemetry' : '2. 记忆库实时统计' }}
                    <el-tooltip :content="sectionTwoHint" placement="bottom" :show-after="250" :enterable="false" :hide-after="0">
                        <span class="term-hint">?</span>
                    </el-tooltip>
                </div>
            </div>

            <el-row :gutter="12" class="stat-cards-row">
                <el-col :xs="24" :sm="12" :xl="6">
                    <div class="stat-box">
                        <div class="stat-box-head">
                            <span>
                                {{ isEn ? '① Vault Documents (L1 + L2)' : '① 本地明文金库文档 (Vault Docs)' }}
                                <el-tooltip :content="cardHints.docs" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                                    <span class="term-hint">?</span>
                                </el-tooltip>
                            </span>
                            <el-tag size="small" type="info">L1 + L2</el-tag>
                        </div>
                        <div class="stat-big">
                            {{ status.artifacts?.vault_docs ?? 0 }} <small>{{ isEn ? 'units' : '条单元' }}</small>
                        </div>
                        <div class="stat-foot">
                            <span
                                >{{ isEn ? 'Addresses' : '记忆地址' }}: <b>{{ status.artifacts?.vault_cells ?? 0 }}</b>
                                <el-tooltip :content="cardHints.cells" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                                    <span class="term-hint">?</span>
                                </el-tooltip>
                            </span>
                        </div>
                    </div>
                </el-col>

                <el-col :xs="24" :sm="12" :xl="6">
                    <div class="stat-box">
                        <div class="stat-box-head">
                            <span>
                                {{ isEn ? '② L3 Recall Trail (Noetic)' : '② L3 检索沉淀 (Noetic)' }}
                                <el-tooltip :content="cardHints.noetic" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                                    <span class="term-hint">?</span>
                                </el-tooltip>
                            </span>
                            <MemoryLayerBadge
                                layer="noetic"
                                :label="`${status.artifacts?.vault_noetic ?? 0} ${isEn ? 'active' : '条生效'}`"
                            />
                        </div>
                        <div class="stat-big">
                            {{ status.artifacts?.vault_noetic ?? 0 }} <small>{{ isEn ? 'entries' : '条沉淀条目' }}</small>
                        </div>
                        <div class="stat-foot">
                            <span>{{ isEn ? 'Query + Cited sentences + Answer' : '提问 Q + 引证原句 + 回答 A' }}</span>
                        </div>
                    </div>
                </el-col>

                <el-col :xs="24" :sm="12" :xl="6">
                    <div class="stat-box" style="cursor: pointer" @click="router.push('/admin/dashboard/graph')">
                        <div class="stat-box-head">
                            <span>
                                {{ isEn ? '③ Three-Layer Total (Graph)' : '③ 三层记忆合计 (Graph)' }}
                                <el-tooltip :content="cardHints.manifold" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                                    <span class="term-hint">?</span>
                                </el-tooltip>
                            </span>
                            <el-tag size="small" type="info">{{ status.artifacts?.eligible_nodes ?? 0 }} {{ isEn ? 'nodes' : '个节点' }}</el-tag>
                        </div>
                        <div class="stat-big">
                            {{ status.artifacts?.active_assertions ?? 0 }} <small>{{ isEn ? 'total memory units' : '条有效记忆单元' }}</small>
                        </div>
                        <div class="stat-foot">
                            <span
                                >{{ isEn ? 'Addresses' : '记忆地址' }}: <b>{{ status.artifacts?.vault_cells ?? 0 }}</b></span
                            >
                            <span
                                >{{ isEn ? 'L1+L2' : '文档/情景' }}: <b>{{ status.artifacts?.vault_docs ?? 0 }}</b></span
                            >
                            <span
                                >{{ isEn ? 'L3 Noetic' : '检索沉淀' }}: <b>{{ status.artifacts?.vault_noetic ?? 0 }}</b></span
                            >
                        </div>
                    </div>
                </el-col>

                <el-col :xs="24" :sm="12" :xl="6">
                    <div class="stat-box">
                        <div class="stat-box-head">
                            <span>
                                {{
                                    isEn ? '④ Retrieval Path (Local)' : '④ 检索通道（本地）'
                                }}
                                <el-tooltip :content="cardHints.channel" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                                    <span class="term-hint">?</span>
                                </el-tooltip>
                            </span>
                            <el-tag size="small" type="success">{{ isEn ? 'local_plain' : '本地模式' }}</el-tag>
                        </div>
                        <div class="stat-big">
                            {{ isEn ? 'Local' : '本地' }}
                            <small>{{ isEn ? 'Cluster Kernel (local_plain)' : '簇检索内核 (local_plain)' }}</small>
                        </div>
                        <div class="stat-foot">
                            <span
                                ><code>{{ status.artifacts?.upstream_endpoint || 'local://vault' }}</code></span
                            >
                        </div>
                    </div>
                </el-col>
            </el-row>

            <!-- 近期记忆与 Hook 实时双栏 -->
            <el-row :gutter="12" style="margin-top: 12px">
                <el-col :xs="24" :lg="12">
                    <div class="sub-panel">
                        <div class="sub-panel-title">{{ isEn ? 'Latest Memory Records (Live WS)' : '最新写入的记忆条目 (WS 实时流)' }}</div>
                        <el-table
                            :data="status.recent_assertions || []"
                            size="small"
                            border
                            stripe
                            class="ag-data-table"
                            header-cell-class-name="table-header-cell"
                            max-height="220"
                            style="cursor: pointer"
                            @row-click="(row: any) => row?.id && router.push({ path: '/admin/plugin/rolling-memory/index', query: { unit_id: row.id, ref: String(Date.now()) } })"
                        >
                            <el-table-column prop="rendered" :label="isEn ? 'Memory Content' : '记忆内容'">
                                <template #default="{ row }">
                                    <MemoryHoverPreview
                                        :text="row.rendered"
                                        :title="row.id"
                                        :subtitle="row.time_text || ''"
                                        :hint="{ layer: row.layer || row.warrant }"
                                    >
                                        <div class="rm-artifact-cell">{{ row.rendered }}</div>
                                    </MemoryHoverPreview>
                                </template>
                            </el-table-column>
                            <el-table-column prop="confidence" width="120">
                                <template #header>
                                    {{ isEn ? 'Weight' : '记忆权重' }}
                                    <el-tooltip :content="tableHints.weight" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                                        <span class="term-hint">?</span>
                                    </el-tooltip>
                                </template>
                                <template #default="{ row }"> {{ ((row.confidence || 0) * 100).toFixed(0) }}% </template>
                            </el-table-column>
                        </el-table>
                    </div>
                </el-col>
                <el-col :xs="24" :lg="12">
                    <div class="sub-panel">
                        <div class="sub-panel-title">
                            {{ isEn ? 'Live MCP Recall Stream (WS)' : 'MCP Recall 实时检索流水 (WS 实时流)' }}
                            <el-tooltip :content="tableHints.hookStream" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                                <span class="term-hint">?</span>
                            </el-tooltip>
                        </div>
                        <el-table
                            :data="status.hook_events || []"
                            size="small"
                            border
                            stripe
                            class="ag-data-table"
                            header-cell-class-name="table-header-cell"
                            max-height="220"
                        >
                            <el-table-column prop="time_text" :label="isEn ? 'Time' : '时间'" width="80" />
                            <el-table-column prop="event" width="110">
                                <template #header>{{ isEn ? 'Event' : '事件' }}</template>
                                <template #default="{ row }">{{ eventLabel(row.event) }}</template>
                            </el-table-column>
                            <el-table-column prop="outcome" width="130">
                                <template #header>
                                    {{ isEn ? 'Outcome' : '结果' }}
                                    <el-tooltip :content="tableHints.outcome" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                                        <span class="term-hint">?</span>
                                    </el-tooltip>
                                </template>
                                <template #default="{ row }">
                                    <el-tag size="small">{{ outcomeLabel(row.outcome) }}</el-tag>
                                </template>
                            </el-table-column>
                            <el-table-column>
                                <template #header>
                                    {{ isEn ? 'Detail' : '明细' }}
                                    <el-tooltip :content="tableHints.detail" placement="top" :show-after="250" :enterable="false" :hide-after="0">
                                        <span class="term-hint">?</span>
                                    </el-tooltip>
                                </template>
                                <template #default="{ row }">
                                    <span>{{ detailText(row) }}</span>
                                </template>
                            </el-table-column>
                        </el-table>
                    </div>
                </el-col>
            </el-row>
        </div>

        <SystemUpdateDialog v-model="showSystemUpdateDialog" @updated="loadStatusOnce" />
    </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useAdminInfo } from '@/stores/adminInfo'
import MemoryLayerBadge from '@/components/memory/MemoryLayerBadge.vue'
import { MonitorSocket } from '@/utils/monitorSocket'
import request from '@/utils/request'
import MemoryHoverPreview from '../plugin/rolling-memory/MemoryHoverPreview.vue'
import SystemUpdateDialog from '../plugin/rolling-memory/systemUpdateDialog.vue'

defineOptions({
    name: 'dashboard/artifacts',
})

const { locale } = useI18n()
const router = useRouter()
const adminInfo = useAdminInfo()
const isEn = computed(() =>
    String(locale.value || '')
        .toLowerCase()
        .startsWith('en')
)

const showSystemUpdateDialog = ref(false)
const togglingKey = ref('')
const wsConnected = ref(false)
let statusSocket: MonitorSocket | null = null

const status = ref<any>({})

// ── 「?」悬停人话说明：口径对齐公开帮助文档 docs/plugin-console.md 与后台实现 ──
const heroHint = computed(() =>
    isEn.value
        ? 'The Rolling Autonoetic Memory Engine is the local memory engine: every memory becomes a pure-math “wave fingerprint”. Retrieval and ranking are pure local math (token-based cluster retrieval + self-information gain weighting) — no embedding model, no large model, no network calls.'
        : '「洛灵自知记忆引擎」是本机记忆引擎：每条记忆被编码为纯数学「波形指纹」；检索与排序全部由本机纯数学完成（纯子串分词 + 实体簇/时空簇检索 + 自信息量加权），不需要任何向量嵌入模型或大模型，也不联网。'
)

const wsHint = computed(() =>
    isEn.value
        ? 'WebSocket live push: this page is refreshed over a shared WebSocket channel instead of timer polling (0-Poll), so config changes and new memories appear instantly.'
        : 'WebSocket 实时推送：本页数字由后台通过单连接 WebSocket 主动推送更新，不做定时轮询（0-Poll）；配置改动与记忆写入会立即反映在这里。'
)

const vaultStageHint = computed(() =>
    isEn.value
        ? 'All raw memories live in a local SQLite database. Retrieval runs a fully local deterministic kernel: pure-substring tokenization feeds an entity × spacetime cluster index, and ranking is re-weighted by Shannon self-information gain (rarer terms carry more signal) — no embedding model, no index server, no network call. local_plain means plaintext, local-only storage: nothing is uploaded.'
        : '记忆原文全部保存在本机的 SQLite 数据库里。检索由完全本地的确定性内核完成：纯子串分词后进入实体 × 时空簇索引，排序再用「香农自信息增益」（越罕见的词信息量越大）做连续加权——不依赖向量嵌入模型、不连检索服务、不联网。local_plain ＝ 本地明文模式：数据只存本机、不上传。'
)

const recallStageHint = computed(() =>
    isEn.value
        ? 'Where recalls actually run: straight against the local SQLite vault (local_plain mode) — zero network requests.'
        : '本次检索实际执行位置：直接读本机 SQLite 金库（本地明文模式 local_plain），零网络请求。'
)

const pipelineHint = computed(() =>
    isEn.value
        ? 'The three layers work together: L1 supplies background knowledge, L2 captures every conversation, and L3 crystallizes cited evidence from each recall into reusable “query + cited sentences” entries.'
        : '三层记忆各司其职、互相引证：L1 提供背景知识，L2 记录你与 Agent 的每一次对话，L3 把每次 recall 命中的证据结晶成可复用的「问答 + 引证原句」。'
)

const layerHints = computed(() =>
    isEn.value
        ? {
              l1: 'L1: project folders & documents you mounted in the console, chunked into the vault for retrieval.',
              l2: 'L2: every conversation with your agent is captured automatically — nothing to write by hand.',
              l3: 'L3: each recall crystallizes cited evidence into “query + cited sentences + answer”, reused by later retrievals.',
          }
        : {
              l1: 'L1 文档与代码：在后台挂载的项目目录 / 文档，按内容分块入库，供检索引用。',
              l2: 'L2 会话记录：你与 Agent 的每次对话自动沉淀，无需手动写入。',
              l3: 'L3 检索沉淀：每次 recall 命中证据后自动结晶为「提问 Q + 引证原句 + 回答 A」，下次直接复用。',
          }
)

const engineHint = computed(() =>
    isEn.value
        ? 'Memories are encoded as pure-math “wave fingerprints”; retrieval is pure-math resonance ranking over waves and address coordinates. That is why there is no model of any kind to configure.'
        : '记忆被编码为纯数学「波形指纹」；检索即在波形与地址坐标之上做纯数学共振排序。没有向量嵌入或大模型配置项——这就是「无需配置任何模型」的原因。'
)

const scopeHint = computed(() =>
    isEn.value
        ? 'Memory scope: the namespace these memories live in. local_plain = plaintext, local-only — raw text sits in a local SQLite file, not uploaded and not encrypted.'
        : '记忆域（Scope）：这批记忆的命名空间。local_plain ＝ 本地明文：原文只存本机 SQLite 文件，不上传、不加密。'
)

const sectionOneHint = computed(() =>
    isEn.value
        ? 'Shows which memory scope the engine is bound to and the runtime switches that change how your host — the AI editor or CLI assistant you use (e.g. ZCode, Claude Code) — talks to memory. Changes apply immediately and are saved to local config.'
        : '这里显示记忆引擎绑定到哪个记忆域，以及影响宿主行为的运行开关。宿主＝你正在使用的 AI 编辑器或命令行助手（如 ZCode、Claude Code）；开关改动立即生效并写入本机配置。'
)

const switchHints = computed(() =>
    isEn.value
        ? {
              ledger: 'Writes key readings of every recall call (hit / dedup-skip / empty, delivery and write outcome) into a local ledger. The ROI monitor is computed entirely from this ledger.',
          }
        : {
              ledger: '把每次 recall 调用的关键读数（是否命中 / 去重跳过 / 空检索、交付与写入结果）记入本机账本；ROI 监控屏的全部统计都来自这本账。',
          }
)

const sectionTwoHint = computed(() =>
    isEn.value
        ? 'Counts and live streams from your local vault — the SQLite file on your disk — refreshed in real time over WebSocket.'
        : '统计本机金库（存在你磁盘上的 SQLite 数据库文件）里的记忆规模与实时写入流水，随 WebSocket 实时刷新。'
)

const cardHints = computed(() =>
    isEn.value
        ? {
              docs: 'Every document unit in the vault: L1 background doc/code chunks + L2 session entries.',
              cells: 'Memory addresses: distinct addresses (fragment IDs stripped) — entries from the same source / coordinate group into one address.',
              noetic: 'Entries crystallized from recalls — “query + cited sentences + answer” — reused by later retrievals.',
              manifold:
                  'Combined count of all memory nodes across the three layers: docs/code + sessions + recall trail. Click to open the Memory Graph.',
              channel: 'Where recalls actually go: straight to the local vault, zero network requests.',
          }
        : {
              docs: '金库中的全部「文档单元」：L1 背景文档 / 代码分块 ＋ L2 会话记录条目。',
              cells: '记忆地址：去掉片段编号后的地址数量——同一来源 / 坐标的记忆片段归入同一个记忆地址。',
              noetic: '检索沉淀：每次 recall 命中证据后自动生成的「提问 + 引证原句 + 回答」条目，可被后续检索反复引用。',
              manifold: '三层记忆节点按地址与引证关系构成的整体图谱规模；合计 ＝ 文档/代码 ＋ 会话记录 ＋ 检索沉淀。点击卡片进入记忆图谱。',
              channel: '当前 recall 真正走的链路：直接查询本机金库，零网络请求。',
          }
)

const tableHints = computed(() =>
    isEn.value
        ? {
              weight: 'The entry’s own weight (0–100%), derived from evidence strength at write time; used for ranking in retrieval.',
              hookStream: 'One row per MCP recall tool call, showing whether verified evidence was matched and delivered.',
              outcome:
                  'Effective hit = verified evidence was returned to the agent (observable evidence reuse is tracked by the post-hoc audit: follow-up tool inputs/replies matched delivered anchors; a match does not prove a step was saved); No hits = no matching evidence above threshold.',
              detail: 'Delivered character count and end-to-end recall latency (ms).',
          }
        : {
              weight: '该条记忆自身的置信权重（0–100%）。当前版本写入的金库条目默认记为 100%。',
              hookStream: 'MCP recall 检索调用流水：每次 Agent 调取 recall 记一行（结果见「结果」列）。',
              outcome: '结果含义：有效命中＝命中证据并交付给 Agent（可观察的证据复用见事后审计：后续工具输入/回复与交付锚点匹配；匹配不证明省下一步）；无命中＝库里暂无匹配证据。',
              detail: '本次检索命中状态、交付字符数与端到端检索耗时（ms）。',
          }
)

const eventLabel = (event: string) => {
    const map: Record<string, [string, string]> = {
        recall: ['MCP recall', 'MCP 检索'],
        start: ['Session start', '会话开始'],
        close: ['Session end', '会话结束'],
    }
    const item = map[String(event || '')]
    return item ? (isEn.value ? item[0] : item[1]) : String(event || '')
}

const outcomeLabel = (outcome: string) => {
    const map: Record<string, [string, string]> = {
        injected: ['Effective hit', '有效命中'],
        'injected+nudge': ['Effective hit', '有效命中'],
        hits: ['Effective hit', '有效命中'],
        dedup: ['Dedup skip', '去重跳过'],
        absent: ['No hits', '无命中'],
        silent: ['Silent', '静默'],
        written: ['Effective hit', '有效命中'],
        replayed: ['Archived', '已归档'],
        idle: ['Nothing new', '无新增'],
        ok: ['OK', '正常'],
    }
    const item = map[String(outcome || '')]
    return item ? (isEn.value ? item[0] : item[1]) : String(outcome || '')
}

const detailText = (row: any) => {
    if (row?.event === 'recall') {
        const bands = Number(row.bands || 0)
        const chars = Number(row.chars || 0)
        const ms = Number(row.ms || 0)
        if (bands > 0 || chars > 0 || ms > 0) {
            return isEn.value ? `hits ${bands} · ${chars} chars · ${ms} ms` : `命中 ${bands} · ${chars} 字符 · ${ms} ms`
        }
        if (Number(row.dedup || 0) > 0) return isEn.value ? 'Duplicate of previous — skipped' : '与上一条重复，已跳过'
        return isEn.value ? 'No matching evidence' : '暂无匹配证据'
    }
    const imported = Number(row?.imported || 0)
    const replayed = Number(row?.replayed || 0)
    return isEn.value ? `imported ${imported} · archived ${replayed}` : `导入 ${imported} 条 · 归档 ${replayed} 条`
}

const loadStatusOnce = async () => {
    try {
        const res = await request<any>({ url: '/admin/plugin/rolling-memory/status', method: 'POST', data: {} })
        status.value = res.data.data ?? {}
    } catch {
        // ignore
    }
}

const startStatusWS = () => {
    stopStatusWS()
    const token = adminInfo.token
    if (!token) return
    statusSocket = new MonitorSocket({
        token,
        room: 'rm/status',
        onStatus: (st) => {
            wsConnected.value = st === 'connected'
        },
        onMessage: (env: any) => {
            if (env?.event === 'status' && env.data) {
                status.value = env.data
                wsConnected.value = true
            }
        },
    })
    statusSocket.start()
}

const stopStatusWS = () => {
    if (statusSocket) {
        statusSocket.stop()
        statusSocket = null
    }
    wsConnected.value = false
}

const toggle = async (key: string, value: string) => {
    togglingKey.value = key
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/toggle',
            method: 'POST',
            data: { key, value },
        })
        status.value = res.data.data ?? status.value
    } finally {
        togglingKey.value = ''
    }
}

onMounted(() => {
    loadStatusOnce()
    startStatusWS()
})

let artifactsDeactivated = false
onActivated(() => {
    if (!artifactsDeactivated) return
    artifactsDeactivated = false
    startStatusWS()
})

onDeactivated(() => {
    artifactsDeactivated = true
    stopStatusWS()
})

onBeforeUnmount(() => {
    stopStatusWS()
})
</script>

<style scoped lang="scss">
.rm-artifacts-console {
    display: flex;
    flex-direction: column;
    gap: 12px;
}

.console-hero {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 10px;
    padding: 12px 16px;
    background: var(--ag-bg-color-overlay);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;

    .hero-left {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
    }

    .hero-title {
        font-size: 16px;
        font-weight: 700;
        color: var(--el-text-color-primary);
    }

    .hero-right {
        display: flex;
        align-items: center;
        gap: 6px;
        flex-wrap: wrap;
    }
}

.sse-badge {
    display: inline-flex;
    align-items: center;
    gap: 6px;
}

.sse-dot {
    display: inline-block;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--el-color-info);

    &.is-live {
        background: var(--el-color-success);
        box-shadow: 0 0 6px var(--el-color-success);
    }
}

.term-hint {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    border: 1px solid var(--el-text-color-placeholder);
    color: var(--el-text-color-secondary);
    font-size: 10px;
    font-weight: 700;
    line-height: 1;
    margin: 0 2px;
    cursor: help;
    flex: none;
    vertical-align: middle;
    user-select: none;
}

.switch-item {
    display: inline-flex;
    align-items: center;
    gap: 2px;
}

.arch-pipeline-strip {
    display: flex;
    align-items: stretch;
    gap: 8px;
    padding: 10px 14px;
    background: var(--ag-bg-color-overlay);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
    flex-wrap: wrap;

    .pipe-stage {
        flex: 1;
        min-width: 200px;
        padding: 8px 12px;
        border-radius: 6px;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-extra-light);
        cursor: pointer;

        &.is-wide {
            flex: 1.8;
        }

        &.is-enhanced {
            border-color: var(--el-color-primary-light-5);
            background: var(--el-color-primary-light-9);
        }
    }

    .pipe-eyebrow {
        font-size: 11px;
        font-weight: 700;
        color: var(--el-text-color-secondary);
        margin-bottom: 4px;
    }

    .pipe-main {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
        font-size: 13px;
    }

    .pipe-main-right {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        flex: none;
    }

    .pipe-chips {
        display: flex;
        align-items: center;
        gap: 6px;
        flex-wrap: wrap;
    }

    .pipe-plus {
        font-weight: 700;
        color: var(--el-text-color-secondary);
    }

    .pipe-arrow {
        display: flex;
        align-items: center;
        font-weight: 700;
        color: var(--el-text-color-placeholder);
    }
}

.section-card {
    padding: 14px 16px;
    background: var(--ag-bg-color-overlay);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
}

.section-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 10px;
    margin-bottom: 12px;

    .section-title {
        font-size: 14px;
        font-weight: 700;
        color: var(--el-text-color-primary);
    }

    .storage-switch-group {
        display: flex;
        align-items: center;
        gap: 10px;
        flex-wrap: wrap;
    }
}

.orthogonal-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 10px;
    padding: 10px 12px;
    border-radius: 6px;
    background: var(--el-fill-color-lighter);

    .ortho-left,
    .ortho-right {
        display: flex;
        align-items: center;
        gap: 12px;
        flex-wrap: wrap;
    }

    .ortho-title {
        font-size: 12px;
        font-weight: 600;
        color: var(--el-text-color-regular);
    }
}

.stat-cards-row {
    row-gap: 12px;
}

.stat-box {
    padding: 12px 14px;
    border-radius: 6px;
    border: 1px solid var(--ag-border-color);
    border-left: 3px solid var(--el-color-primary);
    background: var(--ag-bg-color-overlay);
    height: 100%;
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    gap: 8px;

    .stat-box-head {
        display: flex;
        align-items: center;
        justify-content: space-between;
        font-size: 12px;
        font-weight: 600;
        color: var(--el-text-color-secondary);
    }

    .stat-big {
        font-size: 22px;
        font-weight: 700;
        font-family: monospace;
        color: var(--el-text-color-primary);

        small {
            font-size: 12px;
            font-weight: 400;
            color: var(--el-text-color-secondary);
        }
    }

    .stat-foot {
        display: flex;
        align-items: center;
        justify-content: space-between;
        flex-wrap: wrap;
        gap: 6px;
        font-size: 11px;
        color: var(--el-text-color-secondary);
    }
}

.sub-panel {
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 6px;
    padding: 10px;
    background: var(--el-fill-color-blank);

    .sub-panel-title {
        font-size: 12px;
        font-weight: 700;
        margin-bottom: 8px;
        color: var(--el-text-color-primary);
    }
}

.rm-artifact-cell {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
</style>
