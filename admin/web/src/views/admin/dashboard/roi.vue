<template>
    <div class="default-main rm-roi-monitor">
        <!-- ==================== 顶部：全景导航、账本身份与时间窗控制 ==================== -->
        <div class="roi-hero">
            <div class="hero-left">
                <div class="hero-title-row">
                    <div class="hero-badge">
                        <Icon name="lucide-trending-up" :size="20" />
                    </div>
                    <h1 class="hero-title">
                        {{ isEn ? 'Memory ROI & Cognitive Efficacy Monitor' : '记忆 ROI 与认知效能监控台（洛灵自知记忆引擎 · 回忆即记忆）' }}
                    </h1>
                    <el-popover trigger="click" placement="bottom-start" :width="480" popper-class="rm-help-popper">
                        <template #reference>
                            <button type="button" class="tip-q" :aria-label="isEn ? 'ROI methodology' : 'ROI 度量口径说明'">?</button>
                        </template>
                        <div class="rm-help-body">
                            <p v-if="isEn">
                                <strong>How Memory ROI Is Measured:</strong> Memory is recalled on demand by the agent and automatically crystallized into L3
                                recall-trail entries with explicit source citations.
                            </p>
                            <p v-else>
                                <strong>ROI 统计口径：</strong>记忆由 Agent 按需主动调用 `recall` 检索，并在命中底层 L1/L2 证据时自动沉淀为带实名引证链的 L3 认知结晶。
                            </p>
                            <p v-if="isEn">
                                This console tracks 5 signals: 1) L3 Crystallization & Citation Yield; 2) Cross-Session Memory Reuse Multiplier; 3)
                                `tokenz-r` Distillation Compression & Delivery Budget; 4) Zero-Copy Vault Footprint; 5) Per-Workspace (Locus) Coverage
                                & Recall Heat.
                            </p>
                            <p v-else>
                                本监控台统计五项指标：① L3 认知结晶与跨层引证转化；② 跨会话记忆资产复用倍数（写一次、引证 N 次）；③ `tokenz-r`
                                干句提纯压缩与单次交付预算；④ 零拷贝指针存储占比；⑤ 多工作区（Locus）冷热覆盖与检索活跃度。
                            </p>
                        </div>
                    </el-popover>
                    <el-tag size="small" :type="vault ? 'success' : 'info'" effect="plain">
                        {{ vault ? (isEn ? 'Vault Telemetry: LIVE' : '金库实证遥测：在线') : isEn ? 'Loading...' : '加载中...' }}
                    </el-tag>
                    <el-tag size="small" :type="ledgerAvailable ? 'primary' : 'warning'" effect="plain">
                        {{
                            ledgerAvailable
                                ? isEn
                                    ? `Recall Ledger: Verified (${counts?.total ?? 0})`
                                    : `调用账本：已校验 (${counts?.total ?? 0} 条)`
                                : isEn
                                  ? `Recall Ledger: ${reasonLabel(facts.reason)}`
                                  : `调用账本：${reasonLabel(facts.reason)}`
                        }}
                    </el-tag>
                </div>
                <div class="hero-meta">
                    <span class="meta-item">
                        {{ isEn ? 'Architecture:' : '引擎架构：' }}
                        <strong>Rolling Autonoetic Memory Engine · 0-Vector · 0-LLM · tokenz-r (Byte-Level Shannon Information)</strong>
                    </span>
                    <span class="meta-sep">·</span>
                    <span class="meta-item">
                        {{ isEn ? 'Scope:' : '当前作用域：' }}
                        <strong class="font-mono">{{ currentScope }}</strong>
                    </span>
                    <span v-if="selectedLocus" class="meta-sep">·</span>
                    <span v-if="selectedLocus" class="meta-item is-filter" @click="selectedLocus = ''">
                        {{ isEn ? 'Top cited units filter:' : '高引用单元筛选：' }}
                        <strong class="font-mono">{{ selectedLocus }}</strong> ×
                    </span>
                    <span class="meta-sep">·</span>
                    <span class="meta-item">
                        {{ isEn ? 'Updated:' : '最近刷新：' }}
                        <span class="font-mono">{{ lastLoadedText }}</span>
                    </span>
                </div>
            </div>

            <div class="hero-right">
                <el-select v-model="currentScope" size="small" style="width: 130px" @change="onScopeChange()">
                    <el-option label="default" value="default" />
                    <el-option label="mem:self" value="mem:self" />
                </el-select>
                <el-select v-model="currentHarness" size="small" style="width: 142px" @change="onHarnessChange()">
                    <el-option v-for="h in harnessOptions" :key="h.value" :label="h.label" :value="h.value" />
                </el-select>
                <el-select v-model="autoRefreshKey" size="small" style="width: 118px" @change="applyAutoRefresh">
                    <el-option v-for="opt in autoRefreshOptions" :key="opt.key" :label="opt.label" :value="opt.key" />
                </el-select>
                <el-button
                    size="small"
                    type="success"
                    plain
                    @click="
                        router.push({
                            path: '/admin/plugin/rolling-memory/recall',
                            query: { scope: currentScope, workspace: selectedLocus === '(global / unscoped)' ? '' : selectedLocus },
                        })
                    "
                >
                    <Icon name="el-search" :size="13" style="margin-right: 4px" />
                    {{ isEn ? 'Recall Debugger' : '检索调试' }}
                </el-button>
                <el-button size="small" plain @click="router.push('/admin/dashboard/artifacts')">
                    {{ isEn ? 'Memory Console' : '记忆控制台' }}
                </el-button>
                <el-button size="small" type="primary" :loading="loading" @click="load()">
                    <Icon name="el-refresh" :size="13" style="margin-right: 4px" />
                    {{ isEn ? 'Refresh' : '刷新' }}
                    <template v-if="loading"> ({{ loadPct }}%)</template>
                </el-button>
            </div>
        </div>

        <!-- ==================== 滚动监控窗切换带（1h / 24h / 7d / 30d / All-Time） ==================== -->
        <div class="window-strip">
            <div class="ws-head">
                <span class="ws-title">
                    <Icon name="el-clock" :size="14" />
                    {{ isEn ? 'Rolling Telemetry Windows' : '滚动实证监控窗' }}
                </span>
                <span class="ws-sub">
                    {{
                        isEn
                            ? 'Directly aggregated from vault timestamps & recall ledger — click any window to inspect its slice.'
                            : '由本地金库真实时间戳与调用账本直接聚合 —— 点击任意时间窗切换切片口径'
                    }}
                </span>
            </div>
            <div class="ws-grid">
                <button
                    v-for="w in displayWindows"
                    :key="w.key"
                    type="button"
                    class="ws-card"
                    :class="{ 'is-active': w.key === monitorWindow }"
                    @click="monitorWindow = w.key"
                >
                    <div class="ws-card-top">
                        <span class="ws-chip">{{ windowMetaOf(w.key).label }}</span>
                        <span class="ws-span font-mono">{{ windowMetaOf(w.key).span }}</span>
                    </div>
                    <div class="ws-card-main font-mono">
                        <span class="text-primary">+{{ w.noetic_crystallized }} {{ isEn ? 'Noetic' : '结晶' }}</span>
                        <span class="ws-dot">·</span>
                        <span class="text-success">{{ w.citation_links }} {{ isEn ? 'Citations' : '次引证' }}</span>
                    </div>
                    <div class="ws-card-foot font-mono">
                        <span>L2 +{{ formatTok(w.episodic_added) }} · L1 +{{ formatTok(w.background_added) }}</span>
                        <span>{{ formatTok(w.delivered_tokens) }} tok</span>
                    </div>
                </button>
            </div>
        </div>

        <!-- ==================== 第一屏：4 大工程师核心 ROI 指标卡 ==================== -->
        <div class="kpi-grid4">
            <!-- 卡片 1：认知结晶与跨层实名引证转化 (Recall -> L3 Noetic Yield) -->
            <!-- 卡片 1：认知结晶与跨层实名引证转化 (Recall -> L3 Noetic Yield & Intersection Adoption) -->
            <div class="roi-card is-accent">
                <div class="rc-head">
                    <span class="rc-title">
                        {{ isEn ? 'L3 Crystallization & Adoption Audit' : '认知结晶与事后采纳交集审计' }}
                        <el-popover trigger="click" placement="top" :width="440" popper-class="rm-help-popper">
                            <template #reference>
                                <button type="button" class="tip-q">?</button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    Every time an agent `recall` hits underlying L1/L2 evidence, it crystallizes an L3 entry with explicit source unit
                                    citations, then audits whether novel file paths, symbols, or high-information tokens (excluding query echoes) appear in
                                    subsequent tool inputs (`Read`/`Edit`/`Bash`) or replies.
                                </p>
                                <p v-else>
                                    当 Agent 主动发起 `recall` 并命中底层 L1/L2 证据时，自动提纯沉淀为带实名引证链的 L3
                                    条目；同时通过纯数学反回声交集审计（0 模型开销），检验召回净增的文件路径、符号或高信息量特征是否出现在 Agent
                                    随后的工具调用（`Read`/`Edit`/`Bash`）或回复中。
                                </p>
                            </div>
                        </el-popover>
                    </span>
                    <Icon name="el-magic-stick" :size="16" class="text-primary" />
                </div>
                <div class="rc-val font-mono text-primary">
                    {{ activeWindow.noetic_crystallized }}
                    <span class="rc-unit">{{ isEn ? 'crystallized' : '次认知结晶' }}</span>
                    <span class="rc-pill is-success">{{ activeWindow.citation_links }} {{ isEn ? 'citations' : '条引证链' }}</span>
                    <span v-if="windowAuditedRecalls > 0" class="rc-pill is-warn">
                        {{ isEn ? 'Adopted' : '实质采纳' }} {{ windowAdoptionRatePct }}%
                    </span>
                </div>
                <div class="rc-sub font-mono">
                    <span v-if="windowAuditedRecalls > 0">
                        {{ isEn ? 'Adopted:' : '采纳审计：' }}
                        <strong class="text-success">{{ windowAdoptedRecalls }}/{{ windowAuditedRecalls }}</strong>
                        （{{ isEn ? 'Tool' : '工具直达' }} <strong>{{ windowAdoptedTools }}</strong> ·
                        {{ isEn ? 'Context' : '事实采纳' }} <strong>{{ windowAdoptedContexts }}</strong>）
                    </span>
                    <span v-else>
                        {{ isEn ? 'All-time:' : '全量累计：' }}
                        <strong>{{ vault?.noetic_units ?? 0 }}</strong> {{ isEn ? 'active' : '有效结晶' }} /
                        <strong>{{ vault?.total_citation_links ?? 0 }}</strong> {{ isEn ? 'links' : '次引证' }}
                    </span>
                    <span>
                        {{ isEn ? 'Avg' : '均引' }} <strong>{{ vault?.avg_citations_per_recall ?? 0 }}</strong>
                        {{ isEn ? 'units/recall' : '单元/次' }}
                    </span>
                </div>
            </div>

            <!-- 卡片 2：记忆资产复用倍数 (Memory Reuse Multiplier) -->
            <div class="roi-card">
                <div class="rc-head">
                    <span class="rc-title">
                        {{ isEn ? 'Memory Reuse Multiplier (Write Once, Cite N×)' : '记忆资产复用倍数（写一次 · 引证 N 次）' }}
                        <el-popover trigger="click" placement="top" :width="400" popper-class="rm-help-popper">
                            <template #reference>
                                <button type="button" class="tip-q">?</button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    Reuse Multiplier = Total Citation Links ÷ Unique Cited L1/L2 Units. High reuse proves that stored architectural
                                    rulings and recipes are repeatedly recalled across sessions instead of sitting idle.
                                </p>
                                <p v-else>
                                    复用倍数 = 累计跨层引证总次数 ÷
                                    被命中的独立底层单元数。高复用率直接证明沉淀的架构裁定与排障配方在后续多个会话中被反复召回消费，而非一次性僵尸数据。
                                </p>
                            </div>
                        </el-popover>
                    </span>
                    <Icon name="el-circle-check" :size="16" class="text-success" />
                </div>
                <div class="rc-val font-mono text-success">
                    {{ vault?.avg_reuse_multiplier ?? 0 }}×
                    <span class="rc-unit">{{ isEn ? 'avg reuse' : '平均复用' }}</span>
                    <span class="rc-pill is-warn">{{ isEn ? 'Max' : '最高单条' }} {{ vault?.max_reuse_count ?? 0 }}×</span>
                </div>
                <div class="rc-sub font-mono">
                    <span>
                        {{ isEn ? 'Unique hit units:' : '独立命中单元：' }}
                        <strong>{{ vault?.unique_cited_units ?? 0 }}</strong>
                    </span>
                    <span>
                        {{ isEn ? 'Multi-hit golden units (≥2×):' : '高复用金牌单元 (≥2×)：' }}
                        <strong class="text-success">{{ vault?.reused_units_count ?? 0 }}</strong>
                    </span>
                </div>
            </div>

            <!-- 卡片 3：召回干句精炼度与单次 Token 预算 (tokenz-r Delivery Efficiency) -->
            <div class="roi-card">
                <div class="rc-head">
                    <span class="rc-title">
                        {{ isEn ? 'Delivery Budget & Distillation (tokenz-r)' : '召回交付预算与干句提纯 (tokenz-r)' }}
                        <el-popover trigger="click" placement="top" :width="420" popper-class="rm-help-popper">
                            <template #reference>
                                <button type="button" class="tip-q">?</button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    Measured with the unified `tokenz-r` tokenizer. Instead of dumping thousands of raw transcript
                                    turns into the context window, each recall delivers only distilled dry sentences (~hundreds of tokens).
                                </p>
                                <p v-else>
                                    采用全域统一分词标准 `tokenz-r`（基于字节香农信息熵的确定性映射）。每次召回从全库数千记忆单元中仅提取目标单元的分形干句交付给
                                    Agent，以数百 Token 的极小预算完成精准寻址。
                                </p>
                            </div>
                        </el-popover>
                    </span>
                    <Icon name="el-coin" :size="16" class="text-warning" />
                </div>
                <div class="rc-val font-mono">
                    ~{{ vault?.avg_delivered_tokens ?? 0 }}
                    <span class="rc-unit">{{ isEn ? 'tokens / recall' : 'Tokens / 次召回' }}</span>
                    <span v-if="(vault?.distillation_saved_pct ?? 0) > 0" class="rc-pill is-success">
                        {{ isEn ? 'Distilled' : '干句提纯' }} -{{ vault?.distillation_saved_pct }}%
                    </span>
                </div>
                <div class="rc-sub font-mono">
                    <span>
                        {{ isEn ? 'Window delivered:' : '窗内交付：' }}
                        <strong>{{ formatTok(activeWindow.delivered_tokens) }}</strong> tok
                    </span>
                    <span>
                        {{ isEn ? 'All-time delivered:' : '累计交付：' }}
                        <strong>{{ formatTok(vault?.total_delivered_tokens ?? 0) }}</strong> tok
                    </span>
                </div>
            </div>

            <!-- 卡片 4：零拷贝明文金库与三层认知资产底座 (Zero-Copy Vault Footprint) -->
            <div class="roi-card">
                <div class="rc-head">
                    <span class="rc-title">
                        {{ isEn ? 'Zero-Copy Vault & 3-Layer Footprint' : '零拷贝金库与三层认知底座' }}
                        <el-popover trigger="click" placement="top" :width="420" popper-class="rm-help-popper">
                            <template #reference>
                                <button type="button" class="tip-q">?</button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    L2 Episodic memories store compact `@ref:` pointers to the host session transcript and hydrate live on demand,
                                    avoiding multi-MB text duplication in SQLite while keeping 100% plaintext sovereignty locally (0 plaintext
                                    egress).
                                </p>
                                <p v-else>
                                    L2 情景记忆采用 `@ref:` 零拷贝指针直接指向宿主会话原件并按需实时水合，避免在本地 SQLite 重复拷贝数 MB
                                    会话原文；本地明文模式（`local_plain`）100% 离线运行，零云端外呼、不发脉冲。
                                </p>
                            </div>
                        </el-popover>
                    </span>
                    <Icon name="lucide-database" :size="16" class="text-primary" />
                </div>
                <div class="rc-val font-mono">
                    {{ vault?.episodic_pointer_ratio_pct ?? 0 }}%
                    <span class="rc-unit">{{ isEn ? 'zero-copy @ref' : '零拷贝指针占比' }}</span>
                    <span class="rc-pill">{{ vault?.total_units ?? 0 }} {{ isEn ? 'units' : '总单元' }}</span>
                </div>
                <div class="rc-sub font-mono">
                    <span>
                        L1 <strong>{{ vault?.background_chunks ?? 0 }}</strong> ({{ vault?.background_files ?? 0 }}f) · L2
                        <strong>{{ vault?.episodic_docs ?? 0 }}</strong> · L3 <strong>{{ vault?.noetic_units ?? 0 }}</strong>
                    </span>
                    <span>
                        <strong>{{ vault?.episodic_cells ?? 0 }}</strong> {{ isEn ? 'L2 memory addresses' : 'L2 记忆地址' }}
                    </span>
                </div>
            </div>
        </div>

        <!-- ==================== 第二屏：近 14 天沉淀×结晶脉冲趋势 + 多工作区覆盖与冷热诊断矩阵 ==================== -->
        <div class="roi-mid-grid">
            <!-- 左：近 14 天每日三层沉淀与认知结晶双轨趋势 -->
            <div class="roi-panel">
                <div class="panel-head">
                    <div>
                        <span class="panel-title">
                            {{ isEn ? '14-Day Ingestion & L3 Crystallization Pulse' : '近 14 天记忆沉淀 × L3 认知结晶脉冲趋势' }}
                        </span>
                        <span class="panel-sub">
                            {{
                                isEn
                                    ? 'Daily L1/L2 memory growth vs active L3 recall crystallizations & citation links'
                                    : '按自然日对比 L1/L2 记忆入库量与 Agent 主动召回产生的 L3 认知结晶及引证数'
                            }}
                        </span>
                    </div>
                    <div class="legend-inline font-mono">
                        <span class="lg-item"><i class="lg-dot is-ep"></i>L2 {{ isEn ? 'Episodic' : '情景' }}</span>
                        <span class="lg-item"><i class="lg-dot is-bg"></i>L1 {{ isEn ? 'Background' : '背景' }}</span>
                        <span class="lg-item"><i class="lg-dot is-nt"></i>L3 {{ isEn ? 'Noetic (Citations)' : '结晶 (引证)' }}</span>
                    </div>
                </div>

                <div class="daily-bars">
                    <div v-for="day in dailySeries" :key="day.date" class="day-col">
                        <div class="day-nums font-mono">
                            <span v-if="day.noetic_count > 0" class="dn-nt" :title="`${day.citation_links} citations`">
                                ★{{ day.noetic_count }}
                            </span>
                            <span v-else class="dn-zero">·</span>
                        </div>
                        <div class="day-track">
                            <div
                                class="bar-seg is-nt"
                                :style="{ height: dayBarHeight(day.noetic_count * 12, maxDailyScale) + '%' }"
                                :title="`${day.date}: L3 Noetic ${day.noetic_count} (${day.citation_links} citations)`"
                            ></div>
                            <div
                                class="bar-seg is-bg"
                                :style="{ height: dayBarHeight(day.background_count, maxDailyScale) + '%' }"
                                :title="`${day.date}: L1 Background ${day.background_count}`"
                            ></div>
                            <div
                                class="bar-seg is-ep"
                                :style="{ height: dayBarHeight(day.episodic_count, maxDailyScale) + '%' }"
                                :title="`${day.date}: L2 Episodic ${day.episodic_count}`"
                            ></div>
                        </div>
                        <div class="day-total font-mono">{{ day.episodic_count + day.background_count }}</div>
                        <div class="day-label font-mono">{{ day.date.slice(5) }}</div>
                    </div>
                </div>
            </div>

            <!-- 右：多工作区 (Locus) 覆盖与冷热诊断矩阵 -->
            <div class="roi-panel">
                <div class="panel-head">
                    <div>
                        <span class="panel-title">
                            {{ isEn ? 'Workspace (Locus) Coverage & Recall Heat Matrix' : '多工作区 (Locus) 覆盖与召回冷热诊断矩阵' }}
                        </span>
                        <span class="panel-sub">
                            {{
                                isEn
                                    ? 'Click a workspace row to filter Top Cited Units & Crystallization Stream below'
                                    : '诊断各项目记忆沉淀与召回活跃度 —— 点击任意工作区联动筛选下方榜单与证据流'
                            }}
                        </span>
                    </div>
                    <el-button v-if="selectedLocus" size="small" link type="primary" @click="selectedLocus = ''">
                        {{ isEn ? 'Clear Filter' : '清除筛选' }}
                    </el-button>
                </div>

                <el-table
                    :data="locusMatrix"
                    size="small"
                    class="roi-table"
                    max-height="320"
                    :row-class-name="locusRowClass"
                    :empty-text="isEn ? 'No records' : '暂无记录'"
                    @row-click="toggleLocusFilter"
                >
                    <el-table-column :label="isEn ? 'Workspace (Locus)' : '工作区 (Locus)'" min-width="150">
                        <template #default="{ row }">
                            <span class="font-mono locus-name">{{ row.locus }}</span>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Status' : '冷热诊断'" width="105">
                        <template #default="{ row }">
                            <el-tag size="small" :type="row.status === 'hot' ? 'success' : row.status === 'warm' ? 'warning' : 'info'" effect="plain">
                                {{
                                    row.status === 'hot'
                                        ? isEn
                                            ? '🔥 Active Recall'
                                            : '🔥 高频召回'
                                        : row.status === 'warm'
                                          ? isEn
                                              ? '⚡ Ingesting'
                                              : '⚡ 近期沉淀'
                                          : isEn
                                            ? '💤 Uncited'
                                            : '💤 待召回激活'
                                }}
                            </el-tag>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'L2 Episodic' : 'L2 情景'" width="85" align="right">
                        <template #default="{ row }">
                            <span class="font-mono">{{ row.episodic_count }}</span>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'L1 Chunks' : 'L1 背景'" width="85" align="right">
                        <template #default="{ row }">
                            <span class="font-mono">{{ row.background_count }}</span>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Cited Units' : '命中单元'" width="85" align="right">
                        <template #default="{ row }">
                            <strong class="font-mono" :class="row.cited_unit_count > 0 ? 'text-success' : ''">
                                {{ row.cited_unit_count }}
                            </strong>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Citation Hits' : '累计引证'" width="90" align="right">
                        <template #default="{ row }">
                            <strong class="font-mono" :class="row.citation_hits > 0 ? 'text-primary' : ''"> {{ row.citation_hits }}× </strong>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Adopted Hits' : '实质采纳'" width="90" align="right">
                        <template #default="{ row }">
                            <strong class="font-mono" :class="row.adopted_hits > 0 ? 'text-success' : ''"> {{ row.adopted_hits }}× </strong>
                        </template>
                    </el-table-column>
                </el-table>
            </div>
        </div>

        <!-- ==================== 第三屏：金牌高复用记忆单元榜 (Top Cited Units) + L3 认知结晶与调用诊断台 ==================== -->
        <div class="roi-bottom-grid">
            <!-- 左：金牌高复用记忆排行榜 -->
            <div class="roi-panel">
                <div class="panel-head">
                    <div>
                        <span class="panel-title">
                            {{ isEn ? 'Top Cited Memory Units (High-ROI Assets)' : '金牌高复用记忆排行榜（真正在干活的记忆单元）' }}
                        </span>
                        <span class="panel-sub">
                            {{
                                isEn
                                    ? 'Underlying L1/L2 units ranked by L3 citation frequency — scroll inside the list; click a row to inspect which queries cited it'
                                    : '按被 Agent 实名引证次数排序的底层 L1/L2 记忆单元 —— 列表内滚动；点击任意行查看引证溯源链'
                            }}
                        </span>
                    </div>
                    <div class="panel-head-actions">
                        <el-tag v-if="selectedLocus" size="small" type="primary" effect="plain" closable @close="selectedLocus = ''">
                            {{ selectedLocus }}
                        </el-tag>
                        <span class="badge-count font-mono">{{ filteredTopUnits.length }} {{ isEn ? 'units' : '条' }}</span>
                    </div>
                </div>

                <el-table
                    :data="filteredTopUnits"
                    size="small"
                    class="roi-table is-clickable"
                    max-height="460"
                    :empty-text="isEn ? 'No records' : '暂无记录'"
                    @row-click="openUnitDrawer"
                >
                    <el-table-column prop="citation_count" :label="isEn ? 'Reused' : '引证复用'" width="106" align="center" sortable>
                        <template #default="{ row }">
                            <div class="unit-cell" style="align-items: center">
                                <span class="cite-pill font-mono" :class="row.citation_count >= 3 ? 'is-gold' : 'is-normal'">
                                    {{ row.citation_count }}×
                                </span>
                                <span v-if="(row.adopted_count ?? 0) > 0" class="font-mono text-success" style="font-size: 11px">
                                    ✓{{ row.adopted_count }} {{ isEn ? 'hit' : '采纳' }}
                                </span>
                            </div>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Unit / Layer' : '单元 ID / 图层'" width="185">
                        <template #default="{ row }">
                            <div class="unit-cell">
                                <span class="font-mono unit-id">{{ row.unit_id }}</span>
                                <div class="unit-tags">
                                    <el-tag size="small" :type="row.layer === 'background' ? 'warning' : 'primary'" effect="plain">
                                        {{ row.layer === 'background' ? 'L1' : 'L2' }}
                                    </el-tag>
                                    <span v-if="row.locus" class="unit-locus font-mono">{{ row.locus }}</span>
                                </div>
                            </div>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Evidence Preview / Pointer' : '干句摘要 / 零拷贝指针'" min-width="220">
                        <template #default="{ row }">
                            <MemoryHoverPreview
                                :text="row.preview"
                                :title="row.unit_id"
                                :subtitle="row.locus || ''"
                                :hint="{ layer: row.layer }"
                            >
                                <div class="preview-line">
                                    <el-tag v-if="row.is_zero_copy_ref" size="small" type="info" effect="plain" style="margin-right: 6px"> @ref </el-tag>
                                    <span class="preview-text">{{ row.preview }}</span>
                                </div>
                                <div v-if="row.recent_queries?.length" class="query-hint">
                                    {{ isEn ? 'Cited by:' : '近期引证查询：' }}{{ row.recent_queries[0] }}
                                </div>
                            </MemoryHoverPreview>
                        </template>
                    </el-table-column>
                    <el-table-column prop="last_cited_at" :label="isEn ? 'Last Cited' : '最近引证'" width="145" align="right" sortable>
                        <template #default="{ row }">
                            <span class="font-mono text-secondary">{{ formatShortTime(row.last_cited_at) }}</span>
                        </template>
                    </el-table-column>
                </el-table>
            </div>

            <!-- 右：L3 认知结晶流 & 实时 MCP 调用账本双 Tab 诊断台 -->
            <div class="roi-panel">
                <div class="panel-head">
                    <div class="tab-switches">
                        <button type="button" class="tab-btn" :class="{ 'is-active': activeTab === 'noetic' }" @click="activeTab = 'noetic'">
                            {{ isEn ? 'L3 Crystallization Stream' : 'L3 认知结晶与证据链' }}
                            <span class="tab-cnt font-mono">{{ filteredNoeticStream.length }}</span>
                        </button>
                        <button type="button" class="tab-btn" :class="{ 'is-active': activeTab === 'ledger' }" @click="activeTab = 'ledger'">
                            {{ isEn ? 'Live MCP Recall Ledger' : 'MCP 实时调用账本与盲区' }}
                            <span class="tab-cnt font-mono">{{ recentEvents.length }}</span>
                        </button>
                    </div>
                    <el-input
                        v-if="activeTab === 'noetic'"
                        v-model="searchQuery"
                        size="small"
                        clearable
                        :placeholder="isEn ? 'Filter query or unit ID...' : '搜索查询词或引证单元 ID...'"
                        style="width: 210px"
                    />
                </div>

                <!-- Tab 1: L3 认知结晶与证据链明细 -->
                <el-table
                    v-if="activeTab === 'noetic'"
                    :data="filteredNoeticStream"
                    size="small"
                    class="roi-table is-clickable"
                    max-height="460"
                    :empty-text="isEn ? 'No records' : '暂无记录'"
                    @row-click="openNoeticDrawer"
                >
                    <el-table-column :label="isEn ? 'Recall Query (Cue)' : 'Agent 召回查询 (Cue)'" min-width="210">
                        <template #default="{ row }">
                            <MemoryHoverPreview
                                :text="row.answer_text || (row.cited_sentences || []).join('\n')"
                                :question="row.query_text"
                                :title="row.noetic_id"
                                :subtitle="row.address_uri || ''"
                                :hint="{ layer: 'noetic' }"
                            >
                                <div class="query-main">{{ row.query_text }}</div>
                                <div class="query-sub font-mono">
                                    <span>{{ row.noetic_id }}</span>
                                    <span v-if="row.assistant_id && row.assistant_id !== 'default'" class="text-primary">· {{ row.assistant_id }}</span>
                                    <span v-if="row.address_uri">· {{ row.address_uri }}</span>
                                </div>
                            </MemoryHoverPreview>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Citations' : '引证单元'" width="90" align="center">
                        <template #default="{ row }">
                            <el-tag size="small" type="success" effect="plain" class="font-mono">
                                {{ row.citation_count }} {{ isEn ? 'units' : '单元' }}
                            </el-tag>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Adoption Audit' : '事后采纳审计'" width="155" align="left">
                        <template #default="{ row }">
                            <div class="adopt-cell">
                                <el-tag
                                    size="small"
                                    :type="
                                        row.adoption_outcome === 'adopted_tool'
                                            ? 'success'
                                            : row.adoption_outcome === 'adopted_context'
                                              ? 'primary'
                                              : 'info'
                                    "
                                    effect="plain"
                                >
                                    {{ adoptionLabel(row.adoption_outcome) }}
                                </el-tag>
                                <span v-if="row.matched_anchors?.length" class="adopt-anchor font-mono" :title="row.matched_anchors.join(', ')">
                                    {{ row.matched_anchors[0] }}
                                </span>
                            </div>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Delivered' : '交付 Token'" width="95" align="right">
                        <template #default="{ row }">
                            <span class="font-mono">{{ row.delivered_tokens }} tok</span>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Time' : '结晶时间'" width="145" align="right">
                        <template #default="{ row }">
                            <span class="font-mono text-secondary">{{ formatShortTime(row.occurred_at) }}</span>
                        </template>
                    </el-table-column>
                </el-table>

                <!-- Tab 2: MCP 实时调用账本与空返回盲区 -->
                <div v-else>
                    <div v-if="!ledgerAvailable" class="ledger-notice">
                        {{ reasonDetail(facts.reason) }}
                    </div>
                    <el-table :data="recentEvents" size="small" class="roi-table" max-height="460" :empty-text="isEn ? 'No records' : '暂无记录'">
                        <el-table-column :label="isEn ? 'Observed At' : '调用时间'" width="155">
                            <template #default="{ row }">
                                <span class="font-mono">{{ formatShortTime(row.observed_at) }}</span>
                            </template>
                        </el-table-column>
                        <el-table-column prop="action" :label="isEn ? 'Action' : '动作'" width="105" />
                        <el-table-column :label="isEn ? 'Outcome' : '结果'" width="110">
                            <template #default="{ row }">
                                <el-tag
                                    size="small"
                                    :type="
                                        row.outcome === 'hits' || row.outcome === 'success'
                                            ? 'success'
                                            : row.outcome === 'empty'
                                              ? 'warning'
                                              : 'danger'
                                    "
                                    effect="plain"
                                >
                                    {{ row.outcome }}
                                </el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'Channel / Hook' : '通道 / 模式'" min-width="150">
                            <template #default="{ row }">
                                <span class="font-mono">{{ row.harness ? `${row.harness} · ` : '' }}{{ row.hook }}</span>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'Delivered Bytes' : '交付字节'" width="120" align="right">
                            <template #default="{ row }">
                                <span class="font-mono">{{ row.text_bytes === null ? '—' : formatBytes(row.text_bytes) }}</span>
                            </template>
                        </el-table-column>
                    </el-table>
                </div>
            </div>
        </div>

        <!-- ==================== 证据链溯源详情抽屉（支持查看金牌单元被引证历史 或 L3 结晶完整干句） ==================== -->
        <el-drawer
            v-model="drawerVisible"
            :title="
                drawerMode === 'unit'
                    ? isEn
                        ? 'Memory Unit Citation Provenance'
                        : '底层记忆单元 · 跨会话引证溯源链'
                    : isEn
                      ? 'L3 Noetic Crystallization Evidence'
                      : 'L3 认知结晶 · 实名引证证据链详情'
            "
            size="760px"
        >
            <div v-if="drawerMode === 'unit' && selectedUnit" class="drawer-body">
                <div class="db-kv">
                    <span class="db-k">{{ isEn ? 'Unit ID' : '记忆单元 ID' }}</span>
                    <span class="db-v font-mono">{{ selectedUnit.unit_id }}</span>
                </div>
                <div class="db-kv">
                    <span class="db-k">{{ isEn ? 'Layer & Workspace' : '所属图层与工作区' }}</span>
                    <span class="db-v font-mono"> {{ selectedUnit.layer.toUpperCase() }} · {{ selectedUnit.locus || '(global / unscoped)' }} </span>
                </div>
                <div class="db-kv">
                    <span class="db-k">{{ isEn ? 'Reuse & Adoption Count' : '累计被引证与实质采纳' }}</span>
                    <span class="db-v font-mono text-success">
                        <strong>{{ selectedUnit.citation_count }}×</strong>
                        <span v-if="(selectedUnit.adopted_count ?? 0) > 0">
                            · {{ isEn ? `Adopted ${selectedUnit.adopted_count}×` : `促成实质采纳 ${selectedUnit.adopted_count}×` }}
                        </span>
                        （{{ isEn ? 'Last cited:' : '最近引证：' }}{{ formatTime(selectedUnit.last_cited_at) }}）
                    </span>
                </div>
                <div v-if="selectedUnit.address_uri" class="db-kv">
                    <span class="db-k">{{ isEn ? 'Memory Address' : '记忆地址' }}</span>
                    <span class="db-v font-mono">{{ selectedUnit.address_uri }}</span>
                </div>
                <div class="db-section">
                    <div class="db-sec-title">
                        {{ isEn ? 'Full Content (hydrated from harness)' : '完整原文（已从宿主会话实时水合）' }}
                        <el-tag v-if="selectedUnit.is_hydrated" size="small" type="success" effect="plain">
                            {{ isEn ? 'hydrated' : '已水合' }}
                        </el-tag>
                        <el-tag v-else-if="selectedUnit.is_zero_copy_ref" size="small" type="warning" effect="plain">
                            {{ isEn ? 'host transcript pruned' : '宿主原文已清理' }}
                        </el-tag>
                    </div>
                    <div v-if="selectedUnit.is_zero_copy_ref && !selectedUnit.is_hydrated" class="db-box">
                        {{
                            isEn
                                ? 'This unit only stores a zero-copy pointer; the host transcript it referenced has been pruned, so the original text can no longer be hydrated.'
                                : '该单元仅存零拷贝指针，其指向的宿主会话原文已被清理，无法再水合出原文（并非界面截断）。'
                        }}
                    </div>
                    <MarkdownPreview :text="selectedUnit.full_text || selectedUnit.preview" />
                </div>
                <div class="db-section">
                    <div class="db-sec-title">
                        {{ isEn ? 'Citations (query + crystallized answer)' : '引证溯源记录（查询 + 结晶答案）' }}
                        ({{ selectedUnit.citations?.length || 0 }})
                    </div>
                    <div v-if="!selectedUnit.citations?.length" class="db-box">
                        {{ isEn ? 'No citation records.' : '暂无引证记录。' }}
                    </div>
                    <div v-for="(cite, idx) in selectedUnit.citations" :key="idx" class="cite-record">
                        <div class="cite-head font-mono">
                            <span class="text-secondary">{{ formatTime(cite.occurred_at) }}</span>
                            <span class="text-secondary">{{ cite.noetic_id }}</span>
                            <span v-if="cite.assistant_id && cite.assistant_id !== 'default'" class="text-primary">{{ cite.assistant_id }}</span>
                            <el-tag
                                v-if="cite.adoption_outcome"
                                size="small"
                                :type="
                                    cite.adoption_outcome === 'adopted_tool'
                                        ? 'success'
                                        : cite.adoption_outcome === 'adopted_context'
                                          ? 'primary'
                                          : 'info'
                                "
                                effect="plain"
                            >
                                {{ adoptionLabel(cite.adoption_outcome) }}
                            </el-tag>
                        </div>
                        <div class="db-box">{{ cite.query_text }}</div>
                        <div v-if="cite.matched_anchors?.length" class="db-tags">
                            <el-tag v-for="(ma, mIdx) in cite.matched_anchors" :key="mIdx" size="small" type="success" effect="plain" class="font-mono">
                                {{ ma }}
                            </el-tag>
                        </div>
                        <MarkdownPreview :text="cite.answer_text" />
                    </div>
                </div>
            </div>

            <div v-else-if="drawerMode === 'noetic' && selectedNoetic" class="drawer-body">
                <div class="db-kv">
                    <span class="db-k">{{ isEn ? 'Noetic ID' : '结晶单元 ID' }}</span>
                    <span class="db-v font-mono">{{ selectedNoetic.noetic_id }}</span>
                </div>
                <div class="db-kv">
                    <span class="db-k">{{ isEn ? 'Crystallized At' : '结晶时间' }}</span>
                    <span class="db-v font-mono">{{ formatTime(selectedNoetic.occurred_at) }}</span>
                </div>
                <div class="db-kv">
                    <span class="db-k">{{ isEn ? 'Delivered Budget' : '交付 Token 预算 (tokenz-r)' }}</span>
                    <span class="db-v font-mono text-primary">{{ selectedNoetic.delivered_tokens }} Tokens</span>
                </div>
                <div class="db-kv">
                    <span class="db-k">{{ isEn ? 'Post-Recall Adoption Audit' : '事后交集采纳审计（反回声）' }}</span>
                    <span class="db-v font-mono">
                        <el-tag
                            size="small"
                            :type="
                                selectedNoetic.adoption_outcome === 'adopted_tool'
                                    ? 'success'
                                    : selectedNoetic.adoption_outcome === 'adopted_context'
                                      ? 'primary'
                                      : 'info'
                            "
                            effect="plain"
                        >
                            {{ adoptionLabel(selectedNoetic.adoption_outcome) }}
                        </el-tag>
                        <span v-if="(selectedNoetic.echo_excluded_count ?? 0) > 0" class="text-secondary" style="margin-left: 8px">
                            （{{ isEn ? `Echo stripped: ${selectedNoetic.echo_excluded_count}` : `已剥离查询回声: ${selectedNoetic.echo_excluded_count} 项` }}）
                        </span>
                    </span>
                </div>
                <div v-if="selectedNoetic.matched_anchors?.length" class="db-section">
                    <div class="db-sec-title">
                        {{ isEn ? 'Matched Novel Anchors in Subsequent Tool / Reply' : '后续工具调用 / 回复中命中的净增记忆锚点' }}
                        ({{ selectedNoetic.matched_anchors.length }})
                    </div>
                    <div class="db-tags">
                        <el-tag v-for="(anchor, aIdx) in selectedNoetic.matched_anchors" :key="aIdx" size="small" type="success" effect="plain" class="font-mono">
                            {{ anchor }}
                        </el-tag>
                    </div>
                </div>
                <div v-if="selectedNoetic.address_uri" class="db-kv">
                    <span class="db-k">{{ isEn ? 'Memory Address' : '记忆地址' }}</span>
                    <span class="db-v font-mono">{{ selectedNoetic.address_uri }}</span>
                </div>
                <div class="db-section">
                    <div class="db-sec-title">{{ isEn ? 'Recall Query (Cue)' : '召回查询词 (Cue)' }}</div>
                    <div class="db-box">{{ selectedNoetic.query_text }}</div>
                </div>
                <div class="db-section">
                    <div class="db-sec-title">
                        {{ isEn ? 'Cited Underlying Units (full content)' : '实名引证的底层记忆单元（完整原文）' }}
                        ({{ selectedNoetic.cited_unit_ids.length }})
                    </div>
                    <div v-if="!selectedNoetic.cited_units?.length" class="db-box">
                        {{ isEn ? 'No cited unit details.' : '暂无被引证单元详情。' }}
                    </div>
                    <div v-for="unit in selectedNoetic.cited_units" :key="unit.unit_id" class="cite-record">
                        <div class="cite-head font-mono">
                            <el-tag size="small" :type="unit.layer === 'background' ? 'warning' : 'primary'" effect="plain">
                                {{ unit.layer === 'background' ? 'L1' : 'L2' }}
                            </el-tag>
                            <span class="unit-id">{{ unit.unit_id }}</span>
                            <span v-if="unit.locus" class="text-secondary">{{ unit.locus }}</span>
                            <el-tag v-if="unit.is_hydrated" size="small" type="success" effect="plain">
                                {{ isEn ? 'hydrated' : '已水合' }}
                            </el-tag>
                            <el-tag v-else-if="unit.is_zero_copy_ref" size="small" type="warning" effect="plain">
                                {{ isEn ? 'host transcript pruned' : '宿主原文已清理' }}
                            </el-tag>
                        </div>
                        <MarkdownPreview
                            :text="
                                unit.full_text ||
                                (unit.is_zero_copy_ref ? (isEn ? '(host transcript pruned)' : '（宿主原文已清理）') : unit.stored_text)
                            "
                        />
                    </div>
                </div>
                <div class="db-section">
                    <div class="db-sec-title">{{ isEn ? 'Crystallized Evidence Sentences' : '结晶证据原句' }}</div>
                    <div
                        v-for="(sent, idx) in selectedNoetic.cited_sentences.length ? selectedNoetic.cited_sentences : [selectedNoetic.answer_text]"
                        :key="idx"
                        class="cite-record"
                    >
                        <MarkdownPreview :text="sent" :hint="{ layer: 'noetic' }" compact />
                    </div>
                </div>
            </div>
        </el-drawer>
    </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAdminInfo } from '@/stores/adminInfo'
