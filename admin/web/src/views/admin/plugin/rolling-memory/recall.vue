<!-- plugin: rolling-memory v0.0.58 -->
<!--
    Rolling-Memory 记忆系统 · 「检索调试」页面（web/src/views/admin/plugin/rolling-memory/recall.vue）。
    职责：
      1) 与 Agent 同一条 recall 链路的交互式调试台：自然语言线索 q + 可选工作区锚、Scope、Reader(owner/deliverable)、
         模式(full/probe)、Bands/Depth 预算与原文回退开关；默认只读（不写 L3），显式勾选才按 Agent 同等路径写入；
      2) 「最近 Agent 检索」：取自 L3 Noetic 的线索，以工作区锚及当前参数重新查询；
      3) 严格对齐宿主 BuildAdmin 表格样式（.table-header + .ag-data-table + 列显隐控制 + 页内二次快搜 + 分页 + .ag-operate-dialog 详情弹窗）。
-->
<template>
    <div class="default-main plugin-rm-recall">
        <!-- 顶部提示条（参照 plugins/ai/web/views/channel/index.vue 标准） -->
        <el-alert
            class="rm-alert"
            type="info"
            :closable="false"
            show-icon
            :title="t('plugin.rolling-memory.recallPage.title')"
            :description="t('plugin.rolling-memory.recallPage.subtitle')"
        />

        <!-- 4 维左边框指示色统计条（BuildAdmin 标准 .stat-item）：只展示本次检索真实返回的读数 -->
        <div v-if="hasResult" class="rm-stats-bar">
            <div class="stat-item primary">
                <span class="stat-label">{{ t('plugin.rolling-memory.recallPage.kpiBandsHits') }}</span>
                <span class="stat-value">{{ result.total_bands ?? 0 }} / {{ result.total_hits ?? 0 }}</span>
            </div>
            <div class="stat-item success">
                <span class="stat-label">{{ t('plugin.rolling-memory.recallPage.kpiCoverage') }}</span>
                <span class="stat-value">{{ formatPercent(result.meta?.cue_coverage) }}</span>
            </div>
            <div class="stat-item warning">
                <span class="stat-label">{{ t('plugin.rolling-memory.recallPage.kpiWorkspace') }}</span>
                <span class="stat-value">{{ result.workspace || t('plugin.rolling-memory.recallPage.workspaceAll') }}</span>
            </div>
            <div class="stat-item info">
                <span class="stat-label">{{ t('plugin.rolling-memory.recallPage.kpiLatency') }}</span>
                <span class="stat-value">
                    {{ result.elapsed_ms ?? 0 }} ms ·
                    {{ result.crystallized ? t('plugin.rolling-memory.recallPage.modeWrite') : t('plugin.rolling-memory.recallPage.modeReadOnly') }}
                </span>
            </div>
        </div>

        <!-- 检索条件与参数面板（对齐 BuildAdmin .table-com-search 容器规范） -->
        <div class="rm-query-panel">
            <div class="rm-param-lbl">
                {{
                    isEn
                        ? 'Workspace is a recall anchor, not a filter. Local vault data is not isolated by scope.'
                        : '工作区是召回锚，不是过滤条件。本地金库数据不按 scope 隔离。'
                }}
            </div>
            <el-row :gutter="10">
                <el-col :xs="24" :sm="16" :md="15">
                    <el-input
                        v-model="form.q"
                        size="default"
                        clearable
                        data-testid="input-recall-q"
                        :placeholder="t('plugin.rolling-memory.recallPage.queryPlaceholder')"
                        @keyup.enter="runRecall"
                    >
                        <template #prepend>{{ t('plugin.rolling-memory.recallPage.cueLbl') }}</template>
                    </el-input>
                </el-col>
                <el-col :xs="24" :sm="8" :md="6">
                    <el-select
                        v-model="form.workspace"
                        size="default"
                        clearable
                        filterable
                        data-testid="select-recall-workspace"
                        :placeholder="t('plugin.rolling-memory.recallPage.workspaceAll')"
                        @change="runRecall"
                    >
                        <el-option v-for="ws in availableWorkspaces" :key="ws.value" :label="`${ws.value} (${ws.count})`" :value="ws.value" />
                    </el-select>
                </el-col>
                <el-col :xs="24" :sm="24" :md="3">
                    <el-button v-blur type="primary" :loading="loading" class="rm-full-btn" data-testid="btn-run-recall" @click="runRecall">
                        <Icon name="lucide-search" :size="14" color="#ffffff" class="btn-icon" />
                        {{ t('plugin.rolling-memory.recallPage.runBtn') }}
                        <template v-if="loading"> ({{ recallBtnPct }}%)</template>
                    </el-button>
                </el-col>
            </el-row>

            <el-row :gutter="10" class="rm-query-row2">
                <el-col :xs="12" :sm="6" :md="4">
                    <div class="rm-param-item">
                        <span class="rm-param-lbl">Scope</span>
                        <el-select v-model="form.scope" size="small" @change="runRecall">
                            <el-option v-for="sc in availableScopes" :key="sc.value" :label="`${sc.value} (${sc.count})`" :value="sc.value" />
                            <el-option v-if="!availableScopes.length" label="mem:self" value="mem:self" />
                        </el-select>
                    </div>
                </el-col>
                <el-col :xs="12" :sm="6" :md="4">
                    <div class="rm-param-item">
                        <span class="rm-param-lbl">Reader</span>
                        <el-select v-model="form.reader" size="small" @change="runRecall">
                            <el-option :label="t('plugin.rolling-memory.recall.readerOwner')" value="owner" />
                            <el-option :label="t('plugin.rolling-memory.recall.readerDeliverable')" value="deliverable" />
                        </el-select>
                    </div>
                </el-col>
                <el-col :xs="12" :sm="6" :md="4">
                    <div class="rm-param-item">
                        <span class="rm-param-lbl">Mode</span>
                        <el-select v-model="form.result_mode" size="small" @change="runRecall">
                            <el-option :label="t('plugin.rolling-memory.recall.modeFull')" value="full" />
                            <el-option :label="t('plugin.rolling-memory.recall.modeProbe')" value="probe" />
                        </el-select>
                    </div>
                </el-col>
                <el-col :xs="12" :sm="6" :md="4">
                    <div class="rm-param-item">
                        <span class="rm-param-lbl">Bands / Depth</span>
                        <div class="rm-inline-pair">
                            <el-input-number v-model="form.bands" size="small" :min="1" :max="30" controls-position="right" @change="runRecall" />
                            <el-input-number v-model="form.depth" size="small" :min="1" :max="4" controls-position="right" @change="runRecall" />
                        </div>
                    </div>
                </el-col>
                <el-col :xs="12" :sm="6" :md="4">
                    <div class="rm-param-item">
                        <span class="rm-param-lbl">{{ t('plugin.rolling-memory.recallPage.minRelLbl') }}</span>
                        <el-select v-model="minConfSelect" size="small" @change="runRecall">
                            <el-option :label="t('plugin.rolling-memory.recallPage.relOptAll')" :value="0" />
                            <el-option :label="t('plugin.rolling-memory.recallPage.relOptAtLeast', { v: '0.95' })" :value="0.95" />
                            <el-option :label="t('plugin.rolling-memory.recallPage.relOptAtLeast', { v: '0.90' })" :value="0.9" />
                            <el-option :label="t('plugin.rolling-memory.recallPage.relOptAtLeast', { v: '0.80' })" :value="0.8" />
                            <el-option :label="t('plugin.rolling-memory.recallPage.relOptAtLeast', { v: '0.60' })" :value="0.6" />
                        </el-select>
                    </div>
                </el-col>
                <el-col :xs="12" :sm="6" :md="4">
                    <div class="rm-param-item">
                        <span class="rm-param-lbl">{{ t('plugin.rolling-memory.recallPage.sortByLbl') }}</span>
                        <el-select v-model="form.sort_by" size="small" @change="runRecall">
                            <el-option :label="t('plugin.rolling-memory.recallPage.sortByDelivery')" value="delivery" />
                            <el-option :label="t('plugin.rolling-memory.recallPage.sortByScore')" value="score" />
                            <el-option :label="t('plugin.rolling-memory.recallPage.sortByRel')" value="confidence" />
                            <el-option :label="t('plugin.rolling-memory.recallPage.sortByTime')" value="time" />
                        </el-select>
                    </div>
                </el-col>
            </el-row>

            <!-- 最近线索按当前参数重新查询，并非历史结果重放 -->
            <div class="rm-presets-row">
                <span class="rm-preset-lbl">
                    {{ t('plugin.rolling-memory.recallPage.recentCuesLbl') }}
                    ({{ isEn ? 'query again with current settings' : '按当前参数重新查询' }}):
                </span>
                <el-tag
                    v-for="cue in recentCues"
                    :key="cue.id"
                    size="small"
                    effect="plain"
                    class="rm-preset-tag"
                    data-testid="recent-cue-tag"
                    :type="cue.status === 'active' ? 'primary' : 'info'"
                    :title="`${cue.time_text} · ${cue.cited} ${t('plugin.rolling-memory.recallPage.citedUnit')}`"
                    @click="replayCue(cue)"
                >
                    <template v-if="cue.workspace">[{{ cue.workspace }}] </template>{{ cue.cue }}
                </el-tag>
                <span v-if="!recentCues.length" class="rm-preset-lbl">
                    {{
                        recentCuesFailed
                            ? isEn
                                ? 'Could not load recent cues'
                                : '最近线索加载失败'
                            : t('plugin.rolling-memory.recallPage.recentCuesEmpty')
                    }}
                </span>
                <div class="rm-presets-right">
                    <span class="rm-param-lbl">{{ t('plugin.rolling-memory.recallPage.includeRawLbl') }}</span>
                    <el-switch v-model="form.include_raw" size="small" @change="runRecall" />
                    <span class="rm-param-lbl">{{ t('plugin.rolling-memory.recallPage.crystallizeLbl') }}</span>
                    <el-switch v-model="form.crystallize" size="small" data-testid="switch-recall-crystallize" />
                </div>
            </div>
        </div>

        <el-alert
            v-if="!queryFailed && hasResult && result.q !== undefined"
            type="info"
            :closable="false"
            show-icon
            :title="isEn ? `Results for cue: ${result.q}` : `结果对应线索：${result.q}`"
        />
        <el-alert
            v-if="queryFailed"
            type="error"
            :closable="false"
            show-icon
            :title="isEn ? 'Query failed. No result is displayed for this request.' : '查询失败，本次未显示结果。'"
        />

        <!-- BuildAdmin 标准表头操作栏 (.table-header) -->
        <div class="table-header ag-scroll-style">
            <el-tooltip :content="t('common.refresh')" placement="top" :enterable="false" :hide-after="0" :show-after="250">
                <el-button v-blur color="#40485b" class="table-header-operate" type="info" :loading="loading" @click="runRecall">
                    <Icon size="14" name="lucide-refresh-cw" color="#ffffff" />
                </el-button>
            </el-tooltip>

            <el-radio-group v-model="activeViewTab" size="small" class="btns-ml-12">
                <el-radio-button value="hits"> {{ t('plugin.rolling-memory.recallPage.tabHitsTable') }} ({{ allHits.length }}) </el-radio-button>
                <el-radio-button value="bands">
                    {{ t('plugin.rolling-memory.recallPage.tabBandsTree') }} ({{ result.bands?.length || 0 }})
                </el-radio-button>
                <el-radio-button value="raw">
                    {{ t('plugin.rolling-memory.recallPage.tabRawJson') }} ({{ result.raw_hits?.length || 0 }})
                </el-radio-button>
            </el-radio-group>

            <el-button v-blur class="table-header-operate btns-ml-12" type="primary" plain @click="goManagePage">
                <Icon size="14" name="lucide-layout-dashboard" />
                <span class="table-header-operate-text">{{ t('plugin.rolling-memory.recallPage.backManageBtn') }}</span>
            </el-button>

            <el-button v-blur class="table-header-operate btns-ml-12" type="warning" plain @click="goMemoryConsole">
                <Icon size="14" name="lucide-cpu" />
                <span class="table-header-operate-text">{{ isEn ? 'Memory Console' : '记忆控制台' }}</span>
            </el-button>

            <!-- 右侧结果集内二次快搜 + 列显隐控制（BuildAdmin 标准） -->
            <div class="table-search">
                <el-input
                    v-model="hitFilterKeyword"
                    class="xs-hidden quick-search"
                    :placeholder="t('plugin.rolling-memory.filters.searchPlaceholder')"
                    clearable
                />
                <div v-if="activeViewTab === 'hits'" class="table-search-button-group">
                    <el-dropdown :max-height="380" :hide-on-click="false">
                        <div class="table-search-button-item">
                            <Icon size="14" name="el-grid" color="var(--el-text-color-primary)" />
                        </div>
                        <template #dropdown>
                            <el-dropdown-menu>
                                <el-dropdown-item v-for="col in columnToggles" :key="col.key">
                                    <el-checkbox v-model="colVisibility[col.key]" size="small" :label="col.label" />
                                </el-dropdown-item>
                            </el-dropdown-menu>
                        </template>
                    </el-dropdown>
                </div>
            </div>
        </div>

        <!-- 视图 1：BuildAdmin 标准数据表格 (.ag-data-table + 分页) -->
        <div v-if="hasResult && activeViewTab === 'hits'">
            <el-table
                v-loading="loading"
                :element-loading-text="recallLoadingText"
                :data="pagedHits"
                :empty-text="hitsEmptyText"
                class="ag-data-table"
                header-cell-class-name="table-header-cell"
                border
                stripe
                @row-dblclick="(row: RecallHitItem) => openDetail(row.id)"
            >
                <el-table-column
                    v-if="colVisibility.band"
                    prop="band_title"
                    :label="t('plugin.rolling-memory.recallPage.colBandChain')"
                    min-width="190"
                    show-overflow-tooltip
                >
                    <template #default="{ row }">
                        <div class="rm-band-cell">
                            <div class="rm-band-title">{{ row.band_title || row.band_subject || row.id }}</div>
                            <div class="rm-chain-sub">
                                <el-tag size="small" type="primary" effect="plain">{{ row.chain_type || 'cell_band' }}</el-tag>
                                <span class="rm-chain-lbl">{{ row.chain_label || row.band_id }}</span>
                            </div>
                        </div>
                    </template>
                </el-table-column>

                <el-table-column v-if="colVisibility.fact" prop="rendered" :label="t('plugin.rolling-memory.recallPage.colFact')" min-width="300">
                    <template #default="{ row }">
                        <MemoryHoverPreview
                            :text="row.rendered || row.o"
                            :title="row.anchor || row.id"
                            :subtitle="row.time || row.locus || ''"
                            :hint="{ layer: row.warrant }"
                        >
                            <div class="rm-fact-box" @click="openDetail(row.id)">
                                <span class="rm-fact-o">{{ row.rendered || row.o }}</span>
                            </div>
                            <div v-if="row.anchor_snippet || row.anchor" class="rm-anchor-preview">
                                <span v-if="row.anchor" class="rm-anchor-badge">{{ row.anchor }}</span>
                                <span v-if="row.anchor_snippet" class="rm-snippet-text">“{{ row.anchor_snippet }}”</span>
                            </div>
                        </MemoryHoverPreview>
                    </template>
                </el-table-column>

                <el-table-column
                    v-if="colVisibility.confidence"
                    prop="confidence"
                    :label="t('plugin.rolling-memory.recallPage.colRelStrength')"
                    width="160"
                    sortable
                >
                    <template #header>
                        <span>
                            {{ t('plugin.rolling-memory.recallPage.colRelStrength') }}
                            <!-- 静态说明改点击式 `?` 弹层 -->
                            <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                                <template #reference>
                                    <button type="button" class="tip-q" :aria-label="t('plugin.rolling-memory.recallPage.colRelStrength')">
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p>{{ t('plugin.rolling-memory.recallPage.relStrengthHint') }}</p>
                                </div>
                            </el-popover>
                        </span>
                    </template>
                    <template #default="{ row }">
                        <div class="rm-metric-cell">
                            <el-tag size="small" type="info" effect="plain">
                                {{ ((row.confidence || 0) * 100).toFixed(0) }}% ({{ Number(row.confidence || 0).toFixed(2) }})
                            </el-tag>
                            <el-progress :percentage="Math.round((row.confidence || 0) * 100)" :stroke-width="5" :show-text="false" />
                        </div>
                    </template>
                </el-table-column>

                <el-table-column v-if="colVisibility.score" prop="score" :label="t('plugin.rolling-memory.recallPage.colScore')" width="140" sortable>
                    <template #default="{ row }">
                        <div class="rm-metric-cell">
                            <el-tag size="small" type="info" effect="plain">
                                {{ t('plugin.rolling-memory.recallPage.colScore') }}: {{ result.result_mode === 'probe' ? '—' : formatScore(row.score) }}
                            </el-tag>
                        </div>
                    </template>
                </el-table-column>

                <el-table-column v-if="colVisibility.coord" prop="warrant" :label="t('plugin.rolling-memory.recallPage.colCoord')" width="225">
                    <template #default="{ row }">
                        <div>
                            <el-tag size="small" type="success">{{ row.status || 'active' }}</el-tag>
                            <el-tag v-if="row.warrant" size="small" type="info" class="rm-ml4">{{ row.warrant }}</el-tag>
                            <el-tag v-if="row.carrier" size="small" type="primary" effect="plain" class="rm-ml4">
                                {{ row.carrier }}
                            </el-tag>
                        </div>
                        <div class="rm-coord-sub">{{ row.time || '-' }} · {{ row.locus || row.band_space || 'mem:self' }}</div>
                    </template>
                </el-table-column>

                <el-table-column :label="t('common.operate')" width="80" align="center" fixed="right">
                    <template #default="{ row }">
                        <el-tooltip :content="t('plugin.rolling-memory.manage.detailBtn')" placement="top" :enterable="false" :hide-after="0" :show-after="250">
                            <el-button v-blur size="small" type="primary" class="ag-table-render-buttons-item" @click="openDetail(row.id)">
                                <Icon size="14" name="lucide-eye" color="#ffffff" />
                            </el-button>
                        </el-tooltip>
                    </template>
                </el-table-column>
            </el-table>

            <div v-if="filteredHits.length > 0" class="ag-table-pagination">
                <el-pagination
                    v-model:page-size="hitPageSize"
                    v-model:current-page="hitCurrentPage"
                    :page-sizes="[10, 20, 50, 100]"
                    background
                    layout="sizes,total, ->, prev, pager, next, jumper"
                    :total="filteredHits.length"
                />
            </div>
        </div>

        <!-- 视图 2：带 × 链层级卡片视图 -->
        <div v-else-if="hasResult && activeViewTab === 'bands'" class="rm-bands-panel">
            <div v-if="!result.bands || result.bands.length === 0" class="rm-empty-tip">
                {{ t('plugin.rolling-memory.recallPage.emptyBands') }}
            </div>
            <div v-for="band in result.bands || []" :key="band.id" class="rm-band-card">
                <div class="rm-band-card-hdr">
                    <div class="rm-band-hdr-left">
                        <span class="rm-band-card-title">{{ band.title || band.subject || band.id }}</span>
                        <el-tag v-if="band.kind" size="small" type="primary" effect="plain">{{ band.kind }}</el-tag>
                        <el-tag v-if="band.time_label" size="small" type="info">{{ band.time_label }}</el-tag>
                        <el-tag v-if="band.space_label" size="small" effect="plain">{{ band.space_label }}</el-tag>
                    </div>
                    <span class="rm-mono">{{ band.id }}</span>
                </div>
                <template v-if="band.chains && band.chains.length">
                    <div v-for="chain in band.chains" :key="chain.key" class="rm-chain-block">
                        <div class="rm-chain-hdr">
                            <el-tag size="small" type="primary">{{ chain.type }}</el-tag>
                            <span class="rm-chain-title">{{ chain.label }}</span>
                            <span class="rm-chain-time">{{ chain.time_label }}</span>
                        </div>
                        <div v-for="node in chain.nodes || []" :key="node.id" class="rm-chain-node">
                            <div class="rm-node-main">
                                <MarkdownPreview :text="node.content || node.rendered" :hint="{ layer: node.warrant || chain.type }" compact />
                            </div>
                            <div class="rm-node-badges">
                                <el-tag v-if="typeof node.relative_strength === 'number'" size="small" type="info" effect="plain">
                                    {{ t('plugin.rolling-memory.recallPage.colRelTag') }} {{ (node.relative_strength * 100).toFixed(0) }}%
                                </el-tag>
                                <el-tag v-if="typeof node.score === 'number'" size="small" type="info" effect="plain">
                                    {{ t('plugin.rolling-memory.recallPage.colScore') }} {{ formatScore(node.score) }}
                                </el-tag>
                                <span class="rm-mono">{{ node.anchor }}</span>
                                <el-button link type="primary" size="small" @click="openDetail(node.id)">
                                    {{ t('plugin.rolling-memory.manage.detailBtn') }}
                                </el-button>
                            </div>
                        </div>
                    </div>
                </template>
                <div v-else-if="band.nodes && band.nodes.length" class="rm-chain-block">
                    <div v-for="node in band.nodes" :key="node.id" class="rm-chain-node">
                        <div class="rm-node-main">
                            <MarkdownPreview :text="node.content || node.rendered" :hint="{ layer: node.warrant || band.kind }" compact />
                        </div>
                        <div class="rm-node-badges">
                            <el-tag v-if="typeof node.relative_strength === 'number'" size="small" type="info" effect="plain">
                                {{ t('plugin.rolling-memory.recallPage.colRelTag') }} {{ (node.relative_strength * 100).toFixed(0) }}%
                            </el-tag>
                            <el-tag v-if="typeof node.score === 'number'" size="small" type="info" effect="plain">
                                {{ t('plugin.rolling-memory.recallPage.colScore') }} {{ formatScore(node.score) }}
                            </el-tag>
                            <span v-if="node.locus" class="rm-mono">{{ node.locus }}</span>
                            <span class="rm-mono">{{ node.anchor }}</span>
                            <el-button link type="primary" size="small" @click="openDetail(node.id)">
                                {{ t('plugin.rolling-memory.manage.detailBtn') }}
                            </el-button>
                        </div>
                    </div>
                </div>
            </div>
        </div>

        <!-- 视图 3：原文回退命中 & 遥测 JSON -->
        <div v-else-if="hasResult" class="rm-bands-panel">
            <el-row :gutter="12">
                <el-col :xs="24" :md="12">
                    <div class="rm-section-title">Raw Hits ({{ result.raw_hits?.length || 0 }})</div>
                    <el-empty
                        v-if="!(result.raw_hits && result.raw_hits.length)"
                        :image-size="60"
                        :description="isEn ? 'No raw fallback hits for this query' : '当前查询无原文兜底命中'"
                    />
                    <div v-for="(rh, idx) in result.raw_hits || []" :key="idx" class="rm-raw-card">
                        <div class="rm-raw-hdr">
                            <span class="rm-mono">{{ rawHitUnitID(rh) || '—' }}</span>
                            <span>layer={{ rh.layer || 'episodic' }} · score={{ Number(rh.score || 0).toFixed(4) }}</span>
                        </div>
                        <MarkdownPreview :text="rh.text" :hint="{ layer: rh.layer }" />
                        <el-button v-if="rawHitUnitID(rh)" link type="primary" size="small" @click="openRawHit(rawHitUnitID(rh))">
                            {{ t('plugin.rolling-memory.manage.detailBtn') }}
                        </el-button>
                    </div>
                </el-col>
                <el-col :xs="24" :md="12">
                    <div class="rm-section-title">Recall Meta & Telemetry JSON</div>
                    <pre class="rm-raw-body">{{ JSON.stringify({ meta: result.meta, gaps: result.gaps, legs: result.legs }, null, 2) }}</pre>
                </el-col>
            </el-row>
        </div>

        <!-- BuildAdmin 标准详情弹窗 (.ag-operate-dialog) -->
        <el-dialog class="ag-operate-dialog" v-model="detailVisible" width="min(820px, 95vw)" destroy-on-close>
            <template #header>
                <div class="title">{{ t('plugin.rolling-memory.manage.drawerTitle') }}</div>
            </template>
            <el-scrollbar v-loading="detailLoading" :element-loading-text="detailLoadingText" class="ag-table-form-scrollbar">
                <div v-if="detailData?.item" class="ag-operate-form">
                    <el-descriptions :column="2" border size="small">
                        <el-descriptions-item :label="t('plugin.rolling-memory.manage.colCell')" :span="2">
                            <strong class="rm-mono">{{ detailData.item.s || '-' }}</strong>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.rolling-memory.manage.colLayer')">
                            <el-tag size="small" type="primary">{{ detailData.item.x }}</el-tag>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.rolling-memory.manage.colObject')" :span="2">
                            {{ detailData.item.o || '-' }}
                        </el-descriptions-item>
                        <el-descriptions-item label="Anchor">
                            <span class="rm-mono">{{ detailData.item.anchor }}</span>
                        </el-descriptions-item>
                        <el-descriptions-item label="Status / Layer">
                            <el-tag size="small" type="success">{{ detailData.item.status }}</el-tag>
                            <el-tag size="small" type="info" class="rm-ml4">{{ detailData.item.warrant }}</el-tag>
                        </el-descriptions-item>
                    </el-descriptions>

                    <div class="rm-section-title">{{ t('plugin.rolling-memory.manage.verbatimAnchorTitle') }}</div>
                    <div class="rm-trace-highlight-box">
                        <span>{{ detailData.trace_before }}</span>
                        <mark class="rm-trace-mark">{{ detailData.trace_anchor }}</mark>
                        <span>{{ detailData.trace_after }}</span>
                    </div>

                    <div class="rm-section-title">{{ t('plugin.rolling-memory.manage.fullTraceTitle') }}</div>
                    <MarkdownPreview :text="detailData.trace_text" :hint="traceHint" />
                </div>
            </el-scrollbar>
        </el-dialog>
    </div>
