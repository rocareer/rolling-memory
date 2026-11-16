// plugin: ai v0.2.0
// AI 资产体检与成本治理 API 及多维成本聚合纯函数（源：plugins/ai/web/views/api.ts；落位副本 web/src/views/admin/plugin/ai/api.ts 逐字节一致）
//
// 契约（服务端 = plugins/ai/service/{report,cost}.go，端点 = POST /admin/plugin/ai/report，节点 plugin/ai/report）：
// - 字段一律 snake_case（不启用 convertCase）；响应信封 {code,message,time,data}；
// - 只读：本文件没有任何写请求；
// - 渠道密钥只以 api_key_set 布尔出现，提示词只以 md5 参与后端校验，前端不可能拿到密钥或提示词原文。
import request from '@/utils/request'

/** 严重级闭集（与 service.Severity* 逐字一致） */
export type AiSeverity = 'error' | 'warning' | 'info'

/** 发现码闭集（与 service.Code* 逐字一致；文案键 plugin.ai.finding.<code>） */
export type AiFindingCode =
    | 'model_price_missing'
    | 'model_price_partial'
    | 'model_peak_windows_invalid'
    | 'model_offpeak_dead'
    | 'channel_key_missing'
    | 'channel_base_url_missing'
    | 'channel_vendor_disabled'
    | 'vendor_dangling'
    | 'driver_unsupported'
    | 'rerank_driver_incompatible'
    | 'model_unbound'
    | 'model_binding_inactive'
    | 'channel_unbound'
    | 'binding_dangling'
    | 'channel_breaker_permanent'
    | 'channel_breaker_open'
    | 'channel_breaker_half_open'
    | 'tpm_budget_fail_open'
    | 'agent_model_unroutable'
    | 'agent_model_type_mismatch'
    | 'policy_model_freeze_violation'
    | 'policy_official_channel_violation'
    | 'cost_unpriced_calls'
    | 'scan_truncated'

/** 一条体检发现（detail 是语言中立的事实串：键=值） */
export interface AiAssetFinding {
    code: AiFindingCode
    severity: AiSeverity
    ref: string
    detail: string
}

/** 覆盖读数（分母 = 本次快照内的资产行数；触顶时按 limits 解释） */
export interface AiAssetSummary {
    vendors: number
    models: number
    channels: number
    bindings: number
    agents: number
    priced_models: number
    models_with_binding: number
    models_routable: number
    channels_with_key: number
    routable_bindings: number
    routable_agents: number
    open_breakers: number
    permanent_breakers: number
    errors: number
    warnings: number
    infos: number
}

/** 逐面硬上限 */
export interface AiAssetLimits {
    vendors: number
    models: number
    channels: number
    bindings: number
    agents: number
}

/** 并发闸门实时快照 */
export interface AiRuntimeGate {
    enabled: boolean
    max: number
    limit: number
    inflight: number
    queued_interactive: number
    queued_eval: number
    queued_batch: number
    waits: number
    rate_limit_hits: number
    downshifts: number
    last_rate_limited_at: string | null
}

/** TPM 预算统计快照 */
export interface AiRuntimeBudget {
    waits: number
    fail_open: number
    wait_p95_ms: number
    wait_samples: number
    last_wait_at: string | null
    last_fail_open_at: string | null
}

/** 运行时与工作区策略态势视图 */
export interface AiRuntimeView {
    gate: AiRuntimeGate
    budget: AiRuntimeBudget
    model_freeze_ok: boolean
    official_isolation_ok: boolean
    official_clone_ok: boolean
    agent_web_config: string
}

/** 成本与缓存经济学总览 */
export interface AiCostSummary {
    window_hours: number
    currency: string
    total_calls: number
    success_calls: number
    failed_calls: number
    canceled_calls: number
    offpeak_calls: number
    offpeak_rate: number
    avg_latency_ms: number
    input_tokens: number
    cache_hit_tokens: number
    cache_miss_tokens: number
    cache_hit_rate: number
    output_tokens: number
    total_tokens: number
    total_cost: number
    list_cost: number
    cache_saved: number
    offpeak_saved: number
    total_saved: number
    saved_rate: number
}