import { useConfig } from '@/stores/config'
import { useLoadingProgressText } from '@/utils/loadingProgress'
import { MonitorSocket } from '@/utils/monitorSocket'
import request from '@/utils/request'
import MarkdownPreview from '@/views/admin/plugin/rolling-memory/MarkdownPreview.vue'
import MemoryHoverPreview from '@/views/admin/plugin/rolling-memory/MemoryHoverPreview.vue'

defineOptions({
    name: 'dashboard/roi',
})

interface RoiFactEvent {
    observed_at: number
    action: string
    outcome: string
    observation: string
    harness: string
    hook: string
    text_bytes: number | null
}

interface RoiFactCounts {
    total: number
    actions: Record<string, number>
    decision_outcomes: Record<string, number>
    output_outcomes: Record<string, number>
    recall_outcomes: Record<string, number>
    known_text_bytes: number
    known_byte_events: number
    unknown_byte_events: number
}

interface RoiFactWindow {
    key: string
    seconds: number
    since_unix: number
    total: number
    actions: Record<string, number>
    recall_outcomes: Record<string, number>
    known_text_bytes: number
    unknown_byte_events: number
    episodic_added: number
    background_added: number
    noetic_crystallized: number
    citation_links: number
    unique_cited_units: number
    delivered_tokens: number
    audited_recalls?: number
    adopted_recalls?: number
    adopted_tool_count?: number
    adopted_context_count?: number
    adoption_rate_pct?: number
}

