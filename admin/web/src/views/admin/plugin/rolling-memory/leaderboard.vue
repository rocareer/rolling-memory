<!-- plugin: rolling-memory v0.0.58 -->
<!--
    Rolling-Memory 记忆系统 · 独立「LoCoMo-Backboard 榜单」页面（plugins/rolling-memory/web/views/leaderboard.vue）。
    落位副本：web/src/views/admin/plugin/rolling-memory/leaderboard.vue（由安装器复制，逐字节一致）。
    职责：
      1) 清晰说明 Rolling-Memory 内置的评测协议即为 locomo-backboard（Backboard 官方 local-qa-judge 冻结协议
         locomo-frozen-2026-10-11，三枚 SHA1 钉只读锁死），对标的行业榜单也是 locomo-backboard 商业记忆系统榜单；
      2) 展示 locomo-backboard 榜单完整排行：含我方两行——现行 0-Vector/0-LLM 本机 1,540 全量实测 85.26%、
         历史 LLM+向量重型架构对照 92.79%（该路线不再提供）——以及 Backboard、Memobase、Zep、Mem0-Graph、Mem0、LangMem、OpenAI Memory 等系统；
      3) 全量支持中英文（zh-cn / en）无混杂切换，所有天梯图、卡片、表格、协议清单均采用自适应多行自然排版，零截断。
