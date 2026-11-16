<!-- plugin: ai v0.2.0 -->
<!--
    AI 资产体检与成本治理工作台。
    本文件是**源**（plugins/ai/web/views/index.vue）；落位副本 web/src/views/admin/plugin/ai/index.vue
    由安装器复制，两者逐字节一致（同步检查比对 sha256）。
-->
<template>
    <div class="default-main plugin-ai">
        <!-- 顶部全局指挥舱：标题 + 后台管理快捷跳板 + 时间窗切换 + 刷新 -->
        <el-card shadow="never" class="plugin-ai-command">
            <div class="plugin-ai-command-bar">
                <div class="plugin-ai-title-wrap">
                    <div class="plugin-ai-title-row">
                        <span class="plugin-ai-title">{{ t('plugin.ai.title') }}</span>
                        <el-tag v-if="report" :type="errors > 0 ? 'danger' : warnings > 0 ? 'warning' : 'success'" size="small">
                            {{ errors > 0 ? t('plugin.ai.severity.error') + ' ' + errors : warnings > 0 ? t('plugin.ai.severity.warning') + ' ' + warnings : 'OK' }}
                        </el-tag>
                    </div>
                    <div class="plugin-ai-subtitle">{{ t('plugin.ai.intro') }}</div>
                </div>
                <div class="plugin-ai-toolbar">
                    <el-button-group size="small">
                        <el-button type="primary" plain @click="goAdmin('/admin/plugin/ai/cost')">{{ t('plugin.ai.nav.costPage') }}</el-button>
                        <el-button @click="goAdmin('/admin/plugin/ai/supplier')">{{ t('plugin.ai.nav.vendors') }}</el-button>
                        <el-button @click="goAdmin('/admin/plugin/ai/channel')">{{ t('plugin.ai.nav.channels') }}</el-button>
                        <el-button @click="goAdmin('/admin/plugin/ai/model')">{{ t('plugin.ai.nav.models') }}</el-button>
                        <el-button @click="goAdmin('/admin/plugin/ai/model')">{{ t('plugin.ai.nav.bindings') }}</el-button>
                        <el-button @click="goAdmin('/admin/plugin/rolling-memory/eval')">{{ t('plugin.ai.nav.agents') }}</el-button>
                        <el-button @click="goAdmin('/admin/plugin/ai/usage-log')">{{ t('plugin.ai.nav.logs') }}</el-button>
                    </el-button-group>
                    <el-radio-group v-model="costHours" size="small" @change="load">
                        <el-radio-button :value="24">{{ t('plugin.ai.window.h24') }}</el-radio-button>
                        <el-radio-button :value="168">{{ t('plugin.ai.window.d7') }}</el-radio-button>
                        <el-radio-button :value="720">{{ t('plugin.ai.window.d30') }}</el-radio-button>
                    </el-radio-group>
                    <el-button v-blur :loading="loading" type="primary" size="small" @click="load">
                        {{ t('plugin.ai.refresh') }}
                    </el-button>
                </div>
            </div>

            <el-alert v-if="failed" class="plugin-ai-alert" type="error" :closable="false" show-icon :title="t('plugin.ai.failedTip')" />
            <el-alert
                v-if="truncated.length > 0"
                class="plugin-ai-alert"
                type="warning"
                :closable="false"
                show-icon
                :title="t('plugin.ai.truncated', { faces: truncated.join(', ') })"
            />

            <!-- 8 宫格可交互 KPI 指挥卡（点击切换对应工作区） -->
            <el-row :gutter="10" class="plugin-ai-stats">
                <el-col v-for="card in statCards" :key="card.key" :xs="12" :sm="6" :md="3">
                    <div
                        class="plugin-ai-stat"
                        :class="{ 'is-active': activeWorkspace === card.workspace, ['tone-' + card.tone]: true }"
                        @click="onSelectCard(card.workspace, card.subTab)"
                    >
                        <div class="plugin-ai-stat-label">{{ card.label }}</div>
                        <div class="plugin-ai-stat-value">{{ card.value }}</div>
                        <div class="plugin-ai-stat-sub">{{ card.sub }}</div>
                    </div>
                </el-col>
            </el-row>
        </el-card>

        <!-- 四大主工作区切换 -->
        <el-card shadow="never" class="plugin-ai-workspace">
            <el-tabs v-model="activeWorkspace" class="plugin-ai-main-tabs">
                <!-- 工作区一：体检诊断与实时态势 -->
                <el-tab-pane :label="t('plugin.ai.workspace.diagnosis')" name="diagnosis">
                    <div v-if="report" class="plugin-ai-posture">
                        <div class="plugin-ai-posture-group">
                            <span class="plugin-ai-runtime-label">{{ t('plugin.ai.runtime.gate') }}：</span>
                            <template v-if="report.runtime.gate.enabled">
                                <el-tag type="primary" effect="plain" size="small">
                                    {{
                                        t('plugin.ai.runtime.gateInflight', {
                                            inflight: report.runtime.gate.inflight,
                                            limit: report.runtime.gate.limit,
                                            max: report.runtime.gate.max,
                                        })
                                    }}
                                </el-tag>
                                <span class="plugin-ai-runtime-sub">
                                    {{
                                        t('plugin.ai.runtime.gateQueue', {
                                            interactive: report.runtime.gate.queued_interactive,
                                            eval: report.runtime.gate.queued_eval,
                                            batch: report.runtime.gate.queued_batch,
                                            downshifts: report.runtime.gate.downshifts,
                                        })
                                    }}
                                </span>
                            </template>
                            <el-tag v-else type="info" effect="plain" size="small">{{ t('plugin.ai.runtime.gateDisabled') }}</el-tag>
                        </div>
                        <div class="plugin-ai-posture-group">
                            <span class="plugin-ai-runtime-label">{{ t('plugin.ai.runtime.budget') }}：</span>
                            <el-tag :type="report.runtime.budget.fail_open > 0 ? 'warning' : 'success'" effect="plain" size="small">
                                {{
                                    t('plugin.ai.runtime.budgetSummary', {
                                        waits: report.runtime.budget.waits,
                                        p95: report.runtime.budget.wait_p95_ms,
                                        failOpen: report.runtime.budget.fail_open,
                                    })
                                }}
                            </el-tag>
                        </div>
                        <div class="plugin-ai-posture-group">
                            <el-tag :type="report.runtime.model_freeze_ok ? 'success' : 'danger'" size="small">
                                {{ t('plugin.ai.runtime.policyFreeze') }}: {{ policyLabel(report.runtime.model_freeze_ok) }}
                            </el-tag>
                            <el-tag :type="report.runtime.official_isolation_ok ? 'success' : 'danger'" size="small">
                                {{ t('plugin.ai.runtime.policyOfficial') }}: {{ policyLabel(report.runtime.official_isolation_ok) }}
                            </el-tag>
                            <el-tag :type="report.runtime.official_clone_ok ? 'success' : 'danger'" size="small">
                                {{ t('plugin.ai.runtime.policyClone') }}: {{ policyLabel(report.runtime.official_clone_ok) }}
                            </el-tag>
                        </div>
                    </div>

                    <div class="plugin-ai-filter-bar">
                        <div class="plugin-ai-actions">
                            <el-radio-group v-model="severityFilter" size="small">
                                <el-radio-button value="all">{{ t('plugin.ai.findings.filterAll') }} ({{ findings.length }})</el-radio-button>
                                <el-radio-button value="error">{{ t('plugin.ai.findings.errors', { count: errors }) }}</el-radio-button>
                                <el-radio-button value="warning">{{ t('plugin.ai.findings.warnings', { count: warnings }) }}</el-radio-button>
                            </el-radio-group>
                            <el-radio-group v-model="categoryFilter" size="small">
                                <el-radio-button value="all">{{ t('plugin.ai.findings.catAll') }}</el-radio-button>
                                <el-radio-button value="topology">{{ t('plugin.ai.findings.catTopology') }}</el-radio-button>
                                <el-radio-button value="pricing">{{ t('plugin.ai.findings.catPricing') }}</el-radio-button>
                                <el-radio-button value="runtime">{{ t('plugin.ai.findings.catRuntime') }}</el-radio-button>
                                <el-radio-button value="policy">{{ t('plugin.ai.findings.catPolicy') }}</el-radio-button>
                            </el-radio-group>
                        </div>
                        <el-input
                            v-model="findingKeyword"
                            size="small"
                            clearable
                            class="plugin-ai-search"
                            :placeholder="t('plugin.ai.searchPlaceholder')"
                        />
                    </div>

                    <el-table v-loading="loading" :data="filteredFindings" border size="small" :empty-text="t('plugin.ai.findings.empty')">
                        <el-table-column :label="t('plugin.ai.findings.severity')" width="95" align="center">
                            <template #default="{ row }">
                                <el-tag :type="severityTagType(row.severity)" size="small">{{ t('plugin.ai.severity.' + row.severity) }}</el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.findings.item')" min-width="240">
                            <template #default="{ row }">{{ t('plugin.ai.finding.' + row.code) }}</template>
                        </el-table-column>
                        <el-table-column prop="ref" :label="t('plugin.ai.findings.ref')" width="170" />
                        <el-table-column prop="detail" :label="t('plugin.ai.findings.detail')" min-width="280" show-overflow-tooltip />
                        <el-table-column :label="t('plugin.ai.findings.action')" width="150" align="center">
                            <template #default="{ row }">
                                <el-link type="primary" :underline="false" class="plugin-ai-link" @click="locateRef(row.ref)">
                                    {{ t('plugin.ai.locate') }}
                                </el-link>
                                <el-link type="primary" :underline="false" class="plugin-ai-link" @click="manageByRef(row.ref)">
                                    {{ t('plugin.ai.manage') }}
                                </el-link>
                            </template>
                        </el-table-column>
                    </el-table>
                </el-tab-pane>

                <!-- 工作区二：成本与缓存经济学子模块 -->
                <el-tab-pane :label="t('plugin.ai.workspace.cost')" name="cost">
                    <div v-if="costSummary" class="plugin-ai-cost-banner">
                        <div class="plugin-ai-cost-head">
                            <span class="plugin-ai-runtime-label">{{ t('plugin.ai.cost.equationTitle') }}</span>
                            <span class="plugin-ai-runtime-sub">
                                {{
                                    t('plugin.ai.cost.tokensBreakdown', {
                                        input: formatInt(costSummary.input_tokens),
                                        hit: formatInt(costSummary.cache_hit_tokens),
                                        hitRate: formatPct(costSummary.cache_hit_rate),
                                        output: formatInt(costSummary.output_tokens),
                                        latency: costSummary.avg_latency_ms,
                                    })
                                }}
                            </span>
                        </div>
                        <el-row :gutter="12" class="plugin-ai-cost-cards">
                            <el-col :xs="12" :sm="8" :md="4">
                                <div class="plugin-ai-cost-box is-primary">
                                    <div class="plugin-ai-stat-label">{{ t('plugin.ai.cost.actualCost') }}</div>
                                    <div class="plugin-ai-stat-value">¥{{ costSummary.total_cost.toFixed(4) }}</div>
                                </div>
                            </el-col>
                            <el-col :xs="12" :sm="8" :md="4">
                                <div class="plugin-ai-cost-box">
                                    <div class="plugin-ai-stat-label">{{ t('plugin.ai.cost.listCost') }}</div>
                                    <div class="plugin-ai-stat-value">¥{{ costSummary.list_cost.toFixed(4) }}</div>
                                </div>
                            </el-col>
                            <el-col :xs="12" :sm="8" :md="4">
                                <div class="plugin-ai-cost-box is-success">
                                    <div class="plugin-ai-stat-label">{{ t('plugin.ai.cost.cacheSaved') }}</div>
                                    <div class="plugin-ai-stat-value">-¥{{ costSummary.cache_saved.toFixed(4) }}</div>
                                </div>
                            </el-col>
                            <el-col :xs="12" :sm="8" :md="4">
                                <div class="plugin-ai-cost-box is-success">
                                    <div class="plugin-ai-stat-label">{{ t('plugin.ai.cost.offpeakSaved') }}</div>
                                    <div class="plugin-ai-stat-value">-¥{{ costSummary.offpeak_saved.toFixed(4) }}</div>
                                </div>
                            </el-col>
                            <el-col :xs="12" :sm="8" :md="4">
                                <div class="plugin-ai-cost-box is-success">
                                    <div class="plugin-ai-stat-label">{{ t('plugin.ai.cost.totalSaved') }}</div>
                                    <div class="plugin-ai-stat-value">-¥{{ costSummary.total_saved.toFixed(4) }}</div>
                                </div>
                            </el-col>
                            <el-col :xs="12" :sm="8" :md="4">
                                <div class="plugin-ai-cost-box is-warning">
                                    <div class="plugin-ai-stat-label">{{ t('plugin.ai.cost.savedRate') }}</div>
                                    <div class="plugin-ai-stat-value">{{ formatPct(costSummary.saved_rate) }}%</div>
                                </div>
                            </el-col>
                        </el-row>
                    </div>

                    <el-tabs v-model="activeCostTab">
                        <el-tab-pane :label="t('plugin.ai.cost.tabByModel')" name="by_model">
                            <el-table v-loading="loading" :data="costByModel" border size="small" :empty-text="t('plugin.ai.emptyFace')">
                                <el-table-column prop="model" :label="t('plugin.ai.cost.colModel')" min-width="190" />
                                <el-table-column prop="vendor_title" :label="t('plugin.ai.model.vendor')" width="130" />
                                <el-table-column :label="t('plugin.ai.cost.colCalls')" width="100" align="right">
                                    <template #default="{ row }">{{ formatInt(row.calls) }}</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colInputTokens')" width="125" align="right">
                                    <template #default="{ row }">{{ formatInt(row.input_tokens) }}</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colCacheHit')" width="155" align="right">
                                    <template #default="{ row }">
                                        {{ formatInt(row.cache_hit_tokens) }} ({{ formatPct(row.cache_hit_rate) }}%)
                                    </template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colOutputTokens')" width="115" align="right">
                                    <template #default="{ row }">{{ formatInt(row.output_tokens) }}</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colOffpeak')" width="140" align="right">
                                    <template #default="{ row }">{{ formatInt(row.offpeak_calls) }} ({{ formatPct(row.offpeak_rate) }}%)</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colActualCost')" width="125" align="right">
                                    <template #default="{ row }">
                                        <span class="plugin-ai-strong">¥{{ row.cost.toFixed(4) }}</span>
                                    </template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colListCost')" width="120" align="right">
                                    <template #default="{ row }">¥{{ row.list_cost.toFixed(4) }}</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colSaved')" width="165" align="right">
                                    <template #default="{ row }">
                                        -¥{{ row.cache_saved.toFixed(2) }} / -¥{{ row.offpeak_saved.toFixed(2) }}
                                    </template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colShare')" width="110" align="right">
                                    <template #default="{ row }">{{ formatPct(row.cost_share) }}%</template>
                                </el-table-column>
                            </el-table>
                        </el-tab-pane>

                        <el-tab-pane :label="t('plugin.ai.cost.tabByAgent')" name="by_agent">
                            <el-table v-loading="loading" :data="costByAgent" border size="small" :empty-text="t('plugin.ai.emptyFace')">
                                <el-table-column :label="t('plugin.ai.cost.colAgent')" min-width="200">
                                    <template #default="{ row }">
                                        <span class="plugin-ai-strong">{{ row.agent_title || row.agent_name }}</span>
                                        <span v-if="row.agent_title" class="plugin-ai-muted"> ({{ row.agent_name }})</span>
                                    </template>
                                </el-table-column>
                                <el-table-column prop="model" :label="t('plugin.ai.cost.colModel')" min-width="170" />
                                <el-table-column :label="t('plugin.ai.cost.colCalls')" width="100" align="right">
                                    <template #default="{ row }">{{ formatInt(row.calls) }}</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colCacheHit')" width="150" align="right">
                                    <template #default="{ row }">
                                        {{ formatInt(row.cache_hit_tokens) }} ({{ formatPct(row.cache_hit_rate) }}%)
                                    </template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colTotalTokens')" width="125" align="right">
                                    <template #default="{ row }">{{ formatInt(row.total_tokens) }}</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colActualCost')" width="125" align="right">
                                    <template #default="{ row }">
                                        <span class="plugin-ai-strong">¥{{ row.cost.toFixed(4) }}</span>
                                    </template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colCostPer1k')" width="130" align="right">
                                    <template #default="{ row }">¥{{ row.cost_per_1k_calls.toFixed(4) }}</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colShare')" width="100" align="right">
                                    <template #default="{ row }">{{ formatPct(row.cost_share) }}%</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.findings.action')" width="110" align="center">
                                    <template #default="{ row }">
                                        <el-link type="primary" :underline="false" @click="goAdmin('/admin/plugin/rolling-memory/eval', { keywords: row.agent_name })">
                                            {{ t('plugin.ai.manage') }}
                                        </el-link>
                                    </template>
                                </el-table-column>
                            </el-table>
                        </el-tab-pane>

                        <el-tab-pane :label="t('plugin.ai.cost.tabByChannel')" name="by_channel">
                            <el-table v-loading="loading" :data="costByChannel" border size="small" :empty-text="t('plugin.ai.emptyFace')">
                                <el-table-column prop="channel_id" label="ID" width="70" />
                                <el-table-column prop="channel_name" :label="t('plugin.ai.cost.colChannel')" min-width="210" />
                                <el-table-column prop="vendor_title" :label="t('plugin.ai.col.vendor')" width="140" />
                                <el-table-column :label="t('plugin.ai.cost.colCalls')" width="110" align="right">
                                    <template #default="{ row }">{{ formatInt(row.calls) }}</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colSuccessRate')" width="110" align="right">
                                    <template #default="{ row }">{{ formatPct(row.success_rate) }}%</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colLatency')" width="115" align="right">
                                    <template #default="{ row }">{{ row.avg_latency_ms }} ms</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colTotalTokens')" width="130" align="right">
                                    <template #default="{ row }">{{ formatInt(row.total_tokens) }}</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colActualCost')" width="130" align="right">
                                    <template #default="{ row }">
                                        <span class="plugin-ai-strong">¥{{ row.cost.toFixed(4) }}</span>
                                    </template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colShare')" width="105" align="right">
                                    <template #default="{ row }">{{ formatPct(row.cost_share) }}%</template>
                                </el-table-column>
                            </el-table>
                        </el-tab-pane>

                        <el-tab-pane :label="t('plugin.ai.cost.tabBySource')" name="by_source">
                            <el-table v-loading="loading" :data="costBySource" border size="small" :empty-text="t('plugin.ai.emptyFace')">
                                <el-table-column prop="source" :label="t('plugin.ai.cost.colSource')" min-width="180" />
                                <el-table-column :label="t('plugin.ai.cost.colCalls')" width="120" align="right">
                                    <template #default="{ row }">{{ formatInt(row.calls) }}</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colOffpeak')" width="130" align="right">
                                    <template #default="{ row }">{{ formatInt(row.offpeak_calls) }}</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colTotalTokens')" width="140" align="right">
                                    <template #default="{ row }">{{ formatInt(row.total_tokens) }}</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colActualCost')" width="140" align="right">
                                    <template #default="{ row }">
                                        <span class="plugin-ai-strong">¥{{ row.cost.toFixed(4) }}</span>
                                    </template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colSaved')" width="140" align="right">
                                    <template #default="{ row }">-¥{{ row.total_saved.toFixed(4) }}</template>
                                </el-table-column>
                                <el-table-column :label="t('plugin.ai.cost.colShare')" width="110" align="right">
                                    <template #default="{ row }">{{ formatPct(row.cost_share) }}%</template>
                                </el-table-column>
                            </el-table>
                        </el-tab-pane>
                    </el-tabs>
                </el-tab-pane>

                <!-- 工作区三：路由拓扑与四维资产台账 -->
                <el-tab-pane :label="t('plugin.ai.workspace.topology')" name="topology">
                    <div class="plugin-ai-filter-bar">
                        <el-radio-group v-model="activeFace" size="small">
                            <el-radio-button value="models">{{ t('plugin.ai.face.models') }} ({{ models.length }})</el-radio-button>
                            <el-radio-button value="channels">{{ t('plugin.ai.face.channels') }} ({{ channels.length }})</el-radio-button>
                            <el-radio-button value="bindings">{{ t('plugin.ai.face.bindings') }} ({{ bindings.length }})</el-radio-button>
                            <el-radio-button value="vendors">{{ t('plugin.ai.face.vendors') }} ({{ vendors.length }})</el-radio-button>
                        </el-radio-group>
                        <div class="plugin-ai-actions">
                            <el-switch v-model="onlyAnomalies" size="small" :active-text="t('plugin.ai.onlyAnomalies')" />
                            <el-input
                                v-model="assetKeyword"
                                size="small"
                                clearable
                                class="plugin-ai-search"
                                :placeholder="t('plugin.ai.searchPlaceholder')"
                            />
                        </div>
                    </div>

                    <el-table v-if="activeFace === 'models'" v-loading="loading" :data="filteredModels" border size="small" :empty-text="t('plugin.ai.emptyFace')">
                        <el-table-column prop="id" label="ID" width="60" />
                        <el-table-column prop="name" :label="t('plugin.ai.model.name')" min-width="180" />
                        <el-table-column prop="vendor_title" :label="t('plugin.ai.model.vendor')" width="135" />
                        <el-table-column :label="t('plugin.ai.col.type')" width="85">
                            <template #default="{ row }">
                                <el-tag effect="plain" size="small">{{ modelTypeLabel(row.type) }}</el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column prop="input_price" :label="t('plugin.ai.model.inputPrice')" width="105" />
                        <el-table-column prop="output_price" :label="t('plugin.ai.model.outputPrice')" width="105" />
                        <el-table-column prop="cache_input_price" :label="t('plugin.ai.model.cachePrice')" width="110" />
                        <el-table-column prop="offpeak_ratio" :label="t('plugin.ai.model.offpeak')" width="90" />
                        <el-table-column :label="t('plugin.ai.model.bindings')" width="105" align="center">
                            <template #default="{ row }">
                                <el-tag :type="row.active_binding_count > 0 ? 'success' : 'danger'" effect="plain" size="small">
                                    {{ row.active_binding_count }} / {{ row.binding_count }}
                                </el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column prop="active_agent_count" :label="t('plugin.ai.model.agents')" width="110" align="center" />
                        <el-table-column :label="t('plugin.ai.model.routeTiers')" min-width="320">
                            <template #default="{ row }">
                                <template v-if="row.route_tiers && row.route_tiers.length > 0">
                                    <div v-for="(tier, idx) in row.route_tiers" :key="tier.priority" class="plugin-ai-tier">
                                        <el-tag size="small" type="primary" effect="plain">
                                            T{{ Number(idx) + 1 }} (P={{ tier.priority }} · ΣW={{ tier.total_weigh }})
                                        </el-tag>
                                        <span v-for="tItem in tier.targets" :key="tItem.binding_id" class="plugin-ai-tier-target">
                                            <el-tag :type="tItem.routable ? 'success' : 'danger'" size="small" effect="light">
                                                #{{ tItem.channel_id }} {{ tItem.channel_name }} ({{ tItem.driver }} · w={{ tItem.weigh }})
                                            </el-tag>
                                        </span>
                                    </div>
                                </template>
                                <el-tag v-else type="danger" size="small">{{ t('plugin.ai.model.noRoute') }}</el-tag>
                            </template>
                        </el-table-column>
                    </el-table>

                    <el-table v-else-if="activeFace === 'channels'" v-loading="loading" :data="filteredChannels" border size="small" :empty-text="t('plugin.ai.emptyFace')">
                        <el-table-column prop="id" label="ID" width="60" />
                        <el-table-column prop="name" :label="t('plugin.ai.channel.name')" min-width="200" show-overflow-tooltip />
                        <el-table-column prop="vendor_title" :label="t('plugin.ai.col.vendor')" width="135" />
                        <el-table-column :label="t('plugin.ai.col.driver')" width="115">
                            <template #default="{ row }">
                                <el-tag :type="row.driver_supported ? 'primary' : 'danger'" effect="plain" size="small">
                                    {{ row.effective_driver }}
                                </el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.channel.rerankCap')" width="90" align="center">
                            <template #default="{ row }">
                                <el-tag :type="row.supports_rerank ? 'success' : 'info'" effect="plain" size="small">
                                    {{ boolLabel(row.supports_rerank) }}
                                </el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.channel.priorityWeigh')" width="115">
                            <template #default="{ row }">P={{ row.priority }} / W={{ row.weigh }}</template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.channel.tpmBudget')" width="170">
                            <template #default="{ row }">
                                <span v-if="row.tpm_limit > 0">
                                    {{ Math.round(row.budget_tokens).toLocaleString() }} / {{ row.tpm_limit.toLocaleString() }}
                                </span>
                                <span v-else class="plugin-ai-muted">{{ t('plugin.ai.channel.tpmUnlimited') }}</span>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.channel.key')" width="90" align="center">
                            <template #default="{ row }">
                                <el-tag :type="row.api_key_set ? 'success' : 'danger'" size="small">{{ boolLabel(row.api_key_set) }}</el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.col.breaker')" width="145" align="center">
                            <template #default="{ row }">
                                <el-tag :type="breakerTagType(row.breaker_state, row.breaker_permanent)" size="small">
                                    {{ breakerLabel(row.breaker_state, row.breaker_permanent) }}
                                </el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.channel.callsFailures')" width="125" align="center">
                            <template #default="{ row }">{{ row.total_calls }} / {{ row.total_failures }}</template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.channel.bindings')" width="105" align="center">
                            <template #default="{ row }">{{ row.active_binding_count }} / {{ row.binding_count }}</template>
                        </el-table-column>
                    </el-table>

                    <el-table v-else-if="activeFace === 'bindings'" v-loading="loading" :data="filteredBindings" border size="small" :empty-text="t('plugin.ai.emptyFace')">
                        <el-table-column prop="id" label="ID" width="60" />
                        <el-table-column prop="model_name" :label="t('plugin.ai.binding.model')" min-width="180" />
                        <el-table-column prop="channel_name" :label="t('plugin.ai.binding.channel')" min-width="200" show-overflow-tooltip />
                        <el-table-column prop="effective_upstream" :label="t('plugin.ai.binding.upstream')" min-width="160" />
                        <el-table-column prop="effective_driver" :label="t('plugin.ai.binding.driver')" width="110" />
                        <el-table-column :label="t('plugin.ai.binding.priorityWeigh')" width="125">
                            <template #default="{ row }">P={{ row.effective_priority }} / W={{ row.effective_weigh }}</template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.binding.routable')" width="95" align="center">
                            <template #default="{ row }">
                                <el-tag :type="row.routable ? 'success' : 'danger'" size="small">{{ boolLabel(row.routable) }}</el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column prop="block_reason" :label="t('plugin.ai.binding.blockReason')" min-width="150" />
                    </el-table>

                    <el-table v-else v-loading="loading" :data="filteredVendors" border size="small" :empty-text="t('plugin.ai.emptyFace')">
                        <el-table-column prop="id" label="ID" width="65" />
                        <el-table-column prop="name" :label="t('plugin.ai.vendor.name')" width="130" />
                        <el-table-column prop="title" :label="t('plugin.ai.vendor.title')" width="160" />
                        <el-table-column :label="t('plugin.ai.vendor.driver')" width="120">
                            <template #default="{ row }">
                                <el-tag :type="row.driver_supported ? 'primary' : 'danger'" effect="plain" size="small">{{ row.driver }}</el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column prop="base_url" :label="t('plugin.ai.vendor.baseUrl')" min-width="260" show-overflow-tooltip />
                        <el-table-column :label="t('plugin.ai.status')" width="90" align="center">
                            <template #default="{ row }">
                                <el-tag :type="statusTagType(row.status)" size="small">{{ statusLabel(row.status) }}</el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column prop="model_count" :label="t('plugin.ai.vendor.models')" width="100" align="center" />
                        <el-table-column :label="t('plugin.ai.vendor.channels')" width="150" align="center">
                            <template #default="{ row }">{{ row.active_channel_count }} / {{ row.channel_count }}</template>
                        </el-table-column>
                    </el-table>
                </el-tab-pane>

                <!-- 工作区四：智能体消费与策略合规矩阵 -->
                <el-tab-pane :label="t('plugin.ai.workspace.governance')" name="governance">
                    <div class="plugin-ai-filter-bar">
                        <el-switch v-model="onlyAnomalies" size="small" :active-text="t('plugin.ai.onlyAnomalies')" />
                        <el-input
                            v-model="assetKeyword"
                            size="small"
                            clearable
                            class="plugin-ai-search"
                            :placeholder="t('plugin.ai.searchPlaceholder')"
                        />
                    </div>
                    <el-table v-loading="loading" :data="filteredAgents" border size="small" :empty-text="t('plugin.ai.emptyFace')">
                        <el-table-column prop="id" label="ID" width="65" />
                        <el-table-column prop="name" :label="t('plugin.ai.agent.name')" min-width="180" />
                        <el-table-column prop="title" :label="t('plugin.ai.agent.title')" min-width="150" show-overflow-tooltip />
                        <el-table-column :label="t('plugin.ai.agent.type')" width="90" align="center">
                            <template #default="{ row }">
                                <el-tag effect="plain" size="small">{{ modelTypeLabel(row.type) }}</el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column prop="model" :label="t('plugin.ai.agent.model')" min-width="180" />
                        <el-table-column :label="t('plugin.ai.status')" width="85" align="center">
                            <template #default="{ row }">
                                <el-tag :type="statusTagType(row.status)" size="small">{{ statusLabel(row.status) }}</el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.agent.typeMatch')" width="95" align="center">
                            <template #default="{ row }">
                                <el-tag :type="row.type_match ? 'success' : 'danger'" size="small">{{ boolLabel(row.type_match) }}</el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.agent.routable')" width="95" align="center">
                            <template #default="{ row }">
                                <el-tag :type="row.routable ? 'success' : 'danger'" size="small">{{ boolLabel(row.routable) }}</el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.agent.compliance')" width="150" align="center">
                            <template #default="{ row }">
                                <el-tag :type="row.whitelist_ok && row.official_ok ? 'success' : 'danger'" size="small">
                                    {{ policyLabel(row.whitelist_ok && row.official_ok) }}
                                </el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column :label="t('plugin.ai.findings.action')" width="110" align="center">
                            <template #default="{ row }">
                                <el-link type="primary" :underline="false" @click="goAdmin('/admin/plugin/rolling-memory/eval', { keywords: row.name })">
                                    {{ t('plugin.ai.manage') }}
                                </el-link>
                            </template>
                        </el-table-column>
                    </el-table>
                </el-tab-pane>
            </el-tabs>
            <div v-if="limitsText" class="plugin-ai-limits">{{ limitsText }}</div>
        </el-card>
    </div>