</template>

<script setup lang="ts">
import { useLoadingProgressText } from '@/utils/loadingProgress'
import request from '@/utils/request'
import { computed, onActivated, onDeactivated, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import MarkdownPreview from './MarkdownPreview.vue'
import MemoryHoverPreview from './MemoryHoverPreview.vue'
import type { MemoryContentHint } from './memoryMarkdown'

defineOptions({
    name: 'plugin/rolling-memory/recall',
})

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const isEn = computed(() =>
    String(locale.value || '')
        .toLowerCase()
        .startsWith('en')
)

interface FacetCount {
    value: string
    count: number
}

interface RecallHitItem {
    id: string
    band_id: string
    band_title: string
    band_subject: string
    band_time_label: string
    band_space: string
    chain_type: string
    chain_label: string
    rendered: string
    confidence: number
    score: number
    status: string
    warrant: string
    time: string
    world: string
    carrier: string
    locus: string
    workspace: string
    incarnation: string
    anchor: string
    anchor_snippet: string
    evidence: string
}

interface RecallQueryResponse {
    scope: string
    q: string
    reader: string
    result_mode: string
    absent: boolean
    elapsed_ms: number
    total_bands: number
    total_chains: number
    total_hits: number
    avg_confidence: number
    max_confidence: number
    min_confidence: number
    avg_score: number
    max_score: number
    anchors_checked: number
    anchor_failed: number
    seeds: number
    candidates: number
    neighbors: number
    legs: Record<string, number>
    gaps: Record<string, number>
    hits: RecallHitItem[]
    Hits?: RecallHitItem[]
    bands: any[]
    raw_hits: any[]
    meta: Record<string, any>
    available_scopes: FacetCount[]
    available_workspaces: FacetCount[]
    workspace: string
    crystallized: boolean
}

interface RecentCueItem {
    id: string
    cue: string
    workspace: string
    status: string
    cited: number
    time_text: string
}

const loading = ref(false)
const { percent: recallBtnPct, text: recallLoadingText } = useLoadingProgressText(loading, () => (isEn.value ? 'Running recall' : '正在执行检索'))
const hasResult = ref(false)
const queryFailed = ref(false)
let recallRequest = 0
const activeViewTab = ref<'hits' | 'bands' | 'raw'>('hits')
const minConfSelect = ref<number>(0)
const availableScopes = ref<FacetCount[]>([])
const availableWorkspaces = ref<FacetCount[]>([])
const recentCues = ref<RecentCueItem[]>([])
const recentCuesFailed = ref(false)
const hitFilterKeyword = ref('')
const hitCurrentPage = ref(1)
const hitPageSize = ref(20)

const colVisibility = reactive<Record<string, boolean>>({
    band: true,
    fact: true,
    confidence: true,
    score: true,
    coord: true,
})

const columnToggles = computed(() => [
    { key: 'band', label: t('plugin.rolling-memory.recallPage.colBandChain') },
    { key: 'fact', label: t('plugin.rolling-memory.recallPage.colFact') },
    { key: 'confidence', label: t('plugin.rolling-memory.recallPage.colRelStrength') },
    { key: 'score', label: t('plugin.rolling-memory.recallPage.colScore') },
    { key: 'coord', label: t('plugin.rolling-memory.recallPage.colCoord') },
])

const form = reactive({
    q: '',
    workspace: '',
    scope: 'mem:self',
    profile: '',
    reader: 'owner',
    result_mode: 'full',
    locus: '',
    carrier: '',
    incarnation: '',
    bands: 10,
    nodes: 60,
    chars: 12000,
    depth: 2,
    include_raw: true,
    raw_limit: 5,
    sort_by: 'delivery',
    // 调试台默认只读：试查不写入 L3；需要复现 Agent 的完整写入路径时再勾选
    crystallize: false,
})

const result = ref<Partial<RecallQueryResponse>>({
    hits: [],
    bands: [],
    raw_hits: [],
    legs: {},
    gaps: {},
})

const allHits = computed<RecallHitItem[]>(() => {
    return (result.value.hits || result.value.Hits || []) as RecallHitItem[]
})

const filteredHits = computed<RecallHitItem[]>(() => {
    const kw = hitFilterKeyword.value.trim().toLowerCase()
    if (!kw) return allHits.value
    return allHits.value.filter((item) => {
        const hay = [item.id, item.band_title, item.rendered, item.anchor, item.anchor_snippet, item.locus]
            .filter(Boolean)
            .join(' ')
            .toLowerCase()
        return hay.includes(kw)
    })
})

const pagedHits = computed<RecallHitItem[]>(() => {
    const start = (hitCurrentPage.value - 1) * hitPageSize.value
    return filteredHits.value.slice(start, start + hitPageSize.value)
})

const hitsEmptyText = computed(() => {
    if (!form.q.trim()) {
        return isEn.value ? 'Enter a search cue above or click a quick preset to run recall' : '请在上方输入检索线索（Cue）或点击预设词开始检索'
    }
    if (allHits.value.length > 0 && filteredHits.value.length === 0) {
        return isEn.value ? 'No hits match the secondary filter keyword' : '当前结果集中无匹配二次过滤关键词的条目'
    }
    return isEn.value ? 'No matching memory units found in current scope' : '当前作用域内未检索到匹配的记忆单元'
})

watch(hitFilterKeyword, () => {
    hitCurrentPage.value = 1
})

const detailVisible = ref(false)
const detailLoading = ref(false)
// 进度文案必须声明在 detailLoading 之后：useLoadingProgressText 的 computed 在 setup 阶段立即求值，
// 提前引用会命中 TDZ（Cannot access 'detailLoading' before initialization）并整页挂载失败。
const { text: detailLoadingText } = useLoadingProgressText(
    () => detailLoading.value,
    () => (isEn.value ? 'Loading provenance' : '正在加载溯源详情')
)
const detailData = ref<any>(null)
// 溯源正文的形态线索：层级取单元坐标；背景分块的「rel_path#anchor」坐标里拆出来源文件，供围栏挑选语言。
const traceHint = computed<MemoryContentHint>(() => {
    const item = detailData.value?.item
    if (!item) return {}
    const anchor = String(item.anchor || '')
    const cut = anchor.lastIndexOf('#')
    return {
        layer: String(item.x || ''),
        relPath: cut > 0 ? anchor.slice(0, cut) : '',
    }
})
let detailRequest = 0

const formatPercent = (v: unknown) => {
    if (typeof v !== 'number' || !Number.isFinite(v)) return '—'
    return (v * 100).toFixed(1) + '%'
}

const formatScore = (v: unknown) => {
    if (typeof v !== 'number' || !Number.isFinite(v)) return '—'
    return v.toFixed(4)
}

const rawHitUnitID = (hit: { trace?: unknown }) => {
    return typeof hit.trace === 'string' ? hit.trace.trim() : ''
}

let recentCueRequest = 0
const loadRecentCues = async () => {
    const requestID = ++recentCueRequest
    recentCuesFailed.value = false
    try {
        const res = await request<{ list: RecentCueItem[] }>(
            {
                url: '/admin/plugin/rolling-memory/recall/recent',
                method: 'POST',
                data: { limit: 12 },
            },
            { cancelDuplicate: false }
        )
        if (requestID === recentCueRequest) recentCues.value = res.data.data?.list || []
    } catch {
        if (requestID === recentCueRequest) {
            recentCues.value = []
            recentCuesFailed.value = true
        }
    }
}

// 一次完整 recall 在本机负载下实测约 10–12s，超过请求层全局 10s 默认超时；
// 调试台需要拿到完整结果，故放宽到 2 分钟（后端以请求上下文驱动 CLI，前端放弃即随之终止）。
const RECALL_QUERY_TIMEOUT_MS = 120000

const runRecall = async () => {
    const requestID = ++recallRequest
    const params = { ...form, min_confidence: minConfSelect.value > 0 ? minConfSelect.value : undefined }
    loading.value = true
    hasResult.value = false
    queryFailed.value = false
    detailRequest++
    detailVisible.value = false
    detailData.value = null
    result.value = { hits: [], bands: [], raw_hits: [], legs: {}, gaps: {} }
    hitCurrentPage.value = 1
    try {
        const res = await request<RecallQueryResponse>(
            {
                url: '/admin/plugin/rolling-memory/recall/query',
                method: 'POST',
                timeout: RECALL_QUERY_TIMEOUT_MS,
                data: params,
            },
            { cancelDuplicate: false }
        )
        if (requestID !== recallRequest) return
        const data = res.data.data
        if (!data) {
            queryFailed.value = true
            return
        }
        result.value = data
        hasResult.value = true
        if (data.available_scopes && data.available_scopes.length > 0) {
            availableScopes.value = data.available_scopes
        }
        availableWorkspaces.value = data.available_workspaces || []
        if (params.crystallize && params.q.trim() !== '') {
            loadRecentCues()
        }
    } catch {
        if (requestID === recallRequest) queryFailed.value = true
    } finally {
        if (requestID === recallRequest) loading.value = false
    }
}

const replayCue = (cue: RecentCueItem) => {
    form.q = cue.cue
    form.workspace = cue.workspace
    runRecall()
}

const openDetail = async (id: string) => {
    const requestID = ++detailRequest
    detailVisible.value = true
    detailLoading.value = true
    detailData.value = null
    try {
        const res = await request(
            {
                url: '/admin/plugin/rolling-memory/memories/detail',
                method: 'POST',
                data: { id },
            },
            { cancelDuplicate: false }
        )
        if (requestID === detailRequest) detailData.value = res.data.data ?? null
    } catch {
        if (requestID === detailRequest) detailVisible.value = false
    } finally {
        if (requestID === detailRequest) detailLoading.value = false
    }
}

const openRawHit = (id: string) => {
    // ref 时间戳让同一单元被再次「详情溯源」时也能重新开窗（目标页按 unit_id+ref 幂等去重）。
    router.push({ path: '/admin/plugin/rolling-memory/index', query: { unit_id: id, ref: String(Date.now()) } })
}

const goManagePage = () => {
    router.push({ path: '/admin/plugin/rolling-memory/index' })
}

const goMemoryConsole = () => {
    router.push({ path: '/admin/dashboard/artifacts' })
}

// 深链可以只传工作区锚和 scope；没有线索时清除旧线索，避免用旧内容自动查询。
// keepalive 下仅在路由参数变化时应用，普通返回仍保留当前表单。
const lastAppliedQuery = ref('')
const applyRouteQuery = (): boolean => {
    const key = JSON.stringify([route.query.q, route.query.workspace, route.query.scope])
    if (key === lastAppliedQuery.value) return false
    lastAppliedQuery.value = key
    const hasContext = ['q', 'workspace', 'scope'].some((field) => Object.hasOwn(route.query, field))
    if (!hasContext) return false
    form.q = typeof route.query.q === 'string' ? route.query.q.trim() : ''
    form.workspace = typeof route.query.workspace === 'string' ? route.query.workspace.trim() : ''
    form.scope = typeof route.query.scope === 'string' && route.query.scope.trim() ? route.query.scope.trim() : 'mem:self'
    return true
}

// keepalive 下首次进入会先后触发 onMounted 与 onActivated：只在真正「离开后再回来」时
// 走 onActivated 分支，避免同一请求连发两次被请求层判重取消（抛 CanceledError）。
let wasDeactivated = false

onMounted(() => {
    applyRouteQuery()
    runRecall()
    loadRecentCues()
})

onActivated(() => {
    if (!wasDeactivated) return
    wasDeactivated = false
    if (applyRouteQuery()) {
        runRecall()
    }
    loadRecentCues()
})

onDeactivated(() => {
    wasDeactivated = true
    lastAppliedQuery.value = ''
})
</script>

<style scoped lang="scss">
.plugin-rm-recall {
    /* 口径说明用点击式 `?` 弹层 */
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

    .rm-alert {
        margin-bottom: 10px;
    }

    .rm-stats-bar {
        display: grid;
        grid-template-columns: repeat(4, minmax(0, 1fr));
        gap: 10px;
        margin-bottom: 10px;
    }
    @media (max-width: 960px) {
        .rm-stats-bar {
            grid-template-columns: repeat(2, minmax(0, 1fr));
        }
    }

    .stat-item {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 10px 14px;
        border: 1px solid var(--el-border-color-lighter);
        border-left: 3px solid var(--el-color-primary);
        border-radius: 6px;
        background: var(--ag-bg-color-overlay);
        &.success {
            border-left-color: var(--el-color-success);
        }
        &.warning {
            border-left-color: var(--el-color-warning);
        }
        &.info {
            border-left-color: var(--el-color-info);
        }
    }
    .stat-label {
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }
    .stat-value {
        font-size: 15px;
        font-weight: 700;
        color: var(--el-text-color-primary);
    }

    .rm-query-panel {
        padding: 14px 16px;
        margin-bottom: 10px;
        background-color: var(--ag-bg-color-overlay);
        border: 1px solid var(--ag-border-color);
        border-radius: var(--el-border-radius-base);
    }

    .rm-query-row2 {
        margin-top: 10px;
    }

    .rm-param-item {
        display: flex;
        flex-direction: column;
        gap: 4px;
        .rm-param-lbl {
            font-size: 12px;
            color: var(--el-text-color-regular);
        }
    }

    .rm-inline-pair {
        display: flex;
        gap: 4px;
    }

    .rm-full-btn {
        width: 100%;
    }

    .btn-icon {
        margin-right: 5px;
    }

    .rm-presets-row {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
        margin-top: 10px;
        .rm-preset-lbl {
            font-size: 12px;
            color: var(--el-text-color-secondary);
        }
        .rm-preset-tag {
            cursor: pointer;
        }
        .rm-presets-right {
            margin-left: auto;
            display: inline-flex;
            align-items: center;
            gap: 8px;
        }
    }

    /* BuildAdmin 标准 .table-header 样式 */
    .table-header {
        position: relative;
        overflow-x: auto;
        box-sizing: border-box;
        display: flex;
        align-items: center;
        width: 100%;
        max-width: 100%;
        background-color: var(--ag-bg-color-overlay);
        border: 1px solid var(--ag-border-color);
        border-bottom: none;
        padding: 13px 15px;
        font-size: 14px;
        .table-header-operate-text {
            margin-left: 6px;
        }
    }
    .btns-ml-12 {
        margin-left: 12px;
    }
    .table-search {
        display: flex;
        margin-left: auto;
        .quick-search {
            width: 220px;
        }
    }
    .table-search-button-group {
        display: flex;
        margin-left: 12px;
        border: 1px solid var(--el-border-color);
        border-radius: var(--el-border-radius-base);
        overflow: hidden;
        .table-search-button-item {
            display: flex;
            align-items: center;
            justify-content: center;
            width: 44px;
            height: 30px;
            border: none;
            outline: none;
            cursor: pointer;
            &:hover {
                background-color: var(--el-color-info-light-7);
            }
        }
    }

    .ag-table-pagination {
        box-sizing: border-box;
        width: 100%;
        max-width: 100%;
        background-color: var(--ag-bg-color-overlay);
        padding: 13px 15px;
        border: 1px solid var(--ag-border-color);
        border-top: none;
    }

    .rm-bands-panel {
        background-color: var(--ag-bg-color-overlay);
        border: 1px solid var(--ag-border-color);
        padding: 14px;
    }

    .rm-band-cell {
        .rm-band-title {
            font-weight: 600;
            color: var(--el-text-color-primary);
        }
        .rm-chain-sub {
            display: flex;
            gap: 6px;
            align-items: center;
            margin-top: 4px;
            font-size: 11px;
            color: var(--el-text-color-secondary);
        }
    }

    /* 召回内容动辄数千字：列表只预览前几行（与 layerBrowser 的 content-cell 同法），全文点开「详情溯源」看 */
    .rm-fact-box {
        display: -webkit-box;
        -webkit-line-clamp: 4;
        -webkit-box-orient: vertical;
        overflow: hidden;
        line-height: 1.5;
        cursor: pointer;
    }

    .rm-anchor-preview {
        margin-top: 4px;
        font-size: 11px;
        color: var(--el-text-color-secondary);
        .rm-anchor-badge {
            padding: 1px 5px;
            margin-right: 6px;
            font-family: monospace;
            background: var(--el-fill-color);
            border-radius: 3px;
        }
        .rm-snippet-text {
            font-style: italic;
        }
    }

    .rm-metric-cell {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }

    .rm-coord-sub {
        margin-top: 3px;
        font-size: 11px;
        color: var(--el-text-color-secondary);
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
        align-items: flex-start;
        justify-content: space-between;
        padding: 6px 0;
        border-top: 1px dashed var(--el-border-color-lighter);

        .rm-node-main {
            flex: 1 1 320px;
            min-width: 0;
        }

        .rm-node-rendered {
            font-size: 13px;
            line-height: 1.45;
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
        white-space: pre-wrap;
        background: var(--el-fill-color-darker);
        border-radius: 4px;
    }

    .rm-trace-highlight-box {
        padding: 12px;
        font-size: 13px;
        line-height: 1.65;
        white-space: pre-wrap;
        background: var(--el-fill-color-light);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 6px;

        .rm-trace-mark {
            padding: 2px 4px;
            font-weight: 600;
            color: var(--el-color-primary-dark-2);
            background: var(--el-color-warning-light-7);
            border-radius: 3px;
        }
    }

    .rm-section-title {
        margin: 12px 0 8px;
        font-size: 13px;
        font-weight: 600;
    }

    .rm-mono {
        font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
        font-size: 11px;
    }

    .rm-ml4 {
        margin-left: 4px;
    }

    .ag-table-render-buttons-item.el-button--small {
        padding: 5px;
        height: auto;
    }

    .rm-empty-tip {
        padding: 32px;
        text-align: center;
        color: var(--el-text-color-secondary);
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