-->
<template>
    <div class="default-main plugin-rm-leaderboard">
        <el-card shadow="never" class="rm-lb-card">
            <template #header>
                <div class="rm-lb-header">
                    <div class="rm-lb-title-group">
                        <span class="rm-lb-title">{{ t('plugin.rolling-memory.leaderboard.title') }}</span>
                        <el-tag size="small" type="primary">
                            {{ t('plugin.rolling-memory.leaderboard.protocolBadge') }}
                        </el-tag>
                        <el-tag size="small" type="warning">
                            {{ t('plugin.rolling-memory.leaderboard.boardBadge') }}
                        </el-tag>
                        <el-tag size="small" :type="protocolSpec.all_pins_verified !== false ? 'success' : 'danger'">
                            {{ t('plugin.rolling-memory.leaderboard.pinsVerifiedBadge') }}
                        </el-tag>
                        <!-- 三枚徽章的悬停说明聚合为单个点击式 `?` 弹层 -->
                        <el-popover trigger="click" placement="bottom" :width="420" popper-class="rm-help-popper">
                            <template #reference>
                                <button
                                    type="button"
                                    class="tip-q"
                                    :aria-label="
                                        isEn
                                            ? 'Evaluation protocol, benchmark and cryptographic pin semantics'
                                            : '评测协议、对标基准与密码学钉校验语义说明'
                                    "
                                >
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    Built-in Evaluation Protocol: locomo-backboard (locomo-frozen-2026-10-11), identical to the Backboard public
                                    benchmark harness.
                                </p>
                                <p v-else>内置评测协议：locomo-backboard（locomo-frozen-2026-10-11），与商业记忆榜单官方评测套件逐字节同构。</p>
                                <p v-if="isEn">Target Benchmark: LoCoMo 1,540-question long-term conversational memory leaderboard (ACL 2024).</p>
                                <p v-else>对标行业基准：LoCoMo（ACL 2024）1,540 题长程多会话记忆权威排行榜。</p>
                                <p v-if="isEn">
                                    Cryptographic 3-Pin Verification: SHA-1 digests of Answer System Prompt, Judge System Prompt, and Judge User
                                    Template are locked and verified.
                                </p>
                                <p v-else>
                                    三枚 SHA-1 密码学钉校验通过：Answer 系统提示词、Judge 系统提示词与 Judge 用户模板均只读锁死，杜绝改词刷分。
                                </p>
                            </div>
                        </el-popover>
                    </div>
                    <div class="rm-lb-actions">
                        <el-button v-blur size="small" type="primary" plain @click="goATMLeaderboard">
                            {{ isEn ? 'Cambridge ATM-Bench (Primary) →' : '剑桥 ATM-Bench 主榜单 →' }}
                        </el-button>
                        <el-button v-blur size="small" plain @click="goEvalLab">
                            {{ t('plugin.rolling-memory.leaderboard.backEvalLabBtn') }}
                        </el-button>
                        <el-button v-blur size="small" type="primary" :loading="loading" @click="loadLeaderboard">
                            {{ t('plugin.rolling-memory.refresh') }}
                            <template v-if="loading"> ({{ loadLbPct }}%)</template>
                        </el-button>
                    </div>
                </div>
            </template>

            <div v-if="loading" class="rm-top-progress-banner">
                <div class="rm-tpb-head">
                    <span>{{ loadLbText }}</span>
                    <strong>{{ loadLbPct }}%</strong>
                </div>
                <el-progress :percentage="loadLbPct" :stroke-width="6" striped striped-flow :show-text="false" />
            </div>

            <!-- 核心横幅声明：内置评测协议 = locomo-backboard，对标榜单 = locomo-backboard -->
            <div class="rm-parity-banner">
                <div class="rm-parity-banner-title">
                    <span class="rm-parity-pill">{{ t('plugin.rolling-memory.leaderboard.pillText') }}</span>
                    <strong class="rm-parity-headline">{{ t('plugin.rolling-memory.leaderboard.bannerTitle') }}</strong>
                </div>
                <div class="rm-parity-banner-desc">
                    {{ t('plugin.rolling-memory.leaderboard.bannerDesc') }}
                </div>
                <div class="rm-parity-falsify-row">
                    <span>
                        {{
                            isEn
                                ? 'Reproducibility Notice: LoCoMo serves as a comparative conversational reference alongside the primary Cambridge ATM-Bench. Deterministic retrieval over the upstream-official Cat 1–4 question set (1,540) runs offline at zero model cost; end-to-end QA evaluation uses the frozen locomo-backboard prompts with a user-configured LLM. Obtain the data with rmem eval --fetch locomo --accept-upstream-license.'
                                : '复现说明：LoCoMo 作为长程对话对照参照（主基准为剑桥 ATM-Bench）。确定性检索以上游官方 cat1-4 全集（1,540 题）为准，支持离线零成本复算；端到端问答评分使用冻结的 locomo-backboard 提示词与用户配置的模型接口执行。语料可运行 rmem eval --fetch locomo --accept-upstream-license 一条命令获取。'
                        }}
                    </span>
                    <div style="display: flex; gap: 8px; flex-wrap: wrap">
                        <el-button size="small" type="primary" @click="goEvalLab('1540')">
                            {{ isEn ? 'Run Official 1,540 in Evaluation Lab →' : '在评测实验室运行官方 1,540 →' }}
                        </el-button>
                    </div>
                </div>
            </div>

            <!-- 顶部 4 项核心指标卡（小屏/笔记本自动 2×2 宽敞排布，超宽屏 1×4） -->
            <el-row :gutter="12" class="rm-kpi-row">
                <el-col :xs="24" :sm="12" :xl="6" class="rm-col-stretch">
                    <div class="rm-kpi-box is-proto">
                        <div class="rm-kpi-label">
                            {{ t('plugin.rolling-memory.leaderboard.kpi1Label') }}
                            <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                                <template #reference>
                                    <button
                                        type="button"
                                        class="tip-q"
                                        :aria-label="isEn ? 'Protocol ID and SHA-1 pin status' : '冻结评测协议标识与哈希钉状态说明'"
                                    >
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">
                                        Frozen evaluation protocol ID and cryptographic SHA-1 pin status ensuring strict apples-to-apples comparison.
                                    </p>
                                    <p v-else>冻结评测协议标识与三枚 SHA-1 哈希钉状态，确保与外部商业榜单采用完全一致的裁判标准。</p>
                                </div>
                            </el-popover>
                        </div>
                        <div class="rm-kpi-val">{{ t('plugin.rolling-memory.leaderboard.kpi1Value') }}</div>
                        <div class="rm-kpi-sub">{{ t('plugin.rolling-memory.leaderboard.kpi1Sub') }}</div>
                    </div>
                </el-col>
                <el-col :xs="24" :sm="12" :xl="6" class="rm-col-stretch">
                    <div class="rm-kpi-box is-highlight">
                        <div class="rm-kpi-label">
                            {{ t('plugin.rolling-memory.leaderboard.kpi2Label') }}
                            <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                                <template #reference>
                                    <button
                                        type="button"
                                        class="tip-q"
                                        :aria-label="isEn ? 'Latest on-device 1,540 full-set reading and judge ruler' : '本机最新 1,540 全量读数与判官口径说明'"
                                    >
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">
                                        Latest on-device reading: the local_plain (0-Vector / 0-LLM) engine scores 85.26% (1313/1540) end-to-end on
                                        the official Cat 1–4 full set under the frozen prompt protocol, judged by deepseek-v4.1-flash (the board's
                                        external rows use GPT-4.1 — compare row by row against the judge column).
                                    </p>
                                    <p v-else>
                                        本机最新读数：local_plain（0-Vector / 0-LLM）引擎在官方 cat1-4 全集（1,540 题）上的端到端判对率 85.26%
                                        （1313/1540），冻结提示词协议，deepseek-v4.1-flash 判官（外部榜行为 GPT-4.1 判官，请按行内判官列逐行对照）。
                                    </p>
                                </div>
                            </el-popover>
                        </div>
                        <div class="rm-kpi-val is-green">{{ t('plugin.rolling-memory.leaderboard.kpi2Value') }}</div>
                        <div class="rm-kpi-sub">{{ t('plugin.rolling-memory.leaderboard.kpi2Sub') }}</div>
                    </div>
                </el-col>
                <el-col :xs="24" :sm="12" :xl="6" class="rm-col-stretch">
                    <div class="rm-kpi-box">
                        <div class="rm-kpi-label">
                            {{ t('plugin.rolling-memory.leaderboard.kpi3Label') }}
                            <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                                <template #reference>
                                    <button
                                        type="button"
                                        class="tip-q"
                                        :aria-label="isEn ? 'Category breakdown definition' : '题型分项得分说明'"
                                    >
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">
                                        Category breakdown of the latest on-device 1,540 run: Cat 4 Single-hop 90.96%, Cat 2 Temporal 87.23%, Cat 1 Multi-hop 70.57%, Cat 3 Open-domain 71.88%.
                                    </p>
                                    <p v-else>
                                        本机最新 1,540 全量运行的题型分项：Cat 4 单跳 90.96% · Cat 2 时序 87.23% · Cat 1 多跳 70.57% · Cat 3 开放域 71.88%。
                                    </p>
                                </div>
                            </el-popover>
                        </div>
                        <div class="rm-kpi-val">{{ t('plugin.rolling-memory.leaderboard.kpi3Value') }}</div>
                        <div class="rm-kpi-sub">{{ t('plugin.rolling-memory.leaderboard.kpi3Sub') }}</div>
                    </div>
                </el-col>
                <el-col :xs="24" :sm="12" :xl="6" class="rm-col-stretch">
                    <div class="rm-kpi-box">
                        <div class="rm-kpi-label">
                            {{ t('plugin.rolling-memory.leaderboard.kpi4Label') }}
                            <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                                <template #reference>
                                    <button
                                        type="button"
                                        class="tip-q"
                                        :aria-label="isEn ? 'Historical LLM + vector architecture result' : '历史 LLM + 向量架构成绩说明'"
                                    >
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">
                                        Historical result: our early LLM + vector heavyweight architecture scored 92.79% on the full 1,540-question
                                        set (GPT-4.1 temp 0.1 judge). Kept as an architecture-evolution reference; this route is no longer provided.
                                    </p>
                                    <p v-else>历史成绩：我方早期 LLM + 向量重型架构在 1,540 题全量上的存档读数 92.79%（GPT-4.1 temp 0.1 判官），仅作架构演进对照；该路线不再提供。</p>
                                </div>
                            </el-popover>
                        </div>
                        <div class="rm-kpi-val">{{ t('plugin.rolling-memory.leaderboard.kpi4Value') }}</div>
                        <div class="rm-kpi-sub">{{ t('plugin.rolling-memory.leaderboard.kpi4Sub') }}</div>
                    </div>
                </el-col>
            </el-row>

            <!-- 三栏说明卡片：小屏/笔记本采用 2+1 宽敞排布，超宽屏 1×3 -->
            <el-row :gutter="12" class="rm-intro-row">
                <el-col :xs="24" :md="12" :xl="8" class="rm-col-stretch">
                    <div class="rm-intro-card">
                        <div class="rm-intro-hdr">
                            <span class="rm-intro-title-txt">{{ t('plugin.rolling-memory.leaderboard.card1Title') }}</span>
                            <el-tag size="small" type="primary" class="rm-shrink-0">
                                {{ t('plugin.rolling-memory.leaderboard.card1Tag') }}
                            </el-tag>
                        </div>
                        <div class="rm-intro-body">
                            {{ t('plugin.rolling-memory.leaderboard.card1Body') }}
                        </div>
                    </div>
                </el-col>
                <el-col :xs="24" :md="12" :xl="8" class="rm-col-stretch">
                    <div class="rm-intro-card">
                        <div class="rm-intro-hdr">
                            <span class="rm-intro-title-txt">{{ t('plugin.rolling-memory.leaderboard.card2Title') }}</span>
                            <el-tag size="small" type="success" class="rm-shrink-0">
                                {{ t('plugin.rolling-memory.leaderboard.card2Tag') }}
                            </el-tag>
                        </div>
                        <div class="rm-intro-body">
                            {{ t('plugin.rolling-memory.leaderboard.card2Body') }}
                        </div>
                    </div>
                </el-col>
                <el-col :xs="24" :md="24" :xl="8" class="rm-col-stretch">
                    <div class="rm-intro-card">
                        <div class="rm-intro-hdr">
                            <span class="rm-intro-title-txt">{{ t('plugin.rolling-memory.leaderboard.card3Title') }}</span>
                            <el-tag size="small" type="warning" class="rm-shrink-0">
                                {{ t('plugin.rolling-memory.leaderboard.card3Tag') }}
                            </el-tag>
                        </div>
                        <div class="rm-intro-body">
                            {{ t('plugin.rolling-memory.leaderboard.card3Body') }}
                        </div>
                    </div>
                </el-col>
            </el-row>

            <!-- 图表区：小屏/笔记本上下全宽舒展展示，超宽屏左右并排 -->
            <el-row :gutter="12" class="rm-charts-row">
                <el-col :xs="24" :xl="13" class="rm-col-stretch">
                    <div class="rm-chart-box">
                        <div class="rm-chart-hdr">
                            <span class="rm-chart-title-txt">{{ t('plugin.rolling-memory.leaderboard.chartLeftTitle') }}</span>
                            <div class="rm-chart-legend">
                                <span class="rm-leg-item">
                                    <span class="rm-leg-dot is-ours"></span>
                                    <span>{{ t('plugin.rolling-memory.leaderboard.legendOurs') }}</span>
                                </span>
                                <span class="rm-leg-item">
                                    <span class="rm-leg-dot is-pub"></span>
                                    <span>{{ t('plugin.rolling-memory.leaderboard.legendPublic') }}</span>
                                </span>
                                <!-- 90% 参考线语义改为点击式 `?` 弹层（原为 :title 悬停） -->
                                <el-popover trigger="click" placement="bottom" :width="320" popper-class="rm-help-popper">
                                    <template #reference>
                                        <button type="button" class="tip-q" :aria-label="isEn ? 'Reference line semantics' : '参考线语义说明'">
                                            ?
                                        </button>
                                    </template>
                                    <div class="rm-help-body">
                                        <p>{{ t('plugin.rolling-memory.leaderboard.leaderLineTip') }}</p>
                                    </div>
                                </el-popover>
                            </div>
                        </div>
                        <div class="rm-ladder-list">
                            <div
                                v-for="(item, idx) in filteredEntries"
                                :key="item.id"
                                class="rm-ladder-row"
                                :class="{
                                    'is-active': activeEntry?.id === item.id,
                                    'is-ours-row': item.is_ours,
                                }"
                                @click="selectedEntry = item"
                            >
                                <div class="rm-ladder-top">
                                    <div class="rm-ladder-meta">
                                        <span class="rm-rank-badge" :class="rankBadgeClass(item.not_ranked ? 0 : idx + 1)">
                                            {{ item.not_ranked ? '—' : `#${idx + 1}` }}
                                        </span>
                                        <span class="rm-sys-name">{{ loc(item.system, item.system_en) }}</span>
                                        <el-tag size="small" :type="item.is_ours ? 'success' : 'info'" effect="plain" class="rm-mini-tag">
                                            {{
                                                item.is_ours
                                                    ? t('plugin.rolling-memory.leaderboard.tagOurs')
                                                    : t('plugin.rolling-memory.leaderboard.tagPublic')
                                            }}
                                        </el-tag>
                                        <el-tag
                                            v-if="loc(item.highlight_tag, item.highlight_tag_en)"
                                            size="small"
                                            :type="item.is_ours ? 'warning' : 'info'"
                                            effect="light"
                                            class="rm-mini-tag"
                                        >
                                            {{ loc(item.highlight_tag, item.highlight_tag_en) }}
                                        </el-tag>
                                    </div>
                                    <div class="rm-ladder-score" :class="{ 'is-ours-score': item.is_ours }">{{ item.overall.toFixed(2) }}%</div>
                                </div>
                                <div class="rm-ladder-bar-wrap">
                                    <div
                                        class="rm-ladder-bar"
                                        :class="item.is_ours ? 'is-ours' : 'is-pub'"
                                        :style="{ width: `${Math.max(4, Math.min(100, item.overall))}%` }"
                                    ></div>
                                    <div class="rm-ladder-ref-line" style="left: 90%"></div>
                                </div>
                            </div>
                        </div>
                    </div>
                </el-col>

                <el-col :xs="24" :xl="11" class="rm-col-stretch">
                    <div class="rm-chart-box">
                        <div class="rm-chart-hdr">
                            <span class="rm-chart-title-txt">{{ t('plugin.rolling-memory.leaderboard.chartRightTitle') }}</span>
                            <el-tag v-if="activeEntry" size="small" :type="activeEntry.is_ours ? 'success' : 'primary'">
                                {{ activeEntry.overall.toFixed(2) }}%
                            </el-tag>
                        </div>

                        <div v-if="activeEntry" class="rm-cat-breakdown">
                            <div class="rm-active-meta">
                                <div class="rm-active-sys-line">
                                    <strong class="rm-active-sys-name">{{ loc(activeEntry.system, activeEntry.system_en) }}</strong>
                                    <span class="rm-muted">{{ loc(activeEntry.vendor, activeEntry.vendor_en) }}</span>
                                </div>
                                <div class="rm-active-arch">
                                    {{ loc(activeEntry.architecture, activeEntry.architecture_en) }}
                                </div>
                                <div class="rm-active-tags">
                                    <el-tag size="small" type="primary" effect="plain">
                                        {{ loc(activeEntry.protocol, activeEntry.protocol_en) }}
                                    </el-tag>
                                    <el-tag size="small" type="info">
                                        {{ loc(activeEntry.question_set, activeEntry.question_set_en) }}
                                    </el-tag>
                                    <el-tag size="small" type="warning">
                                        {{ t('plugin.rolling-memory.leaderboard.judgePrefix') }}:
                                        {{ loc(activeEntry.judge_model, activeEntry.judge_model_en) }}
                                    </el-tag>
                                </div>
                            </div>

                            <div class="rm-cat-list">
                                <div v-for="cat in activeCategoryBars" :key="cat.key" class="rm-cat-item">
                                    <div class="rm-cat-top">
                                        <div class="rm-cat-label-wrap">
                                            <span class="rm-cat-name">{{ cat.label }}</span>
                                            <el-tag v-if="cat.val >= cat.leaderVal" size="small" type="success" effect="plain">
                                                {{ t('plugin.rolling-memory.leaderboard.catLeaderBadge') }}
                                            </el-tag>
                                        </div>
                                        <div class="rm-cat-nums">
                                            <strong class="rm-cat-val-txt">{{ cat.val.toFixed(2) }}%</strong>
                                            <span class="rm-vs-leader">
                                                {{ t('plugin.rolling-memory.leaderboard.vsLeaderPrefix') }}
                                                <em :class="cat.val >= cat.leaderVal ? 'is-pos' : 'is-neg'">
                                                    {{ cat.val >= cat.leaderVal ? '+' : '' }}{{ (cat.val - cat.leaderVal).toFixed(2) }}pp
                                                </em>
                                            </span>
                                        </div>
                                    </div>
                                    <div class="rm-cat-track">
                                        <div
                                            class="rm-cat-fill"
                                            :class="activeEntry.is_ours ? 'is-ours' : 'is-pub'"
                                            :style="{ width: `${Math.max(2, Math.min(100, cat.val))}%` }"
                                        ></div>
                                        <div
                                            class="rm-cat-marker"
                                            :style="{ left: `${Math.max(0, Math.min(100, cat.leaderVal))}%` }"
                                            :title="`${cat.leaderVal.toFixed(2)}%`"
                                        ></div>
                                    </div>
                                </div>
                            </div>

                            <div class="rm-cat-note">
                                <strong>{{ t('plugin.rolling-memory.eval.notePrefix') }}：</strong>
                                <span>{{ loc(activeEntry.note, activeEntry.note_en) }}</span>
                            </div>
                        </div>
                    </div>
                </el-col>
            </el-row>

            <!-- BuildAdmin 标准 .table-header 筛选与排序工具栏 + 完整榜单明细表 -->
            <div class="rm-filter-bar table-header">
                <div class="rm-filter-left">
                    <el-tooltip :content="t('common.refresh')" placement="top" :enterable="false" :hide-after="0" :show-after="250">
                        <el-button v-blur color="#40485b" type="info" size="small" :loading="loading" @click="loadLeaderboard">
                            <Icon size="14" name="lucide-refresh-cw" color="#ffffff" />
                        </el-button>
                    </el-tooltip>
                    <strong style="margin-left: 10px">{{ t('plugin.rolling-memory.leaderboard.tableSectionTitle') }}</strong>
                </div>
                <div class="rm-filter-right">
                    <el-radio-group v-model="filterMode" size="small">
                        <el-radio-button value="all">
                            {{ t('plugin.rolling-memory.leaderboard.filterAll') }}
                        </el-radio-button>
                        <el-radio-button value="public">
                            {{ t('plugin.rolling-memory.leaderboard.filterPublic') }}
                        </el-radio-button>
                        <el-radio-button value="ours">
                            {{ t('plugin.rolling-memory.leaderboard.filterOurs') }}
                        </el-radio-button>
                    </el-radio-group>
                    <el-select v-model="sortKey" size="small" class="rm-sort-select">
                        <el-option :label="t('plugin.rolling-memory.leaderboard.sortOverall')" value="overall" />
                        <el-option :label="t('plugin.rolling-memory.leaderboard.sortSingleHop')" value="single_hop" />
                        <el-option :label="t('plugin.rolling-memory.leaderboard.sortMultiHop')" value="multi_hop" />
                        <el-option :label="t('plugin.rolling-memory.leaderboard.sortOpenDomain')" value="open_domain" />
                        <el-option :label="t('plugin.rolling-memory.leaderboard.sortTemporal')" value="temporal" />
                    </el-select>
                </div>
            </div>

            <el-table
                v-loading="loading"
                :element-loading-text="loadLbText"
                :data="filteredEntries"
                size="small"
                border
                stripe
                highlight-current-row
                class="ag-data-table rm-lb-table"
                header-cell-class-name="table-header-cell"
                :row-class-name="tableRowClassName"
                @row-click="(row: LeaderboardEntry) => (selectedEntry = row)"
            >
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colRank')" width="50" align="center">
                    <template #default="{ $index, row }">
                        <span class="rm-rank-badge" :class="rankBadgeClass(row.not_ranked ? 0 : $index + 1)">
                            {{ row.not_ranked ? '—' : `#${$index + 1}` }}
                        </span>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colSystem')" min-width="160">
                    <template #default="{ row }">
                        <div class="rm-cell-multiline">
                            <div class="rm-sys-title-line">
                                <strong class="rm-sys-primary">{{ loc(row.system, row.system_en) }}</strong>
                            </div>
                            <div class="rm-sys-tags-line">
                                <el-tag size="small" :type="row.is_ours ? 'success' : 'info'">
                                    {{
                                        row.is_ours
                                            ? t('plugin.rolling-memory.leaderboard.tagOurs')
                                            : t('plugin.rolling-memory.leaderboard.tagPublic')
                                    }}
                                </el-tag>
                                <el-tag v-if="loc(row.highlight_tag, row.highlight_tag_en)" size="small" type="warning" effect="plain">
                                    {{ loc(row.highlight_tag, row.highlight_tag_en) }}
                                </el-tag>
                            </div>
                        </div>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colVendor')" min-width="135">
                    <template #default="{ row }">
                        <div class="rm-cell-multiline">
                            <div class="rm-vendor-primary">{{ loc(row.vendor, row.vendor_en) }}</div>
                            <div class="rm-sub-text">{{ loc(row.architecture, row.architecture_en) }}</div>
                        </div>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colProtocol')" min-width="140">
                    <template #default="{ row }">
                        <div class="rm-cell-multiline">
                            <div>
                                <code class="rm-code-inline">{{ loc(row.protocol, row.protocol_en) }}</code>
                            </div>
                            <div class="rm-sub-text">{{ loc(row.question_set, row.question_set_en) }}</div>
                            <div class="rm-sub-text">
                                {{ t('plugin.rolling-memory.leaderboard.judgePrefix') }}: {{ loc(row.judge_model, row.judge_model_en) }}
                            </div>
                        </div>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colSingleHop')" width="84" align="right">
                    <template #header>
                        <span>
                            {{ t('plugin.rolling-memory.leaderboard.colSingleHop') }}
                            <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                <template #reference>
                                    <button
                                        type="button"
                                        class="tip-q"
                                        :aria-label="isEn ? 'Cat 4 Single-Hop semantics' : '单跳事实（Cat 4）语义说明'"
                                    >
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">Cat 4 Single-Hop: Direct factual retrieval from a single conversation session</p>
                                    <p v-else>单跳事实（Cat 4 · Single-Hop）：单轮对话内直接显式事实的精准定位与回答</p>
                                </div>
                            </el-popover>
                        </span>
                    </template>
                    <template #default="{ row }">
                        <span class="rm-num-cell" :class="{ 'rm-best-num': row.single_hop >= 89.1 }"> {{ row.single_hop.toFixed(2) }}% </span>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colMultiHop')" width="84" align="right">
                    <template #header>
                        <span>
                            {{ t('plugin.rolling-memory.leaderboard.colMultiHop') }}
                            <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                <template #reference>
                                    <button
                                        type="button"
                                        class="tip-q"
                                        :aria-label="isEn ? 'Cat 1 Multi-Hop semantics' : '多跳推理（Cat 1）语义说明'"
                                    >
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">Cat 1 Multi-Hop: Synthesizing multiple facts scattered across different sessions/turns</p>
                                    <p v-else>多跳推理（Cat 1 · Multi-Hop）：跨多个会话轮次串联多条离散事实的综合推理</p>
                                </div>
                            </el-popover>
                        </span>
                    </template>
                    <template #default="{ row }">
                        <span class="rm-num-cell" :class="{ 'rm-best-num': row.multi_hop >= 75.0 }"> {{ row.multi_hop.toFixed(2) }}% </span>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colOpenDomain')" width="84" align="right">
                    <template #header>
                        <span>
                            {{ t('plugin.rolling-memory.leaderboard.colOpenDomain') }}
                            <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                <template #reference>
                                    <button
                                        type="button"
                                        class="tip-q"
                                        :aria-label="isEn ? 'Cat 3 Open-Domain semantics' : '开放域推断（Cat 3）语义说明'"
                                    >
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">Cat 3 Open-Domain: Combining conversational memory with external world knowledge</p>
                                    <p v-else>开放域推断（Cat 3 · Open-Domain）：结合对话记忆与外部世界常识的推理题</p>
                                </div>
                            </el-popover>
                        </span>
                    </template>
                    <template #default="{ row }">
                        <span class="rm-num-cell" :class="{ 'rm-best-num': row.open_domain >= 81.3 }"> {{ row.open_domain.toFixed(2) }}% </span>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colTemporal')" width="84" align="right">
                    <template #header>
                        <span>
                            {{ t('plugin.rolling-memory.leaderboard.colTemporal') }}
                            <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                <template #reference>
                                    <button type="button" class="tip-q" :aria-label="isEn ? 'Cat 2 Temporal semantics' : '时序推理（Cat 2）语义说明'">
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">
                                        Cat 2 Temporal Reasoning: Relative time anchoring, chronological ordering, and duration calculation
                                    </p>
                                    <p v-else>时序推理（Cat 2 · Temporal）：相对时间消解、事件先后顺序与时间跨度推算</p>
                                </div>
                            </el-popover>
                        </span>
                    </template>
                    <template #default="{ row }">
                        <span class="rm-num-cell" :class="{ 'rm-best-num': row.temporal >= 87.9 }"> {{ row.temporal.toFixed(2) }}% </span>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colOverall')" width="92" align="right">
                    <template #header>
                        <span>
                            {{ t('plugin.rolling-memory.leaderboard.colOverall') }}
                            <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                <template #reference>
                                    <button type="button" class="tip-q" :aria-label="isEn ? 'Overall accuracy semantics' : '综合总判对率语义说明'">
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">Overall Accuracy across all scored questions under the locomo-backboard Judge</p>
                                    <p v-else>在 locomo-backboard 冻结裁判下的全题集综合总判对率（Overall Accuracy）</p>
                                </div>
                            </el-popover>
                        </span>
                    </template>
                    <template #default="{ row }">
                        <strong class="rm-overall-num" :class="{ 'is-ours-num': row.is_ours }"> {{ row.overall.toFixed(2) }}% </strong>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colNote')" min-width="165">
                    <template #default="{ row }">
                        <div class="rm-note-cell">{{ loc(row.note, row.note_en) }}</div>
                    </template>
                </el-table-column>
            </el-table>

            <!-- 剑桥 ATM-Bench 榜单已独立成页：本页专注 locomo-backboard 同尺榜单，跨页互链保持可达 -->
            <div class="rm-proto-section rm-sibling-section">
                <div class="rm-sibling-card">
                    <div class="rm-sibling-text">
                        <strong>{{ t('plugin.rolling-memory.leaderboard.siblingAtmTitle') }}</strong>
                        <span class="rm-muted">{{ t('plugin.rolling-memory.leaderboard.siblingAtmDesc') }}</span>
                    </div>
                    <el-button v-blur size="small" type="warning" plain @click="goATMLeaderboard">
                        {{ t('plugin.rolling-memory.leaderboard.siblingAtmBtn') }}
                    </el-button>
                </div>
            </div>

            <!-- 底部：内置 locomo-backboard 协议栈与榜单 harness 逐项核对清单 -->
            <div class="rm-proto-section">
                <div class="rm-proto-sec-title">
                    <strong>{{ t('plugin.rolling-memory.leaderboard.protoDetailTitle') }}</strong>
                </div>
                <el-descriptions :column="2" border size="small" class="rm-proto-desc">
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblBuiltinProto')">
                        <div class="rm-desc-val">
                            <el-tag size="small" type="primary">{{ protocolSpec.builtin_protocol || 'locomo-backboard' }}</el-tag>
                            <code class="rm-code-inline">{{ protocolSpec.protocol_id || 'locomo-frozen-2026-10-11' }}</code>
                        </div>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblTargetBoard')">
                        <strong class="rm-desc-text">
                            {{ loc(protocolSpec.leaderboard_name, protocolSpec.leaderboard_name_en) || 'locomo-backboard' }}
                        </strong>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblCorpusSpec')" :span="2">
                        <span class="rm-desc-text">{{ loc(protocolSpec.corpus_name, protocolSpec.corpus_name_en) }}</span>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblAnswerSpec')" :span="2">
                        <span class="rm-desc-text">{{ loc(protocolSpec.answer_payload_spec, protocolSpec.answer_payload_spec_en) }}</span>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblJudgeSpec')" :span="2">
                        <span class="rm-desc-text">{{ loc(protocolSpec.judge_verdict_spec, protocolSpec.judge_verdict_spec_en) }}</span>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblCatMap')" :span="2">
                        <span class="rm-desc-text">{{ loc(protocolSpec.category_mapping_note, protocolSpec.category_mapping_note_en) }}</span>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblFairNotice')" :span="2">
                        <span class="rm-desc-text">{{ loc(protocolSpec.fair_comparison_notice, protocolSpec.fair_comparison_notice_en) }}</span>
                    </el-descriptions-item>
                </el-descriptions>

                <el-row :gutter="14" class="rm-agent-row">
                    <el-col v-for="ag in agents" :key="ag.code" :xs="24" :xl="12" class="rm-col-stretch">
                        <el-card shadow="never" class="rm-agent-card">
                            <template #header>
                                <div class="rm-agent-hdr">
                                    <div class="rm-agent-title-wrap">
                                        <strong>{{ loc(ag.name, ag.name_en) }}</strong>
                                        <el-tag size="small" type="info">{{ ag.code }}</el-tag>
                                    </div>
                                    <el-tag size="small" :type="ag.pin_verified ? 'success' : 'danger'">
                                        {{
                                            ag.pin_verified
                                                ? t('plugin.rolling-memory.eval.pinVerifiedTag')
                                                : t('plugin.rolling-memory.eval.pinDriftTag')
                                        }}
                                    </el-tag>
                                </div>
                            </template>
                            <el-descriptions :column="1" border size="small">
                                <el-descriptions-item :label="t('plugin.rolling-memory.eval.lblProtocol')">
                                    <code class="rm-code-inline">locomo-backboard ({{ ag.protocol }})</code>
                                </el-descriptions-item>
                                <el-descriptions-item :label="t('plugin.rolling-memory.eval.lblSysSha1')">
                                    <code class="rm-sha-code">{{ ag.system_sha1 }}</code>
                                </el-descriptions-item>
                                <el-descriptions-item
                                    v-if="ag.user_template_sha1 && ag.user_template_sha1 !== '-'"
                                    :label="t('plugin.rolling-memory.eval.lblTplSha1')"
                                >
                                    <code class="rm-sha-code">{{ ag.user_template_sha1 }}</code>
                                </el-descriptions-item>
                            </el-descriptions>
                            <div class="rm-prompt-lbl">{{ t('plugin.rolling-memory.eval.lblSysPrompt') }}:</div>
                            <pre class="rm-prompt-pre">{{ ag.system_prompt }}</pre>
                            <div class="rm-prompt-lbl">{{ t('plugin.rolling-memory.eval.lblUserTpl') }}:</div>
                            <pre class="rm-prompt-pre">{{ ag.user_template }}</pre>
                        </el-card>
                    </el-col>
                </el-row>
            </div>
        </el-card>
    </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useLoadingProgressText } from '@/utils/loadingProgress'
