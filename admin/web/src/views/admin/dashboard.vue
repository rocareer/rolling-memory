<template>
    <div class="default-main dashboard">
        <!-- ==================== 欢迎条 ==================== -->
        <div class="welcome-row">
            <div class="welcome-bar welcome-bar-left">
                <img :src="DashboardHeaderIllustration" class="welcome-illustration" alt="" />
                <div class="welcome-content">
                    <div class="hello">{{ greeting }}，{{ adminInfo.nickname }} 👋</div>
                    <div class="tip">
                        {{ $t('dashboard.welcomeTip') }}
                    </div>
                    <div class="welcome-meta">
                        <div class="meta-item">
                            <Icon :size="14" name="el-calendar" />
                            <span>{{ today }}</span>
                        </div>
                        <div class="meta-item">
                            <Icon :size="14" name="el-timer" />
                            <span>{{ nowTime }}</span>
                        </div>
                        <div class="meta-item meta-live" :class="{ offline: !rmData.engine_online }">
                            <span class="live-dot"></span>
                            <span>
                                {{ rmData.engine_online ? $t('dashboard.engineOnline') : $t('dashboard.engineOffline') }}
                                · v{{ rmData.version || '0.0.4' }} · Core v1.0.1
                            </span>
                        </div>
                        <div class="meta-item meta-mode-switch" data-testid="dashboard-mode-switch">
                            <span class="mode-switch-label">{{ $t('dashboard.modeLabel') }}</span>
                            <div class="mode-switch-track">
                                <el-tooltip
                                    :content="modeTooltip"
                                    placement="top"
                                    :enterable="false"
                                    :hide-after="0"
                                    :show-after="300"
                                >
                                    <span class="mode-switch-pill is-active">{{ modeLabel }}</span>
                                </el-tooltip>
                                <el-tooltip
                                    :content="$t('dashboard.pulsePreviewTip')"
                                    placement="top"
                                    :enterable="false"
                                    :hide-after="0"
                                    :show-after="300"
                                >
                                    <button type="button" class="mode-switch-pill is-preview" @click="showPulseApplyTip">
                                        <Icon :size="12" name="lucide-zap" />
                                        <span>{{ $t('dashboard.previewDetails') }}</span>
                                    </button>
                                </el-tooltip>
                            </div>
                        </div>
                        <el-tooltip
                            :content="
                                config.lang.active === 'en'
                                    ? 'Memory Isolation Scope (default mem:self for user workspace memories; mem:eval:* for isolated benchmark data). Click to cycle scopes.'
                                    : '记忆作用域（Scope）：物理隔离不同领域的记忆（默认 mem:self 为用户真实研发记忆，mem:eval:* 为评测隔离域）。点击可循环切换作用域。'
                            "
                            placement="top"
                            :enterable="false"
                            :hide-after="0"
                            :show-after="300"
                        >
                            <div class="meta-item meta-clickable" @click="cycleScope">
                                <Icon :size="14" name="el-switch" />
                                <span>Scope: {{ rmData.scope }}</span>
                            </div>
                        </el-tooltip>
                        <div class="meta-item">
                            <Icon :size="14" name="el-coin" />
                            <span>
                                {{ $t('dashboard.dbSize') }}: {{ rmData.db_size_mb }} MB · {{ $t('dashboard.conflicts') }}:
                                {{ rmData.conflicts_proposed }}
                            </span>
                        </div>
                        <div class="meta-item meta-poll-group" :class="{ 'meta-stale': dataStale }">
                            <el-tooltip
                                :content="
                                    config.lang.active === 'en'
                                        ? `Click to force an immediate telemetry refresh (last updated at ${rmData.updated_at})`
                                        : `点击立即强制拉取最新监控数据（上次更新于 ${rmData.updated_at}）`
                                "
                                placement="top"
                                :enterable="false"
                                :hide-after="0"
                                :show-after="300"
                            >
                                <span class="poll-trigger" @click="fetchRealtimeData(true)">
                                    <Icon :size="14" name="el-refresh" />
                                    <span>{{ liveBadgeText }}</span>
                                </span>
                            </el-tooltip>
                            <span class="poll-chips">
                                <el-tooltip
                                    v-for="opt in pollOptions"
                                    :key="opt.key"
                                    :content="opt.tip"
                                    placement="top"
                                    :enterable="false"
                                    :hide-after="0"
                                    :show-after="350"
                                >
                                    <button
                                        type="button"
                                        class="poll-chip"
                                        :class="{ 'is-active': pollIntervalKey === opt.key }"
                                        @click.stop="setPollInterval(opt.key)"
                                    >
                                        {{ opt.label }}
                                    </button>
                                </el-tooltip>
                            </span>
                        </div>
                        <el-tooltip
                            :content="
                                config.lang.active === 'en'
                                    ? 'Open built-in interactive CLI Terminal modal to run rmem commands directly'
                                    : '打开内置实时命令行终端弹窗，直接执行或观测 rmem 命令与任务输出'
                            "
                            placement="top"
                            :enterable="false"
                            :hide-after="0"
                            :show-after="300"
                        >
                            <div class="meta-item meta-clickable" @click="terminal.toggle(true)">
                                <Icon :size="14" name="lucide-terminal" />
                                <span>CLI Terminal</span>
                            </div>
                        </el-tooltip>
                        <el-tooltip
                            :content="
                                config.lang.active === 'en'
                                    ? 'Jump to the ROI Monitor (L3 crystallization, citation reuse multiplier, tokenz-r delivery budget & workspace heat matrix)'
                                    : '进入「ROI 监控屏」（L3 认知结晶、跨层引证复用倍数、tokenz-r 交付预算与工作区冷热矩阵）'
                            "
                            placement="top"
                            :enterable="false"
                            :hide-after="0"
                            :show-after="300"
                        >
                            <div class="meta-item meta-clickable" @click="goRoiPage">
                                <Icon :size="14" name="lucide-trending-up" />
                                <span>{{ config.lang.active === 'en' ? 'ROI Monitor' : 'ROI 监控屏' }}</span>
                            </div>
                        </el-tooltip>
                        <el-tooltip
                            :content="
                                config.lang.active === 'en'
                                    ? 'Jump to interactive Memory Unit Knowledge & Entity-Relation Graph (Web Worker Force Layout)'
                                    : '进入交互式记忆图谱（记忆单元实体-关系力导向拓扑画布，支持 1/2 跳邻域展开与证据溯源）'
                            "
                            placement="top"
                            :enterable="false"
                            :hide-after="0"
                            :show-after="300"
                        >
                            <div class="meta-item meta-clickable" @click="goGraphPage">
                                <Icon :size="14" name="lucide-git-graph" />
                                <span>{{ config.lang.active === 'en' ? 'Memory Graph' : '记忆图谱' }}</span>
                            </div>
                        </el-tooltip>
                    </div>
                </div>
            </div>

            <div class="welcome-timer">
                <div class="timer-label">
                    <Icon :size="14" name="el-clock" />
                    <span>{{ $t('dashboard.workTimerLabel') }}</span>
                    <el-popover trigger="click" placement="bottom" :width="360" popper-class="rm-help-popper">
                        <template #reference>
                            <button
                                type="button"
                                class="tip-q"
                                :aria-label="config.lang.active === 'en' ? 'Focus timer definition' : '专注计时器口径说明'"
                            >
                                ?
                            </button>
                        </template>
                        <div class="rm-help-body">
                            <p v-if="config.lang.active === 'en'">Daily focus timer persisted in localStorage; auto-resets at midnight (00:00).</p>
                            <p v-else>今日专注研发计时器（跨页面刷新持久保持，跨零点自动归零重置）。</p>
                        </div>
                    </el-popover>
                </div>
                <div class="timer-time" :class="{ paused: !working }">
                    <div v-for="(unit, idx) in workUnits" :key="unit.label" class="timer-group">
                        <div class="timer-unit">
                            <div class="num">{{ pad(unit.value) }}</div>
                            <div class="txt">{{ unit.label }}</div>
                        </div>
                        <div v-if="idx < workUnits.length - 1" class="colon">:</div>
                    </div>
                </div>
                <el-tooltip
                    :content="
                        config.lang.active === 'en'
                            ? working
                                ? 'Pause focus timer and take a break'
                                : 'Resume focus timer'
                            : working
                              ? '暂停当前工作计时并小憩片刻'
                              : '继续累加今日专注工作时长'
                    "
                    placement="bottom"
                    :enterable="false"
                    :hide-after="0"
                    :show-after="300"
                >
                    <button type="button" class="timer-btn" :class="{ resting: !working }" @click="toggleWork">
                        <Icon :size="14" v-if="working" name="el-coffee" />
                        <Icon :size="14" v-else name="el-video-play" />
                        {{ working ? $t('dashboard.workRest') : $t('dashboard.workContinue') }}
                    </button>
                </el-tooltip>
            </div>
        </div>

        <!-- ==================== KPI 卡片行 ==================== -->
        <div class="kpi-row">
            <div v-for="item in kpiList" :key="item.key" class="kpi-card" :class="{ 'roi-stat-card': item.toRoi }" @click="item.toRoi && goRoiPage()">
                <div class="kpi-top">
                    <div class="kpi-title-wrap">
                        <span class="kpi-title">{{ item.title }}</span>
                        <el-popover trigger="click" placement="top" :width="380" popper-class="rm-help-popper">
                            <template #reference>
                                <button
                                    type="button"
                                    class="tip-q"
                                    :aria-label="config.lang.active === 'en' ? 'Card metric definitions' : '卡片指标口径说明'"
                                >
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p>{{ item.tooltip }}</p>
                                <p>{{ item.valueTip }}</p>
                                <p>{{ item.subTip }}</p>
                            </div>
                        </el-popover>
                    </div>
                    <div class="kpi-icon">
                        <Icon :size="16" :name="item.icon" />
                    </div>
                </div>
                <div class="kpi-value">{{ item.value }}</div>
                <div class="kpi-bottom">
                    <span v-if="item.kind === 'delta'" class="kpi-trend" :class="item.trendUp ? 'up' : 'down'">
                        <Icon :size="12" :name="item.trend >= 0 ? 'el-caret-top' : 'el-caret-bottom'" />
                        {{ Math.abs(item.trend) }}%
                    </span>
                    <span v-else class="kpi-ratio">{{ item.trend }}%</span>
                    <span class="kpi-sub">{{ item.sub }}</span>
                    <div :ref="(el) => setSparkRef(el as HTMLElement, item.key)" class="kpi-spark"></div>
                </div>
            </div>
        </div>

        <!-- ==================== 第二排：ROI 与记忆效能统计卡片行（今日计费净省 × 历史计费净省 × 四维采纳 × 静默防噪） ==================== -->
        <div v-if="efficacyUnavailable" class="roi-unavailable-banner">
            <Icon :size="14" name="lucide-info" />
            <span v-if="config.lang.active === 'en'">
                View detailed recall provenance, citation reuse, and delivered token statistics in the ROI Monitor.
            </span>
            <span v-else> 查看完整的检索溯源、跨会话引证复用与交付 Token 统计，请前往「ROI 监控屏」。 </span>
            <el-button size="small" link type="primary" @click="goRoiPage">
                {{ config.lang.active === 'en' ? 'Open ROI Monitor →' : '进入 ROI 监控屏 →' }}
            </el-button>
        </div>
        <div class="kpi-row roi-kpi-row">
            <!-- 1. 今日记忆沉淀与认知结晶 -->
            <div class="kpi-card roi-stat-card" @click="goRoiPage">
                <div class="kpi-top">
                    <div class="kpi-title-wrap">
                        <span class="kpi-title">{{ config.lang.active === 'en' ? 'Today Ingested & Crystallized' : '今日沉淀与认知结晶' }}</span>
                        <el-popover trigger="click" placement="top" :width="380" popper-class="rm-help-popper">
                            <template #reference><button type="button" class="tip-q">?</button></template>
                            <div class="rm-help-body">
                                {{
                                    config.lang.active === 'en'
                                        ? 'L1-L2 Ingested units vs L3 crystallized.'
                                        : '今日新增 L1-L2 情景/背景记忆，以及触发结晶数与实名引证次数。'
                                }}
                            </div>
                        </el-popover>
                    </div>
                    <Icon :size="16" name="el-sunny" />
                </div>
                <div class="kpi-value text-primary">
                    <template v-if="efficacyUnavailable">—</template>
                    <template v-else>
                        +{{ (vaultRoi?.today_episodic ?? 0) + (vaultRoi?.today_background ?? 0) + (vaultRoi?.today_noetic ?? 0) }}
                        <small class="kpi-inline-unit">
                            {{
                                config.lang.active === 'en'
                                    ? `Noetic ${vaultRoi?.today_noetic ?? 0} · Citations ${vaultRoi?.today_citations ?? 0}`
                                    : `结晶 ${vaultRoi?.today_noetic ?? 0} · 引证 ${vaultRoi?.today_citations ?? 0}`
                            }}
                        </small>
                    </template>
                </div>
                <div class="kpi-bottom">
                    <span class="kpi-ratio"
                        >{{ config.lang.active === 'en' ? 'Episodic' : '情景 L2' }} +{{ vaultRoi?.today_episodic ?? 0 }} ·
                        {{ config.lang.active === 'en' ? 'Background' : '背景 L1' }} +{{ vaultRoi?.today_background ?? 0 }}</span
                    >
                </div>
            </div>

            <!-- 2. 累计认知结晶与跨层引证 -->
            <div class="kpi-card roi-stat-card" @click="goRoiPage">
                <div class="kpi-top">
                    <div class="kpi-title-wrap">
                        <span class="kpi-title">{{ config.lang.active === 'en' ? 'Cumulative Noetic & Links' : '累计认知结晶与跨层引证' }}</span>
                        <el-popover trigger="click" placement="top" :width="380" popper-class="rm-help-popper">
                            <template #reference><button type="button" class="tip-q">?</button></template>
                            <div class="rm-help-body">
                                {{
                                    config.lang.active === 'en'
                                        ? 'Total crystallized L3 handles and citation chain links.'
                                        : '全时段认知结晶总量与跨层引证链长度。'
                                }}
                            </div>
                        </el-popover>
                    </div>
                    <Icon :size="16" name="el-coin" />
                </div>
                <div class="kpi-value">
                    <template v-if="efficacyUnavailable">—</template>
                    <template v-else>
                        {{ vaultRoi?.noetic_units ?? 0 }}
                        <small class="kpi-inline-unit">
                            {{
                                config.lang.active === 'en'
                                    ? `Noetic · ${vaultRoi?.total_citation_links ?? 0} citations`
                                    : `结晶 · ${vaultRoi?.total_citation_links ?? 0} 次引证`
                            }}
                        </small>
                    </template>
                </div>
                <div class="kpi-bottom">
                    <span class="kpi-sub">
                        {{
                            config.lang.active === 'en'
                                ? `Avg ${vaultRoi?.avg_delivered_tokens ?? 0} tokens/recall`
                                : `每次均引 ${vaultRoi?.avg_citations_per_recall ?? 0} 单元 · 单次召回 ~${vaultRoi?.avg_delivered_tokens ?? 0} Tokens`
                        }}
                    </span>
                </div>
            </div>

            <!-- 3. 记忆资产复用倍数 -->
            <div class="kpi-card roi-stat-card" @click="goRoiPage">
                <div class="kpi-top">
                    <div class="kpi-title-wrap">
                        <span class="kpi-title">{{ config.lang.active === 'en' ? 'Memory Reuse Multiplier' : '记忆资产复用倍数 (M1)' }}</span>
                        <el-popover trigger="click" placement="top" :width="380" popper-class="rm-help-popper">
                            <template #reference><button type="button" class="tip-q">?</button></template>
                            <div class="rm-help-body">
                                {{
                                    config.lang.active === 'en'
                                        ? 'Avg citations per unit, showing high-reuse golden units.'
                                        : '平均每个底层记忆被引证次数；展示高复用金牌记忆单元。'
                                }}
                            </div>
                        </el-popover>
                    </div>
                    <Icon :size="16" name="el-circle-check" />
                </div>
                <div class="kpi-value">
                    <template v-if="efficacyUnavailable">—</template>
                    <template v-else>
                        {{ vaultRoi?.avg_reuse_multiplier ?? 0 }}×
                        <small class="kpi-inline-unit">
                            {{
                                config.lang.active === 'en' ? `Max ${vaultRoi?.max_reuse_count ?? 0}×` : `单条最高 ${vaultRoi?.max_reuse_count ?? 0}×`
                            }}
                        </small>
                    </template>
                </div>
                <div class="kpi-bottom">
                    <span class="kpi-ratio">
                        {{
                            config.lang.active === 'en'
                                ? `${vaultRoi?.unique_cited_units ?? 0} unique cited · ${vaultRoi?.reused_units_count ?? 0} multi-hit units`
                                : `${vaultRoi?.unique_cited_units ?? 0} 独立单元被命中 · ${vaultRoi?.reused_units_count ?? 0} 金牌单元`
                        }}
                    </span>
                </div>
            </div>

            <!-- 4. 零拷贝金库与三层资产底座 -->
            <div class="kpi-card roi-stat-card" @click="goRoiPage">
                <div class="kpi-top">
                    <div class="kpi-title-wrap">
                        <span class="kpi-title">{{ config.lang.active === 'en' ? 'Zero-Copy Vault Footprint' : '零拷贝金库底座' }}</span>
                        <el-popover trigger="click" placement="top" :width="380" popper-class="rm-help-popper">
                            <template #reference><button type="button" class="tip-q">?</button></template>
                            <div class="rm-help-body">
                                {{
                                    config.lang.active === 'en'
                                        ? 'Zero-copy episodic pointer ratio and 3-layer asset footprint.'
                                        : '零拷贝引用占比与三层认知空间资产规模。'
                                }}
                            </div>
                        </el-popover>
                    </div>
                    <Icon :size="16" name="lucide-trending-up" />
                </div>
                <div class="kpi-value">
                    <template v-if="efficacyUnavailable">—</template>
                    <template v-else>
                        {{ vaultRoi?.episodic_pointer_ratio_pct ?? 0 }}%
                        <small class="kpi-inline-unit">{{ config.lang.active === 'en' ? 'zero-copy @ref' : '指针占比' }}</small>
                    </template>
                </div>
                <div class="kpi-bottom">
                    <span class="kpi-sub text-primary">
                        {{
                            config.lang.active === 'en'
                                ? `Total ${vaultRoi?.total_units ?? 0} units · ${vaultRoi?.episodic_cells ?? 0} L2 addresses`
                                : `总资产 ${vaultRoi?.total_units ?? 0} 单元 · ${vaultRoi?.episodic_cells ?? 0} L2 记忆地址`
                        }}
                    </span>
                </div>
            </div>
        </div>

        <!-- ==================== 图表行 ==================== -->
        <div class="chart-row">
            <div class="chart-card chart-card-span-2">
                <div class="card-header">
                    <div class="card-title-group">
                        <div class="card-title">
                            {{ $t('dashboard.accessTrend') }}
                            <el-popover trigger="click" placement="top" :width="420" popper-class="rm-help-popper">
                                <template #reference>
                                    <button
                                        type="button"
                                        class="tip-q"
                                        :aria-label="config.lang.active === 'en' ? 'Chart & summary metric definitions' : '图表与汇总指标口径说明'"
                                    >
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="config.lang.active === 'en'">
                                        Chart: dual-axis. Right axis = per-bucket assets: stacked bars L1 Background (amber) / L2 Episodic (cyan) / L3
                                        Recall Trail (purple) plus the L1+L2+L3 total line (green); left axis = Recall Triggers (blue line).
                                    </p>
                                    <p v-else>
                                        图表口径：双轴。右轴柱=按时间桶统计的三层资产（L1 背景文档·琥珀 / L2 情景对话·青色 / L3 认知结晶·紫红），绿线=
                                        三层合计；左轴蓝线=该桶内实际发生的检索召回次数。
                                    </p>
                                    <p v-if="config.lang.active === 'en'">
                                        Bucket time follows each memory unit's initial ingest timestamp: editing an existing background file updates
                                        its content in place without re-counting existing chunks as new assets, so window growth (+N) reconciles with
                                        stock totals.
                                    </p>
                                    <p v-else>
                                        入桶时间按每条记忆的首次入库时间聚合（修改已有文件不会把既有分块重复计为新增），使窗口内新增量（+N）与上方三层存量净增一致对账。
                                    </p>
                                    <p v-if="config.lang.active === 'en'">
                                        Last bucket: the rightmost slot is the current time slot still accumulating real-time events.
                                    </p>
                                    <p v-else>末桶：最右侧末桶代表当前正在实时进行中的时间切片，随新产生的内容与召回实时累加。</p>
                                    <p v-if="config.lang.active === 'en'">
                                        L1 / L2 / L3 Assets: current stored totals and the in-window bucket sum for each layer; L3 counts active
                                        crystallizations.
                                    </p>
                                    <p v-else>
                                        L1 / L2 / L3 三层资产：上方汇总条给出各层当前存量与窗口内新增量（+增长）；L3 统计当前有效的结晶条目。
                                    </p>
                                    <p v-if="config.lang.active === 'en'">
                                        Recall Triggers: MCP recall tool calls initiated by the agent within each time bucket.
                                    </p>
                                    <p v-else>检索召回触发：每个时间桶内 Agent 调用 MCP recall 工具执行记忆检索的次数。</p>
                                </div>
                            </el-popover>
                        </div>
                        <span class="live-slot-pill">
                            <span class="pulse-dot"></span>
                            {{
                                config.lang.active === 'en'
                                    ? `Bucket ${trendSummary.lastBucket} in progress`
                                    : `末桶 ${trendSummary.lastBucket} 实时累积中`
                            }}
                        </span>
                    </div>
                    <div class="trend-ranges">
                        <el-tooltip
                            v-for="r in rangeList"
                            :key="r.value"
                            :content="r.tip"
                            placement="top"
                            :enterable="false"
                            :hide-after="0"
                            :show-after="350"
                        >
                            <button type="button" class="range-chip" :class="{ 'is-active': trendRange === r.value }" @click="setRange(r.value)">
                                {{ r.label }}
                            </button>
                        </el-tooltip>
                    </div>
                </div>
                <div class="trend-summary-strip">
                    <div class="ts-item">
                        <span class="ts-dot" style="background: #67c23a"></span>
                        <span class="ts-label">{{ config.lang.active === 'en' ? 'Total Assets (L1–L3)' : '总资产 (L1–L3)' }}</span>
                        <span class="ts-val text-success">
                            {{ totalLayerAssets.toLocaleString() }}
                            <small class="font-mono">(+{{ trendSummary.totalAssertions.toLocaleString() }})</small>
                        </span>
                    </div>
                    <div class="ts-item">
                        <span class="ts-dot" style="background: #e6a23c"></span>
                        <span class="ts-label">{{ config.lang.active === 'en' ? 'L1 Background' : 'L1 背景资产' }}</span>
                        <span class="ts-val">
                            {{ (rmData.l1_total ?? 0).toLocaleString() }}
                            <small class="font-mono text-warning">(+{{ trendSummary.totalL1.toLocaleString() }})</small>
                        </span>
                    </div>
                    <div class="ts-item">
                        <span class="ts-dot" style="background: #36cfc9"></span>
                        <span class="ts-label">{{ config.lang.active === 'en' ? 'L2 Episodic' : 'L2 情景资产' }}</span>
                        <span class="ts-val">
                            {{ (rmData.l2_total ?? rmData.user_traces ?? 0).toLocaleString() }}
                            <small class="font-mono text-primary">(+{{ trendSummary.totalL2.toLocaleString() }})</small>
                        </span>
                    </div>
                    <div class="ts-item">
                        <span class="ts-dot" style="background: #b37feb"></span>
                        <span class="ts-label">{{ config.lang.active === 'en' ? 'L3 Noetic' : 'L3 结晶资产' }}</span>
                        <span class="ts-val">
                            {{ (rmData.l3_total ?? 0).toLocaleString() }}
                            <small class="font-mono">(+{{ trendSummary.totalL3.toLocaleString() }})</small>
                        </span>
                    </div>
                    <div class="ts-item">
                        <span class="ts-dot" style="background: #409eff"></span>
                        <span class="ts-label">{{ config.lang.active === 'en' ? 'Recall Triggers' : '检索召回触发' }}</span>
                        <span class="ts-val text-primary">{{ trendSummary.totalRecalls.toLocaleString() }}</span>
                    </div>
                    <div class="ts-item">
                        <span class="ts-label">{{ config.lang.active === 'en' ? 'Peak Bucket' : '窗口峰值桶' }}</span>
                        <span class="ts-val font-mono"> {{ trendSummary.peakLabel }} ({{ trendSummary.peakValue.toLocaleString() }}) </span>
                    </div>
                </div>
                <div ref="trendRef" class="chart-body chart-body-lg"></div>
            </div>

            <div class="chart-card">
                <div class="card-header">
                    <div class="card-title">
                        {{ $t('dashboard.trafficSource') }}
                        <el-popover trigger="click" placement="top" :width="420" popper-class="rm-help-popper">
                            <template #reference>
                                <button
                                    type="button"
                                    class="tip-q"
                                    :aria-label="config.lang.active === 'en' ? 'Warrant breakdown definitions' : '立据类型口径说明'"
                                >
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="config.lang.active === 'en'">
                                    Share of the three memory layers in the local vault: L1 Background document/code chunks, L2 Episodic session
                                    records, and L3 active recall crystallizations.
                                </p>
                                <p v-else>直接统计本地金库的三层资产存量分布：L1 背景文档与代码分块、L2 情景会话记录、L3 认知结晶（有效条目）。</p>
                            </div>
                        </el-popover>
                    </div>
                    <el-tag size="small" round type="primary">{{ rmData.scope }}</el-tag>
                </div>
                <div ref="sourceRef" class="chart-body chart-body-md"></div>
            </div>
        </div>

        <!-- ==================== 图表行 ==================== -->
        <div class="chart-row">
            <div class="chart-card">
                <div class="card-header">
                    <div class="card-title">
                        {{ $t('dashboard.categorySales') }}
                        <el-popover trigger="click" placement="top" :width="420" popper-class="rm-help-popper">
                            <template #reference>
                                <button
                                    type="button"
                                    class="tip-q"
                                    :aria-label="config.lang.active === 'en' ? 'Workspace distribution definitions' : '活跃工作区分布说明'"
                                >
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="config.lang.active === 'en'">
                                    Top 6 active workspaces (loci): memory units grouped by the project or directory name recorded at ingest, covering
                                    L1 background chunks and L2 session records. Matches the `[workspace]` filter used in `recall`.
                                </p>
                                <p v-else>
                                    活跃工作区 Top 6：按入库时记录的项目或目录名称（Locus）聚合的记忆单元数量（含 L1 背景分块与 L2
                                    会话记录），与检索时的 `[工作区]` 过滤范围一致。
                                </p>
                            </div>
                        </el-popover>
                    </div>
                    <el-tag size="small" round>{{ $t('dashboard.thisMonth') }}</el-tag>
                </div>
                <div ref="salesRef" class="chart-body chart-body-md"></div>
                <div v-if="rmData.top_predicates.length === 0" class="chart-empty-tip">
                    {{ config.lang.active === 'en' ? 'No active workspaces in current scope' : '当前作用域暂无活跃工作区记录' }}
                </div>
            </div>

            <div class="chart-card">
                <div class="card-header">
                    <div class="card-title">
                        {{ $t('dashboard.serverLoad') }}
                        <el-popover trigger="click" placement="top" :width="420" popper-class="rm-help-popper">
                            <template #reference>
                                <button
                                    type="button"
                                    class="tip-q"
                                    :aria-label="config.lang.active === 'en' ? 'Health gauge definitions' : '健康度指标口径说明'"
                                >
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="config.lang.active === 'en'">
                                    Gauge: Composite Vault Engineering Health Score (0–100%): weighted from L2 session index readiness (45%), 3-layer
                                    addressable provenance integrity (35%), and L3 crystallization freshness (20%).
                                </p>
                                <p v-else>
                                    金库工程健康度仪表盘（0~100%）：由 L2 会话索引就绪率（45%）、三层可寻址溯源完整率（35%）与 L3
                                    认知结晶鲜活率（20%）加权合成。≥85% 为健康（绿），60~85% 为索引构建中（黄）。
                                </p>
                                <p v-if="config.lang.active === 'en'">
                                    Index Ready: Percentage of L2 session pointers with a built full-text retrieval index.
                                </p>
                                <p v-else>L2 索引就绪：已完成检索加速索引构建的 L2 会话记录占比。</p>
                                <p v-if="config.lang.active === 'en'">
                                    Provenance: Percentage of active L1/L2/L3 units carrying verifiable file paths, session coordinates, or cited unit
                                    anchors.
                                </p>
                                <p v-else>可寻址溯源：三层活跃记忆中具备可核查文件路径、会话坐标或引用单元锚点的完整比例。</p>
                                <p v-if="config.lang.active === 'en'">
                                    Stale Crystallizations: Count of L3 entries awaiting automatic refresh on the next recall after underlying source
                                    updates.
                                </p>
                                <p v-else>陈旧结晶：因底层文档或会话更新而待下次检索自动刷新的 L3 记录条数（0 表示全部鲜活）。</p>
                            </div>
                        </el-popover>
                    </div>
                    <el-tag size="small" round :type="rmData.conflicts_proposed === 0 ? 'success' : 'warning'">
                        {{
                            rmData.conflicts_proposed === 0
                                ? $t('dashboard.stable')
                                : config.lang.active === 'en'
                                  ? `${rmData.conflicts_proposed} Stale Noetic`
                                  : `${rmData.conflicts_proposed} 条陈旧结晶`
                        }}
                    </el-tag>
                </div>
                <div ref="gaugeRef" class="chart-body chart-body-gauge"></div>
                <div class="gauge-breakdown">
                    <div class="gb-pill">
                        <span class="gb-k">{{ config.lang.active === 'en' ? 'Index Ready' : 'L2 索引就绪' }}</span>
                        <span class="gb-v text-success">{{ rmData.high_conf_ratio }}%</span>
                    </div>
                    <div class="gb-pill">
                        <span class="gb-k">{{ config.lang.active === 'en' ? 'Provenance' : '可寻址溯源' }}</span>
                        <span class="gb-v text-primary">{{ rmData.anchor_full_ratio }}%</span>
                    </div>
                    <div class="gb-pill">
                        <span class="gb-k">{{ config.lang.active === 'en' ? 'Stale Noetic' : '陈旧结晶' }}</span>
                        <span class="gb-v">{{ rmData.conflicts_proposed }}</span>
                    </div>
                </div>
            </div>

            <div class="chart-card">
                <div class="card-header">
                    <div class="card-title">
                        {{ $t('dashboard.capabilityRadar') }}
                        <el-popover trigger="click" placement="top" :width="440" popper-class="rm-help-popper">
                            <template #reference>
                                <button
                                    type="button"
                                    class="tip-q"
                                    :aria-label="config.lang.active === 'en' ? 'Radar chart definitions' : '能力雷达口径说明'"
                                >
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="config.lang.active === 'en'">
                                    University of Cambridge ATM-Bench 5D Capability Radar (Main-1013 full set &amp; Hard-31 multi-evidence set):
                                    comparing Rolling Memory (local_plain · 0-Vector/0-LLM) against the Cambridge paper official baseline (ATM-RAG:
                                    Qwen3-Embedding-4B + Qwen3-Reranker-4B) across Official Recall@10, Open-Ended QA, List Recall, Number EM, and
                                    End-to-End Hybrid QS.
                                </p>
                                <p v-else>
                                    剑桥大学 ATM-Bench 五维能力雷达（支持切换 Main-1013 全量主榜与 Hard-31 高难多跳榜）：直读本地明文（local_plain ·
                                    零向量/零大模型）实测读数，与剑桥论文官方基准（ATM-RAG：Qwen3-Embedding-4B + Qwen3-Reranker-4B）在官方
                                    Recall@10、开放指称（Open-End）、列表枚举（List Recall）、数值计算（Number EM）与端到端综合 QS
                                    五个正交维度同台对比。
                                </p>
                            </div>
                        </el-popover>
                    </div>
                    <div class="radar-actions">
                        <div class="trend-ranges">
                            <button type="button" class="range-chip" :class="{ 'is-active': radarTier === 'main' }" @click="setRadarTier('main')">
                                Main-1013
                            </button>
                            <button type="button" class="range-chip" :class="{ 'is-active': radarTier === 'hard' }" @click="setRadarTier('hard')">
                                Hard-31
                            </button>
                        </div>
                        <el-tooltip
                            :content="
                                config.lang.active === 'en'
                                    ? `${activeRadarCurrentLabel ? activeRadarCurrentLabel + ' vs ' + activeRadarBaselineLabel + ' · ' : ''}Click to open Cambridge ATM-Bench Leaderboard`
                                    : `${activeRadarCurrentLabel ? activeRadarCurrentLabel + ' vs ' + activeRadarBaselineLabel + ' · ' : ''}点击前往「剑桥 ATM-Bench 榜单」查看完整官方基线对比`
                            "
                            placement="bottom"
                            :enterable="false"
                            :hide-after="0"
                            :show-after="300"
                        >
                            <el-tag size="small" round type="warning" class="meta-clickable radar-tag" @click="goEvalPage">
                                {{ radarTagText }}
                            </el-tag>
                        </el-tooltip>
                    </div>
                </div>
                <div ref="radarRef" class="chart-body chart-body-md"></div>
                <div v-if="!rmData.radar_available" class="chart-empty-tip">
                    {{ $t('dashboard.radarEmpty') }}
                </div>
            </div>
        </div>

        <!-- ==================== 实时命令行监控窗口 ==================== -->
        <div class="chart-card live-console-card">
            <div class="card-header">
                <div class="card-title">
                    {{
                        config.lang.active === 'en'
                            ? `Live MCP Recall Ledger & Vault Stream (${pollIntervalKey} Auto-Sync)`
                            : `实时命令行监控流 (MCP Recall 检索流水 & 最新写入记忆单元 · ${pollIntervalKey} 实时刷新)`
                    }}
                    <el-popover trigger="click" placement="top" :width="460" popper-class="rm-help-popper">
                        <template #reference>
                            <button
                                type="button"
                                class="tip-q"
                                :aria-label="config.lang.active === 'en' ? 'Live console panes definition' : '实时监控窗口口径说明'"
                            >
                                ?
                            </button>
                        </template>
                        <div class="rm-help-body">
                            <p v-if="config.lang.active === 'en'">
                                Left pane: real-time MCP recall execution stream from the local recall ledger. Right pane: latest active memory units
                                written to the local SQLite vault (click any row to inspect in Memory Governance).
                            </p>
                            <p v-else>
                                左窗格：实时展示本地召回账本中的 MCP recall 检索调用流水；右窗格：展示当前 Scope
                                最新写入的活跃记忆单元（点击任意行可跳转记忆管理查看原文切片）。
                            </p>
                        </div>
                    </el-popover>
                </div>
                <div class="console-actions">
                    <div class="trend-ranges">
                        <el-tooltip
                            v-for="opt in pollOptions"
                            :key="opt.key"
                            :content="opt.tip"
                            placement="top"
                            :enterable="false"
                            :hide-after="0"
                            :show-after="350"
                        >
                            <button
                                type="button"
                                class="range-chip"
                                :class="{ 'is-active': pollIntervalKey === opt.key }"
                                @click="setPollInterval(opt.key)"
                            >
                                {{ opt.label }}
                            </button>
                        </el-tooltip>
                    </div>
                    <div class="trend-ranges">
                        <el-tooltip
                            v-for="ef in hookFilterList"
                            :key="ef.value"
                            :content="ef.tip"
                            placement="top"
                            :enterable="false"
                            :hide-after="0"
                            :show-after="350"
                        >
                            <button
                                type="button"
                                class="range-chip"
                                :class="{ 'is-active': hookEventFilter === ef.value }"
                                @click="hookEventFilter = ef.value"
                            >
                                {{ ef.label }}
                            </button>
                        </el-tooltip>
                    </div>
                    <el-tag size="small" :type="dataStale ? 'danger' : 'success'" effect="dark" round>
                        {{ liveBadgeText }}
                    </el-tag>
                    <el-button size="small" type="primary" plain @click="terminal.toggle(true)">
                        <Icon :size="14" name="lucide-terminal" style="margin-right: 4px" />
                        {{ config.lang.active === 'en' ? 'Open CLI Terminal Modal' : '打开命令行终端弹窗' }}
                    </el-button>
                    <el-button size="small" plain @click="goGraphPage">
                        <Icon :size="14" name="lucide-git-graph" style="margin-right: 4px" />
                        {{ config.lang.active === 'en' ? 'Open Memory Graph' : '进入记忆图谱' }}
                    </el-button>
                </div>
            </div>
            <div class="console-grid">
                <div class="console-pane">
                    <div class="pane-head">
                        <span class="dot red"></span>
                        <span class="dot yellow"></span>
                        <span class="dot green"></span>
                        <span class="pane-title">$ rolling-memory recent --limit 12 (Live MCP Recall Stream)</span>
                    </div>
                    <div class="pane-body">
                        <div
                            v-for="(ev, idx) in filteredHookEvents"
                            :key="idx"
                            class="term-row"
                            :title="
                                config.lang.active === 'en'
                                    ? `Action=${ev.event} | Outcome=${ev.outcome === 'injected' ? 'hits' : ev.outcome} | Hits=${ev.bands} | Chars=${ev.chars} | Latency=${ev.ms}ms`
                                    : `调用=${ev.event} | 结果=${ev.outcome === 'injected' ? '有效命中' : ev.outcome} | 命中带=${ev.bands} | 交付字符=${ev.chars} | 耗时=${ev.ms}ms`
                            "
                        >
                            <span class="term-ts">[{{ ev.time_text }}]</span>
                            <span class="term-badge" :class="'ev-' + ev.event">[MCP:{{ ev.event }}]</span>
                            <span class="term-outcome" :class="'out-' + ev.outcome"
                                >outcome={{ ev.outcome === 'injected' ? 'hits' : ev.outcome }}</span
                            >
                            <span v-if="ev.event === 'recall'" class="term-kv"> hits={{ ev.bands }} chars={{ ev.chars }} latency={{ ev.ms }}ms </span>
                            <span v-else class="term-kv"> imported={{ ev.imported }} replayed={{ ev.replayed }} written={{ ev.written }} </span>
                        </div>
                        <div v-if="filteredHookEvents.length === 0" class="term-empty">
                            {{ config.lang.active === 'en' ? '-- no matching recall events --' : '-- 暂无匹配的检索记录 --' }}
                        </div>
                    </div>
                </div>
                <div class="console-pane">
                    <div class="pane-head">
                        <span class="dot red"></span>
                        <span class="dot yellow"></span>
                        <span class="dot green"></span>
                        <span class="pane-title">$ rolling-memory status --db ~/.local/share/rolling-memory/memory.db</span>
                    </div>
                    <div class="pane-body">
                        <div
                            v-for="item in rmData.recent_assertions"
                            :key="item.id"
                            class="term-row term-clickable"
                            :title="
                                config.lang.active === 'en'
                                    ? `Click to view this memory · ID=${item.id} · Anchor=${item.anchor} · Confidence=${item.confidence.toFixed(2)}\n${item.rendered}`
                                    : `点击查看此记忆 · ID=${item.id} · 证据锚=${item.anchor} · 置信度=${item.confidence.toFixed(2)}\n${item.rendered}`
                            "
                            @click="goRecentMemory(item.id)"
                        >
                            <span class="term-ts">[{{ item.anchor }}]</span>
                            <span class="term-badge ev-write">[conf={{ item.confidence.toFixed(2) }}]</span>
                            <span class="term-kv">{{ item.rendered }}</span>
                        </div>
                        <div v-if="rmData.recent_assertions.length === 0" class="term-empty">
                            {{ config.lang.active === 'en' ? '-- no active memory units in scope --' : '-- 当前作用域暂无活跃记忆单元 --' }}
                        </div>
                    </div>
                </div>
            </div>
        </div>
    </div>
