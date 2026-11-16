<template>
    <div
        ref="graphPageRef"
        class="default-main memory-graph-page"
        :class="{
            'is-true-fullscreen': isFullscreen,
            'is-zen-hud': zenMode,
            'is-fs-drawer-open': fsDrawerOpen,
        }"
    >
        <el-card shadow="never" class="graph-card">
            <div v-loading="loading" :element-loading-text="graphLoadingText" class="graph-body">
                <div ref="threeContainerRef" class="graph-chart graph-chart-3d" />

                <!-- 顶部中央：全画幅星舰主指挥舱（双模式切换：真球体宇宙 ⇄ 展开星盘 + 128~ALL 节点容量槽 + 三层速滤 + 一键收起双侧栏 + 真全屏） -->
                <div class="fs-cockpit-topbar" :class="{ 'is-drill': inDrill }">
                    <!-- 双宇宙空间模式切换槽（快捷键 V） -->
                    <div class="fs-capacity-ladder fs-space-switch" :title="isEn ? 'Switch 3D Space Layout (V)' : '切换三维空间拓扑模式 (快捷键 V)'">
                        <button
                            type="button"
                            class="fs-ladder-pill"
                            :class="{ 'is-active': spaceMode === 'sphere' }"
                            @click="setSpaceMode('sphere')"
                        >
                            <Icon name="lucide-globe" :size="11" style="margin-right: 3px; vertical-align: -1px" />
                            {{ isEn ? '3D Sphere' : '真球形宇宙' }}
                        </button>
                        <button
                            type="button"
                            class="fs-ladder-pill"
                            :class="{ 'is-active': spaceMode === 'disc' }"
                            @click="setSpaceMode('disc')"
                        >
                            <Icon name="lucide-orbit" :size="11" style="margin-right: 3px; vertical-align: -1px" />
                            {{ isEn ? 'Galaxy Disc' : '展开星盘' }}
                        </button>
                    </div>

                    <span class="fs-divider" />

                    <span
                        class="fs-live-pill"
                        :title="isLocomoPreview ? (isEn ? 'Built-in LoCoMo 3D memory graph sample (click for preview note)' : '内置 LoCoMo 3D 记忆图谱预置示例（点击查看预览说明）') : (isEn ? 'Zero-poll WebSocket live stream (click to pause/resume)' : '零轮询 WebSocket 实时流（点击暂停/恢复自动刷新）')"
                        @click="isLocomoPreview ? (previewVisible = true) : toggleAutoRefresh()"
                    >
                        <span class="sse-dot" :class="{ 'is-live': isLocomoPreview || (wsConnected && autoRefresh) }" />
                        {{ liveBadgeText }}
                    </span>
                    <span class="fs-stat-text">
                        <b>{{ activeNodes.length }}</b>/{{ totalNodeCount }} {{ isEn ? 'Nodes' : '节点' }} · <b>{{ activeEdges.length }}</b> {{ isEn ? 'Synapses' : '突触' }}
                    </span>

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
                            class="fs-layer-chip is-atom"
                            :class="{ 'is-active': roleFilter === 'atom' }"
                            :disabled="inDrill"
                            @click="roleFilter = roleFilter === 'atom' ? 'all' : 'atom'"
                        >
                            {{ isEn ? 'Atom' : '原子' }} {{ graph?.stats?.atom_total || roleCounts.atom }}
                        </button>
                        <button
                            type="button"
                            class="fs-layer-chip is-mol"
                            :class="{ 'is-active': roleFilter === 'molecule' }"
                            :disabled="inDrill"
                            @click="roleFilter = roleFilter === 'molecule' ? 'all' : 'molecule'"
                        >
                            {{ isEn ? 'Mol' : '分子' }} {{ graph?.stats?.molecule_total || roleCounts.molecule }}
                        </button>
                    </div>

                    <span class="fs-divider" />

                    <!-- 全局粒子拓扑统计（原子/分子/突触） -->
                    <div class="fs-stat-text" style="display:flex; gap:12px; font-size: 10px;">
                        <span title="Active Atomic Particles">A: <b>{{ graph?.stats?.atom_total || roleCounts.atom }}</b></span>
                        <span title="Active Molecular Fragments">M: <b>{{ graph?.stats?.molecule_total || roleCounts.molecule }}</b></span>
                        <span title="Active Dimer Synapses">S: <b>{{ graph?.stats?.dimer_synapse_total || activeEdges.length }}</b></span>
                    </div>

                    <span class="fs-divider" />

                    <!-- 原生内联节点容量阶梯槽（128 ~ 8K / ALL，真全屏下直接点击切换，零弹层依赖） -->
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

                    <!-- 极简全息搜索输入 -->
                    <div class="fs-search-box">
                        <input
                            v-model="keyword"
                            type="text"
                            class="fs-search-input"
                            :disabled="inDrill"
                            :placeholder="isEn ? 'Search memory…' : '搜索记忆/问答…'"
                            @keyup.enter="load()"
                        />
                        <button v-if="keyword" type="button" class="fs-search-clear" @click="keyword = ''; load()">×</button>
                    </div>

                    <!-- 一键收起/展开左右双侧栏（纯净观星模式 · Zen HUD） -->
                    <button
                        type="button"
                        class="fs-action-btn"
                        :class="{ 'is-open': !hubPanelCollapsed || !tracePanelCollapsed }"
                        :title="isEn ? 'Collapse or expand both side panels (H)' : '一键收起 / 展开左右两个侧栏面板 (快捷键 H)'"
                        @click="toggleSidePanels()"
                    >
                        <Icon :name="hubPanelCollapsed && tracePanelCollapsed ? 'lucide-panel-left-open' : 'lucide-panel-left-close'" :size="12" />
                        {{ hubPanelCollapsed && tracePanelCollapsed ? (isEn ? 'Panels' : '展开侧栏') : isEn ? 'Zen' : '收起侧栏' }}
                    </button>

                    <button type="button" class="fs-action-btn is-zap" @click="fireNextCascade()">
                        <Icon name="lucide-zap" :size="12" />
                        {{ isEn ? 'Pulse' : '放电' }}
                    </button>

                    <button type="button" class="fs-action-btn" :title="isEn ? 'Refresh graph' : '刷新图谱'" @click="load()">
                        <Icon name="el-refresh" :size="12" />
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
                    tone="light"
                    :top-px="10"
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
                    <div v-if="hover3DNode.atoms?.length" class="nt-atoms">
                        <span class="nt-sub-tag">{{ isEn ? 'Atoms' : '核心原子' }}</span>
                        <span v-for="a in hover3DNode.atoms.slice(0, 5)" :key="a.token" class="nt-atom-chip">
                            {{ a.token }} <i>{{ a.potential.toFixed(1) }}</i>
                        </span>
                    </div>
                    <div v-if="hover3DNode.molecules?.length" class="nt-mols">
                        <span class="nt-sub-tag is-mol">{{ isEn ? 'Molecule' : '分子波包' }}</span>
                        <span class="nt-mol-text">{{ clip(hover3DNode.molecules[0]!.text, 52) }}</span>
                    </div>
                    <div class="nt-hint">
                        {{ isEn ? 'Click to inspect · Double-click to drill down' : '单击飞入查看详情 · 双击进入 3D 同心轨道下钻' }}
                    </div>
                </div>

                <!-- 左上：核心枢纽导航榜（支持「节点枢纽 / 原子荷 / 分子波包」三模态切换） -->
                <div v-if="activeNodes.length" class="graph-hud graph-hud-hubs">
                    <div class="hud-panel-head" @click="hubPanelCollapsed = !hubPanelCollapsed">
                        <span class="hud-panel-title">
                            <Icon name="lucide-radar" :size="13" style="margin-right: 5px" />
                            {{ isEn ? 'Galaxy Navigator' : '星系粒子导航' }}
                        </span>
                        <span class="hud-panel-badge">{{ hudTab === 'nodes' ? hubList.length : hudTab === 'atoms' ? topAtomList.length : topMoleculeList.length }}</span>
                        <Icon :name="hubPanelCollapsed ? 'lucide-chevron-down' : 'lucide-chevron-up'" :size="13" />
                    </div>
                    <template v-if="!hubPanelCollapsed">
                        <div class="hud-mode-tabs">
                            <button type="button" class="hud-mode-tab" :class="{ 'is-on': hudTab === 'nodes' }" @click="hudTab = 'nodes'">
                                {{ isEn ? 'Nodes' : '节点' }} ({{ activeNodes.length }})
                            </button>
                            <button type="button" class="hud-mode-tab" :class="{ 'is-on': hudTab === 'atoms' }" @click="hudTab = 'atoms'">
                                {{ isEn ? 'Atoms' : '原子荷' }} ({{ graph?.stats?.atom_total ?? 0 }})
                            </button>
                            <button type="button" class="hud-mode-tab" :class="{ 'is-on': hudTab === 'molecules' }" @click="hudTab = 'molecules'">
                                {{ isEn ? 'Molecules' : '分子包' }} ({{ graph?.stats?.molecule_total ?? 0 }})
                            </button>
                        </div>
                        <el-input v-model="hubFilter" size="small" clearable class="hud-filter">
                            <template #prefix>
                                <Icon name="el-search" :size="12" />
                            </template>
                            <template #placeholder>
                                <span class="hud-filter-ph">{{ isEn ? 'Filter nodes / atoms / molecules…' : '过滤节点 / 原子 / 分子…' }}</span>
                            </template>
                        </el-input>
                        <div v-if="hudTab === 'nodes'" class="hud-hub-list">
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
                                <el-tooltip
                                    :content="isEn ? 'Drill down this node as an isolated ego-network' : '以该节点为中心单独下钻为独立视图'"
                                    placement="left"
                                    :enterable="false"
                                    :hide-after="0"
                                    :show-after="250"
                                >
                                    <span class="hud-hub-act" @click.stop="drillInto(n)">
                                        <Icon name="lucide-target" :size="12" />
                                    </span>
                                </el-tooltip>
                            </div>
                            <div v-if="!hubList.length" class="hud-hub-empty">{{ isEn ? 'No matching node' : '无匹配节点' }}</div>
                        </div>
                        <div v-else-if="hudTab === 'atoms'" class="hud-hub-list">
                            <div
                                v-for="a in topAtomList"
                                :key="a.token"
                                class="hud-hub-item"
                                :class="{ 'is-selected': keyword === a.token }"
                                @click="filterByParticle(a.token)"
                            >
                                <i class="geo-glyph geo-glyph--focus" />
                                <span class="hud-hub-name" :title="`${a.token} · m=${a.mass_nats} nats · A=${a.potential}`">
                                    {{ a.token }}
                                    <small class="hud-sub-metric">A={{ a.potential.toFixed(1) }}</small>
                                </span>
                                <span class="hud-hub-deg" :title="isEn ? 'Projected Addresses / Units' : '跨地址数 / 单元频次'">
                                    {{ a.cell_span }}A·{{ a.df }}U
                                </span>
                            </div>
                            <div v-if="!topAtomList.length" class="hud-hub-empty">{{ isEn ? 'No matching atom' : '无匹配原子' }}</div>
                        </div>
                        <div v-else class="hud-hub-list">
                            <div
                                v-for="m in topMoleculeList"
                                :key="m.mol_key"
                                class="hud-hub-item"
                                :class="{ 'is-selected': keyword === m.text }"
                                @click="filterByParticle(m.core_atoms?.[0] || m.text)"
                            >
                                <i class="geo-glyph geo-glyph--noetic" />
                                <span class="hud-hub-name" :title="`${m.text} (${m.mol_key} · γ=${m.gamma})`">
                                    {{ clip(m.text, 18) }}
                                </span>
                                <span class="hud-hub-deg" :title="isEn ? 'Gamma conductivity & Mass' : '传导率 γ 与静质量'">
                                    γ{{ m.gamma.toFixed(2) }}
                                </span>
                            </div>
                            <div v-if="!topMoleculeList.length" class="hud-hub-empty">{{ isEn ? 'No matching molecule' : '无匹配分子波包' }}</div>
                        </div>
                    </template>
                </div>

                <!-- 右上：最近激活扩散链（Living Memory Activation Traces） -->
                <div v-if="recentActivationList.length && !inDrill" class="graph-hud graph-hud-traces">
                    <div class="hud-panel-head" @click="tracePanelCollapsed = !tracePanelCollapsed">
                        <span class="hud-trace-bolt" :class="{ 'is-pulsing': autoPulseEnabled }">
                            <Icon name="lucide-zap" :size="13" color="#38bdf8" />
                        </span>
                        <span class="hud-panel-title">
                            {{ isEn ? 'Recent Activation Traces' : '最近激活扩散链' }}
                        </span>
                        <el-tooltip
                            :content="
                                isEn
                                    ? 'Toggle automatic bio-electric lightning cascade along recent memory traces'
                                    : '切换自动生物电巡航：每隔 3.8s 沿最近激活的记忆链路自动触发 1-Hop → 2-Hop 分形闪电扩散'
                            "
                            placement="bottom"
                            :enterable="false"
                            :hide-after="0"
                            :show-after="300"
                        >
                            <span
                                class="hud-trace-auto"
                                :class="{ 'is-on': autoPulseEnabled }"
                                @click.stop="toggleAutoPulse"
                            >
                                {{ autoPulseEnabled ? 'AUTO' : 'PAUSED' }}
                            </span>
                        </el-tooltip>
                        <Icon :name="tracePanelCollapsed ? 'lucide-chevron-down' : 'lucide-chevron-up'" :size="13" />
                    </div>
                    <template v-if="!tracePanelCollapsed">
                        <div class="hud-trace-sub">
                            <span>{{ isEn ? 'Click any trace to fire 2-Hop lightning cascade' : '点击任一印迹重演 2 跳分形闪电扩散' }}</span>
                            <button type="button" class="hud-trace-fire-btn" @click="fireNextCascade()">
                                <Icon name="lucide-zap" :size="11" style="margin-right: 2px; vertical-align: -1px" />
                                {{ isEn ? 'Pulse' : '放电' }}
                            </button>
                        </div>
                        <div class="hud-hub-list">
                            <div
                                v-for="n in recentActivationList"
                                :key="n.id"
                                class="hud-hub-item hud-trace-item"
                                :class="{ 'is-selected': n.id === selectedNodeId }"
                                @click="fireNodeCascade(n)"
                            >
                                <i class="geo-glyph" :class="nodeGlyphClass(n)" />
                                <div class="hud-trace-body">
                                    <div class="hud-trace-name" :title="n.summary">{{ clip(n.summary, 22) }}</div>
                                    <div class="hud-trace-time">
                                        {{ n.layer === 'noetic' ? 'L3 Recall' : n.layer === 'background' ? 'L1 Doc' : 'L2 Turn' }}
                                        <template v-if="n.observed_at"> · {{ n.observed_at.slice(5, 16) }}</template>
                                    </div>
                                </div>
                                <span class="hud-hub-deg">{{ n.viewDegree }}</span>
                            </div>
                        </div>
                    </template>
                </div>

                <!-- 右下：画布控制罗盘 -->
                <div v-if="activeNodes.length" class="graph-hud graph-hud-ctrl">
                    <el-tooltip
                        :content="isEn ? 'Fire 2-Hop bio-electric lightning cascade' : '触发最近记忆的 2 跳分形闪电激活扩散'"
                        placement="left"
                        :enterable="false"
                        :hide-after="0"
                        :show-after="300"
                    >
                        <button type="button" class="hud-btn" :class="{ 'is-on': autoPulseEnabled }" @click="fireNextCascade()">
                            <Icon name="lucide-zap" :size="14" />
                        </button>
                    </el-tooltip>
                    <span class="hud-divider" />
                    <el-tooltip :content="isEn ? 'Zoom in' : '放大'" placement="left" :enterable="false" :hide-after="0" :show-after="300">
                        <button type="button" class="hud-btn" @click="zoomBy(1.3)">
                            <Icon name="el-zoom-in" :size="14" />
                        </button>
                    </el-tooltip>
                    <el-tooltip :content="isEn ? 'Zoom out' : '缩小'" placement="left" :enterable="false" :hide-after="0" :show-after="300">
                        <button type="button" class="hud-btn" @click="zoomBy(1 / 1.3)">
                            <Icon name="el-zoom-out" :size="14" />
                        </button>
                    </el-tooltip>
                    <el-tooltip :content="isEn ? 'Fit graph to viewport' : '自适应居中（适配视口）'" placement="left" :enterable="false" :hide-after="0" :show-after="300">
                        <button type="button" class="hud-btn" @click="resetFit()">
                            <Icon name="lucide-scan" :size="14" />
                        </button>
                    </el-tooltip>
                    <el-tooltip
                        :content="isFullscreen ? (isEn ? 'Exit True Fullscreen (Esc / F)' : '退出真全屏 (Esc / F)') : isEn ? 'Enter True Fullscreen Cockpit (F)' : '进入真全屏星舰沉浸模式 (F)'"
                        placement="left"
                        :enterable="false"
                        :hide-after="0"
                        :show-after="300"
                    >
                        <button type="button" class="hud-btn hud-btn-fs" :class="{ 'is-on': isFullscreen }" @click="toggleTrueFullscreen()">
                            <Icon :name="isFullscreen ? 'lucide-minimize-2' : 'lucide-maximize-2'" :size="14" />
                        </button>
                    </el-tooltip>
                    <span class="hud-divider" />
                    <el-tooltip
                        :content="isEn ? 'Toggle edge predicate labels (X)' : '切换边上的关系谓词标签'"
                        placement="left"
                        :enterable="false"
                        :hide-after="0"
                        :show-after="300"
                    >
                        <button type="button" class="hud-btn" :class="{ 'is-on': showEdgeLabels }" @click="showEdgeLabels = !showEdgeLabels">
                            <Icon name="el-price-tag" :size="14" />
                        </button>
                    </el-tooltip>
                    <span class="hud-divider" />
                    <el-tooltip :content="isEn ? 'Export current canvas as PNG' : '导出当前画布为高清 PNG'" placement="left" :enterable="false" :hide-after="0" :show-after="300">
                        <button type="button" class="hud-btn" @click="exportPng()">
                            <Icon name="el-download" :size="14" />
                        </button>
                    </el-tooltip>
                </div>

                <el-empty
                    v-if="!activeNodes.length && !loading"
                    class="graph-empty"
                    :description="isEn ? 'No matching nodes in current filter' : '当前筛选条件下暂无图谱节点'"
                />

                <div v-if="activeNodes.length && !zenMode" class="graph-legend">
                    <span class="graph-legend-group">
                        {{ isEn ? 'Node Role:' : '节点角色：' }}
                        <el-popover trigger="click" placement="top" :width="480" popper-class="rm-help-popper">
                            <template #reference>
                                <button type="button" class="tip-q" :aria-label="isEn ? 'Node role & cognitive layer semantics' : '节点角色与认知层级语义说明'">?</button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">Episodic Memory Address Container (cyan translucent glowing shell): the planetary gravity center in Rolling Autonoetic Memory Engine addressing, rendered as a living container with a glowing core.</p>
                                <p v-else>动态记忆地址容器（青色半透明光晕球壳）：引力中心，承载该地址下的多粒度情景记忆，在 3D 中以带发光核心的活体容器呈现。</p>
                                <p v-if="isEn">L1 Background Unit (blue glowing vesicle): long-form document or substrate memory unit (locally mounted directory / document chunks), orbiting inside its parent container.</p>
                                <p v-else>L1 背景记忆单元（蓝色发光囊泡 · Background）：本地挂载目录或文档分块产生的长文本基础记忆单元，以容器内轨道微粒形式环绕母体。</p>
                                <p v-if="isEn">L2 Episodic Unit (emerald glowing vesicle): real-time memory unit produced by daily conversation streams or interaction events.</p>
                                <p v-else>L2 情景记忆单元（翠绿发光囊泡 · Episodic）：日常对话流或交互事件产生的实时情景记忆单元。</p>
                                <p v-if="isEn">L3 Noetic Engram (magenta neural ganglion): the “Q + Cited Units + A” insight engram crystallized on each Recall, linked to cited units by arcing neural bundles.</p>
                                <p v-else>L3 知性问答印迹（紫红神经节 · Noetic）：每次 Recall 检索结晶出的「提问 Q + 引证单元 + 回答 A」认识论印迹，以高拱神经束连向被引证单元。</p>
                            </div>
                        </el-popover>
                    </span>
                    <template v-if="inDrill">
                        <span class="graph-legend-item">
                            <i class="geo-glyph geo-glyph--focus" />
                            {{ isEn ? 'Focus Root (center)' : '下钻焦点（圆心）' }}
                        </span>
                        <span class="graph-legend-item">
                            <i class="geo-glyph geo-glyph--bg" />
                            {{ isEn ? '1-Hop Direct Neighbor' : '1 跳直接邻居' }}
                        </span>
                        <span class="graph-legend-item">
                            <i class="geo-glyph geo-glyph--cell" />
                            {{ isEn ? '2-Hop Indirect' : '2 跳间接关联' }}
                        </span>
                        <span class="graph-legend-item">
                            <i class="geo-glyph" style="background: #94a3b8; opacity: 0.7" />
                            {{ isEn ? '3-Hop Outer' : '3 跳外环' }}
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
                            {{ isEn ? `L1 BG (${roleCounts.background})` : `L1 背景 (${roleCounts.background})` }}
                        </span>
                        <span
                            class="graph-legend-item graph-legend-clickable"
                            :class="{ 'graph-legend-item--off': roleFilter !== 'all' && roleFilter !== 'episodic' }"
                            @click="roleFilter = roleFilter === 'episodic' ? 'all' : 'episodic'"
                        >
                            <i class="geo-glyph geo-glyph--ep" />
                            {{ isEn ? `L2 EP (${roleCounts.episodic})` : `L2 情景 (${roleCounts.episodic})` }}
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
                    <span class="graph-legend-group" style="margin-left: 6px">{{ isEn ? 'Synapses:' : '突触边：' }}</span>
                    <span
                        v-for="r in relationOptions.slice(0, 14)"
                        :key="r.relation"
                        class="graph-legend-item graph-legend-clickable"
                        :class="{ 'graph-legend-item--off': activeRelations.length > 0 && !activeRelations.includes(r.relation) }"
                        @click="toggleRelationChip(r.relation)"
                    >
                        <i class="graph-dot graph-dot--line" :style="{ background: relationColor(r.relation) }" />
                        {{ edgeRelationLabel(r.relation) }} ({{ r.count }})
                    </span>
                </div>
            </div>
        </el-card>

        <!-- 节点详情与邻域展开抽屉（挂载在 graphPageRef 内，确保原生 Fullscreen 下 100% 正常弹出） -->
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
                        ? 'Memory Unit Provenance'
                        : '记忆单元明文详情'
            "
            size="560px"
            :append-to-body="false"
        >
            <template v-if="currentNode">
                <div class="graph-drawer-actions">
                    <el-button size="small" type="warning" :loading="drilling" @click="drillFromDrawer()">
                        <Icon name="lucide-target" :size="13" style="margin-right: 4px" />
                        {{ isEn ? `Drill Down as Center (${drillDepth}-hop)` : `设为下钻中心（${drillDepth} 跳同心环）` }}
                    </el-button>
                    <el-button size="small" type="primary" plain :loading="expanding" @click="mergeNeighbors(1)">
                        <Icon name="el-plus" :size="13" style="margin-right: 4px" />
                        {{ isEn ? 'Merge +1 Hop into Canvas' : '并入当前画布 +1 跳' }}
                    </el-button>
                </div>

                <el-descriptions :column="2" border size="small">
                    <el-descriptions-item :span="2">
                        <template #label>
                            <span>{{ isEn ? 'Node ID / Key' : '节点标识' }}</span>
                        </template>
                        <code>{{ currentNode.node_key }}</code>
                    </el-descriptions-item>
                    <el-descriptions-item>
                        <template #label>
                            <span>{{ isEn ? 'Unit Type' : '单位角色' }}</span>
                        </template>
                        <el-tag size="small" :type="currentNode.kind === 'cell' ? 'primary' : 'success'" effect="plain">
                            {{
                                currentNode.kind === 'cell'
                                    ? isEn
                                        ? 'Memory Address Container'
                                        : '记忆地址容器'
                                    : isEn
                                      ? 'Memory Unit'
                                      : '记忆单元 (Unit)'
                            }}
                        </el-tag>
                    </el-descriptions-item>
                    <el-descriptions-item>
                        <template #label>
                            <span>{{ isEn ? 'Cognitive Layer' : '所属认知层' }}</span>
                        </template>
                        <el-tag
                            size="small"
                            :type="currentNode.layer === 'noetic' ? 'danger' : currentNode.layer === 'background' ? 'primary' : 'success'"
                            effect="plain"
                        >
                            {{
                                currentNode.layer === 'noetic'
                                    ? 'L3 Noetic (知性印迹)'
                                    : currentNode.layer === 'background'
                                      ? 'L1 Background (背景文档)'
                                      : 'L2 Episodic (情景对话)'
                            }}
                        </el-tag>
                    </el-descriptions-item>
                    <el-descriptions-item>
                        <template #label>
                            <span>{{ isEn ? 'Resonance Weight' : '共振权重' }}</span>
                        </template>
                        <b>{{ (currentNode.weight ?? 1).toFixed(3) }}</b>
                    </el-descriptions-item>
                    <el-descriptions-item>
                        <template #label>
                            <span>{{ isEn ? 'Connected Edges' : '关联连边数' }}</span>
                        </template>
                        <b>{{ currentNode.viewDegree }}</b> ({{ isEn ? 'total ' : '全图 ' }}{{ currentNode.degree }})
                    </el-descriptions-item>
                    <el-descriptions-item :span="2" :label="isEn ? 'C6 Address / Anchor' : 'C6 寻址坐标 / 句柄'">
                        <code>{{ currentNode.anchor || currentNode.node_key }}</code>
                    </el-descriptions-item>
                    <el-descriptions-item :span="2" :label="isEn ? 'Recorded At' : '记录时间'">
                        {{ currentNode.observed_at || '-' }}
                    </el-descriptions-item>
                </el-descriptions>

                <!-- 专属视图 A：L3 Noetic 知性问答印迹（分栏展示 Question Q 与 Answer A） -->
                <template v-if="currentNode.layer === 'noetic' && currentNode.kind !== 'cell'">
                    <div class="graph-sub-title">{{ isEn ? 'Question (Q · Retrieval Cue)' : '提问线索 (Question · Q)' }}</div>
                    <div class="graph-node-summary">{{ parseNoeticQA(currentNode).q }}</div>

                    <div class="graph-sub-title">{{ isEn ? 'Synthesized Answer / Cited Evidence (A)' : '知性结晶答案与引证干句 (Answer · A)' }}</div>
                    <div class="graph-node-span">{{ parseNoeticQA(currentNode).a }}</div>
                </template>

                <!-- 专属视图 B：记忆地址容器（展示地址说明与可点击成员卡片列表） -->
                <template v-else-if="currentNode.kind === 'cell'">
                    <div class="graph-sub-title">{{ isEn ? 'Memory Address Cluster Summary' : '记忆地址聚合主题' }}</div>
                    <div class="graph-node-summary">{{ currentNode.summary }}</div>
                    <div class="graph-sub-title">
                        {{ isEn ? 'Stored Memory Units Under This Address (click to open)' : '该记忆地址下收纳的记忆单元（点击直接打开详情）' }}
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
                            <span class="cell-member-text">{{ clip(m.summary, 60) }}</span>
                            <span class="cell-member-drill" :title="isEn ? 'Drill down centered on this unit' : '以该单元为圆心下钻'" @click.stop="drillInto(m)">
                                <Icon name="lucide-target" :size="12" />
                            </span>
                        </div>
                        <div v-if="!cellMemberNodes.length" class="graph-muted">
                            {{ isEn ? 'No member units under this address' : '该记忆地址下暂无成员记忆单元' }}
                        </div>
                    </div>
                </template>

                <!-- 专属视图 C：L1 Background / L2 Episodic 记忆单元（展示本地还原明文） -->
                <template v-else>
                    <div class="graph-sub-title">{{ isEn ? 'Vault Plaintext Content' : '本地明文金库还原内容' }}</div>
                    <div class="graph-node-span">「{{ (currentNode.span || '').trim() || currentNode.summary }}」</div>
                </template>

                <!-- 五级粒子微观能谱（一级守恒原子荷 AtomView × 二级共价分子波包 MoleculeView & 二聚体突触 DimerSynapses） -->
                <template v-if="currentNode.atoms?.length || currentNode.molecules?.length">
                    <div class="graph-sub-title">
                        {{ isEn ? 'Atomic Charge Spectrum (AtomView · Level-1 Particles)' : '一级原子守恒荷能谱 (AtomView · 点击词元联动共振过滤)' }}
                        <span class="graph-muted">({{ currentNode.atoms?.length ?? 0 }})</span>
                    </div>
                    <div v-if="currentNode.atoms?.length" class="particle-atom-grid">
                        <div
                            v-for="a in currentNode.atoms"
                            :key="a.token"
                            class="particle-atom-card"
                            :class="{ 'is-active': keyword === a.token }"
                            @click="filterByParticle(a.token)"
                        >
                            <div class="pa-head">
                                <span class="pa-token">{{ a.token }}</span>
                                <span class="pa-hash">#{{ a.hash_hex }}</span>
                            </div>
                            <div class="pa-metrics">
                                <span :title="isEn ? 'Information Rest Mass m(h) in Nats' : '内生信息静质量 m(h) (Nats)'">m={{ a.mass_nats.toFixed(2) }}</span>
                                <span :title="isEn ? 'Dynamic Excitation Potential A(h)' : '动态激发电位 A(h, Epoch)'">A={{ a.potential.toFixed(2) }}</span>
                                <span :title="isEn ? 'Cross-Address Incidence & Document Frequency' : '跨地址投影数 / 单元频次'">{{ a.cell_span }}A·{{ a.df }}U</span>
                            </div>
                            <div class="pa-bar-track">
                                <div class="pa-bar-fill" :style="{ width: `${Math.min(100, Math.max(12, a.potential * 12))}%` }" />
                            </div>
                        </div>
                    </div>

                    <template v-if="currentNode.molecules?.length">
                        <div class="graph-sub-title">
                            {{ isEn ? 'Covalent Molecule Wavepackets & Dimer Synapses (MoleculeView · Level-2)' : '二级共价分子波包与二聚体突触 (MoleculeView · DimerSynapses)' }}
                            <span class="graph-muted">({{ currentNode.molecules.length }})</span>
                        </div>
                        <div class="particle-mol-list">
                            <div v-for="m in currentNode.molecules" :key="m.mol_key" class="particle-mol-card">
                                <div class="pm-top">
                                    <code class="pm-key">{{ m.mol_key }}</code>
                                    <span class="pm-stat">m={{ m.mass_nats.toFixed(2) }} nats · γ={{ m.gamma.toFixed(2) }} · DF={{ m.df }}</span>
                                </div>
                                <div class="pm-text">「{{ m.text }}」</div>
                                <div v-if="m.core_atoms?.length" class="pm-atoms">
                                    <span class="pm-label">{{ isEn ? 'Covalent Bag:' : '共价原子袋：' }}</span>
                                    <span
                                        v-for="ca in m.core_atoms"
                                        :key="ca"
                                        class="pm-atom-pill"
                                        @click="filterByParticle(ca)"
                                    >
                                        {{ ca }}
                                    </span>
                                </div>
                                <div v-if="m.dimer_peers?.length" class="pm-dimers">
                                    <span class="pm-label">{{ isEn ? 'Dimer Synapse ⇄' : '二阶邻接分子突触 ⇄' }}</span>
                                    <span v-for="(dp, dIdx) in m.dimer_peers" :key="dIdx" class="pm-dimer-peer">
                                        {{ dp }}
                                    </span>
                                </div>
                            </div>
                        </div>
                    </template>
                </template>

                <div class="graph-sub-title">
                    {{
                        currentNode.kind === 'cell'
                            ? isEn
                                ? 'Member Units & Cross-Address Links'
                                : '记忆地址收纳成员与跨地址共振边'
                            : currentNode.layer === 'noetic'
                              ? isEn
                                  ? 'Cited Memory Units (cites Wormholes)'
                                  : '引证的底层记忆单元 (cites 引证虫洞)'
                              : isEn
                                ? 'Parent Address & Cited-By Noetic Insights'
                                : '所属记忆地址与引证该记忆的知性问答'
                    }}
                    <span class="graph-muted">({{ currentNodeEdges.length }})</span>
                    <span class="graph-edge-tabs">
                        <button
                            v-for="t in edgeTabs"
                            :key="t.key"
                            type="button"
                            class="edge-tab"
                            :class="{ 'is-on': edgeTab === t.key }"
                            @click="edgeTab = t.key"
                        >
                            {{ t.label }}
                        </button>
                    </span>
                </div>
                <el-empty v-if="!filteredCurrentNodeEdges.length" :description="isEn ? 'No connected edges' : '暂无关联边'" :image-size="50" />
                <div v-else class="graph-edge-list">
                    <div v-for="e in filteredCurrentNodeEdges" :key="e.id" class="graph-edge-wrap">
                        <div class="graph-edge-item" :class="{ 'is-open': expandedEdgeId === e.id }">
                            <el-tag size="small" :type="e.out ? 'primary' : 'info'" effect="plain">
                                {{ e.out ? (isEn ? 'OUT →' : '出边 →') : isEn ? '← IN' : '← 入边' }}
                            </el-tag>
                            <span class="graph-edge-rel" :style="{ color: relationColor(e.relation) }">{{ edgeRelationLabel(e.relation) }}</span>
                            <span class="graph-edge-peer" :title="e.peerSummary" @click="jumpTo(e)">{{ clip(e.peerSummary, 38) }}</span>
                            <el-tooltip :content="isEn ? 'Preview peer plaintext' : '展开/收起对端节点明文详情'" placement="left" :enterable="false" :hide-after="0" :show-after="250">
                                <span class="graph-edge-act" style="margin-left: auto" @click="toggleEdgeSpan(e)">
                                    <Icon :name="expandedEdgeId === e.id ? 'el-arrow-up' : 'el-arrow-down'" :size="12" />
                                </span>
                            </el-tooltip>
                            <el-tooltip :content="isEn ? 'Drill down centered on this peer node' : '以该对端节点为圆心单独下钻'" placement="left" :enterable="false" :hide-after="0" :show-after="250">
                                <span class="graph-edge-act graph-edge-act--drill" @click="drillToPeer(e)">
                                    <Icon name="lucide-target" :size="12" />
                                </span>
                            </el-tooltip>
                        </div>
                        <div v-if="expandedEdgeId === e.id" class="graph-edge-span">
                            <div v-if="e.span">「{{ e.span }}」</div>
                            <div v-else class="graph-muted">{{ isEn ? 'No plaintext attached' : '暂无明文详情' }}</div>
                        </div>
                    </div>
                </div>
            </template>
        </el-drawer>

        <!-- 预览功能提示（每次进入本页弹出一次；本期为预览能力，完整形态将在后续版本正式提供） -->
        <el-dialog v-model="previewVisible" width="460px" align-center>
            <div class="gpv-head">
                <Icon name="lucide-sparkles" :size="16" />
                <span>{{ isEn ? 'Memory Graph · Preview Feature' : '记忆图谱 · 预览功能' }}</span>
            </div>
            <div class="gpv-body">
                {{
                    isEn
                        ? 'In local mode, this page displays the built-in LoCoMo 3D memory graph sample (5,882 memories · 42 memory addresses) as a preview of 3D memory nebula exploration; full real-time memory graph generation will be provided in the next release.'
                        : '当前本地模式下，本页展示内置预置的 LoCoMo 3D 记忆星云示例数据（5,882 条记忆 · 42 个记忆地址），供你提前体验三维记忆图谱的探索、下钻与激活扩散；真实记忆图谱将在下一版本正式提供。'
                }}
            </div>
            <template #footer>
                <el-button type="primary" @click="previewVisible = false">{{ isEn ? 'Got it' : '我知道了' }}</el-button>
            </template>
        </el-dialog>
    </div>