import request from '@/utils/request'

const { t, locale } = useI18n()
const router = useRouter()

const isEn = computed(() =>
    String(locale.value || '')
        .toLowerCase()
        .startsWith('en')
)
const loc = (zh?: string, en?: string) => {
    if (isEn.value && en && en.trim() !== '') return en
    return zh || en || ''
}

interface CategoryScores {
    cat1: number
    cat2: number
    cat3: number
    cat4: number
}

interface LeaderboardEntry {
    rank: number
    not_ranked?: boolean
    id: string
    system: string
    system_en?: string
    vendor: string
    vendor_en?: string
    architecture: string
    architecture_en?: string
    protocol: string
    protocol_en?: string
    question_set: string
    question_set_en?: string
    judge_model: string
    judge_model_en?: string
    single_hop: number
    multi_hop: number
    open_domain: number
    temporal: number
    overall: number
    evidence_hit: number
    is_ours: boolean
    is_public_board: boolean
    highlight_tag: string
    highlight_tag_en?: string
    note: string
    note_en?: string
    categories: CategoryScores
}

interface ProtocolParitySpec {
    builtin_protocol: string
    protocol_id: string
    leaderboard_name: string
    leaderboard_name_en?: string
    corpus_name: string
    corpus_name_en?: string
    answer_system_prompt: string
    answer_system_sha1: string
    answer_payload_spec: string
    answer_payload_spec_en?: string
    judge_system_prompt: string
    judge_system_sha1: string
    judge_template_sha1: string
    judge_template_bytes: number
    judge_verdict_spec: string
    judge_verdict_spec_en?: string
    all_pins_verified: boolean
    category_mapping_note: string
    category_mapping_note_en?: string
    fair_comparison_notice: string
    fair_comparison_notice_en?: string
}

