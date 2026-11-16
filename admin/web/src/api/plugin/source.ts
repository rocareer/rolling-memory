import request from '@/utils/request'
import type { PluginEnv } from './index'

/**
 * 插件来源与统一清单 API（插件清单方案）。
 *
 * 来源的 list/get/create/update/delete 走宿主通用 CRUD（页面里直接 new TableManagerAPI('/admin/plugin/source/')），
 * 本文件只放通用 CRUD 覆盖不到的：来源测试、清单同步/浏览、预演、安装，以及两侧共用的类型与错误码读取。
 * 字段一律 snake_case（宿主 request 的 convertCase 保持关闭）。
 *
 * 超时口径：全局 10s（utils/request.ts:139）不够远程来源与真实安装，因此
 *   test/sync/preflight/install 分别 120s/60s/120s/300s；install 与 test 关闭重复请求取消，
 *   避免用户重复点击时把正在执行的写操作 abort 掉（安装不可重试）。
 * 错误语义：httpx.Fail 始终 HTTP 200，错误码在 JSON code，稳定分支码在 data.error_code；
 *   本文件所有请求两层 toast 都关：showErrorMessage（业务失败 code!=0）与
 *   showNetworkErrorMessage（传输层：超时/断网/CORS，此时没有 error_code）——
 *   两者都由页面用 catalogErrorKey/errorText 展示本地化「错误 + 下一步」，
 *   否则同一失败会既弹一层原始消息（传输层回退 error.message，是英文）又显示本地化 banner。
 */

export type PluginSourceType = 'local' | 'http' | 'git'
export type SourceSyncStatus = '' | 'ok' | 'error'

/** 来源配置行（admin_plugin_sources；last_* 只读） */
export interface PluginSourceRow {
    id: number
    name: string
    title: string
    type: PluginSourceType
    location: string
    enabled: number
    allow_insecure_http: number
    allow_private_hosts: number
    allow_absolute_paths: number
    weigh: number
    last_sync_status: SourceSyncStatus | string
    last_sync_error: string
    last_catalog_sha256: string
    last_catalog_generated_at?: string | null
    last_synced_at?: string | null
    remark: string
    created_at?: string | null
    updated_at?: string | null
}

/** 来源新增/编辑的白名单字段（last_* / created_by / updated_by 由服务端写） */
export interface PluginSourcePayload {
    name?: string
    title: string
    type: PluginSourceType
    location: string
    enabled: number
    allow_insecure_http: number
    allow_private_hosts: number
    allow_absolute_paths: number
    weigh: number
    remark: string
}

/** 只读测试入参（不写库、不建快照） */
export interface SourceTestPayload {
    type: PluginSourceType
    location: string
    allow_insecure_http: number
    allow_private_hosts: number
    allow_absolute_paths: number
}

export interface SourceTestData {
    ok: boolean
    schema: string
    generated_at?: string | null
    entries: number
    catalog_sha256: string
    errors?: string[] | Record<string, string> | null
}

/** catalog/list 随带的来源汇总（含停用来源） */
export interface CatalogSourceSummary {
    id: number
    name: string
    title: string
    type: PluginSourceType
    location?: string
    enabled: number
    last_sync_status: SourceSyncStatus | string
    last_sync_error: string
    last_catalog_sha256?: string
    last_catalog_generated_at?: string | null
    last_synced_at?: string | null
    entry_count?: number
}

/** 清单一行的快照字段（admin_plugin_catalog_entries + 安装态合并列） */
export interface CatalogEntry {
    id?: number
    source_id: number
    source_name: string
    uid: string
    version: string
    version_major?: number
    version_minor?: number
    version_patch?: number
    title_zh: string
    title_en: string
    desc_zh: string
    desc_en: string
    kind: 'code' | 'declarative' | 'mixed'
    host_api: string
    framework_req: string
    requires: string
    license: string
    package_url: string
    package_path: string
    package_size: number
    package_sha256: string
    package_signed: number
    catalog_sha256: string
    synced_at?: string | null
    /** 台账已安装版本；空 = 未安装 */
    installed_version?: string
    installed_status?: string
    /** 同 uid/version 在别的来源内容一致 */
    same_content?: boolean
    /** 同 uid/version 在别的来源 sha 不同 */
    source_conflict?: boolean
}

export interface CatalogListData {
    list: CatalogEntry[]
    total: number
    sources: CatalogSourceSummary[]
    /** 环境标识（后端判定）：生产环境禁止安装/卸载，页面据此禁用安装入口并解释原因 */
    env?: PluginEnv
}