/** 按厂商归因的成本与节省行 */
export interface AiVendorCostRow {
    vendor_id: number
    vendor_name: string
    vendor_title: string
    driver: string
    channel_count: number
    model_count: number
    currency: string
    calls: number
    success_calls: number
    failed_calls: number
    success_rate: number
    offpeak_calls: number
    offpeak_rate: number
    avg_latency_ms: number
    input_tokens: number
    cache_hit_tokens: number
    cache_hit_rate: number
    output_tokens: number
    total_tokens: number
    cost: number
    list_cost: number
    cache_saved: number
    offpeak_saved: number
    total_saved: number
    saved_rate: number
    cost_share: number
}

/** 按模型归因的成本行 */
export interface AiModelCostRow {
    model: string
    model_type: string
    vendor_id: number
    vendor_title: string
    currency: string
    calls: number
    success_calls: number
    failed_calls: number
    success_rate: number
    offpeak_calls: number
    offpeak_rate: number
    avg_latency_ms: number
    input_tokens: number
    cache_hit_tokens: number
    cache_hit_rate: number
    output_tokens: number
    total_tokens: number
    cost: number
    list_cost: number
    cache_saved: number
    offpeak_saved: number
    total_saved: number
    saved_rate: number
    cost_share: number
}

/** 按渠道归因的成本与性能行 */
export interface AiChannelCostRow {
    channel_id: number
    channel_name: string
    vendor_id: number
    vendor_title: string
    currency: string
    calls: number
    success_calls: number
    failed_calls: number
    success_rate: number
    offpeak_calls: number
    offpeak_rate: number
    avg_latency_ms: number
    input_tokens: number
    cache_hit_tokens: number
    cache_hit_rate: number
    output_tokens: number
    total_tokens: number
    cost: number
    list_cost: number
    cache_saved: number
    offpeak_saved: number
    total_saved: number
    saved_rate: number
    cost_share: number
}

/** 按智能体归因的成本行 */
export interface AiAgentCostItem {
    agent_id: number
    agent_name: string
    agent_title: string
    model: string
    vendor_title: string
    currency: string
    calls: number
    success_calls: number
    failed_calls: number
    success_rate: number
    offpeak_calls: number
    offpeak_rate: number
    avg_latency_ms: number
    input_tokens: number
    cache_hit_tokens: number
    cache_hit_rate: number
    output_tokens: number
    total_tokens: number
    cost: number
    list_cost: number
    cache_saved: number
    offpeak_saved: number
    total_saved: number
    saved_rate: number
    cost_per_1k_calls: number
    cost_share: number
}

/** 按调用来源归因的成本行 */
export interface AiSourceCostRow {
    source: string
    currency: string
    calls: number
    success_calls: number
    failed_calls: number
    success_rate: number
    offpeak_calls: number
    offpeak_rate: number
    avg_latency_ms: number
    input_tokens: number
    cache_hit_tokens: number
    cache_hit_rate: number
    output_tokens: number
    total_tokens: number
    cost: number
    list_cost: number
    cache_saved: number
    offpeak_saved: number
    total_saved: number
    saved_rate: number
    cost_share: number
}

/** 五维交叉聚合桶（厂商×渠道×模型×智能体×来源） */
export interface AiCostCrossBucket {
    vendor_id: number
    vendor_title: string
    channel_id: number
    channel_name: string
    model: string
    model_type: string
    agent_id: number
    agent_name: string
    agent_title: string
    source: string
    currency: string
    calls: number
    success_calls: number
    failed_calls: number
    canceled_calls: number
    offpeak_calls: number
    latency_sum_ms: number
    input_tokens: number
    cache_hit_tokens: number
    output_tokens: number
    total_tokens: number
    cost: number
    list_cost: number
    cache_saved: number
    offpeak_saved: number
    total_saved: number
}

