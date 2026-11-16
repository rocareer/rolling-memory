import { TableManagerAPI } from '@/api/table'
import i18n from '@/lang/index'
import request from '@/utils/request'

/**
 * L2 声明式插件「宿主通用页」的请求封装（设计说明；
 * 后端实现 internal/plugin/l2/handler.go 的六动作与 BuildMeta）。
 *
 * 与 api/plugin/index.ts（宿主插件管理面）的分工：那份是 P2 的管理面台账接口，本份只服务
 * 全部 L2 实体共用的通用页（web/src/views/admin/plugin/render/index.vue）。
 *
 * 本类只覆盖三处与 TableManagerAPI 不同的行为：
 *   ① list 随请求下发当前 locale —— 后端按 lang 取 title_zh/title_en 与 dict 文案（「文案」）；
 *   ② list 关闭请求层的网络错误提示 —— 插件未安装/已停用时接口是 HTTP 404（host.Gate 与
 *      handler.fail 的 ErrNotInstalled 分支），页面要把它转成空态而不是弹红色 toast（「坐标契约」）；
 *   ③ 其余动作（get/create/update/delete/sort）沿用宿主同款契约 —— L2 六动作就是照宿主基路由
 *      `internal/handler/base.go` 的形状定义的（update/get 带 :pk，正对 TableManagerAPI 的拼 URL 方式）。
 *
 * convertCase 保持关闭（全链路 snake_case，与 api/plugin/index.ts 同口径）：请求体的
 * page/limit/sort/order/wheres/lang 与响应的 meta/columns/form/dicts/drag_sort/created_at
 * 逐字对应后端字段名，开了转换反而对不上。
 */

/** 列元数据（后端 l2.ColumnMeta；字段名与 JSON 逐字一致） */
export interface L2ColumnMeta {
    prop: string
    label: string
    /** 列宽（后端按类型给常见取值；缺省交给表格自适应） */
    width?: number
    /** 点击列头排序（后端白名单 = id/created_at + 声明 sort 的字段 + drag_sort 时的 weigh） */
    sortable?: boolean
    /** 渲染器：switch=数值/布尔列（0/1），tag=字符串枚举列（配 custom + dict） */
    render?: string
    /** render=tag 时的值→颜色映射（引擎按 dict 顺序确定性分配） */
    custom?: Record<string, string>
    /** 该列的默认搜索操作符（spec.search.op，宿主 16 项词表原文）；缺省该列不进公共搜索 */
    operator?: string
    /** 参与快速搜索（引擎内是前缀匹配） */
    quickSearch?: boolean
}

/** 表单控件元数据（后端 l2.FormMeta；控件映射闭集：页面按 type 分派） */
export interface L2FormMeta {
    prop: string
    label: string
    /** 控件类型：input|textarea|input-number|switch|date|datetime|select|radio */
    type: string
    required?: boolean
    /** 文本类字段的长度上限（后端写入前同样按它校验） */
    length?: number
}

/** dict 的条目形态（后端 spec 的 {value,label_zh,label_en}；引擎响应期已按 lang 转成 Record） */
export interface L2DictItem {
    value: string | number
    label_zh?: string
    label_en?: string
}

/** dict 的两种到达形态：引擎已转好的 Record，或 spec 原样的双语数组（页面统一收敛为 Record） */
export type L2DictShape = Record<string, string> | L2DictItem[]

/** 列表响应随带的渲染元数据（一次请求带元数据 + 数据） */
export interface L2Meta {
    plugin: string
    entity: string
    /** 实体标题（后端按 lang 取 title_zh/title_en） */
    title: string
    /** 实体级拖动排序开关：true 才有 /sort 路由与排序按钮 */
    drag_sort: boolean
    columns: L2ColumnMeta[]
    form: L2FormMeta[]
    /** 字段名 → {值: 文案}；形状 = 宿主 dict（web/types/table.d.ts 的 Record<值, 文案>） */
    dicts?: Record<string, L2DictShape>
}

/** list 响应载荷（契约对齐宿主 List 的 {list,total}，另带 meta 与 has_more） */
export interface L2ListData {
    meta?: L2Meta
    list: TableRow[]
    total: number
    has_more?: boolean
}

/** 当前界面语言（后端只认中文或英文前缀；缺省中文） */
export const l2Lang = (): string => String(i18n.global.locale.value ?? 'zh-cn')

/**
 * 通用页 API：controllerURL 形如 `/admin/plugin/<插件名>/<实体名>/`
 * （`/admin` 前缀由请求层按 VITE_ADMIN_BASE_ROUTE_PATH 重写，与 api/plugin/index.ts 同法）。
 */
export class L2TableAPI extends TableManagerAPI {
    /**
     * 列表（带 meta）：下发当前 locale；404 由页面转空态，故关掉请求层的网络错误提示
     * （HTTP 200 + code≠0 的业务失败——如无权限——仍由请求层统一提示，不受这里影响）
     */
    list<T = L2ListData>(filter: TableInterface['filter'] = {}) {
        return request<T>(
            {
                url: this.actionURL.get('list'),
                method: 'POST',
                data: { ...filter, lang: l2Lang() },
            },
            {
                showNetworkErrorMessage: false,
            }
        )
    }
}
