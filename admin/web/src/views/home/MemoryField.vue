<template>
    <div
        ref="graphPageRef"
        class="default-main memory-graph-page hero-graph-stage"
        :class="{
            'is-true-fullscreen': isFullscreen,
            'is-zen-hud': zenMode,
            'is-fs-drawer-open': fsDrawerOpen,
        }"
        :style="{ '--hud-top': `${graphHudTopPx}px`, '--hud-bottom-left': `${hudBottomLeftPx}px` }"
    >
        <div v-loading="loading" class="graph-body">
            <!-- 100% 整屏后台 Hypergraph3DEngine 三维活体记忆小宇宙画布 -->
            <div ref="threeContainerRef" class="graph-chart graph-chart-3d" />

                <!-- 顶部中央：全画幅星舰主指挥舱（内联 128~ALL 容量槽 + 球形/星盘切换 + 三层速滤 + LoCoMo 真实接口检索 + 真全屏） -->
                <div class="fs-cockpit-topbar" :class="{ 'is-drill': inDrill }">
                    <!-- 下一代 AI Agent 记忆范式预览 -->
                    <el-tooltip placement="bottom" :effect="isDark ? 'light' : 'dark'" :popper-class="isDark ? 'fs-preview-popper-dark' : 'fs-preview-popper-light'">
                        <template #content>
                            <div class="fs-preview-tip">
                                <strong>{{ isEn ? 'Next-Gen Agent Memory Preview' : '下一代 AI Agent 记忆范式预览' }}</strong>
                                <p>{{ isEn ? '0-LLM · 0-Vector · Zero-Plaintext Cloud Leakage.' : '0-LLM · 0-Vector · 0-明文云端泄漏。' }}</p>
                                <p>{{ isEn ? 'Pulse Mode exploration & closed beta applications are now open.' : '脉冲模式探索与封闭试用申请现已开放。' }}</p>
                            </div>
                        </template>
                        <span class="fs-preview-badge" @click="scrollToPulseSection()">
                            <span class="fs-badge-dot" />
                            {{ isEn ? 'Preview' : '预览功能' }}
                        </span>
                    </el-tooltip>

                    <span class="fs-divider" />

                <span
                    class="fs-live-pill"
                    :title="isEn ? 'Sample memory: 5,882 turns from the public LoCoMo dataset (click to run a recall)' : '示例记忆：公开数据集 LoCoMo 的 5882 轮对话（点击运行一次召回）'"
                    @click="triggerLiveLocomoRecall()"
                >
                    <span class="sse-dot is-live" />
                    {{ isEn ? `LoCoMo sample · ${corpusUnits} memories` : `LoCoMo 示例 · ${corpusUnits} 条记忆` }}
                </span>
                <span class="fs-stat-text">
                    <b>{{ activeNodes.length }}</b>/{{ totalNodeCount }} {{ isEn ? 'Nodes' : '节点' }} · <b>{{ activeEdges.length }}</b> {{ isEn ? 'Synapses' : '突触' }}
                </span>

                <span class="fs-divider" />

                <!-- 球形宇宙 ⇄ 展开星盘 3D 模式切换 -->
                <div class="fs-quick-layers">
                    <button
                        type="button"
                        class="fs-layer-chip"
                        :class="{ 'is-active': spaceMode === 'sphere' }"
                        @click="setSpaceMode('sphere')"
                    >
                        {{ isEn ? 'Sphere 3D' : '球形宇宙' }}
                    </button>
                    <button
                        type="button"
                        class="fs-layer-chip"
                        :class="{ 'is-active': spaceMode === 'disc' }"
                        @click="setSpaceMode('disc')"
                    >
                        {{ isEn ? 'Disc 3D' : '展开星盘' }}
                    </button>
                </div>

                <span class="fs-divider" />

                <!-- 三层与角色快速过滤胶囊 -->
                <div class="fs-quick-layers">
                    <button
                        type="button"
                        class="fs-layer-chip"
                        :class="{ 'is-active': roleFilter === 'all' }"
                        :disabled="inDrill"
                        @click="roleFilter = 'all'"
                    >
                        {{ isEn ? 'All' : '全部' }}
                    </button>
                    <button
                        type="button"
                        class="fs-layer-chip is-cell"
                        :class="{ 'is-active': roleFilter === 'cell' }"
                        :disabled="inDrill"
                        @click="roleFilter = roleFilter === 'cell' ? 'all' : 'cell'"
                    >
                        {{ isEn ? 'Addr' : '地址' }} {{ roleCounts.cell }}
                    </button>
                    <button
                        type="button"
                        class="fs-layer-chip is-bg"
                        :class="{ 'is-active': roleFilter === 'background' }"
                        :disabled="inDrill"
                        @click="roleFilter = roleFilter === 'background' ? 'all' : 'background'"
                    >
                        L1 {{ roleCounts.background }}
                    </button>
                    <button
                        type="button"
                        class="fs-layer-chip is-ep"
                        :class="{ 'is-active': roleFilter === 'episodic' }"
                        :disabled="inDrill"
                        @click="roleFilter = roleFilter === 'episodic' ? 'all' : 'episodic'"
                    >
                        L2 {{ roleCounts.episodic }}
                    </button>
                    <button
                        type="button"
                        class="fs-layer-chip is-noetic"
                        :class="{ 'is-active': roleFilter === 'noetic' }"
                        :disabled="inDrill"
                        @click="roleFilter = roleFilter === 'noetic' ? 'all' : 'noetic'"
                    >
                        L3 {{ roleCounts.noetic }}
                    </button>
                    <button
                        type="button"
                        class="fs-layer-chip"
                        :class="{ 'is-active': roleFilter === 'atom' }"
                        :disabled="inDrill"
                        @click="roleFilter = roleFilter === 'atom' ? 'all' : 'atom'"
                    >
                        {{ isEn ? 'Atom' : '原子' }} {{ graph?.stats?.atom_total || roleCounts.atom }}
                    </button>
                    <button
                        type="button"
                        class="fs-layer-chip"
                        :class="{ 'is-active': roleFilter === 'molecule' }"
                        :disabled="inDrill"
                        @click="roleFilter = roleFilter === 'molecule' ? 'all' : 'molecule'"
                    >
                        {{ isEn ? 'Mol' : '分子' }} {{ graph?.stats?.molecule_total || roleCounts.molecule }}
                    </button>
                </div>

                <span class="fs-divider" />

                <!-- 原子 / 分子 / 突触计数 -->
                <div class="fs-stat-text fs-particle-stats">
                    <span title="Active Atomic Particles">A: <b>{{ graph?.stats?.atom_total || roleCounts.atom }}</b></span>
                    <span title="Active Molecular Fragments">M: <b>{{ graph?.stats?.molecule_total || roleCounts.molecule }}</b></span>
                    <span title="Active Dimer Synapses">S: <b>{{ graph?.stats?.dimer_synapse_total || activeEdges.length }}</b></span>
                </div>

                <span class="fs-divider" />

                <!-- 原生内联节点容量阶梯槽（128 ~ 8K / ALL） -->
                <div class="fs-capacity-ladder" :title="isEn ? 'Subgraph Node Capacity Ladder' : '节点容量阶梯（全屏可直接点选切换）'">
                    <span class="fs-cap-label">{{ isEn ? 'Cap' : '容量' }}</span>
                    <button
                        v-for="cap in [128, 256, 512, 1024, 2048, 4096, 8192, -1]"
                        :key="cap"
                        type="button"
                        class="fs-ladder-pill"
                        :class="{ 'is-active': nodeLimit === cap }"
                        :disabled="inDrill"
                        @click="selectNodeCapacity(cap)"
                    >
                        {{ cap === -1 ? 'ALL' : cap >= 1024 ? `${cap / 1024}K` : cap }}
                    </button>
                </div>

                <span class="fs-divider" />

                <!-- 极简全息搜索 / 真实 Recall 提问输入 -->
                <div class="fs-search-box">
                    <input
                        v-model="keyword"
                        type="text"
                        class="fs-search-input"
                        :disabled="inDrill"
                        :placeholder="isEn ? 'Filter / Recall LoCoMo…' : '搜索或提问 LoCoMo…'"
                        @keyup.enter="onSearchOrRecall()"
                    />
                    <button v-if="keyword" type="button" class="fs-search-clear" @click="keyword = ''; load()">×</button>
                </div>

                <button
                    type="button"
                    class="fs-action-btn"
                    :class="{ 'is-open': !hubPanelCollapsed || !tracePanelCollapsed }"
                    :title="isEn ? 'Collapse or expand side panels (H)' : '一键收起 / 展开左右侧栏 (快捷键 H)'"
                    @click="toggleSidePanels()"
                >
                    <Icon :name="hubPanelCollapsed && tracePanelCollapsed ? 'lucide-panel-left-open' : 'lucide-panel-left-close'" :size="12" />
                    {{ hubPanelCollapsed && tracePanelCollapsed ? (isEn ? 'Panels' : '展开侧栏') : isEn ? 'Zen' : '收起侧栏' }}
                </button>

                <button type="button" class="fs-action-btn is-zap" :disabled="recalling" @click="triggerLiveLocomoRecall()">
                    ⚡ {{ isEn ? 'Live Recall' : '真实召回放电' }}
                </button>

                <button
                    type="button"
                    class="fs-action-btn"
                    :class="{ 'is-exit': isFullscreen }"
                    @click="toggleTrueFullscreen()"
                >
                    <Icon :name="isFullscreen ? 'lucide-minimize-2' : 'lucide-maximize-2'" :size="12" />
                    {{ isFullscreen ? (isEn ? 'Exit (Esc)' : '退出全屏') : isEn ? 'Fullscreen (F)' : '真全屏 (F)' }}
                </button>
            </div>

            <!-- 下钻出口（面包屑 + 态势卡）：单源组件 @/components/graph/GraphDrillOverlay.vue -->
            <GraphDrillOverlay
                :visible="inDrill"
                :crumbs="drillStack"
                :focus="drillFocus"
                :depth="drillDepth"
                :ego="egoStats"
                :is-en="isEn"
                tone="dark"
                :top-px="58"
                @exit="exitDrill()"
                @pop-to="popDrillTo"
                @set-depth="setDrillDepth"
            />

            <!-- 3D 星云悬停毛玻璃信息浮层 -->
            <div
                v-if="hover3DNode"
                class="nebula-tooltip"
                :style="{ left: `${hover3DPos.x}px`, top: `${hover3DPos.y}px` }"
            >
                <div class="nt-title">{{ hover3DNode.summary }}</div>
                <div class="nt-meta" :style="{ color: nodeDotColor(hover3DNode) }">
                    {{
                        hover3DNode.kind === 'cell'
                            ? isEn
                                ? 'Memory Address Container'
                                : '记忆地址容器'
                            : hover3DNode.layer === 'noetic'
                              ? isEn
                                  ? 'L3 Noetic Ganglion (Q&A)'
                                  : 'L3 知性神经节 (Noetic)'
                              : hover3DNode.layer === 'background'
                                ? isEn
                                    ? 'L1 Background Vesicle'
                                    : 'L1 背景记忆囊泡 (Background)'
                                : isEn
                                  ? 'L2 Episodic Vesicle'
                                  : 'L2 情景记忆囊泡 (Episodic)'
                    }}
                    · {{ isEn ? 'Synapses' : '突触连边' }}={{ hover3DNode.viewDegree }}
                </div>
                <div class="nt-anchor">{{ hover3DNode.anchor || hover3DNode.node_key }}</div>
                <div v-if="hover3DNode.span" class="nt-span">「{{ clip(hover3DNode.span, 115) }}」</div>
                <div class="nt-hint">
                    {{ isEn ? 'Click to inspect · Double-click to drill down' : '单击飞入查看详情 · 双击进入 3D 同心轨道下钻' }}
                </div>
            </div>

            <!-- 左上：核心枢纽导航榜（Locate / Drill 快捷入口） -->
            <div v-if="activeNodes.length" class="graph-hud graph-hud-hubs">
                <div class="hud-panel-head" @click="hubPanelCollapsed = !hubPanelCollapsed">
                    <span class="hud-panel-title">
                        <Icon name="lucide-radar" :size="13" style="margin-right: 5px" />
                        {{ isEn ? 'LoCoMo Hub Navigator' : 'LoCoMo 枢纽导航' }}
                    </span>
                    <span class="hud-panel-badge">{{ hubList.length }}</span>
                    <Icon :name="hubPanelCollapsed ? 'lucide-chevron-down' : 'lucide-chevron-up'" :size="13" />
                </div>
                <template v-if="!hubPanelCollapsed">
                    <el-input v-model="hubFilter" size="small" clearable class="hud-filter">
                        <template #prefix>
                            <Icon name="el-search" :size="12" />
                        </template>
                        <template #placeholder>
                            <span class="hud-filter-ph">{{ isEn ? 'Filter / locate node…' : '过滤或定位节点…' }}</span>
                        </template>
                    </el-input>
                    <div class="hud-hub-list">
                        <div
                            v-for="n in hubList"
                            :key="n.id"
                            class="hud-hub-item"
                            :class="{ 'is-selected': n.id === selectedNodeId, 'is-focus': inDrill && drillFocus?.id === n.id }"
                            @click="locateNode(n)"
                        >
                            <i class="geo-glyph" :class="nodeGlyphClass(n)" />
                            <span class="hud-hub-name" :title="n.summary">{{ clip(n.summary, 20) }}</span>
                            <span class="hud-hub-deg">{{ n.viewDegree }}</span>
                            <span
                                class="hud-hub-act"
                                :title="isEn ? 'Drill down this node as an isolated ego-network' : '以该节点为中心单独下钻为独立视图'"
                                @click.stop="drillInto(n)"
                            >
                                <Icon name="lucide-target" :size="12" />
                            </span>
                        </div>
                    </div>
                </template>
            </div>

            <!-- 右上：LoCoMo 真实接口召回模拟器 & 激活扩散链 -->
            <div v-if="!inDrill" class="graph-hud graph-hud-traces">
                <div class="hud-panel-head" @click="tracePanelCollapsed = !tracePanelCollapsed">
                    <span class="hud-trace-bolt" :class="{ 'is-pulsing': autoPulseEnabled || recalling }">
                        <Icon name="lucide-zap" :size="13" color="#38bdf8" />
                    </span>
                    <span class="hud-panel-title">
                        {{ isEn ? 'Try recall on the sample' : '在示例记忆上试试召回' }}
                    </span>
                    <span
                        class="hud-trace-auto"
                        :class="{ 'is-on': autoPulseEnabled }"
                        @click.stop="toggleAutoPulse"
                    >
                        {{ autoPulseEnabled ? 'AUTO' : 'PAUSED' }}
                    </span>
                    <Icon :name="tracePanelCollapsed ? 'lucide-chevron-down' : 'lucide-chevron-up'" :size="13" />
                </div>
                <template v-if="!tracePanelCollapsed">
                    <div class="hud-trace-sub">
                        <span>{{ isEn ? 'Pick a question to see which memories light up. Runs on a local sample; nothing is sent anywhere.' : '选一个问题，看哪些记忆被点亮。在本地示例数据上运行，不发送任何内容。' }}</span>
                    </div>

                    <!-- 自定义提问实时调接口框 -->
                    <div class="hud-custom-recall">
                        <input
                            v-model="customRecallQuery"
                            type="text"
                            class="hud-recall-input"
                            :placeholder="isEn ? 'Ask the sample a question…' : '向示例记忆提个问题…'"
                            @keyup.enter="runCustomRecall()"
                        />
                        <button type="button" class="hud-trace-fire-btn" :disabled="recalling" @click="runCustomRecall()">
                            {{ recalling ? '…' : isEn ? 'Recall' : '召回' }}
                        </button>
                    </div>

                    <!-- 最近一次真实接口 Recall 回执与命中记忆卡片 -->
                    <div v-if="lastRecallResult" class="hud-recall-receipt">
                        <div class="receipt-head">
                            <span class="receipt-tag">{{ isEn ? 'Local sample' : '本地示例' }}</span>
                            <span class="receipt-stat">{{ lastRecallResult.elapsed_ms }}ms</span>
                        </div>
                        <div class="receipt-q">Q: {{ clip(lastRecallResult.q, 54) }}</div>
                        <div v-if="lastRecallResult.answer" class="receipt-a">A: {{ clip(lastRecallResult.answer, 64) }}</div>
                        <div class="receipt-hits">
                            <div
                                v-for="hit in lastRecallResult.hits.slice(0, 3)"
                                :key="hit.unit_id"
                                class="receipt-hit-chip"
                                @click="focusRecallHit(hit)"
                            >
                                <span class="rh-cell">{{ hit.cell_key }}</span>
                                <span class="rh-txt">{{ clip(hit.raw_text || hit.summary, 38) }}</span>
                            </div>
                        </div>
                    </div>

                    <!-- LoCoMo 真实评测问题列表 -->
                    <div class="hud-hub-list">
                        <div
                            v-for="q in locomoQuestions"
                            :key="q.id"
                            class="hud-hub-item hud-trace-item"
                            :class="{ 'is-selected': activeQuestionId === q.id }"
                            @click="triggerLiveLocomoRecall(q)"
                        >
                            <i class="geo-glyph geo-glyph--noetic" />
                            <div class="hud-trace-body">
                                <div class="hud-trace-name" :title="q.question">{{ clip(q.question, 28) }}</div>
                                <div class="hud-trace-time">
                                    LoCoMo {{ q.id }} · Ans: {{ clip(q.answer, 18) }}
                                </div>
                            </div>
                            <span class="hud-hub-deg">{{ q.cited_keys?.length || 2 }}</span>
                        </div>
                    </div>
                </template>
            </div>

            <!-- 左下：极简首屏品牌宣言与快捷操作角标（不遮挡中央 3D 记忆星云） -->
            <div v-if="!zenMode && !inDrill" class="hero-brand-dock">
                <div class="hb-eyebrow">
                    <span class="hb-dot" />
                    <span>{{ t('index.heroEyebrow') }}</span>
                </div>
                <h1 id="hero-title" class="hb-title">
                    {{ t('index.heroTitleA') }}<span>{{ t('index.heroTitleB') }}</span>
                </h1>
                <!-- 按短语断行：每条事实是一个不可拆分的整体，宁可整条换行也不拆在中间 -->
                <ul class="hb-facts">
                    <li v-for="fact in heroFacts" :key="fact">{{ fact }}</li>
                </ul>
                <div class="hb-actions">
                    <button type="button" class="hb-btn-primary" @click="emit('go-console')">
                        <span>{{ t('index.ctaConsole') }}</span>
                        <Icon name="lucide-arrow-right" :size="13" />
                    </button>
                    <button type="button" class="hb-btn-code" @click="copyInstall">
                        <code>$ {{ installCommand }}</code>
                        <Icon :name="copied ? 'lucide-check' : 'lucide-copy'" :size="12" />
                    </button>
                </div>
            </div>

            <!-- 右下：画布控制罗盘 -->
            <div v-if="activeNodes.length" class="graph-hud graph-hud-ctrl">
                <button
                    type="button"
                    class="hud-btn"
                    :class="{ 'is-on': autoPulseEnabled }"
                    :title="isEn ? 'Fire 2-Hop bio-electric lightning cascade' : '触发 2 跳分形闪电激活扩散'"
                    @click="triggerLiveLocomoRecall()"
                >
                    <Icon name="lucide-zap" :size="14" />
                </button>
                <span class="hud-divider" />
                <button type="button" class="hud-btn" :title="isEn ? 'Zoom in' : '放大'" @click="zoomBy(1.3)">
                    <Icon name="el-zoom-in" :size="14" />
                </button>
                <button type="button" class="hud-btn" :title="isEn ? 'Zoom out' : '缩小'" @click="zoomBy(1 / 1.3)">
                    <Icon name="el-zoom-out" :size="14" />
                </button>
                <button type="button" class="hud-btn" :title="isEn ? 'Fit graph to viewport' : '自适应居中'" @click="resetFit()">
                    <Icon name="lucide-scan" :size="14" />
                </button>
                <button
                    type="button"
                    class="hud-btn hud-btn-fs"
                    :class="{ 'is-on': isFullscreen }"
                    :title="isFullscreen ? 'Exit Fullscreen (Esc)' : 'Fullscreen (F)'"
                    @click="toggleTrueFullscreen()"
                >
                    <Icon :name="isFullscreen ? 'lucide-minimize-2' : 'lucide-maximize-2'" :size="14" />
                </button>
            </div>

            <!-- 底部中央悬浮图例胶囊 -->
            <div v-if="activeNodes.length && !zenMode" class="graph-legend">
                <template v-if="inDrill">
                    <span class="graph-legend-item">
                        <i class="geo-glyph geo-glyph--focus" />
                        {{ isEn ? 'Focus Root' : '下钻焦点' }}
                    </span>
                    <span class="graph-legend-item">
                        <i class="geo-glyph geo-glyph--bg" />
                        {{ isEn ? '1-Hop Neighbor' : '1 跳直接邻居' }}
                    </span>
                    <span class="graph-legend-item">
                        <i class="geo-glyph geo-glyph--cell" />
                        {{ isEn ? '2-Hop Indirect' : '2 跳间接关联' }}
                    </span>
                </template>
                <template v-else>
                    <span
                        class="graph-legend-item graph-legend-clickable"
                        :class="{ 'graph-legend-item--off': roleFilter !== 'all' && roleFilter !== 'cell' }"
                        @click="roleFilter = roleFilter === 'cell' ? 'all' : 'cell'"
                    >
                        <i class="geo-glyph geo-glyph--cell" />
                        {{ isEn ? `Addresses (${roleCounts.cell})` : `记忆地址 (${roleCounts.cell})` }}
                    </span>
                    <span
                        class="graph-legend-item graph-legend-clickable"
                        :class="{ 'graph-legend-item--off': roleFilter !== 'all' && roleFilter !== 'background' }"
                        @click="roleFilter = roleFilter === 'background' ? 'all' : 'background'"
                    >
                        <i class="geo-glyph geo-glyph--bg" />
                        {{ isEn ? `L1 Background (${roleCounts.background})` : `L1 背景 (${roleCounts.background})` }}
                    </span>
                    <span
                        class="graph-legend-item graph-legend-clickable"
                        :class="{ 'graph-legend-item--off': roleFilter !== 'all' && roleFilter !== 'episodic' }"
                        @click="roleFilter = roleFilter === 'episodic' ? 'all' : 'episodic'"
                    >
                        <i class="geo-glyph geo-glyph--ep" />
                        {{ isEn ? `L2 Episodic (${roleCounts.episodic})` : `L2 情景 (${roleCounts.episodic})` }}
                    </span>
                    <span
                        class="graph-legend-item graph-legend-clickable"
                        :class="{ 'graph-legend-item--off': roleFilter !== 'all' && roleFilter !== 'noetic' }"
                        @click="roleFilter = roleFilter === 'noetic' ? 'all' : 'noetic'"
                    >
                        <i class="geo-glyph geo-glyph--noetic" />
                        {{ isEn ? `L3 Noetic (${roleCounts.noetic})` : `L3 知性 (${roleCounts.noetic})` }}
                    </span>
                    <span
                        class="graph-legend-item graph-legend-clickable"
                        :class="{ 'graph-legend-item--off': roleFilter !== 'all' && roleFilter !== 'atom' }"
                        @click="roleFilter = roleFilter === 'atom' ? 'all' : 'atom'"
                    >
                        <i class="geo-glyph geo-glyph--focus" />
                        {{ isEn ? `Atom (${roleCounts.atom})` : `原子荷 (${roleCounts.atom})` }}
                    </span>
                    <span
                        class="graph-legend-item graph-legend-clickable"
                        :class="{ 'graph-legend-item--off': roleFilter !== 'all' && roleFilter !== 'molecule' }"
                        @click="roleFilter = roleFilter === 'molecule' ? 'all' : 'molecule'"
                    >
                        <i class="geo-glyph" style="background: #a855f7" />
                        {{ isEn ? `Molecule (${roleCounts.molecule})` : `分子包 (${roleCounts.molecule})` }}
                    </span>
                </template>
                <span class="graph-sep">|</span>
                <span
                    v-for="r in relationOptions"
                    :key="r.relation"
                    class="graph-legend-item graph-legend-chip"
                    :class="{ 'graph-legend-chip--off': activeRelations.length > 0 && !activeRelations.includes(r.relation) }"
                    @click="toggleRelationChip(r.relation)"
                >
                    <i class="geo-glyph" :style="{ background: relationColor(r.relation) }" />
                    {{ edgeRelationLabel(r.relation) }} ({{ r.count }})
                </span>
            </div>
        </div>

        <!-- 节点详情抽屉（挂载在组件根容器内，真全屏下同样可用） -->
        <el-drawer
            v-model="detailVisible"
            :title="
                currentNode?.kind === 'cell'
                    ? isEn
                        ? 'Memory Address Container Details'
                        : '记忆地址容器详情'
                    : currentNode?.layer === 'noetic'
                      ? isEn
                          ? 'L3 Noetic Insight Engram (Q & A)'
                          : 'L3 知性问答印迹详情 (Q & A)'
                      : isEn
                        ? 'LoCoMo Memory Unit Provenance'
                        : 'LoCoMo 记忆单元明文详情'
            "
            size="520px"
            :append-to-body="false"
        >
            <template v-if="currentNode">
                <div class="graph-drawer-actions">
                    <el-button size="small" type="warning" :loading="drilling" @click="drillFromDrawer()">
                        <Icon name="lucide-target" :size="13" style="margin-right: 4px" />
                        {{ isEn ? `Drill Down as Center (${drillDepth}-hop)` : `设为下钻中心（${drillDepth} 跳同心环）` }}
                    </el-button>
                </div>

                <el-descriptions :column="2" border size="small">
                    <el-descriptions-item :span="2" :label="isEn ? 'Node Key' : '节点标识'">
                        <code>{{ currentNode.node_key }}</code>
                    </el-descriptions-item>
                    <el-descriptions-item :label="isEn ? 'Role' : '单位角色'">
                        <el-tag size="small" :type="currentNode.kind === 'cell' ? 'primary' : 'success'" effect="plain">
                            {{ currentNode.kind === 'cell' ? 'Memory Address Container' : 'Memory Unit' }}
                        </el-tag>
                    </el-descriptions-item>
                    <el-descriptions-item :label="isEn ? 'Layer' : '所属认知层'">
                        <el-tag
                            size="small"
                            :type="currentNode.layer === 'noetic' ? 'danger' : currentNode.layer === 'background' ? 'primary' : 'success'"
                            effect="plain"
                        >
                            {{
                                currentNode.layer === 'noetic'
                                    ? 'L3 Noetic'
                                    : currentNode.layer === 'background'
                                      ? 'L1 Background'
                                      : 'L2 Episodic'
                            }}
                        </el-tag>
                    </el-descriptions-item>
                    <el-descriptions-item :span="2" :label="isEn ? 'C6 Address / Anchor' : 'C6 寻址坐标 / 句柄'">
                        <code>{{ currentNode.anchor || currentNode.node_key }}</code>
                    </el-descriptions-item>
                    <el-descriptions-item :span="2" :label="isEn ? 'Recorded At' : '记录时间'">
                        {{ currentNode.observed_at || '-' }}
                    </el-descriptions-item>
                </el-descriptions>

                <template v-if="currentNode.layer === 'noetic' && currentNode.kind !== 'cell'">
                    <div class="graph-sub-title">{{ isEn ? 'Question (Q · Retrieval Cue)' : '提问线索 (Question · Q)' }}</div>
                    <div class="graph-node-summary">{{ parseNoeticQA(currentNode).q }}</div>
                    <div class="graph-sub-title">{{ isEn ? 'Answer / Cited Evidence (A)' : '知性结晶答案 (Answer · A)' }}</div>
                    <div class="graph-node-span">{{ parseNoeticQA(currentNode).a }}</div>
                </template>
                <template v-else-if="currentNode.kind === 'cell'">
                    <div class="graph-sub-title">{{ isEn ? 'Memory Address Cluster Summary' : '记忆地址聚合主题' }}</div>
                    <div class="graph-node-summary">{{ currentNode.summary }}</div>
                    <div class="graph-sub-title">
                        {{ isEn ? 'Stored Memory Units Under This Address' : '该记忆地址下收纳的记忆单元' }}
                    </div>
                    <div class="cell-member-list">
                        <div
                            v-for="m in cellMemberNodes"
                            :key="m.id"
                            class="cell-member-card"
                            @click="openDetail(m)"
                        >
                            <span class="cell-member-layer" :style="{ background: nodeDotColor(m) }">
                                {{ m.layer === 'noetic' ? 'NT' : m.layer === 'background' ? 'BG' : 'EP' }}
                            </span>
                            <span class="cell-member-text">{{ clip(m.summary, 58) }}</span>
                        </div>
                    </div>
                </template>
                <template v-else>
                    <div class="graph-sub-title">{{ isEn ? 'Vault Plaintext Content (eval-locomo.json)' : '本机明文金库还原内容 (eval-locomo.json)' }}</div>
                    <div class="graph-node-span">「{{ (currentNode.span || '').trim() || currentNode.summary }}」</div>
                </template>
            </template>
        </el-drawer>
    </div>
