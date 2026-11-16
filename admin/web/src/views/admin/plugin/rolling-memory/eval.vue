<!-- plugin: rolling-memory v0.0.58 -->
<!--
    Rolling-Memory 评测实验室：按所选评测臂展示本地明文、脉冲明文或脉冲密文上下文。
    明文金库、租户专属评测 scope、语料摘要与逐题引擎 provenance 均只读回带；
    Oracle 仅提供金标证据上限，不计入引擎成绩。
-->
<template>
    <div class="default-main plugin-rm-eval">
        <el-card shadow="never" class="rm-eval-card">
            <template #header>
                <div class="rm-eval-header">
                    <div class="rm-eval-heading">
                        <div class="rm-eval-title-line">
                            <span class="rm-eval-title">{{ isEn ? 'Evaluation Lab' : '评测实验室' }}</span>
                            <el-tag size="small" type="success" effect="plain">{{ evalArmLabel }}</el-tag>
                            <el-tag size="small" type="info" effect="plain">
                                {{ isEn ? 'Production-Identical Engine · Isolated Scope' : '生产同源引擎 · 隔离评测域' }}
                            </el-tag>
                        </div>
                        <p>{{ isEn ? 'Verify retrieval recall and end-to-end QA accuracy on public benchmarks (Cambridge ATM-Bench primary, LoCoMo comparative reference) using the production local engine.' : '基于公开基准（剑桥 ATM-Bench 主基准，LoCoMo 对照参照）在本机独立核验生产检索与问答质量，运行记录与原始证据保存在本地。' }}</p>
                    </div>
                    <div class="rm-eval-actions">
                        <el-button v-blur size="small" plain @click="query.suite === 'atm' ? goATMLeaderboard() : goLeaderboard()">
                            {{ isEn ? 'View leaderboard' : '查看基准榜单' }}
                        </el-button>
                        <el-button v-blur size="small" plain @click="openAIConfigModal(false)">
                            {{ isEn ? 'Model settings' : '模型设置' }}
                        </el-button>
                        <el-button v-blur size="small" :loading="loading" @click="refreshAll">
                            {{ t('plugin.rolling-memory.refresh') }}
                            <template v-if="loading"> ({{ evalLoadPct }}%)</template>
                        </el-button>
                    </div>
                </div>
            </template>

            <div v-if="loading || ingesting !== ''" class="rm-top-progress-banner">
                <div class="rm-tpb-head">
                    <div class="rm-tpb-title">
                        <span class="rm-live-dot is-running"></span>
                        <strong>{{ ingesting !== '' ? ingestStageTitle : overviewLoadStageTitle }}</strong>
                        <el-tag size="small" type="primary" effect="plain">
                            {{ ingesting !== '' ? `${ingestPct}% · ${ingestElapsed}s` : `${evalLoadPct}%` }}
                        </el-tag>
                    </div>
                    <span class="rm-tpb-sub">{{ ingesting !== '' ? ingestStageDesc : overviewLoadStageDesc }}</span>
                </div>
                <el-progress
                    :percentage="ingesting !== '' ? ingestPct : evalLoadPct"
                    :stroke-width="8"
                    striped
                    striped-flow
                    :show-text="false"
                />
            </div>

            <!-- 三步流程条：新用户落地即知「这页怎么用」（选基准 → 开始 → 看成绩）。
                 三张评测保障说明柱卡降为默认收起的折叠条——纯背书性内容不占首屏首行。 -->
            <div class="rm-howto-strip">
                <div class="rm-howto-step">
                    <span class="rm-howto-idx">1</span>
                    <div class="rm-howto-txt">
                        <strong>{{ isEn ? 'Pick benchmark & arm' : '选基准 · 题集 · 检索臂' }}</strong>
                        <span>{{ isEn ? 'ATM-Bench primary / LoCoMo reference' : 'ATM-Bench 主评 / LoCoMo 参照' }}</span>
                    </div>
                </div>
                <span class="rm-howto-arrow">→</span>
                <div class="rm-howto-step">
                    <span class="rm-howto-idx">2</span>
                    <div class="rm-howto-txt">
                        <strong>{{ isEn ? 'Start evaluation' : '开始评测' }}</strong>
                        <span>{{ isEn ? 'Retrieval-only is $0 & offline' : '纯检索 0 成本 · 秒级离线' }}</span>
                    </div>
                </div>
                <span class="rm-howto-arrow">→</span>
                <div class="rm-howto-step">
                    <span class="rm-howto-idx">3</span>
                    <div class="rm-howto-txt">
                        <strong>{{ isEn ? 'Read official scores' : '看官方成绩与逐题诊断' }}</strong>
                        <span>R@10 / R@20 / R@50 · Any/All-Hit@10</span>
                    </div>
                </div>
                <el-button class="rm-howto-cta" type="primary" size="small" :disabled="isJobRunning" @click="activeTab = 'run'">
                    {{ isEn ? 'Start now ↓' : '立即开始 ↓' }}
                </el-button>
            </div>

            <el-collapse class="rm-pillars-collapse">
                <el-collapse-item name="pillars">
                    <template #title>
                        <span class="rm-pillars-title">{{ isEn ? 'Why these results are trustworthy (benchmarks · frozen protocol · isolation)' : '为什么这些成绩可信（公开基准 · 协议冻结 · 作用域隔离）' }}</span>
                    </template>
                    <el-row :gutter="12" class="rm-intro-row rm-why-eval-strip">
                <el-col :xs="24" :md="8" class="rm-col-stretch">
                    <div class="rm-intro-card is-pillar-1">
                        <div class="rm-intro-hdr">
                            <span class="rm-intro-title-txt">{{ isEn ? '1. Standardized Public Benchmarks' : '1. 国际公开基准实测' }}</span>
                            <el-tag size="small" type="info" effect="plain" class="rm-shrink-0">{{ isEn ? 'ATM-Bench Primary · LoCoMo Ref' : 'ATM-Bench 主评 · LoCoMo 参照' }}</el-tag>
                        </div>
                        <div class="rm-intro-body">
                            {{
                                isEn
                                    ? 'Primary benchmark: University of Cambridge ATM-Bench (11,034 multimodal items; Main-1013 R@10 77.21% / QS 65.54%, Hard-31 R@10 46.12% / Any-Hit@10 80.65% / QS 27.38%). Comparative reference: LoCoMo (ACL 2024, 5,882 dialogue turns).'
                                    : '第一主基准采用剑桥大学 ATM-Bench（11,034 条多模态个人记忆；Main-1013 R@10 77.21% / QS 65.54%，Hard-31 R@10 46.12% / Any-Hit@10 80.65% / QS 27.38%）；长程对话基准 LoCoMo（ACL 2024，5,882 轮对话）作为对照参照。'
                            }}
                        </div>
                    </div>
                </el-col>
                <el-col :xs="24" :md="8" class="rm-col-stretch">
                    <div class="rm-intro-card is-pillar-2">
                        <div class="rm-intro-hdr">
                            <span class="rm-intro-title-txt">{{ isEn ? '2. Cryptographically Locked & Reproducible' : '2. 协议冻结与本地可复核' }}</span>
                            <el-tag size="small" type="info" effect="plain" class="rm-shrink-0">{{ isEn ? 'SHA-256 / SHA-1 Pinned' : 'SHA-256 / SHA-1 校验' }}</el-tag>
                        </div>
                        <div class="rm-intro-body">
                            {{
                                isEn
                                    ? 'Question tiers and official evaluation prompts are checked with read-only SHA-256 / SHA-1 digests; the benchmark corpora themselves are not bundled — obtain them from the upstream projects under their terms. Deterministic retrieval runs offline at zero model cost; every question supports per-item evidence inspection.'
                                    : '题集与官方评判提示词以 SHA-256 / SHA-1 摘要只读校验；基准语料按上游许可自行获取、不随产品分发。确定性检索支持离线零成本复算，所有评测记录均开放逐题原始证据核查。'
                            }}
                        </div>
                    </div>
                </el-col>
                <el-col :xs="24" :md="8" class="rm-col-stretch">
                    <div class="rm-intro-card is-pillar-3">
                        <div class="rm-intro-hdr">
                            <span class="rm-intro-title-txt">{{ isEn ? '3. Production-Identical Engine & Scope Isolation' : '3. 生产同源引擎与作用域隔离' }}</span>
                            <el-tag size="small" type="info" effect="plain" class="rm-shrink-0">{{ isEn ? 'Zero Cross-Contamination' : '物理隔离 · 零交叉污染' }}</el-tag>
                        </div>
                        <div class="rm-intro-body">
                            {{
                                isEn
                                    ? 'Evaluations invoke the exact same local engine, tokenizer, and ranking pipeline used by daily Coding Agent recall. Benchmark data is confined to an isolated scope (`*-eval-*`) and never modifies user memory.'
                                    : '评测直接调用日常为 Coding Agent 提供召回服务的同一套本地引擎、分词器与排序管线（无独立评测分支）。评测数据严格限制在专属隔离作用域（`*-eval-*`），不读写个人记忆库。'
                            }}
                        </div>
                    </div>
                </el-col>
            </el-row>
                </el-collapse-item>
            </el-collapse>

            <el-tabs v-model="activeTab" class="rm-eval-tabs rm-primary-tabs">
                <el-tab-pane :label="isEn ? '1. Run Evaluation' : '1. 运行评测'" name="run">
                    <div v-if="activeRecord && liveJob.status === 'idle'" class="rm-latest-score-strip">
                        <div class="rm-lss-main">
                            <el-tag size="small" :type="activeRecord.is_baseline ? 'info' : 'success'">
                                {{ activeRecord.is_baseline ? (isEn ? 'Reference Baseline' : '当前套件参考基线') : (isEn ? 'Latest Local Run' : '最新本机实测成绩') }}
                            </el-tag>
                            <strong>{{ loc(activeRecord.label, activeRecord.label_en) }}</strong>
                            <span class="rm-lss-metrics">
                                <template v-if="hasOfficialRecall(activeRecord)">
                                    {{ query.suite === 'atm' ? (isEn ? 'Official R@10' : '官方 R@10（金标交均）') : (isEn ? 'Gold coverage@10' : '金标交均@10') }}:
                                    <strong class="is-ev">{{ formatPct(activeRecord.r10) }}</strong>
                                    <template v-if="activeRecord.r50 > 0"> · R@50: <strong class="is-ev">{{ formatPct(activeRecord.r50) }}</strong></template>
                                    · Any-Hit@10: <strong>{{ formatPct(activeRecord.any_hit_10) }}</strong>
                                    · All-Hit@10: <strong>{{ formatPct(activeRecord.all_hit_10) }}</strong>
                                </template>
                                <template v-else>
                                    {{ query.suite === 'atm' ? (isEn ? 'Window evidence hit' : '交付窗口证据命中') : (isEn ? 'Evidence Hit' : '证据召回') }}:
                                    <strong class="is-ev">{{ formatPct(activeRecord.evidence_hit) }}</strong>
                                </template>
                                <template v-if="activeRecord.avg_recall_ms > 0"> · {{ isEn ? 'Recall' : '检索' }}: <strong>{{ Math.round(activeRecord.avg_recall_ms) }}ms</strong></template>
                                <template v-if="activeRecord.ingest_ms > 0"> · {{ isEn ? 'Corpus ingest' : '语料导入' }}: <strong>{{ formatDurationMS(activeRecord.ingest_ms) }}</strong></template>
                                <template v-if="activeRecord.judge_scored > 0">
                                    · {{ query.suite === 'atm' ? 'Hybrid QS' : 'Judge Acc' }}:
                                    <strong class="is-jdg">{{ formatPct(activeRecord.judge_rate) }}</strong>
                                </template>
                                <template v-else>
                                    · <span class="rm-muted">{{ isEn ? 'LLM Judge: Not run (0-LLM)' : '大模型评分：未启用（纯检索）' }}</span>
                                </template>
                            </span>
                        </div>
                        <div class="rm-eval-actions">
                            <el-button size="small" type="primary" plain @click="activeTab = 'results'">
                                {{ isEn ? 'View Score Report →' : '查看成绩报告 →' }}
                            </el-button>
                            <el-button v-if="!activeRecord.is_baseline || query.suite === 'atm'" size="small" type="success" plain @click="inspectRunDetails(activeRecord)">
                                {{ isEn ? 'Inspect Questions →' : '逐题诊断明细 →' }}
                            </el-button>
                            <el-button v-if="activeRecord.judge_scored === 0 && !useAgents" size="small" type="warning" plain @click="guideToLLMEval">
                                {{ isEn ? 'Enable LLM Scoring →' : '开启大模型评分 →' }}
                            </el-button>
                        </div>
                    </div>

                    <section class="rm-run-panel" aria-labelledby="rm-run-title">
                        <div class="rm-run-intro">
                            <div>
                                <h2 id="rm-run-title">{{ isEn ? 'Start an evaluation' : '开始一次评测' }}</h2>
                                <p>{{ isEn ? 'Select a benchmark and question set, review the execution parameters, and start.' : '选择评测基准与题集，确认运行参数后启动。默认仅执行本地检索，不发起模型调用。' }}</p>
                            </div>
                            <el-tag size="small" :type="evalCorpusReady || evalConfig.arm_mode === 'oracle' ? 'success' : 'warning'" effect="plain">
                                {{ corpusStatusLabel }}
                            </el-tag>
                        </div>

                        <div v-if="overviewLoadError" class="rm-load-error">
                            <el-alert
                                type="error"
                                show-icon
                                :closable="false"
                                :title="isEn ? 'Benchmark data could not be loaded' : '基准数据加载失败'"
                                :description="overviewLoadError"
                            />
                            <el-button size="small" :loading="loading" @click="loadOverview">{{ isEn ? 'Retry' : '重试' }}</el-button>
                        </div>

                        <el-alert
                            v-if="overview.question_error"
                            type="error"
                            show-icon
                            :closable="false"
                            :title="isEn ? 'The selected question set is unavailable' : '当前题集不可用'"
                            :description="overview.question_error"
                            class="rm-question-error"
                        />

                        <div class="rm-config-grid">
                            <div class="rm-config-main">
                                <div class="rm-field-block">
                                    <span class="rm-field-label">{{ isEn ? 'Benchmark' : '评测基准' }}</span>
                                    <el-radio-group v-model="query.suite" :disabled="isJobRunning || ingesting !== ''" @change="onSuiteChange">
                                        <el-radio-button value="atm">{{ isEn ? 'ATM-Bench (Cambridge · Primary)' : 'ATM-Bench（剑桥主基准）' }}</el-radio-button>
                                        <el-radio-button value="locomo">{{ isEn ? 'LoCoMo (Reference)' : 'LoCoMo（对照参照）' }}</el-radio-button>
                                    </el-radio-group>
                                </div>

                                <div class="rm-field-block">
                                    <span class="rm-field-label">{{ isEn ? 'Question set' : '题集' }}</span>
                                    <el-radio-group v-if="query.suite === 'atm'" v-model="query.tier" :disabled="isJobRunning" data-testid="eval-tier-options" @change="onTierChange">
                                        <el-radio-button value="hard">Hard · {{ overview.tier_hard_count ?? 31 }}</el-radio-button>
                                        <el-radio-button value="1013">Main · {{ overview.tier_1013_count ?? 1013 }}</el-radio-button>
                                    </el-radio-group>
                                    <el-radio-group v-else v-model="query.tier" :disabled="isJobRunning" data-testid="eval-tier-options" @change="onTierChange">
                                        <el-radio-button value="1540">{{ isEn ? 'Official' : '官方题集' }} · {{ overview.tier_1540_count ?? 1540 }}</el-radio-button>
                                    </el-radio-group>
                                    <p class="rm-field-help">{{ selectedTierDescription }}</p>
                                </div>

                                <div class="rm-field-block">
                                    <span class="rm-field-label">{{ isEn ? 'Evaluation mode (Step 1 vs Step 2)' : '评测方式（第一步零成本检索 → 第二步大模型问答评分）' }}</span>
                                    <el-radio-group :model-value="useAgents ? 'answer' : 'retrieval'" :disabled="isJobRunning" @change="onRunModeChange">
                                        <el-radio-button value="retrieval">{{ isEn ? 'Step 1 · Retrieval only ($0 · Offline)' : '第一步 · 仅测本地检索（¥0 免费 · 秒级）' }}</el-radio-button>
                                        <el-radio-button value="answer">{{ isEn ? 'Step 2 · Retrieval + LLM Answer + Judge' : '第二步 · 检索 + 大模型作答与评分' }}</el-radio-button>
                                    </el-radio-group>
                                    <p class="rm-field-help">
                                        {{
                                            useAgents
                                                ? (isEn ? 'Calls your configured LLM to answer each question from retrieved evidence and grades it with the official judge prompt.' : '调用下方配置的大模型基于召回证据作答，并按官方裁判提示词自动打分（QS / Judge Accuracy）。')
                                                : (isEn ? 'No LLM calls. Directly verifies whether ground-truth evidence is recalled into the top-K window.' : '不调任何大模型、不花一分钱 Token，直接检验本地记忆引擎能否把金标证据召回进前 K 条窗口。')
                                        }}
                                    </p>

                                    <!-- 抽样上限只影响第二步（大模型）运行；控在模式选择下方出现，点击模式不会引起上方内容位移。 -->
                                    <div v-if="useAgents && tierTotalCount > 10" class="rm-sample-cap-strip">
                                        <span class="rm-scs-lbl">{{ isEn ? 'Sample size (save tokens on first LLM test):' : '评测题数（大模型首次试跑可先选前 5/10 题省 Token）：' }}</span>
                                        <el-radio-group v-model="evalConfig.sample_limit" size="small" :disabled="isJobRunning">
                                            <el-radio-button :value="5">{{ isEn ? 'Quick 5 Qs' : '快测 5 题' }}</el-radio-button>
                                            <el-radio-button :value="10">{{ isEn ? 'Sample 10 Qs' : '抽样 10 题' }}</el-radio-button>
                                            <el-radio-button :value="0">{{ isEn ? `Full (${tierTotalCount} Qs)` : `全量 (${tierTotalCount} 题)` }}</el-radio-button>
                                        </el-radio-group>
                                    </div>

                                    <div v-if="useAgents" class="rm-inline-llm-guide">
                                        <div class="rm-ilg-top">
                                            <div class="rm-ilg-status">
                                                <el-tag size="small" :type="isAIConfigReadyLocal ? 'success' : 'warning'">
                                                    {{ isAIConfigReadyLocal ? (isEn ? '✓ LLM Channel Ready' : '✓ 大模型通道已就绪') : (isEn ? '⚠ API Key / Model Required' : '⚠ 请先接入大模型通道') }}
                                                </el-tag>
                                                <span v-if="isAIConfigReadyLocal" class="rm-ilg-cur">
                                                    {{ aiForm.answer_model }} · <code>{{ aiForm.base_url }}</code>
                                                </span>
                                                <span v-else class="rm-ilg-cur">
                                                    {{ isEn ? 'Pick an AI Router channel in 1 click or fill your API key:' : '可一键复用「AI 路由」已有渠道，或填入 DeepSeek / OpenAI / 本地 Ollama：' }}
                                                </span>
                                            </div>
                                            <div class="rm-ilg-btns">
                                                <el-button size="small" type="primary" plain @click="openAIConfigModal(false)">
                                                    {{ isEn ? 'Configure / Switch LLM' : '配置 / 切换大模型' }}
                                                </el-button>
                                                <el-button v-if="isAIConfigReadyLocal" size="small" type="success" plain :loading="testingAI" @click="runAIConnectionTest">
                                                    {{ isEn ? 'Ping Model' : '测试连通性' }}
                                                    <template v-if="testingAI"> ({{ testAIPct }}%)</template>
                                                </el-button>
                                            </div>
                                        </div>
                                        <div v-if="pluginChannels.length > 0" class="rm-ilg-channels">
                                            <span class="rm-ilg-ch-lbl">{{ isEn ? 'One-click AI Router channels:' : '一键接入已有渠道：' }}</span>
                                            <el-button
                                                v-for="opt in pluginChannels.slice(0, 4)"
                                                :key="opt.channel_id"
                                                size="small"
                                                :type="opt.routable ? 'success' : 'info'"
                                                plain
                                                :disabled="!opt.routable"
                                                :loading="applyingChannelID === opt.channel_id"
                                                @click="applyAIPluginChannel(opt)"
                                            >
                                                🔌 {{ opt.channel_name }} ({{ (opt.models && opt.models[0]) || opt.upstream_name || opt.vendor }})
                                            </el-button>
                                        </div>
                                        <div v-else class="rm-ilg-channels">
                                            <span class="rm-ilg-ch-lbl">{{ isEn ? 'Quick presets:' : '快捷预设：' }}</span>
                                            <el-button size="small" plain @click="fillModelPreset('deepseek'); openAIConfigModal(false)">DeepSeek</el-button>
                                            <el-button size="small" plain @click="fillModelPreset('openai'); openAIConfigModal(false)">OpenAI GPT-4.1</el-button>
                                            <el-button size="small" plain @click="fillModelPreset('ollama'); saveAIConfig()">{{ isEn ? 'Local Ollama (Keyless)' : '本机 Ollama（免密直连）' }}</el-button>
                                        </div>
                                    </div>
                                </div>
                                <!-- 检索臂是与基准/题集/评测方式同级的第一级物理决策（零云端 vs 脉冲上云 vs 金标上限），
                                     常驻决策区而非沉底参数区：决策点与「运行摘要」读数同屏零滚动。 -->
                                <div class="rm-field-block">
                                    <span class="rm-field-label">{{ isEn ? 'Retrieval arm' : '检索臂' }}</span>
                                    <el-radio-group v-model="evalConfig.arm_mode" :disabled="isJobRunning || ingesting !== ''">
                                        <el-radio-button value="sdk-local-plain">{{ isEn ? 'Local Plain (default)' : '本地明文 local_plain（默认）' }}</el-radio-button>
                                        <el-radio-button v-if="!isLocalMode" value="sdk-pulse-cipher">{{ isEn ? 'Pulse Cipher' : '脉冲密文 pulse_cipher' }}</el-radio-button>
                                        <el-radio-button v-if="!isLocalMode" value="sdk-pulse-plain">{{ isEn ? 'Pulse Plain' : '脉冲明文 pulse_plain' }}</el-radio-button>
                                        <el-radio-button value="oracle">{{ isEn ? 'Oracle ceiling' : 'Oracle 金标上限' }}</el-radio-button>
                                    </el-radio-group>
                                    <p class="rm-field-help">
                                        {{
                                            evalConfig.arm_mode === 'oracle'
                                                ? (isEn ? 'Oracle injects gold evidence directly — an answerer ceiling, not an engine score.' : 'Oracle 直接注入金标证据——是答题上限参照，不是引擎成绩。')
                                                : evalConfig.arm_mode === 'sdk-local-plain'
                                                  ? (isEn ? 'Retrieval runs entirely on this machine (zero cloud requests).' : '检索 100% 在本机执行，零云端请求。')
                                                  : (isEn ? 'Pulse arms call /open/memory/v1 even with Answer/Judge off.' : '脉冲臂即使关闭 Answer/Judge 仍会请求 /open/memory/v1。')
                                        }}
                                    </p>
                                </div>
                            </div>

                            <aside class="rm-run-review">
                                <span class="rm-review-label">{{ isEn ? 'Run review' : '运行摘要' }}</span>
                                <strong class="rm-review-title">{{ selectedTierLabel }}</strong>
                                <dl>
                                    <div><dt>{{ isEn ? 'Retrieval' : '检索' }}</dt><dd>{{ evalArmLabel }}</dd></div>
                                    <div><dt>Top K</dt><dd>{{ evalConfig.arm_mode === 'oracle' ? '—' : evalConfig.top_k }}</dd></div>
                                    <div><dt>{{ isEn ? 'Question cap' : '题目上限' }}</dt><dd>{{ batchLimit }}</dd></div>
                                    <div><dt>{{ isEn ? 'Model calls' : '模型调用' }}</dt><dd>{{ useAgents ? (isEn ? 'Answer + Judge' : '作答 + 判分') : (isEn ? 'Off (0 LLM calls)' : '关闭（0 模型调用）') }}</dd></div>
                                    <div><dt>{{ isEn ? 'Corpus status' : '语料状态' }}</dt><dd :class="evalCorpusReady || evalConfig.arm_mode === 'oracle' ? 'is-ready-txt' : 'is-warn-txt'">{{ corpusStatusLabel }}</dd></div>
                                </dl>
                                <template v-if="!isJobRunning">
                                    <el-button
                                        v-if="evalConfig.arm_mode !== 'oracle' && !evalCorpusReady"
                                        v-blur
                                        type="primary"
                                        size="large"
                                        class="rm-start-button"
                                        data-testid="btn-prepare-and-start"
                                        :loading="ingesting !== '' || batchRunning"
                                        :disabled="Boolean(overview.question_error) || ingesting !== ''"
                                        @click="prepareAndStartBatchEval"
                                    >
                                        {{
                                            ingesting !== ''
                                                ? (isEn ? `Preparing corpus (${ingestPct}% · ${ingestElapsed}s)...` : `正在准备语料 (${ingestPct}% · ${ingestElapsed}s)…`)
                                                : (isEn ? 'Prepare corpus & start evaluation' : '准备语料并开始评测') + startCapSuffix
                                        }}
                                    </el-button>
                                    <el-button
                                        v-else
                                        v-blur
                                        type="primary"
                                        size="large"
                                        class="rm-start-button"
                                        data-testid="btn-start-evaluation"
                                        :loading="batchRunning"
                                        :disabled="Boolean(overview.question_error) || ingesting !== ''"
                                        @click="requestRunBatchEval"
                                    >
                                        {{ batchRunning ? (isEn ? `Starting batch (${batchStartPct}%)...` : `正在启动批次 (${batchStartPct}%)…`) : (isEn ? 'Start evaluation' : '开始评测') }}{{ batchRunning ? '' : startCapSuffix }}
                                    </el-button>
                                </template>
                                <div v-else class="rm-stop-actions">
                                    <el-button v-blur type="danger" @click="stopRunningEval(false)">{{ t('plugin.rolling-memory.eval.stopEvalBtn') }}</el-button>
                                    <el-button v-blur type="primary" plain @click="stopRunningEval(true)">{{ t('plugin.rolling-memory.eval.stopAndSaveBtn') }}</el-button>
                                </div>
                                <span class="rm-review-foot">{{ isEn ? 'Executes in an isolated evaluation scope without modifying personal memory. Results are recorded automatically.' : '在独立评测隔离域执行，不修改个人记忆；运行结果自动记录至历史台账。' }}</span>
                            </aside>
                        </div>

                        <div class="rm-run-advanced rm-run-params-open">
                            <span class="rm-field-label" style="display: block; margin-bottom: 6px">
                                {{ isEn ? 'Run parameters (recall pool depth · question type · workers · run label)' : '运行参数（召回池深度 · 题型 · 并发 · 批次备注）' }}
                            </span>
                            <div class="rm-advanced-controls">
                                    <label>
                                        <span>{{ isEn ? 'Recall pool (raw-limit)' : '召回池深度 (raw-limit)' }}</span>
                                        <!-- raw_limit 是召回候选池深度而非交付窗口；候选档对齐官方评测窗口 R@10/R@20/R@50。 -->
                                        <el-select v-model="evalConfig.top_k" :disabled="isJobRunning || evalConfig.arm_mode === 'oracle'">
                                            <el-option v-for="value in [10, 20, 50]" :key="value" :label="`Top ${value} (R@${value})`" :value="value" />
                                        </el-select>
                                    </label>
                                    <label>
                                        <span>{{ isEn ? 'Batch category' : '运行题型' }}</span>
                                        <el-select v-model="evalConfig.category" clearable :disabled="isJobRunning" :placeholder="t('plugin.rolling-memory.eval.catScopeAll')">
                                            <el-option :label="t('plugin.rolling-memory.eval.catScopeAll')" value="" />
                                            <template v-if="query.suite === 'atm'">
                                                <el-option label="Cat 1 · list_recall" value="1" />
                                                <el-option label="Cat 2 · number" value="2" />
                                                <el-option label="Cat 3 · open_end" value="3" />
                                            </template>
                                            <template v-else>
                                                <el-option :label="t('plugin.rolling-memory.eval.cat4Label')" value="4" />
                                                <el-option :label="t('plugin.rolling-memory.eval.cat2Label')" value="2" />
                                                <el-option :label="t('plugin.rolling-memory.eval.cat1Label')" value="1" />
                                                <el-option :label="t('plugin.rolling-memory.eval.cat3Label')" value="3" />
                                            </template>
                                        </el-select>
                                    </label>
                                    <!-- 纯检索臂走确定性引擎（批内常驻热会话），并发不改读数只换日志交错；仅 LLM 臂暴露。 -->
                                    <label v-if="useAgents">
                                        <span>{{ isEn ? 'LLM workers' : 'LLM 并发数' }}</span>
                                        <el-select v-model="evalConfig.workers" :disabled="isJobRunning">
                                            <el-option v-for="value in [1, 2, 4, 8]" :key="value" :label="String(value)" :value="value" />
                                        </el-select>
                                    </label>
                                    <label class="is-wide">
                                        <span>{{ isEn ? 'Run label (optional)' : '批次备注（可选）' }}</span>
                                        <el-input v-model="evalConfig.custom_label" clearable :disabled="isJobRunning" :placeholder="t('plugin.rolling-memory.eval.customLabelPlaceholder')" />
                                    </label>
                                </div>

                                <el-alert
                                    v-if="evalConfig.arm_mode === 'oracle'"
                                    type="warning"
                                    show-icon
                                    :closable="false"
                                    :title="isEn ? 'Oracle is a gold-evidence ceiling, not an engine score' : 'Oracle 是金标证据上限，不是引擎成绩'"
                                />
                                <div v-else class="rm-cost-estimate-strip">
                                    <span class="rm-ce-text">
                                        {{ useAgents ? (isEn ? `${evalArmLabel} retrieval plus Answer and Judge model calls.` : `${evalArmLabel} 检索，并调用模型作答与判分。`) : (isEn ? `${evalArmLabel} retrieval with Answer/Judge off (0 model calls).` : `${evalArmLabel} 检索；Answer/Judge 已关闭（0 次模型调用）。`) }}
                                    </span>
                                    <el-button v-if="useAgents && !overview.ai_check?.ready" size="small" type="warning" plain @click="openAIConfigModal(false)">{{ isEn ? 'Configure model' : '配置模型' }}</el-button>
                                </div>

                                <!-- 复现命令单一真源 = 运行落档的 repro_cli（与读数同批生成、字对字可审计）：
                                     前端自拼的「草稿命令」自认不等价（缺题型过滤/Judge 模型/超时/备注），已删除。 -->
                                <div v-if="latestReproCli" class="rm-cli-bar">
                                    <div class="rm-cli-cmd-row">
                                        <span class="rm-cli-prompt">$</span>
                                        <code class="rm-cli-cmd">{{ latestReproCli }}</code>
                                        <el-button size="small" type="primary" plain @click="copyText(latestReproCli, 'Copied recorded repro CLI', '已复制落档复现命令')">
                                            <Icon name="el-document-copy" :size="13" style="margin-right: 4px" />{{ isEn ? 'Copy recorded repro' : '复制落档复现命令' }}
                                        </el-button>
                                    </div>
                                    <div class="rm-cli-warn">
                                        {{ isEn ? 'Recorded with the latest run of this suite: byte-for-byte the command that produced the archived reading.' : '取自本套件最近一次落档运行：与其读数同批生成的等价复现命令。' }}
                                    </div>
                                </div>
                        </div>
                    </section>

                    <div v-if="liveJob.status !== 'idle'" class="rm-live-console">
                        <div class="rm-live-header">
                            <div class="rm-live-title-wrap">
                                <span class="rm-live-dot" :class="`is-${liveJob.status}`"></span>
                                <strong>{{ isEn ? 'Current run' : '当前运行' }}</strong>
                                <el-tag size="small" :type="liveJob.status === 'running' ? 'warning' : liveJob.status === 'completed' ? 'success' : 'info'">
                                    {{ liveJob.status.toUpperCase() }}
                                </el-tag>
                                <span class="rm-live-phase">{{ loc(liveJob.phase_text, liveJob.phase_text_en) }}</span>
                            </div>
                            <div class="rm-live-meta">
                                <span>{{ liveJob.done }} / {{ liveJob.total }} ({{ liveDisplayPercent }}%)</span>
                                <span>· {{ isEn ? 'Elapsed' : '已耗时' }} {{ liveJob.elapsed_seconds }}s</span>
                                <span v-if="liveThroughputText">· {{ liveThroughputText }}</span>
                                <span v-if="liveJob.status === 'running' && liveJob.eta_seconds > 0">· ETA ~{{ liveJob.eta_seconds }}s</span>
                                <el-radio-group v-model="liveViewMode" size="small">
                                    <el-radio-button value="feed">{{ t('plugin.rolling-memory.eval.liveFeedTab') }}</el-radio-button>
                                    <el-radio-button value="log">{{ t('plugin.rolling-memory.eval.liveLogTab') }}</el-radio-button>
                                </el-radio-group>
                                <el-button v-if="liveJob.status !== 'running'" size="small" link @click="liveJob.status = 'idle'">{{ isEn ? 'Hide' : '收起' }}</el-button>
                            </div>
                        </div>
                        <div class="rm-live-steps">
                            <div class="rm-step-pill" :class="{ 'is-active': liveJob.phase === 'ingest', 'is-done': liveJob.phase === 'evaluating' || liveJob.phase === 'done' }">
                                <span class="rm-step-idx">1</span>
                                <span>{{ isEn ? 'Isolated Corpus Verify / Ingest' : '隔离域语料核验与准备' }}</span>
                            </div>
                            <div class="rm-step-pill" :class="{ 'is-active': liveJob.phase === 'evaluating', 'is-done': liveJob.phase === 'done' }">
                                <span class="rm-step-idx">2</span>
                                <span>{{ useAgents ? (isEn ? 'Per-Question Recall + Answer + Judge' : '逐题检索 + 作答 + 判分') : (isEn ? 'Per-Question Local Recall' : '逐题本地确定性检索') }} ({{ liveJob.done }}/{{ liveJob.total }})</span>
                            </div>
                            <div class="rm-step-pill" :class="{ 'is-active': liveJob.phase === 'done', 'is-done': liveJob.status === 'completed' }">
                                <span class="rm-step-idx">3</span>
                                <span>{{ isEn ? 'Aggregate Metrics & Ledger Persist' : '指标聚合与台账落盘' }}</span>
                            </div>
                        </div>
                        <div v-if="liveJob.status === 'running' && liveJob.current_question_text" class="rm-live-active-q">
                            <el-tag size="small" type="info" effect="plain">{{ liveJob.current_question_id || '#' }}</el-tag>
                            <span class="rm-laq-txt">{{ liveJob.current_question_text }}</span>
                        </div>
                        <el-progress
                            :percentage="liveDisplayPercent"
                            :status="liveJob.status === 'completed' ? 'success' : liveJob.status === 'failed' ? 'exception' : ''"
                            :stroke-width="10"
                            striped
                            :striped-flow="liveJob.status === 'running'"
                            class="rm-live-progress"
                        />
                        <div class="rm-live-kpis">
                            <div class="rm-live-kpi-box">
                                <span class="lbl">{{ query.suite === 'atm' ? (isEn ? 'Official R@10' : '官方 R@10（金标交均）') : (isEn ? 'Gold coverage@10' : '金标交均@10') }}</span>
                                <strong class="val is-ev">{{ (liveJob.live_r10 || 0) > 0 ? formatPct(liveJob.live_r10) : '—' }}</strong>
                                <span v-if="(liveJob.live_r50 || 0) > 0" class="sub-cats">R@20 {{ formatPct(liveJob.live_r20) }} · R@50 {{ formatPct(liveJob.live_r50) }}</span>
                            </div>
                            <div class="rm-live-kpi-box">
                                <span class="lbl">Any / All-Hit@10</span>
                                <strong class="val">{{ (liveJob.live_any_hit_10 || 0) > 0 ? `${formatPct(liveJob.live_any_hit_10)} / ${formatPct(liveJob.live_all_hit_10)}` : '—' }}</strong>
                            </div>
                            <div class="rm-live-kpi-box"><span class="lbl">{{ t('plugin.rolling-memory.eval.colEv') }}</span><strong class="val is-ev">{{ formatPct(liveJob.live_evidence_hit) }}</strong></div>
                            <div class="rm-live-kpi-box">
                                <span class="lbl">{{ isEn ? 'Retrieval Latency' : '检索延迟（均值）' }}</span>
                                <strong class="val">{{ (liveJob.live_avg_recall_ms || 0) > 0 ? `${Math.round(liveJob.live_avg_recall_ms || 0)}ms` : '—' }}</strong>
                            </div>
                            <div class="rm-live-kpi-box">
                                <span class="lbl">{{ t('plugin.rolling-memory.eval.colJudge') }}</span>
                                <strong v-if="liveJob.live_judge_scored > 0" class="val is-jdg">{{ formatPct(liveJob.live_judge_rate) }}</strong>
                                <span v-else class="val rm-muted">Answer/Judge off</span>
                            </div>
                            <div class="rm-live-kpi-box">
                                <span class="lbl">{{ isEn ? 'Category evidence' : '分类证据召回' }}</span>
                                <span v-if="liveJob.suite === 'atm'" class="sub-cats">C1 {{ formatPct(liveJob.live_category_evidence?.cat1) }} · C2 {{ formatPct(liveJob.live_category_evidence?.cat2) }} · C3 {{ formatPct(liveJob.live_category_evidence?.cat3) }}</span>
                                <span v-else class="sub-cats">C4 {{ formatPct(liveJob.live_category_evidence?.cat4) }} · C2 {{ formatPct(liveJob.live_category_evidence?.cat2) }} · C1 {{ formatPct(liveJob.live_category_evidence?.cat1) }} · C3 {{ formatPct(liveJob.live_category_evidence?.cat3) }}</span>
                            </div>
                        </div>
                        <div v-if="liveJob.status === 'completed' || liveJob.status === 'cancelled'" class="rm-finish-report-card">
                            <div class="rm-frc-head">
                                <div>
                                    <el-tag size="small" type="success" effect="dark" style="margin-right: 8px">
                                        {{ isEn ? 'SCORE REPORT' : '本场评测成绩单' }}
                                    </el-tag>
                                    <strong>{{ liveJob.record ? loc(liveJob.record.label, liveJob.record.label_en) : selectedTierLabel }}</strong>
                                    <span class="rm-muted" style="margin-left: 8px">
                                        {{ isEn ? `Scored ${liveJob.done} questions in ${liveJob.elapsed_seconds}s` : `已完成 ${liveJob.done} 题 · 耗时 ${liveJob.elapsed_seconds}s` }}
                                    </span>
                                </div>
                                <div class="rm-eval-actions">
                                    <el-button size="small" type="primary" @click="viewCompletedRunResults">
                                        {{ isEn ? '1. View Full Score Report & Charts →' : '1. 查看完整成绩报告与图表 →' }}
                                    </el-button>
                                    <el-button size="small" type="success" plain @click="inspectCompletedRunQuestions">
                                        {{ isEn ? '2. Inspect Per-Question Evidence →' : '2. 逐题核查命中证据与诊断 →' }}
                                    </el-button>
                                    <el-button v-if="!liveJob.use_agents" size="small" type="warning" plain @click="guideToLLMEval">
                                        {{ isEn ? '3. Enable LLM Answer + Judge Scoring →' : '3. 下一步：开启大模型问答评分 →' }}
                                    </el-button>
                                    <el-button size="small" plain @click="query.suite === 'atm' ? goATMLeaderboard() : goLeaderboard()">
                                        {{ isEn ? 'Compare on Leaderboard' : '对照公开榜单' }}
                                    </el-button>
                                </div>
                            </div>
                            <div class="rm-frc-explain">
                                <p v-if="(liveJob.live_r10 || 0) > 0">
                                    <strong>{{ isEn ? 'Official gold-set-mean' : '官方金标交均' }}：R@10 {{ formatPct(liveJob.live_r10) }} · R@20 {{ formatPct(liveJob.live_r20) }} · R@50 {{ formatPct(liveJob.live_r50) }} · Any-Hit@10 {{ formatPct(liveJob.live_any_hit_10) }} · All-Hit@10 {{ formatPct(liveJob.live_all_hit_10) }}</strong>
                                </p>
                                <p v-if="!liveJob.use_agents">
                                    {{
                                        isEn
                                            ? ((liveJob.live_r10 || 0) > 0
                                                ? `How to read this score: Official R@10 (mean per-question gold coverage in the top-10 delivered window, same formula as the public leaderboard) is ${formatPct(liveJob.live_r10)} over ${liveJob.done} questions at $0 LLM cost; window evidence hit ${formatPct(liveJob.live_evidence_hit)} is the auxiliary legacy gauge. Reference baseline: ${currentSplitBenchmarkRef}. For an end-to-end QA score, click "3. Enable LLM Answer + Judge Scoring".`
                                                : `How to read this score: Local retrieval hit ground-truth evidence on ${formatPct(liveJob.live_evidence_hit)} of questions (${liveJob.done} Qs, $0 LLM cost). Reference baseline: ${currentSplitBenchmarkRef}.`)
                                            : ((liveJob.live_r10 || 0) > 0
                                                ? `成绩解读：本次为「纯本地检索评测（0 模型调用）」，主指标官方 R@10（交付序前 10 条窗口逐题金标覆盖均值，与公开榜单同公式）为 ${formatPct(liveJob.live_r10)}（${liveJob.done} 题）；窗口证据命中率 ${formatPct(liveJob.live_evidence_hit)} 为辅助旧量尺。公开基准对照：${currentSplitBenchmarkRef}。若需端到端问答得分，请点击上方「3. 开启大模型问答评分」。`
                                                : `成绩解读：本次为「纯本地检索评测（0 模型调用）」，在 ${liveJob.done} 道题中证据命中率为 ${formatPct(liveJob.live_evidence_hit)}（公开基准对照参考：${currentSplitBenchmarkRef}）。`)
                                    }}
                                </p>
                                <p v-else>
                                    {{
                                        isEn
                                            ? `How to read this score: Official R@10 reached ${formatPct(liveJob.live_r10 || 0)}, and end-to-end LLM Judge / Hybrid QS score reached ${formatPct(liveJob.live_judge_rate)} (${liveJob.live_judge_correct}/${liveJob.live_judge_scored}). Reference baseline: ${currentSplitBenchmarkRef}.`
                                            : `成绩解读：本次「检索 + 大模型问答评分」官方 R@10 为 ${formatPct(liveJob.live_r10 || 0)}，端到端官方裁判得分（QS / Judge 判对率）为 ${formatPct(liveJob.live_judge_rate)}（${liveJob.live_judge_correct}/${liveJob.live_judge_scored}，公开基准对照参考：${currentSplitBenchmarkRef}）。`
                                    }}
                                </p>
                            </div>
                            <div v-if="liveJob.bucket_counts && Object.keys(liveJob.bucket_counts).length > 0" class="rm-frc-buckets">
                                <span class="rm-frc-b-lbl">{{ isEn ? 'Click a bucket to filter questions:' : '点击任意分桶直接过滤查看对应题目：' }}</span>
                                <el-tag
                                    v-for="bKey in (['HIT', 'READER_GAP', 'RANK_CUTOFF', 'RECALL_MISS', 'JUDGE_AMBIGUITY'] as EvalRootCauseKey[])"
                                    :key="bKey"
                                    size="small"
                                    :type="rootCauseTagType(bKey)"
                                    class="rm-clickable-bucket"
                                    @click="inspectBucketQuestions(liveJob.record || activeRecord, bKey)"
                                >
                                    {{ rootCauseHumanLabel(bKey) }}: <strong>{{ liveJob.bucket_counts[bKey] ?? 0 }}</strong>
                                </el-tag>
                            </div>
                        </div>

                        <div v-if="liveViewMode === 'feed'" class="rm-live-feed-list">
                            <div v-for="item in (liveJob.recent_items || []).slice(0, 6)" :key="`${item.index}-${item.id}`" class="rm-live-feed-card" @click="openQuestionResultProbe(item)">
                                <div class="rm-lf-top">
                                    <span class="rm-lf-idx">#{{ item.index }} · {{ item.id }} (Cat {{ item.category }})</span>
                                    <div class="rm-lf-badges">
                                        <el-tag size="small" :type="item.evidence_hit ? 'primary' : 'danger'">Ev {{ Math.round((item.best_evidence_recall || 0) * 100) }}%</el-tag>
                                        <el-tag v-if="item.judge" size="small" :type="item.judge === 'correct' ? 'success' : 'danger'">{{ item.judge.toUpperCase() }}</el-tag>
                                        <span class="rm-lf-ms">{{ item.total_ms }}ms</span>
                                    </div>
                                </div>
                                <div class="rm-lf-q">{{ item.question }}</div>
                                <div class="rm-lf-ans"><span>Gold: <strong>{{ item.gold }}</strong></span><span v-if="item.prediction"> · AI: <strong>{{ item.prediction }}</strong></span></div>
                            </div>
                        </div>
                        <pre v-else class="rm-live-log-pre">{{ (liveJob.logs || []).slice().reverse().join('\n') }}</pre>
                    </div>

                    <el-collapse class="rm-environment-panel">
                        <el-collapse-item name="environment">
                            <template #title>
                                <span>{{ isEn ? 'Corpus, isolation and technical evidence' : '语料、隔离与技术凭证' }}</span>
                                <el-tag size="small" :type="evalCorpusReady ? 'success' : 'info'" effect="plain">{{ corpusStatusLabel }}</el-tag>
                            </template>
            <div v-if="evalConfig.arm_mode !== 'oracle'" class="rm-prepare-bar">
                <div>
                    <strong>{{ isEn ? 'Corpus preparation and verification' : '语料准备与核验' }}</strong>
                    <div class="rm-muted">
                        {{
                            isEn
                                ? 'Prepare writes source-faithful documents for the selected arm. Verify checks its manifest, scope and corpus digest. Rebuild replaces only this corpus inside the isolated scope.'
                                : 'Prepare 为所选评测臂写入忠实原文文档；Verify 核验对应 manifest、scope 与语料摘要；Rebuild 仅替换隔离 scope 内属于本语料的文档。'
                        }}
                    </div>
                    <div v-if="ingesting !== ''" class="rm-ingest-progress-box">
                        <div class="rm-ipb-top">
                            <strong>{{ ingestStageTitle }}</strong>
                            <span>{{ ingestPct }}% · {{ ingestElapsed }}s</span>
                        </div>
                        <el-progress :percentage="ingestPct" :stroke-width="8" striped striped-flow :show-text="false" />
                        <div class="rm-muted" style="margin-top: 4px">{{ ingestStageDesc }}</div>
                    </div>
                </div>
                <div class="rm-eval-actions">
                    <el-button size="small" type="primary" :loading="ingesting === 'prepare'" :disabled="isJobRunning || ingesting !== ''" @click="runIngest('prepare')">Prepare<template v-if="ingesting === 'prepare'"> ({{ ingestPct }}%)</template></el-button>
                    <el-button size="small" type="success" plain :loading="ingesting === 'verify'" :disabled="isJobRunning || ingesting !== ''" @click="runIngest('verify')">Verify<template v-if="ingesting === 'verify'"> ({{ ingestPct }}%)</template></el-button>
                    <el-button size="small" type="danger" plain :loading="ingesting === 'rebuild'" :disabled="isJobRunning || ingesting !== ''" @click="confirmRebuild">Rebuild<template v-if="ingesting === 'rebuild'"> ({{ ingestPct }}%)</template></el-button>
                </div>
            </div>

            <el-alert
                :title="readinessTitle"
                :description="readinessDescription"
                :type="evalConfig.arm_mode === 'oracle' ? 'info' : evalCorpusReady ? 'success' : 'warning'"
                show-icon
                :closable="false"
                class="rm-readiness-alert"
            />

            <div v-if="evalConfig.arm_mode !== 'oracle'" class="rm-manifest-grid">
                <div class="rm-manifest-item"><span>Transport</span><code>{{ manifestTransport }}</code></div>
                <div class="rm-manifest-item"><span>Endpoint</span><code>{{ manifestEndpoint }}</code></div>
                <div class="rm-manifest-item"><span>Scope</span><code>{{ evalScopeText }}</code></div>
                <div class="rm-manifest-item"><span>Tenant user_id</span><code>{{ manifestUserID }}</code></div>
                <div class="rm-manifest-item"><span>Documents</span><code>{{ manifestDocuments }}</code></div>
                <div class="rm-manifest-item">
                    <span>local_fallback</span>
                    <code :class="{ 'is-blocked': manifestLocalFallback !== false }">{{ localFallbackText }}</code>
                </div>
                <div class="rm-manifest-item is-wide"><span>Corpus SHA-256</span><code>{{ manifestCorpusDigest }}</code></div>
                <div class="rm-manifest-item is-wide"><span>Store path</span><code>{{ evalStorePath }}</code></div>
            </div>

            <el-alert
                type="info"
                show-icon
                :closable="false"
                class="rm-sdk-note"
                :title="
                    evalConfig.arm_mode === 'oracle'
                        ? (isEn ? 'Oracle supplies gold evidence, not an engine score' : 'Oracle 提供金标证据，不是引擎成绩')
                        : evalConfig.arm_mode === 'sdk-local-plain'
                          ? (isEn ? 'Local Plain: retrieval stays on this machine' : '本地明文：检索只在本机运行')
                          : (isEn ? `${evalArmLabel}: Answer/Judge switches do not disable pulse requests` : `${evalArmLabel}：关闭 Answer/Judge 不等于关闭脉冲请求`)
                "
                :description="
                    evalConfig.arm_mode === 'oracle'
                        ? (isEn ? 'Oracle directly supplies gold evidence. Optional Answer/Judge calls use your configured model endpoint.' : 'Oracle 直接注入金标证据；可选的 Answer/Judge 调用使用你配置的模型接口。')
                        : evalConfig.arm_mode === 'sdk-local-plain'
                          ? (isEn ? 'Local Plain reads the local SQLite vault without cloud requests. Optional Answer/Judge calls use your configured model endpoint.' : '本地明文读取本机 SQLite 金库，零云端请求；可选的 Answer/Judge 调用使用你配置的模型接口。')
                          : (isEn ? `${evalArmLabel} calls /open/memory/v1 even when Answer/Judge are off.` : `${evalArmLabel} 即使关闭 Answer/Judge 仍会请求 /open/memory/v1。`)
                "
            />

                    </el-collapse-item>
                    </el-collapse>
                </el-tab-pane>

                <el-tab-pane :label="isEn ? '2. Score Report & History' : '2. 成绩报告与历史'" name="results">
                    <el-alert
                        v-if="overviewLoadError"
                        type="error"
                        show-icon
                        :closable="false"
                        :title="isEn ? 'Results could not be refreshed' : '结果暂时无法刷新'"
                        :description="overviewLoadError"
                        class="rm-question-error"
                    />
                    <div class="rm-results-header">
                        <div>
                            <h2>{{ isEn ? 'Results and run history' : '结果与运行历史' }}</h2>
                            <p v-if="activeRecord">
                                {{ isEn ? 'Viewing' : '当前查看' }}：<strong>{{ loc(activeRecord.label, activeRecord.label_en) }}</strong>
                                <el-tag size="small" :type="activeRecord.is_baseline ? 'info' : 'success'" style="margin-left: 6px">
                                    {{ activeRecord.is_baseline ? (isEn ? 'Reference Baseline' : '内置参考基线') : (isEn ? 'Your Local Run' : '本机实测记录') }}
                                </el-tag>
                                <span>· {{ activeRecord.arm || (isEn ? 'recorded arm' : '落档评测臂') }} · {{ activeRecord.scored_questions }} {{ isEn ? 'scored questions' : '计分题' }}</span>
                            </p>
                            <p v-else>{{ isEn ? 'Your completed runs will appear here.' : '完成的运行会显示在这里。' }}</p>
                        </div>
                        <div class="rm-eval-actions">
                            <el-button size="small" type="primary" plain :loading="exportingRepro" @click="exportReproBundle('markdown')">
                                {{ isEn ? 'Export Report (.md)' : '导出复现报告 (.md)' }}
                            </el-button>
                            <el-button size="small" plain :loading="exportingRepro" @click="exportReproBundle('json')">
                                {{ isEn ? 'Export Bundle (.json)' : '导出机读包 (.json)' }}
                            </el-button>
                            <el-button v-if="activeRecord && activeRecord.judge_scored === 0" size="small" type="warning" plain @click="guideToLLMEval">
                                {{ isEn ? 'Enable LLM Answer + Judge →' : '开启大模型问答评分 →' }}
                            </el-button>
                            <el-button v-if="activeRecord && (!activeRecord.is_baseline || query.suite === 'atm')" size="small" type="primary" @click="inspectRunDetails(activeRecord)">
                                {{ isEn ? 'Inspect Per-Question Details →' : '查看逐题诊断明细 →' }}
                            </el-button>
                            <el-button size="small" plain @click="activeTab = 'run'">
                                {{ isEn ? 'Back to Run Config' : '返回运行配置' }}
                            </el-button>
                        </div>
                    </div>

                    <!-- Delta 对比矩阵：本次运行 vs 同档封印基线的三列配对视图（官方口径逐行对账，
                         退化超 2.5pp 盈亏线红警）。查看对象为基线时退化为单列绝对值。 -->
                    <div v-if="activeRecord && deltaRows.length > 0" class="rm-delta-matrix">
                        <div class="rm-dm-head">
                            <strong>{{ isEn ? 'Baseline Comparison Matrix' : '基线对比矩阵' }}</strong>
                            <span v-if="deltaBaseline" class="rm-muted">
                                {{ isEn ? 'vs sealed baseline' : '对照封印基线' }}：{{ loc(deltaBaseline.label, deltaBaseline.label_en) }}
                                <template v-if="topKOf(deltaBaseline) > 0">（TopK {{ topKOf(deltaBaseline) }}）</template>
                            </span>
                            <span v-else class="rm-muted">{{ isEn ? 'Viewing a baseline record: absolute values only.' : '当前查看基线记录：仅展示绝对值。' }}</span>
                            <el-tag v-if="topKOf(activeRecord) > 0" size="small" type="info" effect="plain">TopK {{ topKOf(activeRecord) }}</el-tag>
                        </div>
                        <table class="rm-dm-table">
                            <thead>
                                <tr>
                                    <th>{{ isEn ? 'Metric' : '指标' }}</th>
                                    <th>{{ isEn ? 'This Run' : '本次运行' }}</th>
                                    <th>{{ isEn ? 'Baseline' : '基线' }}</th>
                                    <th>Δ</th>
                                </tr>
                            </thead>
                            <tbody>
                                <tr v-for="row in deltaRows" :key="row.key">
                                    <td class="rm-dm-metric">{{ row.label }}</td>
                                    <td class="rm-dm-num">{{ row.kind === 'ms' ? `${Math.round(row.run)}ms` : formatPct(row.run) }}</td>
                                    <td class="rm-dm-num rm-muted">{{ deltaBaseline ? (row.kind === 'ms' ? `${Math.round(row.base)}ms` : formatPct(row.base)) : '—' }}</td>
                                    <td class="rm-dm-num rm-dm-delta" :class="deltaCellClass(row)">
                                        {{ formatDeltaPP(row) }}<template v-if="row.warn"> ⚠</template>
                                    </td>
                                </tr>
                            </tbody>
                        </table>
                        <div v-if="deltaRows.some((r) => r.warn)" class="rm-dm-warnline">
                            {{ isEn ? '⚠ Regression beyond the 2.5pp break-even line vs baseline — inspect per-question diagnosis before accepting this reading.' : '⚠ 相对基线退化超过 2.5pp 盈亏线——采信该读数前请先查逐题诊断。' }}
                        </div>
                    </div>

                    <div v-if="activeRecord" class="rm-score-guide-banner">
                        <div class="rm-sgb-item">
                            <strong v-if="hasOfficialRecall(activeRecord)">
                                {{ query.suite === 'atm' ? (isEn ? '1. Official R@10 (gold-set-mean)' : '1. 官方 R@10（逐题金标交均）') : (isEn ? '1. Gold coverage@10' : '1. 金标交均@10') }}：{{ formatPct(activeRecord.r10) }}
                                （Any-Hit@10 {{ formatPct(activeRecord.any_hit_10) }} · All-Hit@10 {{ formatPct(activeRecord.all_hit_10) }}）
                            </strong>
                            <strong v-else>{{ isEn ? '1. Evidence Hit (delivery window)' : '1. 证据窗口命中率' }}：{{ formatPct(activeRecord.evidence_hit) }}</strong>
                            <span>{{ isEn ? 'Official retrieval metric: mean per-question gold-evidence coverage inside the top-10 delivered window ($0 LLM cost; Any-Hit / All-Hit are auxiliary binary rates).' : '官方检索指标：交付序前 10 条窗口内逐题金标覆盖率均值（0 模型成本；Any-Hit 至少一条 / All-Hit 全部命中为辅助二值指标）。' }}</span>
                        </div>
                        <div class="rm-sgb-item">
                            <strong>{{ query.suite === 'atm' ? (isEn ? '2. End-to-End Hybrid QS' : '2. 端到端混合 QS 得分') : (isEn ? '2. LLM Judge Accuracy' : '2. 大模型裁判判对率') }}：{{ activeRecord.judge_scored > 0 ? formatPct(activeRecord.judge_rate) : (isEn ? 'Not run (0-LLM mode)' : '未启用（当前为纯检索模式）') }}</strong>
                            <span v-if="activeRecord.judge_scored > 0">{{ isEn ? 'Measures final answer quality after feeding retrieved evidence into the Answer LLM and grading with the official Judge prompt.' : '衡量最终回答质量：把召回证据喂给答题大模型后，由官方冻结裁判协议评出的综合得分。' }}</span>
                            <span v-else>{{ isEn ? 'This run only tested retrieval. To score final answers, switch to "Step 2 · Retrieval + LLM Answer + Judge" and configure an LLM channel.' : '本场运行仅测试了本地检索。若需测试大模型最终答题得分，请点击右上角「开启大模型问答评分」接入模型后运行。' }}</span>
                        </div>
                        <div v-if="activeRecord.bucket_counts && Object.keys(activeRecord.bucket_counts).length > 0" class="rm-sgb-item is-full-row">
                            <strong>{{ isEn ? '3. Diagnostic Breakdown (Click any bucket to inspect those questions)' : '3. 逐题归因诊断（点击任意标签直接查看该类题目与证据）' }}</strong>
                            <div class="rm-frc-buckets" style="margin-top: 4px">
                                <el-tag
                                    v-for="bKey in (['HIT', 'READER_GAP', 'RANK_CUTOFF', 'RECALL_MISS', 'JUDGE_AMBIGUITY'] as EvalRootCauseKey[])"
                                    :key="bKey"
                                    size="small"
                                    :type="rootCauseTagType(bKey)"
                                    class="rm-clickable-bucket"
                                    @click="inspectBucketQuestions(activeRecord, bKey)"
                                >
                                    {{ rootCauseHumanLabel(bKey) }}: <strong>{{ activeRecord.bucket_counts[bKey] ?? 0 }}</strong>
                                </el-tag>
                            </div>
                        </div>
                    </div>

                    <div v-if="activeRecord" class="rm-result-kpis">
                        <div>
                            <span>{{ isEn ? 'Official R@10 (gold-set-mean)' : '官方 R@10（金标交均）' }}</span>
                            <strong>{{ activeRecord.r10 > 0 ? formatPct(activeRecord.r10) : '—' }}</strong>
                            <small v-if="activeRecord.r10 > 0" class="rm-kpi-micro">{{ isEn ? 'mean per-question gold coverage in the top-10 delivered window' : '交付序前 10 条窗口 · 逐题金标覆盖均值' }}</small>
                        </div>
                        <div>
                            <span>R@20 / R@50</span>
                            <strong>{{ activeRecord.r20 > 0 ? `${formatPct(activeRecord.r20)} / ${formatPct(activeRecord.r50)}` : '—' }}</strong>
                            <small v-if="activeRecord.r20 > 0" class="rm-kpi-micro">{{ isEn ? 'same gold-set-mean metric, wider windows' : '同口径 · 更宽窗口' }}</small>
                        </div>
                        <div>
                            <span>Any-Hit@10 / All-Hit@10</span>
                            <strong>{{ activeRecord.any_hit_10 > 0 || activeRecord.all_hit_10 > 0 ? `${formatPct(activeRecord.any_hit_10)} / ${formatPct(activeRecord.all_hit_10)}` : '—' }}</strong>
                            <small v-if="activeRecord.any_hit_10 > 0 || activeRecord.all_hit_10 > 0" class="rm-kpi-micro">{{ isEn ? '≥1 gold hit / all gold hit in top-10' : '前 10 条内 ≥1 条命中 / 全部命中' }}</small>
                        </div>
                        <div>
                            <span>{{ query.suite === 'atm' ? (isEn ? 'Official Hybrid QS' : '官方混合 QS 得分') : t('plugin.rolling-memory.eval.colJudge') }}</span>
                            <strong>{{ activeRecord.judge_scored > 0 ? formatPct(activeRecord.judge_rate) : '—' }}</strong>
                        </div>
                        <div>
                            <span>{{ isEn ? 'Retrieval Latency (Mean / P95)' : '检索延迟（均值 / P95）' }}</span>
                            <strong>{{ activeRecord.avg_recall_ms > 0 ? `${Math.round(activeRecord.avg_recall_ms)} / ${Math.round(activeRecord.p95_recall_ms || 0)} ms` : '—' }}</strong>
                            <small v-if="activeRecord.elapsed_seconds > 0" class="rm-kpi-micro">{{ isEn ? `end-to-end ${activeRecord.elapsed_seconds}s` : `端到端 ${activeRecord.elapsed_seconds}s` }}</small>
                        </div>
                        <div>
                            <span>{{ isEn ? 'Corpus Ingest / Scored Qs' : '语料导入 / 计分题' }}</span>
                            <strong>{{ activeRecord.ingest_ms > 0 ? formatDurationMS(activeRecord.ingest_ms) : '—' }} / {{ activeRecord.scored_questions }}</strong>
                            <small v-if="!hasOfficialRecall(activeRecord)" class="rm-kpi-micro">{{ isEn ? `legacy window hit ${formatPct(activeRecord.evidence_hit)}` : `旧口径窗口命中 ${formatPct(activeRecord.evidence_hit)}` }}</small>
                        </div>
                    </div>
                    <el-empty v-else :description="isEn ? 'No runs for the currently offered question sets yet' : '当前提供的题集还没有运行结果'" />

            <el-row v-if="activeRecord" :gutter="12" class="rm-charts-row">
                <el-col :xs="24" :xl="14" class="rm-col-stretch">
                    <div class="rm-chart-box">
                        <div class="rm-chart-hdr">
                            <span class="rm-chart-title-txt">
                                {{ t('plugin.rolling-memory.eval.chart1Title') }}
                                <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                                    <template #reference>
                                        <button type="button" class="tip-q" :aria-label="isEn ? 'Chart 1 semantics' : '图表一语义说明'">?</button>
                                    </template>
                                    <div class="rm-help-body">
                                        <p v-if="isEn">Compares evidence recall with optional Answer/Judge accuracy. Historical bars are read-only; check each run’s provenance.</p>
                                        <p v-else>对比证据召回与可选 Answer/Judge 判对率。历史柱线只读，请核对每次运行的 provenance。</p>
                                    </div>
                                </el-popover>
                            </span>
                            <div class="rm-chart-legend">
                                <span class="rm-leg-item">
                                    <span class="rm-leg-dot is-ev"></span>
                                    <span>{{ t('plugin.rolling-memory.eval.legendEv') }}</span>
                                </span>
                                <span class="rm-leg-item">
                                    <span class="rm-leg-dot is-jdg"></span>
                                    <span>{{ t('plugin.rolling-memory.eval.legendJdg') }}</span>
                                </span>
                                <!-- 图例两项说明聚合为组尾单个点击式 `?` 弹层 -->
                                <el-popover trigger="click" placement="top" :width="380" popper-class="rm-help-popper">
                                    <template #reference>
                                        <button type="button" class="tip-q" :aria-label="isEn ? 'Chart legend semantics' : '图表图例语义说明'">?</button>
                                    </template>
                                    <div class="rm-help-body">
                                        <p v-if="isEn">{{ query.suite === 'atm' ? 'Official R@10 (gold-set-mean): mean per-question gold-evidence coverage inside the top-10 delivered window.' : 'Evidence Hit Rate: recalled context contains the ground-truth evidence.' }}</p>
                                        <p v-else>{{ query.suite === 'atm' ? '官方 R@10（逐题金标交均）：交付序前 10 条窗口内命中金标证据的平均覆盖率。' : '证据命中率：召回上下文包含金标证据的题目比例。' }}</p>
                                        <p v-if="isEn">
                                            {{
                                                query.suite === 'atm'
                                                    ? 'ATM-Bench Official Hybrid QS: list_recall continuous Jaccard / number EM / open_end Judge.'
                                                    : 'LLM Judge Accuracy: Percentage of answers judged CORRECT by the frozen locomo-judge protocol.'
                                            }}
                                        </p>
                                        <p v-else>
                                            {{
                                                query.suite === 'atm'
                                                    ? 'ATM-Bench 官方混合 QS：list_recall 连续 Jaccard · number EM · open_end Judge 的加权均值。'
                                                    : 'LLM 裁判判对率（Judge Accuracy）：由冻结协议 locomo-judge 裁定回答正确的比例。'
                                            }}
                                        </p>
                                    </div>
                                </el-popover>
                            </div>
                        </div>
                        <svg class="rm-bar-svg" viewBox="0 0 740 245" role="img" :aria-label="t('plugin.rolling-memory.eval.chart1Title')">
                            <line x1="52" y1="22" x2="720" y2="22" class="rm-grid-line" />
                            <line x1="52" y1="62" x2="720" y2="62" class="rm-grid-line" />
                            <line x1="52" y1="102" x2="720" y2="102" class="rm-grid-line" />
                            <line x1="52" y1="142" x2="720" y2="142" class="rm-grid-line" />
                            <line x1="52" y1="182" x2="720" y2="182" class="rm-axis-line" />
                            <text x="44" y="26" class="rm-axis-txt" text-anchor="end">100%</text>
                            <text x="44" y="66" class="rm-axis-txt" text-anchor="end">75%</text>
                            <text x="44" y="106" class="rm-axis-txt" text-anchor="end">50%</text>
                            <text x="44" y="146" class="rm-axis-txt" text-anchor="end">25%</text>
                            <text x="44" y="186" class="rm-axis-txt" text-anchor="end">0%</text>

                            <line
                                v-if="query.suite === 'atm' ? atmOracleQSCeiling > 0 : locomoOracleCeiling > 0"
                                x1="52"
                                :y1="182 - (query.suite === 'atm' ? atmOracleQSRatio : locomoOracleCeiling) * 160"
                                x2="720"
                                :y2="182 - (query.suite === 'atm' ? atmOracleQSRatio : locomoOracleCeiling) * 160"
                                class="rm-oracle-line"
                            />

                            <!-- 封印基线参考线：当前查看记录的同档基线官方 R@10 画水平虚线，
                                 任何柱与基线的相对高低一眼可辨（Oracle 上限线为另一条独立参考）。 -->
                            <line
                                v-if="deltaBaseline && (deltaBaseline.r10 || 0) > 0"
                                x1="52"
                                :y1="182 - clamp01(deltaBaseline.r10) * 160"
                                x2="720"
                                :y2="182 - clamp01(deltaBaseline.r10) * 160"
                                class="rm-baseline-line"
                            />

                            <g
                                v-for="(item, idx) in chartRecords"
                                :key="item.id"
                                :transform="`translate(${68 + idx * chartSlotWidth}, 0)`"
                                class="rm-bar-group"
                                :class="{ 'is-active': activeRecord?.id === item.id }"
                                @click="selectedRecord = item"
                            >
                                <rect
                                    v-if="activeRecord?.id === item.id"
                                    x="-4"
                                    y="14"
                                    width="68"
                                    height="212"
                                    rx="6"
                                    class="rm-bar-active-bg"
                                />
                                <rect
                                    x="4"
                                    :y="182 - clamp01(chartBarValue(item)) * 160"
                                    width="24"
                                    :height="Math.max(2, clamp01(chartBarValue(item)) * 160)"
                                    rx="3"
                                    class="rm-bar-ev"
                                />
                                <text
                                    x="16"
                                    :y="182 - clamp01(chartBarValue(item)) * 160 - 4"
                                    class="rm-bar-val"
                                    text-anchor="middle"
                                >
                                    {{ Math.round(clamp01(chartBarValue(item)) * 100) }}%
                                </text>

                                <rect
                                    v-if="item.judge_rate > 0"
                                    x="32"
                                    :y="182 - clamp01(item.judge_rate) * 160"
                                    width="24"
                                    :height="Math.max(2, clamp01(item.judge_rate) * 160)"
                                    rx="3"
                                    class="rm-bar-jdg"
                                />
                                <text
                                    v-if="item.judge_rate > 0"
                                    x="44"
                                    :y="182 - clamp01(item.judge_rate) * 160 - 4"
                                    class="rm-bar-val is-jdg-txt"
                                    text-anchor="middle"
                                >
                                    {{ Math.round(clamp01(item.judge_rate) * 100) }}%
                                </text>

                                <text x="30" y="202" class="rm-bar-lbl" :class="{ 'is-active-lbl': activeRecord?.id === item.id }" text-anchor="middle">
                                    {{ shortChartLabel(item) }}
                                </text>
                                <text x="30" y="218" class="rm-bar-sub" text-anchor="middle">
                                    {{ item.is_baseline ? t('plugin.rolling-memory.eval.baselineShort') : t('plugin.rolling-memory.eval.runShort') }} · {{ tierOptionTag(item.tier) }}<template v-if="topKOf(item) > 0"> · k{{ topKOf(item) }}</template>
                                </text>
                            </g>

                            <!-- 参考线本身保留在柱体下层（细虚线），仅标签移到柱体之后绘制，保证文字永不被高柱遮挡。 -->
                            <text v-if="deltaBaseline && (deltaBaseline.r10 || 0) > 0" x="56" :y="182 - clamp01(deltaBaseline.r10) * 160 - 4" class="rm-baseline-txt">
                                {{ isEn ? 'baseline R@10' : '基线 R@10' }} {{ formatPct(deltaBaseline.r10) }}
                            </text>
                            <text v-if="query.suite === 'atm' ? atmOracleQSCeiling > 0 : locomoOracleCeiling > 0" x="716" :y="query.suite === 'atm' ? atmOracleLineY : locomoOracleLineY" class="rm-oracle-txt" text-anchor="end">
                                {{
                                    query.suite === 'atm'
                                        ? atmOracleQSLabel
                                        : t('plugin.rolling-memory.eval.oracleLine')
                                }}
                            </text>
                        </svg>
                    </div>
                </el-col>

                <el-col :xs="24" :xl="10" class="rm-col-stretch">
                    <div class="rm-chart-box">
                        <div class="rm-chart-hdr">
                            <span class="rm-chart-title-txt">
                                {{ t('plugin.rolling-memory.eval.chart2Title') }}:
                                {{ activeRecord ? loc(activeRecord.label, activeRecord.label_en) : '—' }}
                                <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                                    <template #reference>
                                        <button type="button" class="tip-q" :aria-label="isEn ? 'LoCoMo 4 question categories' : 'LoCoMo 四大题型分类说明'">?</button>
                                    </template>
                                    <div class="rm-help-body">
                                        <p v-if="isEn">LoCoMo 4 Question Categories: Cat 4 = Single-Hop factual recall; Cat 2 = Temporal reasoning; Cat 1 = Multi-Hop synthesis across turns; Cat 3 = Open-Domain commonsense inference.</p>
                                        <p v-else>LoCoMo 四大题型分类剖析：Cat 4 单跳事实定位、Cat 2 时序先后与日期推理、Cat 1 跨轮次多跳综合推理、Cat 3 开放域常识结合推断。</p>
                                    </div>
                                </el-popover>
                            </span>
                            <el-tag size="small" :type="activeRecord?.is_baseline ? 'info' : 'success'">
                                {{ activeRecord?.is_baseline ? t('plugin.rolling-memory.eval.baselineBadge') : t('plugin.rolling-memory.eval.runBadge') }}
                            </el-tag>
                        </div>
                        <div class="rm-cat-bars">
                            <div v-for="cat in categoryRows" :key="cat.key" class="rm-cat-item">
                                <div class="rm-cat-top">
                                    <span class="rm-cat-name">{{ cat.label }}</span>
                                    <span class="rm-cat-nums">
                                        {{ t('plugin.rolling-memory.eval.catEvLabel') }} <strong>{{ formatPct(cat.ev) }}</strong>
                                        <template v-if="cat.jdg > 0">
                                            · {{ t('plugin.rolling-memory.eval.catJdgLabel') }}
                                            <strong class="is-jdg-num">{{ formatPct(cat.jdg) }}</strong>
                                        </template>
                                    </span>
                                </div>
                                <div class="rm-cat-track">
                                    <div class="rm-cat-fill is-ev" :style="{ width: formatPct(cat.ev) }"></div>
                                </div>
                                <div v-if="cat.jdg > 0" class="rm-cat-track" style="margin-top: 4px">
                                    <div class="rm-cat-fill is-jdg" :style="{ width: formatPct(cat.jdg) }"></div>
                                </div>
                            </div>
                        </div>
                        <div class="rm-cat-note">
                            <strong>{{ t('plugin.rolling-memory.eval.notePrefix') }}：</strong>
                            <span>{{ activeRecord ? loc(activeRecord.note, activeRecord.note_en) : '-' }}</span>
                        </div>
                    </div>
                </el-col>
            </el-row>

            <div class="rm-history-toolbar">
                <div>
                    <strong>{{ isEn ? 'Recorded runs' : '运行记录' }}</strong>
                    <span>{{ isEn ? 'Select a row to update the summary above.' : '点击一行更新上方结果摘要。' }}</span>
                </div>
            </div>
            <el-alert
                type="info"
                show-icon
                :closable="false"
                class="rm-readiness-alert"
                :title="isEn ? 'Each score belongs to its recorded run' : '每项成绩都属于其落档运行'"
                :description="isEn ? 'Check the arm and provenance before comparing runs. Archived readings are not presented as current-configuration results.' : '比较前请核对评测臂与溯源信息；归档读数不会冒充当前配置的结果。'"
            />
                    <el-table
                        v-if="allLedgerRows.length > 0"
                        :data="allLedgerRows"
                        max-height="520"
                        size="small"
                        border
                        stripe
                        highlight-current-row
                        class="ag-data-table rm-roomy-table"
                        header-cell-class-name="table-header-cell"
                        @row-click="onLedgerRowClick"
                    >
                        <el-table-column :label="t('plugin.rolling-memory.eval.colType')" width="90" align="center">
                            <template #header>
                                <span>
                                    {{ t('plugin.rolling-memory.eval.colType') }}
                                    <el-popover trigger="click" placement="top" :width="320" popper-class="rm-help-popper">
                                        <template #reference>
                                            <button type="button" class="tip-q" :aria-label="isEn ? 'Ledger type semantics' : '台账类型语义说明'">?</button>
                                        </template>
                                        <div class="rm-help-body">
                                            <p v-if="isEn">Baseline (read-only reference) vs recorded run</p>
                                            <p v-else>台账类型：封印基线（Baseline 参考水位） vs 实测运行（本机执行记录）</p>
                                        </div>
                                    </el-popover>
                                </span>
                            </template>
                            <template #default="{ row }">
                                <el-tag size="small" :type="row.is_baseline ? 'info' : 'success'">
                                    {{ row.is_baseline ? t('plugin.rolling-memory.eval.typeBaseline') : t('plugin.rolling-memory.eval.typeRun') }}
                                </el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.rolling-memory.eval.colLabel')" min-width="200">
                            <template #default="{ row }">
                                <div class="rm-cell-wrap">
                                    <strong>{{ loc(row.label, row.label_en) }}</strong>
                                </div>
                            </template>
                        </el-table-column>
                        <el-table-column prop="created_at" :label="t('plugin.rolling-memory.eval.colTime')" width="150" />
                        <el-table-column :label="t('plugin.rolling-memory.eval.colTier')" width="100">
                            <template #header>
                                <span>
                                    {{ t('plugin.rolling-memory.eval.colTier') }}
                                    <el-popover trigger="click" placement="top" :width="300" popper-class="rm-help-popper">
                                        <template #reference>
                                            <button type="button" class="tip-q" :aria-label="isEn ? 'Benchmark tier semantics' : '题集档位语义说明'">?</button>
                                        </template>
                                        <div class="rm-help-body">
                                            <p v-if="isEn">Benchmark tier and number of scored questions</p>
                                            <p v-else>评测题集档位与实际计分题数</p>
                                        </div>
                                    </el-popover>
                                </span>
                            </template>
                            <template #default="{ row }">
                                <span class="rm-num-cell">{{ tierOptionTag(row.tier) }} ({{ row.scored_questions }})</span>
                            </template>
                        </el-table-column>
                        <el-table-column prop="model" :label="t('plugin.rolling-memory.eval.colModel')" min-width="120" />
                        <el-table-column label="TopK" width="78" align="center">
                            <template #default="{ row }">
                                <span v-if="topKOf(row) > 0" class="rm-num-cell">{{ topKOf(row) }}</span>
                                <span v-else class="rm-muted">—</span>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'Official R@10' : '官方 R@10'" width="112" align="right">
                            <template #header>
                                <span>
                                    {{ isEn ? 'Official R@10' : '官方 R@10' }}
                                    <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                        <template #reference>
                                            <button type="button" class="tip-q" :aria-label="isEn ? 'Official R@10 semantics' : '官方 R@10 语义说明'">?</button>
                                        </template>
                                        <div class="rm-help-body">
                                            <p v-if="isEn">Official gold-set-mean Recall@10: mean per-question gold-evidence coverage inside the top-10 delivered window (same formula as the public ATM-Bench leaderboard). Subtitle shows Any-Hit@10 / All-Hit@10.</p>
                                            <p v-else>官方 R@10（逐题金标交均）：交付序前 10 条窗口内金标覆盖率均值，与 ATM-Bench 公开榜单同公式；小字为 Any-Hit@10 / All-Hit@10。</p>
                                        </div>
                                    </el-popover>
                                </span>
                            </template>
                            <template #default="{ row }">
                                <template v-if="hasOfficialRecall(row)">
                                    <strong class="rm-num-cell" style="color: var(--el-color-primary)">{{ formatPct(row.r10) }}</strong>
                                    <div class="rm-muted" style="font-size: 11px">A {{ formatPct(row.any_hit_10) }} · F {{ formatPct(row.all_hit_10) }}</div>
                                </template>
                                <span v-else class="rm-muted">—</span>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'R@20 / R@50' : 'R@20 / R@50'" width="120" align="right">
                            <template #default="{ row }">
                                <span v-if="(row.r20 || 0) > 0 || (row.r50 || 0) > 0" class="rm-num-cell">{{ (row.r20 || 0) > 0 ? formatPct(row.r20) : '—' }} / {{ (row.r50 || 0) > 0 ? formatPct(row.r50) : '—' }}</span>
                                <span v-else class="rm-muted">—</span>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'Recall / Ingest' : '检索 / 导入耗时'" width="112" align="right">
                            <template #default="{ row }">
                                <span v-if="(row.avg_recall_ms || 0) > 0" class="rm-num-cell">{{ Math.round(row.avg_recall_ms) }}ms</span>
                                <span v-else-if="(row.elapsed_seconds || 0) > 0" class="rm-num-cell">{{ row.elapsed_seconds }}s</span>
                                <span v-else class="rm-muted">—</span>
                                <div v-if="(row.ingest_ms || 0) > 0" class="rm-muted" style="font-size: 11px">
                                    {{ isEn ? 'Ingest' : '导入' }} {{ formatDurationMS(row.ingest_ms) }}
                                </div>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.rolling-memory.eval.colEv')" width="100" align="right">
                            <template #header>
                                <span>
                                    {{ t('plugin.rolling-memory.eval.colEv') }}
                                    <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                        <template #reference>
                                            <button type="button" class="tip-q" :aria-label="isEn ? 'Evidence hit rate semantics' : '证据命中率语义说明'">?</button>
                                        </template>
                                        <div class="rm-help-body">
                                            <p v-if="isEn">Evidence Hit Rate: % of questions where retrieved context contains ground-truth evidence</p>
                                            <p v-else>证据命中率（Evidence Hit）：召回结果命中金标证据的比例（附全交付窗口命中率）</p>
                                        </div>
                                    </el-popover>
                                </span>
                            </template>
                            <template #default="{ row }">
                                <strong class="rm-num-cell">{{ formatPct(row.evidence_hit) }}</strong>
                                <div v-if="row.full_evidence_hit && row.full_evidence_hit > row.evidence_hit" class="rm-muted" style="font-size: 11px">
                                    Full {{ formatPct(row.full_evidence_hit) }}
                                </div>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.rolling-memory.eval.colAic')" width="100" align="right">
                            <template #header>
                                <span>
                                    {{ t('plugin.rolling-memory.eval.colAic') }}
                                    <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                        <template #reference>
                                            <button type="button" class="tip-q" :aria-label="isEn ? 'Answer-in-Context semantics' : '答案在上下文率语义说明'">?</button>
                                        </template>
                                        <div class="rm-help-body">
                                            <p v-if="isEn">Answer-in-Context (AIC / TokenRecall): % of ground-truth answer tokens present in the recalled context window</p>
                                            <p v-else>答案在上下文率（AIC / TokenRecall）：召回上下文中包含标准答案关键 Token 的覆盖比例</p>
                                        </div>
                                    </el-popover>
                                </span>
                            </template>
                            <template #default="{ row }">
                                <span class="rm-num-cell">{{ formatPct(row.answer_in_context) }}</span>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.rolling-memory.eval.colJudge')" width="120" align="right">
                            <template #header>
                                <span>
                                    {{ t('plugin.rolling-memory.eval.colJudge') }}
                                    <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                                        <template #reference>
                                            <button type="button" class="tip-q" :aria-label="isEn ? 'Judge score semantics' : '判官得分语义说明'">?</button>
                                        </template>
                                        <div class="rm-help-body">
                                            <p v-if="isEn">
                                                {{
                                                    query.suite === 'atm'
                                                        ? 'ATM-Bench Official Hybrid QS (percentage); binary List/Number verdict counts remain in each run record and drive the 5-bucket attribution.'
                                                        : 'End-to-End LLM Judge Accuracy (correct / scored) under frozen judge prompt'
                                                }}
                                            </p>
                                            <p v-else>
                                                {{
                                                    query.suite === 'atm'
                                                        ? 'ATM-Bench 官方混合 QS（百分比）；List/Number 二值判定计数仍保留在运行记录中并服务五桶归因。'
                                                        : '端到端 Judge 判对率（判对题数 / 总评题数，基于官方冻结协议）'
                                                }}
                                            </p>
                                        </div>
                                    </el-popover>
                                </span>
                            </template>
                            <template #default="{ row }">
                                <strong v-if="row.judge_scored > 0" class="rm-num-cell" style="color: var(--el-color-success)">
                                    {{ formatPct(row.judge_rate) }}
                                    <span v-if="query.suite === 'atm'" class="rm-muted">{{ isEn ? '(QS)' : '（QS）' }}</span>
                                    <span v-else> ({{ row.judge_correct }}/{{ row.judge_scored }})</span>
                                    <!-- QS 头条用已评分分母；存在未判/失败题时同屏给出全分母读数，防止选择性偏高分。 -->
                                    <span
                                        v-if="query.suite === 'atm' && row.judge_scored < row.scored_questions"
                                        class="rm-muted"
                                        style="display: block; font-size: 11px"
                                    >
                                        {{
                                            isEn
                                                ? `all-denominator ${formatPct((row.judge_rate * row.judge_scored) / row.scored_questions)}`
                                                : `全分母 ${formatPct((row.judge_rate * row.judge_scored) / row.scored_questions)}`
                                        }}
                                    </span>
                                </strong>
                                <span v-else class="rm-muted">{{ t('plugin.rolling-memory.eval.noJudgeText') }}</span>
                            </template>
                        </el-table-column>
                        <el-table-column
                            :label="
                                query.suite === 'atm'
                                    ? isEn
                                        ? 'Subtypes (C1/C2/C3)'
                                        : '三类分项 (C1/C2/C3)'
                                    : t('plugin.rolling-memory.eval.colCats')
                            "
                            min-width="130"
                        >
                            <template #default="{ row }">
                                <template v-if="query.suite === 'atm'">
                                    <span v-if="row.judge_scored > 0" class="rm-num-cell">
                                        C1 {{ formatPct(row.category_judge?.cat1) }} · C2 {{ formatPct(row.category_judge?.cat2) }} · C3 {{ formatPct(row.category_judge?.cat3) }}
                                    </span>
                                    <span v-else class="rm-num-cell">
                                        C1 {{ formatPct(row.category_evidence?.cat1) }} · C2 {{ formatPct(row.category_evidence?.cat2) }} · C3 {{ formatPct(row.category_evidence?.cat3) }}
                                    </span>
                                </template>
                                <template v-else>
                                    <span v-if="row.judge_scored > 0" class="rm-num-cell">
                                        C4 {{ formatPct(row.category_judge?.cat4) }} · C2 {{ formatPct(row.category_judge?.cat2) }} · C1 {{ formatPct(row.category_judge?.cat1) }}
                                    </span>
                                    <span v-else class="rm-num-cell">
                                        C4 {{ formatPct(row.category_evidence?.cat4) }} · C2 {{ formatPct(row.category_evidence?.cat2) }} · C1 {{ formatPct(row.category_evidence?.cat1) }}
                                    </span>
                                </template>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'Recall / Ingest' : '检索 / 导入耗时'" width="124" align="right">
                            <template #header>
                                <span>
                                    {{ isEn ? 'Recall / Ingest' : '检索 / 导入' }}
                                    <el-popover trigger="click" placement="top" :width="320" popper-class="rm-help-popper">
                                        <template #reference>
                                            <button type="button" class="tip-q" :aria-label="isEn ? 'Latency semantics' : '耗时语义说明'">?</button>
                                        </template>
                                        <div class="rm-help-body">
                                            <p v-if="isEn">Per-question retrieval latency (mean / P95) and corpus ingest + index build time recorded for this run.</p>
                                            <p v-else>单题检索延迟（均值 / P95）与该场运行落档的语料导入建索引耗时。</p>
                                        </div>
                                    </el-popover>
                                </span>
                            </template>
                            <template #default="{ row }">
                                <span v-if="(row.avg_recall_ms || 0) > 0" class="rm-num-cell">{{ Math.round(row.avg_recall_ms) }}<template v-if="(row.p95_recall_ms || 0) > 0">/{{ Math.round(row.p95_recall_ms) }}</template>ms</span>
                                <span v-else class="rm-muted">—</span>
                                <div v-if="(row.ingest_ms || 0) > 0" class="rm-muted" style="font-size: 11px">{{ isEn ? 'ingest' : '导入' }} {{ formatDurationMS(row.ingest_ms) }}</div>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.rolling-memory.eval.colAction')" width="150" align="center" fixed="right">
                            <template #default="{ row }">
                                <el-button size="small" type="primary" link @click.stop="inspectRunDetails(row)">
                                    {{ t('plugin.rolling-memory.eval.inspectRunBtn') }}
                                </el-button>
                                <el-button v-if="!row.is_baseline" size="small" type="danger" link @click.stop="deleteRunRecord(row)">
                                    {{ isEn ? 'Delete' : '删除' }}
                                </el-button>
                            </template>
                        </el-table-column>
                    </el-table>
                </el-tab-pane>

                <el-tab-pane
                    :label="isEn ? '3. Per-Question Diagnosis' : '3. 逐题诊断与错题剖析'"
                    name="detail"
                >
                    <div class="rm-detail-context">
                        <el-button link type="primary" @click="activeTab = 'results'">← {{ isEn ? 'Back to Score Report' : '返回成绩报告' }}</el-button>
                        <span v-if="detailRecord">{{ loc(detailRecord.label, detailRecord.label_en) }} · {{ detailRecord.arm }}</span>
                        <el-button v-if="detailRecord && detailRecord.judge_scored === 0" size="small" type="warning" plain style="margin-left: auto" @click="guideToLLMEval">
                            {{ isEn ? 'Enable LLM Answer + Judge for this set →' : '为该题集开启大模型问答评分 →' }}
                        </el-button>
                    </div>
                    <div class="rm-q-filter">
                        <el-select
                            v-model="detailRunId"
                            size="small"
                            style="width: 260px"
                            @change="onDetailRunSelect"
                        >
                            <el-option
                                v-for="r in allLedgerRows"
                                :key="r.id"
                                :label="`${loc(r.label, r.label_en)} (${tierOptionTag(r.tier)})`"
                                :value="r.id"
                            />
                        </el-select>
                        <el-radio-group v-model="detailFilter.verdict" size="small">
                            <el-radio-button value="all">
                                {{ isEn ? 'All' : '全部' }} ({{ detailItems.length }})
                            </el-radio-button>
                            <el-radio-button value="ev_hit">
                                🎯 Ev Hit ({{ detailStats.evHit }})
                            </el-radio-button>
                            <el-radio-button value="ev_miss">
                                ⚠️ Ev Miss ({{ detailStats.evMiss }})
                            </el-radio-button>
                            <el-radio-button value="correct">
                                ✓ CORRECT ({{ detailStats.correct }})
                            </el-radio-button>
                            <el-radio-button value="wrong">
                                ✗ WRONG ({{ detailStats.wrong }})
                            </el-radio-button>
                        </el-radio-group>
                        <el-select
                            v-model="detailFilter.category"
                            size="small"
                            clearable
                            :placeholder="t('plugin.rolling-memory.eval.filterCatPlaceholder')"
                            style="width: 195px"
                        >
                            <template v-if="query.suite === 'atm'">
                                <el-option :label="isEn ? 'Cat 1 · list_recall (Jaccard)' : 'Cat 1 · 证据ID列表 (list_recall)'" value="1" />
                                <el-option :label="isEn ? 'Cat 2 · number (Deterministic EM)' : 'Cat 2 · 数值与日期 (number)'" value="2" />
                                <el-option :label="isEn ? 'Cat 3 · open_end (LLM Judge)' : 'Cat 3 · 开放问答 (open_end)'" value="3" />
                            </template>
                            <template v-else>
                                <el-option :label="t('plugin.rolling-memory.eval.cat4Label')" value="4" />
                                <el-option :label="t('plugin.rolling-memory.eval.cat2Label')" value="2" />
                                <el-option :label="t('plugin.rolling-memory.eval.cat1Label')" value="1" />
                                <el-option :label="t('plugin.rolling-memory.eval.cat3Label')" value="3" />
                            </template>
                        </el-select>
                        <el-input
                            v-model="detailFilter.keyword"
                            size="small"
                            clearable
                            :placeholder="t('plugin.rolling-memory.eval.filterKwPlaceholder')"
                            style="width: 220px"
                        />
                    </div>

                    <!-- 错题根因四桶过滤器：纯数值/枚举判据（阈值与后端 eval.go 严格对齐），严禁解析判词自然语言 -->
                    <div class="rm-q-filter">
                        <span class="rm-q-filter-lbl">{{ isEn ? 'Root Cause' : '根因分桶' }}:</span>
                        <el-radio-group v-model="detailFilter.rootCause" size="small">
                            <el-radio-button value="all">
                                {{ isEn ? 'All Buckets' : '全部桶' }} ({{ detailItems.length }})
                            </el-radio-button>
                            <el-radio-button value="RECALL_MISS">
                                RECALL_MISS {{ isEn ? '· recall miss' : '· 检索未命中' }} ({{ detailStats.recallMiss }})
                            </el-radio-button>
                            <el-radio-button value="RANK_CUTOFF">
                                RANK_CUTOFF {{ isEn ? '· rank/cap cutoff' : '· 预算截断' }} ({{ detailStats.rankCutoff }})
                            </el-radio-button>
                            <el-radio-button value="READER_GAP">
                                READER_GAP {{ isEn ? '· reader gap' : '· 答题漏读' }} ({{ detailStats.readerGap }})
                            </el-radio-button>
                            <el-radio-button value="JUDGE_AMBIGUITY">
                                JUDGE_AMBIGUITY ({{ detailStats.judgeAmbiguity }})
                            </el-radio-button>
                            <el-radio-button value="HIT">
                                ✓ HIT ({{ detailStats.hit }})
                            </el-radio-button>
                        </el-radio-group>
                        <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                            <template #reference>
                                <button type="button" class="tip-q" :aria-label="isEn ? 'RANK_CUTOFF bucket semantics' : 'RANK_CUTOFF 分桶语义说明'">?</button>
                            </template>
                            <div class="rm-help-body">
                                <p>{{ rankCutoffNote }}</p>
                            </div>
                        </el-popover>
                        <el-tag size="small" type="info" effect="plain" style="margin-left: auto">
                            ⌨️ {{ isEn ? 'Keys: j/k prev/next · r rerun · c copy CLI · Esc' : '快捷键: j/k 上下题 · r 重跑 · c 复制CLI · Esc' }}
                        </el-tag>
                        <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                            <template #reference>
                                <button type="button" class="tip-q" :aria-label="isEn ? 'Keyboard shortcuts semantics' : '快捷键语义说明'">?</button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">Keyboard shortcuts (when not typing in an input): j / k = Next / Previous question · r = Re-run SDK retrieval with Answer/Judge off · c = Copy Repro CLI · Esc = Close drawer</p>
                                <p v-else>程序员快捷键（非输入框焦点时生效）：j / k = 下一题 / 上一题 · r = 关闭 Answer/Judge 重跑 SDK 检索 · c = 复制复现 CLI · Esc = 关闭抽屉</p>
                            </div>
                        </el-popover>
                    </div>

                    <!-- Run A vs Run B 回归对比：按 item.id 左连接两场运行的逐题明细 -->
                    <div class="rm-q-filter">
                        <span class="rm-q-filter-lbl">{{ isEn ? 'Run A (base)' : 'Run A（基线）' }}:</span>
                        <el-select v-model="compareRunId" size="small" clearable :placeholder="isEn ? 'Pick another run to diff' : '选择另一场运行做逐题对比'" style="width: 265px">
                            <el-option
                                v-for="r in allLedgerRows"
                                :key="r.id"
                                :label="`${loc(r.label, r.label_en)} (${tierOptionTag(r.tier)})${r.is_baseline ? (isEn ? ' · baseline (no per-question detail)' : ' · 基线（无逐题明细）') : ''}`"
                                :value="r.id"
                            />
                        </el-select>
                        <template v-if="compareRunId">
                            <el-radio-group v-model="detailFilter.diff" size="small">
                                <el-radio-button value="all">{{ isEn ? 'All Δ' : '全部 Δ' }} ({{ detailItems.length }})</el-radio-button>
                                <el-radio-button value="regressed">
                                    ↘ Regressed {{ isEn ? '(right → wrong)' : '（原对→现错）' }} ({{ diffStats.regressed }})
                                </el-radio-button>
                                <el-radio-button value="fixed">
                                    ↗ Fixed {{ isEn ? '(wrong → right)' : '（原错→现对）' }} ({{ diffStats.fixed }})
                                </el-radio-button>
                            </el-radio-group>
                            <span class="rm-diff-summary">{{ diffSummaryText }}</span>
                            <span v-if="loadingCompare" class="rm-diff-summary is-loading">
                                {{ compareLoadingText }}
                            </span>
                        </template>
                    </div>

                    <el-alert
                        v-if="syntheticDetailWarning !== ''"
                        :title="syntheticDetailWarning"
                        type="warning"
                        show-icon
                        :closable="false"
                        class="rm-synth-alert"
                    />

                    <el-alert
                        v-if="!detailAvailable"
                        :title="detailReason || (isEn ? 'No per-question detail for this run' : '该运行没有逐题明细')"
                        type="info"
                        show-icon
                        :closable="false"
                        class="rm-synth-alert"
                    />

                    <el-alert
                        v-if="compareRunId && !compareAvailable"
                        :title="
                            (isEn ? 'Run A has no per-question detail: ' : 'Run A 没有逐题明细：') +
                            (compareReason || (isEn ? 'Δ comparison is unavailable for this run.' : '该运行不能做逐题 Δ 对比。'))
                        "
                        type="warning"
                        show-icon
                        :closable="false"
                        class="rm-synth-alert"
                    />

                    <!-- 本场运行的后端落档复现信息（repro_cli / 档位声明 / 五桶计数，eval.go:136-143） -->
                    <div v-if="detailRecord && (detailReproCli || backendBucketCounts)" class="rm-repro-row">
                        <span class="rm-q-filter-lbl">{{ isEn ? 'Recorded run' : '本场落档' }}:</span>
                        <template v-if="detailReproCli">
                            <code class="rm-repro-code">{{ detailReproCli }}</code>
                            <el-button size="small" type="primary" plain @click="copyText(detailReproCli, 'Copied recorded repro', '已复制实测复现命令')">
                                <Icon name="el-document-copy" :size="13" style="margin-right: 4px" />
                                {{ isEn ? 'Copy' : '复制' }}
                            </el-button>
                        </template>
                        <span v-if="backendBucketCounts" class="rm-diff-summary">{{ bucketSummaryText }}</span>
                    </div>

                    <el-table v-loading="loadingDetails" :element-loading-text="detailsLoadingText" :data="pagedDetailItems" size="small" border stripe class="ag-data-table rm-roomy-table" header-cell-class-name="table-header-cell" max-height="520">
                        <el-table-column prop="index" label="#" width="55" align="center" />
                        <el-table-column prop="id" label="ID" width="115" />
                        <el-table-column prop="category" :label="t('plugin.rolling-memory.eval.colCategory')" width="85" align="center">
                            <template #default="{ row }">
                                <el-tag size="small" type="info">Cat {{ row.category }}</el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.rolling-memory.eval.colQuestion')" min-width="220">
                            <template #default="{ row }">
                                <div class="rm-cell-wrap">{{ row.question }}</div>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.rolling-memory.eval.colGold')" min-width="150">
                            <template #default="{ row }">
                                <div class="rm-cell-wrap"><strong>{{ row.gold }}</strong></div>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'R@10 / R@50' : 'R@10 / R@50'" width="145" align="center">
                            <template #default="{ row }">
                                <el-tag size="small" :type="row.evidence_hit ? 'primary' : 'danger'">
                                    R@10 {{ Math.round(((row.r10 ?? row.best_evidence_recall) || 0) * 100) }}% · R@50 {{ Math.round(((row.r50 ?? row.best_evidence_recall) || 0) * 100) }}%
                                </el-tag>
                                <div v-if="(row.recall_ms || 0) > 0" class="rm-muted" style="font-size: 11px; margin-top: 2px">
                                    {{ isEn ? 'Recall' : '检索' }} {{ row.recall_ms }}ms
                                </div>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'Root Cause' : '根因诊断'" width="140" align="center">
                            <template #header>
                                <span>
                                    {{ isEn ? 'Root Cause' : '根因诊断' }}
                                    <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                                        <template #reference>
                                            <button type="button" class="tip-q" :aria-label="isEn ? 'Root cause diagnosis semantics' : '根因诊断语义说明'">?</button>
                                        </template>
                                        <div class="rm-help-body">
                                            <p>{{ rootCauseNote }}</p>
                                        </div>
                                    </el-popover>
                                </span>
                            </template>
                            <template #default="{ row }">
                                <el-tag size="small" effect="plain" :type="rootCauseTagType(rootCauseOf(row))">
                                    {{ rootCauseOf(row) }}
                                </el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="isEn ? 'AI Prediction & Judge Verdict' : 'AI 回答 & Judge 判词'" min-width="260">
                            <template #default="{ row }">
                                <div v-if="row.judge || row.prediction" class="rm-cell-wrap">
                                    <div>
                                        <el-tag
                                            v-if="row.judge"
                                            size="small"
                                            :type="row.judge === 'correct' ? 'success' : row.judge === 'wrong' ? 'danger' : 'warning'"
                                            style="margin-right: 6px"
                                        >
                                            {{ row.judge.toUpperCase() }}
                                        </el-tag>
                                        <span>{{ row.prediction || '-' }}</span>
                                    </div>
                                    <div v-if="row.judge_reason" class="rm-muted" style="font-size: 11.5px; margin-top: 3px">
                                        Judge: {{ row.judge_reason }}
                                    </div>
                                </div>
                                <span v-else class="rm-muted">{{ t('plugin.rolling-memory.eval.noJudgeText') }}</span>
                            </template>
                        </el-table-column>
                        <el-table-column v-if="compareRunId" :label="isEn ? 'Δ vs Run A' : 'Δ 对比 Run A'" width="180">
                            <template #header>
                                <span>
                                    {{ isEn ? 'Δ vs Run A' : 'Δ 对比 Run A' }}
                                    <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                                        <template #reference>
                                            <button type="button" class="tip-q" :aria-label="isEn ? 'Regression delta semantics' : '对比变化语义说明'">?</button>
                                        </template>
                                        <div class="rm-help-body">
                                            <p>{{ regressionNote }}</p>
                                        </div>
                                    </el-popover>
                                </span>
                            </template>
                            <template #default="{ row }">
                                <div v-if="compareItemOf(row)" class="rm-cell-wrap rm-delta-cell">
                                    <el-tag size="small" effect="plain" :type="diffTagType(diffKindOf(row))">
                                        {{ diffKindLabel(diffKindOf(row)) }}
                                    </el-tag>
                                    <span class="rm-mono">
                                        Ev {{ compareItemOf(row)?.evidence_hit ? 'HIT' : 'MISS' }} → {{ row.evidence_hit ? 'HIT' : 'MISS' }}
                                    </span>
                                    <span class="rm-mono">ΔF1 {{ formatDelta(row.f1 - (compareItemOf(row)?.f1 || 0)) }}</span>
                                </div>
                                <span v-else class="rm-muted">{{ isEn ? 'absent in Run A' : 'Run A 未含该题' }}</span>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.rolling-memory.eval.colAction')" width="120" align="center" fixed="right">
                            <template #default="{ row }">
                                <el-button size="small" type="primary" link @click="openQuestionResultProbe(row)">
                                    {{ t('plugin.rolling-memory.eval.probeBtn') }}
                                </el-button>
                            </template>
                        </el-table-column>
                    </el-table>
                    <div v-if="filteredDetailItems.length > detailPageSize" class="rm-pager" style="margin-top: 10px; display: flex; justify-content: flex-end">
                        <el-pagination
                            v-model:current-page="detailPage"
                            v-model:page-size="detailPageSize"
                            :page-sizes="[25, 50, 100, 200]"
                            :total="filteredDetailItems.length"
                            layout="total, sizes, prev, pager, next"
                            size="small"
                            background
                        />
                    </div>
                </el-tab-pane>

                <el-tab-pane :label="`${isEn ? '4. Question Set' : '4. 题库浏览'} (${tierOptionTag(query.tier)})`" name="questions">
                    <el-alert
                        v-if="overview.question_error"
                        type="error"
                        show-icon
                        :closable="false"
                        :title="isEn ? 'Questions cannot be loaded' : '题目无法加载'"
                        :description="overview.question_error"
                        class="rm-question-error"
                    />
                    <div class="rm-q-filter">
                        <el-select
                            v-model="query.category"
                            size="small"
                            clearable
                            :placeholder="t('plugin.rolling-memory.eval.filterCatPlaceholder')"
                            style="width: 220px"
                            @change="onFilterChange"
                        >
                            <template v-if="query.suite === 'atm'">
                                <el-option :label="isEn ? 'Cat 1 · list_recall (Jaccard)' : 'Cat 1 · 证据ID列表 (list_recall)'" value="1" />
                                <el-option :label="isEn ? 'Cat 2 · number (Deterministic EM)' : 'Cat 2 · 数值与日期 (number)'" value="2" />
                                <el-option :label="isEn ? 'Cat 3 · open_end (LLM Judge)' : 'Cat 3 · 开放问答 (open_end)'" value="3" />
                            </template>
                            <template v-else>
                                <el-option :label="t('plugin.rolling-memory.eval.cat4Label')" value="4" />
                                <el-option :label="t('plugin.rolling-memory.eval.cat2Label')" value="2" />
                                <el-option :label="t('plugin.rolling-memory.eval.cat1Label')" value="1" />
                                <el-option :label="t('plugin.rolling-memory.eval.cat3Label')" value="3" />
                            </template>
                        </el-select>
                        <el-input
                            v-model="query.keyword"
                            size="small"
                            clearable
                            :placeholder="t('plugin.rolling-memory.eval.filterKwPlaceholder')"
                            style="width: 280px"
                            @keyup.enter="onFilterChange"
                            @clear="onFilterChange"
                        />
                    </div>
                    <el-table v-loading="loading" :element-loading-text="evalTableLoadingText" :data="overview.questions || []" size="small" border stripe class="ag-data-table rm-roomy-table" header-cell-class-name="table-header-cell">
                        <el-table-column prop="id" label="ID" width="125" />
                        <el-table-column prop="category" :label="t('plugin.rolling-memory.eval.colCategory')" width="95" align="center">
                            <template #default="{ row }">
                                <el-tag size="small" type="info">Cat {{ row.category }}</el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.rolling-memory.eval.colQuestion')" min-width="250">
                            <template #default="{ row }">
                                <div class="rm-cell-wrap">{{ row.question }}</div>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.rolling-memory.eval.colGold')" min-width="180">
                            <template #default="{ row }">
                                <div class="rm-cell-wrap"><strong>{{ row.answer }}</strong></div>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.rolling-memory.eval.colEvidence')" min-width="260">
                            <template #default="{ row }">
                                <MemoryHoverPreview
                                    :text="(row.evidence_texts || []).join('\n\n') || t('plugin.rolling-memory.eval.poolEvidenceHint')"
                                    :question="row.question"
                                    :title="row.id"
                                    :subtitle="`Cat ${row.category || ''}`"
                                    :hint="{ layer: 'episodic' }"
                                >
                                    <div class="rm-cell-wrap">
                                        {{ (row.evidence_texts || []).join(' | ') || t('plugin.rolling-memory.eval.poolEvidenceHint') }}
                                    </div>
                                </MemoryHoverPreview>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.rolling-memory.eval.colAction')" width="130" align="center" fixed="right">
                            <template #default="{ row }">
                                <el-button size="small" type="primary" link @click="probeQuestion(row)">
                                    {{ t('plugin.rolling-memory.eval.probeBtn') }}
                                </el-button>
                            </template>
                        </el-table-column>
                    </el-table>
                    <div class="rm-pagination">
                        <el-pagination
                            v-model:current-page="query.page"
                            v-model:page-size="query.page_size"
                            :total="overview.total_questions || 0"
                            :page-sizes="[10, 20, 50, 100]"
                            layout="total, sizes, prev, pager, next"
                            small
                            @size-change="loadOverview"
                            @current-change="loadOverview"
                        />
                    </div>
                </el-tab-pane>

                <el-tab-pane :label="isEn ? '5. LLM & Protocol' : '5. 模型与协议'" name="protocol">
                    <div class="rm-proto-sec">
                        <div class="rm-proto-hdr">
                            <div class="rm-agent-title-wrap">
                                <strong>{{ t('plugin.rolling-memory.eval.protoSec1Title') }}</strong>
                                <el-tag size="small" :type="overview.ai_check?.ready ? 'success' : 'warning'">
                                    {{ overview.ai_check?.ready ? t('plugin.rolling-memory.eval.aiReadyTag') : t('plugin.rolling-memory.eval.aiNotReadyTag') }}
                                </el-tag>
                            </div>
                            <div class="rm-eval-actions">
                                <el-button size="small" plain @click="fillModelPreset('deepseek')">DeepSeek</el-button>
                                <el-button size="small" plain @click="fillModelPreset('openai')">OpenAI GPT-4.1</el-button>
                                <el-button size="small" plain @click="fillModelPreset('ollama')">Local Ollama</el-button>
                                <el-button v-blur size="small" type="success" plain :loading="testingAI" @click="runAIConnectionTest">
                                    {{ t('plugin.rolling-memory.eval.testConnBtn') }}
                                    <template v-if="testingAI"> ({{ testAIPct }}%)</template>
                                </el-button>
                                <el-button v-blur size="small" type="primary" :loading="savingAI" @click="saveAIConfig">
                                    {{ t('plugin.rolling-memory.eval.saveModelBtn') }}
                                    <template v-if="savingAI"> ({{ saveAIPct }}%)</template>
                                </el-button>
                            </div>
                        </div>

                        <div v-if="testingAI" class="rm-ingest-progress-box" style="margin-bottom: 10px">
                            <div class="rm-ipb-top">
                                <strong>{{ isEn ? 'Testing Dual-Agent (Answer + Judge) protocol handshake...' : '正在测试 Answer + Judge 双智能体协议连通性…' }}</strong>
                                <span>{{ testAIPct }}%</span>
                            </div>
                            <el-progress :percentage="testAIPct" :stroke-width="6" striped striped-flow :show-text="false" />
                        </div>

                        <div v-if="aiTestResult" class="rm-ai-test-banner" :class="aiTestResult.success ? 'is-ok' : 'is-err'">
                            <div class="rm-ai-test-top">
                                <strong>{{ loc(aiTestResult.message, aiTestResult.message_en) }}</strong>
                                <span v-if="aiTestResult.total_ms">Total {{ aiTestResult.total_ms }}ms</span>
                            </div>
                            <div v-if="aiTestResult.answer_output" class="rm-ai-test-sub">
                                Answer ({{ aiTestResult.answer_ms }}ms): <code>{{ aiTestResult.answer_output }}</code>
                                <template v-if="aiTestResult.judge_label">
                                    · Judge ({{ aiTestResult.judge_ms }}ms): <strong>{{ aiTestResult.judge_label }}</strong>
                                    ({{ aiTestResult.judge_reason }})
                                </template>
                            </div>
                        </div>
                        <el-form :model="aiForm" label-width="165px" size="small">
                            <el-row :gutter="16">
                                <el-col :xs="24" :md="12">
                                    <el-form-item :label="t('plugin.rolling-memory.eval.lblBaseUrl')">
                                        <el-input v-model="aiForm.base_url" placeholder="https://api.deepseek.com/v1 or http://127.0.0.1:11434/v1" />
                                    </el-form-item>
                                </el-col>
                                <el-col :xs="24" :md="12">
                                    <el-form-item :label="t('plugin.rolling-memory.eval.lblApiKey')">
                                        <el-input
                                            v-model="aiForm.api_key"
                                            type="password"
                                            show-password
                                            :placeholder="aiForm.has_api_key ? t('plugin.rolling-memory.eval.keyConfiguredHint') : t('plugin.rolling-memory.eval.keyEmptyHint')"
                                        />
                                    </el-form-item>
                                </el-col>
                                <el-col :xs="24" :md="8">
                                    <el-form-item :label="t('plugin.rolling-memory.eval.lblAnswerModel')">
                                        <el-input v-model="aiForm.answer_model" placeholder="deepseek-chat / deepseek-v4.1-flash" />
                                    </el-form-item>
                                </el-col>
                                <el-col :xs="24" :md="8">
                                    <el-form-item :label="t('plugin.rolling-memory.eval.lblJudgeModel')">
                                        <el-input v-model="aiForm.judge_model" placeholder="deepseek-chat / deepseek-v4.1-flash" />
                                    </el-form-item>
                                </el-col>
                                <el-col :xs="24" :md="4">
                                    <el-form-item :label="t('plugin.rolling-memory.eval.lblMaxTokens')">
                                        <el-input-number v-model="aiForm.max_tokens" :min="32768" :max="131072" :step="1024" style="width: 100%" />
                                    </el-form-item>
                                </el-col>
                                <el-col :xs="24" :md="4">
                                    <el-form-item :label="t('plugin.rolling-memory.eval.lblTimeout')">
                                        <el-input-number v-model="aiForm.timeout_sec" :min="180" :max="600" :step="30" style="width: 100%" />
                                    </el-form-item>
                                </el-col>
                            </el-row>
                        </el-form>
                    </div>

                    <div class="rm-proto-sec">
                        <div class="rm-proto-hdr">
                            <strong>
                                {{
                                    query.suite === 'atm'
                                        ? isEn
                                            ? '2. Cambridge ATM-Bench Official Frozen Protocol Pair (atmbench-official-v1 · 2 Read-Only SHA1 Pins)'
                                            : '2. 剑桥 ATM-Bench 官方冻结协议对（atmbench-official-v1 · 两枚 SHA1 钉只读保护）'
                                        : t('plugin.rolling-memory.eval.protoSec2Title')
                                }}
                            </strong>
                        </div>
                        <el-row :gutter="14">
                            <el-col v-for="ag in overview.agents || []" :key="ag.code" :xs="24" :xl="12" class="rm-col-stretch">
                                <el-card shadow="never" class="rm-agent-card">
                                    <template #header>
                                        <div class="rm-agent-hdr">
                                            <div class="rm-agent-title-wrap">
                                                <strong>{{ loc(ag.name, ag.name_en) }}</strong>
                                                <el-tag size="small" type="info">{{ ag.code }}</el-tag>
                                            </div>
                                            <el-tag size="small" :type="ag.pin_verified ? 'success' : 'danger'">
                                                {{ ag.pin_verified ? t('plugin.rolling-memory.eval.pinVerifiedTag') : t('plugin.rolling-memory.eval.pinDriftTag') }}
                                            </el-tag>
                                        </div>
                                    </template>
                                    <el-descriptions :column="1" border size="small">
                                        <el-descriptions-item :label="t('plugin.rolling-memory.eval.lblRole')">{{ ag.role }}</el-descriptions-item>
                                        <el-descriptions-item :label="t('plugin.rolling-memory.eval.lblBoundModel')">{{ ag.model }}</el-descriptions-item>
                                        <el-descriptions-item :label="t('plugin.rolling-memory.eval.lblProtocol')">
                                            <code>{{ query.suite === 'atm' ? `atmbench-official (${ag.protocol})` : `locomo-backboard (${ag.protocol})` }}</code>
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
                        <template v-if="query.suite === 'atm'">
                            <div class="rm-proto-sec-title" style="margin-top: 16px">
                                <strong>{{ isEn ? '3. Official scoring protocol & SHA-256 pins' : '3. 官方判分协议与 SHA-256 钉' }}</strong>
                            </div>
                            <el-descriptions :column="2" border size="small">
                                <el-descriptions-item :label="isEn ? 'Number scorer' : 'Number 判分'">
                                    <span>{{ overview.protocol_spec?.number_scorer || 'official deterministic_accuracy' }}</span>
                                    <br /><code class="rm-code-inline">{{ overview.protocol_spec?.number_scorer_version || '-' }}</code>
                                </el-descriptions-item>
                                <el-descriptions-item :label="isEn ? 'List scorer' : 'List 判分'">
                                    <span>{{ overview.protocol_spec?.list_scorer || 'official list_jaccard_score' }}</span>
                                    <br /><code class="rm-code-inline">{{ overview.protocol_spec?.list_scorer_version || '-' }}</code>
                                </el-descriptions-item>
                                <el-descriptions-item :label="isEn ? 'Judge parser' : '判官解析'" :span="2">
                                    <span>{{ overview.protocol_spec?.judge_parser || 'official parse_judge_response' }}</span>
                                </el-descriptions-item>
                                <el-descriptions-item :label="isEn ? 'Scoring fixture SHA-256' : '判分 fixture SHA-256'" :span="2">
                                    <code class="rm-sha-code">{{ overview.protocol_spec?.scoring_fixture_sha256 || '-' }}</code>
                                </el-descriptions-item>
                                <el-descriptions-item :label="isEn ? 'Answer user prompt SHA-1' : '答题 user 模板 SHA-1'" :span="2">
                                    <code class="rm-sha-code">{{ overview.protocol_spec?.answer_payload_sha1 || '-' }}</code>
                                </el-descriptions-item>
                                <el-descriptions-item :label="isEn ? 'Official alignment' : '官方对齐'">
                                    <el-tag size="small" :type="overview.protocol_spec?.official_scoring_aligned ? 'success' : 'danger'">
                                        {{ overview.protocol_spec?.official_scoring_aligned ? (isEn ? 'ALIGNED' : '已对齐') : (isEn ? 'DRIFT' : '未对齐') }}
                                    </el-tag>
                                    <el-tag size="small" style="margin-left: 8px" :type="overview.protocol_spec?.all_pins_verified ? 'success' : 'danger'">
                                        {{ overview.protocol_spec?.all_pins_verified ? (isEn ? 'PIN GATE PASS' : '钉门禁通过') : (isEn ? 'PIN GATE FAIL' : '钉门禁失败') }}
                                    </el-tag>
                                </el-descriptions-item>
                                <el-descriptions-item :label="isEn ? 'Recall metric' : '召回口径'">
                                    <span>{{ loc(overview.protocol_spec?.recall_metric_spec, overview.protocol_spec?.recall_metric_spec_en) }}</span>
                                </el-descriptions-item>
                                <el-descriptions-item :label="isEn ? 'Corpus / question-tier pins' : '语料 / 题集钉'" :span="2">
                                    <div v-for="(value, name) in (overview.protocol_spec?.corpus_pins || {})" :key="'eval-corpus-pin-' + name">
                                        <code class="rm-code-inline">{{ name }}</code>
                                        <code class="rm-sha-code">{{ value }}</code>
                                    </div>
                                </el-descriptions-item>
                            </el-descriptions>
                        </template>
                    </div>
                </el-tab-pane>
            </el-tabs>
        </el-card>

        <el-drawer
            v-model="probeVisible"
            :title="isEn ? 'Single-question retrieval debugger' : '单题检索调试'"
            size="min(92vw, 1080px)"
            class="rm-probe-drawer"
        >
            <div v-if="probeTarget" class="rm-probe-body">
                <el-alert
                    type="info"
                    show-icon
                    :closable="false"
                    :title="isEn ? 'Saved result and new probe are separate' : '已保存结果与新的实时探针相互独立'"
                    :description="isEn ? `Running a probe uses the current draft: ${evalArmLabel}, Top K ${probeTopK}, ${evalScopeText}. It does not overwrite the recorded run.` : `点击运行将使用当前草稿配置：${evalArmLabel} · Top K ${probeTopK} · ${evalScopeText}；不会覆盖历史运行。`"
                    class="rm-question-error"
                />
                <div class="rm-probe-toolbar">
                    <div class="rm-agent-title-wrap">
                        <el-tag size="small" type="info">{{ probeTarget.id || 'custom' }} · Cat {{ probeTarget.category || '4' }}</el-tag>
                        <el-select v-model="probeTopK" size="small" style="width: 122px">
                            <el-option label="Top K 5" :value="5" />
                            <el-option label="Top K 10" :value="10" />
                            <el-option label="Top K 20" :value="20" />
                            <el-option label="Top K 40" :value="40" />
                            <el-option label="Top K 50" :value="50" />
                        </el-select>
                    </div>
                    <div class="rm-eval-actions">
                        <el-button size="small" plain :disabled="filteredDetailItems.length === 0" @click="stepProbeQuestion(-1)">
                            ◀ {{ isEn ? 'Prev (k)' : '上一题 (k)' }}
                        </el-button>
                        <el-button size="small" plain :disabled="filteredDetailItems.length === 0" @click="stepProbeQuestion(1)">
                            {{ isEn ? 'Next (j)' : '下一题 (j)' }} ▶
                        </el-button>
                        <el-button size="small" type="primary" :loading="probing && !probeWithAgents" @click="executeProbe(false)">
                            {{
                                evalConfig.arm_mode === 'oracle'
                                    ? isEn
                                        ? 'Run Oracle gold probe (Answer/Judge off)'
                                        : '运行 Oracle 金标探针（关闭 Answer/Judge）'
                                    : isEn
                                      ? 'Run SDK retrieval (Answer/Judge off)'
                                      : '运行 SDK 检索（关闭 Answer/Judge）'
                            }} (r)<template v-if="probing && !probeWithAgents"> · {{ probePct }}%</template>
                        </el-button>
                        <el-button size="small" type="success" :loading="probing && probeWithAgents" @click="executeProbe(true)">
                            {{ t('plugin.rolling-memory.eval.probeRunFullBtn') }}<template v-if="probing && probeWithAgents"> · {{ probePct }}%</template>
                        </el-button>
                    </div>
                </div>
                <div v-if="probing" class="rm-ingest-progress-box" style="margin-top: 8px">
                    <div class="rm-ipb-top">
                        <strong>{{ probeLoadingText }}</strong>
                        <span>{{ probePct }}%</span>
                    </div>
                    <el-progress :percentage="probePct" :stroke-width="6" striped striped-flow :show-text="false" />
                </div>

                <el-form label-width="110px" size="small" style="margin-top: 10px">
                    <el-form-item :label="t('plugin.rolling-memory.eval.colQuestion')">
                        <el-input v-model="probeTarget.question" type="textarea" :rows="2" />
                    </el-form-item>
                    <el-form-item :label="t('plugin.rolling-memory.eval.colGold')">
                        <el-input v-model="probeTarget.answer" />
                    </el-form-item>
                </el-form>

                <div v-if="probeItem" class="rm-probe-metrics">
                    <div class="rm-live-kpis" style="margin: 8px 0">
                        <div class="rm-live-kpi-box">
                            <span class="lbl">{{ t('plugin.rolling-memory.eval.colEv') }}</span>
                            <strong class="val" :class="probeItem.evidence_hit ? 'is-ev' : ''">{{ probeItem.evidence_hit ? 'HIT' : 'MISS' }} ({{ Math.round((probeItem.best_evidence_recall || 0) * 100) }}%)</strong>
                            <span class="sub-cats">{{ isEn ? 'threshold' : '阈值' }} ≥ {{ EVIDENCE_HIT_RECALL_THRESHOLD }}</span>
                        </div>
                        <div class="rm-live-kpi-box">
                            <span class="lbl">{{ t('plugin.rolling-memory.eval.colAic') }}</span>
                            <strong class="val">{{ probeItem.answer_in_context ? 'YES' : 'NO' }}</strong>
                            <span class="sub-cats">{{ Math.round((probeItem.gold_token_recall || 0) * 100) }}%</span>
                        </div>
                        <div class="rm-live-kpi-box">
                            <span class="lbl">{{ isEn ? 'Evidence hits' : '证据命中' }}</span>
                            <strong class="val is-ev">{{ probeCloudHitCount }}</strong>
                            <span class="sub-cats">Top K {{ probeTopK }}</span>
                        </div>
                        <div class="rm-live-kpi-box">
                            <span class="lbl">{{ isEn ? 'Root Cause' : '根因诊断' }}</span>
                            <el-tag size="small" effect="plain" :type="rootCauseTagType(rootCauseOf(probeItem))">{{ rootCauseOf(probeItem) }}</el-tag>
                        </div>
                        <div class="rm-live-kpi-box">
                            <span class="lbl">Latency</span><strong class="val">{{ probeItem.total_ms }}ms</strong><span class="sub-cats">Recall {{ probeItem.recall_ms }}ms</span>
                        </div>
                    </div>
                </div>

                <div v-if="probeItem || probeRecall" class="rm-provenance-box">
                    <div class="rm-split-title">Engine provenance · recall.meta</div>
                    <el-descriptions :column="2" border size="small">
                        <el-descriptions-item label="engine_transport">{{ probeProvenance.engine_transport || '—' }}</el-descriptions-item>
                        <el-descriptions-item label="engine_upstream">{{ probeProvenance.engine_upstream || '—' }}</el-descriptions-item>
                        <el-descriptions-item label="engine_scope">{{ probeProvenance.engine_scope || '—' }}</el-descriptions-item>
                        <el-descriptions-item label="cloud_hits">{{ probeProvenance.cloud_hits ?? '—' }}</el-descriptions-item>
                        <el-descriptions-item label="local_fallback">
                            <span :class="{ 'is-blocked': probeProvenance.local_fallback !== false }">{{ probeProvenance.local_fallback === null ? 'unknown' : probeProvenance.local_fallback ? 'true (blocked)' : 'false' }}</span>
                        </el-descriptions-item>
                        <el-descriptions-item label="store_path">{{ probeProvenance.store_path || '—' }}</el-descriptions-item>
                        <el-descriptions-item label="corpus_sha256" :span="2">{{ probeProvenance.corpus_sha256 || '—' }}</el-descriptions-item>
                    </el-descriptions>
                    <el-alert v-if="probeProvenance.local_fallback === true" type="error" show-icon :closable="false" :title="isEn ? 'This item reports a prohibited local fallback' : '该题报告了不允许的本地回退'" />
                    <el-collapse>
                        <el-collapse-item title="provenance meta JSON" name="provenance"><pre class="rm-probe-pre">{{ probeProvenanceJSON }}</pre></el-collapse-item>
                    </el-collapse>
                </div>

                <template v-if="probeItem">
                    <div class="rm-probe-split">
                        <div class="rm-split-col">
                            <div class="rm-split-title">{{ isEn ? 'Evidence slices' : '证据切片' }} · {{ probeCloudHitCount }}</div>
                            <div class="rm-split-body">
                                <div v-if="recallEvidenceHits.length === 0 && probeEvidenceItems.length === 0" class="rm-muted">{{ isEn ? 'No structured hit payload; inspect provenance and the delivered context below.' : '本次未返回结构化命中载荷；请核查 provenance 与下方交付上下文。' }}</div>
                                <div v-for="(hit, hitIndex) in recallEvidenceHits" :key="hit.unit_id || hit.trace || hitIndex" class="rm-raw-card">
                                    <div class="rm-raw-hdr"><span class="rm-mono">{{ hit.unit_id || hit.trace || `hit-${hitIndex + 1}` }}</span><span>session={{ hit.session ?? '—' }} · turn={{ hit.turn ?? '—' }}</span></div>
                                    <MarkdownPreview :text="hit.text" :hint="{ layer: 'episodic' }" compact />
                                </div>
                                <pre v-if="recallEvidenceHits.length === 0 && probeEvidenceItems.length > 0" class="rm-split-pre">{{ formatEvidenceLines(probeEvidenceItems) }}</pre>
                            </div>
                        </div>
                    </div>

                    <div v-if="probeReproCli" class="rm-repro-row">
                        <span class="rm-q-filter-lbl">Repro CLI:</span><code class="rm-repro-code">{{ probeReproCli }}</code>
                        <el-button size="small" type="primary" plain @click="copyText(probeReproCli, 'Copied item repro', '已复制单题复现命令')"><Icon name="el-document-copy" :size="13" style="margin-right: 4px" />{{ isEn ? 'Copy' : '复制' }}</el-button>
                    </div>
                    <el-alert v-else-if="probeItem.repro_cli" type="info" show-icon :closable="false" :title="isEn ? 'This historical item has no current SDK repro CLI' : '该历史题目没有可展示的现行 SDK 复现命令'" />

                    <div v-if="probeItem.evidence_matches && probeItem.evidence_matches.length > 0" class="rm-ev-match-box">
                        <div class="rm-prompt-lbl">{{ t('plugin.rolling-memory.eval.colEvidence') }}:</div>
                                <div v-for="(em, eIdx) in probeItem.evidence_matches" :key="eIdx" class="rm-ev-match-row"><el-tag size="small" :type="em.hit ? 'success' : 'danger'">{{ Math.round((em.token_recall || 0) * 100) }}%</el-tag><div class="rm-ev-match-txt"><MarkdownPreview :text="em.text" :hint="{ layer: 'episodic' }" compact hide-toolbar /></div></div>
                    </div>

                    <div v-if="probeItem.prediction || probeItem.judge" class="rm-ai-test-banner" :class="probeItem.judge === 'correct' ? 'is-ok' : 'is-err'" style="margin-top: 10px">
                        <div class="rm-ai-test-top"><span><strong>Answer ({{ probeItem.answer_ms }}ms):</strong> {{ probeItem.prediction || '-' }}</span><el-tag v-if="probeItem.judge" size="small" :type="probeItem.judge === 'correct' ? 'success' : 'danger'">{{ probeItem.judge.toUpperCase() }} (F1={{ probeItem.f1 }})</el-tag></div>
                        <div v-if="probeItem.judge_reason" class="rm-ai-test-sub"><strong>Judge ({{ probeItem.judge_ms }}ms):</strong> {{ probeItem.judge_reason }}</div>
                    </div>
                    <div v-else class="rm-probe-llm-cta">
                        <div>
                            <strong>{{ isEn ? 'Want to see how an LLM answers and scores this question?' : '想看大模型在这道题上的真实回答与裁判打分？' }}</strong>
                            <span>{{ isEn ? 'This record only ran local retrieval (0 LLM calls). Click the button on the right to feed the recalled evidence above into your Answer + Judge LLM.' : '当前记录仅执行了本地检索（未调用大模型）。点击右侧按钮即可把上方召回的证据喂给大模型作答并按官方裁判协议打分。' }}</span>
                        </div>
                        <el-button size="small" type="success" :loading="probing && probeWithAgents" @click="executeProbe(true)">
                            {{ isEn ? '🤖 Run LLM Answer + Judge on This Question' : '🤖 对本题执行大模型作答与判分' }}
                        </el-button>
                    </div>

                    <div v-if="probeItem.formatted_items && probeItem.formatted_items.length > 0">
                        <div class="rm-prompt-lbl">{{ isEn ? 'Numbered memory lines delivered to Answer' : '送入 Answer 的编号记忆行' }} ({{ probeItem.formatted_items.length }}):</div>
                        <pre class="rm-prompt-pre" style="max-height: 210px">{{ formatEvidenceLines(probeItem.formatted_items) }}</pre>
                    </div>
                </template>

                <div class="rm-probe-result-title">
                    {{ t('plugin.rolling-memory.eval.lblRealtimeRecall') }} ({{ evalScopeText }}):
                </div>
                <el-collapse v-model="probeJsonPanels">
                    <el-collapse-item
                        :title="
                            probeRecall
                                ? isEn
                                    ? 'SDK recall JSON (structured payload)'
                                    : '原始召回 JSON（结构化载荷）'
                                : isEn
                                  ? 'Flattened context delivered to the answerer'
                                  : '送入答题侧的压平上下文'
                        "
                        name="raw"
                    >
                        <pre v-loading="probing" :element-loading-text="probeLoadingText" class="rm-probe-pre">{{ probeResultText }}</pre>
                    </el-collapse-item>
                </el-collapse>
            </div>
        </el-drawer>

        <!-- 前置拦截 Modal 1：AI 模型通道配置检查与阻断对话框 -->
        <el-dialog
            v-model="aiModalVisible"
            :title="t('plugin.rolling-memory.eval.modalAiBlockTitle')"
            width="680px"
            destroy-on-close
        >
            <div class="rm-modal-body">
                <el-alert
                    :title="
                        aiModalBlockedAction
                            ? t('plugin.rolling-memory.eval.modalAiBlockDesc')
                            : loc(overview.ai_check?.summary, overview.ai_check?.summary_en)
                    "
                    :type="overview.ai_check?.ready ? 'success' : 'warning'"
                    show-icon
                    :closable="false"
                    style="margin-bottom: 12px"
                />

                <div class="rm-check-list">
                    <div v-for="chk in overview.ai_check?.checks || []" :key="chk.key" class="rm-check-row">
                        <el-tag size="small" :type="chk.passed ? 'success' : 'danger'">
                            {{ chk.passed ? '✓ PASS' : '✗ BLOCK' }}
                        </el-tag>
                        <strong class="rm-check-lbl">{{ loc(chk.label, chk.label_en) }}</strong>
                        <span class="rm-check-detail">{{ loc(chk.detail, chk.detail_en) }}</span>
                    </div>
                </div>

                <!-- AI 路由插件（plugins/ai）渠道一键接入：服务端写入生效凭证，明文密钥不出服务端 -->
                <div class="rm-plugin-channels">
                    <div class="rm-plugin-channels-head">
                        <span class="rm-plugin-channels-title">
                            🔌 {{ t('plugin.rolling-memory.eval.pluginChannelsTitle') }}
                        </span>
                        <el-tag
                            size="small"
                            :type="overview.ai_check?.plugin_channels_ok ? 'success' : 'info'"
                        >
                            {{ t('plugin.rolling-memory.eval.pluginChannelsRoutable', { n: pluginChannelRoutable }) }}
                        </el-tag>
                        <el-button link type="primary" size="small" @click="goAIPluginChannels">
                            {{ t('plugin.rolling-memory.eval.pluginChannelsManage') }}
                        </el-button>
                    </div>
                    <div v-if="pluginChannels.length === 0" class="rm-plugin-channels-empty">
                        {{ t('plugin.rolling-memory.eval.pluginChannelsEmpty') }}
                    </div>
                    <div v-else class="rm-plugin-channels-list">
                        <div
                            v-for="opt in pluginChannels"
                            :key="opt.channel_id"
                            class="rm-plugin-channel-card"
                            :class="{ 'is-routable': opt.routable }"
                        >
                            <div class="rm-pc-main">
                                <div class="rm-pc-name">
                                    <el-tag size="small" :type="opt.routable ? 'success' : 'warning'" effect="plain">
                                        {{ opt.routable ? '✓' : '⚠' }}
                                    </el-tag>
                                    <strong>{{ opt.channel_name || '#' + opt.channel_id }}</strong>
                                    <el-tag size="small" type="info" effect="plain">{{ opt.vendor || 'other' }}</el-tag>
                                    <el-tag v-if="opt.has_key" size="small" type="success" effect="plain">
                                        {{ t('plugin.rolling-memory.eval.pluginChannelsKeySet') }}
                                    </el-tag>
                                    <el-tag v-if="opt.circuit && opt.circuit !== 'closed'" size="small" type="danger" effect="plain">
                                        {{ opt.circuit }}
                                    </el-tag>
                                </div>
                                <div class="rm-pc-meta">
                                    <span class="rm-pc-url">{{ opt.base_url || '—' }}</span>
                                    <span v-if="opt.models && opt.models.length" class="rm-pc-models">
                                        {{ t('plugin.rolling-memory.eval.pluginChannelsModels') }}: {{ opt.models.join(' · ') }}
                                    </span>
                                </div>
                            </div>
                            <div class="rm-pc-actions">
                                <el-select
                                    v-if="opt.models && opt.models.length > 1"
                                    v-model="pluginChannelModelPick[opt.channel_id]"
                                    size="small"
                                    style="width: 140px"
                                >
                                    <el-option v-for="m in opt.models" :key="m" :label="m" :value="m" />
                                </el-select>
                                <el-button
                                    size="small"
                                    type="primary"
                                    plain
                                    :disabled="!opt.routable"
                                    :loading="applyingChannelID === opt.channel_id"
                                    @click="applyAIPluginChannel(opt)"
                                >
                                    {{ t('plugin.rolling-memory.eval.pluginChannelsApply') }}
                                </el-button>
                            </div>
                        </div>
                    </div>
                </div>

                <div class="rm-modal-presets">
                    <span>{{ isEn ? 'Quick Fill:' : '一键填充预设：' }}</span>
                    <el-button size="small" plain @click="fillModelPreset('deepseek')">DeepSeek</el-button>
                    <el-button size="small" plain @click="fillModelPreset('openai')">OpenAI GPT-4.1</el-button>
                    <el-button size="small" plain @click="fillModelPreset('ollama')">{{ isEn ? 'Local Ollama (no key)' : 'Local Ollama (免密)' }}</el-button>
                </div>

                <el-form :model="aiForm" label-width="145px" size="small" style="margin-top: 10px">
                    <el-form-item :label="t('plugin.rolling-memory.eval.lblBaseUrl')">
                        <el-input v-model="aiForm.base_url" placeholder="https://api.deepseek.com/v1 or http://127.0.0.1:11434/v1" />
                    </el-form-item>
                    <el-form-item :label="t('plugin.rolling-memory.eval.lblApiKey')">
                        <el-input
                            v-model="aiForm.api_key"
                            type="password"
                            show-password
                            :placeholder="aiForm.has_api_key ? t('plugin.rolling-memory.eval.keyConfiguredHint') : t('plugin.rolling-memory.eval.keyEmptyHint')"
                        />
                    </el-form-item>
                    <el-row :gutter="12">
                        <el-col :span="12">
                            <el-form-item :label="t('plugin.rolling-memory.eval.lblAnswerModel')">
                                <el-input v-model="aiForm.answer_model" placeholder="deepseek-chat / gpt-4.1" />
                            </el-form-item>
                        </el-col>
                        <el-col :span="12">
                            <el-form-item :label="t('plugin.rolling-memory.eval.lblJudgeModel')">
                                <el-input v-model="aiForm.judge_model" placeholder="deepseek-chat / gpt-4.1" />
                            </el-form-item>
                        </el-col>
                    </el-row>
                </el-form>

                <div v-if="aiTestResult" class="rm-ai-test-banner" :class="aiTestResult.success ? 'is-ok' : 'is-err'">
                    <div class="rm-ai-test-top">
                        <strong>{{ loc(aiTestResult.message, aiTestResult.message_en) }}</strong>
                        <span v-if="aiTestResult.total_ms">{{ aiTestResult.total_ms }}ms</span>
                    </div>
                </div>
            </div>
            <template #footer>
                <div class="rm-modal-footer">
                    <el-button
                        v-if="aiModalBlockedAction"
                        size="small"
                        type="info"
                        plain
                        @click="fallbackToRetrievalOnlyEval"
                    >
                            {{ evalConfig.arm_mode === 'oracle' ? (isEn ? 'Continue with Answer/Judge off' : '关闭 Answer/Judge 继续') : evalConfig.arm_mode === 'sdk-local-plain' ? (isEn ? 'Continue with Local Plain retrieval' : '继续本地明文检索') : (isEn ? `Continue with ${evalArmLabel} requests` : `继续 ${evalArmLabel} 请求`) }}
                    </el-button>
                    <div style="margin-left: auto; display: flex; gap: 8px">
                        <el-button size="small" type="success" plain :loading="testingAI" @click="runAIConnectionTest">
                            {{ t('plugin.rolling-memory.eval.testConnBtn') }}
                        </el-button>
                        <el-button size="small" type="primary" :loading="savingAI" @click="saveAIConfigAndResume">
                            {{ aiModalBlockedAction ? t('plugin.rolling-memory.eval.modalSaveAndContinueBtn') : t('plugin.rolling-memory.eval.saveModelBtn') }}
                        </el-button>
                    </div>
                </div>
            </template>
        </el-dialog>

        <!-- Readiness gate: preparation never auto-starts a batch. -->
        <el-dialog v-model="corpusModalVisible" :title="evalCorpusReady ? (isEn ? 'Corpus is ready' : '语料已就绪') : (isEn ? 'Prepare the evaluation corpus' : '准备评测语料')" width="620px" destroy-on-close>
            <el-alert :title="readinessTitle" :description="readinessDescription" :type="evalCorpusReady ? 'success' : 'warning'" show-icon :closable="false" />
            <p v-if="!evalCorpusReady" style="margin: 10px 2px 0; font-size: 12px; line-height: 1.7; color: var(--el-text-color-secondary)">
                {{
                    isEn
                        ? 'The benchmark corpus is not bundled: obtain it from the upstream project. For ATM-Bench, one CLI command fetches the required files (~35 MB), rebuilds them locally and verifies the digests: rmem eval --fetch atm --accept-upstream-license'
                        : '基准语料不随产品分发：请从上游项目获取。ATM-Bench 可用一条命令获取必要文件（约 35 MB）、在本机重建并校验摘要：rmem eval --fetch atm --accept-upstream-license'
                }}
            </p>
            <el-descriptions :column="1" border size="small" style="margin-top: 12px">
                <el-descriptions-item label="scope">{{ evalScopeText }}</el-descriptions-item>
                <el-descriptions-item label="store_path">{{ evalStorePath }}</el-descriptions-item>
                <el-descriptions-item label="readiness.ready">{{ overview.readiness?.ready === true ? 'true' : 'false' }}</el-descriptions-item>
                <el-descriptions-item label="local_fallback">{{ localFallbackText }}</el-descriptions-item>
            </el-descriptions>
            <template #footer>
                <el-button size="small" @click="corpusModalVisible = false">{{ isEn ? 'Close' : '关闭' }}</el-button>
                <template v-if="evalCorpusReady">
                    <el-button size="small" type="primary" @click="corpusModalVisible = false; requestRunBatchEval()">{{ isEn ? 'Review and start' : '确认并开始评测' }}</el-button>
                </template>
                <template v-else>
                    <el-button size="small" type="success" plain :loading="ingesting === 'verify'" :disabled="ingesting !== ''" @click="runIngest('verify')">{{ isEn ? 'Verify Only' : '仅核验 (Verify)' }}</el-button>
                    <el-button size="small" type="info" plain :loading="ingesting === 'prepare'" :disabled="ingesting !== ''" @click="runIngest('prepare')">{{ isEn ? 'Prepare Only' : '仅准备语料 (Prepare)' }}</el-button>
                    <el-button size="small" type="primary" :loading="ingesting === 'prepare' || batchRunning" :disabled="ingesting !== ''" @click="corpusModalVisible = false; prepareAndStartBatchEval()">
                        {{ isEn ? 'Prepare Corpus & Start' : '准备语料并开始评测' }}
                    </el-button>
                </template>
            </template>
        </el-dialog>
    </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useAdminInfo } from '@/stores/adminInfo'