interface RoiDailyBucket {
    date: string
    episodic_count: number
    background_count: number
    noetic_count: number
    citation_links: number
    delivered_tokens: number
}

interface RoiUnitCitation {
    noetic_id: string
    query_text: string
    answer_text: string
    occurred_at: number
    assistant_id?: string
    adoption_outcome?: string
    matched_anchors?: string[]
}

interface RoiTopCitedUnit {
    unit_id: string
    layer: string
    locus: string
    address_uri: string
    waveform_bar: string
    citation_count: number
    adopted_count?: number
    last_cited_at: number
    occurred_at: number
    preview: string
    full_text: string
    is_hydrated: boolean
    tokens: number
    is_zero_copy_ref: boolean
    recent_queries: string[]
    citations: RoiUnitCitation[]
}

interface RoiLocusRow {
    locus: string
    episodic_count: number
    background_count: number
    cited_unit_count: number
    citation_hits: number
    last_active_at: number
    status: 'hot' | 'warm' | 'cold'
}

interface RoiCitedUnitDetail {
    unit_id: string
    layer: string
    locus: string
    address_uri: string
    stored_text: string
    full_text: string
    is_hydrated: boolean
    is_zero_copy_ref: boolean
}

interface RoiNoeticEvent {
    noetic_id: string
    occurred_at: number
    status: string
    query_text: string
    answer_text: string
    cited_unit_ids: string[]
    cited_sentences: string[]
    cited_units: RoiCitedUnitDetail[]
    citation_count: number
    delivered_tokens: number
    address_uri: string
    waveform_bar: string
    assistant_id: string
    incarnation_id: string
    adoption_outcome?: string
    matched_anchors?: string[]
    echo_excluded_count?: number
}