/** 成本与缓存经济学完整子模块视图 */
export interface AiCostView {
    summary: AiCostSummary
    by_vendor: AiVendorCostRow[]
    by_model: AiModelCostRow[]
    by_channel: AiChannelCostRow[]
    by_agent: AiAgentCostItem[]
    by_source: AiSourceCostRow[]
    buckets: AiCostCrossBucket[]
}

/** 厂商行 */
export interface AiAssetVendor {
    id: number
    name: string
    title: string
    driver: string
    driver_supported: boolean
    base_url: string
    status: string
    model_count: number
    channel_count: number
    active_channel_count: number
}

/** 单个路由候选节点 */
export interface AiRouteTarget {
    binding_id: number
    channel_id: number
    channel_name: string
    upstream_name: string
    driver: string
    priority: number
    weigh: number
    tpm_limit: number
    breaker_state: string
    routable: boolean
}

/** 模型路由降级层 */
export interface AiRouteTier {
    priority: number
    total_weigh: number
    targets: AiRouteTarget[]
}

/** 模型行（price_ok 与路由拓扑分层由服务端算齐，前端不重算） */
export interface AiAssetModel {
    id: number
    name: string
    vendor_id: number
    vendor_title: string
    type: string
    status: string
    input_price: number
    output_price: number
    cache_input_price: number
    price_currency: string
    offpeak_ratio: number
    peak_windows: string
    price_ok: boolean
    peak_windows_ok: boolean
    whitelist_ok: boolean
    binding_count: number
    active_binding_count: number
    active_agent_count: number
    route_tiers: AiRouteTier[]
}

/** 渠道行（只有 api_key_set 布尔，无密钥值；含实时熔断态与 TPM 水位） */
export interface AiAssetChannel {
    id: number
    vendor_id: number
    vendor_title: string
    name: string
    driver: string
    effective_driver: string
    driver_supported: boolean
    supports_rerank: boolean
    effective_base_url: string
    priority: number
    weigh: number
    timeout_ms: number
    tpm_limit: number
    budget_tokens: number
    budget_updated_ms: number
    status: string
    api_key_set: boolean
    binding_count: number
    active_binding_count: number
    breaker_state: string
    breaker_permanent: boolean
    breaker_failures: number
    total_calls: number
    total_failures: number
    last_error: string
}

/** 绑定行 */
export interface AiAssetBinding {
    id: number
    channel_id: number
    channel_name: string
    model_id: number
    model_name: string
    model_type: string
    upstream_name: string
    effective_upstream: string
    effective_driver: string
    effective_priority: number
    effective_weigh: number
    status: string
    routable: boolean
    block_reason: string
}

/** 智能体消费与合规行 */
export interface AiAssetAgent {
    id: number
    name: string
    title: string
    type: string
    model: string
    model_found: boolean
    model_type: string
    type_match: boolean
    routable: boolean
    whitelist_ok: boolean
    official_ok: boolean
    version: number
    status: string
}

/** 体检报告（一次请求拿全） */
export interface AiAssetReport {
    generated_at: number
    limits: AiAssetLimits
    summary: AiAssetSummary
    runtime: AiRuntimeView
    cost: AiCostView
    vendors: AiAssetVendor[]
    models: AiAssetModel[]
    channels: AiAssetChannel[]
    bindings: AiAssetBinding[]
    agents: AiAssetAgent[]
    findings: AiAssetFinding[]
    truncated: string[]
}

/**
 * 读取 AI 资产体检与成本经济学报告（POST，支持指定 cost_hours = 24 | 168 | 720）。
 */
export async function getAssetReport(costHours = 24): Promise<AiAssetReport> {
    const res = await request<AiAssetReport>({
        url: '/admin/plugin/ai/report',
        method: 'POST',
        data: { cost_hours: costHours },
    })
    return res.data.data
}

function round4(v: number): number {
    return Math.round(v * 1e4) / 1e4
}