import { useLoadingProgressText } from '@/utils/loadingProgress'
import { MonitorSocket } from '@/utils/monitorSocket'
import request from '@/utils/request'
import MarkdownPreview from './MarkdownPreview.vue'
import MemoryHoverPreview from './MemoryHoverPreview.vue'

const { t, locale } = useI18n()
const router = useRouter()
const adminInfo = useAdminInfo()

const isEn = computed(() => String(locale.value || '').toLowerCase().startsWith('en'))
const loc = (zh?: string, en?: string) => {
    if (isEn.value && en && en.trim() !== '') return en
    return zh || en || ''
}

const goLeaderboard = () => {
    router.push({ path: '/admin/plugin/rolling-memory/leaderboard' })
}

const goATMLeaderboard = () => {
    router.push({ path: '/admin/plugin/rolling-memory/atm-leaderboard' })
}

// goAIPluginChannels 跳转 AI 路由插件的渠道管理页（前置拦截弹窗的「去管理」入口）。
const goAIPluginChannels = () => {
    router.push({ path: '/admin/plugin/ai/channel' })
}

interface EvalManifest {
    transport?: string
    endpoint?: string
    user_id?: string
    scope?: string
    suite?: string
    corpus_sha256?: string
    documents?: number
    local_fallback?: boolean
    store_path?: string
    provenance?: Record<string, any>
}

