<!--
    plugin: ai v0.2.0
    AI 成本与缓存经济学独立账单页（路由 /admin/plugin/ai/cost，节点 plugin/ai/cost）：
    本文件是**源**（plugins/ai/web/views/cost.vue）；落位副本 web/src/views/admin/plugin/ai/cost.vue
    必须与本文件逐字节一致（插件一致性检查的 frontend-sync 子检查）。
-->
<template>
    <div class="default-main ag-plugin-ai-cost" v-loading="loading" data-testid="plugin-ai-cost-page">
        <!-- 顶部命令栏：标题 + 时间窗切换 + 跨页跳板 -->
        <el-card class="ag-cost-header" shadow="never">
            <div class="ag-cost-header__row">
                <div>
                    <div class="ag-cost-header__title">{{ t('plugin.ai.cost.pageTitle') }}</div>
                    <div class="ag-cost-header__intro">{{ t('plugin.ai.cost.pageIntro') }}</div>
                </div>
                <div class="ag-cost-header__actions">
                    <el-radio-group v-model="costHours" size="small" data-testid="plugin-ai-cost-window" @change="load">
                        <el-radio-button :value="24">{{ t('plugin.ai.window.h24') }}</el-radio-button>
                        <el-radio-button :value="168">{{ t('plugin.ai.window.d7') }}</el-radio-button>
                        <el-radio-button :value="720">{{ t('plugin.ai.window.d30') }}</el-radio-button>
                    </el-radio-group>
                    <el-button-group size="small">
                        <el-button @click="goPage('/admin/plugin/ai/index')" data-testid="plugin-ai-cost-nav-health">
                            {{ t('plugin.ai.nav.healthConsole') }}
                        </el-button>
                        <el-button @click="goPage('/admin/plugin/ai/supplier')">{{ t('plugin.ai.nav.vendors') }}</el-button>
                        <el-button @click="goPage('/admin/plugin/ai/channel')">{{ t('plugin.ai.nav.channels') }}</el-button>
                        <el-button @click="goPage('/admin/plugin/ai/model')">{{ t('plugin.ai.nav.models') }}</el-button>
                        <el-button @click="goPage('/admin/plugin/rolling-memory/eval')">{{ t('plugin.ai.nav.agents') }}</el-button>
                        <el-button @click="goPage('/admin/plugin/ai/usage-log')">{{ t('plugin.ai.nav.logs') }}</el-button>
                    </el-button-group>
                    <el-button type="primary" size="small" :loading="loading" data-testid="plugin-ai-cost-refresh" @click="load">
                        {{ t('plugin.ai.refresh') }}
                    </el-button>
                </div>
            </div>

            <!-- 五维交叉筛选栏 -->
            <div class="ag-cost-filters" v-if="report">
                <el-select
                    v-model="filterVendor"
                    clearable
                    size="small"
                    class="ag-cost-filters__item"
                    :placeholder="t('plugin.ai.cost.filterVendor')"
                    data-testid="plugin-ai-cost-filter-vendor"
                >
                    <el-option v-for="v in vendorOptions" :key="v" :label="v" :value="v" />
                </el-select>
                <el-select
                    v-model="filterChannel"
                    clearable
                    filterable
                    size="small"
                    class="ag-cost-filters__item"
                    :placeholder="t('plugin.ai.cost.filterChannel')"
                    data-testid="plugin-ai-cost-filter-channel"
                >
                    <el-option v-for="c in channelOptions" :key="c" :label="c" :value="c" />
                </el-select>
                <el-select
                    v-model="filterModel"
                    clearable
                    filterable
                    size="small"
                    class="ag-cost-filters__item"
                    :placeholder="t('plugin.ai.cost.filterModel')"
                    data-testid="plugin-ai-cost-filter-model"
                >
                    <el-option v-for="m in modelOptions" :key="m" :label="m" :value="m" />
                </el-select>
                <el-select
                    v-model="filterAgent"
                    clearable
                    filterable
                    size="small"
                    class="ag-cost-filters__item"
                    :placeholder="t('plugin.ai.cost.filterAgent')"
                    data-testid="plugin-ai-cost-filter-agent"
                >
                    <el-option v-for="a in agentOptions" :key="a" :label="a" :value="a" />
                </el-select>
                <el-select
                    v-model="filterSource"
                    clearable
                    size="small"
                    class="ag-cost-filters__item"
                    :placeholder="t('plugin.ai.cost.filterSource')"
                    data-testid="plugin-ai-cost-filter-source"
                >
                    <el-option v-for="s in sourceOptions" :key="s" :label="s" :value="s" />
                </el-select>
                <el-input
                    v-model="searchKeyword"
                    clearable
                    size="small"
                    class="ag-cost-filters__search"
                    :placeholder="t('plugin.ai.searchPlaceholder')"
                    data-testid="plugin-ai-cost-search"
                />
                <el-button v-if="hasCrossFilter" size="small" @click="clearAllFilters" data-testid="plugin-ai-cost-clear-filter">
                    {{ t('plugin.ai.clearFilter') }}
                </el-button>
            </div>
        </el-card>

        <el-alert
            v-if="loadError"
            class="ag-cost-alert"
            type="error"
            show-icon
            :closable="false"
            :title="t('plugin.ai.failedTip')"
            data-testid="plugin-ai-cost-error"
        />

        <el-alert
            v-if="hasCrossFilter && report"
            class="ag-cost-alert"
            type="info"
            show-icon
            :closable="false"
            :title="t('plugin.ai.cost.filteredTip', { buckets: filteredBuckets.length })"
        />

        <template v-if="report">
            <!-- 6 张经济学核心 KPI 卡 -->
            <el-row :gutter="12" class="ag-cost-stats">
                <el-col :xs="12" :sm="8" :md="4">
                    <el-card shadow="never" class="ag-cost-stat">
                        <div class="ag-cost-stat__label">{{ t('plugin.ai.cost.actualCost') }}</div>
                        <div class="ag-cost-stat__value is-primary">¥{{ activeSummary.total_cost.toFixed(2) }}</div>
                        <div class="ag-cost-stat__sub">
                            {{ t('plugin.ai.stats.costTotalSub', { list: activeSummary.list_cost.toFixed(2), calls: activeSummary.total_calls }) }}
                        </div>
                    </el-card>
                </el-col>
                <el-col :xs="12" :sm="8" :md="4">
                    <el-card shadow="never" class="ag-cost-stat">
                        <div class="ag-cost-stat__label">{{ t('plugin.ai.cost.totalSaved') }}</div>
                        <div class="ag-cost-stat__value is-ok">¥{{ activeSummary.total_saved.toFixed(2) }}</div>
                        <div class="ag-cost-stat__sub">
                            {{ t('plugin.ai.cost.savedRate') }} {{ (activeSummary.saved_rate * 100).toFixed(1) }}%
                        </div>
                    </el-card>
                </el-col>
                <el-col :xs="12" :sm="8" :md="4">
                    <el-card shadow="never" class="ag-cost-stat">
                        <div class="ag-cost-stat__label">{{ t('plugin.ai.stats.cacheHit') }}</div>
                        <div class="ag-cost-stat__value is-ok">{{ (activeSummary.cache_hit_rate * 100).toFixed(1) }}%</div>
                        <div class="ag-cost-stat__sub">
                            {{ t('plugin.ai.stats.cacheHitSub', { saved: activeSummary.cache_saved.toFixed(2) }) }}
                        </div>
                    </el-card>
                </el-col>
                <el-col :xs="12" :sm="8" :md="4">
                    <el-card shadow="never" class="ag-cost-stat">
                        <div class="ag-cost-stat__label">{{ t('plugin.ai.stats.offpeak') }}</div>
                        <div class="ag-cost-stat__value">{{ (activeSummary.offpeak_rate * 100).toFixed(1) }}%</div>
                        <div class="ag-cost-stat__sub">
                            {{
                                t('plugin.ai.stats.offpeakSub', {
                                    saved: activeSummary.offpeak_saved.toFixed(2),
                                    rate: (activeSummary.saved_rate * 100).toFixed(1),
                                })
                            }}
                        </div>
                    </el-card>
                </el-col>
                <el-col :xs="12" :sm="8" :md="4">
                    <el-card shadow="never" class="ag-cost-stat">
                        <div class="ag-cost-stat__label">{{ t('plugin.ai.cost.callsCard') }}</div>
                        <div class="ag-cost-stat__value">{{ formatTokenCount(activeSummary.total_calls) }}</div>
                        <div class="ag-cost-stat__sub">
                            {{
                                t('plugin.ai.cost.callsCardSub', {
                                    success: activeSummary.success_calls,
                                    failed: activeSummary.failed_calls,
                                    rate: activeSummary.total_calls > 0 ? ((activeSummary.success_calls / activeSummary.total_calls) * 100).toFixed(1) : '100.0',
                                })
                            }}
                        </div>
                    </el-card>
                </el-col>
                <el-col :xs="12" :sm="8" :md="4">
                    <el-card shadow="never" class="ag-cost-stat">
                        <div class="ag-cost-stat__label">{{ t('plugin.ai.cost.tokensCard') }}</div>
                        <div class="ag-cost-stat__value">{{ formatTokenCount(activeSummary.total_tokens) }}</div>
                        <div class="ag-cost-stat__sub">
                            {{
                                t('plugin.ai.cost.tokensCardSub', {
                                    input: formatTokenCount(activeSummary.input_tokens),
                                    output: formatTokenCount(activeSummary.output_tokens),
                                })
                            }}
                        </div>
                    </el-card>
                </el-col>
            </el-row>

            <!-- 恒等分解式横幅 -->
            <el-card class="ag-cost-equation" shadow="never">
                <div class="ag-cost-equation__header">
                    <span class="ag-cost-equation__title">{{ t('plugin.ai.cost.equationTitle') }}</span>
                    <span class="ag-cost-equation__meta">
                        {{
                            t('plugin.ai.cost.tokensBreakdown', {
                                input: formatTokenCount(activeSummary.input_tokens),
                                hit: formatTokenCount(activeSummary.cache_hit_tokens),
                                hitRate: (activeSummary.cache_hit_rate * 100).toFixed(1),
                                output: formatTokenCount(activeSummary.output_tokens),
                                latency: activeSummary.avg_latency_ms,
                            })
                        }}
                    </span>
                </div>
                <div class="ag-cost-equation__grid">
                    <div class="ag-cost-equation__box">
                        <div class="ag-cost-equation__label">{{ t('plugin.ai.cost.listCost') }}</div>
                        <div class="ag-cost-equation__val">¥{{ activeSummary.list_cost.toFixed(4) }}</div>
                    </div>
                    <div class="ag-cost-equation__op">=</div>
                    <div class="ag-cost-equation__box is-primary">
                        <div class="ag-cost-equation__label">{{ t('plugin.ai.cost.actualCost') }}</div>
                        <div class="ag-cost-equation__val">¥{{ activeSummary.total_cost.toFixed(4) }}</div>
                    </div>
                    <div class="ag-cost-equation__op">+</div>
                    <div class="ag-cost-equation__box is-ok">
                        <div class="ag-cost-equation__label">{{ t('plugin.ai.cost.cacheSaved') }}</div>
                        <div class="ag-cost-equation__val">¥{{ activeSummary.cache_saved.toFixed(4) }}</div>
                    </div>
                    <div class="ag-cost-equation__op">+</div>
                    <div class="ag-cost-equation__box is-ok">
                        <div class="ag-cost-equation__label">{{ t('plugin.ai.cost.offpeakSaved') }}</div>
                        <div class="ag-cost-equation__val">¥{{ activeSummary.offpeak_saved.toFixed(4) }}</div>
                    </div>
                </div>
            </el-card>

            <!-- 五维账单页签 -->
            <el-card class="ag-cost-tabs-card" shadow="never">
                <el-tabs v-model="activeTab" data-testid="plugin-ai-cost-tabs">
                    <!-- 1. 按厂商（Vendor） -->
                    <el-tab-pane :label="`${t('plugin.ai.cost.tabByVendor')} (${activeByVendor.length})`" name="vendor">
                        <el-table :data="activeByVendor" stripe size="small" :empty-text="t('plugin.ai.emptyFace')">
                            <el-table-column prop="vendor_title" :label="t('plugin.ai.cost.colVendor')" min-width="150" />
                            <el-table-column prop="driver" :label="t('plugin.ai.cost.colDriver')" width="120" />
                            <el-table-column :label="t('plugin.ai.cost.colAssets')" width="130">
                                <template #default="{ row }">{{ row.channel_count }} / {{ row.model_count }}</template>
                            </el-table-column>
                            <el-table-column prop="calls" :label="t('plugin.ai.cost.colCalls')" width="100" />
                            <el-table-column :label="t('plugin.ai.cost.colSuccessRate')" width="95">
                                <template #default="{ row }">{{ (row.success_rate * 100).toFixed(1) }}%</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colInputTokens')" width="115">
                                <template #default="{ row }">{{ formatTokenCount(row.input_tokens) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colCacheHit')" width="150">
                                <template #default="{ row }">
                                    {{ formatTokenCount(row.cache_hit_tokens) }} ({{ (row.cache_hit_rate * 100).toFixed(1) }}%)
                                </template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colOutputTokens')" width="110">
                                <template #default="{ row }">{{ formatTokenCount(row.output_tokens) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colOffpeak')" width="135">
                                <template #default="{ row }">{{ row.offpeak_calls }} ({{ (row.offpeak_rate * 100).toFixed(1) }}%)</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colActualCost')" width="120">
                                <template #default="{ row }"><span class="ag-cost-num">¥{{ row.cost.toFixed(4) }}</span></template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colListCost')" width="115">
                                <template #default="{ row }">¥{{ row.list_cost.toFixed(4) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colSaved')" min-width="155">
                                <template #default="{ row }">
                                    <span class="ag-saved-num">¥{{ row.cache_saved.toFixed(4) }} / ¥{{ row.offpeak_saved.toFixed(4) }}</span>
                                </template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colShare')" width="95">
                                <template #default="{ row }">{{ (row.cost_share * 100).toFixed(1) }}%</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.findings.action')" width="130" fixed="right">
                                <template #default="{ row }">
                                    <div class="ag-row-links">
                                        <el-link type="primary" :underline="false" @click="drillBy('vendor', row.vendor_title)">
                                            {{ t('plugin.ai.drillFilter') }}
                                        </el-link>
                                        <el-link type="primary" :underline="false" @click="goPage('/admin/plugin/ai/supplier', { quick_search: row.vendor_title })">
                                            {{ t('plugin.ai.manage') }}
                                        </el-link>
                                    </div>
                                </template>
                            </el-table-column>
                        </el-table>
                    </el-tab-pane>

                    <!-- 2. 按渠道（Channel） -->
                    <el-tab-pane :label="`${t('plugin.ai.cost.tabByChannel')} (${activeByChannel.length})`" name="channel">
                        <el-table :data="activeByChannel" stripe size="small" :empty-text="t('plugin.ai.emptyFace')">
                            <el-table-column prop="channel_name" :label="t('plugin.ai.cost.colChannel')" min-width="150" />
                            <el-table-column prop="vendor_title" :label="t('plugin.ai.cost.colVendor')" width="130" />
                            <el-table-column prop="calls" :label="t('plugin.ai.cost.colCalls')" width="95" />
                            <el-table-column :label="t('plugin.ai.cost.colSuccessRate')" width="95">
                                <template #default="{ row }">{{ (row.success_rate * 100).toFixed(1) }}%</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colLatency')" width="95">
                                <template #default="{ row }">{{ row.avg_latency_ms }}ms</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colInputTokens')" width="110">
                                <template #default="{ row }">{{ formatTokenCount(row.input_tokens) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colCacheHit')" width="145">
                                <template #default="{ row }">
                                    {{ formatTokenCount(row.cache_hit_tokens) }} ({{ (row.cache_hit_rate * 100).toFixed(1) }}%)
                                </template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colOutputTokens')" width="105">
                                <template #default="{ row }">{{ formatTokenCount(row.output_tokens) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colOffpeak')" width="130">
                                <template #default="{ row }">{{ row.offpeak_calls }} ({{ (row.offpeak_rate * 100).toFixed(1) }}%)</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colActualCost')" width="120">
                                <template #default="{ row }"><span class="ag-cost-num">¥{{ row.cost.toFixed(4) }}</span></template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colSaved')" min-width="150">
                                <template #default="{ row }">
                                    <span class="ag-saved-num">¥{{ row.cache_saved.toFixed(4) }} / ¥{{ row.offpeak_saved.toFixed(4) }}</span>
                                </template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colShare')" width="90">
                                <template #default="{ row }">{{ (row.cost_share * 100).toFixed(1) }}%</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.findings.action')" width="165" fixed="right">
                                <template #default="{ row }">
                                    <div class="ag-row-links">
                                        <el-link type="primary" :underline="false" @click="drillBy('channel', row.channel_name)">
                                            {{ t('plugin.ai.drillFilter') }}
                                        </el-link>
                                        <el-link type="primary" :underline="false" @click="goPage('/admin/plugin/ai/channel', { quick_search: row.channel_name })">
                                            {{ t('plugin.ai.manage') }}
                                        </el-link>
                                        <el-link type="primary" :underline="false" @click="goPage('/admin/plugin/ai/usage-log', { quick_search: row.channel_name })">
                                            {{ t('plugin.ai.viewLogs') }}
                                        </el-link>
                                    </div>
                                </template>
                            </el-table-column>
                        </el-table>
                    </el-tab-pane>

                    <!-- 3. 按模型（Model） -->
                    <el-tab-pane :label="`${t('plugin.ai.cost.tabByModel')} (${activeByModel.length})`" name="model">
                        <el-table :data="activeByModel" stripe size="small" :empty-text="t('plugin.ai.emptyFace')">
                            <el-table-column prop="model" :label="t('plugin.ai.cost.colModel')" min-width="180" />
                            <el-table-column prop="vendor_title" :label="t('plugin.ai.cost.colVendor')" width="125" />
                            <el-table-column :label="t('plugin.ai.col.type')" width="90">
                                <template #default="{ row }">
                                    <el-tag size="small" effect="plain">{{ modelTypeLabel(row.model_type) }}</el-tag>
                                </template>
                            </el-table-column>
                            <el-table-column prop="calls" :label="t('plugin.ai.cost.colCalls')" width="95" />
                            <el-table-column :label="t('plugin.ai.cost.colInputTokens')" width="110">
                                <template #default="{ row }">{{ formatTokenCount(row.input_tokens) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colCacheHit')" width="145">
                                <template #default="{ row }">
                                    {{ formatTokenCount(row.cache_hit_tokens) }} ({{ (row.cache_hit_rate * 100).toFixed(1) }}%)
                                </template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colOutputTokens')" width="105">
                                <template #default="{ row }">{{ formatTokenCount(row.output_tokens) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colOffpeak')" width="130">
                                <template #default="{ row }">{{ row.offpeak_calls }} ({{ (row.offpeak_rate * 100).toFixed(1) }}%)</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colActualCost')" width="120">
                                <template #default="{ row }"><span class="ag-cost-num">¥{{ row.cost.toFixed(4) }}</span></template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colListCost')" width="115">
                                <template #default="{ row }">¥{{ row.list_cost.toFixed(4) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colSaved')" min-width="150">
                                <template #default="{ row }">
                                    <span class="ag-saved-num">¥{{ row.cache_saved.toFixed(4) }} / ¥{{ row.offpeak_saved.toFixed(4) }}</span>
                                </template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colShare')" width="90">
                                <template #default="{ row }">{{ (row.cost_share * 100).toFixed(1) }}%</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.findings.action')" width="165" fixed="right">
                                <template #default="{ row }">
                                    <div class="ag-row-links">
                                        <el-link type="primary" :underline="false" @click="drillBy('model', row.model)">
                                            {{ t('plugin.ai.drillFilter') }}
                                        </el-link>
                                        <el-link type="primary" :underline="false" @click="goPage('/admin/plugin/ai/model', { quick_search: row.model })">
                                            {{ t('plugin.ai.manage') }}
                                        </el-link>
                                        <el-link type="primary" :underline="false" @click="goPage('/admin/plugin/ai/usage-log', { quick_search: row.model })">
                                            {{ t('plugin.ai.viewLogs') }}
                                        </el-link>
                                    </div>
                                </template>
                            </el-table-column>
                        </el-table>
                    </el-tab-pane>

                    <!-- 4. 按智能体（Agent） -->
                    <el-tab-pane :label="`${t('plugin.ai.cost.tabByAgent')} (${activeByAgent.length})`" name="agent">
                        <el-table :data="activeByAgent" stripe size="small" :empty-text="t('plugin.ai.emptyFace')">
                            <el-table-column :label="t('plugin.ai.cost.colAgent')" min-width="180">
                                <template #default="{ row }">
                                    <div>{{ row.agent_name }}</div>
                                    <div v-if="row.agent_title" class="ag-sub-text">{{ row.agent_title }}</div>
                                </template>
                            </el-table-column>
                            <el-table-column prop="model" :label="t('plugin.ai.cost.colModel')" min-width="145" />
                            <el-table-column prop="vendor_title" :label="t('plugin.ai.cost.colVendor')" width="120" />
                            <el-table-column prop="calls" :label="t('plugin.ai.cost.colCalls')" width="95" />
                            <el-table-column :label="t('plugin.ai.cost.colInputTokens')" width="110">
                                <template #default="{ row }">{{ formatTokenCount(row.input_tokens) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colCacheHit')" width="145">
                                <template #default="{ row }">
                                    {{ formatTokenCount(row.cache_hit_tokens) }} ({{ (row.cache_hit_rate * 100).toFixed(1) }}%)
                                </template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colOutputTokens')" width="105">
                                <template #default="{ row }">{{ formatTokenCount(row.output_tokens) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colActualCost')" width="115">
                                <template #default="{ row }"><span class="ag-cost-num">¥{{ row.cost.toFixed(4) }}</span></template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colSaved')" width="120">
                                <template #default="{ row }"><span class="ag-saved-num">¥{{ row.total_saved.toFixed(4) }}</span></template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colCostPer1k')" width="115">
                                <template #default="{ row }">¥{{ row.cost_per_1k_calls.toFixed(4) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colShare')" width="90">
                                <template #default="{ row }">{{ (row.cost_share * 100).toFixed(1) }}%</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.findings.action')" width="165" fixed="right">
                                <template #default="{ row }">
                                    <div class="ag-row-links">
                                        <el-link type="primary" :underline="false" @click="drillBy('agent', row.agent_name)">
                                            {{ t('plugin.ai.drillFilter') }}
                                        </el-link>
                                        <el-link type="primary" :underline="false" @click="goPage('/admin/plugin/rolling-memory/eval', { quick_search: row.agent_name })">
                                            {{ t('plugin.ai.manage') }}
                                        </el-link>
                                        <el-link type="primary" :underline="false" @click="goPage('/admin/plugin/ai/usage-log', { quick_search: row.agent_name })">
                                            {{ t('plugin.ai.viewLogs') }}
                                        </el-link>
                                    </div>
                                </template>
                            </el-table-column>
                        </el-table>
                    </el-tab-pane>

                    <!-- 5. 按调用来源（Source） -->
                    <el-tab-pane :label="`${t('plugin.ai.cost.tabBySource')} (${activeBySource.length})`" name="source">
                        <el-table :data="activeBySource" stripe size="small" :empty-text="t('plugin.ai.emptyFace')">
                            <el-table-column prop="source" :label="t('plugin.ai.cost.colSource')" min-width="150" />
                            <el-table-column prop="calls" :label="t('plugin.ai.cost.colCalls')" width="105" />
                            <el-table-column :label="t('plugin.ai.cost.colSuccessRate')" width="100">
                                <template #default="{ row }">{{ (row.success_rate * 100).toFixed(1) }}%</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colCacheHit')" width="150">
                                <template #default="{ row }">
                                    {{ formatTokenCount(row.cache_hit_tokens) }} ({{ (row.cache_hit_rate * 100).toFixed(1) }}%)
                                </template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colOffpeak')" width="140">
                                <template #default="{ row }">{{ row.offpeak_calls }} ({{ (row.offpeak_rate * 100).toFixed(1) }}%)</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colTotalTokens')" width="130">
                                <template #default="{ row }">{{ formatTokenCount(row.total_tokens) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colActualCost')" width="125">
                                <template #default="{ row }"><span class="ag-cost-num">¥{{ row.cost.toFixed(4) }}</span></template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colSaved')" min-width="150">
                                <template #default="{ row }">
                                    <span class="ag-saved-num">¥{{ row.cache_saved.toFixed(4) }} / ¥{{ row.offpeak_saved.toFixed(4) }}</span>
                                </template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colShare')" width="100">
                                <template #default="{ row }">{{ (row.cost_share * 100).toFixed(1) }}%</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.findings.action')" width="130" fixed="right">
                                <template #default="{ row }">
                                    <div class="ag-row-links">
                                        <el-link type="primary" :underline="false" @click="drillBy('source', row.source)">
                                            {{ t('plugin.ai.drillFilter') }}
                                        </el-link>
                                        <el-link type="primary" :underline="false" @click="goPage('/admin/plugin/ai/usage-log', { quick_search: row.source })">
                                            {{ t('plugin.ai.viewLogs') }}
                                        </el-link>
                                    </div>
                                </template>
                            </el-table-column>
                        </el-table>
                    </el-tab-pane>

                    <!-- 6. 五维交叉明细桶 -->
                    <el-tab-pane :label="`${t('plugin.ai.cost.tabCrossBuckets')} (${filteredBuckets.length})`" name="buckets">
                        <el-table :data="filteredBuckets" stripe size="small" :empty-text="t('plugin.ai.emptyFace')">
                            <el-table-column prop="vendor_title" :label="t('plugin.ai.cost.colVendor')" width="120" />
                            <el-table-column prop="channel_name" :label="t('plugin.ai.cost.colChannel')" width="130" />
                            <el-table-column prop="model" :label="t('plugin.ai.cost.colModel')" min-width="150" />
                            <el-table-column prop="agent_name" :label="t('plugin.ai.cost.colAgent')" min-width="150" />
                            <el-table-column prop="source" :label="t('plugin.ai.cost.colSource')" width="105" />
                            <el-table-column prop="calls" :label="t('plugin.ai.cost.colCalls')" width="85" />
                            <el-table-column :label="t('plugin.ai.cost.colTotalTokens')" width="115">
                                <template #default="{ row }">{{ formatTokenCount(row.total_tokens) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colActualCost')" width="115">
                                <template #default="{ row }"><span class="ag-cost-num">¥{{ row.cost.toFixed(4) }}</span></template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colListCost')" width="110">
                                <template #default="{ row }">¥{{ row.list_cost.toFixed(4) }}</template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.cost.colSaved')" min-width="140">
                                <template #default="{ row }">
                                    <span class="ag-saved-num">¥{{ row.cache_saved.toFixed(4) }} / ¥{{ row.offpeak_saved.toFixed(4) }}</span>
                                </template>
                            </el-table-column>
                            <el-table-column :label="t('plugin.ai.findings.action')" width="90" fixed="right">
                                <template #default="{ row }">
                                    <div class="ag-row-links">
                                        <el-link type="primary" :underline="false" @click="goPage('/admin/plugin/ai/usage-log', { quick_search: row.agent_name })">
                                            {{ t('plugin.ai.viewLogs') }}
                                        </el-link>
                                    </div>
                                </template>
                            </el-table-column>
                        </el-table>
                    </el-tab-pane>
                </el-tabs>
            </el-card>
        </template>
    </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
    formatTokenCount,
    getAssetReport,
    recomputeByAgent,
    recomputeByChannel,
    recomputeByModel,
    recomputeBySource,
    recomputeByVendor,
    recomputeCostSummary,
    type AiAgentCostItem,
    type AiAssetReport,
    type AiChannelCostRow,
    type AiCostCrossBucket,
    type AiCostSummary,
    type AiModelCostRow,
    type AiSourceCostRow,
    type AiVendorCostRow,
} from './api'