interface RoiFactsVault {
    schema_era: string
    total_units: number
    background_dirs: number
    background_files: number
    background_chunks: number
    background_bytes: number
    episodic_docs: number
    episodic_pointers: number
    episodic_text: number
    episodic_pointer_ratio_pct: number
    episodic_cells: number
    noetic_units: number
    noetic_stale: number
    noetic_with_citations: number
    total_citation_links: number
    unique_cited_units: number
    reused_units_count: number
    avg_reuse_multiplier: number
    max_reuse_count: number
    avg_citations_per_recall: number
    total_delivered_tokens: number
    avg_delivered_tokens: number
    inline_raw_tokens: number
    inline_distilled_tokens: number
    distillation_saved_pct: number
    today_episodic: number
    today_background: number
    today_noetic: number
    today_citations: number
    injection_ledger_rows: number
    audited_recalls?: number
    adopted_recalls?: number
    adopted_tool_count?: number
    adopted_context_count?: number
    echo_excluded_total?: number
    adoption_rate_pct?: number
}

interface RoiFactsPayload {
    status: 'available' | 'unknown'
    reason?: string
    source: string
    coverage: string
    identity_status: string
    scope: string
    harness: string
    counts: RoiFactCounts | null
    windows: RoiFactWindow[]
    vault_windows: RoiFactWindow[]
    vault: RoiFactsVault | null
    daily_series: RoiDailyBucket[]
    top_cited_units: RoiTopCitedUnit[]
    locus_matrix: RoiLocusRow[]
    noetic_stream: RoiNoeticEvent[]
    recent_events: RoiFactEvent[]
}