/** 格式化 Token 数量显示（k / M） */
export function formatTokenCount(n: number): string {
    if (!n || n <= 0) return '0'
    if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`
    if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
    return String(n)
}

/** 基于过滤后的交叉桶重算成本总览 */
export function recomputeCostSummary(buckets: AiCostCrossBucket[], windowHours: number): AiCostSummary {
    let totalCalls = 0,
        successCalls = 0,
        failedCalls = 0,
        canceledCalls = 0,
        offpeakCalls = 0,
        latencySum = 0
    let inputTokens = 0,
        cacheHitTokens = 0,
        outputTokens = 0,
        totalTokens = 0
    let totalCost = 0,
        listCost = 0,
        cacheSaved = 0,
        offpeakSaved = 0
    for (const b of buckets) {
        totalCalls += b.calls
        successCalls += b.success_calls
        failedCalls += b.failed_calls
        canceledCalls += b.canceled_calls
        offpeakCalls += b.offpeak_calls
        latencySum += b.latency_sum_ms
        inputTokens += b.input_tokens
        cacheHitTokens += b.cache_hit_tokens
        outputTokens += b.output_tokens
        totalTokens += b.total_tokens
        totalCost += b.cost
        listCost += b.list_cost
        cacheSaved += b.cache_saved
        offpeakSaved += b.offpeak_saved
    }
    const totalSaved = round4(cacheSaved + offpeakSaved)
    return {
        window_hours: windowHours,
        currency: 'CNY',
        total_calls: totalCalls,
        success_calls: successCalls,
        failed_calls: failedCalls,
        canceled_calls: canceledCalls,
        offpeak_calls: offpeakCalls,
        offpeak_rate: totalCalls > 0 ? round4(offpeakCalls / totalCalls) : 0,
        avg_latency_ms: totalCalls > 0 ? Math.floor(latencySum / totalCalls) : 0,
        input_tokens: inputTokens,
        cache_hit_tokens: cacheHitTokens,
        cache_miss_tokens: Math.max(0, inputTokens - cacheHitTokens),
        cache_hit_rate: inputTokens > 0 ? round4(cacheHitTokens / inputTokens) : 0,
        output_tokens: outputTokens,
        total_tokens: totalTokens,
        total_cost: round4(totalCost),
        list_cost: round4(listCost),
        cache_saved: round4(cacheSaved),
        offpeak_saved: round4(offpeakSaved),
        total_saved: totalSaved,
        saved_rate: listCost > 0 ? round4(totalSaved / listCost) : 0,
    }
}

/** 基于过滤后的交叉桶重算厂商排行榜 */
export function recomputeByVendor(buckets: AiCostCrossBucket[], baseVendors: AiVendorCostRow[], totalCost: number): AiVendorCostRow[] {
    const map = new Map<string, AiVendorCostRow>()
    const chSets = new Map<string, Set<number>>()
    const mdSets = new Map<string, Set<string>>()
    for (const b of buckets) {
        const key = b.vendor_title || `#${b.vendor_id}`
        let r = map.get(key)
        if (!r) {
            const orig = baseVendors.find((v) => v.vendor_title === b.vendor_title)
            r = {
                vendor_id: b.vendor_id,
                vendor_name: orig?.vendor_name ?? key,
                vendor_title: key,
                driver: orig?.driver ?? '',
                channel_count: 0,
                model_count: 0,
                currency: b.currency,
                calls: 0,
                success_calls: 0,
                failed_calls: 0,
                success_rate: 0,
                offpeak_calls: 0,
                offpeak_rate: 0,
                avg_latency_ms: 0,
                input_tokens: 0,
                cache_hit_tokens: 0,
                cache_hit_rate: 0,
                output_tokens: 0,
                total_tokens: 0,
                cost: 0,
                list_cost: 0,
                cache_saved: 0,
                offpeak_saved: 0,
                total_saved: 0,
                saved_rate: 0,
                cost_share: 0,
            }
            map.set(key, r)
            chSets.set(key, new Set())
            mdSets.set(key, new Set())
        }
        chSets.get(key)!.add(b.channel_id)
        mdSets.get(key)!.add(b.model)
        r.calls += b.calls
        r.success_calls += b.success_calls
        r.failed_calls += b.failed_calls
        r.offpeak_calls += b.offpeak_calls
        r.avg_latency_ms += b.latency_sum_ms
        r.input_tokens += b.input_tokens
        r.cache_hit_tokens += b.cache_hit_tokens
        r.output_tokens += b.output_tokens
        r.total_tokens += b.total_tokens
        r.cost = round4(r.cost + b.cost)
        r.list_cost = round4(r.list_cost + b.list_cost)
        r.cache_saved = round4(r.cache_saved + b.cache_saved)
        r.offpeak_saved = round4(r.offpeak_saved + b.offpeak_saved)
    }
    const rows = Array.from(map.values())
    for (const r of rows) {
        r.channel_count = chSets.get(r.vendor_title)?.size ?? 0
        r.model_count = mdSets.get(r.vendor_title)?.size ?? 0
        if (r.calls > 0) {
            r.avg_latency_ms = Math.floor(r.avg_latency_ms / r.calls)
            r.success_rate = round4(r.success_calls / r.calls)
            r.offpeak_rate = round4(r.offpeak_calls / r.calls)
        }
        if (r.input_tokens > 0) r.cache_hit_rate = round4(r.cache_hit_tokens / r.input_tokens)
        r.total_saved = round4(r.cache_saved + r.offpeak_saved)
        if (r.list_cost > 0) r.saved_rate = round4(r.total_saved / r.list_cost)
        if (totalCost > 0) r.cost_share = round4(r.cost / totalCost)
    }
    return rows.sort((a, b) => b.cost - a.cost || b.calls - a.calls)
}