defineOptions({
    name: 'plugin/ai/cost',
})

type CostTab = 'vendor' | 'channel' | 'model' | 'agent' | 'source' | 'buckets'

const { t } = useI18n()
const router = useRouter()
const route = useRoute()

const loading = ref(false)
const loadError = ref(false)
const report = ref<AiAssetReport | null>(null)
const costHours = ref<number>(24)
const activeTab = ref<CostTab>('vendor')

const filterVendor = ref<string>('')
const filterChannel = ref<string>('')
const filterModel = ref<string>('')
const filterAgent = ref<string>('')
const filterSource = ref<string>('')
const searchKeyword = ref<string>('')

const hasCrossFilter = computed<boolean>(() =>
    Boolean(filterVendor.value || filterChannel.value || filterModel.value || filterAgent.value || filterSource.value || searchKeyword.value.trim())
)

const vendorOptions = computed<string[]>(() => (report.value ? report.value.cost.by_vendor.map((v) => v.vendor_title).filter(Boolean) : []))
const channelOptions = computed<string[]>(() => (report.value ? report.value.cost.by_channel.map((c) => c.channel_name).filter(Boolean) : []))
const modelOptions = computed<string[]>(() => (report.value ? report.value.cost.by_model.map((m) => m.model).filter(Boolean) : []))
const agentOptions = computed<string[]>(() => {
    if (!report.value) return []
    const set = new Set<string>()
    for (const b of report.value.cost.buckets) if (b.agent_name) set.add(b.agent_name)
    return Array.from(set).sort()
})
const sourceOptions = computed<string[]>(() => (report.value ? report.value.cost.by_source.map((s) => s.source).filter(Boolean) : []))

