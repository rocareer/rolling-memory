<!-- plugin: rolling-memory v0.0.58 -->
<!--
    Rolling-Memory 记忆系统 · 独立「剑桥 ATM-Bench 榜单」页面（plugins/rolling-memory/web/views/atmLeaderboard.vue）。
    落位副本：web/src/views/admin/plugin/rolling-memory/atmLeaderboard.vue（由安装器复制，逐字节一致）。
    职责：
      1) 独立承载剑桥大学 ATM-Bench 多模态个人指称记忆基准榜单（Tab 1 主榜 1,013 题全量、Tab 2 Hard 榜 31 题；官方无 100 题档位），
         与「LoCoMo-Backboard 榜单」页面平行成页、互不混排（原内嵌段已从 leaderboard.vue 迁出）；
      2) 展示官方 atmbench-official-v1 协议（两枚 SHA-1 钉：MMRag 答题系统提示词 · 混合 QS 判官模板）、
         11,034 条 SGM 多模态语料规模、读档梯度实测读数与外部基线（档位字面量仅作 legacy 线格式兼容，不作能力档划分）；
      3) 全量支持中英文（zh-cn / en）无混杂切换，所有卡片、表格、协议清单均采用自适应多行自然排版，零截断。
-->
<template>
    <div class="default-main plugin-rm-atm-leaderboard">
        <el-card shadow="never" class="rm-lb-card">
            <template #header>
                <div class="rm-lb-header">
                    <div class="rm-lb-title-group">
                        <span class="rm-lb-title">{{ t('plugin.rolling-memory.atmLeaderboard.title') }}</span>
                        <el-tag size="small" type="primary">
                            {{ t('plugin.rolling-memory.atmLeaderboard.protocolBadge') }}
                        </el-tag>
                        <el-tag size="small" type="success">
                            {{ t('plugin.rolling-memory.atmLeaderboard.corpusBadge') }}
                        </el-tag>
                        <el-tag size="small" :type="pinsVerified ? 'success' : 'danger'">
                            {{ t('plugin.rolling-memory.atmLeaderboard.pinsVerifiedBadge') }}
                        </el-tag>
                        <!-- 三枚徽章的悬停说明聚合为单个点击式 `?` 弹层 -->
                        <el-popover trigger="click" placement="bottom" :width="420" popper-class="rm-help-popper">
                            <template #reference>
                                <button
                                    type="button"
                                    class="tip-q"
                                    :aria-label="
                                        isEn ? 'Protocol, SGM corpus and cryptographic pin semantics' : '评测协议、SGM 语料与密码学钉校验语义说明'
                                    "
                                >
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    Built-in Evaluation Protocol: atmbench-official-v1 (Cambridge ATM-Bench MMRag Top-10 evidence retrieval + hybrid
                                    QS scoring).
                                </p>
                                <p v-else>内置评测协议：atmbench-official-v1（剑桥 ATM-Bench 官方 MMRag Top-10 证据召回 + 混合 QS 评分）。</p>
                                <p v-if="isEn">
                                    Corpus: 4-year SGM personal multimodal memory — 6,742 emails + 3,759 GPS/OCR photos + 533 videos = 11,034 items
                                    (SHA-256 verified).
                                </p>
                                <p v-else>
                                    语料：4 年跨度 SGM 个人多模态记忆——6,742 封邮件 + 3,759 张带 GPS/OCR 照片 + 533 段视频 = 11,034 条（带 SHA-256
                                    校验）。
                                </p>
                                <p v-if="isEn">
                                    Cryptographic pin verification: SHA-1 digests of the MMRag Answer System Prompt and the hybrid QS Judge Template
                                    are locked and verified.
                                </p>
                                <p v-else>密码学钉校验：MMRag 答题系统提示词与混合 QS 判官模板的 SHA-1 摘要只读锁死并实时校验。</p>
                            </div>
                        </el-popover>
                    </div>
                    <div class="rm-lb-actions">
                        <el-button v-blur size="small" type="primary" plain @click="goLoCoMoBoard">
                            {{ t('plugin.rolling-memory.atmLeaderboard.goLocomoBtn') }}
                        </el-button>
                        <el-button v-blur size="small" type="primary" plain @click="goEvalLab(query.tier)">
                            {{ t('plugin.rolling-memory.atmLeaderboard.backEvalLabBtn') }}
                        </el-button>
                        <el-button v-blur size="small" type="primary" :loading="loading" @click="loadBoard">
                            {{ t('plugin.rolling-memory.refresh') }}
                            <template v-if="loading"> ({{ loadBoardPct }}%)</template>
                        </el-button>
                    </div>
                </div>
            </template>

            <div v-if="loading" class="rm-top-progress-banner">
                <div class="rm-tpb-head">
                    <span>{{ loadBoardText }}</span>
                    <strong>{{ loadBoardPct }}%</strong>
                </div>
                <el-progress :percentage="loadBoardPct" :stroke-width="6" striped striped-flow :show-text="false" />
            </div>

            <!-- 核心声明横幅：语料规模 + 官方协议 + CLI 复现命令 -->
            <div class="rm-parity-banner">
                <div class="rm-parity-banner-title">
                    <span class="rm-parity-pill">CAMBRIDGE ATM-BENCH</span>
                    <strong class="rm-parity-headline">
                        {{ loc(board.benchmark_name, board.benchmark_name_en) || t('plugin.rolling-memory.atmLeaderboard.benchmarkFallback') }}
                    </strong>
                </div>
                <div class="rm-parity-banner-desc">
                    {{ loc(board.corpus_summary, board.corpus_summary_en) }}
                </div>
                <div class="rm-parity-banner-desc">
                    <code class="rm-code-inline">{{ board.repro_command || 'rmem eval --suite atm --tier hard --pulse-mode local_plain --top-k 50 --answerer none --judge off' }}</code>
                    <el-button
                        v-blur
                        size="small"
                        type="primary"
                        plain
                        @click="
                            copyText(
                                board.repro_command || 'rmem eval --suite atm --tier hard --pulse-mode local_plain --top-k 50 --answerer none --judge off',
                                'Copied ATM repro CLI',
                                '已复制 ATM-Bench 复现命令'
                            )
                        "
                    >
                        {{ t('plugin.rolling-memory.atmLeaderboard.copyReproBtn') }}
                    </el-button>
                </div>
                <div class="rm-parity-falsify-row">
                    <span>
                        {{
                            isEn
                                ? 'Reproducibility Notice: Deterministic 0-LLM retrieval (11,034 SGM items, Hard-31 & Main-1013) can be recomputed offline at zero model cost; end-to-end QA with the official hybrid QS judge runs with a user-configured LLM endpoint. All runs execute on the production local engine in an isolated scope. The corpus is obtained under the upstream license — one CLI command downloads the required files (~35 MB), rebuilds them locally and verifies the digests: rmem eval --fetch atm --accept-upstream-license'
                                : '复现说明：0-LLM 确定性检索（11,034 条 SGM 语料，Hard-31 与 Main-1,013）支持离线零成本复算；端到端问答与官方混合 QS 评分支持配置自有模型接口验证。所有评测均在隔离作用域中调用生产同源本地引擎执行。语料按上游许可获取——一条命令即可下载必要文件（约 35 MB）、在本机重建并校验摘要：rmem eval --fetch atm --accept-upstream-license'
                        }}
                    </span>
                    <el-button size="small" type="primary" @click="goEvalLab(boardTab === 'main' ? '1013' : 'hard')">
                        {{ isEn ? 'Open in Evaluation Lab →' : '在评测实验室中复现 →' }}
                    </el-button>
                </div>
            </div>

            <!-- 顶部 4 项核心指标卡（小屏/笔记本自动 2×2 宽敞排布，超宽屏 1×4） -->
            <el-row :gutter="12" class="rm-kpi-row">
                <el-col :xs="24" :sm="12" :xl="6" class="rm-col-stretch">
                    <div class="rm-kpi-box is-proto">
                        <div class="rm-kpi-label">
                            {{ t('plugin.rolling-memory.atmLeaderboard.kpi1Label') }}
                            <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                <template #reference>
                                    <button type="button" class="tip-q" :aria-label="isEn ? 'SGM corpus scale semantics' : 'SGM 语料规模说明'">
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">
                                        Official University of Cambridge SGM corpus scale: 4-year personal multimodal memory of 11,034 items.
                                    </p>
                                    <p v-else>剑桥大学官方 SGM 语料规模：跨 4 年的个人多模态记忆共 11,034 条。</p>
                                </div>
                            </el-popover>
                        </div>
                        <div class="rm-kpi-val">11,034 <span class="rm-kpi-unit">SGM</span></div>
                        <div class="rm-kpi-sub">{{ t('plugin.rolling-memory.atmLeaderboard.kpi1Sub') }}</div>
                    </div>
                </el-col>
                <el-col :xs="24" :sm="12" :xl="6" class="rm-col-stretch">
                    <div class="rm-kpi-box is-highlight">
                        <div class="rm-kpi-label">
                            {{ t('plugin.rolling-memory.atmLeaderboard.kpi2Label') }}
                            <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                <template #reference>
                                    <button
                                        type="button"
                                        class="tip-q"
                                        :aria-label="isEn ? 'ATM-Bench-Hard reading semantics' : 'ATM-Bench-Hard 最优读数说明'"
                                    >
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">
                                        Our best ATM-Bench-Hard reading across delivered read tiers (deterministic retrieval + official hybrid QS).
                                    </p>
                                    <p v-else>我方在 ATM-Bench-Hard 31 题上已交付读档的最优读数（确定性检索 + 官方混合 QS）。</p>
                                </div>
                            </el-popover>
                        </div>
                        <div class="rm-kpi-val is-green">{{ hardBest ? hardBest.qs_score.toFixed(2) + '%' : '—' }}</div>
                        <div class="rm-kpi-sub">
                            {{
                                hardBest
                                    ? `R@10 ${hardBest.recall_at_10.toFixed(2)}% · ${loc(hardBest.system, hardBest.system_en)}`
                                    : t('plugin.rolling-memory.atmLeaderboard.kpi2SubFallback')
                            }}
                        </div>
                    </div>
                </el-col>
                <el-col :xs="24" :sm="12" :xl="6" class="rm-col-stretch">
                    <div class="rm-kpi-box is-highlight">
                        <div class="rm-kpi-label">
                            {{ t('plugin.rolling-memory.atmLeaderboard.kpi3Label') }}
                            <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                <template #reference>
                                    <button type="button" class="tip-q" :aria-label="isEn ? 'Main-board reading semantics' : '主榜最优读数说明'">
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">
                                        Our best Main-board reading (official 1,013-question full set) by deterministic evidence recall R@10.
                                    </p>
                                    <p v-else>我方主榜（官方 1,013 题全量）按确定性证据召回 R@10 的最优读数。</p>
                                </div>
                            </el-popover>
                        </div>
                        <div class="rm-kpi-val is-green">{{ mainBest ? mainBest.recall_at_10.toFixed(2) + '%' : '—' }}</div>
                        <div class="rm-kpi-sub">
                            {{
                                mainBest
                                    ? `QS ${mainBest.qs_score.toFixed(2)}% · ${loc(mainBest.system, mainBest.system_en)}`
                                    : t('plugin.rolling-memory.atmLeaderboard.kpi3SubFallback')
                            }}
                        </div>
                    </div>
                </el-col>
                <el-col :xs="24" :sm="12" :xl="6" class="rm-col-stretch">
                    <div class="rm-kpi-box">
                        <div class="rm-kpi-label">
                            {{ t('plugin.rolling-memory.atmLeaderboard.kpi4Label') }}
                            <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                                <template #reference>
                                    <button
                                        type="button"
                                        class="tip-q"
                                        :aria-label="isEn ? 'Protocol ID and Oracle ceiling semantics' : '协议标识与 Oracle 理论上限说明'"
                                    >
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">
                                        Official protocol ID and cryptographic pin status; the gold-evidence ceiling row shows the theoretical upper
                                        bound of this board.
                                    </p>
                                    <p v-else>官方协议标识与密码学钉状态；金标证据直投行给出本榜的理论上限。</p>
                                </div>
                            </el-popover>
                        </div>
                        <div class="rm-kpi-val">{{ atmSpec.protocol_id || 'atmbench-official-v1' }}</div>
                        <div class="rm-kpi-sub">
                            {{
                                hardOracle
                                    ? t('plugin.rolling-memory.atmLeaderboard.kpi4Sub', { qs: hardOracle.qs_score.toFixed(2) })
                                    : t('plugin.rolling-memory.atmLeaderboard.kpi4SubFallback')
                            }}
                        </div>
                    </div>
                </el-col>
            </el-row>

            <!-- 读档梯度实测台账（仅当存在带读档声明的行时展示）+ 参考/端到端基线单列 -->
            <div v-if="atmBaselines.length" class="rm-proto-section">
                <template v-if="tierLadder.length">
                <div class="rm-proto-sec-title">
                    <strong>{{ t('plugin.rolling-memory.atmLeaderboard.ladderTitle') }}</strong>
                    <span class="rm-muted">{{ t('plugin.rolling-memory.atmLeaderboard.ladderHint') }}</span>
                </div>
                <el-table
                    :data="tierLadder"
                    size="small"
                    border
                    stripe
                    header-cell-class-name="table-header-cell"
                    class="ag-data-table rm-lb-table rm-ladder-table"
                >
                    <el-table-column :label="isEn ? 'Read Tier' : '读档'" width="88" align="center">
                        <template #default="{ row }">
                            <el-tag
                                size="small"
                                :effect="row.profile_tier === 'x' ? 'dark' : 'light'"
                                :type="
                                    row.profile_tier === 'x'
                                        ? 'danger'
                                        : row.profile_tier === 'max'
                                          ? 'warning'
                                          : row.profile_tier === 'pro'
                                            ? 'primary'
                                            : 'success'
                                "
                            >
                                {{ (row.profile_tier || '-').toUpperCase() }}
                            </el-tag>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Question Set' : '题集'" width="110" align="center">
                        <template #default="{ row }">
                            <span class="rm-num-cell">{{ tierLabel(row.tier) }}</span>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Official R@10 ⬆' : '官方 R@10（金标交均）⬆'" width="128" align="right">
                        <template #default="{ row }">
                            <template v-if="(row.r10 || 0) > 0">
                                <strong class="rm-num-cell" style="color: var(--el-color-primary)">{{ (row.r10 * 100).toFixed(2) }}%</strong>
                                <div class="rm-sub-text">Any {{ ((row.any_hit_10 || 0) * 100).toFixed(1) }}% · All {{ ((row.all_hit_10 || 0) * 100).toFixed(1) }}%</div>
                            </template>
                            <template v-else>
                                <span class="rm-num-cell">{{ (row.evidence_hit * 100).toFixed(2) }}%</span>
                                <div class="rm-sub-text">{{ isEn ? 'legacy Hit@10' : '旧口径 Hit@10' }}</div>
                            </template>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'R@20 / R@50 ⬆' : 'R@20 / R@50 ⬆'" width="118" align="right">
                        <template #default="{ row }">
                            <span v-if="(row.r20 || 0) > 0" class="rm-num-cell">{{ (row.r20 * 100).toFixed(1) }}% / {{ (row.r50 * 100).toFixed(1) }}%</span>
                            <span v-else-if="row.full_evidence_hit" class="rm-num-cell">{{ (row.full_evidence_hit * 100).toFixed(2) }}%</span>
                            <span v-else class="rm-muted">—</span>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Hybrid QS ⬆' : '融合 QS ⬆'" width="128" align="right">
                        <template #default="{ row }">
                            <strong class="rm-overall-num" :class="{ 'is-ours-num': row.judge_scored > 0 }">
                                {{ row.judge_scored > 0 ? (row.judge_rate * 100).toFixed(2) + '%' : '—' }}
                            </strong>
                            <div v-if="row.judge_scored > 0" class="rm-sub-text">
                                {{ t('plugin.rolling-memory.atmLeaderboard.judgeCorrect', { correct: row.judge_correct, scored: row.judge_scored }) }}
                            </div>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Elapsed / Ingest' : '耗时 / 导入'" width="104" align="right">
                        <template #default="{ row }">
                            <span class="rm-num-cell">{{ formatElapsed(row.elapsed_seconds) }}</span>
                            <div v-if="(row.ingest_ms || 0) > 0" class="rm-sub-text">{{ isEn ? 'ingest' : '导入' }} {{ row.ingest_ms >= 1000 ? (row.ingest_ms / 1000).toFixed(1) + 's' : row.ingest_ms + 'ms' }}</div>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Failure Buckets' : '错题五桶归因'" min-width="190">
                        <template #default="{ row }">
                            <div class="rm-bucket-cell">
                                <el-tag
                                    v-for="(cnt, name) in row.bucket_counts || {}"
                                    :key="name"
                                    size="small"
                                    :type="name === 'HIT' ? 'success' : 'info'"
                                    effect="plain"
                                >
                                    {{ name }} {{ cnt }}
                                </el-tag>
                                <span v-if="!row.bucket_counts || !Object.keys(row.bucket_counts).length" class="rm-muted">—</span>
                            </div>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Reproduce CLI' : '等价复现命令'" width="108" align="center">
                        <template #default="{ row }">
                            <el-button
                                v-if="row.repro_cli"
                                v-blur
                                size="small"
                                type="primary"
                                link
                                @click="copyText(row.repro_cli, 'Copied tier repro CLI', '已复制该读档复现命令')"
                            >
                                {{ t('plugin.rolling-memory.atmLeaderboard.copyBtn') }}
                            </el-button>
                            <span v-else class="rm-muted">—</span>
                        </template>
                    </el-table-column>
                </el-table>
                </template>

                <!-- 无读档声明的行（对外参考基线 / 端到端基线）单列一张小表，避免与读档梯度混排 -->
                <div v-if="refBaselines.length" class="rm-ladder-refs">
                    <div class="rm-ref-title">{{ t('plugin.rolling-memory.atmLeaderboard.refTitle') }}</div>
                    <el-table
                        :data="refBaselines"
                        size="small"
                        border
                        stripe
                        header-cell-class-name="table-header-cell"
                        class="ag-data-table rm-ref-table"
                    >
                        <el-table-column :label="isEn ? 'Reference / E2E Baseline' : '参考 / 端到端基线'" min-width="260">
                            <template #default="{ row }">
                                <div class="rm-cell-multiline">
                                    <strong class="rm-sys-primary">{{ loc(row.label, row.label_en) }}</strong>
                                    <span class="rm-sub-text">{{ row.arm }}</span>
                                </div>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'Question Set' : '题集'" width="120" align="center">
                            <template #default="{ row }">
                                <span class="rm-num-cell">{{ tierLabel(row.tier) }}</span>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'Official R@10 ⬆' : '官方 R@10 ⬆'" width="118" align="right">
                            <template #default="{ row }">
                                <template v-if="(row.r10 || 0) > 0">
                                    <strong class="rm-num-cell" style="color: var(--el-color-primary)">{{ (row.r10 * 100).toFixed(2) }}%</strong>
                                    <div class="rm-sub-text">Any {{ ((row.any_hit_10 || 0) * 100).toFixed(1) }}%</div>
                                </template>
                                <template v-else>
                                    <span class="rm-num-cell">{{ (row.evidence_hit * 100).toFixed(2) }}%</span>
                                    <div class="rm-sub-text">{{ isEn ? 'legacy Hit@10' : '旧口径 Hit@10' }}</div>
                                </template>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'R@20 / R@50 ⬆' : 'R@20 / R@50 ⬆'" width="118" align="right">
                            <template #default="{ row }">
                                <span v-if="(row.r20 || 0) > 0 || (row.r50 || 0) > 0" class="rm-num-cell">{{ ((row.r20 || 0) * 100).toFixed(1) }}% / {{ ((row.r50 || 0) * 100).toFixed(1) }}%</span>
                                <span v-else-if="row.full_evidence_hit" class="rm-num-cell">{{ (row.full_evidence_hit * 100).toFixed(2) }}%</span>
                                <span v-else class="rm-muted">—</span>
                                <div v-if="(row.all_hit_10 || 0) > 0" class="rm-sub-text">All@10 {{ ((row.all_hit_10 || 0) * 100).toFixed(1) }}%</div>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'Recall / Ingest' : '检索 / 导入耗时'" width="118" align="right">
                            <template #default="{ row }">
                                <span v-if="(row.avg_recall_ms || 0) > 0" class="rm-num-cell">{{ row.avg_recall_ms.toFixed(1) }}ms</span>
                                <span v-else class="rm-num-cell">{{ formatElapsed(row.elapsed_seconds) }}</span>
                                <div v-if="(row.ingest_ms || 0) > 0" class="rm-sub-text">{{ isEn ? 'Ingest' : '导入' }} {{ (row.ingest_ms / 1000).toFixed(2) }}s</div>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'Hybrid QS ⬆' : '融合 QS ⬆'" width="120" align="right">
                            <template #default="{ row }">
                                <strong class="rm-overall-num" :class="{ 'is-ours-num': row.judge_scored > 0 }">
                                    {{ row.judge_scored > 0 ? (row.judge_rate * 100).toFixed(2) + '%' : '—' }}
                                </strong>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'Notes' : '口径说明'" min-width="260">
                            <template #default="{ row }">
                                <div class="rm-note-cell">{{ loc(row.note, row.note_en) }}</div>
                            </template>
                        </el-table-column>
                    </el-table>
                </div>

                <div class="rm-ladder-notes">
                    <div v-for="row in tierLadder" :key="row.id" class="rm-ladder-note">
                        <strong>{{ loc(row.label, row.label_en) }}</strong>
                        <div class="rm-note-cell">{{ loc(row.note, row.note_en) }}</div>
                    </div>
                </div>
            </div>

            <!-- 榜单主体：Tab 1 主榜 / Tab 2 Hard 高难榜 -->
            <div v-if="board.hard_entries?.length || board.main_entries?.length" class="rm-proto-section">
                <div class="rm-filter-bar">
                    <div class="rm-filter-left">
                        <strong>
                            {{
                                isEn
                                    ? 'University of Cambridge ATM-Bench — Multimodal Personal Memory Leaderboard (Primary Benchmark)'
                                    : '剑桥大学 ATM-Bench — 多模态个人指称记忆基准榜单（主基准 · 11,034 条 SGM 记忆）'
                            }}
                        </strong>
                        <el-tag size="small" type="success" style="margin-left: 8px">
                            {{ isEn ? '11,034 SGM Multimodal Items' : '11,034 条 SGM 多模态记忆' }}
                        </el-tag>
                        <el-tag size="small" type="primary" effect="plain" style="margin-left: 6px"> atmbench-official-v1 (QS) </el-tag>
                    </div>
                    <div class="rm-filter-right">
                        <el-radio-group v-model="boardTab" size="small" @change="onBoardTabChange">
                            <el-radio-button value="main">
                                {{ isEn ? 'Tab 1: ATM-Bench Main (1,013 Qs)' : 'Tab 1：ATM-Bench 主榜 (1,013 题全量)' }}
                            </el-radio-button>
                            <el-radio-button value="hard">
                                {{
                                    isEn
                                        ? 'Tab 2: ATM-Bench-Hard (31 Multi-Hop / Abstention Qs)'
                                        : 'Tab 2：ATM-Bench-Hard 高难子集 (31 道跨年多跳/弃权题)'
                                }}
                            </el-radio-button>
                        </el-radio-group>
                        <el-radio-group v-model="atmCategoryFilter" size="small">
                            <el-radio-button value="all">{{ isEn ? 'All' : '全部' }}</el-radio-button>
                            <el-radio-button value="Memory">Memory</el-radio-button>
                            <el-radio-button value="RAG">RAG</el-radio-button>
                            <el-radio-button v-if="boardTab === 'hard'" value="Oracle">Oracle</el-radio-button>
                            <el-radio-button value="ours">{{ isEn ? 'Ours' : '我方系统' }}</el-radio-button>
                        </el-radio-group>
                        <el-select v-model="atmSortKey" size="small" style="width: 185px">
                            <el-option :label="isEn ? 'Sort: R@10 (DESC)' : '排序：R@10 证据召回'" value="recall_at_10" />
                            <el-option :label="isEn ? 'Sort: QS Score (DESC)' : '排序：QS 综合分'" value="qs_score" />
                            <el-option :label="isEn ? 'Sort: R@25 (DESC)' : '排序：R@25 召回'" value="recall_at_25" />
                            <el-option :label="isEn ? 'Sort: R@50 (DESC)' : '排序：R@50 召回'" value="recall_at_50" />
                            <el-option :label="isEn ? 'Sort: List Recall (DESC)' : '排序：List 列表枚举'" value="list_recall" />
                            <el-option :label="isEn ? 'Sort: Number EM (DESC)' : '排序：Number 数值日期'" value="number_em" />
                            <el-option :label="isEn ? 'Sort: Open-End (DESC)' : '排序：Open 开放问答'" value="open_end" />
                        </el-select>
                    </div>
                </div>

                <el-row :gutter="12" class="rm-charts-row" style="margin-bottom: 12px">
                    <el-col :xs="24" :xl="13" class="rm-col-stretch">
                        <div class="rm-chart-box">
                            <div class="rm-chart-hdr">
                                <span class="rm-chart-title-txt">
                                    {{
                                        boardTab === 'main'
                                            ? isEn
                                                ? 'ATM-Bench Main (1,013 Qs) Score Ladder (Click any system to inspect breakdown)'
                                                : 'ATM-Bench 主榜（1,013 题）天梯对比（点击任意系统联动右侧分题型剖析）'
                                            : isEn
                                              ? 'ATM-Bench-Hard (31 Qs) Score Ladder (Click any system to inspect breakdown)'
                                              : 'ATM-Bench-Hard（31 题）天梯对比（点击任意系统联动右侧分题型剖析）'
                                    }}
                                </span>
                                <el-radio-group v-model="atmLadderMetric" size="small">
                                    <el-radio-button value="recall_at_10">R@10</el-radio-button>
                                    <el-radio-button value="qs_score">Hybrid QS</el-radio-button>
                                </el-radio-group>
                            </div>
                            <div class="rm-ladder-list">
                                <div
                                    v-for="(item, idx) in activeRows"
                                    :key="`${item.board}-${item.system}-${idx}`"
                                    class="rm-ladder-row"
                                    :class="{
                                        'is-active': activeAtmEntry?.system === item.system,
                                        'is-ours-row': item.is_ours,
                                    }"
                                    @click="selectedAtmEntry = item"
                                >
                                    <div class="rm-ladder-top">
                                        <div class="rm-ladder-meta">
                                            <span class="rm-rank-badge" :class="rankBadgeClass(displayRankOf(idx))">
                                                {{ item.rank >= 90 ? '—' : `#${displayRankOf(idx)}` }}
                                            </span>
                                            <span class="rm-sys-name">{{ loc(item.system, item.system_en) }}</span>
                                            <el-tag size="small" :type="item.is_ours ? 'success' : item.category === 'Oracle' ? 'warning' : 'info'" effect="plain">
                                                {{ item.is_ours ? (isEn ? 'Ours' : '我方系统') : item.category }}
                                            </el-tag>
                                        </div>
                                        <div class="rm-ladder-score" :class="{ 'is-ours-score': item.is_ours }">
                                            {{ Number(item[atmLadderMetric] || 0).toFixed(2) }}%
                                        </div>
                                    </div>
                                    <div class="rm-ladder-bar-wrap">
                                        <div
                                            class="rm-ladder-bar"
                                            :class="item.is_ours ? 'is-ours' : item.category === 'Oracle' ? 'is-oracle' : 'is-pub'"
                                            :style="{ width: `${Math.max(3, Math.min(100, Number(item[atmLadderMetric] || 0)))}%` }"
                                        ></div>
                                    </div>
                                </div>
                            </div>
                        </div>
                    </el-col>
                    <el-col :xs="24" :xl="11" class="rm-col-stretch">
                        <div class="rm-chart-box">
                            <div class="rm-chart-hdr">
                                <span class="rm-chart-title-txt">
                                    {{ isEn ? '3-Category & Recall Depth Breakdown' : '三类题型与检索深度剖析（List / Number / Open & R@10–50）' }}
                                </span>
                                <el-tag v-if="activeAtmEntry" size="small" :type="activeAtmEntry.is_ours ? 'success' : 'primary'">
                                    R@10 {{ activeAtmEntry.recall_at_10.toFixed(2) }}% · QS {{ activeAtmEntry.qs_score.toFixed(2) }}%
                                </el-tag>
                            </div>
                            <div v-if="activeAtmEntry" class="rm-cat-breakdown">
                                <div class="rm-active-meta">
                                    <div class="rm-active-sys-line">
                                        <strong class="rm-active-sys-name">{{ loc(activeAtmEntry.system, activeAtmEntry.system_en) }}</strong>
                                        <span class="rm-muted">{{ loc(activeAtmEntry.vendor, activeAtmEntry.vendor_en) }}</span>
                                    </div>
                                    <div class="rm-active-arch">{{ loc(activeAtmEntry.architecture, activeAtmEntry.architecture_en) }}</div>
                                    <div class="rm-active-tags">
                                        <el-tag size="small" type="info" effect="plain">{{ isEn ? 'Index time' : '建库耗时' }}: {{ activeAtmEntry.index_time_hr }}</el-tag>
                                        <el-tag size="small" type="primary" effect="plain">R@25 {{ activeAtmEntry.recall_at_25.toFixed(2) }}% · R@50 {{ activeAtmEntry.recall_at_50.toFixed(2) }}%</el-tag>
                                    </div>
                                </div>
                                <div class="rm-cat-list">
                                    <div v-for="bar in activeAtmBars" :key="bar.key" class="rm-cat-item">
                                        <div class="rm-cat-top">
                                            <span class="rm-cat-name">{{ bar.label }}</span>
                                            <strong class="rm-cat-val-txt">{{ bar.val.toFixed(2) }}%</strong>
                                        </div>
                                        <div class="rm-cat-track">
                                            <div
                                                class="rm-cat-fill"
                                                :class="activeAtmEntry.is_ours ? 'is-ours' : 'is-pub'"
                                                :style="{ width: `${Math.max(2, Math.min(100, bar.val))}%` }"
                                            ></div>
                                        </div>
                                    </div>
                                </div>
                                <div class="rm-cat-note">
                                    <strong>{{ isEn ? 'Note' : '说明' }}：</strong>
                                    <span>{{ loc(activeAtmEntry.note, activeAtmEntry.note_en) }}</span>
                                </div>
                            </div>
                        </div>
                    </el-col>
                </el-row>

                <div class="rm-parity-banner" style="margin-top: 8px; margin-bottom: 12px">
                    <div class="rm-parity-banner-title">
                        <span class="rm-parity-pill">
                            {{ boardTab === 'hard' ? 'CAMBRIDGE ATM-BENCH-HARD (31 Qs)' : 'CAMBRIDGE ATM-BENCH MAIN (1,013 Qs)' }}
                        </span>
                        <strong class="rm-parity-headline">
                            {{ loc(board.corpus_summary, board.corpus_summary_en) }}
                        </strong>
                    </div>
                    <div class="rm-parity-banner-desc">
                        <code class="rm-code-inline">{{
                            boardTab === 'hard'
                                ? 'rmem eval --suite atm --tier hard --pulse-mode local_plain --top-k 50 --answerer none --judge off'
                                : 'rmem eval --suite atm --tier 1013 --pulse-mode local_plain --top-k 50 --answerer none --judge off'
                        }}</code>
                        <el-button
                            v-blur
                            size="small"
                            type="primary"
                            plain
                            @click="
                                copyText(
                                    boardTab === 'hard'
                                        ? 'rmem eval --suite atm --tier hard --pulse-mode local_plain --top-k 50 --answerer none --judge off'
                                        : 'rmem eval --suite atm --tier 1013 --pulse-mode local_plain --top-k 50 --answerer none --judge off',
                                    'Copied ATM repro CLI',
                                    '已复制 ATM-Bench 复现命令'
                                )
                            "
                        >
                            {{ t('plugin.rolling-memory.atmLeaderboard.copyReproBtn') }}
                        </el-button>
                        <el-button size="small" type="success" plain @click="goEvalLab(boardTab === 'main' ? '1013' : 'hard')">
                            {{ isEn ? 'Run this split in Evaluation Lab →' : '在评测实验室运行该分集 →' }}
                        </el-button>
                    </div>
                </div>

                <el-table
                    v-loading="loading"
                    :element-loading-text="loadBoardText"
                    :data="activeRows"
                    size="small"
                    border
                    stripe
                    highlight-current-row
                    header-cell-class-name="table-header-cell"
                    class="ag-data-table rm-lb-table rm-board-table"
                    :row-class-name="atmRowClassName"
                    @row-click="(row: ATMBenchEntry) => (selectedAtmEntry = row)"
                >
                    <el-table-column :label="isEn ? 'Rank' : '排名'" width="50" align="center">
                        <template #default="{ row, $index }">
                            <span class="rm-rank-badge" :class="rankBadgeClass(displayRankOf($index))">{{
                                row.rank >= 90 ? '—' : '#' + displayRankOf($index)
                            }}</span>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Category' : '类别'" width="72" align="center">
                        <template #default="{ row }">
                            <el-tag size="small" :type="row.category === 'Memory' ? 'success' : row.category === 'Oracle' ? 'warning' : 'info'">
                                {{ row.category }}
                            </el-tag>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Memory / Retrieval System' : '参评记忆 / 检索系统'" min-width="175">
                        <template #default="{ row }">
                            <div class="rm-cell-multiline">
                                <div class="rm-sys-title-line">
                                    <strong class="rm-sys-primary">{{ loc(row.system, row.system_en) }}</strong>
                                </div>
                                <div class="rm-sub-text">{{ loc(row.architecture, row.architecture_en) }}</div>
                                <div class="rm-sys-tags-line">
                                    <el-tag
                                        v-if="loc(row.highlight_tag, row.highlight_tag_en)"
                                        size="small"
                                        :type="row.is_ours ? 'success' : 'warning'"
                                        effect="plain"
                                    >
                                        {{ loc(row.highlight_tag, row.highlight_tag_en) }}
                                    </el-tag>
                                </div>
                            </div>
                        </template>
                    </el-table-column>
                    <el-table-column label="R@10 ⬆" width="76" align="right">
                        <template #default="{ row }">
                            <span class="rm-num-cell">{{ row.recall_at_10.toFixed(2) }}%</span>
                        </template>
                    </el-table-column>
                    <el-table-column label="R@25 ⬆" width="76" align="right">
                        <template #default="{ row }">
                            <span class="rm-num-cell">{{ row.recall_at_25.toFixed(2) }}%</span>
                        </template>
                    </el-table-column>
                    <el-table-column label="R@50 ⬆" width="76" align="right">
                        <template #default="{ row }">
                            <span class="rm-num-cell">{{ row.recall_at_50.toFixed(2) }}%</span>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'QS Score ⬆' : 'QS 综合分 ⬆'" width="92" align="right">
                        <template #default="{ row }">
                            <strong class="rm-overall-num" :class="{ 'is-ours-num': row.is_ours }"> {{ row.qs_score.toFixed(2) }}% </strong>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'List / Num / Open' : '分题型 (List/Num/Open)'" width="138" align="right">
                        <template #default="{ row }">
                            <span class="rm-num-cell">
                                {{ row.list_recall.toFixed(1) }}% / {{ row.number_em.toFixed(1) }}% / {{ row.open_end.toFixed(1) }}%
                            </span>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Index Time ⬇' : '建库耗时 ⬇'" width="102" align="right">
                        <template #default="{ row }">
                            <strong :style="{ color: row.is_ours ? '#10b981' : 'inherit' }">{{ row.index_time_hr }}</strong>
                        </template>
                    </el-table-column>
                    <el-table-column :label="isEn ? 'Notes & Verification Details' : '实测说明与核验详情'" min-width="180">
                        <template #default="{ row }">
                            <div class="rm-note-cell">{{ loc(row.note, row.note_en) }}</div>
                        </template>
                    </el-table-column>
                </el-table>
            </div>

            <!-- 底部：atmbench-official-v1 协议栈与官方智能体逐项核对清单 -->
            <div class="rm-proto-section">
                <div class="rm-proto-sec-title">
                    <strong>{{ t('plugin.rolling-memory.atmLeaderboard.protoDetailTitle') }}</strong>
                </div>
                <el-descriptions :column="2" border size="small" class="rm-proto-desc">
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblBuiltinProto')">
                        <div class="rm-desc-val">
                            <el-tag size="small" type="primary">{{ atmSpec.builtin_protocol || 'atmbench-official' }}</el-tag>
                            <code class="rm-code-inline">{{ atmSpec.protocol_id || 'atmbench-official-v1' }}</code>
                        </div>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblTargetBoard')">
                        <strong class="rm-desc-text">
                            {{ loc(atmSpec.leaderboard_name, atmSpec.leaderboard_name_en) || 'University of Cambridge ATM-Bench Leaderboard' }}
                        </strong>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblCorpusSpec')" :span="2">
                        <span class="rm-desc-text">{{ loc(atmSpec.corpus_name, atmSpec.corpus_name_en) }}</span>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblAnswerSpec')" :span="2">
                        <span class="rm-desc-text">{{ loc(atmSpec.answer_payload_spec, atmSpec.answer_payload_spec_en) }}</span>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblJudgeSpec')" :span="2">
                        <span class="rm-desc-text">{{ loc(atmSpec.judge_verdict_spec, atmSpec.judge_verdict_spec_en) }}</span>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblCatMap')" :span="2">
                        <span class="rm-desc-text">{{ loc(atmSpec.category_mapping_note, atmSpec.category_mapping_note_en) }}</span>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblFairNotice')" :span="2">
                        <span class="rm-desc-text">{{ loc(atmSpec.fair_comparison_notice, atmSpec.fair_comparison_notice_en) }}</span>
                    </el-descriptions-item>
                </el-descriptions>

                <div class="rm-proto-sec-title" style="margin-top: 16px">
                    <strong>{{ isEn ? 'Official scoring protocol & SHA-256 pins' : '官方判分协议与 SHA-256 钉' }}</strong>
                </div>
                <el-descriptions :column="2" border size="small" class="rm-proto-desc">
                    <el-descriptions-item :label="isEn ? 'Number scorer' : 'Number 判分'">
                        <span class="rm-desc-text">{{ atmSpec.number_scorer || 'official deterministic_accuracy' }}</span>
                        <br />
                        <code class="rm-code-inline">{{ atmSpec.number_scorer_version || '-' }}</code>
                    </el-descriptions-item>
                    <el-descriptions-item :label="isEn ? 'List scorer' : 'List 判分'">
                        <span class="rm-desc-text">{{ atmSpec.list_scorer || 'official list_jaccard_score' }}</span>
                        <br />
                        <code class="rm-code-inline">{{ atmSpec.list_scorer_version || '-' }}</code>
                    </el-descriptions-item>
                    <el-descriptions-item :label="isEn ? 'Judge parser' : '判官解析'" :span="2">
                        <span class="rm-desc-text">{{ atmSpec.judge_parser || 'official parse_judge_response' }}</span>
                    </el-descriptions-item>
                    <el-descriptions-item :label="isEn ? 'Scoring fixture SHA-256' : '判分 fixture SHA-256'" :span="2">
                        <code class="rm-sha-code">{{ atmSpec.scoring_fixture_sha256 || '-' }}</code>
                    </el-descriptions-item>
                    <el-descriptions-item :label="isEn ? 'Answer user prompt SHA-1' : '答题 user 模板 SHA-1'" :span="2">
                        <code class="rm-sha-code">{{ atmSpec.answer_payload_sha1 || '-' }}</code>
                    </el-descriptions-item>
                    <el-descriptions-item :label="isEn ? 'Official alignment' : '官方对齐'">
                        <el-tag size="small" :type="atmSpec.official_scoring_aligned ? 'success' : 'danger'">
                            {{ atmSpec.official_scoring_aligned ? (isEn ? 'ALIGNED' : '已对齐') : (isEn ? 'DRIFT' : '未对齐') }}
                        </el-tag>
                        <el-tag size="small" style="margin-left: 8px" :type="atmSpec.all_pins_verified ? 'success' : 'danger'">
                            {{ atmSpec.all_pins_verified ? (isEn ? 'PIN GATE PASS' : '钉门禁通过') : (isEn ? 'PIN GATE FAIL' : '钉门禁失败') }}
                        </el-tag>
                    </el-descriptions-item>
                    <el-descriptions-item :label="isEn ? 'Recall metric' : '召回口径'">
                        <span class="rm-desc-text">{{ loc(atmSpec.recall_metric_spec, atmSpec.recall_metric_spec_en) }}</span>
                    </el-descriptions-item>
                    <el-descriptions-item :label="isEn ? 'Corpus / question-tier pins' : '语料 / 题集钉'" :span="2">
                        <div v-for="(value, name) in (atmSpec.corpus_pins || {})" :key="'atm-corpus-pin-' + name" class="rm-pin-line">
                            <code class="rm-code-inline">{{ name }}</code>
                            <code class="rm-sha-code">{{ value }}</code>
                        </div>
                    </el-descriptions-item>
                    <el-descriptions-item :label="isEn ? 'Upstream evaluator source pins' : '官方评测源码钉'" :span="2">
                        <div v-for="(value, name) in (atmSpec.evaluator_source_pins || {})" :key="'atm-source-pin-' + name" class="rm-pin-line">
                            <code class="rm-code-inline">{{ name }}</code>
                            <code class="rm-sha-code">{{ value }}</code>
                        </div>
                    </el-descriptions-item>
                </el-descriptions>

                <el-row :gutter="14" class="rm-agent-row">
                    <el-col v-for="ag in atmAgents" :key="ag.code" :xs="24" :xl="12" class="rm-col-stretch">
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
                                    <code class="rm-code-inline">atmbench-official-v1 ({{ ag.protocol }})</code>
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
import { ElMessage } from 'element-plus'
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