</template>

<script setup lang="ts">
import { useConfig } from '@/stores/config'
import { useTerminal } from '@/stores/terminal'
import { useAdminInfo } from '@/stores/adminInfo'
import { useLoadingProgressText } from '@/utils/loadingProgress'
import { MonitorSocket } from '@/utils/monitorSocket'
import request from '@/utils/request'
import { Local } from '@/utils/storage'
import { useEventListener } from '@vueuse/core'
import { ElMessage } from 'element-plus'
import { computed, nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { computeEgoLayout, computeLayout, type LayoutEdge, type LayoutNode, type LayoutResult } from '@/components/graph/layout'
import { Hypergraph3DEngine, type Graph3DNode, type Graph3DSpaceMode } from '@/components/graph/engine'
import GraphDrillOverlay from '@/components/graph/GraphDrillOverlay.vue'
import LayoutWorker from '@/components/graph/layout.worker?worker'

defineOptions({
    name: 'dashboard/graph',
})

const router = useRouter()
const route = useRoute()

interface GraphAtomParticle {
    token: string
    hash_hex?: string
    df: number
    mass_nats: number
    potential: number
    weight: number
    cell_span: number
    unit_span: number
}

interface GraphMoleculeFragment {
    mol_key: string
    text: string
    df: number
    mass_nats: number
    gamma: number
    core_atoms?: string[]
    dimer_peers?: string[]
    unit_span?: number
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
    atoms?: GraphAtomParticle[]
    molecules?: GraphMoleculeFragment[]
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

interface GraphScope {
    source: string
    scope_key: string
    events: number
    links: number
    materials: number
}

interface GraphRelationOption {
    relation: string
    count: number
    classes?: { key: string; display: string }[]
}

interface GraphPayload {
    source: string
    scope_key: string
    scopes: GraphScope[]
    nodes: GraphNode[]
    edges: GraphEdge[]
    relations: GraphRelationOption[]
    stats: {
        event_total: number
        link_total: number
        node_shown: number
        edge_shown: number
        isolated_shown: number
        avg_entity_degree?: number
        multi_edge_entity_ratio?: number
        avg_relation_reuse?: number
        object_node_ratio?: number
        layer_node_counts?: Record<string, number>
        atom_total?: number
        molecule_total?: number
        dimer_synapse_total?: number
        top_atoms?: GraphAtomParticle[]
        top_molecules?: GraphMoleculeFragment[]
    }
    truncated: {
        nodes: boolean
        edges: boolean
    }
}

type ViewNode = GraphNode & { viewDegree: number }
type ViewEdge = GraphEdge

interface DrillCrumb {
    key: string
    summary: string
    id: number
}

const config = useConfig()
const terminal = useTerminal()
const isEn = computed(() => config.lang.active === 'en')
const isDark = computed(() => config.layout.dark)

const ECHARTS_PALETTE = [
    '#409eff',
    '#67c23a',
    '#e6a23c',
    '#f56c6c',
    '#a855f7',
    '#06b6d4',
    '#ec4899',
    '#10b981',
    '#f97316',
    '#6366f1',
]

const loading = ref(false)
const { text: graphLoadingText } = useLoadingProgressText(loading, () =>
    isEn.value ? 'Building 3D Memory Graph' : '正在构建 3D 记忆图谱'
)
// 预览功能提示弹窗（每次进入本页弹出一次）
const previewVisible = ref(false)
const updatedText = ref('')
const scopes = ref<GraphScope[]>([])
const scopeKey = ref('mem:self')
const keyword = ref('')
const roleFilter = ref<'all' | 'cell' | 'background' | 'episodic' | 'noetic' | 'atom' | 'molecule'>('all')
const keepOneHopContext = ref(true)
const activeRelations = ref<string[]>([])
const showEdgeLabels = ref(false)
const nodeLimit = ref(256)
const selectedLayers = ref<string[]>(['background', 'episodic', 'noetic'])
const layerWeights = ref<{ background: number; episodic: number; noetic: number }>({
    background: 1.0,
    episodic: 1.0,
    noetic: 1.2,
})
const graph = ref<GraphPayload | null>(null)

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

// ==================== 单节点独立下钻（Ego-Network Drill-down） ====================

const drillStack = ref<DrillCrumb[]>([])
const drillFocus = ref<ViewNode | null>(null)
const drillDepth = ref<1 | 2 | 3>(2)
const drillPayload = ref<{ nodes: GraphNode[]; edges: GraphEdge[] } | null>(null)
const drilling = ref(false)

const inDrill = computed(() => drillStack.value.length > 0 && !!drillPayload.value)

// 节点注册表：跨全景 / 下钻响应合并，保证按 node_key 兜底合成焦点节点
const nodeByKey = new Map<string, GraphNode>()

// ==================== HUD 状态 ====================

const hubPanelCollapsed = ref(false)
const hudTab = ref<'nodes' | 'atoms' | 'molecules'>('nodes')
const hubFilter = ref('')
const selectedNodeId = ref<number | null>(null)
let selectTimer: ReturnType<typeof setTimeout> | null = null

const topAtomList = computed<GraphAtomParticle[]>(() => {
    const raw = graph.value?.stats?.top_atoms ?? []
    const q = hubFilter.value.trim().toLowerCase()
    return q ? raw.filter((a) => a.token.toLowerCase().includes(q)) : raw
})

const topMoleculeList = computed<GraphMoleculeFragment[]>(() => {
    const raw = graph.value?.stats?.top_molecules ?? []
    const q = hubFilter.value.trim().toLowerCase()
    return q ? raw.filter((m) => m.text.toLowerCase().includes(q) || m.mol_key.toLowerCase().includes(q)) : raw
})

function filterByParticle(term: string): void {
    const clean = (term || '').trim()
    if (!clean) return
    if (inDrill.value) exitDrill()
    keyword.value = keyword.value === clean ? '' : clean
    void load()
}

const relationOptions = computed(() => graph.value?.relations ?? [])

const relationColorMap = computed(() => {
    const map: Record<string, string> = {
        member_of: '#06b6d4',
        cites: '#ec4899',
        weak_edge: '#f59e0b',
        atom_of: '#fbbf24',
        molecule_of: '#a855f7',
    }
    relationOptions.value.forEach((r, i) => {
        if (!map[r.relation]) {
            map[r.relation] = ECHARTS_PALETTE[i % ECHARTS_PALETTE.length]!
        }
    })
    return map
})

function relationColor(relation: string): string {
    const mapped = relationColorMap.value[relation]
    if (mapped) return mapped
    let h = 0
    for (let i = 0; i < relation.length; i++) {
        h = (h * 31 + relation.charCodeAt(i)) >>> 0
    }
    return ECHARTS_PALETTE[h % ECHARTS_PALETTE.length]!
}

function nodeDotColor(n: { kind: string; layer?: string; intention?: string }): string {
    if (n.kind === 'cell') return '#0891b2'
    if (n.kind === 'atom') return '#f59e0b'
    if (n.kind === 'molecule') return '#a855f7'
    if (n.layer === 'noetic') return '#ec4899'
    if (n.layer === 'background') return '#3b82f6'
    return '#10b981'
}

function toggleRelationChip(rel: string) {
    const idx = activeRelations.value.indexOf(rel)
    if (idx >= 0) {
        activeRelations.value.splice(idx, 1)
    } else {
        activeRelations.value.push(rel)
    }
}

/** 对节点/边集合应用当前角色/层级过滤、实时关键词过滤与边类型过滤（带 1 跳上下文连通保护，绝不断边或变空白） */
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

/** Ego 同心环布局解算（下钻模式：焦点居中 + 1/2/3 跳同心圆环） */
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

/** HUD 枢纽导航榜：按可见连接度降序（可关键字过滤），单击定位、图标下钻 */
const hubList = computed(() => {
    const pool = activeNodes.value
    const q = hubFilter.value.trim().toLowerCase()
    const filtered = q ? pool.filter((n) => n.summary.toLowerCase().includes(q) || n.node_key.toLowerCase().includes(q)) : pool
    return [...filtered].sort((a, b) => b.viewDegree - a.viewDegree || a.id - b.id).slice(0, 14)
})

const tracePanelCollapsed = ref(false)
const autoPulseEnabled = ref(true)
const SPACE_MODE_KEY = 'rm-graph-space-mode'
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

/** 最近被激活扩散的记忆印迹榜（优先展示最新 Recall 结晶的 L3 Noetic 神经节与最新 L2/L1 活跃单元） */
const recentActivationList = computed(() => {
    const pool = activeNodes.value.filter((n) => n.kind !== 'cell' && n.viewDegree > 0)
    const noetics = pool
        .filter((n) => n.layer === 'noetic')
        .sort((a, b) => (b.observed_at || '').localeCompare(a.observed_at || '') || b.viewDegree - a.viewDegree)
    const units = pool
        .filter((n) => n.layer !== 'noetic')
        .sort((a, b) => (b.observed_at || '').localeCompare(a.observed_at || '') || b.viewDegree - a.viewDegree)
    return [...noetics.slice(0, 4), ...units.slice(0, 3)].slice(0, 6)
})

const filteredEdgeCount = computed(() => activeEdges.value.length)

// ==================== 布局调度（Web Worker + 同步降级） ====================

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
        }, 200)
    })
    sizeObserver.observe(el)
}