</template>

<script setup lang="ts">
import type {
    AiAgentCostItem,
    AiAssetAgent,
    AiAssetBinding,
    AiAssetChannel,
    AiAssetFinding,
    AiAssetModel,
    AiAssetReport,
    AiAssetVendor,
    AiChannelCostRow,
    AiCostSummary,
    AiModelCostRow,
    AiSeverity,
    AiSourceCostRow,
} from './api'
import { getAssetReport } from './api'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

// keep-alive 缓存名 = 菜单 name（plugin.yaml menus：plugin/ai/index）
defineOptions({
    name: 'plugin/ai/index',
})

const { t } = useI18n()
const router = useRouter()

type TagType = 'primary' | 'success' | 'warning' | 'info' | 'danger'
type WorkspaceKey = 'diagnosis' | 'cost' | 'topology' | 'governance'
type FaceKey = 'models' | 'channels' | 'bindings' | 'vendors'
type CostTabKey = 'by_model' | 'by_agent' | 'by_channel' | 'by_source'
type SeverityFilter = 'all' | 'error' | 'warning'
type CategoryFilter = 'all' | 'topology' | 'pricing' | 'runtime' | 'policy'

const loading = ref(false)
const failed = ref(false)
const costHours = ref(24)
const activeWorkspace = ref<WorkspaceKey>('diagnosis')
const activeFace = ref<FaceKey>('models')
const activeCostTab = ref<CostTabKey>('by_model')
const severityFilter = ref<SeverityFilter>('all')
const categoryFilter = ref<CategoryFilter>('all')
const findingKeyword = ref('')
const assetKeyword = ref('')
const onlyAnomalies = ref(false)
const report = ref<AiAssetReport | null>(null)