interface ProtocolParitySpec {
    builtin_protocol?: string
    protocol_id?: string
    leaderboard_name?: string
    leaderboard_name_en?: string
    corpus_name?: string
    corpus_name_en?: string
    answer_payload_spec?: string
    answer_payload_spec_en?: string
    answer_payload_sha1?: string
    judge_verdict_spec?: string
    judge_verdict_spec_en?: string
    category_mapping_note?: string
    category_mapping_note_en?: string
    fair_comparison_notice?: string
    fair_comparison_notice_en?: string
    number_scorer?: string
    number_scorer_version?: string
    list_scorer?: string
    list_scorer_version?: string
    judge_parser?: string
    scoring_fixture_sha256?: string
    evaluator_source_pins?: Record<string, string>
    corpus_pins?: Record<string, string>
    official_scoring_aligned?: boolean
    recall_metric_spec?: string
    recall_metric_spec_en?: string
    all_pins_verified?: boolean
}

interface ATMOfficialPinStatus {
    protocol?: string
    version?: string
    prompt_pins?: Record<string, string>
    prompt_pins_verified?: boolean
    fixture_pin?: string
    fixture_verified?: boolean
    source_pins?: Record<string, string>
    corpus_pins?: Record<string, string>
    corpus_pins_verified?: boolean
    corpus_pins_error?: string
}