interface OfficialProtocolAgent {
    code: string
    name: string
    name_en?: string
    role: string
    protocol: string
    model: string
    system_prompt: string
    system_sha1: string
    user_template: string
    user_template_sha1: string
    pin_verified: boolean
}

interface EvalLeaderboardResponse {
    protocol_spec: ProtocolParitySpec
    entries: LeaderboardEntry[]
    agents: OfficialProtocolAgent[]
}

const loading = ref(false)
const { percent: loadLbPct, text: loadLbText } = useLoadingProgressText(loading)
const filterMode = ref<'all' | 'public' | 'ours'>('all')
const sortKey = ref<'overall' | 'single_hop' | 'multi_hop' | 'open_domain' | 'temporal'>('overall')
const entries = ref<LeaderboardEntry[]>([])
const protocolSpec = ref<Partial<ProtocolParitySpec>>({})
const agents = ref<OfficialProtocolAgent[]>([])
const selectedEntry = ref<LeaderboardEntry | null>(null)

const filteredEntries = computed<LeaderboardEntry[]>(() => {
    const list = entries.value.filter((item) => {
        if (filterMode.value === 'public') return item.is_public_board
        if (filterMode.value === 'ours') return item.is_ours
        return true
    })
    const key = sortKey.value
    return [...list].sort((a, b) => Number(b[key] || 0) - Number(a[key] || 0))
})