const findings = computed<AiAssetFinding[]>(() => report.value?.findings ?? [])
const vendors = computed<AiAssetVendor[]>(() => report.value?.vendors ?? [])
const models = computed<AiAssetModel[]>(() => report.value?.models ?? [])
const channels = computed<AiAssetChannel[]>(() => report.value?.channels ?? [])
const bindings = computed<AiAssetBinding[]>(() => report.value?.bindings ?? [])
const agents = computed<AiAssetAgent[]>(() => report.value?.agents ?? [])
const costSummary = computed<AiCostSummary | null>(() => report.value?.cost?.summary ?? null)
const costByModel = computed<AiModelCostRow[]>(() => report.value?.cost?.by_model ?? [])
const costByAgent = computed<AiAgentCostItem[]>(() => report.value?.cost?.by_agent ?? [])
const costByChannel = computed<AiChannelCostRow[]>(() => report.value?.cost?.by_channel ?? [])
const costBySource = computed<AiSourceCostRow[]>(() => report.value?.cost?.by_source ?? [])
const truncated = computed<string[]>(() => report.value?.truncated ?? [])
const errors = computed(() => report.value?.summary.errors ?? 0)
const warnings = computed(() => report.value?.summary.warnings ?? 0)

const categoryOfCode = (code: string): CategoryFilter => {
    if (code.includes('price') || code.includes('peak') || code.includes('offpeak') || code.includes('cost')) return 'pricing'
    if (code.includes('breaker') || code.includes('tpm')) return 'runtime'
    if (code.includes('agent') || code.includes('policy')) return 'policy'
    return 'topology'
}