interface OfficialProtocolAgent {
    code: string
    name: string
    name_en?: string
    protocol: string
    model: string
    system_prompt: string
    system_sha1: string
    user_template: string
    user_template_sha1?: string
    user_template_sha?: string
    pin_verified: boolean
}

interface ATMBenchEntry {
    rank: number
    board: string
    category: string
    system: string
    system_en?: string
    vendor: string
    vendor_en?: string
    architecture: string
    architecture_en?: string
    is_ours: boolean
    recall_at_10: number
    recall_at_25: number
    recall_at_50: number
    qs_score: number
    list_recall: number
    number_em: number
    open_end: number
    index_time_hr: string
    highlight_tag: string
    highlight_tag_en?: string
    note: string
    note_en?: string
}

interface ATMBenchBoard {
    benchmark_name: string
    benchmark_name_en?: string
    corpus_summary: string
    corpus_summary_en?: string
    repro_command: string
    hard_entries: ATMBenchEntry[]
    main_entries: ATMBenchEntry[]
}

interface ATMBaselineRecord {
    id: string
    label: string
    label_en?: string
    tier: string
    scored_questions: number
    arm: string
    protocol: string
    evidence_hit: number
    full_evidence_hit?: number
    // 官方 gold-set-mean 口径（与 eval.go EvalRunRecord 同契约；旧记录缺省 undefined 走 legacy 列展示）
    r10?: number
    r20?: number
    r50?: number
    any_hit_10?: number
    all_hit_10?: number
    ingest_ms?: number
    avg_recall_ms?: number
    p95_recall_ms?: number
    judge_rate: number
    judge_correct: number
    judge_scored: number
    elapsed_seconds: number
    note: string
    note_en?: string
    repro_cli?: string
    profile_tier?: string
    bucket_counts?: Record<string, number>
}