const filteredBuckets = computed<AiCostCrossBucket[]>(() => {
    if (!report.value) return []
    const kw = searchKeyword.value.trim().toLowerCase()
    return report.value.cost.buckets.filter((b) => {
        if (filterVendor.value && b.vendor_title !== filterVendor.value) return false
        if (filterChannel.value && b.channel_name !== filterChannel.value) return false
        if (filterModel.value && b.model !== filterModel.value) return false
        if (filterAgent.value && b.agent_name !== filterAgent.value) return false
        if (filterSource.value && b.source !== filterSource.value) return false
        if (kw && !`${b.vendor_title} ${b.channel_name} ${b.model} ${b.agent_name} ${b.agent_title} ${b.source}`.toLowerCase().includes(kw)) return false
        return true
    })
})

const activeSummary = computed<AiCostSummary>(() => {
    if (!report.value) return recomputeCostSummary([], costHours.value)
    return hasCrossFilter.value ? recomputeCostSummary(filteredBuckets.value, costHours.value) : report.value.cost.summary
})

const activeByVendor = computed<AiVendorCostRow[]>(() => {
    if (!report.value) return []
    return hasCrossFilter.value
        ? recomputeByVendor(filteredBuckets.value, report.value.cost.by_vendor, activeSummary.value.total_cost)
        : report.value.cost.by_vendor
})