const router = useRouter()
const config = useConfig()
const adminInfo = useAdminInfo()
const isEn = computed(() => config.lang.active === 'en')

const currentScope = ref('default')
const currentHarness = ref('')
const loading = ref(false)
const { percent: loadPct } = useLoadingProgressText(loading)
const lastLoadedAt = ref(0)
const monitorWindow = ref('all')
const autoRefreshKey = ref('stream')
const selectedLocus = ref('')
const activeTab = ref<'noetic' | 'ledger'>('noetic')
const searchQuery = ref('')

const harnessOptions = computed(() => [
    { value: '', label: isEn.value ? 'All Harnesses' : '全部宿主 (All)' },
    { value: 'zcode', label: 'ZCode' },
    { value: 'claude-code', label: 'Claude Code' },
    { value: 'codex', label: 'Codex' },
    { value: 'workbuddy', label: 'WorkBuddy' },
    { value: 'codebuddy', label: 'CodeBuddy' },
    { value: 'cursor', label: 'Cursor' },
    { value: 'windsurf', label: 'Windsurf' },
    { value: 'cline', label: 'Cline' },
    { value: 'dsh', label: 'DSH' },
])

const drawerVisible = ref(false)
const drawerMode = ref<'unit' | 'noetic'>('unit')
const selectedUnit = ref<RoiTopCitedUnit | null>(null)
const selectedNoetic = ref<RoiNoeticEvent | null>(null)

