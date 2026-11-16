// plugin: kb v1.0.0
import request from '@/utils/request'

/**
 * 知识库插件后台管理面 API（授权面板：grant/list | add | remove；文档入库：document/import）。
 *
 * 端点前缀由宿主提供：插件包 mount 拿到的是宿主建好的 plugin/kb 子组（已挂运行时闸），
 * 本模块只写相对段。形状以本插件后端实现为权威口径（handler/admin/kb）：
 * - grant/list   收 {library_id, page?, limit?}，回 {list, total, page, limit}（库管理权门；分页 ≤100、主键托底序）；
 * - grant/add    收 {library_id, subject_kind(all|team|user), subject_id, perm(read|write)}，回 {row}
 *                （同主体同权限轴幂等——已授直接返回现有行，不重复落审计）；
 * - grant/remove 收 {library_id, grant_id}（**非 {pks}**）；
 * - document/import 收 JSON {library_id, name, text} 或 multipart（file + library_id/name），
 *                回 {document_id, created, job_id, job_key, created_job, build_state}（同库同内容幂等）。
 *
 * 契约依据：可见性单一来源 = 授权行（kb_grant）；撤销 = 软删 grant 行即时生效；
 * 授权变更与审计（kb_audit_log）同事务，由服务层保证。
 *
 * 权限节点由 plugin.yaml 的 menus 声明：grant/list 由中间件回落 plugin/kb/grant/read；
 * add / remove / import 各自独立节点。
 * 通用口径：仅 GET/POST；字段 snake_case（不启用 convertCase）。
 */

/** 授权主体闭集（kb_grant.subject_kind；all=公开一行，subject_id 恒 0 哨兵） */
export type KbGrantSubjectKind = 'all' | 'team' | 'user'

/** 权限轴闭集（read|write 正交：write 不扩读，双授落两行） */
export type KbGrantPerm = 'read' | 'write'

/** 授权行（列表显式列由服务层固定：主键 + 主体 + 权限轴 + 授权人 + 时间戳） */
export interface KbGrantRow {
    id: number
    library_id: number
    subject_kind: KbGrantSubjectKind
    subject_id: number
    perm: KbGrantPerm
    granted_by_kind: string
    granted_by_id: number
    updated_at?: string | null
    created_at?: string | null
}

/** 库行（列表显式列由服务层固定：主键 + 属主 + 库信息 + 状态 + 时间戳） */
export interface KbLibraryRow {
    id: number
    owner_kind: string
    owner_id: number
    name: string
    description: string
    status: number
    updated_at?: string | null
    created_at?: string | null
}

/** 入库回执（document/import；JSON 与 multipart 形态同一条下游路径、同一份回执） */
export interface KbImportResult {
    document_id: number
    /** false = 同库同内容已存在活的文档（幂等：直接返回既有文档，不重复建文档） */
    created: boolean
    job_id?: number
    job_key?: string
    created_job?: boolean
    build_state?: string
}

/** 授权面板列表（门槛 = 库管理权（属主 ∪ 后台轴），与 grant/add、grant/remove 同门） */
export function listKbGrants(data: { library_id: number; page?: number; limit?: number }) {
    return request<{ list: KbGrantRow[]; total: number; page: number; limit: number }>({
        url: '/admin/plugin/kb/grant/list',
        method: 'POST',
        data,
    })
}

/** 授权（grant_add；主体存活性与同事务审计由服务层保证） */
export function addKbGrant(data: { library_id: number; subject_kind: KbGrantSubjectKind; subject_id: number; perm: KbGrantPerm }) {
    return request<{ row: KbGrantRow }>(
        {
            url: '/admin/plugin/kb/grant/add',
            method: 'POST',
            data,
        },
        {
            showSuccessMessage: true,
        }
    )
}

/** 撤销授权（grant_remove；软删 grant 行 = 即时生效） */
export function removeKbGrant(data: { library_id: number; grant_id: number }) {
    return request(
        {
            url: '/admin/plugin/kb/grant/remove',
            method: 'POST',
            data,
        },
        {
            showSuccessMessage: true,
        }
    )
}

/**
 * 文档入库（document/import）：同一端点的两种入参形态——
 * 选了文件 ⇒ multipart（file + library_id + name——后端该分支只读这三项；文本由 JSON 形态提交，选定文件时文本不参与）；只填文本 ⇒ JSON。
 * 成功提示不在此处给（回执里的 created 决定文案，由调用方按幂等语义展示）。
 */
export function importKbDocument(data: { library_id: number; name: string; text?: string; file?: File | null }) {
    if (data.file) {
        const form = new FormData()
        form.append('file', data.file)
        form.append('library_id', String(data.library_id))
        form.append('name', data.name)
        return request<KbImportResult>({
            url: '/admin/plugin/kb/document/import',
            method: 'POST',
            data: form,
        })
    }

    return request<KbImportResult>({
        url: '/admin/plugin/kb/document/import',
        method: 'POST',
        data: {
            library_id: data.library_id,
            name: data.name,
            text: data.text ?? '',
        },
    })
}