interface ATMLeaderboardResponse {
    atm_bench?: ATMBenchBoard
    atm_protocol_spec?: ProtocolParitySpec
    atm_agents?: OfficialProtocolAgent[]
    atm_baselines?: ATMBaselineRecord[]
    atm_official_pins?: ATMOfficialPinStatus
}

const loading = ref(false)
const { percent: loadBoardPct, text: loadBoardText } = useLoadingProgressText(loading)
const board = ref<Partial<ATMBenchBoard>>({})
const atmSpec = ref<Partial<ProtocolParitySpec>>({})
const atmAgents = ref<OfficialProtocolAgent[]>([])
const atmBaselines = ref<ATMBaselineRecord[]>([])
const boardTab = ref<'hard' | 'main'>('main')
const query = ref<{ tier: 'hard' | '1013' }>({ tier: '1013' })
const atmCategoryFilter = ref<'all' | 'Memory' | 'RAG' | 'Oracle' | 'ours'>('all')
const atmSortKey = ref<'recall_at_10' | 'qs_score' | 'recall_at_25' | 'recall_at_50' | 'list_recall' | 'number_em' | 'open_end'>('recall_at_10')
const atmLadderMetric = ref<'recall_at_10' | 'qs_score'>('recall_at_10')
const selectedAtmEntry = ref<ATMBenchEntry | null>(null)