const facts = ref<RoiFactsPayload>({
    status: 'unknown',
    reason: 'ledger_missing',
    source: 'admin_selected_db',
    coverage: 'unknown',
    identity_status: 'unknown',
    scope: 'default',
    harness: '',
    counts: null,
    windows: [],
    vault_windows: [],
    vault: null,
    daily_series: [],
    top_cited_units: [],
    locus_matrix: [],
    noetic_stream: [],
    recent_events: [],
})

// 实时流为默认口径：服务端共享 watcher 在金库/效能台账指纹变化时經 WebSocket 房间广播推帧（重聚合自带 ≥5s 节流），
// 静默期 0 次重聚合，无前端定时轮询。
const autoRefreshOptions = computed(() => [
    { key: 'stream', label: isEn.value ? 'Realtime (WS)' : '实时流 (WS)' },
    { key: 'off', label: isEn.value ? 'Manual' : '手动刷新' },
])

let factsSocket: MonitorSocket | null = null
const streamConnected = ref(false)

const ledgerAvailable = computed(() => facts.value.status === 'available')
const counts = computed(() => facts.value.counts)
const vault = computed(() => facts.value.vault)
const dailySeries = computed(() => facts.value.daily_series || [])
const locusMatrix = computed(() => facts.value.locus_matrix || [])
const recentEvents = computed(() => facts.value.recent_events || [])

const displayWindows = computed<RoiFactWindow[]>(() => {
    if (facts.value.vault_windows?.length) {
        return facts.value.vault_windows
    }
    if (facts.value.windows?.length) {
        return facts.value.windows
    }
    return ['1h', '24h', '7d', '30d', 'all'].map((key) => ({
        key,
        seconds: 0,
        since_unix: 0,
        total: 0,
        actions: {},
        recall_outcomes: {},
        known_text_bytes: 0,
        unknown_byte_events: 0,
        episodic_added: 0,
        background_added: 0,
        noetic_crystallized: 0,
        citation_links: 0,
        unique_cited_units: 0,
        delivered_tokens: 0,
    }))
})

const activeWindow = computed<RoiFactWindow>(
    () => displayWindows.value.find((item) => item.key === monitorWindow.value) || displayWindows.value[displayWindows.value.length - 1]
)

const windowAuditedRecalls = computed(() => (activeWindow.value?.audited_recalls ?? 0) > 0 ? activeWindow.value!.audited_recalls! : (vault.value?.audited_recalls ?? 0))
const windowAdoptedRecalls = computed(() => (activeWindow.value?.audited_recalls ?? 0) > 0 ? (activeWindow.value!.adopted_recalls ?? 0) : (vault.value?.adopted_recalls ?? 0))
const windowAdoptedTools = computed(() => (activeWindow.value?.audited_recalls ?? 0) > 0 ? (activeWindow.value!.adopted_tool_count ?? 0) : (vault.value?.adopted_tool_count ?? 0))
const windowAdoptedContexts = computed(() => (activeWindow.value?.audited_recalls ?? 0) > 0 ? (activeWindow.value!.adopted_context_count ?? 0) : (vault.value?.adopted_context_count ?? 0))
const windowAdoptionRatePct = computed(() => (activeWindow.value?.audited_recalls ?? 0) > 0 ? (activeWindow.value!.adoption_rate_pct ?? 0) : (vault.value?.adoption_rate_pct ?? 0))

const maxDailyScale = computed(() => {
    let maxVal = 10
    for (const d of dailySeries.value) {
        const v = d.episodic_count + d.background_count + d.noetic_count * 12
        if (v > maxVal) maxVal = v
    }
    return maxVal
})

const filteredTopUnits = computed(() => {
    const list = facts.value.top_cited_units || []
    if (!selectedLocus.value) return list
    return list.filter((u) => (u.locus || '(global / unscoped)') === selectedLocus.value)
})

const filteredNoeticStream = computed(() => {
    let list = facts.value.noetic_stream || []
    if (currentHarness.value) {
        list = list.filter((ev) => (ev.assistant_id || '').toLowerCase() === currentHarness.value.toLowerCase())
    }
    const q = searchQuery.value.trim().toLowerCase()
    if (q) {
        list = list.filter(
            (ev) =>
                ev.query_text.toLowerCase().includes(q) ||
                ev.noetic_id.toLowerCase().includes(q) ||
                ev.cited_unit_ids.some((id) => id.toLowerCase().includes(q)) ||
                (ev.matched_anchors || []).some((a) => a.toLowerCase().includes(q))
        )
    }
    return list
})

const lastLoadedText = computed(() => {
    if (!lastLoadedAt.value) return '—'
    return formatTime(lastLoadedAt.value)
})

function windowMetaOf(key: string): { label: string; span: string } {
    switch (key) {
        case '1h':
            return { label: isEn.value ? 'Last Hour' : '近 1 小时', span: '1h' }
        case '24h':
            return { label: isEn.value ? 'Last 24 Hours' : '近 24 小时', span: '24h' }
        case '7d':
            return { label: isEn.value ? 'Last 7 Days' : '近 7 天', span: '7d' }
        case '30d':
            return { label: isEn.value ? 'Last 30 Days' : '近 30 天', span: '30d' }
        default:
            return { label: isEn.value ? 'All-Time' : '全量历史', span: 'ALL' }
    }
}

function dayBarHeight(val: number, maxVal: number): number {
    if (!val || maxVal <= 0) return 0
    return Math.min(100, Math.max(4, Math.round((val * 100) / maxVal)))
}