const filteredFindings = computed<AiAssetFinding[]>(() => {
    const kw = findingKeyword.value.trim().toLowerCase()
    return findings.value.filter((item) => {
        if (severityFilter.value !== 'all' && item.severity !== severityFilter.value) return false
        if (categoryFilter.value !== 'all' && categoryOfCode(item.code) !== categoryFilter.value) return false
        return !kw || item.ref.toLowerCase().includes(kw) || item.detail.toLowerCase().includes(kw) || item.code.toLowerCase().includes(kw)
    })
})

const matchKw = (text: string) => {
    const kw = assetKeyword.value.trim().toLowerCase()
    return !kw || text.toLowerCase().includes(kw)
}

const filteredModels = computed(() =>
    models.value.filter((m) => (!onlyAnomalies.value || m.active_binding_count === 0 || !m.price_ok || !m.peak_windows_ok || !m.whitelist_ok) && matchKw(`${m.id} ${m.name} ${m.vendor_title} ${m.type}`))
)
const filteredChannels = computed(() =>
    channels.value.filter((c) => (!onlyAnomalies.value || !c.api_key_set || c.breaker_state !== 'closed' || c.breaker_permanent || c.active_binding_count === 0) && matchKw(`${c.id} ${c.name} ${c.vendor_title} ${c.effective_driver}`))
)
const filteredBindings = computed(() =>
    bindings.value.filter((b) => (!onlyAnomalies.value || !b.routable) && matchKw(`${b.id} ${b.model_name} ${b.channel_name} ${b.effective_upstream} ${b.effective_driver}`))
)
const filteredVendors = computed(() =>
    vendors.value.filter((v) => (!onlyAnomalies.value || v.status !== 'enable' || !v.driver_supported || v.active_channel_count === 0) && matchKw(`${v.id} ${v.name} ${v.title} ${v.driver} ${v.base_url}`))
)
const filteredAgents = computed(() =>
    agents.value.filter((a) => (!onlyAnomalies.value || !a.routable || !a.type_match || !a.whitelist_ok || !a.official_ok) && matchKw(`${a.id} ${a.name} ${a.title} ${a.model} ${a.type}`))
)