interface EvalReadiness {
    ready?: boolean
    message?: string
    message_en?: string
    error?: string
    detail?: string
    detail_en?: string
}

interface RecallMeta {
    engine_transport?: string
    engine_scope?: string
    engine_upstream?: string
    local_fallback?: boolean
    cloud_hits?: number
    transport?: string
    endpoint?: string
    scope?: string
    store_path?: string
    corpus_sha256?: string
    provenance?: Record<string, any>
    [key: string]: any
}

interface AIModelConfig {
    provider: string
    base_url: string
    api_key: string
    has_api_key: boolean
    answer_model: string
    judge_model: string
    temperature: number
    max_tokens: number
    timeout_sec: number
}

interface AICheckItem {
    key: string
    label: string
    label_en?: string
    passed: boolean
    detail: string
    detail_en?: string
    blocking: boolean
}

// AIPluginChannelOption AI 路由插件（plugins/ai）渠道候选（评测实验室一键接入数据面）。
interface AIPluginChannelOption {
    channel_id: number
    channel_name: string
    vendor: string
    base_url: string
    has_key: boolean
    status: number
    circuit: string
    upstream_name: string
    routable: boolean
    models: string[]
}

interface AIModelCheckResult {
    ready: boolean
    is_localhost: boolean
    summary: string
    summary_en?: string
    checks: AICheckItem[]
    plugin_channels?: AIPluginChannelOption[]
    plugin_channels_ok?: boolean
}