const activeByChannel = computed<AiChannelCostRow[]>(() => {
    if (!report.value) return []
    return hasCrossFilter.value ? recomputeByChannel(filteredBuckets.value, activeSummary.value.total_cost) : report.value.cost.by_channel
})

const activeByModel = computed<AiModelCostRow[]>(() => {
    if (!report.value) return []
    return hasCrossFilter.value ? recomputeByModel(filteredBuckets.value, activeSummary.value.total_cost) : report.value.cost.by_model
})

const activeByAgent = computed<AiAgentCostItem[]>(() => {
    if (!report.value) return []
    return hasCrossFilter.value ? recomputeByAgent(filteredBuckets.value, activeSummary.value.total_cost) : report.value.cost.by_agent
})

const activeBySource = computed<AiSourceCostRow[]>(() => {
    if (!report.value) return []
    return hasCrossFilter.value ? recomputeBySource(filteredBuckets.value, activeSummary.value.total_cost) : report.value.cost.by_source
})

function drillBy(dim: 'vendor' | 'channel' | 'model' | 'agent' | 'source', value: string) {
    if (dim === 'vendor') filterVendor.value = filterVendor.value === value ? '' : value
    if (dim === 'channel') filterChannel.value = filterChannel.value === value ? '' : value
    if (dim === 'model') filterModel.value = filterModel.value === value ? '' : value
    if (dim === 'agent') filterAgent.value = filterAgent.value === value ? '' : value
    if (dim === 'source') filterSource.value = filterSource.value === value ? '' : value
}