const pinsVerified = computed(() => atmSpec.value.all_pins_verified !== false)

// tierLadder / refBaselines 台账分流：声明了读档字面量（profile_tier，legacy 线格式兼容值）的行进读档梯度表；
// 无读档声明的行（对外参考基线 HippoRAG 2 / Mem0 与不带档声明的端到端基线）单列参考表，避免混排误读为同尺。
const tierLadder = computed(() => atmBaselines.value.filter((row) => !!row.profile_tier))
const refBaselines = computed(() => atmBaselines.value.filter((row) => !row.profile_tier))

const rawBoardRows = computed<ATMBenchEntry[]>(() => {
    if (boardTab.value === 'main') {
        return board.value.main_entries || []
    }
    return board.value.hard_entries || []
})

const activeRows = computed<ATMBenchEntry[]>(() => {
    const filtered = rawBoardRows.value.filter((row) => {
        if (atmCategoryFilter.value === 'all') return true
        if (atmCategoryFilter.value === 'ours') return row.is_ours
        return row.category === atmCategoryFilter.value
    })
    const key = atmSortKey.value
    return [...filtered].sort((a, b) => Number(b[key] || 0) - Number(a[key] || 0))
})

const activeAtmEntry = computed<ATMBenchEntry | null>(() => {
    if (selectedAtmEntry.value && rawBoardRows.value.some((r) => r.system === selectedAtmEntry.value?.system)) {
        return selectedAtmEntry.value
    }
    const ours = activeRows.value.find((r) => r.is_ours)
    return ours || activeRows.value[0] || null
})