/** 基于过滤后的交叉桶重算渠道排行榜 */
export function recomputeByChannel(buckets: AiCostCrossBucket[], totalCost: number): AiChannelCostRow[] {
    const map = new Map<string, AiChannelCostRow>()
    for (const b of buckets) {
        let r = map.get(b.channel_name)
        if (!r) {
            r = {
                channel_id: b.channel_id,
                channel_name: b.channel_name,
                vendor_id: b.vendor_id,
                vendor_title: b.vendor_title,
                currency: b.currency,
                calls: 0,
                success_calls: 0,
                failed_calls: 0,
                success_rate: 0,
                offpeak_calls: 0,
                offpeak_rate: 0,
                avg_latency_ms: 0,
                input_tokens: 0,
                cache_hit_tokens: 0,
                cache_hit_rate: 0,
                output_tokens: 0,
                total_tokens: 0,
                cost: 0,
                list_cost: 0,
                cache_saved: 0,
                offpeak_saved: 0,
                total_saved: 0,
                saved_rate: 0,
                cost_share: 0,
            }
            map.set(b.channel_name, r)
        }
        r.calls += b.calls
        r.success_calls += b.success_calls
        r.failed_calls += b.failed_calls
        r.offpeak_calls += b.offpeak_calls
        r.avg_latency_ms += b.latency_sum_ms
        r.input_tokens += b.input_tokens
        r.cache_hit_tokens += b.cache_hit_tokens
        r.output_tokens += b.output_tokens
        r.total_tokens += b.total_tokens
        r.cost = round4(r.cost + b.cost)
        r.list_cost = round4(r.list_cost + b.list_cost)
        r.cache_saved = round4(r.cache_saved + b.cache_saved)
        r.offpeak_saved = round4(r.offpeak_saved + b.offpeak_saved)
    }
    const rows = Array.from(map.values())
    for (const r of rows) {
        if (r.calls > 0) {
            r.avg_latency_ms = Math.floor(r.avg_latency_ms / r.calls)
            r.success_rate = round4(r.success_calls / r.calls)
            r.offpeak_rate = round4(r.offpeak_calls / r.calls)
        }
        if (r.input_tokens > 0) r.cache_hit_rate = round4(r.cache_hit_tokens / r.input_tokens)
        r.total_saved = round4(r.cache_saved + r.offpeak_saved)
        if (r.list_cost > 0) r.saved_rate = round4(r.total_saved / r.list_cost)
        if (totalCost > 0) r.cost_share = round4(r.cost / totalCost)
    }
    return rows.sort((a, b) => b.cost - a.cost || b.calls - a.calls)
}