function clearAllFilters() {
    filterVendor.value = ''
    filterChannel.value = ''
    filterModel.value = ''
    filterAgent.value = ''
    filterSource.value = ''
    searchKeyword.value = ''
}

function goPage(path: string, query?: Record<string, string>) {
    router.push({ path, query })
}

function modelTypeLabel(tp: string): string {
    if (tp === 'chat' || tp === 'embedding' || tp === 'rerank') return t(`plugin.ai.model.typeValue.${tp}`)
    return tp || t('plugin.ai.model.typeValue.unknown')
}

async function load() {
    loading.value = true
    loadError.value = false
    try {
        report.value = await getAssetReport(costHours.value)
    } catch {
        report.value = null
        loadError.value = true
    } finally {
        loading.value = false
    }
}

onMounted(() => {
    const q = route.query
    if (q.hours && [24, 168, 720].includes(Number(q.hours))) costHours.value = Number(q.hours)
    if (typeof q.tab === 'string' && ['vendor', 'channel', 'model', 'agent', 'source', 'buckets'].includes(q.tab)) {
        activeTab.value = q.tab as CostTab
    }
    if (typeof q.vendor === 'string') filterVendor.value = q.vendor
    if (typeof q.channel === 'string') filterChannel.value = q.channel
    if (typeof q.model === 'string') filterModel.value = q.model
    if (typeof q.agent === 'string') filterAgent.value = q.agent
    if (typeof q.source === 'string') filterSource.value = q.source
    if (typeof q.quick_search === 'string') searchKeyword.value = q.quick_search
    load()
})
</script>