const activeAtmBars = computed(() => {
    const cur = activeAtmEntry.value
    return [
        { key: 'r10', label: isEn.value ? 'Official Recall@10 (GT Coverage Mean)' : '官方 Recall@10（金标覆盖率均值）', val: cur?.recall_at_10 ?? 0 },
        { key: 'qs', label: isEn.value ? 'Official Hybrid QS (End-to-End QA)' : '官方混合 QS（端到端问答综合分）', val: cur?.qs_score ?? 0 },
        { key: 'list', label: isEn.value ? 'Cat 1 · List Recall (Evidence ID Jaccard)' : 'Cat 1 · 证据 ID 列表枚举 (List Recall)', val: cur?.list_recall ?? 0 },
        { key: 'num', label: isEn.value ? 'Cat 2 · Number / Date (Deterministic EM)' : 'Cat 2 · 数值与日期推理 (Number EM)', val: cur?.number_em ?? 0 },
        { key: 'open', label: isEn.value ? 'Cat 3 · Open-End (LLM Judge)' : 'Cat 3 · 开放问答 (Open-End)', val: cur?.open_end ?? 0 },
    ]
})

const onBoardTabChange = () => {
    query.value.tier = boardTab.value === 'main' ? '1013' : 'hard'
    if (boardTab.value === 'main' && atmCategoryFilter.value === 'Oracle') {
        atmCategoryFilter.value = 'all'
    }
    selectedAtmEntry.value = null
}