</template>

<script setup lang="ts">
import { useConfig } from '@/stores/config'
import request from '@/utils/request'
import { Local } from '@/utils/storage'
import { useEventListener } from '@vueuse/core'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Hypergraph3DEngine, type Graph3DNode, type Graph3DSpaceMode } from '@/components/graph/engine'
import GraphDrillOverlay from '@/components/graph/GraphDrillOverlay.vue'
import { computeEgoLayout, computeLayout, type LayoutEdge, type LayoutNode, type LayoutResult } from '@/components/graph/layout'
import LayoutWorker from '@/components/graph/layout.worker?worker'

const emit = defineEmits<{
    (e: 'go-console'): void
    (e: 'scroll-explore'): void
}>()

function scrollToPulseSection() {
    document.getElementById('pulse-preview-section')?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

const { t } = useI18n()
const config = useConfig()
const isEn = computed(() => config.lang.active === 'en')
const isDark = computed(() => config.layout.dark)
// 首屏事实条按「·」拆成独立短语，每条整体换行，避免在短语中间断开
const heroFacts = computed(() =>
    t('index.heroFacts')
        .split('·')
        .map((s) => s.trim())
        .filter(Boolean)
)

const installCommand = 'rolling-memory install --with-hooks'
const copied = ref(false)
let copyTimer: ReturnType<typeof setTimeout> | null = null

async function copyInstall() {
    try {
        await navigator.clipboard.writeText(installCommand)
    } catch {
        const textarea = document.createElement('textarea')
        textarea.value = installCommand
        textarea.style.position = 'fixed'
        textarea.style.opacity = '0'
        document.body.appendChild(textarea)
        textarea.select()
        document.execCommand('copy')
        textarea.remove()
    }
    copied.value = true
    if (copyTimer) clearTimeout(copyTimer)
    copyTimer = setTimeout(() => {
        copied.value = false
    }, 1800)
}

interface GraphNode {
    id: number
    node_key: string
    summary: string
    kind: string
    layer?: string
    weight?: number
    certainty: string
    intention: string
    status: string
    confidence: number
    observed_at: string
    valid_from: string
    valid_to: string
    anchor: string
    span: string
    aliases: string[]
    degree: number
}

interface GraphEdge {
    id: number
    assertion_id: string
    from: number
    to: number
    from_key: string
    to_key: string
    from_label: string
    to_label: string
    relation: string
    layer?: string
    weight?: number
    warrant: string
    certainty: string
    confidence: number
    status: string
    observed_at: string
    valid_from: string
    anchor: string
    span: string
}

interface LocomoQuestion {
    id: string
    sample_id: string
    category: string
    question: string
    answer: string
    noetic_key: string
    noetic_id: number
    cited_keys: string[]
    cited_ids: number[]
}

interface LocomoRecallHit {
    unit_id: string
    node_key: string
    node_id: number
    cell_key: string
    layer: string
    summary: string
    raw_text: string
    address_uri: string
    observed_at: string
    score: number
}

interface LocomoRecallResponse {
    q: string
    question_id: string
    answer: string
    scope: string
    transport: string
    elapsed_ms: number
    pulse_bytes: number
    seed_node_id: number
    seed_node_key: string
    hit_node_ids: number[]
    hits: LocomoRecallHit[]
}

interface GraphPayload {
    source: string
    scope_key: string
    nodes: GraphNode[]
    edges: GraphEdge[]
    relations: { relation: string; count: number }[]
    questions?: LocomoQuestion[]
    corpus?: {
        suite: string
        scope: string
        total_units: number
        total_cells: number
        total_noetic: number
        transport: string
    }
    stats: {
        event_total: number
        link_total: number
        node_shown: number
        edge_shown: number
        isolated_shown: number
        layer_node_counts?: Record<string, number>
        atom_total?: number
        molecule_total?: number
        dimer_synapse_total?: number
    }
}

type ViewNode = GraphNode & { viewDegree: number }
type ViewEdge = GraphEdge

interface DrillCrumb {
    key: string
    summary: string
    id: number
}

const ECHARTS_PALETTE = ['#409eff', '#67c23a', '#e6a23c', '#f56c6c', '#a855f7', '#06b6d4', '#ec4899', '#10b981']

const loading = ref(false)
const recalling = ref(false)
const keyword = ref('')
const customRecallQuery = ref('')
const roleFilter = ref<'all' | 'cell' | 'background' | 'episodic' | 'noetic' | 'atom' | 'molecule'>('all')
const keepOneHopContext = ref(true)
const activeRelations = ref<string[]>([])
const showEdgeLabels = ref(false)
const nodeLimit = ref(256)
const graph = ref<GraphPayload | null>(null)
const locomoQuestions = ref<LocomoQuestion[]>([])
const activeQuestionId = ref('')
const lastRecallResult = ref<LocomoRecallResponse | null>(null)
let questionCursor = 0

const corpusUnits = computed(() => graph.value?.corpus?.total_units || 5882)
const totalNodeCount = computed(() => graph.value?.stats?.event_total || graph.value?.nodes?.length || 0)

const roleCounts = computed(() => {
    const out = { cell: 0, background: 0, episodic: 0, noetic: 0, atom: 0, molecule: 0 }
    for (const n of graph.value?.nodes ?? []) {
        if (n.kind === 'cell') out.cell++
        else if (n.kind === 'atom') out.atom++
        else if (n.kind === 'molecule') out.molecule++
        else if (n.layer === 'noetic') out.noetic++
        else if (n.layer === 'background') out.background++
        else out.episodic++
    }
    return out
})

function edgeRelationLabel(rel: string): string {
    if (rel === 'member_of') return isEn.value ? 'member_of (Address)' : 'member_of (归属地址)'
    if (rel === 'cites') return isEn.value ? 'cites (Wormhole)' : 'cites (引证虫洞)'
    if (rel === 'weak_edge') return isEn.value ? 'weak_edge (Resonance)' : 'weak_edge (跨地址共振)'
    if (rel === 'atom_of') return isEn.value ? 'atom_of (Atom Charge)' : 'atom_of (原子荷投影)'
    if (rel === 'molecule_of') return isEn.value ? 'molecule_of (Molecule)' : 'molecule_of (分子波包)'
    return rel
}

function parseNoeticQA(node: GraphNode): { q: string; a: string } {
    const raw = (node.span || '').trim()
    const idx = raw.indexOf('\nA:')
    if (idx >= 0) {
        const qPart = raw.slice(0, idx).replace(/^Q:\s*/, '').trim()
        const aPart = raw.slice(idx + 3).trim()
        return { q: qPart || node.summary, a: aPart || '-' }
    }
    return { q: node.summary.trim(), a: raw || '-' }
}

function nodeGlyphClass(n: { kind: string; layer?: string }): string {
    if (n.kind === 'cell') return 'geo-glyph--cell'
    if (n.kind === 'atom') return 'geo-glyph--focus'
    if (n.kind === 'molecule') return 'geo-glyph--noetic'
    if (n.layer === 'noetic') return 'geo-glyph--noetic'
    if (n.layer === 'background') return 'geo-glyph--bg'
    return 'geo-glyph--ep'
}

const drillStack = ref<DrillCrumb[]>([])
const drillFocus = ref<ViewNode | null>(null)
const drillDepth = ref<1 | 2 | 3>(2)
const drillPayload = ref<{ nodes: GraphNode[]; edges: GraphEdge[] } | null>(null)
const drilling = ref(false)
const inDrill = computed(() => drillStack.value.length > 0 && !!drillPayload.value)
const nodeByKey = new Map<string, GraphNode>()

const hubPanelCollapsed = ref(false)
// 手机屏上召回面板默认收起，先让人看到星图；点标题即可展开
const tracePanelCollapsed = ref(typeof window !== 'undefined' && window.matchMedia('(max-width: 640px)').matches)
const hubFilter = ref('')
const selectedNodeId = ref<number | null>(null)
let selectTimer: ReturnType<typeof setTimeout> | null = null

const relationOptions = computed(() => graph.value?.relations ?? [])
const relationColorMap = computed(() => {
    const map: Record<string, string> = {
        member_of: '#06b6d4',
        cites: '#ec4899',
        weak_edge: '#f59e0b',
    }
    relationOptions.value.forEach((r, i) => {
        if (!map[r.relation]) {
            map[r.relation] = ECHARTS_PALETTE[i % ECHARTS_PALETTE.length]!
        }
    })
    return map
})

function relationColor(relation: string): string {
    return relationColorMap.value[relation] || '#38bdf8'
}

function nodeDotColor(n: { kind: string; layer?: string }): string {
    if (n.kind === 'cell') return '#0891b2'
    if (n.kind === 'atom') return '#f59e0b'
    if (n.kind === 'molecule') return '#a855f7'
    if (n.layer === 'noetic') return '#ec4899'
    if (n.layer === 'background') return '#3b82f6'
    return '#10b981'
}

function toggleRelationChip(rel: string) {
    const idx = activeRelations.value.indexOf(rel)
    if (idx >= 0) activeRelations.value.splice(idx, 1)
    else activeRelations.value.push(rel)
}

function applyFilters(
    rawNodes: GraphNode[],
    rawEdges: GraphEdge[],
    opts: { isDrill?: boolean }
): { nodes: ViewNode[]; edges: GraphEdge[] } {
    const relSet = activeRelations.value.length ? new Set(activeRelations.value) : null
    const candidateEdges = rawEdges.filter((e) => !relSet || relSet.has(e.relation))

    if (opts.isDrill) {
        const deg = new Map<number, number>()
        for (const e of candidateEdges) {
            deg.set(e.from, (deg.get(e.from) ?? 0) + 1)
            deg.set(e.to, (deg.get(e.to) ?? 0) + 1)
        }
        return {
            nodes: rawNodes.map((n) => ({ ...n, viewDegree: deg.get(n.id) ?? 0 })),
            edges: candidateEdges,
        }
    }

    const rf = roleFilter.value
    const kw = keyword.value.trim().toLowerCase()
    const hasNodeFilter = rf !== 'all' || kw !== ''

    const seedMatched = new Set<number>()
    for (const n of rawNodes) {
        if (rf !== 'all') {
            if (rf === 'cell' && n.kind !== 'cell') continue
            if (rf === 'atom' && n.kind !== 'atom') continue
            if (rf === 'molecule' && n.kind !== 'molecule') continue
            if (
                rf !== 'cell' &&
                rf !== 'atom' &&
                rf !== 'molecule' &&
                (n.kind !== 'unit' || (n.layer || 'episodic') !== rf)
            )
                continue
        }
        if (kw !== '') {
            const hay = `${n.node_key} ${n.summary} ${n.span} ${n.anchor}`.toLowerCase()
            if (!hay.includes(kw)) continue
        }
        seedMatched.add(n.id)
    }

    const keptIds = new Set<number>(seedMatched)
    if (hasNodeFilter && keepOneHopContext.value && seedMatched.size > 0) {
        for (const e of candidateEdges) {
            if (seedMatched.has(e.from)) keptIds.add(e.to)
            if (seedMatched.has(e.to)) keptIds.add(e.from)
        }
    }

    const visibleEdges = candidateEdges.filter((e) => keptIds.has(e.from) && keptIds.has(e.to))
    const deg = new Map<number, number>()
    for (const e of visibleEdges) {
        deg.set(e.from, (deg.get(e.from) ?? 0) + 1)
        deg.set(e.to, (deg.get(e.to) ?? 0) + 1)
    }

    const nodes: ViewNode[] = []
    for (const n of rawNodes) {
        if (!keptIds.has(n.id)) continue
        const viewDegree = deg.get(n.id) ?? 0
        if (relSet && viewDegree === 0 && !hasNodeFilter) continue
        nodes.push({ ...n, viewDegree })
    }
    return { nodes, edges: visibleEdges }
}

const view = computed<{ nodes: ViewNode[]; edges: ViewEdge[] }>(() => {
    const g = graph.value
    if (!g) return { nodes: [], edges: [] }
    return applyFilters(g.nodes, g.edges, { isDrill: false })
})

const drillView = computed<{ nodes: ViewNode[]; edges: ViewEdge[] }>(() => {
    const p = drillPayload.value
    if (!p) return { nodes: [], edges: [] }
    return applyFilters(p.nodes, p.edges, { isDrill: true })
})

const activeView = computed(() => (inDrill.value ? drillView.value : view.value))
const activeNodes = computed(() => activeView.value.nodes)
const activeEdges = computed(() => activeView.value.edges)

const egoResult = computed<LayoutResult | null>(() => {
    const v = drillView.value
    const focus = drillFocus.value
    if (!focus || !v.nodes.length) return null
    const nodes: LayoutNode[] = v.nodes.map((n) => ({ id: n.id, degree: n.viewDegree }))
    const edges: LayoutEdge[] = v.edges.map((e) => ({ from: e.from, to: e.to, relation: e.relation }))
    return computeEgoLayout(nodes, edges, focus.id)
})

const egoStats = computed(() => {
    const ego = egoResult.value
    const focus = drillFocus.value
    if (!ego || !focus) return null
    let hop1 = 0
    let hop2 = 0
    let hop3 = 0
    for (const n of drillView.value.nodes) {
        const h = ego.hopMap?.[n.id] ?? 3
        if (n.id === focus.id) continue
        if (h === 1) hop1++
        else if (h === 2) hop2++
        else hop3++
    }
    let out = 0
    let inn = 0
    for (const e of drillView.value.edges) {
        if (e.from === focus.id) out++
        if (e.to === focus.id) inn++
    }
    return { hop1, hop2, hop3, out, in: inn }
})

const hubList = computed(() => {
    const pool = activeNodes.value
    const q = hubFilter.value.trim().toLowerCase()
    const filtered = q ? pool.filter((n) => n.summary.toLowerCase().includes(q) || n.node_key.toLowerCase().includes(q)) : pool
    return [...filtered].sort((a, b) => b.viewDegree - a.viewDegree || a.id - b.id).slice(0, 12)
})

const autoPulseEnabled = ref(true)
const SPACE_MODE_KEY = 'rm-hero-graph-space-mode'
const spaceMode = ref<Graph3DSpaceMode>(Local.get(SPACE_MODE_KEY) === 'disc' ? 'disc' : 'sphere')
const zenMode = computed(() => hubPanelCollapsed.value && tracePanelCollapsed.value)

function setSpaceMode(mode: Graph3DSpaceMode): void {
    if (spaceMode.value === mode) return
    spaceMode.value = mode
    Local.set(SPACE_MODE_KEY, mode)
    void nextTick(() => renderChart(true))
}

function toggleSpaceMode(): void {
    setSpaceMode(spaceMode.value === 'sphere' ? 'disc' : 'sphere')
}

function toggleSidePanels(): void {
    const collapseBoth = !hubPanelCollapsed.value || !tracePanelCollapsed.value
    hubPanelCollapsed.value = collapseBoth
    tracePanelCollapsed.value = collapseBoth
}

function selectNodeCapacity(cap: number): void {
    if (nodeLimit.value === cap && !loading.value) return
    nodeLimit.value = cap
    void load()
}

const graphPageRef = ref<HTMLElement | null>(null)
const isFullscreen = ref(false)
const fsDrawerOpen = ref(false)
const threeContainerRef = ref<HTMLElement | null>(null)
const hover3DNode = ref<Graph3DNode | null>(null)
const hover3DPos = ref({ x: 0, y: 0 })
let threeEngine: Hypergraph3DEngine | null = null
const viewSize = ref({ w: 0, h: 0 })
let sizeObserver: ResizeObserver | null = null
let sizeTimer: ReturnType<typeof setTimeout> | null = null

function watchViewport(el: HTMLElement): void {
    if (typeof ResizeObserver === 'undefined') return
    sizeObserver = new ResizeObserver(() => {
        if (sizeTimer) clearTimeout(sizeTimer)
        sizeTimer = setTimeout(() => {
            const el3 = threeContainerRef.value
            const w = Math.max(el.clientWidth, el3?.clientWidth ?? 0)
            const h = Math.max(el.clientHeight, el3?.clientHeight ?? 0)
            if (w <= 0 || h <= 0) return
            if (Math.abs(w - viewSize.value.w) <= 24 && Math.abs(h - viewSize.value.h) <= 24) return
            viewSize.value = { w, h }
            threeEngine?.resize(w, h)
        }, 160)
    })
    sizeObserver.observe(el)
}

// 侧栏让位：按顶栏与左下品牌卡的真实尺寸（会随宽度与语言变化）给两侧面板留位，代替写死的 top / max-height，避免浮层互相压住
const graphHudTopPx = ref(58)
const hudBottomLeftPx = ref(64)
let topbarObserver: ResizeObserver | null = null

function watchCockpitTopbar(): void {
    const stage = graphPageRef.value
    const bar = stage?.querySelector<HTMLElement>('.fs-cockpit-topbar')
    if (!stage || !bar || typeof ResizeObserver === 'undefined') return
    const measure = () => {
        if (!bar.classList.contains('is-drill')) {
            graphHudTopPx.value = Math.round(bar.offsetTop + bar.offsetHeight + 10)
        }
        const dock = stage.querySelector<HTMLElement>('.hero-brand-dock')
        const dockVisible = dock && dock.offsetParent !== null
        hudBottomLeftPx.value = dockVisible ? Math.round(stage.clientHeight - dock.offsetTop + 10) : 64
    }
    topbarObserver = new ResizeObserver(measure)
    topbarObserver.observe(bar)
    topbarObserver.observe(stage)
    const dock = stage.querySelector<HTMLElement>('.hero-brand-dock')
    if (dock) topbarObserver.observe(dock)
}

function ensureThreeEngine(): Hypergraph3DEngine | null {
    const el = threeContainerRef.value
    if (!el) return null
    if (!threeEngine) {
        threeEngine = new Hypergraph3DEngine(el, {
            enableZoom: false, // 禁用默认缩放交互
            onNodeHover(node: Graph3DNode | null, clientX: number, clientY: number) {
                if (!node) {
                    hover3DNode.value = null
                    return
                }
                hover3DNode.value = node
                const rect = el.getBoundingClientRect()
                hover3DPos.value = {
                    x: Math.max(8, Math.min(rect.width - 330, clientX - rect.left + 18)),
                    y: Math.max(8, Math.min(rect.height - 140, clientY - rect.top - 14)),
                }
            },
            onNodeClick(node: Graph3DNode) {
                const live = activeView.value.nodes.find((n) => n.id === node.id) ?? (node as unknown as ViewNode)
                selectedNodeId.value = node.id
                if (selectTimer) clearTimeout(selectTimer)
                selectTimer = setTimeout(() => {
                    selectedNodeId.value = null
                    selectTimer = null
                    threeEngine?.setSelectedNode(null)
                }, 2600)
                openDetail(live)
            },
            onNodeDblClick(node: Graph3DNode) {
                const live = activeView.value.nodes.find((n) => n.id === node.id) ?? (node as unknown as ViewNode)
                void drillInto(live)
            },
        })
    }
    return threeEngine
}

function renderChart(forceFit = false): void {
    const ctrl = ensureThreeEngine()
    if (!ctrl) return
    const el3 = threeContainerRef.value
    if (el3 && el3.clientWidth > 0 && el3.clientHeight > 0) {
        ctrl.resize(el3.clientWidth, el3.clientHeight)
    }
    const mode = inDrill.value
    const v = activeView.value
    const ego = mode ? egoResult.value : null
    const coords: Record<number, [number, number]> = mode ? (ego?.coords ?? {}) : layout.value
    if (!mode && Object.keys(coords).length === 0) return
    ctrl.setData(
        {
            nodes: v.nodes,
            edges: v.edges,
            coords2D: coords,
            spaceMode: spaceMode.value,
            inDrill: mode,
            focusId: mode ? (drillFocus.value?.id ?? null) : null,
            hopMap: ego?.hopMap ?? {},
            selectedNodeId: selectedNodeId.value,
            showEdgeLabels: showEdgeLabels.value,
            isDark: true,
        },
        forceFit
    )
}

const layoutInput = computed<{ nodes: GraphNode[]; edges: GraphEdge[] } | null>(() => {
    const g = graph.value
    if (!g) return null
    return { nodes: g.nodes, edges: g.edges }
})

let prevCoords: Record<number, [number, number]> = {}
const layout = ref<Record<number, [number, number]>>({})
const layoutPending = ref(false)
let layoutWorker: Worker | null = null
let layoutWorkerBroken = false
let layoutSeq = 0
let layoutTimer: ReturnType<typeof setTimeout> | null = null

function ensureLayoutWorker(): Worker | null {
    if (layoutWorker || layoutWorkerBroken) return layoutWorker
    try {
        layoutWorker = new LayoutWorker()
        layoutWorker.onmessage = (e: MessageEvent<{ seq: number } & LayoutResult>) => {
            const { seq, coords } = e.data
            if (layoutTimer) {
                clearTimeout(layoutTimer)
                layoutTimer = null
            }
            if (seq !== layoutSeq) return
            prevCoords = coords
            layout.value = coords
            layoutPending.value = false
        }
        layoutWorker.onerror = () => {
            layoutWorkerBroken = true
            layoutWorker = null
            layoutPending.value = false
        }
    } catch {
        layoutWorkerBroken = true
        layoutWorker = null
    }
    return layoutWorker
}

function runLayoutSync(payload: { nodes: LayoutNode[]; edges: LayoutEdge[] }, aspect: number): void {
    const res = computeLayout(payload.nodes, payload.edges, aspect, prevCoords)
    prevCoords = res.coords
    layout.value = res.coords
    layoutPending.value = false
}

watch(
    [layoutInput, viewSize],
    ([inp, vs]) => {
        if (inDrill.value) return
        if (!inp || inp.nodes.length === 0) {
            layout.value = {}
            prevCoords = {}
            layoutPending.value = false
            return
        }
        const size = vs as { w: number; h: number }
        const aspect = size.w > 0 && size.h > 0 ? Math.min(2, Math.max(0.5, size.w / size.h)) : 1.4
        const payload = {
            nodes: inp.nodes.map((n) => ({ id: n.id, degree: n.degree })),
            edges: inp.edges.map((e) => ({ from: e.from, to: e.to })),
        }
        if (payload.nodes.length <= 350 || payload.nodes.length > 600) {
            runLayoutSync(payload, aspect)
            return
        }
        const worker = ensureLayoutWorker()
        if (worker) {
            layoutPending.value = true
            layoutSeq++
            worker.postMessage({ seq: layoutSeq, ...payload, aspect, prev: prevCoords })
            if (layoutTimer) clearTimeout(layoutTimer)
            layoutTimer = setTimeout(() => {
                layoutTimer = null
                if (!layoutPending.value) return
                layoutWorkerBroken = true
                layoutWorker?.terminate()
                layoutWorker = null
                runLayoutSync(payload, aspect)
            }, 2500)
            return
        }
        runLayoutSync(payload, aspect)
    },
    { immediate: true }
)

watch(
    [activeView, egoResult, layout, relationColorMap, isDark, () => config.lang.active, showEdgeLabels, selectedNodeId],
    () => {
        nextTick(() => renderChart())
    },
    { deep: false }
)

function zoomBy(factor: number): void {
    threeEngine?.zoomBy(factor)
}

function resetFit(): void {
    threeEngine?.fitToViewport(true)
}

function syncEngineResize(): void {
    void nextTick(() => {
        const el = threeContainerRef.value
        if (el && el.clientWidth > 0 && el.clientHeight > 0) {
            viewSize.value = { w: el.clientWidth, h: el.clientHeight }
            threeEngine?.resize(el.clientWidth, el.clientHeight)
        }
    })
}

async function toggleTrueFullscreen(): Promise<void> {
    const host = graphPageRef.value
    if (!host) return
    try {
        if (document.fullscreenElement) {
            await document.exitFullscreen()
            isFullscreen.value = false
        } else if (typeof host.requestFullscreen === 'function') {
            await host.requestFullscreen()
            isFullscreen.value = true
        } else {
            isFullscreen.value = !isFullscreen.value
        }
    } catch {
        isFullscreen.value = !isFullscreen.value
    }
    if (isFullscreen.value) {
        hubPanelCollapsed.value = true
        tracePanelCollapsed.value = true
    } else {
        fsDrawerOpen.value = false
        hubPanelCollapsed.value = false
        tracePanelCollapsed.value = false
    }
    syncEngineResize()
    setTimeout(syncEngineResize, 120)
}

useEventListener(document, 'fullscreenchange', () => {
    const nextFs = Boolean(document.fullscreenElement)
    isFullscreen.value = nextFs
    if (nextFs) {
        hubPanelCollapsed.value = true
        tracePanelCollapsed.value = true
    } else {
        fsDrawerOpen.value = false
        hubPanelCollapsed.value = false
        tracePanelCollapsed.value = false
    }
    syncEngineResize()
})

// 星舰驾驶舱键位（与后台图谱页同源）：Esc 依次退出「详情抽屉 → 下钻 → 真全屏」，
// F / H / V 分别切换真全屏 / 侧栏 / 球形宇宙⇄展开星盘；输入框聚焦时不劫持按键。
useEventListener(window, 'keydown', (e: KeyboardEvent) => {
    const t = e.target as HTMLElement | null
    if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return
    if ((e.key === 'f' || e.key === 'F') && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault()
        void toggleTrueFullscreen()
        return
    }
    if ((e.key === 'h' || e.key === 'H') && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault()
        toggleSidePanels()
        return
    }
    if ((e.key === 'v' || e.key === 'V') && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault()
        toggleSpaceMode()
        return
    }
    if (e.key !== 'Escape') return
    if (detailVisible.value) {
        detailVisible.value = false
        return
    }
    if (inDrill.value) {
        exitDrill()
        return
    }
    if (isFullscreen.value) void toggleTrueFullscreen()
})

function locateNode(node: ViewNode): void {
    selectedNodeId.value = node.id
    if (selectTimer) clearTimeout(selectTimer)
    selectTimer = setTimeout(() => {
        selectedNodeId.value = null
        selectTimer = null
        threeEngine?.setSelectedNode(null)
    }, 2600)
    const ctrl = ensureThreeEngine()
    ctrl?.setSelectedNode(node.id)
    ctrl?.triggerActivationCascade(node.id, true)
}

function toggleAutoPulse(): void {
    autoPulseEnabled.value = !autoPulseEnabled.value
    threeEngine?.setAutoPulse(autoPulseEnabled.value)
}

function onSearchOrRecall(): void {
    if (keyword.value.trim().endsWith('?') || keyword.value.trim().length > 16) {
        customRecallQuery.value = keyword.value.trim()
        void runCustomRecall()
        return
    }
    void load()
}

async function runCustomRecall(): Promise<void> {
    const q = customRecallQuery.value.trim()
    if (!q) {
        await triggerLiveLocomoRecall(undefined, false)
        return
    }
    await executeLocomoRecall({ q }, false)
}

async function triggerLiveLocomoRecall(qItem?: LocomoQuestion, fly = false): Promise<void> {
    const targetQ =
        qItem ||
        (locomoQuestions.value.length > 0
            ? locomoQuestions.value[questionCursor++ % locomoQuestions.value.length]
            : undefined)
    if (targetQ) {
        activeQuestionId.value = targetQ.id
        await executeLocomoRecall({ question_id: targetQ.id, q: targetQ.question }, fly)
    } else {
        const ctrl = ensureThreeEngine()
        ctrl?.triggerActivationCascade(undefined, false)
    }
}

async function executeLocomoRecall(payload: { q?: string; question_id?: string }, fly = false): Promise<void> {
    if (recalling.value) return
    recalling.value = true
    try {
        const res = await request<LocomoRecallResponse>(
            {
                url: '/plugin/rolling-memory/locomo/recall',
                method: 'POST',
                data: {
                    q: payload.q || '',
                    question_id: payload.question_id || '',
                    top_k: 6,
                },
            },
            { showErrorMessage: false, showNetworkErrorMessage: false, cancelDuplicate: false }
        )
        const data = res.data?.data
        if (data) {
            lastRecallResult.value = data
            const ctrl = ensureThreeEngine()
            // 优先在当前可见节点中寻找震源节点（L3 Noetic 或首个命中 Unit）触发 1-Hop → 2-Hop 分叉闪电级联
            const visibleIds = new Set(activeNodes.value.map((n) => n.id))
            let targetSeedId: number | undefined = undefined
            if (data.seed_node_id && visibleIds.has(data.seed_node_id)) {
                targetSeedId = data.seed_node_id
            } else {
                const matchedHit = (data.hit_node_ids ?? []).find((id) => visibleIds.has(id))
                if (matchedHit) targetSeedId = matchedHit
            }
            const firedId = ctrl?.triggerActivationCascade(targetSeedId, fly) ?? null
            if (firedId !== null) {
                selectedNodeId.value = firedId
                ctrl?.setSelectedNode(firedId, fly)
                if (selectTimer) clearTimeout(selectTimer)
                selectTimer = setTimeout(() => {
                    selectedNodeId.value = null
                    selectTimer = null
                    threeEngine?.setSelectedNode(null, false)
                }, 3200)
            }
        }
    } catch {
        const ctrl = ensureThreeEngine()
        ctrl?.triggerActivationCascade(undefined, false)
    } finally {
        recalling.value = false
    }
}

function focusRecallHit(hit: LocomoRecallHit): void {
    const target =
        activeNodes.value.find((n) => n.id === hit.node_id || n.node_key === hit.node_key) ||
        activeNodes.value.find((n) => n.node_key === `cell:${hit.cell_key}`)
    if (target) {
        locateNode(target)
        openDetail(target)
    }
}

let graphFetching = false

async function load(): Promise<void> {
    if (graphFetching) return
    if (inDrill.value) exitDrill()
    graphFetching = true
    loading.value = true
    try {
        const res = await request<GraphPayload>(
            {
                url: '/plugin/rolling-memory/locomo/graph',
                method: 'POST',
                data: {
                    limit: nodeLimit.value,
                    keyword: keyword.value,
                },
            },
            { showErrorMessage: false, showNetworkErrorMessage: false, cancelDuplicate: false }
        )
        const data = res.data?.data
        if (data) {
            graph.value = data
            nodeByKey.clear()
            for (const n of data.nodes ?? []) {
                nodeByKey.set(n.node_key, n)
            }
            if (Array.isArray(data.questions) && data.questions.length > 0) {
                locomoQuestions.value = data.questions
                if (!activeQuestionId.value) {
                    activeQuestionId.value = data.questions[0].id
                }
            }
            await nextTick()
            renderChart(true)
        }
    } finally {
        graphFetching = false
        loading.value = false
    }
}

async function drillInto(node: { node_key: string; summary: string; id?: number }, opts?: { replace?: boolean }): Promise<void> {
    if (drilling.value || !node?.node_key) return
    drilling.value = true
    try {
        const res = await request<GraphPayload>(
            {
                url: '/plugin/rolling-memory/locomo/expand',
                method: 'POST',
                data: {
                    seed_keys: [node.node_key],
                    depth: drillDepth.value,
                    limit: 380,
                },
            },
            { showErrorMessage: false, showNetworkErrorMessage: false, cancelDuplicate: false }
        )
        const data = res.data?.data
        if (!data) return
        const nodes = [...(data.nodes ?? [])]
        const edges = [...(data.edges ?? [])]
        for (const n of nodes) nodeByKey.set(n.node_key, n)

        const focusNode = nodes.find((n) => n.node_key === node.node_key) || nodeByKey.get(node.node_key)
        if (!focusNode) return
        if (!nodes.some((n) => n.id === focusNode.id)) nodes.unshift(focusNode)

        const viewDegree = edges.filter((e) => e.from === focusNode.id || e.to === focusNode.id).length
        const focusView: ViewNode = { ...focusNode, viewDegree }
        drillFocus.value = focusView
        drillPayload.value = { nodes, edges }
        currentNode.value = focusView

        const crumb: DrillCrumb = { key: focusNode.node_key, summary: focusNode.summary, id: focusNode.id }
        if (opts?.replace && drillStack.value.length) {
            drillStack.value = [...drillStack.value.slice(0, -1), crumb]
        } else {
            drillStack.value = [...drillStack.value, crumb]
        }
        detailVisible.value = false
        await nextTick()
        renderChart(true)
    } finally {
        drilling.value = false
    }
}

function setDrillDepth(d: 1 | 2 | 3): void {
    if (drillDepth.value === d) return
    drillDepth.value = d
    if (inDrill.value) {
        const top = drillStack.value[drillStack.value.length - 1]
        if (top) void drillInto({ node_key: top.key, summary: top.summary, id: top.id }, { replace: true })
    }
}

function popDrillTo(index: number): void {
    if (index < 0) {
        exitDrill()
        return
    }
    const target = drillStack.value[index]
    if (!target || index === drillStack.value.length - 1) return
    drillStack.value = drillStack.value.slice(0, index)
    void drillInto({ node_key: target.key, summary: target.summary, id: target.id })
}

function exitDrill(): void {
    if (!inDrill.value && !drillStack.value.length) return
    drillStack.value = []
    drillPayload.value = null
    drillFocus.value = null
    void nextTick(() => renderChart(true))
}

function drillFromDrawer(): void {
    const node = currentNode.value
    if (node) void drillInto(node)
}

const detailVisible = ref(false)
const currentNode = ref<ViewNode | null>(null)

function openDetail(node: ViewNode): void {
    currentNode.value = node
    detailVisible.value = true
}

const cellMemberNodes = computed<ViewNode[]>(() => {
    const node = currentNode.value
    if (!node || node.kind !== 'cell') return []
    const srcNodes = inDrill.value ? (drillPayload.value?.nodes ?? []) : (graph.value?.nodes ?? [])
    const srcEdges = inDrill.value ? (drillPayload.value?.edges ?? []) : (graph.value?.edges ?? [])
    const byId = new Map(srcNodes.map((n) => [n.id, n]))
    for (const n of graph.value?.nodes ?? []) {
        if (!byId.has(n.id)) byId.set(n.id, n)
    }
    const seen = new Set<number>()
    const out: ViewNode[] = []
    for (const e of [...srcEdges, ...(graph.value?.edges ?? [])]) {
        if (e.relation !== 'member_of' || e.to !== node.id || seen.has(e.from)) continue
        const m = byId.get(e.from)
        if (m) {
            seen.add(e.from)
            out.push({ ...m, viewDegree: m.degree || 1 })
        }
    }
    out.sort((a, b) => (b.degree ?? 0) - (a.degree ?? 0))
    return out
})

function clip(s: string, len: number): string {
    const one = (s || '').replace(/\s+/g, ' ').trim()
    return one.length > len ? `${one.slice(0, len)}…` : one
}

useEventListener(window, 'resize', () => {
    const el = threeContainerRef.value
    if (el && el.clientWidth > 0 && el.clientHeight > 0) threeEngine?.resize(el.clientWidth, el.clientHeight)
})

let bootRecallTimer: ReturnType<typeof setTimeout> | null = null

onMounted(async () => {
    const el = threeContainerRef.value
    if (el) {
        viewSize.value = { w: el.clientWidth, h: el.clientHeight }
        watchViewport(el)
    }
    watchCockpitTopbar()
    await load()
    // 入场后自动触发第一次真实 LoCoMo 接口召回与 2 跳分形闪电扩散
    bootRecallTimer = setTimeout(() => {
        void triggerLiveLocomoRecall()
    }, 900)
})

onBeforeUnmount(() => {
    if (bootRecallTimer) clearTimeout(bootRecallTimer)
    if (copyTimer) clearTimeout(copyTimer)
    sizeObserver?.disconnect()
    sizeObserver = null
    topbarObserver?.disconnect()
    topbarObserver = null
    if (sizeTimer) clearTimeout(sizeTimer)
    if (layoutTimer) clearTimeout(layoutTimer)
    if (selectTimer) clearTimeout(selectTimer)
    layoutWorker?.terminate()
    layoutWorker = null
    threeEngine?.dispose()
    threeEngine = null
})
</script>

<style scoped lang="scss">
.hero-graph-stage {
    position: relative;
    width: 100vw;
    height: calc(100dvh - 82px);
    min-height: 680px;
    margin-left: calc(50% - 50vw);
    padding: 0 !important;
    overflow: hidden;
    background: #000103;
    border-bottom: 1px solid rgba(56, 189, 248, 0.22);

    .graph-body {
        position: relative;
        width: 100%;
        height: 100%;
        min-height: 680px;
    }

    .graph-chart,
    .graph-chart-3d {
        position: relative;
        width: 100% !important;
        height: 100% !important;
        min-height: 680px !important;
        overflow: hidden;
        cursor: grab;
        background: radial-gradient(ellipse at 50% 42%, #050c1a 0%, #02050c 50%, #000103 100%);

        &:active {
            cursor: grabbing;
        }

        &::after {
            content: '';
            position: absolute;
            inset: 0;
            pointer-events: none;
            z-index: 2;
            background: radial-gradient(
                ellipse at 50% 48%,
                rgba(0, 0, 0, 0) 54%,
                rgba(1, 4, 12, 0.42) 82%,
                rgba(0, 1, 5, 0.78) 100%
            );
        }

        :deep(canvas) {
            display: block;
        }
    }

    &.is-true-fullscreen,
    &:fullscreen {
        position: fixed !important;
        inset: 0 !important;
        z-index: 2600 !important;
        width: 100vw !important;
        height: 100vh !important;
        margin: 0 !important;
    }

    .geo-glyph {
        display: inline-block;
        width: 9px;
        height: 9px;
        margin-right: 5px;
        vertical-align: -1px;
        flex-shrink: 0;
        border-radius: 50%;

        &--cell {
            width: 11px;
            height: 11px;
            background: radial-gradient(
                circle,
                rgba(34, 211, 238, 0.95) 0 22%,
                rgba(8, 145, 178, 0.32) 45%,
                rgba(8, 145, 178, 0.1) 70%,
                rgba(34, 211, 238, 0.55) 100%
            );
            box-shadow: 0 0 6px rgba(34, 211, 238, 0.45);
        }

        &--bg {
            background: #3b82f6;
        }

        &--ep {
            background: #10b981;
        }

        &--noetic {
            width: 10px;
            height: 10px;
            background: #ec4899;
            box-shadow: 0 0 0 2px rgba(236, 72, 153, 0.28);
        }

        &--focus {
            width: 11px;
            height: 11px;
            background: #f59e0b;
            box-shadow: 0 0 0 2.5px rgba(245, 158, 11, 0.38);
        }
    }

    // 下钻态：星舰主指挥舱的控件全部为 :disabled（死控件），整体淡出让位给面包屑与下钻态势卡，
    // 否则其 16 号 z-index 会压住面包屑（实测 1280×720 下顶栏换行到 90px 高、x 320~960，正好盖住面包屑）。
    .fs-cockpit-topbar.is-drill {
        opacity: 0;
        pointer-events: none;
    }

    .fs-cockpit-topbar {
        position: absolute;
        top: 12px;
        left: 50%;
        transform: translateX(-50%);
        z-index: 16;
        display: inline-flex;
        align-items: center;
        flex-wrap: wrap;
        justify-content: center;
        max-width: calc(100% - 24px);
        gap: 6px;
        padding: 5px 12px;
        border-radius: 999px;
        border: 1px solid rgba(56, 189, 248, 0.3);
        background: linear-gradient(180deg, rgba(6, 14, 28, 0.88), rgba(3, 8, 18, 0.84));
        box-shadow:
            0 10px 32px rgba(0, 2, 10, 0.75),
            0 0 18px rgba(34, 211, 238, 0.12);
        backdrop-filter: blur(14px);
        color: #e2e8f0;
        font-size: 11.5px;
        white-space: nowrap;

        .fs-preview-badge {
            display: inline-flex;
            align-items: center;
            gap: 5px;
            padding: 2px 8px;
            border-radius: 999px;
            background: rgba(236, 72, 153, 0.15);
            border: 1px solid rgba(236, 72, 153, 0.35);
            color: #f9a8d4;
            font-size: 10.5px;
            font-weight: 700;
            cursor: pointer;
            transition: all 0.2s;
            &:hover { background: rgba(236, 72, 153, 0.25); border-color: #f9a8d4; }
        }
        .fs-badge-dot {
            width: 6px; height: 6px; border-radius: 50%; background: #f9a8d4;
            box-shadow: 0 0 4px #f9a8d4;
        }

        .sse-dot {
            display: inline-block;
            width: 7px;
            height: 7px;
            border-radius: 50%;
            background: #10b981;
            box-shadow: 0 0 6px #10b981;
        }

        .fs-live-pill {
            display: inline-flex;
            align-items: center;
            gap: 5px;
            padding: 2px 8px;
            border-radius: 999px;
            background: rgba(16, 185, 129, 0.14);
            border: 1px solid rgba(16, 185, 129, 0.35);
            color: #6ee7b7;
            font-size: 10.5px;
            font-weight: 700;
            cursor: pointer;
        }

        .fs-stat-text {
            color: #94a3b8;
            font-size: 11px;
            b {
                color: #f1f5f9;
            }
        }

        .fs-particle-stats {
            display: flex;
            gap: 10px;
            font-size: 10.5px;
        }

        .fs-divider {
            width: 1px;
            height: 14px;
            background: rgba(148, 163, 184, 0.25);
        }

        .fs-quick-layers {
            display: inline-flex;
            align-items: center;
            gap: 4px;
        }

        .fs-layer-chip {
            padding: 2px 8px;
            border-radius: 999px;
            border: 1px solid rgba(148, 163, 184, 0.22);
            background: rgba(15, 23, 42, 0.65);
            color: #cbd5e1;
            font-size: 10.5px;
            font-weight: 600;
            cursor: pointer;

            &.is-active {
                background: rgba(56, 189, 248, 0.22);
                border-color: #38bdf8;
                color: #ffffff;
            }
        }

        .fs-capacity-ladder {
            display: inline-flex;
            align-items: center;
            gap: 2px;
            padding: 2px;
            border-radius: 999px;
            background: rgba(2, 6, 16, 0.72);
            border: 1px solid rgba(56, 189, 248, 0.2);
        }

        .fs-cap-label {
            padding: 0 5px 0 6px;
            font-size: 10px;
            font-weight: 700;
            color: #64748b;
        }

        .fs-ladder-pill {
            padding: 2px 6px;
            border-radius: 999px;
            border: none;
            background: transparent;
            color: #94a3b8;
            font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
            font-size: 10px;
            font-weight: 700;
            cursor: pointer;

            &.is-active {
                color: #020617;
                background: linear-gradient(135deg, #38bdf8, #22d3ee);
            }
        }

        .fs-search-box {
            position: relative;
            display: inline-flex;
            align-items: center;
        }

        .fs-search-input {
            width: 132px;
            height: 22px;
            padding: 0 20px 0 9px;
            border-radius: 999px;
            border: 1px solid rgba(148, 163, 184, 0.26);
            background: rgba(2, 8, 20, 0.72);
            color: #f1f5f9;
            font-size: 11px;
            outline: none;
        }

        .fs-search-clear {
            position: absolute;
            right: 5px;
            border: none;
            background: transparent;
            color: #94a3b8;
            cursor: pointer;
        }

        .fs-action-btn {
            display: inline-flex;
            align-items: center;
            gap: 4px;
            padding: 3px 9px;
            border-radius: 999px;
            border: 1px solid rgba(56, 189, 248, 0.3);
            background: rgba(14, 116, 144, 0.2);
            color: #e0f2fe;
            font-size: 11px;
            font-weight: 600;
            cursor: pointer;

            &.is-zap {
                border-color: rgba(245, 158, 11, 0.55);
                background: rgba(245, 158, 11, 0.2);
                color: #fde68a;
            }
        }
    }

    .graph-hud {
        position: absolute;
        z-index: 6;
        border: 1px solid rgba(56, 189, 248, 0.22);
        border-radius: 10px;
        background: rgba(6, 14, 28, 0.78);
        color: #e2e8f0;
        backdrop-filter: blur(12px);
        box-shadow: 0 8px 28px rgba(0, 2, 10, 0.65);
    }

    .graph-hud-hubs {
        top: var(--hud-top, 58px);
        left: 14px;
        width: 248px;
        max-height: calc(100% - var(--hud-top, 58px) - var(--hud-bottom-left, 64px));
        overflow-y: auto;
        padding: 8px 10px;
    }

    .graph-hud-traces {
        top: var(--hud-top, 58px);
        right: 14px;
        width: 310px;
        /* 底部给右下画布控制罗盘（约 190px 高）留位；视口很矮时至少保留 200px 让列表可用 */
        max-height: max(200px, calc(100% - var(--hud-top, 58px) - 260px));
        overflow-y: auto;
        padding: 8px 10px;
    }

    .hud-panel-head {
        display: flex;
        align-items: center;
        gap: 6px;
        cursor: pointer;
        user-select: none;
    }

    .hud-panel-title {
        flex: 1;
        min-width: 0;
        overflow: hidden;
        font-size: 12px;
        font-weight: 700;
        color: #f1f5f9;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .hud-panel-badge {
        padding: 1px 6px;
        border-radius: 999px;
        font-size: 10px;
        font-weight: 700;
        background: rgba(56, 189, 248, 0.18);
        color: #38bdf8;
    }

    .hud-trace-auto {
        padding: 1px 6px;
        border-radius: 999px;
        font-size: 9.5px;
        font-weight: 700;
        background: rgba(148, 163, 184, 0.18);
        color: #94a3b8;
        &.is-on {
            background: rgba(16, 185, 129, 0.22);
            color: #6ee7b7;
        }
    }

    .hud-trace-sub {
        margin: 6px 0;
        font-size: 11px;
        color: #94a3b8;
        line-height: 1.5;
        text-wrap: pretty;
    }

    .hud-custom-recall {
        display: flex;
        gap: 5px;
        margin-bottom: 7px;
    }

    .hud-recall-input {
        flex: 1;
        min-width: 0;
        height: 26px;
        padding: 0 8px;
        border-radius: 6px;
        border: 1px solid rgba(56, 189, 248, 0.28);
        background: rgba(2, 8, 20, 0.8);
        color: #f1f5f9;
        font-size: 11px;
        outline: none;
    }

    .hud-trace-fire-btn {
        padding: 3px 9px;
        border-radius: 6px;
        border: 1px solid rgba(56, 189, 248, 0.45);
        background: rgba(14, 165, 233, 0.25);
        color: #e0f2fe;
        font-size: 11px;
        font-weight: 600;
        cursor: pointer;
    }

    .hud-recall-receipt {
        margin-bottom: 7px;
        padding: 7px 8px;
        border-radius: 7px;
        border: 1px solid rgba(16, 185, 129, 0.35);
        background: rgba(6, 78, 59, 0.22);
        font-size: 10.5px;

        .receipt-head {
            display: flex;
            justify-content: space-between;
            color: #6ee7b7;
            font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
            font-size: 9.5px;
            margin-bottom: 3px;
        }

        .receipt-q {
            color: #f8fafc;
            font-weight: 600;
        }

        .receipt-a {
            color: #38bdf8;
            margin-top: 2px;
        }

        .receipt-hits {
            display: flex;
            flex-direction: column;
            gap: 3px;
            margin-top: 5px;
        }

        .receipt-hit-chip {
            display: flex;
            align-items: center;
            gap: 5px;
            padding: 3px 6px;
            border-radius: 4px;
            background: rgba(2, 8, 20, 0.65);
            cursor: pointer;
            &:hover {
                background: rgba(14, 165, 233, 0.22);
            }
            .rh-cell {
                color: #22d3ee;
                font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
                font-size: 9px;
                flex-shrink: 0;
            }
            .rh-txt {
                color: #cbd5e1;
                font-size: 10px;
                overflow: hidden;
                text-overflow: ellipsis;
                white-space: nowrap;
            }
        }
    }

    .hud-filter {
        margin: 6px 0;
    }

    .hud-hub-list {
        max-height: 220px;
        overflow-y: auto;
        display: flex;
        flex-direction: column;
        gap: 3px;
    }

    .hud-hub-item {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 4px 6px;
        border-radius: 6px;
        font-size: 11.5px;
        cursor: pointer;
        transition: background 0.14s ease;

        &:hover,
        &.is-selected {
            background: rgba(56, 189, 248, 0.16);
        }
    }

    .hud-hub-name {
        flex: 1;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .hud-hub-deg {
        font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
        font-size: 10px;
        color: #94a3b8;
    }

    .hud-trace-body {
        flex: 1;
        min-width: 0;
    }

    .hud-trace-name {
        font-size: 11px;
        font-weight: 600;
        color: #f1f5f9;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .hud-trace-time {
        font-size: 9.5px;
        color: #94a3b8;
    }

    .hero-brand-dock {
        position: absolute;
        left: 14px;
        bottom: 54px;
        z-index: 6;
        max-width: 390px;
        padding: 12px 14px;
        border-radius: 12px;
        border: 1px solid rgba(56, 189, 248, 0.24);
        background: rgba(5, 12, 24, 0.76);
        backdrop-filter: blur(14px);
        color: #e2e8f0;
        pointer-events: auto;

        .hb-eyebrow {
            display: inline-flex;
            align-items: center;
            gap: 6px;
            font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
            font-size: 9.5px;
            color: #38bdf8;
        }

        .hb-dot {
            width: 6px;
            height: 6px;
            border-radius: 50%;
            background: #10b981;
            box-shadow: 0 0 6px #10b981;
        }

        .hb-title {
            margin: 6px 0 6px;
            font-size: 20px;
            font-weight: 700;
            line-height: 1.25;
            color: #f8fafc;
            text-wrap: balance;
            word-break: keep-all;
            overflow-wrap: break-word;
            span {
                display: block;
                color: #38bdf8;
            }
        }

        .hb-facts {
            display: flex;
            flex-wrap: wrap;
            gap: 3px 12px;
            padding: 0;
            margin: 0 0 10px;
            list-style: none;
            font-size: 11.5px;
            line-height: 1.5;
            color: #94a3b8;

            li {
                display: inline-flex;
                align-items: center;
                gap: 6px;
                white-space: nowrap;

                &::before {
                    width: 4px;
                    height: 4px;
                    content: '';
                    border-radius: 50%;
                    background: #38bdf8;
                }
            }
        }

        .hb-actions {
            display: flex;
            flex-wrap: wrap;
            gap: 7px;
        }

        .hb-btn-primary {
            display: inline-flex;
            align-items: center;
            gap: 5px;
            height: 30px;
            padding: 0 12px;
            border-radius: 6px;
            border: 1px solid #38bdf8;
            background: linear-gradient(135deg, #0284c7, #0ea5e9);
            color: #ffffff;
            font-size: 11.5px;
            font-weight: 600;
            cursor: pointer;
        }

        .hb-btn-code {
            display: inline-flex;
            align-items: center;
            gap: 6px;
            height: 30px;
            padding: 0 10px;
            border-radius: 6px;
            border: 1px solid rgba(148, 163, 184, 0.28);
            background: rgba(2, 6, 16, 0.8);
            color: #cbd5e1;
            font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
            font-size: 10.5px;
            cursor: pointer;
        }
    }

    .graph-hud-ctrl {
        right: 14px;
        bottom: 58px;
        display: flex;
        flex-direction: column;
        padding: 4px;
        gap: 2px;

        .hud-btn {
            width: 30px;
            height: 30px;
            display: inline-flex;
            align-items: center;
            justify-content: center;
            border: none;
            border-radius: 7px;
            background: transparent;
            color: #cbd5e1;
            cursor: pointer;
            &:hover,
            &.is-on {
                background: rgba(56, 189, 248, 0.2);
                color: #38bdf8;
            }
        }

        .hud-divider {
            height: 1px;
            margin: 2px 4px;
            background: rgba(148, 163, 184, 0.2);
        }
    }

    .graph-legend {
        position: absolute;
        bottom: 12px;
        left: 50%;
        transform: translateX(-50%);
        z-index: 7;
        display: flex;
        align-items: center;
        gap: 10px;
        padding: 6px 16px;
        border-radius: 999px;
        border: 1px solid rgba(56, 189, 248, 0.22);
        background: rgba(6, 14, 28, 0.78);
        backdrop-filter: blur(10px);
        white-space: nowrap;
        max-width: calc(100vw - 32px);
        overflow-x: auto;
        color: #cbd5e1;
        font-size: 11.5px;
    }

    .graph-legend-item {
        display: inline-flex;
        align-items: center;
        gap: 5px;
        cursor: pointer;
        &--off {
            opacity: 0.4;
        }
    }

    .nebula-tooltip {
        position: absolute;
        z-index: 12;
        max-width: 320px;
        padding: 10px 12px;
        border-radius: 10px;
        border: 1px solid rgba(56, 189, 248, 0.35);
        background: rgba(4, 10, 22, 0.9);
        backdrop-filter: blur(12px);
        color: #f1f5f9;
        pointer-events: none;
        font-size: 11.5px;

        .nt-title {
            font-weight: 700;
            margin-bottom: 3px;
        }
        .nt-meta {
            font-size: 10.5px;
            margin-bottom: 3px;
        }
        .nt-anchor {
            font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
            font-size: 10px;
            color: #94a3b8;
        }
        .nt-span {
            margin-top: 4px;
            color: #cbd5e1;
            font-size: 11px;
        }
        .nt-hint {
            margin-top: 5px;
            font-size: 10px;
            color: #38bdf8;
        }
    }

    .graph-sub-title {
        margin: 14px 0 6px;
        font-size: 12px;
        font-weight: 700;
    }

    .graph-node-summary,
    .graph-node-span {
        padding: 8px 10px;
        border-radius: 6px;
        background: var(--el-fill-color-light);
        font-size: 12px;
        line-height: 1.55;
        white-space: pre-wrap;
    }

    .cell-member-list {
        display: flex;
        flex-direction: column;
        gap: 6px;
    }

    .cell-member-card {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 6px 8px;
        border-radius: 6px;
        background: var(--el-fill-color-light);
        cursor: pointer;

        .cell-member-layer {
            padding: 1px 5px;
            border-radius: 4px;
            color: #fff;
            font-size: 10px;
            font-weight: 700;
        }
        .cell-member-text {
            flex: 1;
            font-size: 12px;
        }
    }
}

/* 平板及以下：右侧召回面板与右下罗盘可能相遇，罗盘改为横排贴底，召回面板就能占满右侧 */
@media (max-width: 1100px) {
    .hero-graph-stage {
        .graph-hud-ctrl {
            bottom: 56px;
            flex-direction: row;
        }
        .graph-hud-ctrl .hud-divider {
            width: 1px;
            height: auto;
            margin: 4px 2px;
        }
        .graph-hud-traces {
            max-height: max(200px, calc(100% - var(--hud-top, 58px) - 120px));
        }
    }
}

@media (max-width: 1280px) {
    .hero-graph-stage .fs-cockpit-topbar .fs-particle-stats {
        display: none;
    }
}

@media (max-width: 900px) {
    .hero-graph-stage {
        .graph-hud-hubs,
        .hero-brand-dock {
            display: none;
        }
        .graph-hud-traces {
            width: 260px;
        }
    }
}

/* 手机：顶栏只留核心控件，右侧召回面板默认收在顶栏下方，不压住星图 */
@media (max-width: 640px) {
    .hero-graph-stage {
        .fs-cockpit-topbar {
            border-radius: 16px;

            .fs-quick-layers,
            .fs-capacity-ladder,
            .fs-search-box,
            .fs-divider {
                display: none;
            }
        }
        .graph-hud-traces {
            right: 10px;
            left: 10px;
            width: auto;
            max-height: max(200px, calc(100% - var(--hud-top, 58px) - 120px));
        }
        .graph-hud-ctrl {
            right: 10px;
        }
    }
}
</style>