</template>

<script setup lang="ts">
import DashboardHeaderIllustration from '@/assets/svg/dashboard-illustration.svg'
import router from '@/router'
import { useAdminInfo } from '@/stores/adminInfo'
import { useConfig } from '@/stores/config'
import { useTerminal } from '@/stores/terminal'
import { MonitorSocket } from '@/utils/monitorSocket'
import request from '@/utils/request'
import { Local } from '@/utils/storage'
import { useEventListener } from '@vueuse/core'
import type { ECharts, EChartsOption } from 'echarts'
import * as echarts from 'echarts'
import { debounce, padStart } from 'lodash-es'
import { computed, nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const config = useConfig()
const adminInfo = useAdminInfo()
const terminal = useTerminal()
const { t } = useI18n()
const workTimerKey = 'ai-go-dashboard-work-timer'

const modeLabel = computed(() => {
    return rmData.mode === 'cloud' ? t('dashboard.cloudPulse') : t('dashboard.localPlain')
})

const modeTooltip = computed(() => {
    return rmData.mode === 'cloud' ? t('dashboard.cloudPulseTip') : t('dashboard.localPlainTip')
})

const goGraphPage = () => {
    router.push('/admin/dashboard/graph')
}
const goRoiPage = () => {
    router.push('/admin/dashboard/roi')
}
const goEvalPage = () => {
    router.push('/admin/plugin/rolling-memory/atm-leaderboard')
}
const goRecentMemory = (id: string) => {
    router.push({ path: '/admin/plugin/rolling-memory/index', query: { unit_id: id } })
}

// ==================== 工具函数 ====================

/**
 * 在一位数字的前面补零
 */
const pad = (n: number) => padStart(String(n), 2, '0')

/**
 * 构造一条底部渐变面积色（同色系深浅过渡）
 */
const makeAreaGradient = (color: string, from = 0.35, to = 0) =>
    new echarts.graphic.LinearGradient(0, 0, 0, 1, [
        { offset: 0, color: withAlpha(color, from) },
        { offset: 1, color: withAlpha(color, to) },
    ])

/**
 * 构造一条从上到下渐变的柱状色（顶饱和 → 底通透）
 */
const makeBarGradient = (color: string) =>
    new echarts.graphic.LinearGradient(0, 0, 0, 1, [
        { offset: 0, color },
        { offset: 1, color: color + '55' },
    ])

/**
 * 把 hex 或 rgb 色转成 rgba 字符串
 */
const withAlpha = (color: string, alpha: number) => {
    if (color.startsWith('#')) {
        const hex = color.slice(1)
        const bigint = parseInt(hex.length === 3 ? hex.replace(/./g, (c) => c + c) : hex, 16)
        const r = (bigint >> 16) & 255
        const g = (bigint >> 8) & 255
        const b = bigint & 255
        return `rgba(${r}, ${g}, ${b}, ${alpha})`
    }
    return color
}

// ==================== 顶部欢迎条 ====================

const nowTime = ref('')
const today = computed(() =>
    new Date().toLocaleDateString(config.lang.active === 'en' ? 'en-US' : 'zh-CN', {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        weekday: 'long',
    })
)
const greeting = computed(() => {
    const h = new Date().getHours()
    if (h < 6) return t('dashboard.greetingNight')
    if (h < 11) return t('dashboard.greetingMorning')
    if (h < 13) return t('dashboard.greetingNoon')
    if (h < 18) return t('dashboard.greetingAfternoon')
    return t('dashboard.greetingEvening')
})

// ==================== 今日工作计时 ====================

// 持久化到 localStorage，跨页面刷新保持；跨 0 点自动重置
type WorkState = {
    date: string // yyyy-mm-dd
    accumulated: number // 已累计工作秒数（不含正在计时的当前段）
    startAt: number | null // 当前段起点时间戳（ms），null 表示暂停
}

const todayKey = () => {
    const d = new Date()
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

const loadWorkState = (): WorkState => {
    const cached = Local.get(workTimerKey) as WorkState | null
    if (cached && cached.date === todayKey()) return cached
    // 新的一天，或首次进入：默认启动计时
    return { date: todayKey(), accumulated: 0, startAt: Date.now() }
}

const saveWorkState = () => Local.set(workTimerKey, workState)

const workState: WorkState = reactive(loadWorkState())
const workSeconds = ref(0)
const working = computed(() => workState.startAt !== null)
const workUnits = computed(() => [
    { label: t('dashboard.hours'), value: Math.floor(workSeconds.value / 3600) },
    { label: t('dashboard.minutes'), value: Math.floor((workSeconds.value % 3600) / 60) },
    { label: t('dashboard.seconds'), value: workSeconds.value % 60 },
])

const updateWorkTime = (now: Date) => {
    // 跨 0 点重置
    if (workState.date !== todayKey()) {
        workState.date = todayKey()
        workState.accumulated = 0
        workState.startAt = now.getTime()
        saveWorkState()
    }
    const running = workState.startAt !== null ? Math.floor((now.getTime() - workState.startAt) / 1000) : 0
    workSeconds.value = workState.accumulated + running
}

const toggleWork = () => {
    const now = Date.now()
    if (workState.startAt !== null) {
        // 暂停：把当前段并入累计
        workState.accumulated += Math.floor((now - workState.startAt) / 1000)
        workState.startAt = null
    } else {
        // 继续
        workState.startAt = now
    }
    saveWorkState()
    updateWorkTime(new Date())
}

// ==================== 每秒 tick（同时更新时钟与工作计时） ====================

let clockTimer: ReturnType<typeof setInterval> | null = null

const tick = () => {
    const d = new Date()
    nowTime.value = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
    updateWorkTime(d)
}

// ==================== Rolling-Memory 实时控制台状态 ====================

interface FacetCount {
    value: string
    count: number
}

interface DashboardTrendSeries {
    dates: string[]
    assertions: number[]
    activity: number[]
    recalls?: number[]
    traces?: number[]
    l1_added?: number[]
    l2_added?: number[]
    l3_added?: number[]
}

interface HookEventRow {
    ts: number
    time_text: string
    event: string
    injected: number
    bands: number
    raw: number
    ms: number
    chars: number
    imported: number
    replayed: number
    written: number
    outcome: string
}

interface RecentAssertionRow {
    id: string
    rendered: string
    confidence: number
    anchor: string
    status: string
}

interface EfficacyFunnel {
    high_value_adopted: number
    context_assisted: number
    dedup_suppressed: number
    silence_filtered: number
    noise_injected: number
}

interface HarnessEfficacyRow {
    id: string
    title: string
    detected: boolean
    installed: boolean
    hooks_enabled: boolean
    sessions: number
    turns: number
    injections: number
    adopted: number
    silenced: number
    adopt_rate: number
    silence_rate: number
    saved_rounds: number
    net_saved_tokens: number
    avg_latency_ms: number
    status: string
}

interface SessionEvaluationRow {
    session_id: string
    harness: string
    workspace: string
    prompt: string
    outcome: string
    verdict_label: string
    attribution_source?: string
    recalled_summary: string
    matched_anchors: string[]
    echo_excluded?: number
    has_code_coord: boolean
    causal_verified?: boolean
    self_healed?: number
    model_requests: number
    tool_calls: number
    explore_steps?: number
    round_cost_tokens?: number
    saved_rounds: number
    injected_tokens: number
    saved_tokens: number
    net_tokens: number
    latency_ms: number
    recorded_at: number
    time_text: string
}

interface SessionContinuityPayload {
    ready: boolean
    active_locus: string
    where_spatial_bands: number
    episodic_tail_traces: number
    active_thread_assertions: number
    ledger_protected_bands: number
    last_tail_preview: string
}

interface VaultRoiSummary {
    schema_era: string
    total_units: number
    background_dirs: number
    background_files: number
    background_chunks: number
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
    audited_recalls?: number
    adopted_recalls?: number
    adopted_tool_count?: number
    adopted_context_count?: number
    adoption_rate_pct?: number
}

/** 事后采纳审计的窗口读数（1h/24h/7d/30d/all），随 dashboard payload 的 efficacy 节点下发。 */
interface AdoptionWindowRow {
    key: string
    audited_recalls: number
    adopted_recalls: number
    adopted_tool_count: number
    adopted_context_count: number
    adoption_rate_pct: number
}

interface EfficacyReportPayload {
    status?: 'pending' | 'ready' | 'stale' | 'error' | 'unavailable'
    reason?: string
    vault_roi?: VaultRoiSummary
    adoption_windows?: AdoptionWindowRow[]
    scope: string
    evaluated_at: string
    total_hook_runs: number
    total_sessions: number
    evaluated_turns: number
    injected_count: number
    silenced_count: number
    adopted_count: number
    code_coord_hits: number
    novel_coord_hits?: number
    causal_direct_hits?: number
    navigation_direct_hits?: number
    guardrail_hits?: number
    continuity_hits?: number
    echo_excluded_count?: number
    self_healed_count?: number
    writeback_count?: number
    adopt_rate: number
    silence_rate: number
    avg_saved_rounds: number
    total_saved_rounds: number
    total_saved_seconds?: number
    injected_tokens: number
    saved_fresh_tokens?: number
    saved_cached_tokens?: number
    gross_saved_tokens: number
    billed_net_saved_tokens?: number
    billed_token_roi?: number
    net_saved_tokens: number
    net_token_roi: number
    today_turns?: number
    today_injected_count?: number
    today_adopted_count?: number
    today_saved_rounds?: number
    today_saved_seconds?: number
    today_injected_tokens?: number
    today_saved_fresh_tokens?: number
    today_saved_cached_tokens?: number
    today_gross_saved_tokens?: number
    today_billed_net_saved_tokens?: number
    today_billed_token_roi?: number
    today_net_saved_tokens?: number
    today_net_token_roi?: number
    week_saved_rounds?: number
    week_net_saved_tokens?: number
    avg_latency_ms: number
    efficacy_score: number
    grade: string
    continuity?: SessionContinuityPayload
    funnel: EfficacyFunnel
    harnesses: HarnessEfficacyRow[]
    sessions: SessionEvaluationRow[]
}

interface DashboardPayload {
    engine_online: boolean
    version: string
    mode: string
    scope: string
    db_size_mb: number
    updated_at: string
    available_scopes?: FacetCount[]
    active_assertions: number
    total_assertions: number
    l1_total?: number
    l2_total?: number
    l3_total?: number
    high_conf_count: number
    private_count: number
    high_conf_ratio: number
    assertion_spark: number[]
    total_nodes: number
    total_traces: number
    user_traces: number
    anchor_full_ratio: number
    node_spark: number[]
    last_recall_ms: number
    avg_recall_ms: number
    latency_trend: number
    latency_spark: number[]
    total_hook_events: number
    injected_events: number
    inject_ratio: number
    hook_spark: number[]
    trend_1h: DashboardTrendSeries
    trend_1d: DashboardTrendSeries
    trend_7d: DashboardTrendSeries
    trend_30d: DashboardTrendSeries
    warrant_dist: FacetCount[]
    top_predicates: FacetCount[]
    health_score: number
    avg_confidence: number
    conflicts_proposed: number
    radar_available: boolean
    radar_current: number[]
    radar_baseline: number[]
    radar_current_label: string
    radar_current_label_en?: string
    radar_baseline_label: string
    radar_baseline_label_en?: string
    radar_hard_current?: number[]
    radar_hard_baseline?: number[]
    radar_hard_current_label?: string
    radar_hard_current_label_en?: string
    radar_hard_baseline_label?: string
    radar_hard_baseline_label_en?: string
    recent_events: HookEventRow[]
    recent_assertions: RecentAssertionRow[]
    efficacy?: EfficacyReportPayload
}

const currentScope = ref('default')
const dynamicScopes = computed<string[]>(() => {
    const list = (rmData.available_scopes || []).map((s) => s.value).filter(Boolean)
    return list.length > 0 ? list : ['default']
})

// 初始态全部为零值：首帧到第一次响应之间不出数，避免演示数字被当成真实读数
const rmData = reactive<DashboardPayload>({
    engine_online: false,
    version: '',
    mode: '',
    scope: 'default',
    db_size_mb: 0,
    updated_at: '--:--:--',
    available_scopes: [],
    active_assertions: 0,
    total_assertions: 0,
    l1_total: 0,
    l2_total: 0,
    l3_total: 0,
    high_conf_count: 0,
    private_count: 0,
    high_conf_ratio: 0,
    assertion_spark: [],
    total_nodes: 0,
    total_traces: 0,
    user_traces: 0,
    anchor_full_ratio: 0,
    node_spark: [],
    last_recall_ms: 0,
    avg_recall_ms: 0,
    latency_trend: 0,
    latency_spark: [],
    total_hook_events: 0,
    injected_events: 0,
    inject_ratio: 0,
    hook_spark: [],
    trend_1h: {
        dates: [],
        assertions: [],
        activity: [],
        recalls: [],
        traces: [],
    },
    trend_1d: {
        dates: [],
        assertions: [],
        activity: [],
        recalls: [],
        traces: [],
    },
    trend_7d: {
        dates: [],
        assertions: [],
        activity: [],
        recalls: [],
        traces: [],
    },
    trend_30d: {
        dates: [],
        assertions: [],
        activity: [],
        recalls: [],
        traces: [],
    },
    warrant_dist: [],
    top_predicates: [],
    health_score: 0,
    avg_confidence: 0,
    conflicts_proposed: 0,
    radar_available: false,
    radar_current: [],
    radar_baseline: [],
    radar_current_label: '',
    radar_current_label_en: '',
    radar_baseline_label: '',
    radar_baseline_label_en: '',
    radar_hard_current: [],
    radar_hard_baseline: [],
    radar_hard_current_label: '',
    radar_hard_current_label_en: '',
    radar_hard_baseline_label: '',
    radar_hard_baseline_label_en: '',
    recent_events: [],
    recent_assertions: [],
})

import { ElMessageBox } from 'element-plus'

const cycleScope = () => {
    const scopes = dynamicScopes.value
    const idx = scopes.indexOf(currentScope.value)
    currentScope.value = scopes[(idx + 1) % scopes.length]!
    if (pollIntervalKey.value === 'stream') {
        restartPollTimer()
    } else {
        fetchRealtimeData(true)
    }
}

const showPulseApplyTip = () => {
    const isEnLang = config.lang.active === 'en'
    ElMessageBox.alert(
        isEnLang
            ? 'Pulse plain (pulse_plain) sends pulses and text; pulse cipher (pulse_cipher) sends encrypted pulses without text. To request preview access, email pick@psychologylake.org.cn (support: support@psychologylake.org.cn).'
            : '脉冲明文（pulse_plain）同时发送脉冲与原文；脉冲密文（pulse_cipher）仅发送密文脉冲，不发送原文。如需申请预览，请发邮件至 pick@psychologylake.org.cn（支持：support@psychologylake.org.cn）。',
        isEnLang ? 'Preview access' : '申请预览',
        { confirmButtonText: isEnLang ? 'OK' : '确定', type: 'info' }
    )
}

type HookEventFilterKey = 'all' | 'hits' | 'empty'
const hookEventFilter = ref<HookEventFilterKey>('all')
const hookFilterList = computed(() => {
    const isEn = config.lang.active === 'en'
    return [
        {
            value: 'all' as const,
            label: 'ALL',
            tip: isEn ? 'Show all MCP recall calls in the fact ledger' : '显示事实账本中的全部 MCP recall 检索记录',
        },
        {
            value: 'hits' as const,
            label: isEn ? 'hits' : '有效命中',
            tip: isEn
                ? 'Effective recall hits: MCP recall returned verified evidence and crystallized L3 Noetic'
                : '有效命中：MCP recall 命中底层 L1/L2 证据并交付给 Agent（同时自动巩固结晶 L3）',
        },
        {
            value: 'empty' as const,
            label: isEn ? 'empty' : '无命中',
            tip: isEn
                ? 'Empty / silent recalls: no matching evidence above threshold in current vault'
                : '无命中 / 空检索：当前金库中未找到满足特异性门限的匹配证据',
        },
    ]
})
const filteredHookEvents = computed(() => {
    const list = rmData.recent_events || []
    if (hookEventFilter.value === 'all') return list
    if (hookEventFilter.value === 'hits') {
        return list.filter((ev) => ev.injected > 0 || ev.outcome === 'injected' || ev.outcome === 'injected+nudge' || ev.outcome === 'hits')
    }
    return list.filter((ev) => ev.injected === 0 && ev.outcome !== 'injected' && ev.outcome !== 'injected+nudge' && ev.outcome !== 'hits')
})

const defaultEfficacy: EfficacyReportPayload = {
    scope: 'mem:self',
    evaluated_at: '--',
    total_hook_runs: 0,
    total_sessions: 0,
    evaluated_turns: 0,
    injected_count: 0,
    silenced_count: 0,
    adopted_count: 0,
    code_coord_hits: 0,
    novel_coord_hits: 0,
    causal_direct_hits: 0,
    echo_excluded_count: 0,
    self_healed_count: 0,
    writeback_count: 0,
    adopt_rate: 0,
    silence_rate: 0,
    avg_saved_rounds: 0,
    total_saved_rounds: 0,
    injected_tokens: 0,
    gross_saved_tokens: 0,
    net_saved_tokens: 0,
    net_token_roi: 0,
    avg_latency_ms: 0,
    efficacy_score: 0,
    grade: '-',
    funnel: {
        high_value_adopted: 0,
        context_assisted: 0,
        dedup_suppressed: 0,
        silence_filtered: 0,
        noise_injected: 0,
    },
    harnesses: [],
    sessions: [],
}

const efficacyData = computed<EfficacyReportPayload>(() => rmData.efficacy || defaultEfficacy)
const vaultRoi = computed(() => efficacyData.value.vault_roi)

// 当结算数据源不可用时，若有金库工程 ROI，则展示真实工程指标；否则显示「—」。
const efficacyUnavailable = computed(() => efficacyData.value.status === 'unavailable' && !vaultRoi.value)

let inFlight = false

const fetchRealtimeData = async (force = false) => {
    if (!force && typeof document !== 'undefined' && document.visibilityState === 'hidden') {
        return
    }
    if (inFlight && !force) {
        return
    }
    inFlight = true
    try {
        const res = await request<DashboardPayload>(
            {
                url: '/admin/plugin/rolling-memory/dashboard',
                method: 'POST',
                data: { scope: currentScope.value },
            },
            { showErrorMessage: false, showNetworkErrorMessage: false, cancelDuplicate: false }
        )
        const d = res.data?.data
        if (d) {
            failStreak.value = 0
            Object.assign(rmData, d)
            nextTick(() => {
                renderAll(false)
            })
        }
    } catch {
        // 静默容错但计失败次数：连续失败后徽标转为「数据已过期」，不再假装 LIVE
        failStreak.value += 1
    } finally {
        inFlight = false
    }
}

// ==================== 实时事件流开关（同源 WebSocket 房间广播 · 零前端轮询） ====================

type PollIntervalKey = 'stream' | 'off'
const pollIntervalStorageKey = 'ai-go-dashboard-poll-interval'
const pollOptions = computed<{ key: PollIntervalKey; label: string; tip: string }[]>(() => {
    const isEn = config.lang.active === 'en'
    return [
        {
            key: 'stream',
            label: '⚡ Stream',
            tip: isEn
                ? 'WebSocket Real-Time Event Stream: 250ms os.Stat file-change watcher on SQLite WAL & log (0 SQL queries when idle, sub-second push on write)'
                : 'WebSocket 实时事件流模式：后端以 250ms os.Stat 监听 SQLite WAL 与日志变更（静默期 0 次 SQL 查询，有新写入或召回时亚秒级推送）',
        },
        {
            key: 'off',
            label: isEn ? 'Manual' : '手动',
            tip: isEn ? 'Pause real-time stream; click Refresh to fetch on demand' : '暂停实时流推送，点击手动刷新按需拉取',
        },
    ]
})
const loadPollIntervalKey = (): PollIntervalKey => {
    const saved = Local.get(pollIntervalStorageKey) as string | null
    if (saved === 'off') {
        return 'off'
    }
    return 'stream'
}
const pollIntervalKey = ref<PollIntervalKey>(loadPollIntervalKey())

let wsSocket: MonitorSocket | null = null
const streamConnected = ref(false)

const stopDashboardStream = () => {
    if (wsSocket) {
        wsSocket.stop()
        wsSocket = null
    }
    streamConnected.value = false
}

const startDashboardStream = () => {
    stopDashboardStream()
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') {
        return
    }
    const token = adminInfo.token
    if (!token) return
    const scope = String(currentScope.value || '').trim() || 'default'
    wsSocket = new MonitorSocket({
        token,
        room: `rm/dashboard:${scope}`,
        onStatus: (st) => {
            streamConnected.value = st === 'connected'
            if (st === 'connected') {
                failStreak.value = 0
            }
        },
        onMessage: (env: any) => {
            if (env?.event === 'dashboard' && env.data) {
                const parsed = env.data as DashboardPayload
                failStreak.value = 0
                streamConnected.value = true
                Object.assign(rmData, parsed)
                nextTick(() => {
                    renderAll(false)
                })
            }
        },
    })
    wsSocket.start()
}

const restartPollTimer = () => {
    if (pollIntervalKey.value === 'stream') {
        startDashboardStream()
        return
    }
    stopDashboardStream()
}

const setPollInterval = (key: PollIntervalKey) => {
    pollIntervalKey.value = key
    Local.set(pollIntervalStorageKey, key)
    restartPollTimer()
    if (key !== 'stream') {
        fetchRealtimeData(true)
    }
}

const failStreak = ref(0)
const dataStale = computed(() => failStreak.value >= 2)
const liveBadgeText = computed(() => {
    if (dataStale.value) {
        return `${t('dashboard.liveStale')} (${rmData.updated_at})`
    }
    const modeTag = pollIntervalKey.value === 'stream' ? '⚡ STREAM' : 'MANUAL'
    return `${modeTag} (${rmData.updated_at})`
})
type RadarTierKey = 'main' | 'hard'
const radarTier = ref<RadarTierKey>('main')
const setRadarTier = (tier: RadarTierKey) => {
    radarTier.value = tier
    renderRadar(true)
}

const activeRadarCurrent = computed<number[]>(() => {
    if (radarTier.value === 'hard' && rmData.radar_hard_current?.length === 5) {
        return rmData.radar_hard_current
    }
    return rmData.radar_current || []
})
const activeRadarBaseline = computed<number[]>(() => {
    if (radarTier.value === 'hard' && rmData.radar_hard_baseline?.length === 5) {
        return rmData.radar_hard_baseline
    }
    return rmData.radar_baseline || []
})
const activeRadarCurrentLabel = computed(() => {
    const isEn = config.lang.active === 'en'
    if (radarTier.value === 'hard') {
        return (isEn ? rmData.radar_hard_current_label_en : rmData.radar_hard_current_label) || rmData.radar_hard_current_label || ''
    }
    return (isEn ? rmData.radar_current_label_en : rmData.radar_current_label) || rmData.radar_current_label || ''
})
const activeRadarBaselineLabel = computed(() => {
    const isEn = config.lang.active === 'en'
    if (radarTier.value === 'hard') {
        return (isEn ? rmData.radar_hard_baseline_label_en : rmData.radar_hard_baseline_label) || rmData.radar_hard_baseline_label || ''
    }
    return (isEn ? rmData.radar_baseline_label_en : rmData.radar_baseline_label) || rmData.radar_baseline_label || ''
})

const radarTagText = computed(() => {
    if (!rmData.radar_available) {
        return t('dashboard.radarNoRun')
    }
    const vals = activeRadarCurrent.value
    const base = activeRadarBaseline.value
    if (vals.length === 5 && base.length === 5) {
        return `R@10 ${vals[0]}% · QS ${vals[4]}% (vs ${base[4]}%)`
    }
    return radarTier.value === 'hard' ? 'ATM Hard-31' : 'ATM Main-1013'
})

// ==================== KPI 卡片数据 ====================

type KpiItem = {
    key: 'user' | 'order' | 'sales' | 'visit'
    title: string
    tooltip: string
    value: string
    valueTip: string
    trend: number
    trendUp: boolean
    kind: 'ratio' | 'delta'
    sub: string
    subTip: string
    icon: string
    spark: number[]
    /** 该卡与事后采纳审计同源时指向 ROI 监控屏（点击整卡跳转）。 */
    toRoi?: boolean
}
const kpiList = computed<KpiItem[]>(() => {
    const isEn = config.lang.active === 'en'
    // 「有效命中」只代表证据已送达；真实有用率＝实质采纳率（事后采纳审计），
    // 与 ROI 监控屏同源：全时段取自金库汇总，近 24 小时取自审计窗口。
    const adoptionAll = vaultRoi.value?.adoption_rate_pct
    const adoption24h = (efficacyData.value.adoption_windows || []).find((w) => w.key === '24h')?.adoption_rate_pct
    const adoptionKnown = typeof adoptionAll === 'number' && typeof adoption24h === 'number'
    return [
        {
            key: 'user',
            title: t('dashboard.totalUsers'),
            tooltip: isEn
                ? 'Active vs Total Memory Units across L1 Background, L2 Episodic, and L3 Noetic. Active units participate in recall; stale L3 entries awaiting re-crystallization are included in the right-hand total.'
                : '活跃 / 总记忆单元（L1 背景分块 + L2 情景对话 + L3 认知结晶）：左侧为现行有效（active）并参与检索召回的单元数；右侧为含待重结晶陈旧条目（stale）的总量。',
            value: `${rmData.active_assertions.toLocaleString()} / ${rmData.total_assertions.toLocaleString()}`,
            valueTip: isEn
                ? `Active: ${rmData.active_assertions} · Total: ${rmData.total_assertions} · L2 Index Ready: ${rmData.high_conf_ratio}%`
                : `现行活跃：${rmData.active_assertions} 条 · 全库总量：${rmData.total_assertions} 条 · L2 索引就绪率：${rmData.high_conf_ratio}%`,
            trend: rmData.high_conf_ratio,
            trendUp: true,
            kind: 'ratio',
            sub: t('dashboard.kpiSubAssertions'),
            subTip: isEn ? 'Percentage of L2 session records with a ready retrieval index' : '已完成检索加速索引构建的 L2 会话记录占比',
            icon: 'el-collection',
            spark: rmData.assertion_spark?.length ? rmData.assertion_spark : [10, 20, 30, 40],
        },
        {
            key: 'order',
            title: t('dashboard.todayOrders'),
            tooltip: isEn
                ? 'Total 3-Layer Vault Units vs L2 Episodic Session Turns. Left is the combined L1+L2+L3 unit count; right is the L2 conversation turn count.'
                : '三层金库总节点 / L2 情景会话轨迹：左侧为 L1 背景分块、L2 情景记录与 L3 认知结晶的总节点数；右侧为自动沉淀的用户会话轨迹（L2 Episodic）条数。',
            value: `${rmData.total_nodes.toLocaleString()} / ${rmData.user_traces.toLocaleString()}`,
            valueTip: isEn
                ? `Total 3-Layer Units: ${rmData.total_nodes} · L2 Episodic Traces: ${rmData.user_traces}`
                : `三层总节点：${rmData.total_nodes} 个 · L2 情景会话记录：${rmData.user_traces} 条`,
            trend: rmData.anchor_full_ratio,
            trendUp: true,
            kind: 'ratio',
            sub: t('dashboard.kpiSubNodes'),
            subTip: isEn
                ? 'Percentage of active L1/L2/L3 units carrying verifiable file paths, session coordinates, or cited unit anchors'
                : '三层活跃记忆中具备可核查文件路径、会话坐标或引用单元锚点的完整率',
            icon: 'el-share',
            spark: rmData.node_spark?.length ? rmData.node_spark : [10, 20, 30, 40],
        },
        {
            key: 'sales',
            title: t('dashboard.salesAmount'),
            tooltip: isEn
                ? '0-LLM Local Recall Latency (ms): End-to-end execution time of SQLite FTS5/BM25 + Shannon Self-Information Continuous Gain + Temporal Window ranking.'
                : '0-LLM 本地检索耗时（毫秒）：本地 SQLite 明文金库（FTS5/BM25 + 香农自信息连续增益 + 时序共振）检索并返回结果的端到端延迟。',
            value: `${rmData.last_recall_ms} ms`,
            valueTip: isEn
                ? `Latest recall: ${rmData.last_recall_ms} ms · Rolling 20-call average: ${rmData.avg_recall_ms} ms`
                : `最近一次召回耗时：${rmData.last_recall_ms} ms · 近 20 次滑动均值：${rmData.avg_recall_ms} ms`,
            trend: rmData.latency_trend,
            trendUp: rmData.latency_trend <= 0,
            kind: 'delta',
            sub: `${t('dashboard.kpiSubLatency')} (${rmData.avg_recall_ms}ms)`,
            subTip: isEn
                ? 'Relative delta between latest recall latency and rolling average (green downward means faster)'
                : '最近一次耗时相对近期均值的变化率（下降/绿色表示检索更快）',
            icon: 'el-timer',
            spark: rmData.latency_spark?.length ? rmData.latency_spark : [210, 215, 205, 211],
        },
        {
            key: 'visit',
            title: t('dashboard.pageVisits'),
            tooltip: isEn
                ? 'MCP `recall` queries and deliveries: total calls made by the agent, and calls that matched and delivered verified evidence. An "effective hit" only means evidence was delivered — not that it was actually used. See the adopted rate (post-hoc adoption audit) for real usefulness.'
                : 'MCP `recall` 检索与交付：Agent 调取 `recall` 工具执行检索的总次数，以及命中底层记忆并交付证据的次数。有效命中只代表「证据已送达」，不等于「是否真的被用上」——真实有用率见本卡的实质采纳率（事后采纳审计）。',
            value: `${rmData.total_hook_events.toLocaleString()} (${rmData.injected_events.toLocaleString()})`,
            valueTip: isEn
                ? `Total MCP Recalls: ${rmData.total_hook_events} · Effective Hits Delivered: ${rmData.injected_events} (${rmData.inject_ratio}%)`
                : `MCP 累计检索：${rmData.total_hook_events} 次 · 有效命中交付：${rmData.injected_events} 次（有效命中率 ${rmData.inject_ratio}%）`,
            trend: rmData.inject_ratio,
            trendUp: true,
            kind: 'ratio',
            sub: adoptionKnown
                ? isEn
                    ? `Hit Rate · Adopted Rate ${adoptionAll}% (24h ${adoption24h}%)`
                    : `有效命中率 · 实质采纳率 ${adoptionAll}%（24h ${adoption24h}%）`
                : t('dashboard.kpiSubHooks'),
            subTip: adoptionKnown
                ? isEn
                    ? `Real usefulness (adopted · post-hoc adoption audit): all-time ${adoptionAll}% (${vaultRoi.value?.adopted_recalls ?? 0}/${vaultRoi.value?.audited_recalls ?? 0} settled; ${vaultRoi.value?.adopted_tool_count ?? 0} direct + ${vaultRoi.value?.adopted_context_count ?? 0} context) · last 24h ${adoption24h}%. Zero-model deterministic anchor-intersection audit over each recall's follow-up tool calls and replies; see ROI Monitor for details.`
                    : `真实有用率（实质采纳 · 事后采纳审计）：全时段 ${adoptionAll}%（${vaultRoi.value?.adopted_recalls ?? 0}/${vaultRoi.value?.audited_recalls ?? 0} 次已结算；工具直达 ${vaultRoi.value?.adopted_tool_count ?? 0} + 事实采纳 ${vaultRoi.value?.adopted_context_count ?? 0}）· 近 24 小时 ${adoption24h}%。口径：对每次召回后的后续工具调用与回复做零模型确定性锚点交集审计；明细见 ROI 监控屏。`
                : isEn
                  ? 'Effective Hit Rate = Recalls Returning Evidence / Total MCP Recall Calls'
                  : '有效命中率 = 命中并交付证据的次数 / MCP recall 检索总次数',
            icon: 'el-connection',
            spark: rmData.hook_spark?.length ? rmData.hook_spark : [15, 25, 35, 45],
            toRoi: true,
        },
    ]
})

// ==================== 图表实例统一管理 ====================

type ChartKey = 'trend' | 'source' | 'sales' | 'gauge' | 'radar'
const trendRef = ref<HTMLElement | null>(null)
const sourceRef = ref<HTMLElement | null>(null)
const salesRef = ref<HTMLElement | null>(null)
const gaugeRef = ref<HTMLElement | null>(null)
const radarRef = ref<HTMLElement | null>(null)
const chartRefs: Record<ChartKey, ReturnType<typeof ref<HTMLElement | null>>> = {
    trend: trendRef,
    source: sourceRef,
    sales: salesRef,
    gauge: gaugeRef,
    radar: radarRef,
}
const charts: Partial<Record<ChartKey, ECharts>> = {}

/**
 * 惰性获取 ECharts 实例；同一 DOM 上重复调用返回同一实例
 */
const ensureChart = (key: ChartKey): ECharts | null => {
    if (charts[key]) return charts[key]!
    const el = chartRefs[key].value
    if (!el) return null
    charts[key] = echarts.init(el)
    return charts[key]!
}

// ==================== Spark（KPI 内嵌迷你图） ====================

const sparkEls: Partial<Record<KpiItem['key'], HTMLElement | null>> = {}
const sparkCharts: Partial<Record<KpiItem['key'], ECharts>> = {}
const sparkSigs: Partial<Record<KpiItem['key'], string>> = {}
const chartSigs: Partial<Record<ChartKey, string>> = {}
const setSparkRef = (el: HTMLElement | null, key: KpiItem['key']) => {
    sparkEls[key] = el
}

const renderSpark = (item: KpiItem, force = false) => {
    const el = sparkEls[item.key]
    if (!el) return
    const sig = `${item.spark.join(',')}|${isDark.value}`
    if (!force && sparkSigs[item.key] === sig && sparkCharts[item.key]) {
        return
    }
    sparkSigs[item.key] = sig
    let chart = sparkCharts[item.key]
    if (!chart) {
        chart = echarts.init(el)
        sparkCharts[item.key] = chart
    }
    chart.setOption({
        animation: false,
        grid: { top: 4, right: 4, bottom: 4, left: 4 },
        xAxis: { type: 'category', show: false, data: item.spark.map((_, i) => i) },
        yAxis: { type: 'value', show: false, scale: true },
        tooltip: { show: false },
        series: [
            {
                type: 'line',
                data: item.spark,
                smooth: true,
                showSymbol: false,
                // 线条细一点、面积渐变淡一点，避免整排 4 个 spark 视觉过重
                lineStyle: { color: '#409eff', width: 1.5 },
                areaStyle: { color: makeAreaGradient('#409eff', 0.25, 0) },
            },
        ],
    })
}

// ==================== 主题（暗/亮） ====================
type ThemeTokens = {
    textPrimary: string
    textSecondary: string
    borderColor: string
    axisLine: string
    splitLine: string
    bg: string
    splitArea: [string, string]
}
const isDark = computed(() => config.layout.dark)
const theme = computed<ThemeTokens>(() =>
    isDark.value
        ? {
              textPrimary: '#e5eaf3',
              textSecondary: '#a3a6ad',
              borderColor: '#414243',
              axisLine: '#4c4d4f',
              splitLine: '#363637',
              bg: '#1d1e1f',
              splitArea: ['#2a2a2b', '#232324'],
          }
        : {
              textPrimary: '#303133',
              textSecondary: '#606266',
              borderColor: '#e4e7ed',
              axisLine: '#dcdfe6',
              splitLine: '#ebeef5',
              bg: '#ffffff',
              splitArea: ['#fafbfc', '#f5f7fa'],
          }
)

/**
 * 主题化的通用 tooltip 配置（appendToBody 脱离 overflow:hidden 卡片裁剪，extraCssText 禁止拦截鼠标事件）
 */
const buildTooltip = (extra: Partial<EChartsOption['tooltip']> = {}): EChartsOption['tooltip'] => ({
    backgroundColor: theme.value.bg,
    borderColor: theme.value.borderColor,
    textStyle: { color: theme.value.textPrimary },
    appendToBody: true,
    extraCssText: 'pointer-events: none; z-index: 2050;',
    ...extra,
})

/**
 * 主题化的通用 x/y 轴配置
 */
const buildAxis = (opt: { hideLine?: boolean } = {}) => ({
    axisLine: opt.hideLine ? { show: false } : { lineStyle: { color: theme.value.axisLine } },
    axisTick: opt.hideLine ? { show: false } : undefined,
    splitLine: { lineStyle: { color: theme.value.splitLine } },
    axisLabel: { color: theme.value.textSecondary },
})

// ==================== 记忆吞吐与检索召回趋势 ====================

type TrendRangeKey = '1h' | '1d' | '7d' | '30d'
const trendRange = ref<TrendRangeKey>('1h')
const rangeList = computed(() => {
    const isEn = config.lang.active === 'en'
    return [
        {
            value: '1h' as const,
            label: t('dashboard.last1Hour'),
            tip: isEn ? 'Last 60 minutes in 5-minute buckets (12 slots)' : '最近 1 小时（按 5 分钟分桶，共 12 个时间槽）',
        },
        {
            value: '1d' as const,
            label: t('dashboard.last1Day'),
            tip: isEn ? 'Last 24 hours in 1-hour buckets (24 slots)' : '最近 24 小时（按 1 小时分桶，共 24 个时间槽）',
        },
        {
            value: '7d' as const,
            label: t('dashboard.last7Days'),
            tip: isEn ? 'Last 7 days in daily buckets' : '最近 7 天（按自然日分桶）',
        },
        {
            value: '30d' as const,
            label: t('dashboard.last30Days'),
            tip: isEn ? 'Last 30 days in daily buckets' : '最近 30 天（按自然日分桶）',
        },
    ]
})
const setRange = (v: TrendRangeKey) => {
    trendRange.value = v
    renderTrend(true)
}

const buildTrendData = (range: TrendRangeKey) => {
    const srcMap: Record<TrendRangeKey, DashboardTrendSeries> = {
        '1h': rmData.trend_1h,
        '1d': rmData.trend_1d,
        '7d': rmData.trend_7d,
        '30d': rmData.trend_30d,
    }
    const src = srcMap[range]
    if (src && src.dates && src.dates.length > 0) {
        const zeros = () => src.dates.map(() => 0)
        const l1 = src.l1_added && src.l1_added.length === src.dates.length ? src.l1_added : zeros()
        const l2 =
            src.l2_added && src.l2_added.length === src.dates.length
                ? src.l2_added
                : src.traces && src.traces.length === src.dates.length
                  ? src.traces
                  : zeros()
        const l3 = src.l3_added && src.l3_added.length === src.dates.length ? src.l3_added : zeros()
        const uv =
            src.assertions && src.assertions.length === src.dates.length
                ? src.assertions
                : src.dates.map((_, i) => (l1[i] ?? 0) + (l2[i] ?? 0) + (l3[i] ?? 0))
        return {
            dates: src.dates,
            pv: src.activity,
            uv,
            recalls: src.recalls || zeros(),
            traces: l2,
            l1,
            l2,
            l3,
        }
    }
    const now = new Date()
    const dates: string[] = []
    const pv: number[] = []
    const uv: number[] = []
    const recalls: number[] = []
    const traces: number[] = []
    const l1: number[] = []
    const l2: number[] = []
    const l3: number[] = []
    if (range === '1h') {
        for (let i = 11; i >= 0; i--) {
            const d = new Date(now.getTime() - i * 5 * 60 * 1000)
            dates.push(`${pad(d.getHours())}:${pad(d.getMinutes())}`)
            pv.push(0)
            uv.push(0)
            recalls.push(0)
            traces.push(0)
            l1.push(0)
            l2.push(0)
            l3.push(0)
        }
        return { dates, pv, uv, recalls, traces, l1, l2, l3 }
    }
    if (range === '1d') {
        for (let i = 23; i >= 0; i--) {
            const d = new Date(now.getTime() - i * 60 * 60 * 1000)
            dates.push(`${pad(d.getHours())}:00`)
            pv.push(0)
            uv.push(0)
            recalls.push(0)
            traces.push(0)
            l1.push(0)
            l2.push(0)
            l3.push(0)
        }
        return { dates, pv, uv, recalls, traces, l1, l2, l3 }
    }
    const days = range === '30d' ? 30 : 7
    for (let i = days - 1; i >= 0; i--) {
        const d = new Date(now)
        d.setDate(d.getDate() - i)
        dates.push(`${d.getMonth() + 1}/${d.getDate()}`)
        pv.push(0)
        uv.push(0)
        recalls.push(0)
        traces.push(0)
        l1.push(0)
        l2.push(0)
        l3.push(0)
    }
    return { dates, pv, uv, recalls, traces, l1, l2, l3 }
}

// 「总资产 (L1–L3)」= 三层存量之和，与卡片内 L1/L2/L3 三行及增量同口径；
// 缺字段（老载荷）时回退到活跃单元数，避免显示 0 造成更坏的误导。
const totalLayerAssets = computed(() => {
    const sum = (rmData.l1_total ?? 0) + (rmData.l2_total ?? rmData.user_traces ?? 0) + (rmData.l3_total ?? 0)
    return sum > 0 ? sum : rmData.active_assertions
})

const trendSummary = computed(() => {
    const { dates, pv, uv, recalls, traces, l1, l2, l3 } = buildTrendData(trendRange.value)
    const sum = (arr: number[]) => arr.reduce((a, b) => a + b, 0)
    let peakIdx = 0
    let peakVal = 0
    pv.forEach((v, i) => {
        if (v >= peakVal) {
            peakVal = v
            peakIdx = i
        }
    })
    return {
        totalAssertions: sum(uv),
        totalRecalls: sum(recalls),
        totalTraces: sum(traces),
        totalL1: sum(l1),
        totalL2: sum(l2),
        totalL3: sum(l3),
        peakLabel: dates[peakIdx] || '--',
        peakValue: peakVal,
        lastBucket: dates[dates.length - 1] || '--',
    }
})

const renderTrend = (force = false) => {
    const chart = ensureChart('trend')
    if (!chart) return
    const { dates, pv, uv, recalls, l1, l2, l3 } = buildTrendData(trendRange.value)
    const sig = `${trendRange.value}|${dates.join(',')}|${pv.join(',')}|${uv.join(',')}|${recalls.join(',')}|${l1.join(',')}|${l2.join(',')}|${l3.join(',')}|${isDark.value}|${config.lang.active}`
    if (!force && chartSigs.trend === sig) {
        return
    }
    chartSigs.trend = sig
    const themeTokens = theme.value
    const labelRecalls = t('dashboard.trendRecalls')
    const labelL1 = t('dashboard.trendL1')
    const labelL2 = t('dashboard.trendL2')
    const labelL3 = t('dashboard.trendL3')
    const labelAssertions = t('dashboard.trendAssertions')
    const isEn = config.lang.active === 'en'

    chart.setOption({
        color: ['#e6a23c', '#36cfc9', '#b37feb', '#67c23a', '#409eff'],
        tooltip: buildTooltip({
            trigger: 'axis',
            axisPointer: { type: 'shadow' },
            formatter: (params: unknown) => {
                const list = Array.isArray(params) ? params : [params]
                const first = list[0] as { dataIndex?: number; axisValueLabel?: string } | undefined
                const idx = first?.dataIndex ?? 0
                const isLast = idx === dates.length - 1
                const slotTag = isLast
                    ? isEn
                        ? ' <span style="color:#409eff;font-size:11px">(in progress)</span>'
                        : ' <span style="color:#409eff;font-size:11px">(当前桶进行中)</span>'
                    : ''
                const l1Val = l1[idx] ?? 0
                const l2Val = l2[idx] ?? 0
                const l3Val = l3[idx] ?? 0
                const asrtVal = uv[idx] ?? l1Val + l2Val + l3Val
                const recVal = recalls[idx] ?? 0
                return [
                    `<div style="font-weight:700;margin-bottom:4px">${dates[idx] || ''}${slotTag}</div>`,
                    `<div><span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:#67c23a;margin-right:6px"></span>${labelAssertions}: <b>${asrtVal.toLocaleString()}</b></div>`,
                    `<div style="margin-top:3px"><span style="display:inline-block;width:8px;height:8px;border-radius:2px;background:#e6a23c;margin-right:6px"></span>${labelL1}: <b>${l1Val.toLocaleString()}</b></div>`,
                    `<div style="margin-top:3px"><span style="display:inline-block;width:8px;height:8px;border-radius:2px;background:#36cfc9;margin-right:6px"></span>${labelL2}: <b>${l2Val.toLocaleString()}</b></div>`,
                    `<div style="margin-top:3px"><span style="display:inline-block;width:8px;height:8px;border-radius:2px;background:#b37feb;margin-right:6px"></span>${labelL3}: <b>${l3Val.toLocaleString()}</b></div>`,
                    `<div style="margin-top:3px"><span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:#409eff;margin-right:6px"></span>${labelRecalls}: <b>${recVal.toLocaleString()}</b></div>`,
                ].join('')
            },
        }),
        legend: {
            data: [labelL1, labelL2, labelL3, labelAssertions, labelRecalls],
            right: 12,
            top: 4,
            textStyle: { color: themeTokens.textSecondary },
            icon: 'roundRect',
            itemWidth: 12,
            itemHeight: 8,
            itemGap: 12,
        },
        grid: { top: 36, right: 54, bottom: 28, left: 50 },
        xAxis: { type: 'category', data: dates, boundaryGap: true, ...buildAxis() },
        yAxis: [
            {
                type: 'value',
                position: 'left',
                minInterval: 1,
                ...buildAxis({ hideLine: true }),
                axisLabel: { color: '#409eff' },
            },
            {
                type: 'value',
                position: 'right',
                minInterval: 1,
                ...buildAxis({ hideLine: true }),
                splitLine: { show: false },
                axisLabel: { color: '#67c23a' },
            },
        ],
        series: [
            {
                name: labelL1,
                type: 'bar',
                stack: 'layers',
                yAxisIndex: 1,
                data: l1,
                barMaxWidth: 18,
                itemStyle: { color: '#e6a23c' },
                emphasis: { focus: 'series' },
            },
            {
                name: labelL2,
                type: 'bar',
                stack: 'layers',
                yAxisIndex: 1,
                data: l2,
                barMaxWidth: 18,
                itemStyle: { color: '#36cfc9' },
                emphasis: { focus: 'series' },
            },
            {
                name: labelL3,
                type: 'bar',
                stack: 'layers',
                yAxisIndex: 1,
                data: l3,
                barMaxWidth: 18,
                itemStyle: { color: '#b37feb', borderRadius: [3, 3, 0, 0] },
                emphasis: { focus: 'series' },
            },
            {
                name: labelAssertions,
                type: 'line',
                yAxisIndex: 1,
                data: uv,
                smooth: true,
                showSymbol: false,
                lineStyle: { color: '#67c23a', width: 2 },
                areaStyle: { color: makeAreaGradient('#67c23a', 0.14, 0) },
                emphasis: { focus: 'series' },
            },
            {
                name: labelRecalls,
                type: 'line',
                yAxisIndex: 0,
                data: recalls,
                smooth: true,
                showSymbol: false,
                lineStyle: { color: '#409eff', width: 2.2 },
                areaStyle: { color: makeAreaGradient('#409eff', 0.18, 0) },
                emphasis: { focus: 'series' },
            },
        ],
    })
}

// ==================== 记忆三层分布与立据类型（Layer / Warrant） ====================

const warrantLabel = (key: string) => {
    const isEn = config.lang.active === 'en'
    switch (key) {
        case 'background':
            return isEn ? 'L1 Background' : 'L1 背景文档 (Background)'
        case 'episodic':
            return isEn ? 'L2 Episodic' : 'L2 情景对话 (Episodic)'
        case 'noetic':
            return isEn ? 'L3 Noetic' : 'L3 认知结晶 (Noetic)'
        case 'referential':
            return t('dashboard.searchEngine')
        case 'experiential':
            return t('dashboard.directAccess')
        case 'procedural':
            return t('dashboard.socialMedia')
        case 'performative':
            return t('dashboard.referralLink')
        default:
            return key
    }
}

const renderSource = (force = false) => {
    const chart = ensureChart('source')
    if (!chart) return
    const themeTokens = theme.value
    const pieData = rmData.warrant_dist.map((w) => ({
        value: w.count,
        name: warrantLabel(w.value),
    }))
    const sig = `${pieData.map((d) => `${d.name}:${d.value}`).join(',')}|${rmData.total_assertions}|${isDark.value}|${config.lang.active}`
    if (!force && chartSigs.source === sig) {
        return
    }
    chartSigs.source = sig

    chart.setOption({
        color: ['#409eff', '#67c23a', '#e6a23c', '#909399'],
        tooltip: buildTooltip({ trigger: 'item', formatter: '{b}: <b>{c}</b> ({d}%)' }),
        graphic: [
            {
                type: 'text',
                left: 'center',
                top: '39%',
                style: {
                    text: rmData.total_assertions.toLocaleString(),
                    textAlign: 'center',
                    fill: themeTokens.textPrimary,
                    fontSize: 22,
                    fontWeight: 700,
                },
            },
            {
                type: 'text',
                left: 'center',
                top: '50%',
                style: {
                    text: config.lang.active === 'en' ? 'Total in Scope' : '域内总记忆单元',
                    textAlign: 'center',
                    fill: themeTokens.textSecondary,
                    fontSize: 11,
                },
            },
        ],
        legend: {
            bottom: 0,
            left: 'center',
            textStyle: { color: themeTokens.textSecondary },
            itemWidth: 10,
            itemHeight: 10,
        },
        series: [
            {
                name: t('dashboard.trafficSource'),
                type: 'pie',
                radius: ['50%', '72%'],
                center: ['50%', '45%'],
                avoidLabelOverlap: true,
                itemStyle: { borderRadius: 8, borderColor: themeTokens.bg, borderWidth: 2 },
                label: { show: false },
                labelLine: { show: false },
                data: pieData,
            },
        ],
    })
}

// ==================== 高频关系谓词 Top 6 ====================

const renderSales = (force = false) => {
    const chart = ensureChart('sales')
    if (!chart) return
    const themeTokens = theme.value
    const items = rmData.top_predicates.map((p) => ({ name: p.value, value: p.count }))
    const sig = `${items.map((i) => `${i.name}:${i.value}`).join(',')}|${isDark.value}|${config.lang.active}`
    if (!force && chartSigs.sales === sig) {
        return
    }
    chartSigs.sales = sig
    if (items.length === 0) {
        chart.clear()
        return
    }
    chart.setOption({
        tooltip: buildTooltip({ trigger: 'axis', axisPointer: { type: 'shadow' } }),
        grid: { top: 20, right: 16, bottom: 30, left: 50 },
        xAxis: { type: 'category', data: items.map((i) => i.name), ...buildAxis() },
        yAxis: { type: 'value', minInterval: 1, ...buildAxis({ hideLine: true }) },
        series: [
            {
                type: 'bar',
                data: items.map((i) => i.value),
                itemStyle: {
                    color: makeBarGradient('#409eff'),
                    borderRadius: [6, 6, 0, 0],
                },
                barWidth: 22,
                label: { show: true, position: 'top', color: themeTokens.textSecondary, fontSize: 11 },
            },
        ],
    })
}

// ==================== 记忆置信度与健康度仪表盘 ====================

const renderGauge = (force = false) => {
    const chart = ensureChart('gauge')
    if (!chart) return
    const themeTokens = theme.value
    const score = rmData.health_score > 0 ? rmData.health_score : 0
    const sig = `${score}|${isDark.value}|${config.lang.active}`
    if (!force && chartSigs.gauge === sig) {
        return
    }
    chartSigs.gauge = sig
    chart.setOption({
        series: [
            {
                type: 'gauge',
                center: ['50%', '58%'],
                startAngle: 200,
                endAngle: -20,
                min: 0,
                max: 100,
                splitNumber: 10,
                itemStyle: { color: score >= 85 ? '#67c23a' : score >= 60 ? '#e6a23c' : '#f56c6c' },
                progress: { show: true, width: 18, roundCap: true },
                pointer: { show: false },
                axisLine: {
                    lineStyle: {
                        width: 18,
                        color: [
                            [0.6, '#f56c6c'],
                            [0.85, '#e6a23c'],
                            [1, '#67c23a'],
                        ],
                    },
                },
                axisTick: { show: false },
                splitLine: { show: false },
                axisLabel: { show: false },
                anchor: { show: false },
                title: {
                    show: true,
                    offsetCenter: [0, '24%'],
                    fontSize: 12,
                    color: themeTokens.textSecondary,
                },
                detail: {
                    valueAnimation: true,
                    fontSize: 30,
                    fontWeight: 700,
                    offsetCenter: [0, '-10%'],
                    formatter: '{value}%',
                    color: themeTokens.textPrimary,
                },
                data: [{ value: score, name: t('dashboard.cpuUsage') }],
            },
        ],
    })
}

// ==================== 记忆引擎五维能力雷达 ====================

const renderRadar = (force = false) => {
    const chart = ensureChart('radar')
    if (!chart) return
    const themeTokens = theme.value
    const indicators = [
        t('dashboard.stability'),
        t('dashboard.performance'),
        t('dashboard.security'),
        t('dashboard.usability'),
        t('dashboard.scalability'),
    ]
    const currentVals = activeRadarCurrent.value
    const baselineVals = activeRadarBaseline.value.length === 5 ? activeRadarBaseline.value : currentVals
    const sig = `${rmData.radar_available}|${radarTier.value}|${currentVals.join(',')}|${baselineVals.join(',')}|${isDark.value}|${config.lang.active}`
    if (!force && chartSigs.radar === sig) {
        return
    }
    chartSigs.radar = sig
    // 读数全部来自真实评测记录；无记录时不画（卡片上显示空态提示）
    if (!rmData.radar_available || currentVals.length !== 5) {
        chart.clear()
        return
    }
    const isEn = config.lang.active === 'en'
    const ourShortName = t('dashboard.currentVersion')
    const baseShortName = t('dashboard.previousVersion')
    const tierBadge = radarTier.value === 'hard' ? 'ATM-Bench Hard-31' : 'ATM-Bench Main-1013'
    chart.setOption({
        tooltip: buildTooltip({
            position: (pos: number[], _params: unknown, _dom: unknown, _rect: unknown, size: { contentSize: number[]; viewSize: number[] }) => {
                const [x, y] = pos
                const [cw, ch] = size.contentSize
                const [vw] = size.viewSize
                const left = x + cw + 16 < vw ? x + 14 : Math.max(8, x - cw - 14)
                const top = Math.max(36, y - ch / 2)
                return [left, top]
            },
            formatter: () => {
                const rows = indicators
                    .map((ind, i) => {
                        const cur = Number(currentVals[i] ?? 0).toFixed(1)
                        const base = Number(baselineVals[i] ?? 0).toFixed(1)
                        const diff = Number(currentVals[i] ?? 0) - Number(baselineVals[i] ?? 0)
                        const diffStr = `${diff >= 0 ? '+' : ''}${diff.toFixed(1)}pp`
                        const diffColor = diff >= 0 ? '#67c23a' : '#f56c6c'
                        return `<div style="display:flex;justify-content:space-between;gap:14px;font-size:12px;line-height:1.65;">
                            <span style="color:${themeTokens.textSecondary}">${ind}</span>
                            <span style="font-variant-numeric:tabular-nums;">
                                <b style="color:#409eff">${cur}%</b>
                                <span style="opacity:0.65;margin:0 3px;">vs</span>
                                <span style="color:#e6a23c">${base}%</span>
                                <b style="color:${diffColor};margin-left:4px;">(${diffStr})</b>
                            </span>
                        </div>`
                    })
                    .join('')
                return `<div style="min-width:230px;">
                    <div style="font-weight:700;font-size:12.5px;margin-bottom:4px;padding-bottom:4px;border-bottom:1px solid ${themeTokens.borderColor};">
                        ${tierBadge} · ${isEn ? 'Ours vs Official Baseline' : '我方 vs 剑桥官方基准'}
                    </div>
                    ${rows}
                </div>`
            },
        }),
        legend: {
            bottom: 0,
            itemGap: 14,
            itemWidth: 10,
            itemHeight: 8,
            textStyle: { color: themeTokens.textSecondary, fontSize: 11 },
        },
        radar: {
            indicator: indicators.map((name) => ({ name, max: 100 })),
            radius: '54%',
            center: ['50%', '47%'],
            nameGap: 7,
            splitLine: { lineStyle: { color: themeTokens.splitLine } },
            splitArea: { areaStyle: { color: themeTokens.splitArea as string[] } },
            axisLine: { lineStyle: { color: themeTokens.axisLine } },
            axisName: { color: themeTokens.textSecondary, fontSize: 11 },
        },
        series: [
            {
                type: 'radar',
                data: [
                    {
                        value: currentVals,
                        name: ourShortName,
                        lineStyle: { color: '#409eff', width: 2 },
                        itemStyle: { color: '#409eff' },
                        areaStyle: { color: withAlpha('#409eff', 0.25) },
                    },
                    {
                        value: baselineVals,
                        name: baseShortName,
                        lineStyle: { color: '#e6a23c', width: 2, type: 'dashed' },
                        itemStyle: { color: '#e6a23c' },
                        areaStyle: { color: withAlpha('#e6a23c', 0.16) },
                    },
                ],
            },
        ],
    })
}

// ==================== 生命周期 ====================

const renderAll = (force = false) => {
    renderTrend(force)
    renderSource(force)
    renderSales(force)
    renderGauge(force)
    renderRadar(force)
    kpiList.value.forEach((item) => renderSpark(item, force))
}

const resizeAll = () => {
    Object.values(charts).forEach((c) => c?.resize())
    Object.values(sparkCharts).forEach((c) => c?.resize())
}
// 窗口拖拽期间会连续触发 resize，防抖 100ms 减少全量重排
const debouncedResize = debounce(resizeAll, 100)

const disposeAll = () => {
    Object.values(charts).forEach((c) => c?.dispose())
    Object.values(sparkCharts).forEach((c) => c?.dispose())
}

// 窗口 resize、页面卸载与标签页切回前台时自动响应
useEventListener(window, 'resize', debouncedResize)
useEventListener(window, 'beforeunload', saveWorkState)
useEventListener(document, 'visibilitychange', () => {
    if (document.visibilityState === 'visible') {
        if (pollIntervalKey.value === 'stream') {
            startDashboardStream()
        } else {
            fetchRealtimeData(true)
        }
    } else if (pollIntervalKey.value === 'stream') {
        stopDashboardStream()
    }
})

let wasDeactivated = false

onMounted(() => {
    tick()
    clockTimer = setInterval(tick, 1000)

    nextTick(() => {
        renderAll(true)
        if (pollIntervalKey.value !== 'stream') {
            fetchRealtimeData(true)
        }
    })
    restartPollTimer()
})

onActivated(() => {
    if (!wasDeactivated) return
    wasDeactivated = false
    if (!clockTimer) clockTimer = setInterval(tick, 1000)
    restartPollTimer()
    nextTick(() => {
        resizeAll()
        renderAll(false)
    })
})

onDeactivated(() => {
    wasDeactivated = true
    if (clockTimer) {
        clearInterval(clockTimer)
        clockTimer = null
    }
    stopDashboardStream()
})

onBeforeUnmount(() => {
    if (clockTimer) clearInterval(clockTimer)
    stopDashboardStream()
    debouncedResize.cancel()
    saveWorkState()
    disposeAll()
})

// 暗/亮模式切换
watch(
    () => [config.layout.dark, config.lang.active],
    () => nextTick(() => renderAll(true))
)
</script>

<style scoped lang="scss">
.dashboard {
    display: flex;
    flex-direction: column;
    gap: var(--ag-main-space);
}

// ============================================================
// 欢迎条
// ============================================================
.welcome-row {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: var(--ag-main-space);
    align-items: stretch;
}

// 左侧文案卡
.welcome-bar-left {
    grid-column: span 3;
}

// 通用欢迎卡容器
.welcome-bar {
    position: relative;
    display: flex;
    align-items: center;
    min-width: 0;
    min-height: 160px;
    padding: 14px 28px;
    border-radius: var(--el-border-radius-base);
    background: #e1eaf9;
    color: var(--el-text-color-primary);
    box-shadow: 0 2px 12px rgba(64, 158, 255, 0.08);
    overflow: hidden;
    transition:
        box-shadow 0.35s ease,
        transform 0.35s ease;

    // 背景两处径向光斑装饰
    &::before,
    &::after {
        content: '';
        position: absolute;
        border-radius: 50%;
        pointer-events: none;
        opacity: 0.7;
        transition:
            transform 0.6s ease,
            opacity 0.6s ease;
    }
    &::before {
        top: -120px;
        right: -60px;
        width: 220px;
        height: 220px;
        background: radial-gradient(circle, rgba(64, 158, 255, 0.35) 0%, transparent 70%);
    }
    &::after {
        bottom: -100px;
        left: -40px;
        width: 180px;
        height: 180px;
        background: radial-gradient(circle, rgba(64, 158, 255, 0.22) 0%, transparent 70%);
    }

    &:hover {
        transform: translateY(-2px);
        box-shadow: 0 6px 18px rgba(64, 158, 255, 0.16);
        &::before,
        &::after {
            opacity: 0.4;
        }
        &::before {
            transform: translate(-8px, 6px) scale(1.08);
        }
        &::after {
            transform: translate(8px, -6px) scale(1.08);
        }
        .hello {
            transform: translateX(2px);
        }
        .welcome-illustration {
            transform: translateY(-53%) scale(1.03);
        }
    }
}

.welcome-illustration {
    position: absolute;
    top: 50%;
    right: 24px;
    z-index: 0;
    width: 180px;
    max-width: 30%;
    height: auto;
    pointer-events: none;
    opacity: 0.9;
    transition: transform 0.5s cubic-bezier(0.34, 1.56, 0.64, 1);
    animation: welcome-float 4s ease-in-out infinite;
}

.welcome-content {
    position: relative;
    z-index: 1;
    flex: 1;
    min-width: 0;
    padding-right: 220px;
}

.hello {
    margin-bottom: 8px;
    font-size: 20px;
    font-weight: 700;
    color: var(--el-text-color-primary);
    transition: transform 0.3s ease;
}
.tip {
    font-size: 13px;
    line-height: 1.7;
    color: var(--el-text-color-primary);
}

.welcome-meta {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    margin-top: 12px;
}
.meta-item {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 3px 10px;
    font-size: 12px;
    color: var(--el-text-color-regular);
    background: rgba(255, 255, 255, 0.6);
    border-radius: 20px;
    transition:
        background 0.25s ease,
        transform 0.25s ease;

    .el-icon {
        font-size: 14px;
        color: var(--el-color-primary);
    }
    &:hover {
        background: rgba(255, 255, 255, 0.9);
        transform: translateY(-1px);
    }
}
.meta-clickable {
    cursor: pointer;
    user-select: none;
}
.meta-poll-group {
    padding: 2px 4px 2px 10px;
    gap: 8px;

    .poll-trigger {
        display: inline-flex;
        align-items: center;
        gap: 5px;
        cursor: pointer;
        user-select: none;
    }
    .poll-chips {
        display: inline-flex;
        align-items: center;
        gap: 2px;
        padding: 2px;
        background: rgba(64, 158, 255, 0.12);
        border-radius: 14px;
    }
    .poll-chip {
        padding: 1px 7px;
        font-size: 11px;
        font-weight: 600;
        line-height: 1.4;
        color: var(--el-text-color-secondary);
        background: transparent;
        border: none;
        border-radius: 12px;
        cursor: pointer;
        transition: all 0.2s ease;

        &:hover {
            color: var(--el-color-primary);
        }
        &.is-active {
            color: #ffffff;
            background: var(--el-color-primary);
            box-shadow: 0 1px 4px rgba(64, 158, 255, 0.35);
        }
    }
}
.meta-mode-switch {
    padding: 3px 5px 3px 12px;
    gap: 8px;
    background: rgba(255, 255, 255, 0.88);
    border: 1.5px solid rgba(64, 158, 255, 0.35);
    box-shadow: 0 2px 10px rgba(64, 158, 255, 0.14);

    .mode-switch-label {
        font-size: 12px;
        font-weight: 700;
        color: var(--el-text-color-primary);
        letter-spacing: 0.02em;
    }

    .mode-switch-track {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 2px;
        background: rgba(15, 23, 42, 0.08);
        border-radius: 999px;
    }

    .mode-switch-pill {
        display: inline-flex;
        align-items: center;
        gap: 5px;
        height: 24px;
        padding: 0 11px;
        font-size: 11.5px;
        font-weight: 700;
        border-radius: 999px;
        border: 1px solid transparent;
        cursor: pointer;
        transition: all 0.2s ease;

        &.is-active {
            color: #ffffff;
            background: linear-gradient(135deg, #10b981, #059669);
            box-shadow: 0 2px 6px rgba(16, 185, 129, 0.38);
        }

        .mode-pill-dot {
            width: 6px;
            height: 6px;
            border-radius: 50%;
            background: #ffffff;
            box-shadow: 0 0 6px #ffffff;
        }

        &.is-preview {
            color: #db2777;
            background: rgba(236, 72, 153, 0.1);
            border-color: rgba(236, 72, 153, 0.35);

            &:hover {
                background: rgba(236, 72, 153, 0.2);
                border-color: #db2777;
                transform: translateY(-1px);
            }
        }

        .mode-preview-badge {
            padding: 1px 5px;
            font-size: 9.5px;
            font-weight: 800;
            line-height: 1.2;
            color: #ffffff;
            background: linear-gradient(135deg, #ec4899, #db2777);
            border-radius: 999px;
        }
    }
}
.meta-live {
    font-weight: 600;
    color: var(--el-color-success);
    .live-dot {
        width: 7px;
        height: 7px;
        border-radius: 50%;
        background: var(--el-color-success);
        box-shadow: 0 0 0 3px color-mix(in srgb, var(--el-color-success) 25%, transparent);
    }
    &.offline {
        color: var(--el-color-info);
        .live-dot {
            background: var(--el-color-info);
            box-shadow: none;
        }
    }
}

@keyframes welcome-float {
    0%,
    100% {
        transform: translateY(-50%);
    }
    50% {
        transform: translateY(calc(-50% - 6px));
    }
}

// 右侧工作计时
.welcome-timer {
    grid-column: span 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 10px;
    padding: 0 12px;
}
.timer-label {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    font-weight: 500;
    color: var(--el-text-color-regular);

    .el-icon {
        font-size: 15px;
        color: var(--el-color-primary);
    }
}
.timer-time {
    display: flex;
    align-items: baseline;
    gap: 6px;

    .timer-group {
        display: contents;
    }
    .timer-unit {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 3px;
    }
    .num {
        font-size: 30px;
        font-weight: 700;
        font-variant-numeric: tabular-nums;
        line-height: 1;
        letter-spacing: 0.5px;
        color: var(--el-color-primary);
        transition: color 0.3s ease;
    }
    .txt {
        font-size: 11px;
        font-weight: 500;
        color: var(--el-text-color-secondary);
    }
    .colon {
        padding-bottom: 14px;
        font-size: 24px;
        font-weight: 700;
        line-height: 1;
        color: var(--el-color-primary-light-3);
        opacity: 0.7;
        animation: timer-blink 1s ease-in-out infinite;
    }

    &.paused {
        .num {
            color: var(--el-text-color-secondary);
        }
        .colon {
            animation: none;
            opacity: 0.25;
        }
    }
}

@keyframes timer-blink {
    0%,
    100% {
        opacity: 0.7;
    }
    50% {
        opacity: 0.2;
    }
}

.timer-btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 6px 16px;
    font-size: 13px;
    font-weight: 500;
    color: #ffffff;
    background: var(--el-color-primary);
    border: none;
    border-radius: 20px;
    outline: none;
    cursor: pointer;
    box-shadow: 0 3px 10px color-mix(in srgb, var(--el-color-primary) 25%, transparent);
    transition:
        transform 0.25s ease,
        box-shadow 0.25s ease,
        filter 0.25s ease;

    &:hover {
        transform: translateY(-1px);
        filter: brightness(1.08);
        box-shadow: 0 5px 14px color-mix(in srgb, var(--el-color-primary) 35%, transparent);
    }
    &:active {
        transform: translateY(0);
    }

    &.resting {
        background: var(--el-color-success);
        box-shadow: 0 3px 10px color-mix(in srgb, var(--el-color-success) 25%, transparent);
        &:hover {
            box-shadow: 0 5px 14px color-mix(in srgb, var(--el-color-success) 35%, transparent);
        }
    }
}

// ============================================================
// KPI 卡片
// ============================================================
.kpi-row {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: var(--ag-main-space);
}
.roi-stat-card {
    cursor: pointer;
}
.roi-unavailable-banner {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: 10px;
    padding: 9px 12px;
    font-size: 12px;
    line-height: 1.6;
    color: var(--el-text-color-regular);
    background: var(--el-fill-color-light);
    border: 1px dashed var(--el-border-color);
    border-radius: 8px;
}
.kpi-inline-unit {
    margin-left: 6px;
    font-size: 12px;
    font-weight: 500;
    color: var(--el-text-color-secondary);
}
.kpi-card {
    position: relative;
    min-width: 0;
    padding: 18px 20px;
    background: var(--el-bg-color-overlay);
    border-radius: var(--el-border-radius-base);
    overflow: hidden;
    transition:
        transform 0.25s ease,
        box-shadow 0.25s ease;

    &:hover {
        transform: translateY(-1px);
        box-shadow: 0 2px 8px rgba(0, 0, 0, 0.05);
    }
}
.kpi-top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    font-size: 13px;
    color: var(--el-text-color-secondary);
}
.kpi-title-wrap {
    display: inline-flex;
    align-items: center;
    gap: 5px;
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
    font-size: 10.5px;
    font-weight: 700;
    line-height: 1;
    color: var(--el-text-color-secondary);
    background: var(--el-fill-color);
    border: 1px solid var(--el-border-color-light);
    border-radius: 50%;
    cursor: pointer;
    user-select: none;
    vertical-align: middle;
    transition: all 0.2s ease;

    &:hover {
        color: #ffffff;
        background: var(--el-color-primary);
        border-color: var(--el-color-primary);
    }
}
.kpi-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    font-size: 16px;
    color: var(--el-color-primary);
    background: color-mix(in srgb, var(--el-color-primary) 12%, transparent);
    border-radius: var(--el-border-radius-base);
}
.kpi-value {
    margin: 10px 0 6px;
    font-size: 28px;
    font-weight: 700;
    letter-spacing: 0.5px;
    color: var(--el-text-color-primary);
}
.kpi-bottom {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
    white-space: nowrap;

    .kpi-trend {
        display: inline-flex;
        align-items: center;
        gap: 2px;
        flex-shrink: 0;
        padding: 2px 6px;
        font-weight: 600;
        border-radius: var(--el-border-radius-base);

        &.up {
            color: var(--el-color-success);
            background: color-mix(in srgb, var(--el-color-success) 12%, transparent);
        }
        &.down {
            color: var(--el-color-danger);
            background: color-mix(in srgb, var(--el-color-danger) 12%, transparent);
        }
    }
    .kpi-ratio {
        display: inline-flex;
        align-items: center;
        flex-shrink: 0;
        padding: 2px 6px;
        font-weight: 600;
        color: var(--el-text-color-regular);
        background: var(--el-fill-color-light);
        border-radius: var(--el-border-radius-base);
    }
    .kpi-sub {
        flex-shrink: 1;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .kpi-spark {
        flex: 1;
        min-width: 0;
        height: 32px;
        margin-left: 8px;
    }
}

// ============================================================
// 图表卡片
// ============================================================
.chart-row {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: var(--ag-main-space);
}
.chart-card-span-2 {
    grid-column: span 2;
    min-width: 0;
}
.chart-card {
    position: relative;
    min-width: 0;
    overflow: hidden;
    padding: 16px 18px;
    background: var(--el-bg-color-overlay);
    border-radius: var(--el-border-radius-base);
    transition: box-shadow 0.25s ease;

    &:hover {
        box-shadow: 0 6px 20px rgba(0, 0, 0, 0.06);
    }
}
.card-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    min-width: 0;
    margin-bottom: 8px;

    .card-title {
        position: relative;
        flex-shrink: 0;
        padding-left: 10px;
        font-size: 15px;
        font-weight: 600;
        color: var(--el-text-color-primary);

        &::before {
            content: '';
            position: absolute;
            left: 0;
            top: 50%;
            width: 3px;
            height: 14px;
            background: var(--el-color-primary);
            border-radius: 2px;
            transform: translateY(-50%);
        }
    }

    .radar-actions {
        display: flex;
        align-items: center;
        gap: 6px;
        min-width: 0;
        flex-wrap: wrap;
        justify-content: flex-end;
    }

    .radar-tag {
        max-width: 210px;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;

        :deep(.el-tag__content) {
            display: block;
            overflow: hidden;
            text-overflow: ellipsis;
            white-space: nowrap;
        }
    }
}
.chart-body-lg {
    height: 288px;
}
.chart-body-md {
    height: 280px;
}
.chart-body-gauge {
    height: 232px;
}
.card-title-group {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
}
.live-slot-pill {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 2px 8px;
    font-size: 11px;
    font-weight: 500;
    color: var(--el-color-primary);
    background: color-mix(in srgb, var(--el-color-primary) 10%, transparent);
    border-radius: 12px;

    .pulse-dot {
        width: 6px;
        height: 6px;
        border-radius: 50%;
        background: var(--el-color-primary);
        animation: timer-blink 1.2s ease-in-out infinite;
    }
}
.trend-summary-strip {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 8px;
    padding: 6px 10px;
    margin-bottom: 4px;
    background: var(--el-fill-color-lighter);
    border: 1px solid var(--el-border-color-extra-light);
    border-radius: 6px;

    .ts-item {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 12px;
        min-width: 0;
    }
    .ts-dot {
        width: 7px;
        height: 7px;
        border-radius: 50%;
        flex-shrink: 0;
    }
    .ts-label {
        color: var(--el-text-color-secondary);
        white-space: nowrap;
    }
    .ts-val {
        margin-left: auto;
        font-weight: 700;
        font-variant-numeric: tabular-nums;
        color: var(--el-text-color-primary);
    }
}
.gauge-breakdown {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 6px;
    margin-top: 4px;

    .gb-pill {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 2px;
        padding: 4px 6px;
        background: var(--el-fill-color-lighter);
        border-radius: 6px;
        font-size: 11px;
    }
    .gb-k {
        color: var(--el-text-color-secondary);
    }
    .gb-v {
        font-weight: 700;
        font-variant-numeric: tabular-nums;
    }
}
// 无实测读数时的空态提示（雷达图等只画真实数据的卡片）
.chart-empty-tip {
    position: absolute;
    inset: 48px 18px 16px;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 12px;
    color: var(--el-text-color-secondary);
    text-align: center;
    pointer-events: none;
}