function toggleLocusFilter(row: RoiLocusRow) {
    selectedLocus.value = selectedLocus.value === row.locus ? '' : row.locus
}

function locusRowClass({ row }: { row: RoiLocusRow }) {
    return selectedLocus.value === row.locus ? 'is-selected-row' : ''
}

function openUnitDrawer(row: RoiTopCitedUnit) {
    selectedUnit.value = row
    drawerMode.value = 'unit'
    drawerVisible.value = true
}

function openNoeticDrawer(row: RoiNoeticEvent) {
    selectedNoetic.value = row
    drawerMode.value = 'noetic'
    drawerVisible.value = true
}

function adoptionLabel(outcome?: string): string {
    switch (outcome) {
        case 'adopted_tool':
            return isEn.value ? '🎯 Tool Hit' : '🎯 工具直达'
        case 'adopted_context':
            return isEn.value ? '✅ Context Hit' : '✅ 事实采纳'
        case 'unadopted':
            return isEn.value ? 'Unadopted' : '未采纳'
        default:
            return isEn.value ? 'Pending' : '待后续动作'
    }
}

function reasonLabel(reason?: string): string {
    switch (reason) {
        case 'ledger_missing':
            return isEn.value ? 'Standby' : '待触发'
        case 'no_matching_events':
            return isEn.value ? '0 Events' : '暂无调用'
        case 'ledger_identity_unverified':
            return isEn.value ? 'Epoch Pending' : '待轮转'
        default:
            return reason || 'Standby'
    }
}

function reasonDetail(reason?: string): string {
    switch (reason) {
        case 'ledger_missing':
        case 'no_matching_events':
            return isEn.value
                ? 'No MCP recall events recorded in this scope yet. L3 Noetic crystallizations are already shown in the first tab.'
                : '当前作用域暂无底层账本事件记录；已发生的 Agent 召回结晶请查看左侧「L3 认知结晶与证据链」标签页。'
        case 'ledger_identity_unverified':
            return isEn.value
                ? 'The sidecar ledger belongs to an earlier database epoch and will rotate automatically on the next MCP recall.'
                : '旁路账本属于上一世代，将在下一次 MCP recall 调用时自动归档轮转。'
        default:
            return isEn.value ? 'No live ledger events in this view.' : '当前视图下暂无实时调用账本事件。'
    }
}