interface AIConnectionTestResponse {
    success: boolean
    pin_verified: boolean
    endpoint: string
    answer_model: string
    judge_model: string
    answer_ok: boolean
    answer_ms: number
    answer_output: string
    answer_error: string
    judge_ok: boolean
    judge_ms: number
    judge_label: string
    judge_reason: string
    judge_error: string
    total_ms: number
    message: string
    message_en?: string
    config_check: AIModelCheckResult
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

interface CategoryScores {
    cat1: number
    cat2: number
    cat3: number
    cat4: number
}

interface EvalRunRecord {
    id: string
    suite?: string
    label: string
    label_en?: string
    created_at: string
    is_baseline: boolean
    tier: string
    scored_questions: number
    arm: string
    model: string
    protocol: string
    evidence_hit: number
    full_evidence_hit?: number
    answer_in_context: number
    judge_rate: number
    judge_correct: number
    judge_scored: number
    elapsed_seconds: number
    // 官方检索指标（gold-set-mean 口径；旧台账记录缺省为 0/undefined）
    r10?: number
    r20?: number
    r50?: number
    any_hit_10?: number
    all_hit_10?: number
    ingest_ms?: number
    top_k?: number
    avg_recall_ms?: number
    p95_recall_ms?: number
    category_evidence: CategoryScores
    category_judge: CategoryScores
    note: string
    note_en?: string
    // 仅展示通过现行 SDK 参数校验的复现命令；历史旧命令保留在后台台账，不上屏冒充现行代次。
    repro_cli?: string
    bucket_counts?: Record<string, number>
}

interface EvalQuestionItem {
    id: string
    sample_id: string
    category: string
    question: string
    answer: string
    evidence_texts: string[]
}

interface EvidenceMatchItem {
    text: string
    token_recall: number
    hit: boolean
}

interface EvalQuestionResultItem {
    index: number
    id: string
    sample_id: string
    category: string
    question: string
    gold: string
    evidence_texts: string[]
    evidence_matches?: EvidenceMatchItem[]
    best_evidence_recall: number
    evidence_hit: boolean
    answer_in_context: boolean
    gold_token_recall: number
    prediction: string
    f1: number
    exact: boolean
    judge: string
    judge_reason: string
    context_preview: string
    context?: string
    formatted_items?: string[]
    context_chars: number
    bands: number
    nodes: number
    raw: number
    recall_ms: number
    answer_ms: number
    judge_ms: number
    total_ms: number
    cloud_hits?: number
    engine_transport?: string
    engine_scope?: string
    engine_upstream?: string
    local_fallback?: boolean
    retrieval_unit_ids?: string[]
    corpus_sha256?: string
    meta?: RecallMeta
    recall?: StructuredRecallPayload
    // L0 工作台后端契约（admin/plugins/rolling-memory/eval.go:399-411，缺省键=旧后端，前端按同一判据兜底）：
    //   failure_bucket 五项根因桶闭集；band_items/raw_items 交付面按来源拆分的编号行；
    //   repro_cli 单题等价复现命令；context_ordering 交付行序标注（bands_first | raw_first）。
    failure_bucket?: string
    band_items?: string[]
    raw_items?: string[]
    repro_cli?: string
    context_ordering?: string
    bands_delivered?: number
    nodes_delivered?: number
    raw_delivered?: number
    best_head_evidence_recall?: number
    head_evidence_hit?: boolean
}

// 单题证据切片与引擎 provenance。raw_hits 仅是既有 wire 字段的兼容读取，不代表独立检索臂。
interface RecallEvidenceHit {
    unit_id?: string
    trace?: string
    session?: string | number
    turn?: string | number
    author?: string
    text?: string
    occurred_at?: number
}

interface StructuredRecallPayload {
    scope?: string
    total_hits?: number
    raw_hits?: RecallEvidenceHit[]
    meta?: RecallMeta
    gaps?: Record<string, number>
    legs?: Record<string, number>
}

// EvalRootCauseKey 错题根因闭集（仅纯数值 / 枚举判据；RANK_CUTOFF 需后端补截断信号，前端只渲禁用态）。
type EvalRootCauseKey = 'HIT' | 'RECALL_MISS' | 'RANK_CUTOFF' | 'READER_GAP' | 'JUDGE_AMBIGUITY'

// EvalRunOutcome 单题对错信号：有判词取判词，未启用 Answer/Judge 时回落到证据命中。
type EvalRunOutcome = 'correct' | 'wrong' | 'error' | 'ev_hit' | 'ev_miss'

// EvalDiffKind 逐题回归对比结论。
type EvalDiffKind = 'regressed' | 'fixed' | 'same' | 'changed' | 'absent'

interface EvalJobStatus {
    job_id: string
    status: string
    phase: string
    phase_text: string
    phase_text_en?: string
    tier: string
    arm_mode: string
    // 评测套件（atm | locomo；空 = 旧记录）——ATM 混合 QS 与 LoCoMo 判对率的展示口径区分依据。
    suite?: string
    use_agents: boolean
    top_k?: number
    workers: number
    category_filter: string
    total: number
    done: number
    percent: number
    started_at: string
    elapsed_seconds: number
    eta_seconds: number
    current_question_id: string
    current_question_text: string
    live_evidence_hit: number
    live_answer_in_context: number
    live_f1: number
    // 官方 gold-set-mean 实时聚合（与落档 r10/r20/r50 同源；旧后端缺省 undefined）
    live_r10?: number
    live_r20?: number
    live_r50?: number
    live_any_hit_10?: number
    live_all_hit_10?: number
    live_avg_recall_ms?: number
    live_judge_rate: number
    live_judge_correct: number
    live_judge_scored: number
    live_judge_wrong: number
    live_judge_error: number
    live_category_evidence: CategoryScores
    live_category_judge: CategoryScores
    latest_item?: EvalQuestionResultItem
    recent_items: EvalQuestionResultItem[]
    logs: string[]
    record?: EvalRunRecord
    error?: string
    // L0 工作台后端契约（eval.go:452-455）：实时五项根因桶计数。
    bucket_counts?: Record<string, number>
}

interface ProtocolParitySpec {
    builtin_protocol?: string
    protocol_id?: string
    number_scorer?: string
    number_scorer_version?: string
    list_scorer?: string
    list_scorer_version?: string
    judge_parser?: string
    scoring_fixture_sha256?: string
    answer_payload_sha1?: string
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
    prompt_pins_verified?: boolean
    fixture_verified?: boolean
    corpus_pins_verified?: boolean
    corpus_pins_error?: string
}

interface EvalOverviewResponse {
    suite?: string
    mode?: 'local' | 'cloud'
    store_path?: string
    manifest?: EvalManifest | null
    readiness?: EvalReadiness | null
    ai_config: AIModelConfig
    ai_check: AIModelCheckResult
    agents: OfficialProtocolAgent[]
    protocol_spec?: ProtocolParitySpec
    atm_official_pins?: ATMOfficialPinStatus
    baselines: EvalRunRecord[]
    run_history: EvalRunRecord[]
    active_job: EvalJobStatus
    tier: string
    tier_hard_count?: number
    tier_100_count?: number
    tier_200_count: number
    tier_1540_count: number
    tier_1013_count?: number
    total_questions: number
    page: number
    page_size: number
    questions: EvalQuestionItem[]
    question_error?: string
    atm_oracle_qs_ceiling?: number
}

const loading = ref(false)
const overviewLoadError = ref('')
const ingesting = ref('')
// Prepare/Rebuild 会向隔离评测 scope 写入整份语料（LoCoMo 5,882 / ATM 11,034 条），
// 耗时以分钟计，必须使用独立的长超时；后端已把工作上下文与 HTTP 连接解耦，
// 客户端断开不会杀死已经开始的 ingest。
const INGEST_TIMEOUT_MS = 60 * 60 * 1000
const ingestElapsed = ref(0)
let ingestTimer: ReturnType<typeof setInterval> | null = null
const startIngestClock = () => {
    ingestElapsed.value = 0
    const startedAt = Date.now()
    if (ingestTimer) clearInterval(ingestTimer)
    ingestTimer = setInterval(() => {
        ingestElapsed.value = Math.floor((Date.now() - startedAt) / 1000)
    }, 1000)
}
const stopIngestClock = () => {
    if (ingestTimer) {
        clearInterval(ingestTimer)
        ingestTimer = null
    }
}
const batchRunning = ref(false)
const savingAI = ref(false)
const testingAI = ref(false)
const useAgents = ref(false)
const tierTotalCount = computed(() => {
    if (query.suite === 'atm') return query.tier === '1013' ? 1013 : 31
    if (query.tier === '100') return 100
    return query.tier === '1540' ? 1540 : 200
})
const batchLimit = computed(() => {
    const full = query.suite === 'atm'
        ? (query.tier === '1013' ? 1013 : 31)
        : 1540
    const cap = typeof evalConfig !== 'undefined' ? Number(evalConfig.sample_limit || 0) : 0
    if (cap > 0 && cap < full) return cap
    return full
})
// 抽样进行时主按钮直接写明本次只跑前 N 题，避免被误读为整集全量运行。
const startCapSuffix = computed(() => {
    const cap = batchLimit.value
    const full = tierTotalCount.value
    if (!(cap > 0) || cap >= full) return ''
    return isEn.value ? ` · first ${cap} Qs` : ` · 前 ${cap} 题`
})
const activeTab = ref('run')
const liveViewMode = ref<'feed' | 'log'>('feed')
const overview = ref<Partial<EvalOverviewResponse>>({})
// 本地模式为纯本地发布面：仅 local_plain 臂可运行（零脉冲、零云端请求）；脉冲协议与 SDK 保留供下一代接入。
const isLocalMode = computed(() => String(overview.value?.mode || 'local') !== 'cloud')
const selectedRecord = ref<EvalRunRecord | null>(null)
const aiTestResult = ref<AIConnectionTestResponse | null>(null)

const aiModalVisible = ref(false)
const aiModalBlockedAction = ref<'' | 'batch' | 'probe' | 'switch'>('')
const corpusModalVisible = ref(false)

// Oracle 是答题金标上限，不属于引擎成绩。
const locomoOracleCeiling = computed(() => {
    const oracle = visibleBaselines.value.find((b) => b.id === 'baseline-oracle' || b.arm === 'oracle-llm')
    return Number(oracle?.judge_rate || 0)
})
const locomoOracleLineY = computed(() => 182 - locomoOracleCeiling.value * 160 - 5)
const atmOracleQSCeiling = computed(() => Number(overview.value.atm_oracle_qs_ceiling || 0))
const atmOracleQSRatio = computed(() => (atmOracleQSCeiling.value > 0 ? Math.min(1, atmOracleQSCeiling.value / 100) : 0))
const atmOracleLineY = computed(() => (atmOracleQSCeiling.value > 0 ? 182 - atmOracleQSRatio.value * 160 - 5 : 26))
const atmOracleQSLabel = computed(() => {
    const v = atmOracleQSCeiling.value
    if (!(v > 0)) return isEn.value ? 'Oracle gold QS ceiling (not an engine result)' : 'Oracle 金标 QS 上限（不是引擎成绩）'
    return isEn.value ? `Oracle gold QS ceiling ${v.toFixed(2)}% (not an engine result)` : `Oracle 金标 QS 上限 ${v.toFixed(2)}%（不是引擎成绩）`
})

// AI 路由插件渠道一键接入（前置拦截弹窗）
const applyingChannelID = ref<number | null>(null)
const pluginChannelModelPick = reactive<Record<number, string>>({})
const pluginChannels = computed<AIPluginChannelOption[]>(() => overview.value.ai_check?.plugin_channels || [])
const pluginChannelRoutable = computed(() => pluginChannels.value.filter((item) => item.routable).length)

// 渠道切换时同步模型选择（默认取该渠道绑定的第一条模型）
watch(pluginChannels, (list) => {
    for (const item of list) {
        if (!pluginChannelModelPick[item.channel_id] && item.models && item.models.length > 0) {
            pluginChannelModelPick[item.channel_id] = item.models[0]
        }
    }
})

const evalConfig = reactive({
    arm_mode: 'sdk-local-plain' as 'sdk-pulse-cipher' | 'sdk-pulse-plain' | 'sdk-local-plain' | 'oracle',
    pulse_mode: 'local_plain' as 'pulse_cipher' | 'pulse_plain' | 'local_plain',
    top_k: 50,
    workers: 2,
    category: '',
    custom_label: '',
    sample_limit: 0,
})

const evalArmLabel = computed(() => {
    switch (evalConfig.arm_mode) {
        case 'sdk-local-plain': return isEn.value ? 'Local Plain (local_plain)' : '本地明文 (local_plain)'
        case 'sdk-pulse-plain': return isEn.value ? 'Pulse Plain (pulse_plain)' : '脉冲明文 (pulse_plain)'
        case 'sdk-pulse-cipher': return isEn.value ? 'Pulse Cipher (pulse_cipher)' : '脉冲密文 (pulse_cipher)'
        case 'oracle': return isEn.value ? 'Oracle gold ceiling' : 'Oracle 金标上限'
    }
    return '—'
})

watch(() => evalConfig.arm_mode, (val) => {
    overview.value = { ...overview.value, manifest: null, readiness: undefined, store_path: '' }
    if (val === 'sdk-pulse-plain') {
        evalConfig.pulse_mode = 'pulse_plain'
    } else if (val === 'sdk-pulse-cipher') {
        evalConfig.pulse_mode = 'pulse_cipher'
    } else {
        evalConfig.pulse_mode = 'local_plain'
    }
    void loadOverview()
}, { flush: 'sync' })

const liveJob = reactive<EvalJobStatus>({
    job_id: '',
    status: 'idle',
    phase: 'idle',
    phase_text: '',
    phase_text_en: '',
    tier: 'hard',
    arm_mode: 'sdk-local-plain',
    use_agents: false,
    top_k: 50,
    workers: 2,
    category_filter: '',
    total: 0,
    done: 0,
    percent: 0,
    started_at: '',
    elapsed_seconds: 0,
    eta_seconds: 0,
    current_question_id: '',
    current_question_text: '',
    live_evidence_hit: 0,
    live_answer_in_context: 0,
    live_f1: 0,
    live_r10: 0,
    live_r20: 0,
    live_r50: 0,
    live_any_hit_10: 0,
    live_all_hit_10: 0,
    live_avg_recall_ms: 0,
    live_judge_rate: 0,
    live_judge_correct: 0,
    live_judge_scored: 0,
    live_judge_wrong: 0,
    live_judge_error: 0,
    live_category_evidence: { cat1: 0, cat2: 0, cat3: 0, cat4: 0 },
    live_category_judge: { cat1: 0, cat2: 0, cat3: 0, cat4: 0 },
    recent_items: [],
    logs: [],
})

const isJobRunning = computed(() => liveJob.status === 'running')

const detailRunId = ref('')
// compareRunId Run A（基线侧）运行 ID：选定后按 item.id 左连接做逐题回归对比。
const compareRunId = ref('')
const loadingDetails = ref(false)
const detailItems = ref<EvalQuestionResultItem[]>([])
// 逐题明细可用性：封印基线与未落盘运行没有逐题行，必须显式空态，不能假装有合成明细。
const detailAvailable = ref(true)
const detailReason = ref('')
// compareItems detail 侧对比运行的逐题明细（Run A）。
const compareItems = ref<EvalQuestionResultItem[]>([])
const loadingCompare = ref(false)
const compareAvailable = ref(true)
const compareReason = ref('')
const detailFilter = reactive({
    verdict: 'all',
    category: '',
    keyword: '',
    rootCause: 'all' as 'all' | EvalRootCauseKey,
    diff: 'all' as 'all' | 'regressed' | 'fixed',
})

const aiForm = reactive<AIModelConfig>({
    provider: 'openai',
    base_url: 'https://api.deepseek.com/v1',
    api_key: '',
    has_api_key: false,
    answer_model: 'deepseek-chat',
    judge_model: 'deepseek-chat',
    temperature: 0,
    max_tokens: 32768,
    timeout_sec: 180,
})

const query = reactive({
    suite: 'atm' as 'locomo' | 'atm',
    tier: 'hard',
    category: '',
    keyword: '',
    page: 1,
    page_size: 20,
})

// scope 必须同时匹配套件和所选模式；世代后缀由服务端分配。
const evalScopeMatches = (scope: unknown, suite: string, pulseMode: string): boolean => {
    const text = String(scope || '').trim().toLowerCase()
    if (/\s|:/.test(text)) return false
    const index = text.lastIndexOf('-mem-eval-')
    if (index <= 0) return false
    const parts = text.slice(index + '-mem-eval-'.length).split('-')
    if (parts.some((part) => !part) || parts[0] !== (suite === 'atm' ? 'atmbench' : 'locomo')) return false
    const mode = parts[1] === 'local' && parts[2] === 'plain' ? 'local_plain'
        : parts[1] === 'pulse' && parts[2] === 'plain' ? 'pulse_plain'
        : parts[1] === 'pulse' && parts[2] === 'cipher' ? 'pulse_cipher' : ''
    return mode === pulseMode
}

const expectedEvalScope = computed(() => {
    const base = query.suite === 'atm' ? 'u10-mem-eval-atmbench' : 'u10-mem-eval-locomo'
    if (evalConfig.arm_mode === 'sdk-pulse-plain') return `${base}-pulse-plain`
    if (evalConfig.arm_mode === 'sdk-pulse-cipher') return `${base}-pulse-cipher`
    if (evalConfig.arm_mode === 'sdk-local-plain') return `${base}-local-plain`
    return '—'
})
const evalManifest = computed<EvalManifest | null>(() => overview.value.manifest || null)
const evalScopeText = computed(() => {
    if (evalConfig.arm_mode === 'oracle') return '—'
    const actual = String(evalManifest.value?.scope || '').trim()
    if (evalScopeMatches(actual, query.suite, evalConfig.pulse_mode)) return actual
    return `${expectedEvalScope.value}${isEn.value ? ' · generation auto-allocated per run' : ' · 每次评测自动分配世代'}`
})
const manifestTransport = computed(() => String(evalManifest.value?.transport || '') || '—')
const manifestEndpoint = computed(() => String(evalManifest.value?.endpoint || '') || '—')
const manifestUserID = computed(() => String(evalManifest.value?.user_id || '') || '—')
const manifestDocuments = computed(() => {
    const documents = evalManifest.value?.documents
    return typeof documents === 'number' ? documents : '—'
})
const manifestCorpusDigest = computed(() => String(evalManifest.value?.corpus_sha256 || '') || '—')
const manifestLocalFallback = computed(() => (typeof evalManifest.value?.local_fallback === 'boolean' ? evalManifest.value.local_fallback : null))
const localFallbackText = computed(() => (manifestLocalFallback.value === null ? 'unknown' : manifestLocalFallback.value ? 'true (blocked)' : 'false'))
const evalStorePath = computed(() => {
    if (evalConfig.arm_mode === 'oracle') return '—'
    const actual = String(overview.value.store_path || evalManifest.value?.store_path || '').trim()
    if (actual) return actual
    const base = query.suite === 'atm' ? 'eval-atm' : 'eval-locomo'
    const suffix = evalConfig.arm_mode === 'sdk-pulse-plain' ? '-pulse-plain' : evalConfig.arm_mode === 'sdk-pulse-cipher' ? '-pulse-cipher' : '-local-plain'
    return `~/.local/state/rolling-memory/eval/${base}${suffix}.json`
})

// 只允许 readiness 与 manifest 共同放行；四表计数、旧 scope 与旧锁文件均不参与判定。
const evalCorpusReady = computed(() => {
    const manifest = evalManifest.value
    if (overview.value.readiness?.ready !== true || !manifest) return false
    if (evalConfig.arm_mode === 'oracle') return false
    if (evalConfig.arm_mode !== `sdk-${evalConfig.pulse_mode.replace('_', '-')}`) return false
    const mode = evalConfig.pulse_mode
    const transport = mode === 'local_plain' ? 'ramesdk.local_plain.offline-v1'
        : mode === 'pulse_plain' ? 'ramesdk.pulse_plain.open-memory-v1' : 'ramesdk.pulse_cipher.open-memory-v1'
    const endpoint = String(manifest.endpoint || '')
    return (
        manifest.local_fallback === false &&
        String(manifest.suite || '') === query.suite &&
        evalScopeMatches(manifest.scope, query.suite, mode) &&
        String(manifest.user_id || '') === '10' &&
        String(manifest.transport || '') === transport &&
        (mode === 'local_plain' ? endpoint.startsWith('local://') : /^https?:\/\//.test(endpoint) && endpoint.includes('/open/memory/v1')) &&
        Number(manifest.documents || 0) > 0 &&
        String(manifest.corpus_sha256 || '').trim() !== ''
    )
})

const readinessTitle = computed(() => {
    if (evalConfig.arm_mode === 'oracle') return isEn.value ? 'Oracle does not require corpus verification' : 'Oracle 不需要语料核验'
    if (evalCorpusReady.value) return isEn.value ? `${evalArmLabel.value} corpus verified; batch runs are allowed` : `${evalArmLabel.value} 语料已核验，可以启动批次`
    return isEn.value ? 'Batch blocked: Prepare and Verify must pass first' : '批次已阻断：必须先完成 Prepare 并通过 Verify'
})
const readinessDescription = computed(() => {
    if (evalConfig.arm_mode === 'oracle') return isEn.value ? 'Oracle supplies gold evidence and is not an engine result.' : 'Oracle 使用金标证据，不是引擎成绩。'
    const readiness = overview.value.readiness
    const detail = String(readiness?.error || loc(readiness?.detail || readiness?.message, readiness?.detail_en || readiness?.message_en) || '').trim()
    if (evalCorpusReady.value) {
        return isEn.value
            ? 'readiness.ready=true and the manifest matches the selected arm’s transport, endpoint, scope, documents, corpus digest and local_fallback=false.'
            : 'readiness.ready=true，且 manifest 已匹配所选评测臂的 transport、endpoint、scope、documents、语料摘要与 local_fallback=false。'
    }
    const fallback = isEn.value
        ? 'The gate does not read legacy table counts. A failed Prepare/Verify never auto-starts a batch.'
        : '门禁不读取旧四表计数；Prepare/Verify 失败后绝不会自动启动批次。'
    return detail ? `${fallback} ${detail}` : fallback
})

const probeVisible = ref(false)
const probing = ref(false)
const probeWithAgents = ref(false)
const probeTopK = ref(50)
const probeTarget = ref<EvalQuestionItem | null>(null)
const probeItem = ref<EvalQuestionResultItem | null>(null)
const probeResultText = ref('')
// probeRecall 单题探针返回的结构化 recall 对象（仅 mode=single 实时探针可用；批次明细入口为 null）。
const probeRecall = ref<StructuredRecallPayload | null>(null)
const probeJsonPanels = ref<string[]>([])

// 评测判据阈值：与后端 admin/plugins/rolling-memory/eval.go:2944（evidence_hit = bestEv >= 0.6）
// 与 eval.go:2947（answer_in_context = goldRecall >= 0.8）严格对齐，禁止各写一套魔数。
const EVIDENCE_HIT_RECALL_THRESHOLD = 0.6
const ANSWER_IN_CONTEXT_TOKEN_THRESHOLD = 0.8
// JUDGE_EQUIVALENT_F1_THRESHOLD 判词歧义档阈值：判 wrong 但答题与金标实质等价（F1 ≥ 0.5）归 JUDGE_AMBIGUITY，
// 与后端 classifyFailureBucket（eval.go:2883）一致。
const JUDGE_EQUIVALENT_F1_THRESHOLD = 0.5

const clamp01 = (v: any) => Math.max(0, Math.min(1, Number(v || 0)))
const formatPct = (v: any) => `${(clamp01(v) * 100).toFixed(1)}%`
// 官方金标覆盖（gold-set-mean）记录判定与展示辅助：r10 / Any-Hit@10 任一有读数即视为官方口径。
const hasOfficialRecall = (r: any) => Boolean(r) && ((Number(r.r10) || 0) > 0 || (Number(r.any_hit_10) || 0) > 0)
// 图表主柱取值：优先官方 R@10；旧记录回退交付窗口证据命中率（不冒充官方口径，仅作历史可视化）。
const chartBarValue = (r: any) => ((Number(r?.r10) || 0) > 0 ? Number(r.r10) : Number(r?.evidence_hit) || 0)
// 毫秒级耗时的人读格式化（<1s 显示 ms，≥1s 显示一位小数秒）。
const formatDurationMS = (ms: any) => {
    const v = Number(ms) || 0
    return v >= 1000 ? `${(v / 1000).toFixed(1)}s` : `${Math.round(v)}ms`
}
const formatDelta = (v: number) => `${Number(v || 0) >= 0 ? '+' : ''}${Number(v || 0).toFixed(3)}`

// ===== Delta 对比矩阵（基线 / 本次 / Δ 三列）=====
// deltaBaseline：与当前查看记录同套件同题集档位的封印基线（我方实测行优先）；查看对象
// 本身是基线时不自比（返回 null，矩阵退化为单列绝对值）。
const deltaBaseline = computed<EvalRunRecord | null>(() => {
    const rec = activeRecord.value
    if (!rec || rec.is_baseline) return null
    const pool = visibleBaselines.value.filter((b) => b.tier === rec.tier)
    return pool.find((b) => /-rm/.test(b.id)) || pool[0] || null
})
interface DeltaRow {
    key: string
    label: string
    run: number
    base: number
    deltaPP: number // 百分点差（run-base）×100
    kind: 'pct' | 'ms'
    warn: boolean // 退化超 2.5pp 盈亏线红警
}
const deltaRows = computed<DeltaRow[]>(() => {
    const rec = activeRecord.value
    if (!rec) return []
    const base = deltaBaseline.value
    const pct = (key: string, label: string, pickVal: (r: EvalRunRecord) => number): DeltaRow => {
        const run = Number(pickVal(rec)) || 0
        const b = base ? Number(pickVal(base)) || 0 : 0
        const d = base ? (run - b) * 100 : 0
        return { key, label, run, base: b, deltaPP: d, kind: 'pct', warn: base != null && d < -2.5 }
    }
    const rows: DeltaRow[] = [
        pct('r10', isEn.value ? 'Official R@10 (gold-set-mean)' : '官方 R@10（金标交均）', (r) => r.r10 || 0),
        pct('r20', 'R@20', (r) => r.r20 || 0),
        pct('r50', 'R@50', (r) => r.r50 || 0),
        pct('any', 'Any-Hit@10', (r) => r.any_hit_10 || 0),
        pct('all', 'All-Hit@10', (r) => r.all_hit_10 || 0),
    ]
    if ((rec.judge_scored || 0) > 0 || (base?.judge_scored || 0) > 0) {
        rows.push(pct('qs', isEn.value ? 'Hybrid QS / Judge' : '混合 QS / 判官', (r) => (r.judge_scored > 0 ? r.judge_rate : 0)))
    }
    // 延迟行：量纲为 ms，Δ 为毫秒差（越低越好，恶化 = 正差）——不参与 2.5pp 盈亏线。
    const runMS = Number(rec.avg_recall_ms) || 0
    const baseMS = base ? Number(base.avg_recall_ms) || 0 : 0
    if (runMS > 0 || baseMS > 0) {
        rows.push({ key: 'lat', label: isEn.value ? 'Retrieval latency (mean ms)' : '检索延迟（均值 ms）', run: runMS, base: baseMS, deltaPP: base ? runMS - baseMS : 0, kind: 'ms', warn: false })
    }
    return rows
})
const formatDeltaPP = (row: DeltaRow) => {
    if (!deltaBaseline.value) return '—'
    if (row.kind === 'ms') return `${row.deltaPP >= 0 ? '+' : ''}${Math.round(row.deltaPP)}ms`
    return `${row.deltaPP >= 0 ? '+' : ''}${row.deltaPP.toFixed(2)}pp`
}
const deltaCellClass = (row: DeltaRow) => {
    if (!deltaBaseline.value || Math.abs(row.deltaPP) < 0.005) return 'is-flat'
    if (row.kind === 'ms') return row.deltaPP > 0 ? 'is-down' : 'is-up'
    return row.deltaPP > 0 ? 'is-up' : row.warn ? 'is-warn' : 'is-down'
}

// topKOf：优先读结构化落档键；旧记录回退解析 armLabel 的 `k=NN` / `rlNN` 尾注。
const topKOf = (r: any) => {
    const structured = Number(r?.top_k) || 0
    if (structured > 0) return structured
    const m = String(r?.arm || '').match(/(?:k=|rl)(\d+)/)
    return m ? Number(m[1]) : 0
}

// 导出复现包：md = 下载自包含 Markdown 报告；json = 下载机读台账包。
const exportingRepro = ref(false)
const exportReproBundle = async (format: 'markdown' | 'json') => {
    const rec = activeRecord.value
    if (!rec || exportingRepro.value) return
    exportingRepro.value = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/eval/export',
            method: 'POST',
            data: { suite: query.suite, run_id: rec.is_baseline ? '' : rec.id, format },
        })
        const data = res.data.data || {}
        let blobText = ''
        let fname = `${rec.id || 'eval-run'}-repro`
        if (format === 'markdown') {
            blobText = String(data?.markdown || '')
            fname += '.md'
        } else {
            blobText = JSON.stringify(data, null, 2)
            fname += '.json'
        }
        if (!blobText) throw new Error('empty export payload')
        const blob = new Blob([blobText], { type: format === 'markdown' ? 'text/markdown' : 'application/json' })
        const url = URL.createObjectURL(blob)
        const a = document.createElement('a')
        a.href = url
        a.download = fname
        a.click()
        URL.revokeObjectURL(url)
        ElMessage.success(isEn.value ? `Exported ${fname}` : `已导出 ${fname}`)
    } catch (err: any) {
        ElMessage.error((isEn.value ? 'Export failed: ' : '导出失败：') + String(err?.message || err))
    } finally {
        exportingRepro.value = false
    }
}