/** 基于过滤后的交叉桶重算模型排行榜 */
export function recomputeByModel(buckets: AiCostCrossBucket[], totalCost: number): AiModelCostRow[] {
    const map = new Map<string, AiModelCostRow>()
    for (const b of buckets) {
        let r = map.get(b.model)
        if (!r) {
            r = {
                model: b.model,
                model_type: b.model_type,
                vendor_id: b.vendor_id,
                vendor_title: b.vendor_title,
                currency: b.currency,
                calls: 0,
                success_calls: 0,
                failed_calls: 0,
                success_rate: 0,
                offpeak_calls: 0,
                offpeak_rate: 0,
                avg_latency_ms: 0,
                input_tokens: 0,
                cache_hit_tokens: 0,
                cache_hit_rate: 0,
                output_tokens: 0,
                total_tokens: 0,
                cost: 0,
                list_cost: 0,
                cache_saved: 0,
                offpeak_saved: 0,
                total_saved: 0,
                saved_rate: 0,
                cost_share: 0,
            }
            map.set(b.model, r)
        }
        r.calls += b.calls
        r.success_calls += b.success_calls
        r.failed_calls += b.failed_calls
        r.offpeak_calls += b.offpeak_calls
        r.avg_latency_ms += b.latency_sum_ms
        r.input_tokens += b.input_tokens
        r.cache_hit_tokens += b.cache_hit_tokens
        r.output_tokens += b.output_tokens
        r.total_tokens += b.total_tokens
        r.cost = round4(r.cost + b.cost)
        r.list_cost = round4(r.list_cost + b.list_cost)
        r.cache_saved = round4(r.cache_saved + b.cache_saved)
        r.offpeak_saved = round4(r.offpeak_saved + b.offpeak_saved)
    }
    const rows = Array.from(map.values())
    for (const r of rows) {
        if (r.calls > 0) {
            r.avg_latency_ms = Math.floor(r.avg_latency_ms / r.calls)
            r.success_rate = round4(r.success_calls / r.calls)
            r.offpeak_rate = round4(r.offpeak_calls / r.calls)
        }
        if (r.input_tokens > 0) r.cache_hit_rate = round4(r.cache_hit_tokens / r.input_tokens)
        r.total_saved = round4(r.cache_saved + r.offpeak_saved)
        if (r.list_cost > 0) r.saved_rate = round4(r.total_saved / r.list_cost)
        if (totalCost > 0) r.cost_share = round4(r.cost / totalCost)
    }
    return rows.sort((a, b) => b.cost - a.cost || b.calls - a.calls)
}