<style scoped lang="scss">
.ag-plugin-ai-cost {
    display: flex;
    flex-direction: column;
    gap: 12px;
}
.ag-cost-header__row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
}
.ag-cost-header__title {
    font-size: 16px;
    font-weight: 600;
}
.ag-cost-header__intro {
    margin-top: 4px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
}
.ag-cost-header__actions {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
}
.ag-cost-filters {
    margin-top: 12px;
    padding-top: 12px;
    border-top: 1px solid var(--el-border-color-lighter);
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}
.ag-cost-filters__item {
    width: 160px;
}
.ag-cost-filters__search {
    width: 220px;
}
.ag-cost-stats {
    row-gap: 12px;
}
.ag-cost-stat__label {
    font-size: 12px;
    color: var(--el-text-color-secondary);
}
.ag-cost-stat__value {
    margin-top: 6px;
    font-size: 20px;
    font-weight: 600;
    line-height: 1.2;
    &.is-primary {
        color: var(--el-color-primary);
    }
    &.is-ok {
        color: var(--el-color-success);
    }
}
.ag-cost-stat__sub {
    margin-top: 4px;
    font-size: 11px;
    color: var(--el-text-color-secondary);
}
.ag-cost-equation__header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;
    margin-bottom: 12px;
}
.ag-cost-equation__title {
    font-weight: 600;
    font-size: 14px;
}
.ag-cost-equation__meta {
    font-size: 12px;
    color: var(--el-text-color-secondary);
}
.ag-cost-equation__grid {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
}
.ag-cost-equation__box {
    flex: 1;
    min-width: 140px;
    padding: 10px 14px;
    border-radius: 6px;
    background: var(--el-fill-color-light);
    &.is-primary {
        background: var(--el-color-primary-light-9);
    }
    &.is-ok {
        background: var(--el-color-success-light-9);
    }
}
.ag-cost-equation__label {
    font-size: 12px;
    color: var(--el-text-color-secondary);
}
.ag-cost-equation__val {
    margin-top: 4px;
    font-size: 18px;
    font-weight: 600;
    font-family: var(--el-font-family-monospace, monospace);
}
.ag-cost-equation__op {
    font-size: 18px;
    font-weight: 700;
    color: var(--el-text-color-secondary);
}
.ag-cost-num {
    font-weight: 600;
    font-family: var(--el-font-family-monospace, monospace);
}
.ag-saved-num {
    color: var(--el-color-success);
    font-family: var(--el-font-family-monospace, monospace);
}
.ag-sub-text {
    font-size: 11px;
    color: var(--el-text-color-secondary);
}
.ag-row-links {
    display: inline-flex;
    align-items: center;
    gap: 10px;
}
</style>
