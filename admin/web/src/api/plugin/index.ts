import request from '@/utils/request'

/**
 * 插件管理面 API（宿主所有；设计说明API 表）。
 *
 * 形状以后端实现为权威口径（internal/handler/admin/plugin/plugin.go 的五端点、只 GET/POST）：
 * - list  POST /admin/plugin/list    → {list, total, runtime, env}（契约对齐宿主通用 List，另带三处真相对照与环境标识）
 * - get   POST /admin/plugin/get     → {row, entities, audit, files, env}（动作审计分页 page/limit；env 供抽屉置灰卸载入口）
 * - enable/disable POST /admin/plugin/{enable,disable} → {name, status, kind, wired, effect}
 * - uninstall POST /admin/plugin/uninstall → {name, purge, keep_files, effect, report}（写路径复用 CLI 同一实现；不可逆）
 *
 * 权限节点（框架数据库迁移的 3 个种子 + 000009 补的 plugin/uninstall，均不授予任何角色组 ⇒ 默认只有超管可用）：
 * list/get 由 AdminPermission 回落 plugin/read，enable/disable/uninstall 精确匹配；后端另有逐 handler 超管校验。
 * 字段 snake_case（convertCase 保持关闭）。
 */

/** 插件模块运行环境（后端判定，唯一权威：internal/plugin/cli/environment.go） */
export interface PluginEnv {
    /** 判定结果：dev（开发环境，允许安装/卸载）| prod（生产环境，禁止安装/卸载） */
    name: 'dev' | 'prod'
    /** 判定依据：config（显式配置 plugin.env）| auto（未配置，按「是否源码检出」自动判定） */
    source: 'config' | 'auto'
    /** 显式配置的原始值（source=config 时非空；认不出的值一律按 prod 处置，页面据此提示笔误） */
    configured?: string
    /** 安装/卸载是否放行（= name === 'dev'；后端与门禁同点算出，前端直接用它，不复述规则） */
    lifecycle_allowed: boolean
}

/** 插件形态闭集（manifest.kind） */
export type PluginKind = 'code' | 'declarative' | 'mixed'

/** 插件状态闭集（admin_plugins.status；四态状态机） */
export type PluginStatus = 'failed' | 'installed' | 'enabled' | 'disabled'

/** 差异码（后端 Drift* 常量；文案由 plugin.list.wired.* 渲染） */
export type PluginDrift = 'unwired' | 'missing_register' | 'declarative_register' | 'failed'

/** 生效方式码（enable/disable 回执） */
export type PluginEffect = 'gate_ttl' | 'rebuild_required' | 'declarative'

/** 台账行 + 运行时标记（后端 service/admin/plugin 的 Row） */
export interface PluginRow {
    id: number
    name: string
    title: string
    version: string
    kind: PluginKind
    status: PluginStatus
    host_api: string
    host_version: string
    framework_req: string
    requires: string
    checksum: string
    source: string
    signed_by: string
    installed_at?: string | null
    updated_at?: string | null
    remark: string
    /** 存量库迁移基线（adopt）装入的接管边界；>0 时界面禁用 --purge（CLI 三重确认才放行） */
    adopted_through: number
    /** L2 实体行数 */
    entities: number
    /** 进程内注册表是否认识该插件（编译期接线真相） */
    wired: boolean
    /** 注册文件是否有注册行；null = 注册文件不可读（不在源码检出内运行） */
    registered: boolean | null
    /** 差异码列表（空 = 三处一致） */
    drift: PluginDrift[]
}

/** 页面级三处真相对照 */
export interface PluginRuntime {
    registry_file: boolean
    wired: string[]
    registered: string[]
    /** 接线悬空：进程内有、台账无 */
    orphans: string[]
}

/** 列表响应 */
export interface PluginListData {
    list: PluginRow[]
    total: number
    runtime: PluginRuntime
    /** 环境标识（后端判定）：生产环境禁止安装/卸载，插件页面据此渲染标识与提示 */
    env?: PluginEnv
}

/** L2 实体元数据行（不含 spec：大字段不进列表） */
export interface PluginEntityRow {
    id: number
    plugin: string
    name: string
    title_zh: string
    title_en: string
    table_name: string
    created_at?: string | null
    updated_at?: string | null
}

/** 动作审计行（append-only；detail 为 JSON 原文） */
export interface PluginActionRow {
    id: number
    plugin: string
    action: string
    operator: string
    status: string
    detail: unknown
    created_at: string
}

/** 落位文件清单行（与 install --dry-run 的 ② 段同源） */
export interface PluginFileRow {
    rel: string
    target: string
    bytes: number
    sha256: string
    /** 工作树同名文件的诊断文本（CLI 同源）；空 = 新增 */
    conflict: string
}

/** 文件清单视图（available=false：不在源码检出内运行，或源目录不存在） */
export interface PluginFiles {
    available: boolean
    source?: string
    list: PluginFileRow[]
}

/** 动审审计面板（page/limit 为归一后的生效值） */
export interface PluginAudit {
    list: PluginActionRow[]
    total: number
    page: number
    limit: number
}

/** 详情响应 */
export interface PluginDetailData {
    row: PluginRow
    entities: PluginEntityRow[]
    audit: PluginAudit
    files: PluginFiles
    /** 环境标识（后端判定）：抽屉据此置灰卸载入口 */
    env?: PluginEnv
}

/** enable/disable 回执 */
export interface PluginActionResult {
    name: string
    status: PluginStatus
    kind: PluginKind
    wired: boolean
    effect: PluginEffect
}

/** 卸载回执（report = CLI 同一实现的执行回执，已脱敏） */
export interface PluginUninstallResult {
    name: string
    purge: boolean
    keep_files: boolean
    /** 生效方式码：rebuild_required（code/mixed：注册行已撤，需重编译）| gate_ttl（declarative：台账闸即时） */
    effect: PluginEffect
    /** 执行回执（含逐段删/保留清单与计数） */
    report: string
}

/** 单插件详情（manifest 摘要 + 文件清单 + 校验和 + 实体列表 + 动作审计分页） */
export function getPlugin(data: { name: string; page?: number; limit?: number }) {
    return request<PluginDetailData>({
        url: '/admin/plugin/get',
        method: 'POST',
        data,
    })
}

/** 启用插件：台账 enabled + 菜单/节点 status=1 + 审计（菜单与权限节点随之下发） */
export function enablePlugin(data: { name: string }) {
    return request<PluginActionResult>(
        {
            url: '/admin/plugin/enable',
            method: 'POST',
            data,
        },
        {
            showSuccessMessage: true,
        }
    )
}

/** 停用插件：台账 disabled + 菜单/节点 status=0 + 审计（路由 404、菜单隐藏；表与数据保留） */
export function disablePlugin(data: { name: string }) {
    return request<PluginActionResult>(
        {
            url: '/admin/plugin/disable',
            method: 'POST',
            data,
        },
        {
            showSuccessMessage: true,
        }
    )
}

/** 卸载插件：固定顺序 a–e（迁移回滚仅 purge）+ 删落位物 + 删 DB 归属行 + 撤注册行 + 审计；不可逆。
 *  门禁全在后端同一实现：环境（生产禁止）/ 先禁用后卸载 / purge 需确认（界面勾选）/ adopt 装入 purge 默认拒绝 / failed 才认 force。 */
export function uninstallPlugin(data: { name: string; purge?: boolean; keep_files?: boolean; force?: boolean }) {
    return request<PluginUninstallResult>({
        url: '/admin/plugin/uninstall',
        method: 'POST',
        data,
    })
}