// tierOptionTag 把档位渲染成人类可读的短标签：数字档位（100/200/1013/1540）用 T 前缀，
// 命名档位（ATM 的 hard）直接用词本身，避免出现「(Thard)」这类拼接怪串。
const tierOptionTag = (tier: any) => {
    const text = String(tier ?? '').trim()
    if (text === '') return ''
    if (/^\d+$/.test(text)) return `T${text}`
    return text.charAt(0).toUpperCase() + text.slice(1)
}

const shortChartLabel = (item: EvalRunRecord) => {
    const arm = String(item.arm || '').toLowerCase()
    if (arm.includes('oracle') || String(item.label || '').includes('Oracle')) return 'Oracle · ceiling'
    if (arm.includes('sdk-pulse-cipher')) return 'SDK Pulse Cipher'
    const label = loc(item.label, item.label_en).trim()
    const tag = tierOptionTag(item.tier)
    return label ? `${tag} · ${isEn.value ? 'history' : '历史'}` : tag
}

const includeArchivedTiers = ref(true)
const isRecordInCurrentSuite = (record: EvalRunRecord) => !record.suite || record.suite === query.suite
// 不再提供的 LoCoMo 便捷档（100/200）：历史台账/图表保留只读展示并标注「历史」。
const isRetiredRecord = (record: EvalRunRecord) => {
    if (record.suite && record.suite !== 'locomo') return false
    const tier = String(record.tier || '')
    return tier === '100' || tier === '200'
}
const visibleRunHistory = computed<EvalRunRecord[]>(() => {
    const rows = (overview.value.run_history || []).filter((record) => isRecordInCurrentSuite(record))
    return includeArchivedTiers.value ? rows : rows.filter((record) => !isRetiredRecord(record))
})
const visibleBaselines = computed<EvalRunRecord[]>(() => {
    const rows = (overview.value.baselines || []).filter((record) => isRecordInCurrentSuite(record))
    return includeArchivedTiers.value ? rows : rows.filter((record) => !isRetiredRecord(record))
})
const archivedTierCount = computed(() => {
    const rows = [...(overview.value.run_history || []), ...(overview.value.baselines || [])]
    return rows.filter((record) => isRetiredRecord(record)).length
})
const allLedgerRows = computed<EvalRunRecord[]>(() => [...visibleRunHistory.value, ...visibleBaselines.value])