// bestOurs 在该榜中取我方（is_ours）按给定指标升序的最优行；无我方行时返回 null。
const bestOurs = (rows: ATMBenchEntry[] | undefined, pick: (row: ATMBenchEntry) => number) => {
    const ours = (rows || []).filter((row) => row.is_ours)
    if (!ours.length) return null
    return ours.reduce((best, row) => (pick(row) > pick(best) ? row : best), ours[0])
}

const hardBest = computed(() => bestOurs(board.value.hard_entries, (row) => row.qs_score))
const mainBest = computed(() => bestOurs(board.value.main_entries, (row) => row.recall_at_10))
const hardOracle = computed(() => (board.value.hard_entries || []).find((row) => row.category === 'Oracle') || null)

const tierLabel = (tier: string) => {
    if (tier === 'hard') return isEn.value ? 'Hard (31 Qs)' : 'Hard 31 题'
    if (tier === '1013') return isEn.value ? 'Main (1,013 Qs)' : '主榜 1,013 题'
    return tier || '—'
}

const formatElapsed = (seconds: number) => {
    const value = Number(seconds) || 0
    return value >= 60 ? `${(value / 60).toFixed(1)} min` : `${value.toFixed(1)} s`
}

const displayRankOf = (idx: number) => {
    const rows = activeRows.value
    const curr = rows[idx]
    if (!curr || curr.rank >= 90) return 0
    let r = 0
    for (let i = 0; i <= idx; i++) {
        if (rows[i] && rows[i].rank < 90) r++
    }
    return r
}