export interface CatalogListParams {
    source_id?: number
    uid?: string
    keyword?: string
    page?: number
    limit?: number
}

export interface CatalogSyncResult {
    source_id: number
    name: string
    status: 'ok' | 'error' | string
    catalog_sha256?: string
    entries?: number
    generated_at?: string
    error?: string
}

export interface CatalogSyncData {
    results: CatalogSyncResult[]
    partial: boolean
}

/** 预演回执里的包摘要（后端 CatalogPackageView：不含本机路径/临时目录） */
export interface CatalogPackageView {
    size: number
    sha256: string
    signed: boolean
}

/** 清单条目自带的 package（后端 PluginEntry.Package：url/path 二者恰好一个） */
export interface CatalogEntryPackage extends CatalogPackageView {
    url?: string
    path?: string
}

/**
 * 预演回执里的条目：后端是 pluginSource.PluginEntry（包外清单条目），
 * **没有 source_id / source_name**；来源信息由页面从当前选中的 catalog 行带入。
 */
export interface CatalogPluginEntry {
    uid: string
    version: string
    title_zh: string
    title_en: string
    desc_zh: string
    desc_en: string
    kind: 'code' | 'declarative' | 'mixed' | string
    host_api: string
    framework_req?: string
    requires?: string[]
    license?: string
    package: CatalogEntryPackage
}

export interface CatalogPreflightData {
    ok: boolean
    installable: boolean
    /** catalog_stale / already_installed / plugin_failed / rule_name_collision / ... / ok */
    reason: string
    report: string
    entry: CatalogPluginEntry
    package: CatalogPackageView
    catalog_sha256: string
    /** 后端 []string：规则名/路径、实体表、目标文件依次追加，前端按数组展示 */
    conflicts?: string[] | null
    existing_targets?: string[] | null
    signature_present: boolean
    trusted_keys_configured: boolean
    trusted_keys_error?: string
    signature_verified: boolean
    allow_unsigned_required: boolean
    /** 预演失败但 HTTP code=0 时的稳定分支码（如 rule_name_collision） */
    error_code?: string
}

export interface CatalogInstallParams {
    source_id: number
    uid: string
    version: string
    allow_unsigned: boolean
    /** 预演时看到的包 sha；服务端据此拒绝 TOCTOU */
    confirm_sha256: string
}

export interface CatalogInstallData {
    name: string
    version: string
    status: string
    effect: 'rebuild_required' | 'gate_ttl' | string
    checksum: string
    signed_by: string
    catalog_sha256: string
}

/** 来源只读测试：读取并严格解析清单，不写库、不建快照 */
export function testSource(data: SourceTestPayload) {
    return request<SourceTestData>(
        {
            url: '/admin/plugin/source/test',
            method: 'POST',
            data,
            timeout: 120000,
        },
        {
            cancelDuplicate: false,
            showErrorMessage: false,
            // 传输层错误（超时/断网）没有 error_code，弹原始消息会盖过测试弹窗里的本地化提示
            showNetworkErrorMessage: false,
        }
    )
}

/** 清单浏览：每个 (source_id, uid, version) 一行，不去重；sources 为全部来源汇总 */
export function listCatalog(data: CatalogListParams) {
    return request<CatalogListData>(
        {
            url: '/admin/plugin/catalog/list',
            method: 'POST',
            data,
        },
        {
            // 页面用 loadError 区服务失败与「没有来源」，避免只留一个 toast + 错误空态
            showErrorMessage: false,
            showNetworkErrorMessage: false,
        }
    )
}

/** 同步清单快照：source_ids 缺省 = 全部启用来源；失败逐来源报告，partial=true 表示部分失败 */
export function syncCatalog(data: { source_ids?: number[] } = {}) {
    return request<CatalogSyncData>(
        {
            url: '/admin/plugin/catalog/sync',
            method: 'POST',
            data,
            timeout: 60000,
        },
        {
            showErrorMessage: false,
            showNetworkErrorMessage: false,
        }
    )
}

/** 非破坏预演：与安装同一条 cli.Install --dry-run 管线，不落盘、不写库 */
export function preflightCatalog(data: { source_id: number; uid: string; version: string }) {
    return request<CatalogPreflightData>(
        {
            url: '/admin/plugin/catalog/preflight',
            method: 'POST',
            data,
            timeout: 120000,
        },
        {
            showErrorMessage: false,
            showNetworkErrorMessage: false,
        }
    )
}