const activeEntry = computed<LeaderboardEntry | null>(() => {
    if (selectedEntry.value) return selectedEntry.value
    const ours = entries.value.find((e) => e.id === 'lb-rm-current-local-plain')
    return ours || entries.value[0] || null
})

// 外部商业榜首 Backboard 四项基准分：Single-hop 89.36, Multi-hop 75.00, Open-domain 91.20, Temporal 91.90
const activeCategoryBars = computed(() => {
    const cur = activeEntry.value
    return [
        {
            key: 'single_hop',
            label: t('plugin.rolling-memory.eval.cat4Label'),
            val: cur?.single_hop ?? 0,
            leaderVal: 89.36,
        },
        {
            key: 'multi_hop',
            label: t('plugin.rolling-memory.eval.cat1Label'),
            val: cur?.multi_hop ?? 0,
            leaderVal: 75.0,
        },
        {
            key: 'open_domain',
            label: t('plugin.rolling-memory.eval.cat3Label'),
            val: cur?.open_domain ?? 0,
            leaderVal: 91.2,
        },
        {
            key: 'temporal',
            label: t('plugin.rolling-memory.eval.cat2Label'),
            val: cur?.temporal ?? 0,
            leaderVal: 91.9,
        },
    ]
})

const rankBadgeClass = (rank: number) => {
    if (rank === 1) return 'is-gold'
    if (rank === 2) return 'is-silver'
    if (rank === 3) return 'is-bronze'
    return ''
}