const formatInt = (n?: number) => (n === undefined ? '-' : Math.round(n).toLocaleString())
const formatPct = (r?: number) => (r === undefined ? '0.0' : (r * 100).toFixed(1))

const statCards = computed(() => {
    const s = report.value?.summary
    const c = costSummary.value
    const rt = report.value?.runtime
    const pair = (a?: number, b?: number) => (a === undefined || b === undefined ? '-' : `${a} / ${b}`)
    const policyOK = rt ? rt.model_freeze_ok && rt.official_isolation_ok && rt.official_clone_ok : true
    return [
        { key: 'findings', workspace: 'diagnosis' as WorkspaceKey, subTab: '', tone: (s?.errors ?? 0) > 0 ? 'danger' : (s?.warnings ?? 0) > 0 ? 'warning' : 'success', label: t('plugin.ai.stats.findings'), value: pair(s?.errors, s?.warnings), sub: t('plugin.ai.stats.findingsSub') },
        { key: 'models', workspace: 'topology' as WorkspaceKey, subTab: 'models', tone: 'primary', label: t('plugin.ai.stats.models'), value: pair(s?.models_routable, s?.models), sub: t('plugin.ai.stats.modelsSub', { priced: s?.priced_models ?? 0, total: s?.models ?? 0 }) },
        { key: 'channels', workspace: 'topology' as WorkspaceKey, subTab: 'channels', tone: 'primary', label: t('plugin.ai.stats.channels'), value: pair(s?.channels_with_key, s?.channels), sub: t('plugin.ai.stats.channelsSub', { bindings: s?.routable_bindings ?? 0 }) },
        { key: 'breakers', workspace: 'topology' as WorkspaceKey, subTab: 'channels', tone: (s?.permanent_breakers ?? 0) > 0 || (s?.open_breakers ?? 0) > 0 ? 'danger' : 'success', label: t('plugin.ai.stats.breakers'), value: pair(s?.permanent_breakers, s?.open_breakers), sub: t('plugin.ai.stats.breakersSub', { perm: s?.permanent_breakers ?? 0, open: s?.open_breakers ?? 0, failOpen: rt?.budget.fail_open ?? 0 }) },
        { key: 'agents', workspace: 'governance' as WorkspaceKey, subTab: '', tone: policyOK ? 'success' : 'danger', label: t('plugin.ai.stats.agents'), value: pair(s?.routable_agents, s?.agents), sub: t('plugin.ai.stats.agentsSub', { pass: policyLabel(policyOK) }) },
        { key: 'costTotal', workspace: 'cost' as WorkspaceKey, subTab: 'by_model', tone: 'primary', label: t('plugin.ai.stats.costTotal'), value: c ? `¥${c.total_cost.toFixed(2)}` : '-', sub: t('plugin.ai.stats.costTotalSub', { list: c ? c.list_cost.toFixed(2) : '0.00', calls: formatInt(c?.total_calls) }) },
        { key: 'cacheHit', workspace: 'cost' as WorkspaceKey, subTab: 'by_agent', tone: 'success', label: t('plugin.ai.stats.cacheHit'), value: c ? `${formatPct(c.cache_hit_rate)}%` : '-', sub: t('plugin.ai.stats.cacheHitSub', { saved: c ? c.cache_saved.toFixed(2) : '0.00' }) },
        { key: 'offpeak', workspace: 'cost' as WorkspaceKey, subTab: 'by_source', tone: 'success', label: t('plugin.ai.stats.offpeak'), value: c ? `${formatPct(c.offpeak_rate)}%` : '-', sub: t('plugin.ai.stats.offpeakSub', { saved: c ? c.offpeak_saved.toFixed(2) : '0.00', rate: formatPct(c?.saved_rate) }) },
    ]
})