const rankBadgeClass = (rank: number) => {
    if (rank === 1) return 'is-gold'
    if (rank === 2) return 'is-silver'
    if (rank === 3) return 'is-bronze'
    return ''
}

const atmRowClassName = ({ row }: { row: ATMBenchEntry }) => {
    if (row.is_ours) return 'rm-ours-table-row'
    return ''
}

// copyText 复制文本：照同仓样板 eval.vue:3335-3363 的写法（clipboard + ElMessage 回显全文，
// 失败降级用 execCommand 兜底后 ElMessage.info 全文）。
const copyText = async (text: string, enPrefix = 'Copied', zhPrefix = '已复制') => {
    const value = String(text || '').trim()
    if (!value) {
        ElMessage.warning(isEn.value ? 'Nothing to copy' : '暂无可复制内容')
        return
    }
    try {
        await navigator.clipboard.writeText(value)
        ElMessage.success(`${isEn.value ? enPrefix : zhPrefix}: ${value}`)
        return
    } catch {
        const textarea = document.createElement('textarea')
        textarea.value = value
        textarea.style.position = 'fixed'
        textarea.style.opacity = '0'
        document.body.appendChild(textarea)
        textarea.select()
        try {
            document.execCommand('copy')
            ElMessage.success(`${isEn.value ? enPrefix : zhPrefix}: ${value}`)
        } catch {
            ElMessage.info(value)
        } finally {
            document.body.removeChild(textarea)
        }
    }
}

const loadBoard = async () => {
    loading.value = true
    try {
        const res = await request<ATMLeaderboardResponse>({
            url: '/admin/plugin/rolling-memory/eval/atm/leaderboard',
            method: 'POST',
            data: {},
        })
        const data = res.data.data
        if (data) {
            board.value = data.atm_bench || {}
            atmSpec.value = data.atm_protocol_spec || {}
            atmAgents.value = data.atm_agents || []
            atmBaselines.value = data.atm_baselines || []
        }
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        loading.value = false
    }
}

// goEvalLab 跳转评测实验室并带上 ATM 体系与题集参数（eval.vue 读取 route.query.suite/tier 预置）。
const goEvalLab = (tier: 'hard' | '1013') => {
    query.value.tier = tier || 'hard'
    router.push({ path: '/admin/plugin/rolling-memory/eval', query: { suite: 'atm', tier: query.value.tier } })
}

const goLoCoMoBoard = () => {
    router.push({ path: '/admin/plugin/rolling-memory/leaderboard' })
}

onMounted(() => {
    loadBoard()
})
</script>

<style scoped lang="scss">
.plugin-rm-atm-leaderboard {
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
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
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

    .rm-kpi-unit {
        font-size: 12.5px;
        font-weight: 600;
        color: var(--el-text-color-secondary);
    }

    .rm-kpi-sub {
        font-size: 12px;
        line-height: 1.5;
        color: var(--el-text-color-regular);
        word-break: break-word;
    }

    .rm-filter-bar {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        align-items: center;
        justify-content: space-between;
        padding: 13px 15px;
        margin-bottom: 10px;
        font-size: 13.5px;
        background-color: var(--ag-bg-color-overlay);
        border: 1px solid var(--ag-border-color);
        border-radius: 6px;
    }

    .rm-filter-right {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
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

    .rm-bucket-cell {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
        align-items: center;
        padding: 2px 0;
    }

    .rm-ladder-refs {
        margin-top: 14px;
    }

    .rm-ref-title {
        margin-bottom: 8px;
        font-size: 12.5px;
        font-weight: 600;
        color: var(--el-text-color-secondary);
    }

    .rm-ladder-notes {
        display: flex;
        flex-direction: column;
        gap: 8px;
        padding-top: 10px;
    }

    .rm-ladder-note {
        font-size: 12.5px;
        line-height: 1.55;

        strong {
            color: var(--el-text-color-primary);
        }
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

    .rm-proto-section {
        padding-top: 16px;
        margin-top: 18px;
        border-top: 1px solid var(--el-border-color-lighter);
    }

    .rm-proto-sec-title {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: baseline;
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

    .rm-ladder-list {
        display: flex;
        flex-direction: column;
        gap: 7px;
        max-height: 360px;
        overflow: auto;
    }

    .rm-ladder-row {
        display: flex;
        flex-direction: column;
        gap: 5px;
        padding: 7px 10px;
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

        &.is-ours-row {
            border-left: 3px solid var(--el-color-success);
        }
    }

    .rm-ladder-top {
        display: flex;
        gap: 10px;
        align-items: center;
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
        font-size: 12.5px;
        font-weight: 600;
        color: var(--el-text-color-primary);
    }

    .rm-ladder-score {
        flex-shrink: 0;
        font-size: 13.5px;
        font-weight: 700;
        font-variant-numeric: tabular-nums;

        &.is-ours-score {
            color: var(--el-color-success);
        }
    }

    .rm-ladder-bar-wrap {
        height: 7px;
        background: var(--el-fill-color);
        border-radius: 4px;
    }

    .rm-ladder-bar {
        height: 100%;
        border-radius: 4px;

        &.is-ours {
            background: linear-gradient(90deg, #10b981, #059669);
        }

        &.is-oracle {
            background: linear-gradient(90deg, #f59e0b, #d97706);
        }

        &.is-pub {
            background: linear-gradient(90deg, #60a5fa, #2563eb);
        }
    }

    .rm-cat-breakdown {
        display: flex;
        flex: 1;
        flex-direction: column;
        justify-content: space-between;
        gap: 12px;
    }

    .rm-active-meta {
        display: flex;
        flex-direction: column;
        gap: 5px;
        padding: 10px 12px;
        font-size: 12px;
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
        font-size: 13.5px;
        color: var(--el-text-color-primary);
    }

    .rm-active-arch {
        color: var(--el-text-color-regular);
        line-height: 1.5;
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
        gap: 10px;
    }

    .rm-cat-item {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }

    .rm-cat-top {
        display: flex;
        align-items: center;
        justify-content: space-between;
        font-size: 12px;
    }

    .rm-cat-track {
        height: 8px;
        background: var(--el-fill-color);
        border-radius: 4px;
    }

    .rm-cat-fill {
        height: 100%;
        border-radius: 4px;

        &.is-ours {
            background: #10b981;
        }

        &.is-pub {
            background: #3b82f6;
        }
    }

    .rm-cat-note {
        padding-top: 8px;
        font-size: 12px;
        line-height: 1.55;
        color: var(--el-text-color-regular);
        border-top: 1px dashed var(--el-border-color-lighter);
    }

    :deep(.rm-ours-table-row) {
        font-weight: 600;
        background-color: var(--el-color-success-light-9) !important;
    }

    .rm-muted {
        font-size: 12px;
        color: var(--el-text-color-secondary);
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