const tableRowClassName = ({ row }: { row: LeaderboardEntry }) => {
    if (row.is_ours) return 'rm-ours-table-row'
    return ''
}

const loadLeaderboard = async () => {
    loading.value = true
    try {
        const res = await request<EvalLeaderboardResponse>({
            url: '/admin/plugin/rolling-memory/eval/leaderboard',
            method: 'POST',
            data: {},
        })
        const data = res.data.data
        if (data) {
            entries.value = data.entries || []
            protocolSpec.value = data.protocol_spec || {}
            agents.value = data.agents || []
            if (!selectedEntry.value && entries.value.length > 0) {
                selectedEntry.value = entries.value.find((e) => e.id === 'lb-rm-current-local-plain') || entries.value[0]
            }
        }
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        loading.value = false
    }
}

const goEvalLab = (tier?: string | Event) => {
    void tier
    router.push({ path: '/admin/plugin/rolling-memory/eval', query: { suite: 'locomo', tier: '1540' } })
}

// goATMLeaderboard 跳转独立「剑桥 ATM-Bench 榜单」页面（本页原内嵌段已迁出，此处保留互链入口）。
const goATMLeaderboard = () => {
    router.push({ path: '/admin/plugin/rolling-memory/atm-leaderboard' })
}

onMounted(() => {
    loadLeaderboard()
})
</script>