const onSelectCard = (ws: WorkspaceKey, subTab: string) => {
    activeWorkspace.value = ws
    if (ws === 'topology' && (subTab === 'models' || subTab === 'channels' || subTab === 'bindings' || subTab === 'vendors')) activeFace.value = subTab
    else if (ws === 'cost' && (subTab === 'by_model' || subTab === 'by_agent' || subTab === 'by_channel' || subTab === 'by_source')) activeCostTab.value = subTab
}

const locateRef = (refStr: string) => {
    const [prefix, idOrName] = refStr.split(':')
    assetKeyword.value = idOrName || ''
    onlyAnomalies.value = false
    if (prefix === 'model' || prefix === 'channel' || prefix === 'binding' || prefix === 'vendor') {
        activeWorkspace.value = 'topology'
        activeFace.value = (prefix + 's') as FaceKey
    } else if (prefix === 'agent') {
        activeWorkspace.value = 'governance'
    } else if (prefix === 'cost') {
        activeWorkspace.value = 'cost'
        activeCostTab.value = 'by_model'
    }
}

const goAdmin = (path: string, query?: Record<string, any>) => router.push({ path, query })

const manageByRef = (refStr: string) => {
    const [prefix] = refStr.split(':')
    const map: Record<string, string> = { model: '/admin/plugin/ai/model', channel: '/admin/plugin/ai/channel', binding: '/admin/plugin/ai/model', vendor: '/admin/plugin/ai/supplier', agent: '/admin/plugin/rolling-memory/eval', cost: '/admin/plugin/ai/usage-log' }
    goAdmin(map[prefix] || '/admin/plugin/ai/channel')
}