/** 基于过滤后的交叉桶重算智能体排行榜 */
export function recomputeByAgent(buckets: AiCostCrossBucket[], totalCost: number): AiAgentCostItem[] {
    const map = new Map<string, AiAgentCostItem>()
    for (const b of buckets) {
        const key = `${b.agent_id}:${b.agent_name}:${b.model}`
        let r = map.get(key)
        if (!r) {
            r = {
                agent_id: b.agent_id,
                agent_name: b.agent_name,
                agent_title: b.agent_title,
                model: b.model,
                vendor_title: b.vendor_title,
                currency: b.currency,
                calls: 0,
                success_calls: 0,
                failed_calls: 0,
                success_rate: 0,
                offpeak_calls: 0,
                offpeak_rate: 0,
                avg_latency_ms: 0,
                input_tokens: 0,
                cache_hit_tokens: 0,
                cache_hit_rate: 0,
                output_tokens: 0,
                total_tokens: 0,
                cost: 0,
                list_cost: 0,
                cache_saved: 0,
                offpeak_saved: 0,
                total_saved: 0,
                saved_rate: 0,
                cost_per_1k_calls: 0,
                cost_share: 0,
            }
            map.set(key, r)
        }
        r.calls += b.calls
        r.success_calls += b.success_calls
        r.failed_calls += b.failed_calls
        r.offpeak_calls += b.offpeak_calls
        r.avg_latency_ms += b.latency_sum_ms
        r.input_tokens += b.input_tokens
        r.cache_hit_tokens += b.cache_hit_tokens
        r.output_tokens += b.output_tokens
        r.total_tokens += b.total_tokens
        r.cost = round4(r.cost + b.cost)
        r.list_cost = round4(r.list_cost + b.list_cost)
        r.cache_saved = round4(r.cache_saved + b.cache_saved)
        r.offpeak_saved = round4(r.offpeak_saved + b.offpeak_saved)
        r.total_saved = round4(r.total_saved + b.total_saved)
    }
    const rows = Array.from(map.values())
    for (const r of rows) {
        if (r.calls > 0) {
            r.avg_latency_ms = Math.floor(r.avg_latency_ms / r.calls)
            r.success_rate = round4(r.success_calls / r.calls)
            r.offpeak_rate = round4(r.offpeak_calls / r.calls)
            r.cost_per_1k_calls = round4((r.cost * 1000) / r.calls)
        }
        if (r.input_tokens > 0) r.cache_hit_rate = round4(r.cache_hit_tokens / r.input_tokens)
        if (r.list_cost > 0) r.saved_rate = round4(r.total_saved / r.list_cost)
        if (totalCost > 0) r.cost_share = round4(r.cost / totalCost)
    }
    return rows.sort((a, b) => b.cost - a.cost || b.calls - a.calls)
}

/** 基于过滤后的交叉桶重算调用来源排行榜 */
export function recomputeBySource(buckets: AiCostCrossBucket[], totalCost: number): AiSourceCostRow[] {
    const map = new Map<string, AiSourceCostRow>()
    for (const b of buckets) {
        let r = map.get(b.source)
        if (!r) {
            r = {
                source: b.source,
                currency: b.currency,
                calls: 0,
                success_calls: 0,
                failed_calls: 0,
                success_rate: 0,
                offpeak_calls: 0,
                offpeak_rate: 0,
                avg_latency_ms: 0,
                input_tokens: 0,
                cache_hit_tokens: 0,
                cache_hit_rate: 0,
                output_tokens: 0,
                total_tokens: 0,
                cost: 0,
                list_cost: 0,
                cache_saved: 0,
                offpeak_saved: 0,
                total_saved: 0,
                saved_rate: 0,
                cost_share: 0,
            }
            map.set(b.source, r)
        }
        r.calls += b.calls
        r.success_calls += b.success_calls
        r.failed_calls += b.failed_calls
        r.offpeak_calls += b.offpeak_calls
        r.avg_latency_ms += b.latency_sum_ms
        r.input_tokens += b.input_tokens
        r.cache_hit_tokens += b.cache_hit_tokens
        r.output_tokens += b.output_tokens
        r.total_tokens += b.total_tokens
        r.cost = round4(r.cost + b.cost)
        r.list_cost = round4(r.list_cost + b.list_cost)
        r.cache_saved = round4(r.cache_saved + b.cache_saved)
        r.offpeak_saved = round4(r.offpeak_saved + b.offpeak_saved)
        r.total_saved = round4(r.total_saved + b.total_saved)
    }
    const rows = Array.from(map.values())
    for (const r of rows) {
        if (r.calls > 0) {
            r.avg_latency_ms = Math.floor(r.avg_latency_ms / r.calls)
            r.success_rate = round4(r.success_calls / r.calls)
            r.offpeak_rate = round4(r.offpeak_calls / r.calls)
        }
        if (r.input_tokens > 0) r.cache_hit_rate = round4(r.cache_hit_tokens / r.input_tokens)
        if (r.list_cost > 0) r.saved_rate = round4(r.total_saved / r.list_cost)
        if (totalCost > 0) r.cost_share = round4(r.cost / totalCost)
    }
    return rows.sort((a, b) => b.cost - a.cost || b.calls - a.calls)
}