const detailRecord = computed<EvalRunRecord | null>(() => {
    return allLedgerRows.value.find((r) => r.id === detailRunId.value) || selectedRecord.value || null
})

// rootCauseOf 错题根因五桶判定：优先后端 failure_bucket，缺省时按后端同一阶梯（eval.go:2871-2897）机械兜底。
// 只用布尔 / 计数 / 判词 / F1 数值，零词句语义（严禁解析 judge_reason 自然语言，AGENTS）。
// 阶梯：判词 correct ⇒ HIT；判词 unparsed/error ⇒ JUDGE_AMBIGUITY；判词 wrong 且 F1 ≥ 0.5（实质等价）⇒ JUDGE_AMBIGUITY；
// 证据在位（evidence_hit 或 answer_in_context）⇒ wrong=READER_GAP 其余=HIT；
// 证据不在位但召回腿有产出（best_evidence_recall / raw / bands / nodes 任一非零）⇒ RANK_CUTOFF；召回腿完全空手 ⇒ RECALL_MISS。
// 入参取 any：el-table 插槽 row 为宿主表的 DefaultRow（与同文件 probeQuestion/openQuestionResultProbe 的 row: any 一致）。
const rootCauseOf = (it: any): EvalRootCauseKey => {
    const fromBackend = String(it?.failure_bucket || '').trim().toUpperCase()
    if (fromBackend === 'HIT' || fromBackend === 'RECALL_MISS' || fromBackend === 'RANK_CUTOFF' || fromBackend === 'READER_GAP' || fromBackend === 'JUDGE_AMBIGUITY') {
        return fromBackend as EvalRootCauseKey
    }
    const verdict = String(it?.judge || '').trim().toLowerCase()
    const f1 = Number(it?.f1 || 0)
    if (verdict === 'correct') return 'HIT'
    if (verdict === 'unparsed' || verdict === 'error') return 'JUDGE_AMBIGUITY'
    if (verdict === 'wrong' && f1 >= JUDGE_EQUIVALENT_F1_THRESHOLD) return 'JUDGE_AMBIGUITY'
    if (it?.evidence_hit || it?.answer_in_context) return verdict === 'wrong' ? 'READER_GAP' : 'HIT'
    if (Number(it?.best_evidence_recall || 0) > 0 || Number(it?.cloud_hits || 0) > 0 || Number(it?.raw || 0) > 0 || Number(it?.bands || 0) > 0 || Number(it?.nodes || 0) > 0) {
        return 'RANK_CUTOFF'
    }
    return 'RECALL_MISS'
}

const rootCauseTagType = (key: EvalRootCauseKey) => {
    if (key === 'HIT') return 'success'
    if (key === 'RECALL_MISS') return 'danger'
    if (key === 'READER_GAP') return 'warning'
    return 'info'
}

const rootCauseHumanLabel = (key: EvalRootCauseKey) => {
    switch (key) {
        case 'HIT':
            return isEn.value ? '✓ HIT (Evidence / Answer Hit)' : '✓ 命中 (HIT)'
        case 'READER_GAP':
            return isEn.value ? 'READER_GAP (Evidence Recalled, LLM Wrong)' : '证据已召回但答题错 (READER_GAP)'
        case 'RANK_CUTOFF':
            return isEn.value ? 'RANK_CUTOFF (Recalled Outside Top-K)' : '候选有命中未进前K (RANK_CUTOFF)'
        case 'RECALL_MISS':
            return isEn.value ? 'RECALL_MISS (Not Recalled)' : '检索未命中 (RECALL_MISS)'
        case 'JUDGE_AMBIGUITY':
            return isEn.value ? 'JUDGE_AMBIGUITY (Equivalent / Unparsed)' : '实质等价或判词歧义 (JUDGE_AMBIGUITY)'
    }
    return key
}

const inspectBucketQuestions = async (record: EvalRunRecord | null | undefined, bucket: EvalRootCauseKey) => {
    const target = record || activeRecord.value
    if (!target) return
    detailFilter.verdict = 'all'
    detailFilter.category = ''
    detailFilter.keyword = ''
    detailFilter.rootCause = bucket
    await inspectRunDetails(target)
}

const rootCauseNote = computed(() =>
    isEn.value
        ? 'Root-cause classification ladder: verdict = correct → HIT; unparsed/error or wrong with F1 ≥ 0.5 (materially equivalent) → JUDGE_AMBIGUITY; evidence present in delivered context → wrong = READER_GAP else HIT; evidence absent from delivered window but retrieval produced candidate hits → RANK_CUTOFF; retrieval returned empty → RECALL_MISS.'
        : '错题归因五分类规则：判词 correct → HIT；解析异常或判词 wrong 但词面 F1 ≥ 0.5（实质等价）→ JUDGE_AMBIGUITY；证据已进入交付窗口但答题错误 → READER_GAP；检索有候选产出但证据未进入交付窗口 → RANK_CUTOFF；检索完全无命中 → RECALL_MISS。'
)

const rankCutoffNote = computed(() =>
    isEn.value
        ? 'RANK_CUTOFF = retrieval returned candidate hits, but the target evidence did not land inside the delivered top-K window.'
        : 'RANK_CUTOFF = 检索阶段有候选命中，但目标证据未进入最终交付窗口。'
)

const regressionNote = computed(() =>
    isEn.value
        ? 'Per-question diff against Run A by item.id. Outcome uses judge verdict when present, otherwise evidence_hit. Regressed = right → wrong; Fixed = wrong → right.'
        : '按 item.id 与 Run A 逐题对比：对错信号优先取判词，缺判词时回落到证据命中。Regressed = 原对→现错，Fixed = 原错→现对。'
)

const compareItemsById = computed<Map<string, EvalQuestionResultItem>>(() => {
    const map = new Map<string, EvalQuestionResultItem>()
    for (const it of compareItems.value) {
        if (it && it.id) map.set(it.id, it)
    }
    return map
})

const compareItemOf = (row: any): EvalQuestionResultItem | undefined => compareItemsById.value.get(row?.id)

// outcomeOf 单题对错信号：有判词取判词，否则回落证据命中。
const outcomeOf = (it: any): EvalRunOutcome => {
    if (it.judge === 'correct') return 'correct'
    if (it.judge === 'wrong') return 'wrong'
    if (it.judge === 'error') return 'error'
    return it.evidence_hit ? 'ev_hit' : 'ev_miss'
}

const isGoodOutcome = (o: EvalRunOutcome) => o === 'correct' || o === 'ev_hit'
const isBadOutcome = (o: EvalRunOutcome) => o === 'wrong' || o === 'error' || o === 'ev_miss'

const diffKindOf = (row: any): EvalDiffKind => {
    const base = compareItemOf(row)
    if (!base) return 'absent'
    const ob = outcomeOf(base)
    const oc = outcomeOf(row)
    if (isGoodOutcome(ob) && isBadOutcome(oc)) return 'regressed'
    if (isBadOutcome(ob) && isGoodOutcome(oc)) return 'fixed'
    return ob === oc ? 'same' : 'changed'
}

const diffKindLabel = (kind: EvalDiffKind) => {
    if (kind === 'regressed') return isEn.value ? '↘ Regressed' : '↘ 退化'
    if (kind === 'fixed') return isEn.value ? '↗ Fixed' : '↗ 修复'
    if (kind === 'same') return isEn.value ? '= same' : '= 持平'
    if (kind === 'changed') return isEn.value ? '≠ changed' : '≠ 变化'
    return isEn.value ? 'absent' : '未含'
}

const diffTagType = (kind: EvalDiffKind) => {
    if (kind === 'regressed') return 'danger'
    if (kind === 'fixed') return 'success'
    if (kind === 'changed') return 'warning'
    return 'info'
}

const detailStats = computed(() => {
    let evHit = 0
    let evMiss = 0
    let correct = 0
    let wrong = 0
    let recallMiss = 0
    let rankCutoff = 0
    let readerGap = 0
    let judgeAmbiguity = 0
    let hit = 0
    for (const it of detailItems.value) {
        if (it.evidence_hit) evHit++
        else evMiss++
        if (it.judge === 'correct') correct++
        else if (it.judge === 'wrong') wrong++
        const bucket = rootCauseOf(it)
        if (bucket === 'RECALL_MISS') recallMiss++
        else if (bucket === 'RANK_CUTOFF') rankCutoff++
        else if (bucket === 'READER_GAP') readerGap++
        else if (bucket === 'JUDGE_AMBIGUITY') judgeAmbiguity++
        else hit++
    }
    return { evHit, evMiss, correct, wrong, recallMiss, rankCutoff, readerGap, judgeAmbiguity, hit }
})

// backendBucketCounts 后端为整场运行落档的五桶计数（台账 repor 用；缺省时由 detailStats 计）。
const backendBucketCounts = computed<Record<string, number> | null>(() => detailRecord.value?.bucket_counts || null)

const diffStats = computed(() => {
    let regressed = 0
    let fixed = 0
    let same = 0
    let changed = 0
    let absent = 0
    for (const it of detailItems.value) {
        const kind = diffKindOf(it)
        if (kind === 'regressed') regressed++
        else if (kind === 'fixed') fixed++
        else if (kind === 'same') same++
        else if (kind === 'changed') changed++
        else absent++
    }
    return { regressed, fixed, same, changed, absent }
})

const diffSummaryText = computed(() => {
    const s = diffStats.value
    return isEn.value
        ? `Fixed ${s.fixed} · Regressed ${s.regressed} · Changed ${s.changed} · Same ${s.same} · Only in Run B ${s.absent}`
        : `修复 ${s.fixed} · 退化 ${s.regressed} · 变化 ${s.changed} · 持平 ${s.same} · 仅 Run B 有 ${s.absent}`
})

// syntheticDetailWarning 封印基线提示：内置基线只有聚合读数，后端不会伪造逐题行
// （loadRunDetailItems 的 Strict Falsification Guard），因此基线不能作为逐题 Δ 的 Run A/B。
const syntheticDetailWarning = computed(() => {
    const runs = allLedgerRows.value
    const curr = detailRecord.value || runs.find((r) => r.id === detailRunId.value)
    const base = runs.find((r) => r.id === compareRunId.value)
    const parts: string[] = []
    if (base?.is_baseline) {
        parts.push(isEn.value ? `Run A "${loc(base.label, base.label_en)}" is a sealed baseline` : `Run A「${loc(base.label, base.label_en)}」为封印基线`)
    }
    if (curr?.is_baseline) {
        parts.push(isEn.value ? `Run B "${loc(curr.label, curr.label_en)}" is a sealed baseline` : `Run B「${loc(curr.label, curr.label_en)}」为封印基线`)
    }
    if (parts.length === 0) return ''
    return isEn.value
        ? `${parts.join(' · ')}: sealed baselines expose aggregate readings only — there are no persisted per-question rows, so per-question Δ is unavailable.`
        : `${parts.join(' · ')}：封印基线只有聚合读数，没有落盘的逐题明细，因此逐题 Δ 不可用。`
})

const detailPage = ref(1)
const detailPageSize = ref(50)

const filteredDetailItems = computed(() => {
    const kw = detailFilter.keyword.trim().toLowerCase()
    return detailItems.value.filter((it) => {
        if (detailFilter.category && it.category !== detailFilter.category) return false
        if (detailFilter.verdict === 'ev_hit' && !it.evidence_hit) return false
        if (detailFilter.verdict === 'ev_miss' && it.evidence_hit) return false
        if (detailFilter.verdict === 'correct' && it.judge !== 'correct') return false
        if (detailFilter.verdict === 'wrong' && it.judge !== 'wrong') return false
        if (detailFilter.rootCause !== 'all' && rootCauseOf(it) !== detailFilter.rootCause) return false
        if (compareRunId.value && detailFilter.diff !== 'all' && diffKindOf(it) !== detailFilter.diff) return false
        if (kw) {
            const hay = `${it.id} ${it.question} ${it.gold} ${it.prediction || ''} ${it.judge_reason || ''}`.toLowerCase()
            if (!hay.includes(kw)) return false
        }
        return true
    })
})

const pagedDetailItems = computed(() => {
    const list = filteredDetailItems.value
    const start = (detailPage.value - 1) * detailPageSize.value
    return list.slice(start, start + detailPageSize.value)
})

watch(
    () => [detailFilter.category, detailFilter.verdict, detailFilter.rootCause, detailFilter.diff, detailFilter.keyword, detailRunId.value],
    () => {
        detailPage.value = 1
    }
)

const chartRecords = computed<EvalRunRecord[]>(() => {
    // 实测历史优先于封印基线：新实测不再因「基线 + 前 2 条」截断从图上消失；
    // 槽位自适应 chartSlotWidth，总量以画布可读性上限 9 根封顶（并非丢弃台账，表格仍全量）。
    const records = [...visibleRunHistory.value, ...visibleBaselines.value]
    return records.slice(0, 9)
})

// chartSlotWidth 按柱数均分 740 宽画布的绘图区（x∈[68,720]），9 根以内每根 ≥72px 保持可读。
const chartSlotWidth = computed(() => {
    const n = Math.max(chartRecords.value.length, 1)
    return Math.min(92, Math.floor(652 / n))
})

const activeRecord = computed<EvalRunRecord | null>(() => {
    if (selectedRecord.value && isRecordInCurrentSuite(selectedRecord.value) && (includeArchivedTiers.value || !isRetiredRecord(selectedRecord.value))) return selectedRecord.value
    return visibleRunHistory.value[0] || visibleBaselines.value[0] || null
})

const categoryRows = computed(() => {
    const rec = activeRecord.value
    const ev = rec?.category_evidence || { cat1: 0, cat2: 0, cat3: 0, cat4: 0 }
    const jdg = rec?.category_judge || { cat1: 0, cat2: 0, cat3: 0, cat4: 0 }
    if (query.suite === 'atm') {
        return [
            {
                key: 'cat1',
                label: isEn.value ? 'Cat 1 · list_recall (Evidence ID List Jaccard)' : 'Cat 1 · 证据ID列表枚举 (list_recall · Jaccard)',
                ev: ev.cat1,
                jdg: jdg.cat1,
            },
            {
                key: 'cat2',
                label: isEn.value ? 'Cat 2 · number (Deterministic Numeric/Date EM)' : 'Cat 2 · 数值与日期推理 (number · 确定性 EM)',
                ev: ev.cat2,
                jdg: jdg.cat2,
            },
            {
                key: 'cat3',
                label: isEn.value ? 'Cat 3 · open_end (Open-Ended LLM Judge)' : 'Cat 3 · 多模态开放问答 (open_end · LLM Judge)',
                ev: ev.cat3,
                jdg: jdg.cat3,
            },
        ]
    }
    return [
        { key: 'cat4', label: t('plugin.rolling-memory.eval.cat4Label'), ev: ev.cat4, jdg: jdg.cat4 },
        { key: 'cat2', label: t('plugin.rolling-memory.eval.cat2Label'), ev: ev.cat2, jdg: jdg.cat2 },
        { key: 'cat1', label: t('plugin.rolling-memory.eval.cat1Label'), ev: ev.cat1, jdg: jdg.cat1 },
        { key: 'cat3', label: t('plugin.rolling-memory.eval.cat3Label'), ev: ev.cat3, jdg: jdg.cat3 },
    ]
})

const isLocalhostUrl = (u: string) => {
    const s = String(u || '').toLowerCase()
    return s.includes('127.0.0.1') || s.includes('localhost') || s.includes('0.0.0.0')
}

// 前端即时校验模型配置是否满足双智能体运行条件
const isAIConfigReadyLocal = computed(() => {
    const base = String(aiForm.base_url || '').trim()
    if (!base.startsWith('http://') && !base.startsWith('https://')) return false
    if (!String(aiForm.answer_model || '').trim() || !String(aiForm.judge_model || '').trim()) return false
    const hasKey = Boolean((aiForm.api_key && !aiForm.api_key.includes('***')) || aiForm.has_api_key)
    if (!isLocalhostUrl(base) && !hasKey) return false
    return true
})

const openAIConfigModal = async (fromBlocked: '' | 'batch' | 'probe' | 'switch' | false = false) => {
    aiModalBlockedAction.value = fromBlocked || ''
    try {
        const res = await request<{ ai_check: AIModelCheckResult; ai_config: AIModelConfig }>({
            url: '/admin/plugin/rolling-memory/eval/run',
            method: 'POST',
            data: { mode: 'check_ai', ai_config: aiForm },
        })
        if (res.data.data?.ai_check) {
            overview.value.ai_check = res.data.data.ai_check
        }
    } catch {
        ElMessage.warning(isEn.value ? 'Could not refresh the AI configuration check; showing the last known state.' : '刷新 AI 配置预检失败，当前显示上次已知状态。')
    }
    aiModalVisible.value = true
}

const onUseAgentsSwitchChange = async (val: any) => {
    if (val && !isAIConfigReadyLocal.value) {
        await openAIConfigModal('switch')
    }
}

const onRunModeChange = async (value: string | number | boolean | undefined) => {
    const enabled = value === 'answer'
    useAgents.value = enabled
    if (enabled) await onUseAgentsSwitchChange(true)
}

const selectedTierLabel = computed(() => {
    if (query.suite === 'atm') return query.tier === '1013' ? 'ATM-Bench · Main 1013' : 'ATM-Bench · Hard 31'
    return isEn.value ? 'LoCoMo · Official 1540 (cat1-4)' : 'LoCoMo · 官方 1540（cat1-4）'
})
const selectedTierDescription = computed(() => {
    if (query.suite === 'atm') {
        return query.tier === '1013'
            ? (isEn.value ? 'The complete official 1,013-question set for full reproduction runs.' : '官方完整 1,013 题，适合正式复现与完整对照。')
            : (isEn.value ? 'The official 31-question hard subset for focused, difficult-case checks.' : '官方 31 道高难题，适合集中检查困难样本。')
    }
    return isEn.value
        ? 'The upstream-official Cat 1–4 question set (1,540 questions) with official evidence; the 100/200 convenience tiers are retired.'
        : '上游官方 cat1-4 全集（1,540 题，随附官方证据）；便捷档 100/200 不再提供。'
})
const corpusStatusLabel = computed(() => {
    if (evalConfig.arm_mode === 'oracle') return isEn.value ? 'Gold evidence ceiling' : '金标证据上限'
    if (evalCorpusReady.value) return isEn.value ? 'Corpus ready' : '语料已就绪'
    return isEn.value ? 'Preparation required' : '需要准备语料'
})

const fillModelPreset = (preset: 'deepseek' | 'openai' | 'ollama') => {
    aiTestResult.value = null
    if (preset === 'deepseek') {
        aiForm.base_url = 'https://api.deepseek.com/v1'
        aiForm.api_key = ''
        aiForm.has_api_key = false
        aiForm.answer_model = 'deepseek-chat'
        aiForm.judge_model = 'deepseek-chat'
    } else if (preset === 'openai') {
        aiForm.base_url = 'https://api.openai.com/v1'
        aiForm.api_key = ''
        aiForm.has_api_key = false
        aiForm.answer_model = 'gpt-4.1'
        aiForm.judge_model = 'gpt-4.1'
    } else if (preset === 'ollama') {
        aiForm.base_url = 'http://127.0.0.1:11434/v1'
        aiForm.api_key = 'ollama'
        aiForm.has_api_key = true
        aiForm.answer_model = 'qwen2.5:7b'
        aiForm.judge_model = 'qwen2.5:7b'
    }
}
watch(aiForm, () => {
    aiTestResult.value = null
}, { deep: true })

const refreshAll = () => {
    void loadOverview()
}

const portableStorePath = (value: string) => {
    const trimmed = String(value || '').trim()
    return trimmed.replace(/^(?:\/Users\/[^/]+|\/home\/[^/]+)(?=\/)/, '~')
}
const normalizeReproCommand = (raw: string) => {
    return String(raw || '')
        .trim()
        .replace(/^(?:\.\/)?bin\/rmem(?=\s)/, 'rmem')
        .replace(/(?:\/Users\/[^/\s]+|\/home\/[^/\s]+)(?=\/\.local\/)/g, '~')
}
// latestReproCli 复现命令单一真源：本套件最近一次落档运行随读数同批生成的 repro_cli。
// （前端自拼「草稿命令」已删除——其自认缺少题型过滤/Judge 模型/超时/备注，属不等价第二真源。）
const latestReproCli = computed(() => {
    for (const r of visibleRunHistory.value) {
        const cli = normalizeReproCommand(String(r.repro_cli || ''))
        if (isCurrentReproCli(cli)) return cli
    }
    const base = visibleBaselines.value.find((r) => isCurrentReproCli(normalizeReproCommand(String(r.repro_cli || ''))))
    return base ? normalizeReproCommand(String(base.repro_cli || '')) : ''
})

const isCurrentReproCli = (value: string) => {
    const cli = String(value || '').trim()
    // 后端落档命令带仓库内二进制前缀（./bin/rmem 或 bin/rmem），验收时两种都算现行 SDK 复现命令。
    if (!/^(?:\.\/)?(?:bin\/)?rmem\s+eval(?:\s|$)/.test(cli)) return false
    const allowedFlags = new Set(['--suite', '--tier', '--eval-store', '--top-k', '--answerer', '--judge', '--eval-model', '--eval-base-url', '--oracle', '--workers', '--pulse-mode'])
    for (const match of cli.matchAll(/(?:^|\s)(--[a-z0-9-]+)/gi)) {
        if (!allowedFlags.has(match[1] || '')) return false
    }
    return /(?:^|\s)--eval-store(?:=|\s)/.test(cli) && /(?:^|\s)--top-k(?:=|\s)/.test(cli)
}

// copyText 复制文本：照同仓样板 roi.vue:1135-1142 的写法（clipboard + ElMessage 回显全文，
// 失败降级用 execCommand 兜底后 ElMessage.info 全文）。插件视图只依赖 @/utils/request 与 @/stores/adminInfo，
// 故此处内联降级实现（与 admin/web/src/utils/common.ts:70 的 copy() 同算法），不新增跨模块宿主依赖。
const copyText = async (text: string, enPrefix = 'Copied', zhPrefix = '已复制') => {
    const value = String(text || '')
    if (value === '') return
    let copied = false
    try {
        await navigator.clipboard.writeText(value)
        copied = true
    } catch {
        const textarea = document.createElement('textarea')
        textarea.value = value
        textarea.style.position = 'fixed'
        textarea.style.left = '-9999px'
        document.body.appendChild(textarea)
        textarea.focus()
        textarea.select()
        try {
            copied = document.execCommand('copy')
        } catch {
            copied = false
        } finally {
            document.body.removeChild(textarea)
        }
    }
    if (copied) {
        ElMessage.success(`${isEn.value ? enPrefix : zhPrefix}: ${value}`)
    } else {
        ElMessage.info(value)
    }
}

// 历史记录若仍携带旧命令，只保留成绩台账，不展示为现行 SDK 复现命令。
const detailReproCli = computed(() => {
    const cli = normalizeReproCommand(String(detailRecord.value?.repro_cli || ''))
    return isCurrentReproCli(cli) ? cli : ''
})

// bucketSummaryText 本场五桶计数摘要：优先取后端落档 bucket_counts，缺省时由明细前端兜底统计。
const bucketSummaryText = computed(() => {
    const backend = backendBucketCounts.value
    const s = detailStats.value
    const hit = backend ? Number(backend.HIT || 0) : s.hit
    const recallMiss = backend ? Number(backend.RECALL_MISS || 0) : s.recallMiss
    const rankCutoff = backend ? Number(backend.RANK_CUTOFF || 0) : s.rankCutoff
    const readerGap = backend ? Number(backend.READER_GAP || 0) : s.readerGap
    const judgeAmbiguity = backend ? Number(backend.JUDGE_AMBIGUITY || 0) : s.judgeAmbiguity
    const src = backend ? (isEn.value ? 'backend ledger' : '后端落档') : isEn.value ? 'computed from loaded rows' : '按已加载明细前端统计'
    return isEn.value
        ? `Buckets (${src}): HIT ${hit} · RECALL_MISS ${recallMiss} · RANK_CUTOFF ${rankCutoff} · READER_GAP ${readerGap} · JUDGE_AMBIGUITY ${judgeAmbiguity}`
        : `五桶计数（${src}）：HIT ${hit} · RECALL_MISS ${recallMiss} · RANK_CUTOFF ${rankCutoff} · READER_GAP ${readerGap} · JUDGE_AMBIGUITY ${judgeAmbiguity}`
})

let overviewRequestID = 0
const loadOverview = async () => {
    const requestID = ++overviewRequestID
    loading.value = true
    overviewLoadError.value = ''
    try {
        const res = await request<EvalOverviewResponse>({
            url: '/admin/plugin/rolling-memory/eval/overview',
            method: 'POST',
            data: { ...query, arm_mode: evalConfig.arm_mode, pulse_mode: evalConfig.pulse_mode },
            // 首次核验要等待本地金库探针；与控制台 status 子进程的 45s 硬上限一致。
            timeout: 45000,
        })
        if (requestID !== overviewRequestID) return
        const data = res.data.data ?? ({} as EvalOverviewResponse)
        overview.value = data
        // 本地模式只开放 local_plain：若历史选择停在脉冲臂，落到本地臂（触发一次按 local_plain 重载）。
        if (isLocalMode.value && evalConfig.arm_mode !== 'sdk-local-plain' && evalConfig.arm_mode !== 'oracle') {
            evalConfig.arm_mode = 'sdk-local-plain'
            return
        }
        if (data.ai_config) {
            Object.assign(aiForm, data.ai_config)
        }
        if (data.active_job && data.active_job.status === 'running') {
            Object.assign(liveJob, data.active_job)
            attachStreamEval()
        }
        const firstRun = (data.run_history || []).find((record) => includeArchivedTiers.value || !isRetiredRecord(record))
        const firstBaseline = (data.baselines || []).find((record) => includeArchivedTiers.value || !isRetiredRecord(record))
        if (!selectedRecord.value) {
            selectedRecord.value = firstRun || firstBaseline || null
        }
        if (!detailRunId.value && firstRun?.id) {
            // 只默认到当前提供题集的真实运行；归档基线没有逐题明细。
            detailRunId.value = firstRun.id
        }
        if (!detailRunId.value) {
            detailAvailable.value = false
            detailReason.value = isEn.value ? 'Pick a run to inspect per-question detail.' : '请选择一场运行查看逐题明细。'
        }
        if (detailRunId.value && detailItems.value.length === 0) {
            void loadRunDetailList(detailRunId.value)
        }
    } catch (error: any) {
        if (requestID === overviewRequestID) {
            overviewLoadError.value = String(error?.message || (isEn.value ? 'Could not load this benchmark' : '当前基准加载失败'))
        }
    } finally {
        if (requestID === overviewRequestID) loading.value = false
    }
}

const saveAIConfig = async () => {
    savingAI.value = true
    try {
        const res = await request<{ ai_config: AIModelConfig; ai_check: AIModelCheckResult; agents: OfficialProtocolAgent[] }>({
            url: '/admin/plugin/rolling-memory/ai/save',
            method: 'POST',
            data: aiForm,
        })
        const d = res.data.data
        if (d?.ai_config) Object.assign(aiForm, d.ai_config)
        if (d?.ai_check) overview.value.ai_check = d.ai_check
        if (d?.agents) overview.value.agents = d.agents
        ElMessage.success(isEn.value ? 'Model configuration saved' : 'AI 模型通道配置已保存')
        return d?.ai_check?.ready ?? false
    } catch {
        // 业务与网络错误由请求拦截器统一提示；保存失败按「配置未就绪」返回给调用方。
        return false
    } finally {
        savingAI.value = false
    }
}

const runAIConnectionTest = async () => {
    testingAI.value = true
    aiTestResult.value = null
    try {
        const res = await request<AIConnectionTestResponse>({
            url: '/admin/plugin/rolling-memory/eval/run',
            method: 'POST',
            data: { mode: 'test_ai', ai_config: aiForm },
            // 连接测试会真实调用 Answer + Judge 两段冻结协议；后端按 timeout_sec 等待（默认 60s，可配 300s），
            // 不能沿用全局 10s 超时，否则慢模型会被前端提前掐断。
            timeout: Math.max(30, Number(aiForm.timeout_sec || 60)) * 1000 + 15000,
        })
        const d = res.data.data
        if (d) {
            aiTestResult.value = d
            if (d.config_check) overview.value.ai_check = d.config_check
            if (d.success) {
                ElMessage.success(loc(d.message, d.message_en))
            } else {
                ElMessage.warning(loc(d.message, d.message_en))
            }
        }
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        testingAI.value = false
    }
}

const saveAIConfigAndResume = async () => {
    const ready = await saveAIConfig()
    if (!ready) {
        ElMessage.warning(
            loc(overview.value.ai_check?.summary, overview.value.ai_check?.summary_en) ||
                (isEn.value ? 'Configuration still incomplete' : '模型配置仍未完整，请检查必填项')
        )
        return
    }
    const pendingAction = aiModalBlockedAction.value
    aiModalVisible.value = false
    aiModalBlockedAction.value = ''
    if (pendingAction === 'batch') {
        await requestRunBatchEval()
    } else if (pendingAction === 'probe') {
        await executeProbe(true)
    }
}

const fallbackToRetrievalOnlyEval = async () => {
    useAgents.value = false
    const pendingAction = aiModalBlockedAction.value
    aiModalVisible.value = false
    aiModalBlockedAction.value = ''
    if (pendingAction === 'batch') {
        await requestRunBatchEval()
    } else if (pendingAction === 'probe') {
        await executeProbe(false)
    }
}

// applyAIPluginChannel 一键接入 AI 路由插件渠道：服务端读取该渠道生效凭证（渠道优先、空则继承厂商）
// 写入评测配置；明文密钥不出服务端，本侧只回显掩码与新预检结果。
const applyAIPluginChannel = async (option: AIPluginChannelOption) => {
    applyingChannelID.value = option.channel_id
    try {
        const res = await request<{
            ai_config: AIModelConfig
            ai_check: AIModelCheckResult
            applied?: { channel_id: number; base_url: string; model: string; models: string[] }
            has_key?: boolean
        }>({
            url: '/admin/plugin/rolling-memory/eval/run',
            method: 'POST',
            data: {
                mode: 'ai_apply_channel',
                channel_id: option.channel_id,
                apply_model: pluginChannelModelPick[option.channel_id] || '',
            },
        })
        const data = res.data.data
        if (data?.ai_config) {
            Object.assign(aiForm, {
                provider: data.ai_config.provider || aiForm.provider,
                base_url: data.ai_config.base_url || aiForm.base_url,
                api_key: '',
                has_api_key: !!data.ai_config.has_api_key,
                answer_model: data.ai_config.answer_model || aiForm.answer_model,
                judge_model: data.ai_config.judge_model || aiForm.judge_model,
                max_tokens: data.ai_config.max_tokens || aiForm.max_tokens,
                timeout_sec: data.ai_config.timeout_sec || aiForm.timeout_sec,
            })
        }
        if (data?.ai_check) {
            overview.value = { ...overview.value, ai_check: data.ai_check }
        }
        aiTestResult.value = null
        const model = data?.applied?.model || option.upstream_name || ''
        ElMessage.success(
            isEn.value
                ? `Channel "${option.channel_name}" applied (model: ${model})`
                : `已接入 AI 渠道「${option.channel_name}」（模型：${model}）`
        )
    } catch (error: any) {
        ElMessage.error(error?.message || (isEn.value ? 'Apply channel failed' : '接入 AI 渠道失败'))
    } finally {
        applyingChannelID.value = null
    }
}

const onSuiteChange = () => {
    overview.value = {
        ...overview.value,
        manifest: null,
        readiness: undefined,
        store_path: '',
        baselines: [],
        run_history: [],
        questions: [],
        total_questions: 0,
    }
    overviewLoadError.value = ''
    query.page = 1
    query.category = ''
    evalConfig.category = ''
    selectedRecord.value = null
    detailRunId.value = ''
    detailItems.value = []
    compareRunId.value = ''
    compareItems.value = []
    compareAvailable.value = true
    compareReason.value = ''
    corpusModalVisible.value = false
    if (query.suite === 'atm') {
        query.tier = 'hard'
        evalConfig.top_k = 50
        probeTopK.value = 50
    } else {
        query.tier = '1540'
        evalConfig.top_k = 20
        probeTopK.value = 20
    }
    void loadOverview()
}

const onTierChange = () => {
    query.page = 1
    void loadOverview()
}

const onFilterChange = () => {
    query.page = 1
    loadOverview()
}