function formatTok(value: number | null | undefined): string {
    const n = Number(value ?? 0)
    if (!n) return '0'
    if (Math.abs(n) >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`
    if (Math.abs(n) >= 1_000) return `${(n / 1_000).toFixed(1)}K`
    return String(n)
}

function formatBytes(value: number | null | undefined): string {
    const bytes = Number(value ?? 0)
    if (!bytes) return '0 B'
    if (bytes < 1024) return `${bytes} B`
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
    return `${(bytes / 1024 / 1024).toFixed(2)} MB`
}

function formatTime(unix: number): string {
    if (!unix) return '—'
    const date = new Date(unix * 1000)
    const pad = (v: number) => String(v).padStart(2, '0')
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}

function formatShortTime(unix: number): string {
    if (!unix) return '—'
    const date = new Date(unix * 1000)
    const pad = (v: number) => String(v).padStart(2, '0')
    return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

// 仅合并真正的 facts 载荷：瞬时错误信封不得清空监控面（无 vault/矩阵字段即视为无效载荷）
function applyFactsPayload(payload: RoiFactsPayload | null | undefined): boolean {
    const isFactsPayload = Boolean(payload) && ('vault' in payload! || 'vault_windows' in payload! || 'locus_matrix' in payload!)
    if (!isFactsPayload) return false
    facts.value = {
        ...facts.value,
        ...payload,
        windows: payload!.windows || [],
        vault_windows: payload!.vault_windows || [],
        daily_series: payload!.daily_series || [],
        top_cited_units: payload!.top_cited_units || [],
        locus_matrix: payload!.locus_matrix || [],
        noetic_stream: payload!.noetic_stream || [],
        recent_events: payload!.recent_events || [],
    }
    lastLoadedAt.value = Math.floor(Date.now() / 1000)
    return true
}

async function load(): Promise<void> {
    if (loading.value) return
    loading.value = true
    try {
        const res = await request<RoiFactsPayload>({
            url: '/admin/plugin/rolling-memory/roi/facts',
            method: 'post',
            timeout: 185000,
            data: { scope: currentScope.value, harness: currentHarness.value, limit: 40 },
        })
        applyFactsPayload((res.data?.data || res.data) as RoiFactsPayload)
    } catch (e) {
        console.error('ROI Facts load failed:', e)
        // 保持 loading 态以显示错误或防抖
    } finally {
        loading.value = false
    }
}

function stopFactsStream(): void {
    if (factsSocket) {
        factsSocket.stop()
        factsSocket = null
    }
    streamConnected.value = false
}

// ROI 实时流：走同源 MonitorSocket WebSocket 房间广播（rm/roi:<scope>[@<harness>]），变化即推。
function startFactsStream(): void {
    stopFactsStream()
    const token = adminInfo.token
    if (!token) return
    if (typeof document !== 'undefined' && document.hidden) return
    const scope = String(currentScope.value || '').trim() || 'default'
    const harness = String(currentHarness.value || '').trim()
    const room = harness ? `rm/roi:${scope}@${harness}` : `rm/roi:${scope}`
    factsSocket = new MonitorSocket({
        token,
        room,
        onStatus: (st) => {
            streamConnected.value = st === 'connected'
        },
        onMessage: (env: any) => {
            if (env?.event === 'roi' && env.data) {
                applyFactsPayload(env.data as RoiFactsPayload)
            }
        },
    })
    factsSocket.start()
}

// 切换 scope / harness：先取一次全量 facts，再按当前自动刷新口径重建（实时流需换房间重连，保证订阅口径与选择一致）。
function onScopeChange(): void {
    void load()
    applyAutoRefresh()
}

function onHarnessChange(): void {
    void load()
    applyAutoRefresh()
}

function applyAutoRefresh(): void {
    if (autoRefreshKey.value === 'stream') {
        startFactsStream()
        return
    }
    stopFactsStream()
}

const onVisibilityChange = () => {
    if (typeof document === 'undefined') return
    if (document.hidden) {
        stopFactsStream()
    } else {
        applyAutoRefresh()
    }
}

onMounted(() => {
    void load()
    applyAutoRefresh()
    if (typeof document !== 'undefined') {
        document.addEventListener('visibilitychange', onVisibilityChange)
    }
})

let roiDeactivated = false
onActivated(() => {
    if (!roiDeactivated) return
    roiDeactivated = false
    applyAutoRefresh()
})

onDeactivated(() => {
    roiDeactivated = true
    stopFactsStream()
})

onBeforeUnmount(() => {
    if (typeof document !== 'undefined') {
        document.removeEventListener('visibilitychange', onVisibilityChange)
    }
    stopFactsStream()
})
</script>

<style scoped lang="scss">
.rm-roi-monitor {
    display: flex;
    flex-direction: column;
    gap: 14px;
}

.roi-hero {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
    padding: 16px 18px;
    background: var(--el-bg-color-overlay);
    border: 1px solid var(--el-border-color-light);
    border-radius: 10px;
}

.hero-left {
    display: flex;
    flex-direction: column;
    gap: 8px;
}

.hero-title-row {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}

.hero-badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
    border-radius: 8px;
}

.hero-title {
    margin: 0;
    font-size: 18px;
    font-weight: 600;
    color: var(--el-text-color-primary);
}

.hero-meta {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    font-size: 12px;
    color: var(--el-text-color-secondary);
}

.meta-item.is-filter {
    padding: 1px 8px;
    color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
    border-radius: 999px;
    cursor: pointer;
}

.hero-right {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}

.tip-q {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 15px;
    height: 15px;
    padding: 0;
    margin-left: 4px;
    font-family: inherit;
    font-size: 10px;
    font-weight: 700;
    line-height: 1;
    color: var(--el-text-color-secondary);
    background: var(--el-fill-color);
    border: 1px solid var(--el-border-color-light);
    border-radius: 50%;
    cursor: pointer;
}

.tip-q:hover {
    color: var(--el-color-primary);
    border-color: var(--el-color-primary);
}

.window-strip {
    padding: 12px 16px;
    background: var(--el-bg-color-overlay);
    border: 1px solid var(--el-border-color-light);
    border-radius: 10px;
}

.ws-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-bottom: 10px;
    flex-wrap: wrap;
}

.ws-title {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);
}

.ws-sub {
    font-size: 12px;
    color: var(--el-text-color-secondary);
}

.ws-grid {
    display: grid;
    grid-template-columns: repeat(5, minmax(0, 1fr));
    gap: 10px;
    padding-bottom: 2px;
}

.ws-grid::-webkit-scrollbar {
    height: 6px;
}

.ws-grid::-webkit-scrollbar-thumb {
    background: var(--el-border-color);
    border-radius: 3px;
}

.ws-card {
    display: flex;
    flex-direction: column;
    gap: 5px;
    padding: 10px 12px;
    text-align: left;
    background: var(--el-fill-color-blank);
    border: 1px solid var(--el-border-color-light);
    border-radius: 8px;
    cursor: pointer;
    transition:
        border-color 0.2s ease,
        background 0.2s ease;
}

.ws-card.is-active {
    border-color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
}

.ws-card-top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    font-size: 12px;
}

.ws-chip {
    font-weight: 600;
    color: var(--el-text-color-primary);
}

.ws-span {
    font-size: 11px;
    color: var(--el-text-color-secondary);
}

.ws-card-main {
    display: flex;
    align-items: center;
    gap: 6px;
    row-gap: 2px;
    flex-wrap: wrap;
    font-size: 13px;
    font-weight: 600;
}

.ws-card-main > span {
    white-space: nowrap;
}

.ws-card-foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 4px 10px;
    flex-wrap: wrap;
    font-size: 11px;
    color: var(--el-text-color-secondary);
}

.ws-card-foot > span {
    white-space: nowrap;
}

.kpi-grid4 {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 12px;
}

.roi-card {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 14px 16px;
    background: var(--el-bg-color-overlay);
    border: 1px solid var(--el-border-color-light);
    border-radius: 10px;
}

.roi-card.is-accent {
    border-color: var(--el-color-primary-light-5);
}

.rc-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
}

.rc-title {
    display: inline-flex;
    align-items: center;
    font-size: 12px;
    font-weight: 600;
    color: var(--el-text-color-regular);
}

.rc-val {
    display: flex;
    align-items: baseline;
    gap: 6px;
    flex-wrap: wrap;
    font-size: 26px;
    font-weight: 700;
    color: var(--el-text-color-primary);
}

.rc-unit {
    font-size: 12px;
    font-weight: 500;
    color: var(--el-text-color-secondary);
}

.rc-pill {
    padding: 2px 8px;
    font-size: 11px;
    font-weight: 600;
    color: var(--el-text-color-regular);
    background: var(--el-fill-color);
    border-radius: 999px;
}

.rc-pill.is-success {
    color: var(--el-color-success);
    background: var(--el-color-success-light-9);
}

.rc-pill.is-warn {
    color: var(--el-color-warning);
    background: var(--el-color-warning-light-9);
}

.rc-sub {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    flex-wrap: wrap;
    font-size: 11px;
    color: var(--el-text-color-secondary);
}

.roi-mid-grid,
.roi-bottom-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: 12px;
}

.roi-panel {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 14px 16px;
    background: var(--el-bg-color-overlay);
    border: 1px solid var(--el-border-color-light);
    border-radius: 10px;
}

.panel-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 10px;
    flex-wrap: wrap;
}

.panel-title {
    display: block;
    font-size: 14px;
    font-weight: 600;
    color: var(--el-text-color-primary);
}

.panel-sub {
    display: block;
    margin-top: 2px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
}

.legend-inline {
    display: flex;
    align-items: center;
    gap: 10px;
    font-size: 11px;
    color: var(--el-text-color-secondary);
}

.lg-item {
    display: inline-flex;
    align-items: center;
    gap: 4px;
}

.lg-dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 2px;
}

.lg-dot.is-ep {
    background: var(--el-color-primary);
}

.lg-dot.is-bg {
    background: var(--el-color-warning);
}

.lg-dot.is-nt {
    background: var(--el-color-success);
}

.daily-bars {
    display: grid;
    grid-template-columns: repeat(14, minmax(0, 1fr));
    gap: 6px;
    align-items: end;
    height: 196px;
    padding: 10px 6px 4px;
    background: var(--el-fill-color-lighter);
    border-radius: 8px;
}

.day-col {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 4px;
    height: 100%;
}

.day-nums {
    font-size: 10px;
    line-height: 1;
}

.dn-nt {
    color: var(--el-color-success);
    font-weight: 700;
}

.dn-zero {
    color: var(--el-text-color-placeholder);
}

.day-track {
    flex: 1;
    width: 100%;
    max-width: 22px;
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
    gap: 1px;
    background: var(--el-fill-color);
    border-radius: 4px;
    overflow: hidden;
}

.bar-seg {
    width: 100%;
    transition: height 0.25s ease;
}

.bar-seg.is-ep {
    background: var(--el-color-primary);
}

.bar-seg.is-bg {
    background: var(--el-color-warning);
}

.bar-seg.is-nt {
    background: var(--el-color-success);
}

.day-total {
    font-size: 10px;
    color: var(--el-text-color-regular);
}

.day-label {
    font-size: 10px;
    color: var(--el-text-color-secondary);
}

.roi-table {
    width: 100%;
}

.roi-table.is-clickable :deep(.el-table__row) {
    cursor: pointer;
}

.panel-head-actions {
    display: inline-flex;
    align-items: center;
    gap: 8px;
}

.badge-count {
    padding: 2px 10px;
    font-size: 11px;
    font-weight: 600;
    color: var(--el-text-color-regular);
    background: var(--el-fill-color);
    border-radius: 999px;
}

.cite-pill {
    display: inline-block;
    padding: 2px 8px;
    font-size: 12px;
    font-weight: 700;
    border-radius: 999px;
}

.cite-pill.is-gold {
    color: var(--el-color-success);
    background: var(--el-color-success-light-9);
}

.cite-pill.is-normal {
    color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
}

.unit-cell {
    display: flex;
    flex-direction: column;
    gap: 3px;
}

.unit-id {
    font-size: 12px;
    font-weight: 600;
    color: var(--el-text-color-primary);
}

.unit-tags {
    display: flex;
    align-items: center;
    gap: 6px;
}

.unit-locus {
    font-size: 11px;
    color: var(--el-text-color-secondary);
}

.preview-line {
    display: flex;
    align-items: center;
    font-size: 12px;
    color: var(--el-text-color-regular);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.query-hint {
    margin-top: 2px;
    font-size: 11px;
    color: var(--el-text-color-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.tab-switches {
    display: flex;
    align-items: center;
    gap: 6px;
}

.tab-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 6px 12px;
    font-size: 12px;
    font-weight: 600;
    color: var(--el-text-color-regular);
    background: var(--el-fill-color-light);
    border: 1px solid var(--el-border-color-light);
    border-radius: 6px;
    cursor: pointer;
}

.tab-btn.is-active {
    color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
    border-color: var(--el-color-primary);
}

.tab-cnt {
    padding: 1px 6px;
    font-size: 11px;
    background: var(--el-bg-color);
    border-radius: 999px;
}

.query-main {
    font-size: 12px;
    font-weight: 600;
    color: var(--el-text-color-primary);
}

.adopt-cell {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 3px;
}

.adopt-anchor {
    max-width: 145px;
    font-size: 11px;
    color: var(--el-text-color-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}

.query-sub {
    display: flex;
    gap: 6px;
    font-size: 11px;
    color: var(--el-text-color-secondary);
}

.ledger-notice {
    margin-bottom: 8px;
    padding: 8px 12px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
    background: var(--el-fill-color-lighter);
    border-radius: 6px;
}

.drawer-body {
    display: flex;
    flex-direction: column;
    gap: 12px;
}

.db-kv {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 10px;
    padding-bottom: 8px;
    border-bottom: 1px solid var(--el-border-color-lighter);
    font-size: 12px;
}

.db-k {
    color: var(--el-text-color-secondary);
}

.db-v {
    color: var(--el-text-color-primary);
}

.db-section {
    display: flex;
    flex-direction: column;
    gap: 6px;
}

.db-sec-title {
    font-size: 12px;
    font-weight: 600;
    color: var(--el-text-color-primary);
}

.db-code {
    margin: 0;
    padding: 10px 12px;
    font-size: 12px;
    line-height: 1.55;
    white-space: pre-wrap;
    word-break: break-word;
    background: var(--el-fill-color-lighter);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 6px;
}

.db-box {
    padding: 8px 10px;
    font-size: 13px;
    font-weight: 600;
    background: var(--el-fill-color-light);
    border-radius: 6px;
}

.db-tags {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
}

.cite-record {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px;
    margin-bottom: 8px;
    background: var(--el-fill-color-blank);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
}

.cite-head {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    font-size: 11px;
}

.cite-head .unit-id {
    font-weight: 600;
    color: var(--el-text-color-primary);
}

.db-query-list {
    margin: 0;
    padding-left: 18px;
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 12px;
}

.font-mono {
    font-family: var(--ag-font-mono, 'JetBrains Mono', 'SFMono-Regular', Consolas, monospace);
}

.text-primary {
    color: var(--el-color-primary);
}

.text-success {
    color: var(--el-color-success);
}

.text-warning {
    color: var(--el-color-warning);
}

.text-secondary {
    color: var(--el-text-color-secondary);
}

@media (max-width: 1360px) {
    .ws-grid {
        display: flex;
        overflow-x: auto;
    }

    .ws-card {
        flex: 1 0 172px;
    }

    .kpi-grid4 {
        grid-template-columns: repeat(2, minmax(0, 1fr));
    }

    .roi-mid-grid,
    .roi-bottom-grid {
        grid-template-columns: minmax(0, 1fr);
    }
}

@media (max-width: 900px) {
    .kpi-grid4 {
        grid-template-columns: minmax(0, 1fr);
    }
}
</style>

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