const limitsText = computed(() => {
    const l = report.value?.limits
    return l ? t('plugin.ai.limits', { vendors: l.vendors, models: l.models, channels: l.channels, bindings: l.bindings, agents: l.agents }) : ''
})

const load = async () => {
    loading.value = true
    failed.value = false
    try {
        report.value = await getAssetReport(costHours.value)
    } catch {
        report.value = null
        failed.value = true
    } finally {
        loading.value = false
    }
}

onMounted(() => load())

const severityTagType = (severity: AiSeverity): TagType => {
    if (severity === 'error') return 'danger'
    if (severity === 'warning') return 'warning'
    return 'info'
}

const statusTagType = (status: string): TagType => (status === 'enable' ? 'success' : 'info')
const statusLabel = (status: string) =>
    t(`plugin.ai.statusValue.${status === 'enable' ? 'enable' : status === 'disable' ? 'disable' : 'unknown'}`)

const modelTypeLabel = (type: string) => {
    if (type === 'chat' || type === 'embedding' || type === 'rerank') {
        return t(`plugin.ai.model.typeValue.${type}`)
    }
    return t('plugin.ai.model.typeValue.unknown')
}

const breakerTagType = (state: string, permanent: boolean): TagType => {
    if (permanent || state === 'open') return 'danger'
    if (state === 'half_open') return 'warning'
    return 'success'
}