const runIngest = async (action: 'prepare' | 'verify' | 'rebuild') => {
    ingesting.value = action
    startIngestClock()
    try {
        const res = await request<{
            elapsed_ms?: number
            manifest?: EvalManifest
            readiness?: EvalReadiness
            store_path?: string
        }>({
            url: '/admin/plugin/rolling-memory/eval/ingest',
            method: 'POST',
            data: { suite: query.suite, arm_mode: evalConfig.arm_mode, pulse_mode: evalConfig.pulse_mode, action },
            timeout: INGEST_TIMEOUT_MS,
        })
        const data = res.data.data
        if (data?.manifest || data?.readiness || data?.store_path) {
            overview.value = {
                ...overview.value,
                manifest: data.manifest || overview.value.manifest,
                readiness: data.readiness || overview.value.readiness,
                store_path: data.store_path || overview.value.store_path,
            }
        }
        await loadOverview()
        if (!evalCorpusReady.value) {
            ElMessage.warning(
                isEn.value
                    ? `${action} finished without a passing readiness/manifest verification; batch remains blocked.`
                    : `${action} 结束后 readiness/manifest 仍未通过，批次保持阻断。`
            )
            return false
        }
        ElMessage.success(isEn.value ? `${action} passed for ${evalScopeText.value}` : `${action} 已通过：${evalScopeText.value}`)
        return true
    } catch (error: any) {
        ElMessage.error(error?.message || (isEn.value ? `${action} failed; batch remains blocked` : `${action} 失败，批次保持阻断`))
        return false
    } finally {
        ingesting.value = ''
        stopIngestClock()
    }
}

const confirmRebuild = async () => {
    try {
        await ElMessageBox.confirm(
            isEn.value
                ? `Rebuild opens a new dedicated generation scope for this run (the previous generation is recycled once idle). It does not clear personal memory. Continue?`
                : `Rebuild 会为本次评测分配新的独立世代 scope（上一世代在空闲后自动回收），不会清空个人记忆。是否继续？`,
            isEn.value ? 'Confirm isolated Rebuild' : '确认隔离域 Rebuild',
            { type: 'warning', confirmButtonText: 'Rebuild', cancelButtonText: isEn.value ? 'Cancel' : '取消' }
        )
    } catch {
        return
    }
    await runIngest('rebuild')
}

let evalSocket: MonitorSocket | null = null
const { percent: evalLoadPct, text: evalTableLoadingText } = useLoadingProgressText(
    () => loading.value,
    () => (isEn.value ? `Loading ${query.suite === 'atm' ? 'ATM-Bench' : 'LoCoMo'} (${query.tier}) benchmark & readiness` : `正在加载 ${query.suite === 'atm' ? 'ATM-Bench' : 'LoCoMo'} (${query.tier}) 题集与就绪状态`),
    650
)
const { percent: ingestPct } = useLoadingProgressText(() => ingesting.value !== '', undefined, 2500)
const { percent: batchStartPct } = useLoadingProgressText(() => batchRunning.value && liveJob.status !== 'running', undefined, 500)
const { text: detailsLoadingText } = useLoadingProgressText(
    () => loadingDetails.value,
    () => (isEn.value ? 'Loading per-question diagnostics' : '正在加载逐题诊断明细'),
    500
)
const { text: compareLoadingText } = useLoadingProgressText(
    () => loadingCompare.value,
    () => (isEn.value ? 'Loading Run A comparison details' : '正在加载 Run A 对比明细'),
    500
)
const { percent: probePct, text: probeLoadingText } = useLoadingProgressText(
    () => probing.value,
    () => (probeWithAgents.value ? (isEn.value ? 'Running Recall + Answer + Judge probe' : '正在执行单题检索 + 作答 + 判分') : (isEn.value ? 'Running local recall probe' : '正在执行单题本地检索探针')),
    600
)
const { percent: testAIPct } = useLoadingProgressText(() => testingAI.value, undefined, 1500)
const { percent: saveAIPct } = useLoadingProgressText(() => savingAI.value, undefined, 400)
const { percent: liveIngestPhasePct } = useLoadingProgressText(() => liveJob.status === 'running' && liveJob.phase === 'ingest', undefined, 1800)

const overviewLoadStageTitle = computed(() =>
    isEn.value
        ? `Verifying ${query.suite === 'atm' ? 'Cambridge ATM-Bench' : 'LoCoMo'} (${query.tier}) workspace`
        : `正在核验 ${query.suite === 'atm' ? '剑桥 ATM-Bench' : 'LoCoMo'} (${query.tier}) 评测环境`
)
const overviewLoadStageDesc = computed(() =>
    isEn.value
        ? 'Checking isolated manifest, SHA-256 corpus pins, question set, and run ledger...'
        : '正在校验隔离域 manifest、SHA-256 语料钉、题集分页与历史运行台账…'
)
const ingestStageTitle = computed(() => {
    const act = ingesting.value.toUpperCase()
    const totalDocs = query.suite === 'atm' ? '11,034' : '5,882'
    if (ingesting.value === 'verify') {
        return isEn.value ? `Verifying isolated corpus (${totalDocs} docs)` : `正在核验隔离域语料完整性（${totalDocs} 条）`
    }
    if (ingesting.value === 'rebuild') {
        return isEn.value ? `Rebuilding isolated generation scope (${totalDocs} docs)` : `正在重建独立世代语料索引（${totalDocs} 条）`
    }
    return isEn.value ? `${act}: Preparing isolated corpus (${totalDocs} docs)` : `正在准备隔离域语料与本地索引（${totalDocs} 条）`
})
const ingestStageDesc = computed(() =>
    isEn.value
        ? 'Step 1: Verify SHA-256 digest · Step 2: Write isolated vault · Step 3: Run readiness probe (server task continues if you leave this page).'
        : '阶段 1：校验 SHA-256 语料指纹 → 阶段 2：写入隔离金库索引 → 阶段 3：执行就绪探针（离开页面不会中断服务端任务）。'
)
const liveDisplayPercent = computed(() => {
    if (liveJob.status === 'completed') return 100
    const raw = Math.min(100, Math.max(0, Number(liveJob.percent || 0)))
    if (raw > 0) return raw
    if (liveJob.status === 'running' && liveJob.phase === 'ingest') {
        return Math.min(15, Math.max(2, Math.round(liveIngestPhasePct.value * 0.15)))
    }
    return raw
})
const liveThroughputText = computed(() => {
    const done = Number(liveJob.done || 0)
    const elapsed = Number(liveJob.elapsed_seconds || 0)
    if (done <= 0 || elapsed <= 0) return ''
    const qps = done / elapsed
    return qps >= 1 ? `${qps.toFixed(1)} q/s` : `${(elapsed / done).toFixed(1)} s/q`
})

const currentSplitBenchmarkRef = computed(() => {
    if (query.suite === 'atm') {
        return query.tier === '1013'
            ? (isEn.value ? 'Rolling Memory v0.0.1 Any-Hit@10 80.36% / Official R@10 77.21% / QS 65.54% (vs MemPalace QS 56.80%)' : '我方基线 Any-Hit@10 80.36% / 官方 R@10 77.21% / QS 65.54%（外部基线 MemPalace QS 56.80%）')
            : (isEn.value ? 'Rolling Memory v0.0.1 Any-Hit@10 80.65% / Official R@10 46.12% / QS 27.38% (vs MemoryOS QS 13.70%)' : '我方基线 Any-Hit@10 80.65% / 官方 R@10 46.12% / QS 27.38%（外部基线 MemoryOS QS 13.70%）')
    }
    return isEn.value ? 'Current baseline: 1313/1540 = 85.26% on the full 1,540 set (deepseek-v4.1-flash judge); historical reference: LLM+vector architecture 92.79% (no longer provided).' : '现行基线：1,540 全量判对 1313/1540 = 85.26%（deepseek-v4.1-flash 判官）；历史对照：LLM + 向量架构 92.79%（不再提供）。'
})

const viewCompletedRunResults = () => {
    if (liveJob.record) {
        selectedRecord.value = liveJob.record
        detailRunId.value = liveJob.record.id
    }
    activeTab.value = 'results'
}

const inspectCompletedRunQuestions = async () => {
    const rec = liveJob.record || selectedRecord.value || visibleRunHistory.value[0]
    if (rec) {
        await inspectRunDetails(rec)
    } else {
        activeTab.value = 'results'
    }
}

const guideToLLMEval = async () => {
    activeTab.value = 'run'
    useAgents.value = true
    if (!isAIConfigReadyLocal.value) {
        await openAIConfigModal('switch')
    } else {
        ElMessage.info(isEn.value ? 'Switched to Step 2 (Retrieval + LLM Answer + Judge). Click "Start evaluation" to run.' : '已切换为「第二步 · 检索 + 大模型作答与评分」，点击右侧「开始评测」即可运行。')
    }
}

const stopEvalStreamClient = () => {
    if (evalSocket) {
        evalSocket.stop()
        evalSocket = null
    }
}

const subscribeEvalRoom = (expectedJobId: string, onTerminalState?: (snap: EvalJobStatus) => void) => {
    stopEvalStreamClient()
    const token = adminInfo.token
    if (!token) return
    evalSocket = new MonitorSocket({
        token,
        room: 'rm/eval',
        onMessage: (env: any) => {
            if ((env?.event === 'eval_progress' || env?.event === 'eval_done') && env.data) {
                const snap = env.data as EvalJobStatus
                if (expectedJobId && snap.job_id && snap.job_id !== expectedJobId) {
                    return
                }
                Object.assign(liveJob, snap)
                if (env.event === 'eval_done' || snap.status === 'completed' || snap.status === 'cancelled' || snap.status === 'failed' || snap.status === 'idle') {
                    onTerminalState?.(snap)
                }
            }
        },
    })
    evalSocket.start()
}

const requestRunBatchEval = async () => {
    // Oracle 只读金标证据，不经过云端语料，后端 worker 对 oracle 明确跳过 prepare；
    // 因此语料门禁只约束 sdk-pulse-cipher 臂，不能把唯一不依赖语料的臂一并挡死。
    if (evalConfig.arm_mode !== 'oracle' && !evalCorpusReady.value) {
        corpusModalVisible.value = true
        ElMessage.warning(isEn.value ? 'Prepare and Verify must pass before starting a batch' : '必须先完成 Prepare 并通过 Verify 才能启动批次')
        return
    }
    if (useAgents.value) {
        const res = await request<{ ai_check: AIModelCheckResult }>({
            url: '/admin/plugin/rolling-memory/eval/run',
            method: 'POST',
            data: { mode: 'check_ai', ai_config: aiForm },
        })
        const chk = res.data.data?.ai_check
        if (chk) overview.value.ai_check = chk
        if (!chk?.ready) {
            aiModalBlockedAction.value = 'batch'
            aiModalVisible.value = true
            return
        }
    }
    await startStreamBatchEval()
}

// prepareAndStartBatchEval 供普通用户在语料尚未准备时一键完成「Prepare 写入语料 + Verify 核验 + 自动开跑」。
const prepareAndStartBatchEval = async () => {
    if (useAgents.value) {
        try {
            const res = await request<{ ai_check: AIModelCheckResult }>({
                url: '/admin/plugin/rolling-memory/eval/run',
                method: 'POST',
                data: { mode: 'check_ai', ai_config: aiForm },
            })
            const chk = res.data.data?.ai_check
            if (chk) overview.value.ai_check = chk
            if (!chk?.ready) {
                aiModalBlockedAction.value = 'batch'
                aiModalVisible.value = true
                return
            }
        } catch {
            return
        }
    }
    const ok = await runIngest('prepare')
    if (ok && evalCorpusReady.value) {
        await startStreamBatchEval()
    }
}

const startStreamBatchEval = async () => {
    if (evalConfig.arm_mode !== 'oracle' && !evalCorpusReady.value) {
        batchRunning.value = false
        corpusModalVisible.value = true
        return
    }
    stopEvalStreamClient()
    batchRunning.value = true
    try {
        const res = await request<{ job?: EvalJobStatus }>({
            url: '/admin/plugin/rolling-memory/eval/run',
            method: 'POST',
            data: {
                action: 'start',
                mode: 'batch',
                suite: query.suite,
                tier: query.tier,
                arm_mode: evalConfig.arm_mode,
                top_k: evalConfig.top_k,
                eval_store: evalStorePath.value,
                scope: evalScopeText.value,
                workers: evalConfig.workers,
                category: evalConfig.category,
                use_agents: useAgents.value,
                limit: batchLimit.value,
                custom_label: evalConfig.custom_label,
                ai_config: aiForm,
            },
            timeout: 0,
        })
        const startedJobId = res.data?.data?.job?.job_id || ''
        if (res.data?.data?.job) {
            Object.assign(liveJob, res.data.data.job)
        }
        subscribeEvalRoom(startedJobId, (snap) => {
            if (snap.status === 'idle') return
            batchRunning.value = false
            stopEvalStreamClient()
            if (snap.record) {
                selectedRecord.value = snap.record
                detailRunId.value = snap.record.id
            }
            loadOverview()
            if (snap.status === 'completed') {
                ElMessage.success(loc(snap.phase_text, snap.phase_text_en))
            } else if (snap.status === 'failed' && snap.error) {
                ElMessage.error(snap.error)
            }
        })
    } catch {
        batchRunning.value = false
        stopEvalStreamClient()
    }
}

const attachStreamEval = () => {
    subscribeEvalRoom(liveJob.job_id || '', (snap) => {
        if (snap.status !== 'running') {
            stopEvalStreamClient()
            loadOverview()
        }
    })
}

const stopRunningEval = async (savePartial: boolean) => {
    stopEvalStreamClient()
    batchRunning.value = false
    const res = await request<{ job: EvalJobStatus; record?: EvalRunRecord; run_history: EvalRunRecord[] }>({
        url: '/admin/plugin/rolling-memory/eval/run',
        method: 'POST',
        data: {
            mode: 'stop',
            save_partial: savePartial,
            suite: query.suite,
            tier: query.tier,
            arm_mode: evalConfig.arm_mode,
            top_k: evalConfig.top_k,
            eval_store: evalStorePath.value,
            scope: evalScopeText.value,
            use_agents: useAgents.value,
        },
    })
    if (res.data.data?.job) Object.assign(liveJob, res.data.data.job)
    if (res.data.data?.run_history) overview.value.run_history = res.data.data.run_history
    if (res.data.data?.record) selectedRecord.value = res.data.data.record
    ElMessage.info(savePartial ? (isEn.value ? 'Stopped and saved completed questions' : '已停止评测并将已完成题目保存至台账') : isEn.value ? 'Evaluation stopped' : '评测已停止')
}

// onLedgerRowClick 台账行点击：必须同时切换明细运行与表头，避免“表头是 A、表格还是 B”。
const onLedgerRowClick = async (row: any) => {
    selectedRecord.value = row
    if (!row?.id) return
    detailRunId.value = row.id
    await loadRunDetailList(row.id)
}

// deleteRunRecord 删除一条实测运行（封印基线由后端拒绝）；明细文件一并清理。
const deleteRunRecord = async (row: any) => {
    if (!row || row.is_baseline) return
    try {
        await ElMessageBox.confirm(
            isEn.value
                ? `Delete run "${loc(row.label, row.label_en)}" and its per-question detail file? Sealed baselines are not affected.`
                : `删除运行「${loc(row.label, row.label_en)}」及其逐题明细文件？封印基线不受影响。`,
            isEn.value ? 'Delete run' : '删除运行记录',
            { type: 'warning', confirmButtonText: isEn.value ? 'Delete' : '删除', cancelButtonText: isEn.value ? 'Cancel' : '取消' }
        )
    } catch {
        return
    }
    const res = await request<{ run_history: EvalRunRecord[] }>({
        url: '/admin/plugin/rolling-memory/eval/run',
        method: 'POST',
        data: { mode: 'delete', run_id: row.id, suite: query.suite },
    })
    if (res.data.data?.run_history) {
        overview.value.run_history = res.data.data.run_history
    }
    if (detailRunId.value === row.id) {
        detailRunId.value = ''
        detailItems.value = []
        detailAvailable.value = false
        detailReason.value = isEn.value ? 'Run deleted; pick another run.' : '该运行已删除，请重新选择运行。'
    }
    if (compareRunId.value === row.id) {
        compareRunId.value = ''
    }
    if (selectedRecord.value?.id === row.id) {
        selectedRecord.value = null
    }
    ElMessage.success(isEn.value ? 'Run deleted' : '运行记录已删除')
    await loadOverview()
}

const inspectRunDetails = async (row: any) => {
    selectedRecord.value = row
    detailRunId.value = row.id
    activeTab.value = 'detail'
    await loadRunDetailList(row.id)
}

const onDetailRunSelect = async (runId: string) => {
    const found = allLedgerRows.value.find((r) => r.id === runId)
    if (found) selectedRecord.value = found
    await loadRunDetailList(runId)
}

let detailRequestID = 0
const loadRunDetailList = async (runId: string) => {
    const requestID = ++detailRequestID
    detailItems.value = []
    if (!runId) {
        detailAvailable.value = false
        detailReason.value = isEn.value ? 'Pick a run to inspect per-question detail.' : '请选择一场运行查看逐题明细。'
        return
    }
    loadingDetails.value = true
    try {
        const res = await request<{ items: EvalQuestionResultItem[]; available?: boolean; baseline?: boolean; reason?: string }>({
            url: '/admin/plugin/rolling-memory/eval/run',
            method: 'POST',
            data: { mode: 'detail', run_id: runId },
        })
        if (requestID !== detailRequestID) return
        const data = res.data.data
        detailItems.value = data?.items || []
        detailAvailable.value = data?.available ?? detailItems.value.length > 0
        detailReason.value = String(data?.reason || '')
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        if (requestID === detailRequestID) loadingDetails.value = false
    }
}

let compareRequestID = 0
// loadCompareDetailList 取 Run A（基线侧）逐题明细（同一 mode=detail 接口，按 item.id 对齐）。
const loadCompareDetailList = async (runId: string) => {
    const requestID = ++compareRequestID
    compareItems.value = []
    if (!runId) {
        compareAvailable.value = true
        compareReason.value = ''
        return
    }
    loadingCompare.value = true
    try {
        const res = await request<{ items: EvalQuestionResultItem[]; available?: boolean; reason?: string }>({
            url: '/admin/plugin/rolling-memory/eval/run',
            method: 'POST',
            data: { mode: 'detail', run_id: runId },
        })
        if (requestID !== compareRequestID) return
        const data = res.data.data
        compareItems.value = data?.items || []
        compareAvailable.value = data?.available ?? compareItems.value.length > 0
        compareReason.value = String(data?.reason || '')
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        if (requestID === compareRequestID) loadingCompare.value = false
    }
}

// 对比运行切换：重取明细并复位 Δ 过滤器（清空选择时一并复位）。
watch(compareRunId, async (runId) => {
    detailFilter.diff = 'all'
    await loadCompareDetailList(String(runId || ''))
})

// ===== 单题 X-Ray：SDK 命中、交付证据与引擎 provenance =====
const recallEvidenceHits = computed<RecallEvidenceHit[]>(() => probeRecall.value?.raw_hits || [])
const probeEvidenceItems = computed<string[]>(() => probeItem.value?.raw_items || [])
const formatEvidenceLines = (lines: string[]) => lines.join('\n')
const probeCloudHitCount = computed(() => {
    const direct = probeItem.value?.cloud_hits ?? probeRecall.value?.meta?.cloud_hits
    if (typeof direct === 'number') return direct
    return recallEvidenceHits.value.length || Number(probeItem.value?.raw || 0) || probeEvidenceItems.value.length
})
const probeProvenance = computed(() => {
    const item = probeItem.value
    const meta: RecallMeta = probeRecall.value?.meta || item?.meta || {}
    const localFallback = item?.local_fallback ?? meta.local_fallback
    return {
        engine_transport: String(item?.engine_transport || meta.engine_transport || meta.transport || ''),
        engine_scope: String(item?.engine_scope || meta.engine_scope || meta.scope || probeRecall.value?.scope || ''),
        engine_upstream: String(item?.engine_upstream || meta.engine_upstream || meta.endpoint || ''),
        local_fallback: typeof localFallback === 'boolean' ? localFallback : null,
        cloud_hits: item?.cloud_hits ?? meta.cloud_hits ?? null,
        store_path: String(meta.store_path || ''),
        corpus_sha256: String(item?.corpus_sha256 || meta.corpus_sha256 || ''),
    }
})
const probeProvenanceJSON = computed(() => {
    const meta: RecallMeta = probeRecall.value?.meta || probeItem.value?.meta || {}
    if (Object.keys(meta).length > 0) {
        return JSON.stringify(meta.provenance || meta, null, 2)
    }
    // 历史明细没有完整 recall 对象，只保留逐题落档的 provenance 标量，避免显示空 {} 冒充证据。
    const item = probeItem.value
    if (!item) return '{}'
    return JSON.stringify(
        {
            engine_transport: item.engine_transport || null,
            engine_scope: item.engine_scope || null,
            engine_upstream: item.engine_upstream || null,
            local_fallback: item.local_fallback ?? null,
            cloud_hits: item.cloud_hits ?? null,
            retrieval_unit_ids: item.retrieval_unit_ids || [],
            corpus_sha256: item.corpus_sha256 || null,
        },
        null,
        2
    )
})
const probeReproCli = computed(() => {
    const cli = normalizeReproCommand(String(probeItem.value?.repro_cli || ''))
    return isCurrentReproCli(cli) ? cli : ''
})

const probeQuestion = async (row: any) => {
    probeTarget.value = { ...row, evidence_texts: [...(row.evidence_texts || [])] }
    probeItem.value = null
    probeRecall.value = null
    probeVisible.value = true
    await executeProbe(false)
}

const openQuestionResultProbe = (item: any) => {
    probeTarget.value = {
        id: item.id,
        sample_id: item.sample_id,
        category: item.category,
        question: item.question,
        answer: item.gold,
        evidence_texts: item.evidence_texts || [],
    }
    probeItem.value = item
    // 新明细可直接携带 SDK recall；旧明细仅保留压平上下文与已落档的引擎 meta。
    probeRecall.value = (item.recall as StructuredRecallPayload | undefined) ?? null
    probeResultText.value = item.context_preview || item.context || ''
    probeVisible.value = true
}

const executeProbe = async (withAgents: boolean) => {
    if (!probeTarget.value) return
    // 单题探针必须跟随当前选择的臂：sdk-pulse-cipher / sdk-pulse-plain / sdk-local-plain 需要已核验语料；oracle 只读金标证据，不需要语料。
    const probeArm = evalConfig.arm_mode === 'oracle' ? 'oracle' : evalConfig.arm_mode
    if (probeArm !== 'oracle' && !evalCorpusReady.value) {
        corpusModalVisible.value = true
        ElMessage.warning(isEn.value ? 'Prepare and Verify must pass before running SDK X-Ray' : '必须先完成 Prepare 并通过 Verify 才能运行 SDK X-Ray')
        return
    }
    if (withAgents) {
        let agentsReady = false
        try {
            const chkRes = await request<{ ai_check: AIModelCheckResult }>({
                url: '/admin/plugin/rolling-memory/eval/run',
                method: 'POST',
                data: { mode: 'check_ai', ai_config: aiForm },
            })
            if (chkRes.data.data?.ai_check) {
                overview.value.ai_check = chkRes.data.data.ai_check
            }
            agentsReady = Boolean(chkRes.data.data?.ai_check?.ready)
        } catch {
            // 业务与网络错误由请求拦截器统一提示。
            return
        }
        if (!agentsReady) {
            aiModalBlockedAction.value = 'probe'
            aiModalVisible.value = true
            return
        }
    }
    probing.value = true
    probeWithAgents.value = withAgents
    try {
        const res = await request<{ item: EvalQuestionResultItem; recall: Record<string, any> }>({
            url: '/admin/plugin/rolling-memory/eval/run',
            method: 'POST',
            data: {
                mode: 'single',
                suite: query.suite,
                question_id: probeTarget.value.id,
                category: probeTarget.value.category,
                question: probeTarget.value.question,
                gold: probeTarget.value.answer,
                evidence_texts: probeTarget.value.evidence_texts,
                arm_mode: probeArm,
                top_k: probeTopK.value,
                eval_store: evalStorePath.value,
                scope: evalScopeText.value,
                use_agents: withAgents,
                ai_config: aiForm,
            },
        })
        if (res.data.data?.item) {
            probeItem.value = res.data.data.item
        }
        // 同时保留结构化 recall 对象（双栏显微镜用）与既有的 JSON 文本兜底。
        probeRecall.value = (res.data.data?.recall as StructuredRecallPayload | undefined) ?? null
        probeResultText.value = JSON.stringify(res.data.data?.recall ?? {}, null, 2)
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        probing.value = false
    }
}

// stepProbeQuestion 在当前过滤后的逐题明细列表中按偏移量 (+1 下一题 / -1 上一题) 切换单题显微镜目标
const stepProbeQuestion = (delta: number) => {
    const list = filteredDetailItems.value
    if (!list || list.length === 0) return
    const currId = probeTarget.value?.id || ''
    let idx = list.findIndex((it) => it.id === currId)
    if (idx < 0) {
        idx = delta >= 0 ? 0 : list.length - 1
    } else {
        idx = (idx + delta + list.length) % list.length
    }
    const nextItem = list[idx]
    if (nextItem) {
        detailPage.value = Math.floor(idx / detailPageSize.value) + 1
        openQuestionResultProbe(nextItem)
    }
}

// onEvalKeydown：j/k 上下题 · r 关闭 Answer/Judge 重跑 SDK 检索 · c 复制复现 CLI · Esc 关抽屉。
const onEvalKeydown = (ev: KeyboardEvent) => {
    if (ev.metaKey || ev.ctrlKey || ev.altKey) return
    const target = ev.target as HTMLElement | null
    if (target) {
        const tag = (target.tagName || '').toUpperCase()
        if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || target.isContentEditable) {
            return
        }
    }
    if (aiModalVisible.value || corpusModalVisible.value) return
    const key = ev.key
    if (key === 'Escape' && probeVisible.value) {
        ev.preventDefault()
        probeVisible.value = false
        return
    }
    if (!probeVisible.value && activeTab.value !== 'detail') return
    if (key === 'j' || key === 'J') {
        ev.preventDefault()
        stepProbeQuestion(1)
    } else if (key === 'k' || key === 'K') {
        ev.preventDefault()
        stepProbeQuestion(-1)
    } else if (key === 'r' || key === 'R') {
        if (probeTarget.value && !probing.value) {
            ev.preventDefault()
            void executeProbe(false)
        }
    } else if (key === 'c' || key === 'C') {
        ev.preventDefault()
        const cli = String(probeReproCli.value || detailReproCli.value || latestReproCli.value || '').trim()
        if (cli) {
            void copyText(cli, 'Copied Repro CLI (c)', '已通过快捷键(c)复制复现命令')
        }
    }
}

// applyRouteQuery 预置评测体系与题集：默认以剑桥 ATM-Bench 为第一基准（LoCoMo 为对照参照），
// 从榜单页直达时只接受当前提供的闭集；非法或未知参数回落到各套件默认题集，实际运行题数由 batchLimit 同源派生。
const applyRouteQuery = () => {
    const route = router.currentRoute.value
    const suite = String(route.query.suite || '').toLowerCase()
    if (suite === 'atm' || suite === 'locomo') {
        query.suite = suite
    } else {
        query.suite = 'atm'
    }
    const tier = String(route.query.tier || '').toLowerCase()
    const allowed = query.suite === 'atm' ? ['hard', '1013'] : ['1540']
    if (allowed.includes(tier)) {
        query.tier = tier
    } else {
        query.tier = query.suite === 'atm' ? 'hard' : '1540'
    }
    if (query.suite === 'atm') {
        evalConfig.top_k = 50
        probeTopK.value = 50
    } else {
        evalConfig.top_k = 20
        probeTopK.value = 20
    }
}

let evalDeactivated = false
onMounted(() => {
    applyRouteQuery()
    loadOverview()
    window.addEventListener('keydown', onEvalKeydown)
})
onActivated(() => {
    if (!evalDeactivated) return
    evalDeactivated = false
    window.addEventListener('keydown', onEvalKeydown)
    const rq = router.currentRoute.value.query
    const reqSuite = String(rq.suite || '').toLowerCase()
    const reqTier = String(rq.tier || '').toLowerCase()
    if ((reqSuite && reqSuite !== query.suite) || (reqTier && reqTier !== query.tier)) {
        applyRouteQuery()
        loadOverview()
    }
})
onDeactivated(() => {
    evalDeactivated = true
    window.removeEventListener('keydown', onEvalKeydown)
})
onBeforeUnmount(() => {
    stopEvalStreamClient()
    stopIngestClock()
    window.removeEventListener('keydown', onEvalKeydown)
})
</script>