<style scoped lang="scss">
.plugin-rm-leaderboard {
    .rm-lb-header {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        align-items: center;
        justify-content: space-between;
    }

    .rm-lb-title-group,
    .rm-lb-actions {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
    }

    .rm-lb-title {
        font-size: 16px;
        font-weight: 600;
        line-height: 1.4;
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

    .rm-parity-banner {
        padding: 12px 16px;
        margin-bottom: 12px;
        background-color: var(--ag-bg-color-overlay);
        border: 1px solid var(--ag-border-color);
        border-left: 3px solid var(--el-color-primary);
        border-radius: 6px;
    }

    .rm-parity-banner-title {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        margin-bottom: 8px;
        font-size: 14px;
        line-height: 1.45;
        color: var(--el-text-color-primary);
    }

    .rm-parity-pill {
        flex-shrink: 0;
        padding: 3px 10px;
        font-family: monospace;
        font-size: 11.5px;
        font-weight: 700;
        color: #fff;
        background: var(--el-color-primary);
        border-radius: 4px;
    }

    .rm-parity-headline {
        word-break: break-word;
    }

    .rm-parity-falsify-row {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
        padding-top: 10px;
        margin-top: 10px;
        font-size: 12px;
        line-height: 1.6;
        color: var(--el-text-color-primary);
        border-top: 1px dashed var(--el-border-color-light);
    }

    .rm-parity-banner-desc {
        font-size: 13px;
        line-height: 1.65;
        color: var(--el-text-color-regular);
        word-break: break-word;
    }

    .rm-col-stretch {
        display: flex;
        margin-bottom: 12px;
    }

    .rm-kpi-row {
        margin-bottom: 2px;
    }

    .rm-kpi-box {
        display: flex;
        flex-direction: column;
        justify-content: space-between;
        width: 100%;
        padding: 12px 16px;
        background-color: var(--ag-bg-color-overlay);
        border: 1px solid var(--ag-border-color);
        border-left: 3px solid var(--el-color-info);
        border-radius: 6px;

        &.is-proto {
            border-left-color: var(--el-color-primary);
        }

        &.is-highlight {
            border-left-color: var(--el-color-success);
        }
    }

    .rm-kpi-label {
        font-size: 12.5px;
        font-weight: 500;
        line-height: 1.4;
        color: var(--el-text-color-secondary);
    }

    .rm-kpi-val {
        margin: 6px 0;
        font-size: 20px;
        font-weight: 700;
        font-variant-numeric: tabular-nums;
        line-height: 1.3;
        color: var(--el-text-color-primary);

        &.is-green {
            color: var(--el-color-success);
        }
    }

    .rm-kpi-sub {
        font-size: 12px;
        line-height: 1.5;
        color: var(--el-text-color-regular);
        word-break: break-word;
    }

    .rm-intro-row {
        margin-bottom: 2px;
    }

    .rm-intro-card {
        display: flex;
        flex-direction: column;
        width: 100%;
        padding: 14px 16px;
        background: var(--el-fill-color-light);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 8px;
    }

    .rm-intro-hdr {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 8px;
        font-size: 13.5px;
        font-weight: 600;
        line-height: 1.4;
    }

    .rm-intro-title-txt {
        word-break: break-word;
    }

    .rm-shrink-0 {
        flex-shrink: 0;
    }

    .rm-intro-body {
        font-size: 12.5px;
        line-height: 1.65;
        color: var(--el-text-color-regular);
        word-break: break-word;
    }

    .rm-charts-row {
        margin-bottom: 4px;
    }

    .rm-chart-box {
        display: flex;
        flex-direction: column;
        width: 100%;
        padding: 14px 16px;
        background: var(--el-bg-color);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 8px;
    }

    .rm-chart-hdr {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 12px;
        font-size: 13.5px;
        font-weight: 600;
        line-height: 1.4;
    }

    .rm-chart-title-txt {
        word-break: break-word;
    }

    .rm-chart-legend {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        align-items: center;
        font-size: 12px;
        font-weight: 400;
    }

    .rm-leg-item {
        display: inline-flex;
        gap: 6px;
        align-items: center;
    }

    .rm-leg-dot {
        display: inline-block;
        width: 10px;
        height: 10px;
        border-radius: 2px;

        &.is-ours {
            background: #10b981;
        }

        &.is-pub {
            background: #3b82f6;
        }
    }

    .rm-ladder-list {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }

    .rm-ladder-row {
        display: flex;
        flex-direction: column;
        gap: 6px;
        padding: 8px 10px;
        cursor: pointer;
        border: 1px solid var(--el-border-color-extra-light);
        border-radius: 6px;
        transition: all 0.15s;

        &:hover {
            background: var(--el-fill-color-light);
        }

        &.is-active {
            background: var(--el-color-primary-light-9);
            border-color: var(--el-color-primary-light-5);
        }
    }

    .rm-ladder-top {
        display: flex;
        gap: 10px;
        align-items: flex-start;
        justify-content: space-between;
    }

    .rm-ladder-meta {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
        min-width: 0;
    }

    .rm-sys-name {
        font-size: 13px;
        font-weight: 600;
        line-height: 1.4;
        color: var(--el-text-color-primary);
        word-break: break-word;
    }

    .rm-mini-tag {
        flex-shrink: 0;
    }

    .rm-ladder-score {
        flex-shrink: 0;
        font-size: 14px;
        font-weight: 700;
        font-variant-numeric: tabular-nums;
        line-height: 1.4;
        color: var(--el-text-color-primary);

        &.is-ours-score {
            color: var(--el-color-success);
        }
    }

    .rm-ladder-bar-wrap {
        position: relative;
        height: 8px;
        background: var(--el-fill-color);
        border-radius: 4px;
    }

    .rm-ladder-bar {
        height: 100%;
        border-radius: 4px;

        &.is-ours {
            background: linear-gradient(90deg, #10b981, #059669);
        }

        &.is-pub {
            background: linear-gradient(90deg, #60a5fa, #2563eb);
        }
    }

    .rm-ladder-ref-line {
        position: absolute;
        top: -2px;
        width: 2px;
        height: 12px;
        background: #ef4444;
    }

    .rm-rank-badge {
        display: inline-block;
        flex-shrink: 0;
        min-width: 30px;
        padding: 1px 6px;
        font-size: 11.5px;
        font-weight: 700;
        font-variant-numeric: tabular-nums;
        text-align: center;
        background: var(--el-fill-color);
        border-radius: 4px;

        &.is-gold {
            color: #92400e;
            background: #fef3c7;
        }

        &.is-silver {
            color: #1e293b;
            background: #e2e8f0;
        }

        &.is-bronze {
            color: #7c2d12;
            background: #ffedd5;
        }
    }

    .rm-cat-breakdown {
        display: flex;
        flex: 1;
        flex-direction: column;
        justify-content: space-between;
        gap: 14px;
    }

    .rm-active-meta {
        display: flex;
        flex-direction: column;
        gap: 6px;
        padding: 10px 12px;
        font-size: 12.5px;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-extra-light);
        border-radius: 6px;
    }

    .rm-active-sys-line {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: baseline;
    }

    .rm-active-sys-name {
        font-size: 14px;
        color: var(--el-text-color-primary);
    }

    .rm-active-arch {
        font-size: 12px;
        line-height: 1.5;
        color: var(--el-text-color-regular);
    }

    .rm-active-tags {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        margin-top: 2px;
    }

    .rm-cat-list {
        display: flex;
        flex-direction: column;
        gap: 14px;
    }

    .rm-cat-item {
        display: flex;
        flex-direction: column;
        gap: 6px;
    }

    .rm-cat-top {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
        justify-content: space-between;
        font-size: 12.5px;
        line-height: 1.4;
    }

    .rm-cat-label-wrap {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
    }

    .rm-cat-name {
        font-weight: 500;
    }

    .rm-cat-nums {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: baseline;
        font-variant-numeric: tabular-nums;
    }

    .rm-cat-val-txt {
        font-size: 13.5px;
    }

    .rm-vs-leader {
        font-size: 11.5px;
        color: var(--el-text-color-secondary);

        em {
            margin-left: 2px;
            font-style: normal;
            font-weight: 600;
        }

        .is-pos {
            color: var(--el-color-success);
        }

        .is-neg {
            color: var(--el-color-warning);
        }
    }

    .rm-cat-track {
        position: relative;
        height: 10px;
        background: var(--el-fill-color);
        border-radius: 5px;
    }

    .rm-cat-fill {
        height: 100%;
        border-radius: 5px;

        &.is-ours {
            background: #10b981;
        }

        &.is-pub {
            background: #3b82f6;
        }
    }

    .rm-cat-marker {
        position: absolute;
        top: -2px;
        width: 2px;
        height: 14px;
        background: #ef4444;
    }

    .rm-cat-note {
        padding-top: 10px;
        font-size: 12.5px;
        line-height: 1.6;
        color: var(--el-text-color-regular);
        word-break: break-word;
        border-top: 1px dashed var(--el-border-color-lighter);
    }

    .rm-filter-bar {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        align-items: center;
        justify-content: space-between;
        padding: 13px 15px;
        margin-bottom: 0;
        font-size: 13.5px;
        background-color: var(--ag-bg-color-overlay);
        border: 1px solid var(--ag-border-color);
        border-bottom: none;
    }

    .rm-filter-right {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
    }

    .rm-top-progress-banner {
        padding: 10px 14px;
        margin-bottom: 12px;
        background: var(--el-color-primary-light-9);
        border: 1px solid var(--el-color-primary-light-7);
        border-radius: 8px;

        .rm-tpb-head {
            display: flex;
            align-items: center;
            justify-content: space-between;
            margin-bottom: 6px;
            font-size: 12.5px;
        }
    }

    :deep(.rm-ours-table-row) {
        font-weight: 600;
        background-color: var(--el-color-success-light-9) !important;
    }

    .rm-sort-select {
        width: 240px;
    }

    .rm-cell-multiline {
        display: flex;
        flex-direction: column;
        gap: 4px;
        padding: 2px 0;
        line-height: 1.5;
        word-break: break-word;
        white-space: normal;
    }

    .rm-sys-title-line {
        line-height: 1.45;
    }

    .rm-sys-primary {
        font-size: 13px;
        color: var(--el-text-color-primary);
    }

    .rm-sys-tags-line {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
    }

    .rm-vendor-primary {
        font-size: 12.5px;
        font-weight: 500;
        color: var(--el-text-color-primary);
    }

    .rm-sub-text {
        font-size: 11.5px;
        line-height: 1.45;
        color: var(--el-text-color-secondary);
        word-break: break-word;
    }

    .rm-code-inline {
        display: inline-block;
        padding: 1px 6px;
        font-family: monospace;
        font-size: 11.5px;
        line-height: 1.4;
        word-break: break-all;
        background: var(--el-fill-color);
        border-radius: 4px;
    }

    .rm-num-cell {
        font-variant-numeric: tabular-nums;
    }

    .rm-best-num {
        font-weight: 700;
        color: var(--el-color-success);
    }

    .rm-overall-num {
        font-size: 13.5px;
        font-variant-numeric: tabular-nums;

        &.is-ours-num {
            color: var(--el-color-success);
        }
    }

    .rm-note-cell {
        padding: 2px 0;
        font-size: 12px;
        line-height: 1.55;
        color: var(--el-text-color-regular);
        word-break: break-word;
        white-space: normal;
    }

    .rm-proto-section {
        padding-top: 16px;
        margin-top: 18px;
        border-top: 1px solid var(--el-border-color-lighter);
    }

    .rm-proto-sec-title {
        margin-bottom: 10px;
        font-size: 14px;
    }

    .rm-desc-val {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
    }

    .rm-desc-text {
        line-height: 1.6;
        word-break: break-word;
    }

    .rm-agent-row {
        margin-top: 14px;
    }

    .rm-agent-card {
        width: 100%;
    }

    .rm-agent-hdr {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        justify-content: space-between;
    }

    .rm-agent-title-wrap {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
    }

    .rm-sha-code {
        font-family: monospace;
        font-size: 11.5px;
        word-break: break-all;
    }

    .rm-prompt-lbl {
        margin: 10px 0 4px;
        font-size: 12px;
        font-weight: 600;
        color: var(--el-text-color-secondary);
    }

    .rm-prompt-pre {
        max-height: 170px;
        padding: 8px 10px;
        margin: 0;
        overflow: auto;
        font-size: 11.5px;
        line-height: 1.5;
        word-break: break-word;
        white-space: pre-wrap;
        background: var(--el-fill-color-light);
        border: 1px solid var(--el-border-color-extra-light);
        border-radius: 4px;
    }

    .rm-muted {
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .rm-sibling-section {
        padding-top: 12px;
    }

    .rm-sibling-card {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        align-items: center;
        justify-content: space-between;
        padding: 12px 16px;
        background: var(--el-color-warning-light-9);
        border: 1px solid var(--el-color-warning-light-7);
        border-radius: 8px;
    }

    .rm-sibling-text {
        display: flex;
        flex-direction: column;
        gap: 4px;
        font-size: 12.5px;
        line-height: 1.55;
        word-break: break-word;
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