/** 从清单安装：无 force、不覆盖；成功后 effect=rebuild_required 需重编译 + 重启 */
export function installCatalog(data: CatalogInstallParams) {
    return request<CatalogInstallData>(
        {
            url: '/admin/plugin/catalog/install',
            method: 'POST',
            data,
            timeout: 300000,
        },
        {
            cancelDuplicate: false,
            showErrorMessage: false,
            // 安装是写操作：失败必须留在页面的失败态里（含原始消息），不能只留一个转瞬即逝的 toast
            showNetworkErrorMessage: false,
        }
    )
}

/** 服务端稳定分支码（data.error_code + preflight reason）；页面据此选本地化「错误 + 下一步」 */
export const KNOWN_CATALOG_ERRORS = [
    'invalid_input',
    'catalog_invalid',
    'network_blocked',
    'upstream_error',
    'repo_root_unavailable',
    'package_not_found',
    'entry_not_found',
    'package_size_mismatch',
    'package_sha_mismatch',
    'package_signed_mismatch',
    'unsigned_not_allowed',
    'signature_invalid',
    'manifest_mismatch',
    'conflict',
    'existing_targets',
    'rule_name_collision',
    'rule_path_collision',
    'entity_table_exists',
    'trusted_keys_invalid',
    'already_installed',
    'plugin_failed',
    'downgrade_not_supported',
    'upgrade_not_supported',
    'source_not_found',
    'source_disabled',
    'catalog_not_synced',
    'catalog_stale',
    // 环境门禁：生产环境禁止安装/卸载（后端 lifecycle_forbidden，HTTP 403）
    'lifecycle_forbidden',
    'install_failed',
]

/**
 * 稳定错误码 → 语言包键：已知码原样使用，未知/空码归一到 unknown
 * （页面据此取 plugin.source.errors.<key>.message/.hint，避免显示原始 key）。
 */
export function catalogErrorKey(code: string | undefined | null, has: (key: string) => boolean): string {
    const value = code ?? ''
    return value && KNOWN_CATALOG_ERRORS.includes(value) && has(`plugin.source.errors.${value}.message`) ? value : 'unknown'
}

export interface ErrorInfo {
    /** 稳定分支码（data.error_code）；传输/网络错误为空 */
    code: string
    /** 服务端 message（已由服务端脱敏），可能为空 */
    message: string
    /** 服务端随失败返回的明细（source/test 的 data.errors） */
    errors?: string[] | Record<string, string> | null
}

interface ErrorPayload {
    message?: string
    data?: {
        error_code?: string
        errors?: string[] | Record<string, string> | null
    }
}

/**
 * 从 request 的 reject 值（AxiosResponse 或 AxiosError）取出稳定错误码、消息与明细。
 *
 * 两种 reject 形状（utils/request.ts）：
 * - 业务失败（HTTP 200 且 code!=0）：reject 的是 AxiosResponse，无 response 层，服务端 message 在 e.data；
 * - 传输失败（网络不可达 / 超时 / CORS）：reject 的是 AxiosError，没有 response 也没有 data，
 *   服务端 message 不存在 —— 回退到 AxiosError.message（如 "timeout of 120000ms exceeded"），
 *   与 request.ts:229 全局 toast 的取值同源，否则页面按 v-if="error.message" 渲染的原始错误行永远不显示。
 * message 口径不变：服务端 message 优先，为空才回退。
 */
export function errorInfo(err: unknown): ErrorInfo {
    const e = err as { response?: { data?: ErrorPayload }; data?: ErrorPayload; message?: string } | undefined
    const payload = e?.response?.data ?? e?.data
    return {
        code: payload?.data?.error_code ?? '',
        message: payload?.message || (typeof e?.message === 'string' ? e.message : ''),
        errors: payload?.data?.errors ?? null,
    }
}

/** 字节数展示（清单的 package_size 上限 20 MiB） */
export const formatBytes = (bytes?: number | null) => {
    if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) {
        return '-'
    }
    const units = ['B', 'KiB', 'MiB', 'GiB']
    let value = bytes
    let unit = 0
    while (value >= 1024 && unit < units.length - 1) {
        value /= 1024
        unit++
    }
    return `${unit === 0 ? value : value.toFixed(1)} ${units[unit]}`
}

/** sha256 摘要的前 12 位（完整值放 title/详情） */
export const shortSha = (sha?: string | null) => (sha ? sha.slice(0, 12) : '-')

/** 双语标题：en 取 title_en，其余取 title_zh（缺失时互相回退） */
export const entryTitle = (entry: Pick<CatalogEntry, 'title_zh' | 'title_en'>, lang: string) =>
    lang === 'en' ? entry.title_en || entry.title_zh : entry.title_zh || entry.title_en