<style scoped lang="scss">
.plugin-rm-eval {
    .rm-eval-header {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        align-items: center;
        justify-content: space-between;
    }

    .rm-eval-title-group,
    .rm-eval-actions {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
    }

    .rm-eval-title {
        font-size: 18px;
        font-weight: 600;
        line-height: 1.4;
    }

    .rm-eval-heading {
        min-width: 260px;
        flex: 1;

        p {
            margin: 4px 0 0;
            font-size: 12.5px;
            line-height: 1.6;
            color: var(--el-text-color-secondary);
        }
    }

    .rm-eval-title-line {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
    }

    .rm-primary-tabs {
        :deep(.el-tabs__header) {
            margin-bottom: 16px;
        }

        :deep(.el-tabs__item) {
            min-width: 96px;
            font-weight: 500;
        }
    }

    .rm-run-panel {
        padding: 18px;
        background: var(--el-bg-color);
        border: 1px solid var(--el-color-primary-light-7);
        border-radius: 8px;
    }

    .rm-run-intro,
    .rm-results-header,
    .rm-history-toolbar,
    .rm-detail-context {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        align-items: center;
        justify-content: space-between;
    }

    .rm-run-intro {
        margin-bottom: 16px;

        h2,
        p {
            margin: 0;
        }

        h2 {
            font-size: 16px;
            font-weight: 600;
        }

        p {
            margin-top: 4px;
            font-size: 12.5px;
            line-height: 1.6;
            color: var(--el-text-color-secondary);
        }
    }

    .rm-config-grid {
        display: grid;
        grid-template-columns: minmax(0, 1fr) minmax(260px, 320px);
        gap: 24px;
        align-items: stretch;
    }

    .rm-config-main {
        min-width: 0;
    }

    .rm-field-block + .rm-field-block {
        padding-top: 16px;
        margin-top: 16px;
        border-top: 1px solid var(--el-border-color-extra-light);
    }

    .rm-field-label {
        display: block;
        margin-bottom: 8px;
        font-size: 13px;
        font-weight: 600;
        color: var(--el-text-color-primary);
    }

    .rm-field-help {
        margin: 7px 0 0;
        font-size: 12px;
        line-height: 1.6;
        color: var(--el-text-color-secondary);
    }

    .rm-run-review {
        display: flex;
        flex-direction: column;
        padding: 16px;
        background: var(--el-color-primary-light-9);
        border: 1px solid var(--el-color-primary-light-7);
        border-radius: 8px;
    }

    .rm-review-label {
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .rm-review-title {
        margin-top: 4px;
        font-size: 16px;
        line-height: 1.5;
    }

    .rm-run-review dl {
        margin: 14px 0;
        font-size: 12px;

        > div {
            display: flex;
            gap: 12px;
            align-items: flex-start;
            justify-content: space-between;
            padding: 6px 0;
            border-bottom: 1px solid var(--el-border-color-extra-light);
        }

        dt {
            color: var(--el-text-color-secondary);
        }

        dd {
            margin: 0;
            font-weight: 500;
            text-align: right;
            word-break: break-word;

            &.is-ready-txt {
                color: var(--el-color-success);
            }

            &.is-warn-txt {
                color: var(--el-color-warning);
            }
        }
    }

    .rm-start-button {
        width: 100%;
        margin-top: auto;
    }

    .rm-stop-actions {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        margin-top: auto;

        .el-button {
            flex: 1;
            margin-left: 0;
        }
    }

    .rm-review-foot {
        display: block;
        margin-top: 10px;
        font-size: 11.5px;
        line-height: 1.55;
        color: var(--el-text-color-secondary);
    }

    .rm-run-advanced,
    .rm-environment-panel {
        margin-top: 16px;
    }

    // 语料与隔离折叠面板：Element Plus 默认 .el-collapse-item__content 左右 padding 为 0，
    // 必须显式给 header 与 content 加上左右内边距，防止展开内容左右贴边顶格。
    .rm-environment-panel {
        border-top: none;
        border-bottom: none;
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 10px;
        overflow: hidden;
        background: var(--el-bg-color);

        :deep(.el-collapse-item__header) {
            height: 44px;
            padding: 0 18px;
            font-size: 13px;
            font-weight: 600;
            background: var(--el-fill-color-lighter);
            border-bottom: 1px solid var(--el-border-color-extra-light);
            gap: 8px;
        }

        :deep(.el-collapse-item__wrap) {
            border-bottom: none;
        }

        :deep(.el-collapse-item__content) {
            padding: 18px 20px;
        }
    }

    // 运行参数面板（治理：增加 padding 防止顶格，内容区与边框留出呼吸空间）。
    .rm-run-params-open {
        padding: 20px;
        margin: 0 4px;
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 12px;
        background: var(--el-fill-color-blank);
    }

    .rm-advanced-controls {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
        gap: 20px 24px;
        margin-bottom: 20px;

        label {
            min-width: 0;

            > span {
                display: block;
                margin-bottom: 6px;
                font-size: 12px;
                color: var(--el-text-color-secondary);
            }

            .el-select,
            .el-input {
                width: 100%;
            }

            &.is-wide {
                grid-column: 1 / -1;
            }
        }
    }

    // 参数区内部三段（控件网格 / 成本提示条 / 落档复现命令条）之间统一 12px 垂直节奏。
    .rm-run-params-open .rm-cost-estimate-strip,
    .rm-run-params-open .rm-cli-bar {
        margin-top: 12px;
    }

    // 三步流程条：落地即答「这页怎么用」。
    .rm-howto-strip {
        display: flex;
        flex-wrap: wrap;
        gap: 10px 14px;
        align-items: center;
        padding: 12px 16px;
        margin-bottom: 12px;
        background: var(--el-color-primary-light-9);
        border: 1px solid var(--el-color-primary-light-7);
        border-radius: 8px;

        .rm-howto-step {
            display: flex;
            gap: 8px;
            align-items: center;
            min-width: 0;
        }

        .rm-howto-idx {
            display: inline-flex;
            flex-shrink: 0;
            align-items: center;
            justify-content: center;
            width: 22px;
            height: 22px;
            font-size: 12px;
            font-weight: 700;
            color: #fff;
            background: var(--el-color-primary);
            border-radius: 50%;
        }

        .rm-howto-txt {
            display: flex;
            flex-direction: column;
            line-height: 1.35;

            strong {
                font-size: 12.5px;
                color: var(--el-text-color-primary);
            }

            span {
                font-size: 11.5px;
                color: var(--el-text-color-secondary);
            }
        }

        .rm-howto-arrow {
            font-size: 14px;
            color: var(--el-color-primary-light-3);
        }

        .rm-howto-cta {
            margin-left: auto;
        }
    }

    // 评测保障说明折叠条：补齐折叠内容左右 padding 防止顶格。
    .rm-pillars-collapse {
        margin-bottom: 12px;
        border-top: none;
        border-bottom: none;

        :deep(.el-collapse-item__header) {
            height: 36px;
            padding: 0 12px;
            font-size: 12.5px;
            background: var(--el-fill-color-lighter);
            border: 1px solid var(--el-border-color-lighter);
            border-radius: 6px;
        }

        :deep(.el-collapse-item__content) {
            padding: 16px 20px 2px; // 强制注入左右 padding
        }


        .rm-pillars-title {
            overflow: hidden;
            color: var(--el-text-color-secondary);
            text-overflow: ellipsis;
            white-space: nowrap;
        }
    }

    // 表格呼吸：行内局促治理——单元格行高/内边距放大、行间留白；
    // 列宽总和超容器时走 el-table 原生横向滚动（禁互相压缩式自适应挤压）。
    .rm-roomy-table {
        :deep(.el-table__cell) {
            padding: 10px 0;
        }

        :deep(.cell) {
            padding: 0 12px;
            line-height: 1.6;
        }

        :deep(.el-table__body-wrapper) {
            overflow-x: auto;
        }
    }

    // Delta 对比矩阵：基线 / 本次 / Δ 三列配对视图（官方口径）。
    .rm-delta-matrix {
        padding: 14px 18px;
        margin-bottom: 16px;
        background: var(--el-bg-color);
        border: 1px solid var(--el-color-primary-light-7);
        border-left: 3px solid var(--el-color-primary);
        border-radius: 10px;

        .rm-dm-head {
            display: flex;
            flex-wrap: wrap;
            gap: 8px 10px;
            align-items: center;
            margin-bottom: 10px;
            font-size: 12.5px;
        }

        .rm-dm-table {
            width: 100%;
            font-size: 12.5px;
            border-collapse: collapse;

            th,
            td {
                padding: 7px 10px;
                text-align: right;
                border-bottom: 1px solid var(--el-border-color-extra-light);
            }

            th:first-child,
            td:first-child {
                text-align: left;
            }

            th {
                font-weight: 600;
                color: var(--el-text-color-secondary);
                background: var(--el-fill-color-lighter);
            }

            tbody tr:last-child td {
                border-bottom: none;
            }
        }

        .rm-dm-num {
            font-variant-numeric: tabular-nums;
        }

        .rm-dm-delta {
            font-weight: 700;

            &.is-up {
                color: var(--el-color-success);
            }

            &.is-down {
                color: var(--el-color-warning);
            }

            &.is-warn {
                color: var(--el-color-danger);
            }

            &.is-flat {
                color: var(--el-text-color-secondary);
                font-weight: 500;
            }
        }

        .rm-dm-warnline {
            margin-top: 8px;
            font-size: 12px;
            color: var(--el-color-danger);
        }
    }

    .rm-results-header {
        margin-bottom: 14px;

        h2,
        p {
            margin: 0;
        }

        h2 {
            font-size: 16px;
            font-weight: 600;
        }

        p {
            max-width: 900px;
            margin-top: 5px;
            font-size: 12.5px;
            line-height: 1.6;
            color: var(--el-text-color-secondary);
        }
    }

    .rm-result-kpis {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(170px, 1fr));
        gap: 10px;
        margin-bottom: 14px;

        > div {
            display: flex;
            flex-direction: column;
            gap: 4px;
            padding: 10px 12px;
            background: var(--el-fill-color-lighter);
            border-left: 3px solid var(--el-color-primary);
            border-radius: 6px;
        }

        span {
            font-size: 11.5px;
            color: var(--el-text-color-secondary);
        }

        strong {
            font-size: 20px;
            font-variant-numeric: tabular-nums;
        }

        .rm-kpi-micro {
            font-size: 11px;
            font-weight: 600;
            color: var(--el-color-success);
        }
    }

    .rm-history-toolbar {
        padding: 10px 0;

        > div {
            display: flex;
            flex-direction: column;
            gap: 3px;
        }

        strong {
            font-size: 13px;
        }

        span {
            font-size: 12px;
            color: var(--el-text-color-secondary);
        }
    }

    .rm-load-error {
        display: flex;
        gap: 10px;
        align-items: center;
        margin-bottom: 12px;

        .el-alert {
            flex: 1;
        }
    }

    .rm-question-error {
        margin-bottom: 12px;
    }

    .rm-detail-context {
        padding-bottom: 10px;
        margin-bottom: 12px;
        font-size: 12px;
        color: var(--el-text-color-secondary);
        border-bottom: 1px solid var(--el-border-color-lighter);
    }

    @media (max-width: 900px) {
        .rm-config-grid {
            grid-template-columns: minmax(0, 1fr);
            gap: 16px;
        }

        .rm-result-kpis {
            grid-template-columns: repeat(2, minmax(0, 1fr));
        }
    }

    @media (max-width: 600px) {
        .rm-run-panel {
            padding: 14px;
        }

        .rm-eval-header,
        .rm-run-intro,
        .rm-results-header {
            align-items: flex-start;
        }

        .rm-eval-actions {
            width: 100%;
        }

        .rm-result-kpis {
            grid-template-columns: minmax(0, 1fr);
        }
    }

    .rm-prepare-bar {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        align-items: center;
        justify-content: space-between;
        padding: 12px 14px;
        margin-bottom: 12px;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 8px;
    }

    .rm-readiness-alert,
    .rm-sdk-note {
        margin-bottom: 12px;
    }

    .rm-manifest-grid {
        display: grid;
        grid-template-columns: repeat(2, minmax(0, 1fr));
        gap: 8px;
        margin-bottom: 12px;
    }

    .rm-manifest-item {
        min-width: 0;
        padding: 8px 10px;
        background: var(--el-fill-color-light);
        border: 1px solid var(--el-border-color-extra-light);
        border-radius: 6px;

        > span {
            display: block;
            margin-bottom: 4px;
            font-size: 11px;
            font-weight: 600;
            color: var(--el-text-color-secondary);
        }

        code {
            display: block;
            font-size: 11.5px;
            line-height: 1.5;
            word-break: break-all;
        }

        &.is-wide {
            grid-column: span 2;
        }
    }

    .rm-provenance-box {
        padding: 10px 12px;
        margin: 10px 0;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 8px;

        .rm-split-title,
        .el-alert,
        .el-collapse {
            margin-top: 8px;
        }
    }

    .is-blocked {
        font-weight: 700;
        color: var(--el-color-danger);
    }

    @media (max-width: 900px) {
        .rm-manifest-grid {
            grid-template-columns: minmax(0, 1fr);
        }

        .rm-manifest-item.is-wide {
            grid-column: span 1;
        }
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

    .rm-col-stretch {
        display: flex;
        margin-bottom: 12px;
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

        &.is-pillar-1 {
            border-left: 3px solid var(--el-color-success);
        }

        &.is-pillar-2 {
            border-left: 3px solid var(--el-color-primary);
        }

        &.is-pillar-3 {
            border-left: 3px solid var(--el-color-warning);
        }
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

    .rm-isolation-mini {
        display: flex;
        flex-direction: column;
        gap: 10px;
        margin-top: 4px;
        font-size: 12.5px;
    }

    .rm-iso-line {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        justify-content: space-between;
        padding: 8px 10px;
        background: var(--el-bg-color);
        border: 1px solid var(--el-border-color-extra-light);
        border-radius: 6px;
    }

    .rm-iso-nums {
        font-variant-numeric: tabular-nums;
        line-height: 1.5;
    }

    .rm-iso-tag {
        padding: 2px 8px;
        font-size: 11.5px;
        font-weight: 600;
        border-radius: 4px;

        &.is-user {
            color: var(--el-color-success);
            background: var(--el-color-success-light-9);
        }

        &.is-eval {
            color: var(--el-color-warning);
            background: var(--el-color-warning-light-9);
        }
    }

    .rm-charts-row {
        margin-bottom: 4px;
    }

    .rm-chart-box {
        display: flex;
        flex-direction: column;
        justify-content: space-between;
        width: 100%;
        padding: 14px 16px;
        background: var(--el-bg-color);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 8px;
    }

    .rm-chart-hdr {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 10px;
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

        &.is-ev {
            background: #3b82f6;
        }

        &.is-jdg {
            background: #10b981;
        }
    }

    .rm-bar-svg {
        display: block;
        width: 100%;
        height: auto;
    }

    .rm-grid-line {
        stroke: var(--el-border-color-lighter);
        stroke-dasharray: 3 3;
        stroke-width: 1;
    }

    .rm-axis-line {
        stroke: var(--el-border-color);
        stroke-width: 1.2;
    }

    .rm-axis-txt {
        fill: var(--el-text-color-secondary);
        font-size: 10.5px;
    }

    .rm-oracle-line {
        stroke: #f59e0b;
        stroke-dasharray: 5 3;
        stroke-width: 1.4;
    }

    /* 封印基线官方 R@10 参考线：与 Oracle 上限线区分配色（主色系实虚线） */
    .rm-baseline-line {
        stroke: var(--el-color-primary);
        stroke-dasharray: 2 3;
        stroke-width: 1.4;
        opacity: 0.75;
    }

    .rm-baseline-txt {
        fill: var(--el-color-primary);
        font-size: 10.5px;
    }

    .rm-oracle-txt {
        fill: #d97706;
        font-size: 10.5px;
        font-weight: 600;
    }

    .rm-bar-group {
        cursor: pointer;

        &:hover {
            opacity: 0.92;
        }
    }

    .rm-bar-active-bg {
        fill: var(--el-color-primary-light-9);
        stroke: var(--el-color-primary-light-5);
        stroke-width: 1;
    }

    .rm-bar-ev {
        fill: #3b82f6;
    }

    .rm-bar-jdg {
        fill: #10b981;
    }

    .rm-bar-val {
        fill: #2563eb;
        font-size: 10px;
        font-weight: 600;

        &.is-jdg-txt {
            fill: #059669;
        }
    }

    .rm-bar-lbl {
        fill: var(--el-text-color-primary);
        font-size: 11px;
        font-weight: 600;

        &.is-active-lbl {
            fill: var(--el-color-primary);
            font-weight: 700;
        }
    }

    .rm-bar-sub {
        fill: var(--el-text-color-secondary);
        font-size: 9.5px;
    }

    .rm-cat-bars {
        display: flex;
        flex-direction: column;
        gap: 12px;
    }

    .rm-cat-item {
        display: flex;
        flex-direction: column;
        gap: 5px;
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

    .rm-cat-nums {
        font-variant-numeric: tabular-nums;

        .is-jdg-num {
            color: #10b981;
        }
    }

    .rm-cat-track {
        height: 8px;
        overflow: hidden;
        background: var(--el-fill-color);
        border-radius: 4px;
    }

    .rm-cat-fill {
        height: 100%;
        border-radius: 4px;

        &.is-ev {
            background: #3b82f6;
        }

        &.is-jdg {
            background: #10b981;
        }
    }

    .rm-cat-note {
        padding-top: 8px;
        margin-top: 10px;
        font-size: 12.5px;
        line-height: 1.6;
        color: var(--el-text-color-regular);
        word-break: break-word;
        border-top: 1px dashed var(--el-border-color-lighter);
    }

    .rm-run-bar {
        display: flex;
        flex-direction: column;
        gap: 10px;
        padding: 12px 14px;
        margin-bottom: 12px;
        font-size: 13px;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 6px;
    }

    .rm-preset-strip {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        padding-bottom: 8px;
        border-bottom: 1px dashed var(--el-border-color-lighter);
    }

    .rm-preset-lbl {
        font-size: 12.5px;
        font-weight: 600;
        color: var(--el-text-color-secondary);
    }

    /* 预设栏内的三轴图例行（独立占行 · 与预设按钮同栏共存） */
    .rm-preset-axes {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
        width: 100%;
        margin-top: 2px;
    }

    /* 开发者 CLI 条：深色终端风格。治理：加边距防止顶格。 */
    .rm-cli-bar {
        display: flex;
        flex-direction: column;
        gap: 10px;
        padding: 16px;
        margin: 12px 4px 0 4px;
        background: #111827;
        border: 1px solid #1f2937;
        border-radius: 10px;
    }

    .rm-cli-preset-row {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
    }

    .rm-cli-lbl {
        font-size: 12px;
        font-weight: 600;
        color: #9ca3af;
    }

    .rm-cli-disabled-wrap {
        display: inline-flex;
    }

    .rm-cli-hint {
        margin-left: auto;
        font-size: 11px;
        color: #6b7280;
    }

    .rm-cli-cmd-row {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: flex-start;
        padding-top: 8px;
        border-top: 1px dashed #374151;
    }

    .rm-cli-prompt {
        font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
        font-size: 12px;
        font-weight: 700;
        line-height: 1.6;
        color: #34d399;
        user-select: none;
    }

    .rm-cli-cmd {
        flex: 1;
        min-width: 240px;
        font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
        font-size: 11.5px;
        line-height: 1.6;
        color: #e5e7eb;
        word-break: break-all;
        white-space: pre-wrap;
        user-select: all;
    }

    .rm-cli-note {
        display: inline-flex;
        gap: 4px;
        align-items: center;
        font-size: 11px;
        line-height: 1.6;
        color: #9ca3af;
        cursor: help;
        align-self: center;
    }

    .rm-cli-warn {
        padding-top: 6px;
        font-size: 11.5px;
        line-height: 1.6;
        color: #fbbf24;
        border-top: 1px dashed #374151;
    }

    .rm-repro-row {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        padding: 6px 8px;
        margin-bottom: 8px;
        background: var(--el-fill-color-light);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 6px;
    }

    .rm-repro-code {
        flex: 1;
        min-width: 220px;
        font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
        font-size: 11.5px;
        line-height: 1.6;
        color: var(--el-text-color-regular);
        word-break: break-all;
        white-space: pre-wrap;
        user-select: all;
    }

    .rm-split-pre {
        max-height: 340px;
        padding: 8px 10px;
        margin: 0;
        overflow: auto;
        font-size: 11.5px;
        line-height: 1.55;
        word-break: break-word;
        white-space: pre-wrap;
        background: var(--el-fill-color-darker);
        border-radius: 4px;
    }

    .rm-run-controls {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        align-items: center;
        justify-content: space-between;
    }

    .rm-run-bar-left,
    .rm-run-bar-right {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
    }

    .rm-live-console {
        padding: 12px 14px;
        margin-top: 4px;
        background: var(--el-bg-color);
        border: 1px solid var(--el-color-primary-light-7);
        border-radius: 8px;
    }

    .rm-live-header {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 8px;
    }

    .rm-live-title-wrap,
    .rm-live-meta {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        font-size: 12.5px;
    }

    .rm-live-phase {
        color: var(--el-color-primary);
        font-weight: 500;
    }

    .rm-live-dot {
        display: inline-block;
        width: 9px;
        height: 9px;
        border-radius: 50%;
        background: #9ca3af;

        &.is-running {
            background: #f59e0b;
            box-shadow: 0 0 0 3px rgba(245, 158, 11, 0.25);
        }

        &.is-completed {
            background: #10b981;
        }

        &.is-failed {
            background: #ef4444;
        }
    }

    .rm-latest-score-strip {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
        padding: 12px 16px;
        margin-bottom: 16px;
        font-size: 12.5px;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-lighter);
        border-left: 3px solid var(--el-color-success);
        border-radius: 8px;

        .rm-lss-main {
            display: flex;
            flex-wrap: wrap;
            gap: 6px 8px;
            align-items: center;
            min-width: 0;
        }

        // 指标串独占一行（flex-basis 100% 强制换行）：标签/标题与数字串不再挤成一条长龙，
        // 数字用等宽数字对齐便于横向扫读。
        .rm-lss-metrics {
            flex-basis: 100%;
            line-height: 1.9;
            font-variant-numeric: tabular-nums;

            .is-ev {
                color: #2563eb;
            }

            .is-jdg {
                color: #059669;
            }
        }
    }

    .rm-sample-cap-strip {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        padding: 8px 10px;
        margin-top: 8px;
        font-size: 12px;
        background: var(--el-color-warning-light-9);
        border: 1px solid var(--el-color-warning-light-7);
        border-radius: 6px;

        .rm-scs-lbl {
            color: var(--el-text-color-regular);
        }
    }

    .rm-inline-llm-guide {
        display: flex;
        flex-direction: column;
        gap: 8px;
        padding: 10px 12px;
        margin-top: 10px;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-color-primary-light-7);
        border-radius: 6px;

        .rm-ilg-top {
            display: flex;
            flex-wrap: wrap;
            gap: 8px;
            align-items: center;
            justify-content: space-between;
        }

        .rm-ilg-status {
            display: flex;
            flex-wrap: wrap;
            gap: 8px;
            align-items: center;
            font-size: 12px;
        }

        .rm-ilg-cur {
            color: var(--el-text-color-regular);
        }

        .rm-ilg-btns,
        .rm-ilg-channels {
            display: flex;
            flex-wrap: wrap;
            gap: 6px;
            align-items: center;
        }

        .rm-ilg-ch-lbl {
            font-size: 11.5px;
            color: var(--el-text-color-secondary);
        }
    }

    .rm-finish-report-card {
        padding: 12px 14px;
        margin-bottom: 10px;
        background: var(--el-color-success-light-9);
        border: 1px solid var(--el-color-success-light-5);
        border-left: 4px solid var(--el-color-success);
        border-radius: 6px;

        .rm-frc-head {
            display: flex;
            flex-wrap: wrap;
            gap: 10px;
            align-items: center;
            justify-content: space-between;
            margin-bottom: 8px;
        }

        .rm-frc-explain {
            font-size: 12.5px;
            line-height: 1.6;
            color: var(--el-text-color-regular);

            p {
                margin: 0;
            }
        }
    }

    .rm-frc-buckets {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
        margin-top: 8px;

        .rm-frc-b-lbl {
            font-size: 11.5px;
            color: var(--el-text-color-secondary);
        }
    }

    .rm-clickable-bucket {
        cursor: pointer;
        user-select: none;
        transition: transform 0.12s ease;

        &:hover {
            transform: translateY(-1px);
        }
    }

    .rm-probe-llm-cta {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
        padding: 10px 12px;
        margin-top: 10px;
        background: var(--el-color-warning-light-9);
        border: 1px solid var(--el-color-warning-light-5);
        border-radius: 6px;

        > div {
            display: flex;
            flex-direction: column;
            gap: 3px;
            max-width: 680px;
            font-size: 12px;
            line-height: 1.5;

            span {
                color: var(--el-text-color-secondary);
            }
        }
    }

    .rm-score-guide-banner {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
        gap: 10px;
        padding: 10px 14px;
        margin-bottom: 12px;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 6px;

        .rm-sgb-item {
            display: flex;
            flex-direction: column;
            gap: 4px;
            font-size: 12px;
            line-height: 1.5;

            &.is-full-row {
                grid-column: 1 / -1;
                padding-top: 6px;
                border-top: 1px dashed var(--el-border-color-lighter);
            }

            strong {
                color: var(--el-text-color-primary);
            }

            span {
                color: var(--el-text-color-secondary);
            }
        }
    }

    .rm-top-progress-banner,
    .rm-ingest-progress-box {
        padding: 10px 14px;
        margin-bottom: 12px;
        background: var(--el-color-primary-light-9);
        border: 1px solid var(--el-color-primary-light-7);
        border-radius: 8px;
    }

    .rm-tpb-head,
    .rm-ipb-top {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 6px;
        font-size: 12.5px;
    }

    .rm-tpb-title {
        display: flex;
        gap: 8px;
        align-items: center;
    }

    .rm-tpb-sub {
        font-size: 11.5px;
        color: var(--el-text-color-secondary);
    }

    .rm-live-steps {
        display: grid;
        grid-template-columns: repeat(3, minmax(0, 1fr));
        gap: 8px;
        margin-bottom: 8px;
    }

    .rm-step-pill {
        display: flex;
        gap: 8px;
        align-items: center;
        padding: 6px 10px;
        font-size: 12px;
        color: var(--el-text-color-secondary);
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 6px;

        .rm-step-idx {
            display: inline-flex;
            align-items: center;
            justify-content: center;
            width: 18px;
            height: 18px;
            font-size: 11px;
            font-weight: 700;
            border-radius: 50%;
            background: var(--el-fill-color-dark);
            color: var(--el-text-color-regular);
        }

        &.is-active {
            color: var(--el-color-primary);
            background: var(--el-color-primary-light-9);
            border-color: var(--el-color-primary-light-5);
            font-weight: 600;

            .rm-step-idx {
                background: var(--el-color-primary);
                color: #fff;
            }
        }

        &.is-done {
            color: var(--el-color-success);
            background: var(--el-color-success-light-9);
            border-color: var(--el-color-success-light-5);

            .rm-step-idx {
                background: var(--el-color-success);
                color: #fff;
            }
        }
    }

    .rm-live-active-q {
        display: flex;
        gap: 8px;
        align-items: center;
        padding: 6px 10px;
        margin-bottom: 6px;
        font-size: 12px;
        background: var(--el-fill-color-lighter);
        border-radius: 6px;

        .rm-laq-txt {
            overflow: hidden;
            text-overflow: ellipsis;
            white-space: nowrap;
            color: var(--el-text-color-regular);
        }
    }

    .rm-live-progress {
        margin: 8px 0 10px;
    }

    .rm-live-kpis {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
        gap: 8px;
        margin-bottom: 10px;
    }

    .rm-live-kpi-box {
        display: flex;
        flex-direction: column;
        gap: 2px;
        padding: 8px 10px;
        background: var(--el-fill-color-light);
        border-radius: 6px;

        .lbl {
            font-size: 11.5px;
            color: var(--el-text-color-secondary);
        }

        .val {
            font-size: 14px;
            font-variant-numeric: tabular-nums;

            &.is-ev {
                color: #2563eb;
            }

            &.is-jdg {
                color: #059669;
            }
        }

        .sub-cats {
            font-size: 11.5px;
            font-weight: 600;
            font-variant-numeric: tabular-nums;
        }
    }

    .rm-live-feed-list {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(310px, 1fr));
        gap: 8px;
        max-height: 240px;
        overflow: auto;
    }

    .rm-live-feed-card {
        padding: 8px 10px;
        cursor: pointer;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 6px;

        &:hover {
            border-color: var(--el-color-primary-light-5);
        }
    }

    .rm-lf-top {
        display: flex;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 4px;
        font-size: 11.5px;
    }

    .rm-lf-idx {
        font-weight: 600;
    }

    .rm-lf-badges {
        display: flex;
        gap: 6px;
        align-items: center;
    }

    .rm-lf-ms {
        font-size: 11px;
        color: var(--el-text-color-secondary);
    }

    .rm-lf-q {
        font-size: 12px;
        line-height: 1.45;
        margin-bottom: 4px;
        word-break: break-word;
    }

    .rm-lf-ans {
        font-size: 11.5px;
        color: var(--el-text-color-regular);
        word-break: break-word;
    }

    .rm-live-log-pre {
        max-height: 200px;
        padding: 8px 10px;
        margin: 0;
        overflow: auto;
        font-size: 11.5px;
        line-height: 1.5;
        white-space: pre-wrap;
        background: #111827;
        color: #e5e7eb;
        border-radius: 6px;
    }

    .rm-ai-test-banner {
        padding: 10px 12px;
        margin-bottom: 12px;
        font-size: 12.5px;
        border-radius: 6px;
        border: 1px solid var(--el-border-color-light);

        &.is-ok {
            background: var(--el-color-success-light-9);
            border-color: var(--el-color-success-light-5);
        }

        &.is-err {
            background: var(--el-color-warning-light-9);
            border-color: var(--el-color-warning-light-5);
        }
    }

    .rm-ai-test-top {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        justify-content: space-between;
    }

    .rm-ai-test-sub {
        margin-top: 6px;
        font-size: 12px;
        line-height: 1.5;
        word-break: break-word;
    }

    .rm-check-list {
        display: flex;
        flex-direction: column;
        gap: 8px;
        margin-bottom: 12px;
    }

    .rm-check-row {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        padding: 7px 10px;
        font-size: 12.5px;
        background: var(--el-fill-color-light);
        border-radius: 6px;
    }

    .rm-check-lbl {
        min-width: 195px;
    }

    .rm-check-detail {
        color: var(--el-text-color-regular);
        word-break: break-word;
    }

    .rm-modal-presets,
    .rm-modal-footer,
    .rm-probe-toolbar {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        justify-content: space-between;
    }

    .rm-plugin-channels {
        margin-bottom: 12px;
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 8px;
        padding: 10px 12px;
        background: var(--el-bg-color);
    }

    .rm-plugin-channels-head {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        margin-bottom: 8px;
    }

    .rm-plugin-channels-title {
        font-weight: 600;
        font-size: 13px;
    }

    .rm-plugin-channels-empty {
        font-size: 12px;
        color: var(--el-text-color-secondary);
        padding: 6px 0;
    }

    .rm-plugin-channels-list {
        display: flex;
        flex-direction: column;
        gap: 6px;
        max-height: 220px;
        overflow-y: auto;
    }

    .rm-plugin-channel-card {
        display: flex;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
        padding: 7px 10px;
        border-radius: 6px;
        background: var(--el-fill-color-light);
        border-left: 3px solid var(--el-border-color);
    }

    .rm-plugin-channel-card.is-routable {
        border-left-color: var(--el-color-success);
    }

    .rm-pc-main {
        min-width: 0;
        flex: 1;
    }

    .rm-pc-name {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
        font-size: 12.5px;
    }

    .rm-pc-meta {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        margin-top: 3px;
        font-size: 11.5px;
        color: var(--el-text-color-secondary);
        word-break: break-all;
    }

    .rm-pc-actions {
        display: flex;
        gap: 6px;
        align-items: center;
        flex-shrink: 0;
    }

    .rm-ev-match-box {
        margin: 8px 0;
    }

    .rm-ev-match-row {
        display: flex;
        gap: 8px;
        align-items: flex-start;
        padding: 5px 8px;
        margin-top: 4px;
        font-size: 12px;
        background: var(--el-fill-color-lighter);
        border-radius: 4px;
    }

    .rm-ev-match-txt {
        line-height: 1.45;
        word-break: break-word;
    }

    .rm-cell-wrap {
        padding: 2px 0;
        line-height: 1.55;
        word-break: break-word;
        white-space: normal;
    }

    .rm-num-cell {
        font-variant-numeric: tabular-nums;
    }

    .rm-q-filter {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        margin-bottom: 10px;
    }

    .rm-q-filter-lbl {
        display: inline-flex;
        align-items: center;
        font-size: 12.5px;
        font-weight: 600;
        color: var(--el-text-color-secondary);
    }

    .rm-diff-summary {
        display: inline-flex;
        align-items: center;
        font-size: 11.5px;
        font-variant-numeric: tabular-nums;
        color: var(--el-text-color-secondary);

        &.is-loading {
            color: var(--el-color-primary);
        }
    }

    .rm-eval-tabs {
        :deep(.el-tabs__item) {
            padding: 0 14px;
            font-size: 13px;
        }
    }

    .rm-synth-alert {
        margin-bottom: 10px;
    }

    .rm-delta-cell {
        display: flex;
        flex-direction: column;
        gap: 3px;
        align-items: flex-start;
    }

    .rm-pagination {
        display: flex;
        justify-content: flex-end;
        margin-top: 12px;
    }

    .rm-proto-sec {
        padding: 12px 0;

        & + .rm-proto-sec {
            border-top: 1px solid var(--el-border-color-lighter);
        }
    }

    .rm-proto-hdr {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 12px;
        font-size: 13.5px;
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
        max-height: 180px;
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

    .rm-probe-result-title {
        margin: 14px 0 6px;
        font-size: 13px;
        font-weight: 600;
    }

    /* 单题 X-Ray 命中证据与交付上下文
       （结构与样式对齐 web/views/recall.vue:667-715 / 1158-1256，禁止自创结构） */
    .rm-probe-split {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
        gap: 12px;
        margin: 10px 0;
    }

    .rm-split-col {
        display: flex;
        flex-direction: column;
        min-width: 0;
    }

    .rm-split-title {
        margin-bottom: 6px;
        font-size: 12.5px;
        font-weight: 600;
        color: var(--el-text-color-secondary);
    }

    .rm-split-body {
        max-height: 420px;
        overflow: auto;
    }

    .rm-band-card {
        padding: 12px;
        margin-bottom: 12px;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 6px;

        .rm-band-card-hdr {
            display: flex;
            flex-wrap: wrap;
            gap: 8px;
            align-items: center;
            justify-content: space-between;
            padding-bottom: 8px;
            margin-bottom: 8px;
            border-bottom: 1px solid var(--el-border-color-lighter);
        }

        .rm-band-hdr-left {
            display: flex;
            flex-wrap: wrap;
            gap: 8px;
            align-items: center;
        }

        .rm-band-card-title {
            font-weight: 600;
        }
    }

    .rm-chain-block {
        padding: 8px 10px;
        margin-bottom: 8px;
        background: var(--el-bg-color);
        border: 1px solid var(--el-border-color-extra-light);
        border-radius: 4px;

        .rm-chain-hdr {
            display: flex;
            flex-wrap: wrap;
            gap: 8px;
            align-items: center;
            margin-bottom: 6px;
            font-size: 12px;
            font-weight: 600;
        }

        .rm-chain-time {
            color: var(--el-text-color-secondary);
        }
    }

    .rm-chain-node {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        justify-content: space-between;
        padding: 6px 0;
        border-top: 1px dashed var(--el-border-color-lighter);

        .rm-node-rendered {
            font-size: 13px;
            line-height: 1.45;
            word-break: break-word;
        }

        .rm-node-badges {
            display: flex;
            flex-wrap: wrap;
            gap: 6px;
            align-items: center;
        }
    }

    .rm-raw-card {
        padding: 10px;
        margin-bottom: 10px;
        background: var(--el-fill-color-light);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 6px;

        .rm-raw-hdr {
            display: flex;
            flex-wrap: wrap;
            gap: 6px;
            justify-content: space-between;
            margin-bottom: 6px;
            font-size: 12px;
            color: var(--el-text-color-secondary);
        }
    }

    .rm-raw-body {
        max-height: 320px;
        padding: 10px;
        margin: 0;
        overflow: auto;
        font-size: 12px;
        line-height: 1.5;
        word-break: break-word;
        white-space: pre-wrap;
        background: var(--el-fill-color-darker);
        border-radius: 4px;
    }

    .rm-mono {
        font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
        font-size: 11px;
    }

    .rm-probe-pre {
        max-height: 440px;
        padding: 10px 12px;
        margin: 0;
        overflow: auto;
        font-size: 11.5px;
        line-height: 1.5;
        word-break: break-word;
        white-space: pre-wrap;
        background: var(--el-fill-color-light);
        border-radius: 6px;
    }

    .rm-muted {
        color: var(--el-text-color-secondary);
    }

    /* 用户视角：3 步复现、质疑与证伪流程向导 & 成本估算面板 */
    .rm-user-guide-box {
        margin-bottom: 12px;
        background: var(--el-bg-color);
        border: 1px solid var(--el-color-primary-light-7);
        border-radius: 8px;
        overflow: hidden;
    }

    .rm-ug-header {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
        padding: 10px 14px;
        background: var(--el-color-primary-light-9);
        cursor: pointer;
        user-select: none;
    }

    .rm-ug-title-wrap,
    .rm-ug-header-right {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
    }

    .rm-ug-badge {
        padding: 2px 8px;
        font-size: 11px;
        font-weight: 700;
        color: #ffffff;
        background: var(--el-color-primary);
        border-radius: 4px;
        letter-spacing: 0.3px;
    }

    .rm-ug-title {
        font-size: 13px;
        line-height: 1.45;
        color: var(--el-text-color-primary);
    }

    .rm-ug-body {
        padding: 12px 14px;
        border-top: 1px solid var(--el-border-color-extra-light);
    }

    /* 小屏/笔记本友好的自适应网格：Step 1 & Step 3 自适应双列或单列，含 5 列成本表的 Step 2 跨全宽舒展展示 */
    .rm-ug-grid {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(380px, 1fr));
        gap: 12px;
    }

    .rm-ug-step-card {
        display: flex;
        flex-direction: column;
        justify-content: space-between;
        min-width: 0;
        width: 100%;
        padding: 14px 16px;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 8px;

        &.is-wide-step {
            grid-column: 1 / -1;
        }
    }

    .rm-ug-table-scroll {
        width: 100%;
        overflow-x: auto;
    }

    .rm-ug-step-hdr {
        display: flex;
        gap: 8px;
        align-items: center;
        margin-bottom: 8px;
        font-size: 13px;
        line-height: 1.4;
    }

    .rm-ug-step-num {
        padding: 2px 7px;
        font-size: 10.5px;
        font-weight: 700;
        color: #1d4ed8;
        background: #dbeafe;
        border-radius: 4px;
        flex-shrink: 0;

        &.is-step2 {
            color: #b45309;
            background: #fef3c7;
        }

        &.is-step3 {
            color: #047857;
            background: #d1fae5;
        }
    }

    .rm-ug-step-desc {
        font-size: 12px;
        line-height: 1.6;
        color: var(--el-text-color-regular);

        p {
            margin: 0 0 6px;
        }

        .rm-ug-highlight {
            padding: 6px 8px;
            background: var(--el-color-warning-light-9);
            border-left: 3px solid var(--el-color-warning);
            border-radius: 4px;
            color: var(--el-text-color-primary);
        }
    }

    .rm-ug-step-actions {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        padding-top: 8px;
        margin-top: 6px;
        border-top: 1px dashed var(--el-border-color-lighter);
    }

    .rm-ug-cost-table {
        width: 100%;
        margin-top: 4px;
        font-size: 11.5px;
        font-variant-numeric: tabular-nums;
        border-collapse: collapse;
        background: var(--el-bg-color);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 4px;
        overflow: hidden;

        th,
        td {
            padding: 4px 7px;
            text-align: left;
            border-bottom: 1px solid var(--el-border-color-extra-light);
        }

        th {
            font-weight: 600;
            color: var(--el-text-color-secondary);
            background: var(--el-fill-color-light);
        }

        .is-free {
            font-weight: 600;
            color: var(--el-color-success);
        }

        .is-recommend {
            color: var(--el-color-primary);
        }
    }

    /* SDK 请求与 Answer/Judge 状态提示条 */
    .rm-cost-estimate-strip {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
        padding: 7px 10px;
        font-size: 12px;
        border-radius: 6px;
        border: 1px solid var(--el-border-color-lighter);

        &.is-free {
            background: var(--el-color-success-light-9);
            border-color: var(--el-color-success-light-5);
        }

        &.is-paid {
            background: var(--el-color-warning-light-9);
            border-color: var(--el-color-warning-light-5);
        }
    }

    .rm-ce-left,
    .rm-ce-right {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
    }

    .rm-ce-text {
        font-variant-numeric: tabular-nums;
        line-height: 1.5;
        color: var(--el-text-color-primary);
    }

    .rm-ac-title {
        font-size: 13px;
        font-weight: 600;
        color: var(--el-text-color-primary);
    }

    .rm-ac-cli-tip {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
        margin-top: 8px;
        font-size: 11.5px;
        color: var(--el-text-color-secondary);

        code {
            padding: 2px 6px;
            font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
            font-size: 11px;
            color: #e5e7eb;
            background: #111827;
            border-radius: 4px;
            word-break: break-all;
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