// ==================== 3D 星云记忆图谱引擎（Three.js WebGL 超图） ====================


/** 懒加载 Three.js 超图引擎并绑定悬停 / 单击 / 双击交互（首次进入 3D 视图时创建）。 */
function ensureThreeEngine(): Hypergraph3DEngine | null {
    const el = threeContainerRef.value
    if (!el) return null
    if (!threeEngine) {
        threeEngine = new Hypergraph3DEngine(el, {
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

/** 3D 渲染调度：把当前可见节点 / 边与二维星系坐标合成三维「记忆地址容器包络 × 容器内微粒 × 跨层突触」拓扑。 */
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
            isDark: isDark.value,
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
const LAYOUT_WORKER_TIMEOUT = 2500

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

        // <=350 节点或 >600 大规模星系簇（O(N) 确定性同心环布局）主线程仅需 ~4ms，直接同步算出坐标并渲染
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
            }, LAYOUT_WORKER_TIMEOUT)
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

// ==================== 视口控制（HUD 罗盘 · 3D 相机） ====================

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
        // 浏览器策略或内嵌环境拒绝原生 Fullscreen 时，无缝回退至 100vw×100vh 视口级真全屏
        isFullscreen.value = !isFullscreen.value
    }
    if (isFullscreen.value) {
        // 进入真全屏沉浸模式时，自动连同左右两个侧栏一起收起，把 100% 宇宙视野还给用户
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

function exportPng(): void {
    const ctrl = ensureThreeEngine()
    if (!ctrl) return
    const a = document.createElement('a')
    a.href = ctrl.exportPngDataURL()
    a.download = `rolling-memory-graph-3d-${scopeKey.value.replace(/[^a-z0-9]+/gi, '_')}-${Date.now()}.png`
    a.click()
}

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

function fireNextCascade(): void {
    const ctrl = ensureThreeEngine()
    const firedId = ctrl?.triggerActivationCascade(undefined, true) ?? null
    if (firedId !== null) {
        selectedNodeId.value = firedId
        if (selectTimer) clearTimeout(selectTimer)
        selectTimer = setTimeout(() => {
            selectedNodeId.value = null
            selectTimer = null
            threeEngine?.setSelectedNode(null)
        }, 2400)
    }
}

function fireNodeCascade(node: ViewNode): void {
    locateNode(node)
}


// ==================== 数据加载 ====================

let graphFetching = false
let autoReloadQueued = false

async function load(opts: { silent?: boolean } = {}): Promise<void> {
    const silent = opts?.silent === true
    if (graphFetching) {
        if (silent) autoReloadQueued = true
        return
    }
    if (!silent && inDrill.value) exitDrill()
    graphFetching = true
    if (!silent) loading.value = true
    try {
        const res = await request<GraphPayload>(
            {
                url: '/admin/plugin/rolling-memory/graph',
                method: 'POST',
                // 图谱装配（金库读取 + 五级粒子富化）首帧可能超过全局 10s 默认超时，向上限放宽到 60s
                timeout: 60000,
                data: {
                    scope: scopeKey.value || 'mem:self',
                    limit: nodeLimit.value,
                    keyword: keyword.value,
                    layers: selectedLayers.value,
                    layer_weights: layerWeights.value,
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
            if (Array.isArray(data.scopes) && data.scopes.length > 0) {
                scopes.value = data.scopes
            }
            const valid = new Set((data.relations ?? []).map((r) => r.relation))
            activeRelations.value = activeRelations.value.filter((r) => valid.has(r))
            const d = new Date()
            updatedText.value = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`
            pendingLiveUpdates.value = false
            await nextTick()
            renderChart()
            // 跨页深链定位：URL 携带 ?focus=<node_key|id> 时自动高亮居中并打开溯源抽屉
            const focusQ = String(route.query.focus || '').trim()
            if (focusQ) {
                const all = (data.nodes ?? []) as GraphNode[]
                const target = all.find((n) => n.node_key === focusQ || n.node_key.endsWith(':' + focusQ) || n.summary.includes(focusQ))
                if (target) {
                    const vn: ViewNode = { ...target, viewDegree: target.degree || 1 }
                    openDetail(vn)
                }
                const cleanQuery = { ...route.query }
                delete cleanQuery.focus
                router.replace({ query: cleanQuery })
            }
        }
    } catch {
        if (!silent) ElMessage.error(isEn.value ? 'Failed to load memory graph' : '加载记忆图谱失败')
    } finally {
        graphFetching = false
        loading.value = false
        if (autoReloadQueued) {
            autoReloadQueued = false
            scheduleAutoReload()
        }
    }
}

// ==================== 零轮询 WebSocket 实时流（金库变更自动刷新） ====================

const adminInfo = useAdminInfo()
const AUTO_REFRESH_KEY = 'rm-graph-auto-refresh'
const autoRefresh = ref(Local.get(AUTO_REFRESH_KEY) !== 'off')
const wsConnected = ref(false)
const pendingLiveUpdates = ref(false)
let statusSocket: MonitorSocket | null = null
let autoReloadTimer: ReturnType<typeof setTimeout> | null = null
let lastAutoReloadAt = 0
let liveDigest = ''
const AUTO_RELOAD_DEBOUNCE_MS = 1200
// 连续写入期间（后台同步 / 评测进行中）金库指纹每秒都在变：只靠 1.2s 去抖会变成
// 「采集 1.4–2.2s → 立刻再采集」，本机 25 分钟实测触发 22 次。加 10s 下限间隔，
// 突发变更仍合并为一次尾随刷新（最后一次变更后必然落到一次加载），服务端聚合成常态负载的问题随之消失。
const AUTO_RELOAD_MIN_INTERVAL_MS = 10000

/** 金库变更指纹：L1/L2 明文文档 + 记忆地址 + L3 知性印迹 + 图谱节点总数，任一变化即触发软刷新。 */
function liveDigestOf(status: Record<string, any>): string {
    const art = (status?.artifacts ?? {}) as Record<string, any>
    return [art.thin_vault_docs ?? 0, art.thin_vault_cells ?? 0, art.thin_vault_noetic ?? 0, status?.total_nodes ?? 0].join('|')
}

const isLocomoPreview = computed(() => (graph.value as Record<string, any> | null)?.corpus?.suite === 'locomo')

/** 防抖软刷新：突发写入合并为一次静默重载；下钻 / 暂停 / 预置演示图谱期间不自动重刷。 */
function scheduleAutoReload(): void {
    if (isLocomoPreview.value) return
    if (!autoRefresh.value || inDrill.value) {
        pendingLiveUpdates.value = true
        return
    }
    const waitUntilAllowed = AUTO_RELOAD_MIN_INTERVAL_MS - (Date.now() - lastAutoReloadAt)
    const wait = Math.max(AUTO_RELOAD_DEBOUNCE_MS, waitUntilAllowed)
    if (autoReloadTimer) clearTimeout(autoReloadTimer)
    autoReloadTimer = setTimeout(() => {
        autoReloadTimer = null
        if (!autoRefresh.value || inDrill.value) {
            pendingLiveUpdates.value = true
            return
        }
        lastAutoReloadAt = Date.now()
        void load({ silent: true })
    }, wait)
}

function toggleAutoRefresh(): void {
    autoRefresh.value = !autoRefresh.value
    Local.set(AUTO_REFRESH_KEY, autoRefresh.value ? 'on' : 'off')
    if (autoRefresh.value && pendingLiveUpdates.value) {
        pendingLiveUpdates.value = false
        scheduleAutoReload()
    }
}

const liveBadgeText = computed(() => {
    if (isLocomoPreview.value) {
        const units = (graph.value as Record<string, any> | null)?.corpus?.total_units ?? 5882
        return isEn.value ? `Preview · LoCoMo Sample (${units})` : `预览功能 · LoCoMo 示例（${units} 条）`
    }
    if (!autoRefresh.value) return isEn.value ? 'Live Refresh Paused' : '实时刷新已暂停'
    if (wsConnected.value) return isEn.value ? 'WS Live Auto-Refresh' : 'WS 实时流自动刷新'
    return isEn.value ? 'Reconnecting WS…' : 'WS 重连中…'
})

const liveBadgeType = computed(() => {
    if (!autoRefresh.value) return pendingLiveUpdates.value ? 'warning' : 'info'
    if (!wsConnected.value) return 'info'
    return pendingLiveUpdates.value ? 'warning' : 'success'
})

function onGlobalStatusStream(ev: Event): void {
    const st = (ev as CustomEvent).detail as Record<string, any> | undefined
    if (!st) return
    wsConnected.value = true
    const digest = liveDigestOf(st)
    if (liveDigest !== '' && digest !== liveDigest) {
        scheduleAutoReload()
    }
    liveDigest = digest
}

function stopStatusStream(): void {
    if (typeof window !== 'undefined') {
        window.removeEventListener('mem:status-stream', onGlobalStatusStream)
    }
    if (statusSocket) {
        statusSocket.stop()
        statusSocket = null
    }
    wsConnected.value = false
}

function startStatusStream(): void {
    stopStatusStream()
    if (typeof window !== 'undefined') {
        window.addEventListener('mem:status-stream', onGlobalStatusStream)
        if ((window as any).__rmLatestStatus) {
            onGlobalStatusStream(new CustomEvent('mem:status-stream', { detail: (window as any).__rmLatestStatus }))
        }
    }
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') {
        return
    }
    const token = adminInfo.token
    if (!token) return
    statusSocket = new MonitorSocket({
        token,
        room: 'rm/status',
        onStatus: (st) => {
            wsConnected.value = st === 'connected'
        },
        onMessage: (env: any) => {
            if (env?.event !== 'status' || !env.data) return
            const st = env.data as Record<string, any>
            wsConnected.value = true
            const digest = liveDigestOf(st)
            if (liveDigest !== '' && digest !== liveDigest) {
                scheduleAutoReload()
            }
            liveDigest = digest
        },
    })
    statusSocket.start()
}

// ==================== 单节点独立下钻（Ego Drill-down） ====================

async function drillInto(node: { node_key: string; summary: string; id?: number }, opts?: { replace?: boolean }): Promise<void> {
    if (drilling.value || !node?.node_key) return
    drilling.value = true
    try {
        const res = await request<GraphPayload>(
            {
                url: '/admin/plugin/rolling-memory/graph/expand',
                method: 'POST',
                timeout: 60000,
                data: {
                    scope: scopeKey.value || 'mem:self',
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

        let focusNode = nodes.find((n) => n.node_key === node.node_key)
        if (!focusNode) {
            focusNode = nodeByKey.get(node.node_key)
        }
        if (!focusNode) {
            ElMessage.info(isEn.value ? 'This node has no assertions in the current scope' : '该节点在当前作用域中暂无关联断言')
            return
        }
        if (!nodes.some((n) => n.id === focusNode!.id)) nodes.unshift(focusNode)

        const viewDegree = edges.filter((e) => e.from === focusNode!.id || e.to === focusNode!.id).length
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
    if (pendingLiveUpdates.value) {
        pendingLiveUpdates.value = false
        scheduleAutoReload()
    }
}

function drillFromDrawer(): void {
    const node = currentNode.value
    if (node) void drillInto(node)
}

function drillToPeer(e: { peerId: number }): void {
    const node = activeView.value.nodes.find((n) => n.id === e.peerId) ?? view.value.nodes.find((n) => n.id === e.peerId)
    if (node) {
        void drillInto(node)
    } else {
        ElMessage.info(isEn.value ? 'Peer node is hidden by current filter' : '对端节点已被当前筛选条件隐藏')
    }
}

// ==================== 节点详情抽屉 ====================

const detailVisible = ref(false)
const currentNode = ref<ViewNode | null>(null)
const expanding = ref(false)
const expandedEdgeId = ref<number | null>(null)
const edgeTab = ref<'all' | 'out' | 'in'>('all')

interface CurrentNodeEdge {
    id: number
    out: boolean
    relation: string
    peerId: number
    peerSummary: string
    confidence: number
    anchor: string
    span: string
}

const currentNodeEdges = computed<CurrentNodeEdge[]>(() => {
    const node = currentNode.value
    if (!node) return []
    const allNodes = [...(graph.value?.nodes ?? []), ...(drillPayload.value?.nodes ?? [])]
    const byId = new Map(allNodes.map((n) => [n.id, n]))
    const out: CurrentNodeEdge[] = []
    const seen = new Set<string>()
    for (const e of activeView.value.edges) {
        const isOut = e.from === node.id
        const isIn = e.to === node.id
        if (!isOut && !isIn) continue
        const peerId = isOut ? e.to : e.from
        const key = `${isOut ? 'out' : 'in'}|${e.relation}|${peerId}`
        if (seen.has(key)) continue
        seen.add(key)
        const peerNode = byId.get(peerId)
        out.push({
            id: e.id,
            out: isOut,
            relation: e.relation,
            peerId,
            peerSummary: peerNode?.summary ?? (isOut ? e.to_label : e.from_label),
            confidence: e.confidence,
            anchor: peerNode?.anchor || e.anchor,
            span: peerNode?.span || e.span,
        })
    }
    return out
})

const edgeTabs = computed(() => [
    { key: 'all' as const, label: `${isEn.value ? 'All' : '全部'} ${currentNodeEdges.value.length}` },
    { key: 'out' as const, label: `${isEn.value ? 'OUT' : '出边'} ${currentNodeEdges.value.filter((e) => e.out).length}` },
    { key: 'in' as const, label: `${isEn.value ? 'IN' : '入边'} ${currentNodeEdges.value.filter((e) => !e.out).length}` },
])

const filteredCurrentNodeEdges = computed(() => {
    if (edgeTab.value === 'all') return currentNodeEdges.value
    if (edgeTab.value === 'out') return currentNodeEdges.value.filter((e) => e.out)
    return currentNodeEdges.value.filter((e) => !e.out)
})

function openDetail(node: ViewNode): void {
    currentNode.value = node
    expandedEdgeId.value = null
    detailVisible.value = true
}

/** 当前详情节点若为记忆地址容器，返回其通过 member_of 归属边收纳的全部成员单元（可点击卡片直接打开）。 */
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

function jumpTo(edge: { peerId: number }): void {
    const node =
        activeView.value.nodes.find((n) => n.id === edge.peerId) ?? view.value.nodes.find((n) => n.id === edge.peerId)
    if (node) {
        currentNode.value = node
        expandedEdgeId.value = null
    } else {
        ElMessage.info(isEn.value ? 'Peer node is hidden by current filter' : '对端节点已被当前筛选条件隐藏')
    }
}

function toggleEdgeSpan(e: { id: number }): void {
    expandedEdgeId.value = expandedEdgeId.value === e.id ? null : e.id
}

/** 将某个节点的 1 跳邻居增量并入当前全景画布（保持既有坐标不重排） */
async function mergeNeighbors(depth: number): Promise<void> {
    const node = currentNode.value
    const g = graph.value
    if (!node || !g) return

    expanding.value = true
    try {
        const res = await request<GraphPayload>(
            {
                url: '/admin/plugin/rolling-memory/graph/expand',
                method: 'POST',
                timeout: 60000,
                data: {
                    scope: scopeKey.value || 'mem:self',
                    seed_keys: [node.node_key],
                    depth,
                    limit: 300,
                },
            },
            { showErrorMessage: false, showNetworkErrorMessage: false, cancelDuplicate: false }
        )
        const data = res.data?.data
        if (!data) return
        const haveNode = new Set(g.nodes.map((x) => x.id))
        const haveEdge = new Set(g.edges.map((e) => e.assertion_id || String(e.id)))
        let addedNodes = 0
        let addedEdges = 0
        for (const x of data.nodes ?? []) {
            nodeByKey.set(x.node_key, x)
            if (!haveNode.has(x.id)) {
                g.nodes.push(x)
                haveNode.add(x.id)
                addedNodes++
            }
        }
        for (const e of data.edges ?? []) {
            const ek = e.assertion_id || String(e.id)
            if (!haveEdge.has(ek)) {
                e.id = g.edges.length + 1
                g.edges.push(e)
                haveEdge.add(ek)
                addedEdges++
            }
        }
        graph.value = {
            ...g,
            stats: { ...g.stats, node_shown: g.nodes.length, edge_shown: g.edges.length },
        }
        if (addedNodes === 0 && addedEdges === 0) {
            ElMessage.info(isEn.value ? 'All neighbors within this hop are already in the graph' : '该跳数内的所有邻居节点已在当前画布中')
        } else {
            ElMessage.success(
                isEn.value
                    ? `Merged +${addedNodes} nodes and +${addedEdges} edges in-place`
                    : `已增量并入 +${addedNodes} 个邻居节点、+${addedEdges} 条关系边`
            )
        }
    } finally {
        expanding.value = false
    }
}

function validRange(node: GraphNode): string {
    if (!node.valid_from && !node.valid_to) return isEn.value ? 'Unbounded / Atemporal' : '长期有效 / 未限定起止'
    return `${node.valid_from || '…'} ~ ${node.valid_to || '…'}`
}

function clip(s: string, len: number): string {
    const one = (s || '').replace(/\s+/g, ' ').trim()
    return one.length > len ? `${one.slice(0, len)}…` : one
}

useEventListener(window, 'resize', () => {
    const el = threeContainerRef.value
    if (el && el.clientWidth > 0 && el.clientHeight > 0) threeEngine?.resize(el.clientWidth, el.clientHeight)
})

useEventListener(window, 'keydown', (e: KeyboardEvent) => {
    if (wasDeactivated) return
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
    if (isFullscreen.value && !document.fullscreenElement) {
        isFullscreen.value = false
        syncEngineResize()
    }
})

useEventListener(document, 'visibilitychange', () => {
    if (wasDeactivated) return
    if (document.visibilityState === 'visible') {
        startStatusStream()
    } else {
        stopStatusStream()
    }
})

onMounted(() => {
    const el = threeContainerRef.value
    if (el) {
        viewSize.value = { w: el.clientWidth, h: el.clientHeight }
        watchViewport(el)
    }
    load()
    startStatusStream()
    previewVisible.value = true
})

let wasDeactivated = false
onActivated(() => {
    if (!wasDeactivated) return
    wasDeactivated = false
    load()
    startStatusStream()
})
onDeactivated(() => {
    wasDeactivated = true
    stopStatusStream()
    if (autoReloadTimer) {
        clearTimeout(autoReloadTimer)
        autoReloadTimer = null
    }
    if (sizeTimer) {
        clearTimeout(sizeTimer)
        sizeTimer = null
    }
    if (layoutTimer) {
        clearTimeout(layoutTimer)
        layoutTimer = null
    }
    if (selectTimer) {
        clearTimeout(selectTimer)
        selectTimer = null
    }
})

onBeforeUnmount(() => {
    stopStatusStream()
    if (autoReloadTimer) {
        clearTimeout(autoReloadTimer)
        autoReloadTimer = null
    }
    sizeObserver?.disconnect()
    sizeObserver = null
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
// 预览功能提示弹窗（内容元素带本组件 scope 标记，teleport 到 body 后 scoped 样式仍可命中）
.gpv-head {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 15px;
    font-weight: 600;
    color: var(--el-text-color-primary);
}

.gpv-body {
    margin-top: 10px;
    font-size: 13px;
    line-height: 1.7;
    color: var(--el-text-color-regular);
}

.memory-graph-page {
    .graph-card {
        :deep(.el-card__header) {
            padding-bottom: 12px;
        }
    }

    .graph-header {
        display: flex;
        align-items: flex-start;
        justify-content: space-between;
        gap: 12px;
        flex-wrap: wrap;
    }

    .graph-titles {
        display: flex;
        align-items: baseline;
        gap: 10px;
        flex-wrap: wrap;
    }

    .graph-title {
        font-size: 16px;
        font-weight: 700;
    }

    .graph-desc {
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .graph-head-actions {
        display: flex;
        align-items: center;
        gap: 8px;

        .sse-badge {
            cursor: pointer;
            display: inline-flex;
            align-items: center;
            gap: 6px;
            user-select: none;

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
        }
    }

    .graph-toolbar {
        display: flex;
        align-items: center;
        gap: 16px;
        flex-wrap: wrap;
        margin-top: 12px;
    }

    .graph-field {
        display: flex;
        align-items: center;
        gap: 8px;
    }

    .graph-field-label {
        display: inline-flex;
        align-items: center;
        gap: 3px;
        font-size: 13px;
        color: var(--el-text-color-regular);
        white-space: nowrap;
    }

    .tip-q {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 14px;
        height: 14px;
        padding: 0;
        margin-left: 3px;
        font-family: inherit;
        font-size: 10px;
        font-weight: 700;
        line-height: 1;
        color: var(--el-text-color-secondary);
        background: var(--el-fill-color);
        border: 1px solid var(--el-border-color-light);
        border-radius: 50%;
        cursor: pointer;
        user-select: none;
        transition: all 0.2s ease;

        &:hover {
            color: #ffffff;
            background: var(--el-color-primary);
            border-color: var(--el-color-primary);
        }
    }

    .graph-scope-select {
        width: 240px;
    }

    .graph-rel-select {
        width: 260px;
    }

    .graph-kw-input {
        width: 210px;
    }

    .graph-limit-input {
        width: 120px;
    }

    .graph-option-key {
        margin-right: 12px;
        font-weight: 600;
    }

    .graph-option-meta {
        float: right;
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .graph-stats {
        display: flex;
        align-items: center;
        gap: 6px;
        flex-wrap: wrap;
        margin-top: 10px;
        font-size: 13px;
        color: var(--el-text-color-regular);
    }

    .graph-stat-chip {
        display: inline-flex;
        align-items: center;
        gap: 5px;
    }

    // 统一圆形星系几何图元（All-Circular Planetary System，零方块/菱形/Emoji 混用）
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

    .graph-stat-chip .geo-glyph,
    .hud-hub-item .geo-glyph,
    .graph-legend-item .geo-glyph {
        margin-right: 0;
    }

    .graph-sep {
        color: var(--el-text-color-placeholder);
        font-style: normal;
    }

    .graph-muted {
        font-style: normal;
        color: var(--el-text-color-secondary);
    }

    .graph-hint {
        margin-left: auto;
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .graph-body {
        position: relative;
        min-height: 360px;
    }

    .graph-empty {
        position: absolute;
        inset: 0;
        display: flex;
        align-items: center;
        justify-content: center;
        background: var(--el-bg-color);
    }

    .graph-chart {
        width: 100%;
        height: calc(100vh - 142px);
        min-height: 620px;
    }

    // 3D 星云画布：电影级深渊黑曜石科幻底色 + 径向光学暗角（Optical Vignette）
    .graph-chart-3d {
        position: relative;
        overflow: hidden;
        border-radius: 10px;
        border: 1px solid rgba(56, 189, 248, 0.22);
        cursor: grab;
        background: radial-gradient(ellipse at 50% 42%, #050c1a 0%, #02050c 50%, #000103 100%);
        box-shadow:
            0 0 65px rgba(0, 2, 8, 0.85) inset,
            0 0 1px rgba(56, 189, 248, 0.35);

        &:active {
            cursor: grabbing;
        }

        // 电影级镜头暗角与微全息舱体边缘（不拦截鼠标事件）
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

    :global(html.dark) .memory-graph-page .graph-chart-3d {
        background: radial-gradient(ellipse at 50% 42%, #050c1a 0%, #02050c 50%, #000103 100%);
    }

    // 真全屏沉浸驾驶舱模式（100vw × 100vh 零边框纯宇宙视场，默认隐藏顶部宽大控制面板，改为顶部中央悬浮星舰微胶囊 + 按需下拉控制舱）
    &.is-true-fullscreen,
    &:fullscreen {
        position: fixed !important;
        inset: 0 !important;
        z-index: 2600 !important;
        width: 100vw !important;
        height: 100vh !important;
        margin: 0 !important;
        padding: 0 !important;
        background: #000103 !important;
        overflow: hidden !important;

        .graph-card {
            position: relative;
            width: 100vw;
            height: 100vh;
            border: none !important;
            border-radius: 0 !important;
            background: transparent !important;

            // 默认彻底收起宽大的 3 行头部控制面板，仅在点击「展开控制舱」时以毛玻璃悬浮抽屉形式下拉呈现
            :deep(.el-card__header) {
                display: none;
            }

            :deep(.el-card__body) {
                width: 100vw;
                height: 100vh;
                padding: 0 !important;
                margin: 0 !important;
            }
        }

        &.is-fs-drawer-open .graph-card :deep(.el-card__header) {
            display: block;
            position: absolute;
            top: 54px;
            left: 50%;
            transform: translateX(-50%);
            z-index: 18;
            width: min(1240px, calc(100vw - 32px));
            padding: 12px 18px 10px;
            border-radius: 14px;
            border: 1px solid rgba(56, 189, 248, 0.32);
            background: rgba(5, 12, 26, 0.92);
            box-shadow:
                0 20px 60px rgba(0, 2, 10, 0.85),
                0 0 28px rgba(34, 211, 238, 0.14);
            backdrop-filter: blur(16px) saturate(160%);
            color: #e2e8f0;
        }

        .graph-body {
            width: 100vw;
            height: 100vh;
            min-height: 100vh;
        }

        .graph-chart,
        .graph-chart-3d {
            width: 100vw !important;
            height: 100vh !important;
            min-height: 100vh !important;
            border: none !important;
            border-radius: 0 !important;
        }

        // 常规模式下的 HUD 布局调整，避开顶部指挥胶囊与侧栏
        .graph-hud-hubs,
        .graph-hud-traces {
            top: 58px;
        }

        // 常规模式下卡片内边距收敛，让画布更满
        .graph-card :deep(.el-card__body) {
            padding: 6px !important;
        }

        .graph-hud-ctrl {
            right: 16px;
            bottom: 64px;
        }

        // 底部图例转化为悬浮在画面底部中央的紧凑深空毛玻璃胶囊，不占用画布高度
        .graph-legend {
            position: absolute;
            bottom: 14px;
            left: 50%;
            transform: translateX(-50%);
            z-index: 7;
            margin: 0 !important;
            padding: 6px 16px !important;
            border-radius: 999px;
            border: 1px solid rgba(56, 189, 248, 0.22);
            background: rgba(6, 14, 28, 0.76);
            box-shadow: 0 8px 28px rgba(0, 2, 10, 0.65);
            backdrop-filter: blur(10px);
            white-space: nowrap;
            max-width: calc(100vw - 32px);
            overflow-x: auto;
            color: #cbd5e1;
        }
    }

    // 全画幅星舰主指挥舱（顶部中央悬浮）
    // 下钻态：指挥舱控件全部 :disabled（死控件），整体让位给面包屑与下钻态势卡，
    // 否则其 16 号 z-index 会压住面包屑（与首页 MemoryField 同套契约，两边保持一致）。
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
            padding: 0 6px 0 7px;
            font-size: 10px;
            font-weight: 700;
            color: #64748b;
            text-transform: uppercase;
            letter-spacing: 0.4px;
        }

        .fs-ladder-pill {
            padding: 2px 7px;
            border-radius: 999px;
            border: none;
            background: transparent;
            color: #94a3b8;
            font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
            font-size: 10.5px;
            font-weight: 700;
            cursor: pointer;
            transition: all 0.14s ease;

            &:hover:not(:disabled) {
                color: #f8fafc;
                background: rgba(56, 189, 248, 0.15);
            }

            &.is-active {
                color: #020617;
                background: linear-gradient(135deg, #38bdf8, #22d3ee);
                box-shadow: 0 0 10px rgba(34, 211, 238, 0.45);
            }

            &:disabled {
                opacity: 0.4;
                cursor: not-allowed;
            }
        }

        .fs-search-box {
            position: relative;
            display: inline-flex;
            align-items: center;
        }

        .fs-search-input {
            width: 128px;
            height: 22px;
            padding: 0 20px 0 9px;
            border-radius: 999px;
            border: 1px solid rgba(148, 163, 184, 0.26);
            background: rgba(2, 8, 20, 0.72);
            color: #f1f5f9;
            font-size: 11px;
            outline: none;
            transition: all 0.16s ease;

            &::placeholder {
                color: #64748b;
            }

            &:focus {
                width: 164px;
                border-color: #38bdf8;
                box-shadow: 0 0 10px rgba(56, 189, 248, 0.25);
            }
        }

        .fs-search-clear {
            position: absolute;
            right: 5px;
            border: none;
            background: transparent;
            color: #94a3b8;
            font-size: 13px;
            cursor: pointer;
            line-height: 1;

            &:hover {
                color: #ffffff;
            }
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
            transition: all 0.15s ease;

            &:hover {
                border-color: rgba(56, 189, 248, 0.5);
                color: #ffffff;
            }

            &.is-active {
                background: rgba(56, 189, 248, 0.22);
                border-color: #38bdf8;
                color: #ffffff;
                box-shadow: 0 0 10px rgba(56, 189, 248, 0.28);
            }

            &.is-bg.is-active {
                background: rgba(59, 130, 246, 0.28);
                border-color: #3b82f6;
            }

            &.is-ep.is-active {
                background: rgba(16, 185, 129, 0.26);
                border-color: #10b981;
            }

            &.is-noetic.is-active {
                background: rgba(236, 72, 153, 0.28);
                border-color: #ec4899;
            }
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
            transition: all 0.15s ease;

            &:hover,
            &.is-open {
                background: rgba(6, 182, 212, 0.35);
                border-color: #22d3ee;
                box-shadow: 0 0 12px rgba(34, 211, 238, 0.3);
            }

            &.is-zap {
                border-color: rgba(236, 72, 153, 0.45);
                background: rgba(236, 72, 153, 0.2);
                color: #fce7f3;

                &:hover {
                    background: rgba(236, 72, 153, 0.38);
                    box-shadow: 0 0 12px rgba(236, 72, 153, 0.4);
                }
            }

            &.is-exit {
                border-color: rgba(248, 113, 113, 0.38);
                background: rgba(239, 68, 68, 0.16);
                color: #fecaca;

                &:hover {
                    background: rgba(239, 68, 68, 0.32);
                }
            }
        }
    }

    // ==================== 3D 星云悬停毛玻璃浮层 ====================

    .nebula-tooltip {
        position: absolute;
        z-index: 8;
        width: 312px;
        padding: 10px 12px 9px;
        border: 1px solid rgba(103, 232, 249, 0.28);
        border-radius: 12px;
        background: linear-gradient(160deg, rgba(15, 23, 42, 0.92), rgba(8, 47, 73, 0.86));
        box-shadow:
            0 10px 34px rgba(2, 12, 27, 0.55),
            0 0 0 1px rgba(34, 211, 238, 0.08) inset,
            0 0 26px rgba(34, 211, 238, 0.14);
        backdrop-filter: blur(10px) saturate(150%);
        pointer-events: none;
        transition: opacity 0.16s ease;

        .nt-title {
            font-size: 13px;
            font-weight: 700;
            line-height: 1.35;
            color: #e2f5ff;
            word-break: break-word;
        }

        .nt-meta {
            margin-top: 3px;
            font-size: 11px;
            font-weight: 600;
            letter-spacing: 0.2px;
            color: #7dd3fc;
        }

        .nt-anchor {
            margin-top: 5px;
            padding: 3px 7px;
            border-radius: 6px;
            font-family: 'JetBrains Mono', 'Fira Code', ui-monospace, SFMono-Regular, Menlo, monospace;
            font-size: 10.5px;
            line-height: 1.5;
            color: #b6e3f5;
            background: rgba(8, 145, 178, 0.16);
            word-break: break-all;
        }

        .nt-span {
            margin-top: 6px;
            padding-left: 8px;
            border-left: 2px solid rgba(34, 211, 238, 0.45);
            font-size: 11.5px;
            line-height: 1.55;
            color: #cbd5e1;
            word-break: break-word;
        }

        .nt-hint {
            margin-top: 7px;
            font-size: 10.5px;
            color: #67e8f9;
            opacity: 0.72;
        }
    }

    :global(html.dark) .memory-graph-page .nebula-tooltip {
        border-color: rgba(103, 232, 249, 0.3);
    }

    // ==================== 画布悬浮 HUD ====================

    .graph-hud {
        position: absolute;
        z-index: 5;
    }

    .graph-hud-hubs {
        top: 10px;
        left: 10px;
        width: 254px;
        max-height: calc(100% - 20px);
        display: flex;
        flex-direction: column;
        overflow: hidden;
        border: 1px solid rgba(56, 189, 248, 0.24);
        border-radius: 10px;
        background: rgba(9, 18, 34, 0.78);
        box-shadow: 0 8px 28px rgba(2, 8, 20, 0.45);
        backdrop-filter: blur(10px);
        color: #e2e8f0;
        opacity: 0.94;
        transition: opacity 0.2s ease;

        &:hover {
            opacity: 1;
        }

        .hud-panel-title {
            color: #f1f5f9;
        }

        .hud-hub-name {
            color: #cbd5e1;
        }

        .hud-hub-item:hover {
            background: rgba(56, 189, 248, 0.14);
        }
    }

    .graph-hud-traces {
        top: 10px;
        right: 10px;
        width: 268px;
        max-height: 320px;
        display: flex;
        flex-direction: column;
        overflow: hidden;
        border: 1px solid rgba(236, 72, 153, 0.32);
        border-radius: 10px;
        background: linear-gradient(165deg, rgba(11, 20, 38, 0.84), rgba(24, 14, 38, 0.8));
        box-shadow:
            0 8px 28px rgba(2, 8, 20, 0.5),
            0 0 22px rgba(236, 72, 153, 0.12);
        backdrop-filter: blur(10px);
        color: #f1f5f9;

        .hud-panel-title {
            color: #f8fafc;
        }
    }

    .hud-trace-bolt {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        font-size: 13px;
        filter: drop-shadow(0 0 6px #38bdf8);

        &.is-pulsing {
            animation: hud-bolt-pulse 1.6s ease-in-out infinite;
        }
    }

    @keyframes hud-bolt-pulse {
        0%,
        100% {
            transform: scale(1);
            opacity: 0.85;
        }
        50% {
            transform: scale(1.22);
            opacity: 1;
        }
    }

    .hud-trace-auto {
        margin-left: auto;
        padding: 1px 6px;
        border-radius: 6px;
        font-size: 10px;
        font-weight: 700;
        letter-spacing: 0.3px;
        color: #94a3b8;
        background: rgba(148, 163, 184, 0.15);
        border: 1px solid rgba(148, 163, 184, 0.28);
        cursor: pointer;
        transition: all 0.16s ease;

        &.is-on {
            color: #67e8f9;
            background: rgba(6, 182, 212, 0.2);
            border-color: rgba(34, 211, 238, 0.48);
            box-shadow: 0 0 10px rgba(34, 211, 238, 0.25);
        }
    }

    .hud-trace-sub {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 6px;
        padding: 5px 10px 3px;
        font-size: 10.5px;
        color: #94a3b8;
    }

    .hud-trace-fire-btn {
        padding: 2px 7px;
        border-radius: 5px;
        border: 1px solid rgba(244, 114, 182, 0.45);
        background: rgba(236, 72, 153, 0.2);
        color: #fbcfe8;
        font-size: 10.5px;
        font-weight: 600;
        cursor: pointer;
        transition: all 0.15s ease;

        &:hover {
            background: rgba(236, 72, 153, 0.36);
            box-shadow: 0 0 12px rgba(236, 72, 153, 0.4);
        }
    }

    .hud-trace-item {
        padding: 4px 6px !important;

        &:hover {
            background: rgba(236, 72, 153, 0.16) !important;
        }
    }

    .hud-trace-body {
        flex: 1 1 auto;
        min-width: 0;
        display: flex;
        flex-direction: column;
        gap: 1px;
    }

    .hud-trace-name {
        font-size: 11.5px;
        color: #e2e8f0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .hud-trace-time {
        font-size: 9.5px;
        color: #94a3b8;
        font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    }

    .hud-panel-head {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 7px 10px;
        cursor: pointer;
        user-select: none;
        border-bottom: 1px solid var(--el-border-color-lighter);

        &:hover {
            background: var(--el-fill-color-lighter);
        }
    }

    .hud-panel-title {
        display: inline-flex;
        align-items: center;
        font-size: 12px;
        font-weight: 700;
        color: var(--el-text-color-primary);
    }

    .hud-panel-badge {
        padding: 0 6px;
        border-radius: 8px;
        font-size: 10px;
        font-weight: 700;
        line-height: 16px;
        color: var(--el-color-primary);
        background: var(--el-color-primary-light-9);
    }

    .hud-panel-head > .icon:last-child {
        margin-left: auto;
        color: var(--el-text-color-secondary);
    }

    .hud-filter {
        width: calc(100% - 16px);
        margin: 8px 8px 4px;
    }

    .hud-filter-ph {
        font-size: 12px;
    }

    .hud-hub-list {
        display: flex;
        flex-direction: column;
        gap: 1px;
        padding: 4px 8px 8px;
        overflow-y: auto;
    }

    .hud-hub-item {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 3px 6px;
        border-radius: 6px;
        font-size: 12px;
        cursor: pointer;
        transition: background 0.16s ease;

        &:hover {
            background: var(--el-fill-color-light);
        }

        &.is-selected {
            background: var(--el-color-warning-light-9);
            box-shadow: inset 2px 0 0 var(--el-color-warning);
        }

        &.is-focus {
            background: var(--el-color-primary-light-9);
            box-shadow: inset 2px 0 0 var(--el-color-primary);
        }
    }

    .hud-hub-name {
        flex: 1 1 auto;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        color: var(--el-text-color-regular);
    }

    .hud-hub-deg {
        flex: none;
        min-width: 18px;
        text-align: center;
        font-size: 10px;
        font-weight: 700;
        color: var(--el-text-color-secondary);
        background: var(--el-fill-color);
        border-radius: 8px;
        padding: 0 4px;
    }

    .hud-hub-act {
        flex: none;
        display: inline-flex;
        align-items: center;
        color: var(--el-text-color-secondary);
        cursor: pointer;

        &:hover {
            color: var(--el-color-warning);
        }
    }

    .hud-hub-empty {
        padding: 8px 4px;
        font-size: 12px;
        text-align: center;
        color: var(--el-text-color-placeholder);
    }

    .graph-hud-ctrl {
        right: 14px;
        bottom: 16px;
        display: flex;
        flex-direction: column;
        gap: 4px;
        padding: 6px;
        border: 1px solid rgba(56, 189, 248, 0.26);
        border-radius: 12px;
        background: rgba(6, 14, 28, 0.82);
        box-shadow: 0 10px 30px rgba(0, 2, 10, 0.65);
        backdrop-filter: blur(12px);
        opacity: 0.92;
        transition: opacity 0.2s ease;

        &:hover {
            opacity: 1;
        }
    }

    .hud-btn {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        border: none;
        border-radius: 7px;
        color: #cbd5e1;
        background: transparent;
        cursor: pointer;
        transition: all 0.16s ease;

        &:hover {
            color: #38bdf8;
            background: rgba(56, 189, 248, 0.16);
        }

        &.is-on {
            color: #ffffff;
            background: rgba(6, 182, 212, 0.36);
            box-shadow: 0 0 10px rgba(34, 211, 238, 0.35);
        }
    }

    .hud-divider {
        height: 1px;
        background: rgba(148, 163, 184, 0.2);
    }

    .graph-legend {
        position: absolute;
        bottom: 14px;
        left: 50%;
        transform: translateX(-50%);
        z-index: 6;
        display: inline-flex;
        align-items: center;
        gap: 10px;
        padding: 6px 16px;
        border-radius: 999px;
        border: 1px solid rgba(56, 189, 248, 0.24);
        background: rgba(6, 14, 28, 0.8);
        box-shadow: 0 10px 30px rgba(0, 2, 10, 0.68);
        backdrop-filter: blur(12px);
        font-size: 11.5px;
        color: #cbd5e1;
        white-space: nowrap;
        max-width: calc(100% - 110px);
        overflow-x: auto;
    }

    .graph-legend-group {
        font-weight: 700;
        color: #e2e8f0;
    }

    .graph-legend-item {
        display: inline-flex;
        align-items: center;
        gap: 5px;
        color: #cbd5e1;

        &--off {
            opacity: 0.35;
            text-decoration: line-through;
        }
    }

    .graph-legend-clickable {
        cursor: pointer;
        user-select: none;
        padding: 2px 7px;
        border-radius: 999px;
        background: rgba(15, 23, 42, 0.65);
        border: 1px solid rgba(148, 163, 184, 0.16);
        transition: all 0.16s ease;

        &:hover {
            background: rgba(56, 189, 248, 0.18);
            border-color: rgba(56, 189, 248, 0.42);
            color: #ffffff;
        }
    }

    .graph-dot {
        display: inline-block;
        width: 10px;
        height: 10px;
        border-radius: 50%;

        &--diamond {
            border-radius: 2px;
            transform: rotate(45deg);
            width: 9px;
            height: 9px;
        }

        &--line {
            width: 14px;
            height: 3px;
            border-radius: 2px;
        }
    }

    // ==================== 抽屉 ====================

    .graph-drawer-actions {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-bottom: 12px;
    }

    .graph-edge-tabs {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        margin-left: 8px;
        vertical-align: middle;
    }

    .edge-tab {
        padding: 1px 8px;
        border: 1px solid var(--el-border-color-light);
        border-radius: 10px;
        font-size: 11px;
        color: var(--el-text-color-regular);
        background: var(--el-bg-color);
        cursor: pointer;
        transition: all 0.16s ease;

        &:hover {
            color: var(--el-color-primary);
            border-color: var(--el-color-primary-light-5);
        }

        &.is-on {
            color: var(--el-color-primary);
            border-color: var(--el-color-primary);
            background: var(--el-color-primary-light-9);
        }
    }
}

.graph-sub-title {
    margin: 16px 0 6px;
    font-size: 13px;
    font-weight: 600;
}

.graph-node-summary {
    font-size: 13px;
    line-height: 1.6;
    padding: 8px 10px;
    border-radius: 6px;
    background: var(--el-fill-color-lighter);
    white-space: pre-wrap;
    word-break: break-word;
}

.graph-node-span {
    padding: 8px 10px;
    border-radius: 6px;
    background: var(--el-fill-color-light);
    font-size: 12px;
    line-height: 1.6;
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 220px;
    overflow: auto;
}

.cell-member-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-height: 340px;
    overflow-y: auto;
    padding: 4px;
    border-radius: 6px;
    background: var(--el-fill-color-lighter);
}

.cell-member-card {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 6px;
    background: var(--el-bg-color-overlay);
    cursor: pointer;
    transition: all 0.15s ease;

    &:hover {
        border-color: var(--el-color-primary);
        background: var(--el-color-primary-light-9);
    }
}

.cell-member-layer {
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 26px;
    height: 18px;
    border-radius: 4px;
    font-size: 10px;
    font-weight: 700;
    color: #fff;
}

.cell-member-text {
    flex: 1;
    font-size: 12px;
    line-height: 1.45;
    color: var(--el-text-color-primary);
    word-break: break-word;
}

.cell-member-drill {
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    border-radius: 4px;
    color: var(--el-color-warning);
    background: var(--el-color-warning-light-9);

    &:hover {
        background: var(--el-color-warning-light-7);
    }
}

.graph-edge-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
}

.graph-edge-wrap {
    display: flex;
    flex-direction: column;
    gap: 4px;
}

.graph-edge-item {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    border-radius: 6px;
    background: var(--el-fill-color-lighter);
    font-size: 12px;
    transition: background 0.2s ease;

    &:hover {
        background: var(--el-fill-color);
    }

    &.is-open {
        background: var(--el-fill-color);
    }
}

.graph-edge-rel {
    font-weight: 700;
    color: var(--el-color-primary);
}

.graph-edge-peer {
    color: var(--el-text-color-regular);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    cursor: pointer;

    &:hover {
        color: var(--el-color-primary);
        text-decoration: underline;
    }
}

.graph-edge-act {
    flex: none;
    display: inline-flex;
    align-items: center;
    color: var(--el-text-color-secondary);
    cursor: pointer;

    &:hover {
        color: var(--el-color-primary);
    }

    &--drill:hover {
        color: var(--el-color-warning);
    }
}

.graph-edge-span {
    padding: 6px 10px;
    border-radius: 6px;
    background: var(--el-fill-color-light);
    font-size: 12px;
    line-height: 1.6;
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 180px;
    overflow: auto;
}

.graph-expand-tip {
    margin-top: 6px;
    font-size: 12px;
    line-height: 1.5;
    color: var(--el-text-color-secondary);
}

.nt-atoms,
.nt-mols {
    margin-top: 6px;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
}

.nt-sub-tag {
    padding: 1px 5px;
    border-radius: 4px;
    font-size: 9.5px;
    font-weight: 700;
    color: #fef08a;
    background: rgba(245, 158, 11, 0.24);
    border: 1px solid rgba(245, 158, 11, 0.4);

    &.is-mol {
        color: #fbcfe8;
        background: rgba(236, 72, 153, 0.22);
        border-color: rgba(236, 72, 153, 0.42);
    }
}

.nt-atom-chip {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    padding: 1px 6px;
    border-radius: 999px;
    font-size: 10px;
    color: #e0f2fe;
    background: rgba(14, 116, 144, 0.28);
    border: 1px solid rgba(56, 189, 248, 0.26);

    i {
        font-style: normal;
        font-size: 9px;
        color: #fbbf24;
    }
}

.nt-mol-text {
    font-size: 10.5px;
    color: #e2e8f0;
}

.hud-mode-tabs {
    display: flex;
    gap: 4px;
    padding: 6px 8px 0;
}

.hud-mode-tab {
    flex: 1;
    padding: 3px 4px;
    border-radius: 6px;
    border: 1px solid rgba(148, 163, 184, 0.2);
    background: rgba(15, 23, 42, 0.55);
    color: #94a3b8;
    font-size: 10.5px;
    font-weight: 600;
    cursor: pointer;
    transition: all 0.15s ease;

    &:hover {
        color: #f8fafc;
        border-color: rgba(56, 189, 248, 0.45);
    }

    &.is-on {
        color: #020617;
        background: linear-gradient(135deg, #38bdf8, #22d3ee);
        border-color: transparent;
        font-weight: 700;
    }
}

.hud-sub-metric {
    margin-left: 4px;
    font-size: 9.5px;
    color: #fbbf24;
}

.particle-atom-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 6px;
}

.particle-atom-card {
    padding: 7px 9px;
    border-radius: 6px;
    border: 1px solid var(--el-border-color-lighter);
    background: var(--el-fill-color-lighter);
    cursor: pointer;
    transition: all 0.15s ease;

    &:hover,
    &.is-active {
        border-color: var(--el-color-warning);
        background: var(--el-color-warning-light-9);
    }

    .pa-head {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 6px;
    }

    .pa-token {
        font-size: 12.5px;
        font-weight: 700;
        color: var(--el-text-color-primary);
    }

    .pa-hash {
        font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
        font-size: 10px;
        color: var(--el-text-color-secondary);
    }

    .pa-metrics {
        margin-top: 3px;
        display: flex;
        align-items: center;
        justify-content: space-between;
        font-size: 10.5px;
        color: var(--el-text-color-regular);
        font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    }

    .pa-bar-track {
        margin-top: 5px;
        height: 4px;
        border-radius: 999px;
        background: var(--el-border-color-lighter);
        overflow: hidden;
    }

    .pa-bar-fill {
        height: 100%;
        border-radius: 999px;
        background: linear-gradient(90deg, #f59e0b, #ec4899);
    }
}

.particle-mol-list {
    display: flex;
    flex-direction: column;
    gap: 7px;
}

.particle-mol-card {
    padding: 8px 10px;
    border-radius: 6px;
    border: 1px solid var(--el-border-color-lighter);
    background: var(--el-fill-color-lighter);
    font-size: 12px;

    .pm-top {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
    }

    .pm-key {
        font-size: 11px;
        font-weight: 700;
        color: var(--el-color-primary);
    }

    .pm-stat {
        font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
        font-size: 10.5px;
        color: var(--el-text-color-secondary);
    }

    .pm-text {
        margin-top: 4px;
        line-height: 1.5;
        color: var(--el-text-color-primary);
        word-break: break-word;
    }

    .pm-atoms,
    .pm-dimers {
        margin-top: 5px;
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 4px;
    }

    .pm-label {
        font-size: 10.5px;
        color: var(--el-text-color-secondary);
    }

    .pm-atom-pill {
        padding: 1px 6px;
        border-radius: 999px;
        font-size: 10.5px;
        font-weight: 600;
        color: var(--el-color-primary);
        background: var(--el-color-primary-light-9);
        border: 1px solid var(--el-color-primary-light-7);
        cursor: pointer;

        &:hover {
            background: var(--el-color-primary-light-7);
        }
    }

    .pm-dimer-peer {
        padding: 1px 6px;
        border-radius: 4px;
        font-size: 10.5px;
        color: var(--el-color-danger);
        background: var(--el-color-danger-light-9);
    }
}
</style>

<!-- 口径说明弹层（teleport 到 body，须为非 scoped 样式） -->
<style lang="scss">
.rm-help-popper {
    max-width: 92vw;
}

.rm-help-popper .rm-help-body {
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 12px;
    line-height: 1.6;
    color: var(--el-text-color-regular);
}

.rm-help-popper .rm-help-body p {
    margin: 0;
}
</style>