const breakerLabel = (state: string, permanent: boolean) => {
    if (permanent) return t('plugin.ai.channel.breakerValue.permanent')
    if (state === 'open' || state === 'half_open') return t(`plugin.ai.channel.breakerValue.${state}`)
    return t('plugin.ai.channel.breakerValue.closed')
}

const boolLabel = (value: boolean) => (value ? t('plugin.ai.yes') : t('plugin.ai.no'))
const policyLabel = (ok: boolean) => (ok ? t('plugin.ai.runtime.pass') : t('plugin.ai.runtime.violated'))
</script>

<style scoped lang="scss">
.plugin-ai {
    .plugin-ai-command {
        margin-bottom: 12px;
    }

    .plugin-ai-command-bar {
        display: flex;
        align-items: flex-start;
        justify-content: space-between;
        flex-wrap: wrap;
        gap: 12px;
        margin-bottom: 12px;
    }

    .plugin-ai-title-row {
        display: flex;
        align-items: center;
        gap: 8px;
    }

    .plugin-ai-title {
        font-size: 18px;
        font-weight: 600;
        color: var(--el-text-color-primary);
    }

    .plugin-ai-subtitle {
        margin-top: 4px;
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .plugin-ai-toolbar {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 8px;
    }

    .plugin-ai-alert {
        margin-bottom: 10px;
    }

    .plugin-ai-stats {
        margin-top: 4px;
    }

    .plugin-ai-stat {
        padding: 10px 12px;
        margin-bottom: 8px;
        border: 1px solid var(--el-border-color-lighter);
        border-radius: var(--el-border-radius-base);
        background: var(--el-fill-color-blank);
        cursor: pointer;
        transition: border-color 0.15s ease;

        &:hover,
        &.is-active {
            border-color: var(--el-color-primary);
            background: var(--el-fill-color-light);
        }

        &.tone-danger .plugin-ai-stat-value {
            color: var(--el-color-danger);
        }

        &.tone-warning .plugin-ai-stat-value {
            color: var(--el-color-warning);
        }

        &.tone-success .plugin-ai-stat-value {
            color: var(--el-color-success);
        }
    }

    .plugin-ai-stat-label {
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .plugin-ai-stat-value {
        margin-top: 4px;
        font-size: 19px;
        font-weight: 700;
        color: var(--el-text-color-primary);
    }

    .plugin-ai-stat-sub {
        margin-top: 3px;
        font-size: 11px;
        color: var(--el-text-color-placeholder);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }

    .plugin-ai-posture {
        display: flex;
        align-items: center;
        justify-content: space-between;
        flex-wrap: wrap;
        gap: 10px;
        padding: 10px 12px;
        margin-bottom: 12px;
        background: var(--el-fill-color-lighter);
        border-radius: var(--el-border-radius-base);
    }

    .plugin-ai-posture-group {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 6px;
        font-size: 12px;
    }

    .plugin-ai-filter-bar {
        display: flex;
        align-items: center;
        justify-content: space-between;
        flex-wrap: wrap;
        gap: 8px;
        margin-bottom: 10px;
    }

    .plugin-ai-actions {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 8px;
    }

    .plugin-ai-search {
        width: 240px;
    }

    .plugin-ai-cost-banner {
        padding: 12px;
        margin-bottom: 12px;
        background: var(--el-fill-color-lighter);
        border-radius: var(--el-border-radius-base);
    }

    .plugin-ai-cost-head {
        display: flex;
        align-items: center;
        justify-content: space-between;
        flex-wrap: wrap;
        gap: 8px;
        margin-bottom: 10px;
    }

    .plugin-ai-cost-box {
        padding: 10px 12px;
        background: var(--el-bg-color);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: var(--el-border-radius-base);

        &.is-primary .plugin-ai-stat-value {
            color: var(--el-color-primary);
        }

        &.is-success .plugin-ai-stat-value {
            color: var(--el-color-success);
        }

        &.is-warning .plugin-ai-stat-value {
            color: var(--el-color-warning);
        }
    }

    .plugin-ai-runtime-label,
    .plugin-ai-strong {
        font-weight: 600;
    }

    .plugin-ai-runtime-sub,
    .plugin-ai-muted {
        color: var(--el-text-color-secondary);
        font-size: 12px;
    }

    .plugin-ai-tier {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 4px;
        margin-bottom: 4px;
    }

    .plugin-ai-limits {
        margin-top: 10px;
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .plugin-ai-link + .plugin-ai-link {
        margin-left: 10px;
    }
}
</style>