// 访问趋势的时间范围切换
.trend-ranges {
    display: inline-flex;
    padding: 2px;
    background: var(--el-fill-color-light);
    border-radius: var(--el-border-radius-base);
}
.range-chip {
    padding: 4px 12px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
    background: transparent;
    border: none;
    border-radius: var(--el-border-radius-base);
    outline: none;
    cursor: pointer;
    transition:
        color 0.25s ease,
        background 0.25s ease,
        box-shadow 0.25s ease;

    &:hover {
        color: var(--el-text-color-primary);
    }
    &.is-active {
        font-weight: 600;
        color: var(--el-color-primary);
        background: var(--el-bg-color);
        box-shadow: 0 2px 6px rgba(0, 0, 0, 0.08);
    }
}

// ============================================================
// 全 Harness 召回效能与成本 ROI 评估舱
// ============================================================
.efficacy-section-card {
    display: flex;
    flex-direction: column;
    gap: 14px;
}
.efficacy-header {
    flex-wrap: wrap;
    gap: 10px;
}
.efficacy-title-wrap {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
}
.efficacy-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}
.eff-kpi-grid {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 12px;
}
.eff-kpi-box {
    padding: 14px 16px;
    background: var(--el-fill-color-lighter);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
}
.eff-kpi-label {
    display: flex;
    align-items: center;
    justify-content: space-between;
    font-size: 12px;
    font-weight: 600;
    color: var(--el-text-color-secondary);
}
.eff-kpi-num {
    margin: 8px 0 4px;
    font-size: 24px;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
    small {
        margin-left: 6px;
        font-size: 12px;
        font-weight: 600;
        opacity: 0.85;
    }
}
.eff-kpi-desc {
    font-size: 11.5px;
    color: var(--el-text-color-secondary);
    line-height: 1.45;
}
.eff-mid-grid {
    display: grid;
    grid-template-columns: 1fr 2fr;
    gap: 14px;
}
.eff-subcard {
    padding: 14px 16px;
    background: var(--el-fill-color-blank);
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
}
.eff-sub-title {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 10px;
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);
}
.eff-cli-hint {
    padding: 2px 8px;
    font-size: 11px;
    color: var(--el-color-primary);
    background: color-mix(in srgb, var(--el-color-primary) 10%, transparent);
    border-radius: 4px;
}
.funnel-list {
    display: flex;
    flex-direction: column;
    gap: 10px;
}
.funnel-item {
    display: flex;
    flex-direction: column;
    gap: 4px;
}
.funnel-row-top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    font-size: 12px;
}
.funnel-name {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--el-text-color-regular);
}
.funnel-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
}
.funnel-val {
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    color: var(--el-text-color-primary);
}
.funnel-bar-bg {
    height: 6px;
    background: var(--el-fill-color);
    border-radius: 4px;
    overflow: hidden;
}
.funnel-bar-fill {
    height: 100%;
    border-radius: 4px;
    transition: width 0.35s ease;
}
.eff-table-wrap {
    overflow-x: auto;
    max-height: 290px;
    overflow-y: auto;
}
.eff-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
    th,
    td {
        padding: 7px 10px;
        text-align: left;
        border-bottom: 1px solid var(--el-border-color-lighter);
        vertical-align: top;
    }
    th {
        position: sticky;
        top: 0;
        z-index: 1;
        font-weight: 600;
        color: var(--el-text-color-secondary);
        background: var(--el-fill-color-light);
    }
    tbody tr:hover {
        background: var(--el-fill-color-lighter);
    }
}
.h-badge {
    display: inline-block;
    padding: 1px 7px;
    font-size: 10.5px;
    font-weight: 700;
    border-radius: 10px;
    &.hb-active {
        color: var(--el-color-success);
        background: color-mix(in srgb, var(--el-color-success) 14%, transparent);
    }
    &.hb-ready {
        color: var(--el-color-primary);
        background: color-mix(in srgb, var(--el-color-primary) 14%, transparent);
    }
    &.hb-standby {
        color: var(--el-text-color-secondary);
        background: var(--el-fill-color);
    }
}
.h-mini-tag {
    display: inline-block;
    margin-top: 2px;
    padding: 0 5px;
    font-size: 10px;
    color: var(--el-color-primary);
    background: color-mix(in srgb, var(--el-color-primary) 12%, transparent);
    border-radius: 4px;
}
.eff-prompt-cell {
    max-width: 260px;
    color: var(--el-text-color-primary);
    line-height: 1.45;
}
.eff-recall-cell {
    max-width: 320px;
}
.eff-summary-line {
    font-size: 11.5px;
    color: var(--el-text-color-regular);
    line-height: 1.4;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.eff-anchor-tags {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    margin-top: 4px;
}
.eff-anchor-chip {
    padding: 1px 6px;
    font-size: 10.5px;
    font-family: 'JetBrains Mono', monospace;
    color: var(--el-color-success);
    background: color-mix(in srgb, var(--el-color-success) 12%, transparent);
    border-radius: 4px;
}
.verdict-pill {
    display: inline-block;
    padding: 2px 8px;
    font-size: 11px;
    font-weight: 600;
    border-radius: 6px;
    &.vp-high_value {
        color: var(--el-color-success);
        background: color-mix(in srgb, var(--el-color-success) 14%, transparent);
    }
    &.vp-assisted {
        color: var(--el-color-primary);
        background: color-mix(in srgb, var(--el-color-primary) 14%, transparent);
    }
    &.vp-silenced,
    &.vp-dedup {
        color: var(--el-color-warning);
        background: color-mix(in srgb, var(--el-color-warning) 14%, transparent);
    }
    &.vp-noise {
        color: var(--el-color-danger);
        background: color-mix(in srgb, var(--el-color-danger) 14%, transparent);
    }
}
.text-success {
    color: var(--el-color-success);
}
.text-primary {
    color: var(--el-color-primary);
}
.text-warning {
    color: var(--el-color-warning);
}
.text-danger {
    color: var(--el-color-danger);
}
.text-muted {
    color: var(--el-text-color-placeholder);
}
.font-mono {
    font-family: 'JetBrains Mono', 'SFMono-Regular', Consolas, Menlo, monospace;
}
.font-bold {
    font-weight: 700;
}

// ============================================================
// 实时命令行监控窗口
// ============================================================
.live-console-card {
    .console-actions {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
    }
}
.console-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 14px;
    margin-top: 6px;
}
.console-pane {
    min-width: 0;
    border-radius: 8px;
    overflow: hidden;
    background: #181924;
    border: 1px solid #2e303e;
    font-family: 'JetBrains Mono', 'SFMono-Regular', Consolas, Menlo, monospace;
}
.pane-head {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 7px 12px;
    background: #232534;
    border-bottom: 1px solid #2e303e;
    .dot {
        width: 9px;
        height: 9px;
        border-radius: 50%;
        &.red {
            background: #ff5f56;
        }
        &.yellow {
            background: #ffbd2e;
        }
        &.green {
            background: #27c93f;
        }
    }
    .pane-title {
        margin-left: 6px;
        font-size: 11px;
        font-weight: 600;
        color: #cdd6f4;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
}
.pane-body {
    height: 195px;
    overflow-y: auto;
    padding: 8px 12px;
    font-size: 11.5px;
    line-height: 1.65;
    color: #cdd6f4;
}
.term-row {
    display: flex;
    align-items: baseline;
    gap: 6px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
}
.term-ts {
    color: #7f849c;
    flex-shrink: 0;
}
.term-badge {
    font-weight: 700;
    flex-shrink: 0;
    color: #89b4fa;
    &.ev-recall {
        color: #a6e3a1;
    }
    &.ev-close {
        color: #f9e2af;
    }
    &.ev-write {
        color: #cba6f7;
    }
}
.term-outcome {
    flex-shrink: 0;
    color: #94e2d5;
    &.out-injected,
    &.out-written {
        color: #a6e3a1;
    }
}
.term-kv {
    color: #e6e9ef;
    overflow: hidden;
    text-overflow: ellipsis;
}
.term-clickable {
    cursor: pointer;
    border-radius: 4px;
    padding: 0 4px;
    margin: 0 -4px;
    &:hover {
        background: rgba(255, 255, 255, 0.06);
    }
}
.term-empty {
    padding: 20px 0;
    color: #7f849c;
    text-align: center;
    font-size: 11.5px;
}

// ============================================================
// 暗黑模式
// ============================================================
html.dark {
    .welcome-bar {
        background: var(--ag-bg-color-overlay);
    }
    .meta-item {
        background: rgba(255, 255, 255, 0.08);
        &:hover {
            background: rgba(255, 255, 255, 0.14);
        }
    }
}

// ============================================================
// 响应式
// ============================================================
@media (max-width: 1200px) {
    .kpi-row,
    .eff-kpi-grid {
        grid-template-columns: repeat(2, 1fr);
    }
    .chart-row,
    .eff-mid-grid {
        grid-template-columns: 1fr;
    }
    .chart-card-span-2 {
        grid-column: auto;
    }
    .welcome-row {
        grid-template-columns: 1fr;
    }
    .welcome-bar-left,
    .welcome-timer {
        grid-column: auto;
    }
    .welcome-timer {
        min-height: 120px;
        background: #e1eaf9;
        box-shadow: 0 2px 12px rgba(64, 158, 255, 0.08);
        padding: 14px 28px;
        border-radius: var(--el-border-radius-base);
        transition:
            box-shadow 0.35s ease,
            transform 0.35s ease;
        flex-direction: row;
        justify-content: space-evenly;
        .timer-time .num {
            font-size: 50px;
        }
        &:hover {
            transform: translateY(-2px);
            box-shadow: 0 6px 18px rgba(64, 158, 255, 0.16);
        }
    }
    html.dark .welcome-timer {
        background: var(--ag-bg-color-overlay);
    }
}
@media (max-width: 640px) {
    .kpi-row {
        grid-template-columns: 1fr;
    }
    .welcome-bar {
        padding: 16px 20px;
    }
    .welcome-content {
        padding-right: 0;
    }
    .welcome-illustration {
        display: none;
    }
    .welcome-timer {
        flex-direction: column;
        justify-content: center;
        padding: 20px 0;
    }
    .timer-time .num {
        font-size: 26px;
    }
}
</style>

<!-- 指标口径弹层（teleport 到 body，须为非 scoped 样式） -->
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
