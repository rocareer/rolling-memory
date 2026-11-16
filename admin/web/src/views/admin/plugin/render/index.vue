<template>
    <div class="default-main plugin-render">
        <!--
            空态（「坐标契约」）：坐标解析失败、或实体接口 404（插件未安装 / 已停用）
            ⇒ 只显示空态，不抛未捕获错误、不留半截表格。
        -->
        <el-empty v-if="!available" class="plugin-render-empty" :description="emptyDescription">
            <div v-if="emptyHint" class="plugin-render-hint">{{ emptyHint }}</div>
            <el-button v-if="coords" v-blur type="primary" @click="load">{{ t('plugin.render.retry') }}</el-button>
        </el-empty>

        <template v-else>
            <TableHeader
                :manager="tableManager"
                v-model:com-search="tableManager.comSearch"
                :buttons="['refresh', 'add', 'edit', 'delete', 'comSearch', 'quickSearch', 'columnDisplay']"
                :quick-search-placeholder="t('plugin.render.searchPlaceholder')"
            />
            <Table ref="tableRef" :manager="tableManager" :empty-text="t('plugin.render.empty')" />

            <!--
                通用表单（全部 L2 实体共用本页，故弹窗与页面同文件）：控件按 meta.form 的 type 分派，
               控件映射闭集由 agInputType/agInputAttr 落成宿主 agInput 的输入控件。
            -->
            <el-dialog
                class="ag-operate-dialog"
                :close-on-click-modal="false"
                :model-value="['create', 'update'].includes(tableManager.form.operate!)"
                @close="tableManager.toggleForm"
                :destroy-on-close="true"
                :draggable="true"
            >
                <template #header>
                    <div class="title">{{ formTitle }}</div>
                </template>
                <el-scrollbar v-loading="tableManager.form.loading" class="ag-table-form-scrollbar">
                    <div
                        class="ag-operate-form"
                        :class="'ag-' + tableManager.form.operate + '-form'"
                        :style="config.layout.shrink ? '' : 'width: calc(100% - ' + tableManager.form.labelWidth! / 2 + 'px)'"
                    >
                        <el-form
                            ref="formRef"
                            @submit.prevent=""
                            @keyup.enter="tableManager.submitForm(formRef)"
                            :model="tableManager.form.items"
                            :label-position="config.layout.shrink ? 'top' : 'right'"
                            :label-width="tableManager.form.labelWidth + 'px'"
                            :rules="rules"
                            v-if="!tableManager.form.loading"
                        >
                            <el-form-item v-for="item in formMeta" :key="item.prop" :label="item.label" :prop="item.prop">
                                <AgInput
                                    v-model="tableManager.form.items![item.prop]"
                                    :type="agInputType(item.type)"
                                    :attr="agInputAttr(item)"
                                />
                            </el-form-item>
                        </el-form>
                    </div>
                </el-scrollbar>
                <template #footer>
                    <div :style="'width: calc(100% - ' + tableManager.form.labelWidth! / 1.8 + 'px)'">
                        <el-button v-blur @click="tableManager.toggleForm()">{{ t('common.cancel') }}</el-button>
                        <el-button v-blur :loading="tableManager.form.submitLoading" @click="tableManager.submitForm(formRef)" type="primary">
                            {{ tableManager.form.operatePKs && tableManager.form.operatePKs.length > 1 ? t('common.saveAndContinue') : t('common.save') }}
                        </el-button>
                    </div>
                </template>
            </el-dialog>
        </template>
    </div>
</template>

<script setup lang="ts">
import { L2TableAPI, l2Lang, type L2ColumnMeta, type L2DictShape, type L2FormMeta, type L2ListData, type L2Meta } from '@/api/plugin/render'
import AgInput from '@/components/agInput/index.vue'
import type { InputAttr } from '@/components/agInput/index'
import TableHeader from '@/components/table/header/index.vue'
import { getDefaultOptButtons } from '@/components/table/index'
import Table from '@/components/table/index.vue'
import { useTableManager } from '@/hooks/useTableManager'
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { useConfig } from '@/stores/config'
import { auth } from '@/utils/common'
import { buildValidatorRule } from '@/utils/validate'
import { ElMessage, dayjs, type FormItemRule } from 'element-plus'
import { computed, nextTick, onMounted, ref, useTemplateRef } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'

/**
 * L2 声明式插件的**宿主通用页**（设计说明，C7 路线 a）：
 * 全部 L2 实体共用本组件，菜单由安装器按固定公式代写（component 恒为
 * `/src/views/admin/plugin/render/index.vue`，见 internal/plugin/cli/dbwrite.go 的 writeInstallRules）。
 *
 * 页面只做「按元数据渲染」这一件事：列 / 表单 / 搜索 / 排序 / 软删全部来自 list 响应随带的 meta
 * （后端 internal/plugin/l2/handler.go 的 BuildMeta），页面不内置任何字段名、不猜类型。
 *
 * keepalive=0 是菜单侧的硬决定：本组件被 N 个实体共用，而宿主 keep-alive 的 include 按
 * **组件 name** 匹配（layouts/admin/router-view/main.vue 取 tabView.name = 菜单 name）⇒ 组件名不可能
 * 同时等于 N 个规则名。故本页不依赖任何缓存态：每次进入都重新取元数据与数据。
 */
defineOptions({
    name: 'plugin/render',
})

const { t } = useI18n()
const config = useConfig()
const route = useRoute()
const tableRef = useTemplateRef('tableRef')
const formRef = useTemplateRef('formRef')

// ---------- 坐标契约：先剥后台入口前缀 ----------

/** 插件名形状（后端 manifest.namePattern：^[a-z][a-z0-9-]{1,31}$） */
const PLUGIN_NAME_RE = /^[a-z][a-z0-9-]{1,31}$/
/** 实体名形状（后端 l2 的 identifierPattern：^[a-z][a-z0-9_]{0,62}$） */
const ENTITY_NAME_RE = /^[a-z][a-z0-9_]{0,62}$/

/**
 * 从 route.path 解析 (插件名, 实体名)：实际形态是 <adminBaseRoutePath>/plugin/<名>/<实体>
 * （菜单 path 即坐标，规则名同形）。解析失败 ⇒ null ⇒ 页面直接空态、不发任何请求。
 */
const parseEntityCoords = (path: string): { plugin: string; entity: string } | null => {
    const rest = path.startsWith(adminBaseRoutePath) ? path.slice(adminBaseRoutePath.length) : path
    const segments = rest.split('/').filter((segment) => segment !== '')
    if (segments.length !== 3 || segments[0] !== 'plugin') {
        return null
    }
    if (!PLUGIN_NAME_RE.test(segments[1]) || !ENTITY_NAME_RE.test(segments[2])) {
        return null
    }
    return { plugin: segments[1], entity: segments[2] }
}

const coords = parseEntityCoords(route.path)

// ---------- 页面状态 ----------

/** meta 里的表单元数据（弹窗按它渲染控件） */
const formMeta = ref<L2FormMeta[]>([])
/** 字段名 → {值: 文案}（枚举列的 dict 与 select/radio 的选项共用这一份） */
const dicts = ref<Record<string, Record<string, string>>>({})
/** 实体标题（后端按 lang 取 title_zh/title_en） */
const metaTitle = ref('')
/** 实体级拖动排序开关（true 才有 /sort 路由与拖动按钮） */
const dragSortEnabled = ref(false)
/** meta 指纹：元数据没变就不重建列，避免每次刷新都重置用户调过的列显示 */
const metaSignature = ref('')
/** 实体接口 404：插件未安装或已被停用（host.Gate 与 ErrNotInstalled 都是 404） */
const unavailable = ref(false)

const available = computed(() => !!coords && !unavailable.value)
const emptyDescription = computed(() => (coords ? t('plugin.render.notInstalled') : t('plugin.render.badRoute')))
const emptyHint = computed(() => (coords ? t('plugin.render.notInstalledTip', { plugin: coords.plugin, entity: coords.entity }) : ''))

/** 弹窗标题：操作名 + 实体标题（多实体页签并存时能看出在编辑哪张表） */
const formTitle = computed(() => {
    const operate = tableManager.form.operate == 'create' ? t('common.add') : t('common.edit')
    return metaTitle.value ? `${operate} · ${metaTitle.value}` : operate
})

// ---------- 表格管家 ----------

const tableManager = useTableManager({
    // 坐标非法时 URL 里的占位段进不了后端：所有请求入口都由 before 钩子挡下（见下）
    api: new L2TableAPI(`/admin/plugin/${coords?.plugin ?? '-'}/${coords?.entity ?? '-'}/`),
    table: {
        column: [],
        // 默认序：主键倒序（引擎的排序白名单永远含 id，托底必然成立）；drag_sort 实体在首次
        // 取数后改判 weigh 序 —— 界面展示序必须与 /sort 的排序口径一致，否则等权重区间的落位会错位
        filter: {
            sort: 'id',
            order: 'desc',
        },
        // 行内编辑入口只留显式按钮：元数据声明的字段集不可预知（text/date 双击即编辑易误触），
        // 与宿主插件管理页（views/admin/plugin/index.vue）同口径
        dblClickNotEditColumn: ['all'],
    },
    before: {
        getData: () => !!coords,
        delete: () => !!coords,
        toggleForm: () => !!coords,
    },
})

// ---------- 元数据 → 列 / 表单 ----------

/** 控件映射闭集（spec.form 值）→ 宿主 agInput 的输入控件类型（一一对应，不做同义扩张） */
const AG_INPUT_TYPE: Record<string, string> = {
    input: 'string', // el-input
    textarea: 'textarea', // el-input[type=textarea]
    'input-number': 'number', // el-input-number
    switch: 'switch', // el-switch
    date: 'date', // el-date-picker
    datetime: 'datetime', // el-date-picker
    select: 'select', // el-select
    radio: 'radio', // el-radio-group
}

const agInputType = (type: string): string => AG_INPUT_TYPE[type] ?? 'string'

/**
 * 控件属性。attr 恒传对象：agInput 的 attr 默认值 `() => {}` 返回 undefined，
 * 而它的 select/radio 分支会直接取 attr.dict（不传会抛错）。
 */
const agInputAttr = (item: L2FormMeta): InputAttr => {
    const control = agInputType(item.type)
    if (control === 'switch') {
        // AGENTS 状态控件硬条款：bool 列是 SMALLINT 0/1（l2/ddl.go 的 pgBaseType），
        // el-switch 必须显式值域（禁止裸 el-switch：值会被静默改成 false）
        return { activeValue: 1, inactiveValue: 0 }
    }
    if (control === 'select' || control === 'radio') {
        // dict 驱动（引擎按 locale 转好的 Record；用 agInput 的 subAttr 透传不必，dict 即公共属性）
        return { dict: dicts.value[item.prop] ?? {} }
    }
    if (control === 'date' || control === 'datetime') {
        return { placeholder: t('common.pleaseSelect', { field: item.label }) }
    }
    const attr: InputAttr = { placeholder: t('common.pleaseEnter', { field: item.label }) }
    if (item.length && (control === 'string' || control === 'textarea')) {
        // 长度上限（后端写入前同样按 spec.length 校验，这里提前挡住输入）
        attr.maxlength = item.length
    }
    return attr
}

/** 引擎列的展示约定：后端只给 label/width/sortable（l2/handler.go 的 engineColumnMeta），
 *  created_at 是 TIMESTAMPTZ ⇒ 按宿主 datetime 渲染器显示 */
const ENGINE_COLUMN_RENDER: Record<string, TableColumn['render']> = {
    created_at: 'datetime',
}

/** 枚举列调色（与后端 tagColors 同一套；meta.custom 缺失时兜底，保证 tag 列恒带 custom） */
const TAG_PALETTE = ['success', 'info', 'warning', 'danger', 'primary']

const tagCustom = (item: L2ColumnMeta): Record<string, string> => {
    if (item.custom && Object.keys(item.custom).length) {
        return item.custom
    }
    const colors: Record<string, string> = {}
    Object.keys(dicts.value[item.prop] ?? {}).forEach((value, index) => {
        colors[value] = TAG_PALETTE[index % TAG_PALETTE.length]
    })
    return colors
}

const buildColumn = (item: L2ColumnMeta): TableColumn => {
    const column: TableColumn = {
        prop: item.prop,
        label: item.label,
        align: 'center',
        width: item.width || undefined,
        showOverflowTooltip: true,
        // 搜索：spec 声明了 search 才进公共搜索。留空会让 useTableManager 默认成 'eq'，
        // 而引擎的 buildCondition 只认声明过 search/quickSearch 的字段 ⇒ 必被拒的条件不该发出去
        operator: (item.operator as OperatorStr) || false,
        sortable: item.sortable ? 'custom' : undefined,
    }
    if (item.quickSearch) {
        column.quickSearch = true
    }
    // 日期类字段的范围搜索渲染成范围选择器（meta 不带 comSearchRender，按 spec 字段类型就地推断；
    // 不给推断时公共搜索会是两个纯文本框，值仍合法但难用）
    const control = formMeta.value.find((field) => field.prop === item.prop)?.type
    if ((control == 'date' || control == 'datetime') && (item.operator == 'BETWEEN' || item.operator == 'NOT BETWEEN')) {
        column.comSearchRender = control == 'date' ? 'date' : 'datetime'
    }
    const preset = ENGINE_COLUMN_RENDER[item.prop]
    if (preset) {
        column.render = preset
    }
    if (item.render == 'switch') {
        // 数值/布尔列：render=switch，不带 custom（AGENTS 状态列唯一写法）
        column.render = 'switch'
        // 行内开关本身就是一次 update：无 update 权限时置灰（只碰 disabled，不碰值域 —— 值域仍由
        // 单元格值的类型推导为 1/0，见 cellRenderer/switch.vue）
        column.customRenderAttr = { switch: () => ({ disabled: !auth('update') }) }
        return column
    }
    if (item.render == 'tag') {
        // 字符串枚举列：render=tag + custom + dict（AGENTS 状态列唯一写法）
        column.render = 'tag'
        column.custom = tagCustom(item)
        column.dict = dicts.value[item.prop] ?? {}
    }
    return column
}

const buildColumns = (meta: L2Meta): TableColumn[] => {
    const columns: TableColumn[] = [{ type: 'selection', align: 'center', operator: false }]
    for (const item of meta.columns ?? []) {
        columns.push(buildColumn(item))
    }
    columns.push({
        label: t('common.operate'),
        align: 'center',
        width: 120,
        render: 'buttons',
        // 拖动按钮只在 drag_sort 实体上渲染；三个按钮的显隐由 auth('sort'|'update'|'delete') 把守
        buttons: getDefaultOptButtons(dragSortEnabled.value ? ['sort', 'edit', 'delete'] : ['edit', 'delete']),
        operator: false,
    })
    return columns
}

const rules = ref<Partial<Record<string, FormItemRule[]>>>({})

const buildRules = (items: L2FormMeta[]) => {
    const next: Partial<Record<string, FormItemRule[]>> = {}
    for (const item of items) {
        if (item.required) {
            next[item.prop] = [buildValidatorRule({ name: 'required', title: item.label })]
        }
    }
    rules.value = next
}

/**
 * dict 收敛为 Record<值, 文案>：引擎在响应期已按 lang 转好，此处兼容 spec 原样的
 * 双语数组形态 —— 列配置必须拿到 Record，不能把 [{value,label}] 直接喂进去。
 */
const toDictRecord = (raw: L2DictShape | undefined, lang: string): Record<string, string> => {
    if (!raw) {
        return {}
    }
    if (!Array.isArray(raw)) {
        return raw
    }
    const english = lang.startsWith('en')
    const record: Record<string, string> = {}
    for (const item of raw) {
        const label = english ? item.label_en || item.label_zh : item.label_zh || item.label_en
        if (item.value === undefined || item.value === null || !label) {
            continue
        }
        record[String(item.value)] = label
    }
    return record
}

/** 应用 meta：列 / 表单 / dict / 校验规则一次对齐（指纹未变则跳过，保住用户调过的列显示） */
const applyMeta = (meta: L2Meta) => {
    const signature = JSON.stringify([meta.plugin, meta.entity, meta.title, meta.drag_sort, meta.columns, meta.form, meta.dicts])
    if (signature === metaSignature.value) {
        return false
    }
    metaSignature.value = signature
    metaTitle.value = meta.title ?? ''
    dragSortEnabled.value = meta.drag_sort === true

    const records: Record<string, Record<string, string>> = {}
    for (const item of meta.form ?? []) {
        const record = toDictRecord(meta.dicts?.[item.prop], l2Lang())
        if (Object.keys(record).length) {
            records[item.prop] = record
        }
    }
    dicts.value = records
    formMeta.value = meta.form ?? []
    buildRules(formMeta.value)

    tableManager.table.column = buildColumns(meta)
    // 列变了 ⇒ 公共搜索的字段表与表单要按新列重建（useTableManager 的既有事件）
    tableManager.handleEvent('reinit-com-search', { event: 'meta-ready' })
    return true
}

/**
 * URL query 预置搜索的重放（宿主惯例：`initCtx` 从 route.query 预置公共搜索表单与筛选条件）。
 *
 * 为什么必须在列就绪后补一次：本页的列来自首次 list 响应的 meta，而 `initCtx` 只能在 onMounted 的
 * 同步栈里调用（它内部用 `useRoute()` 的 inject，await 之后取不到实例）——那时 `table.column` 还是
 * 空数组，useTableManager 的 `initComSearch` / `setComSearchData` 对空列直接 return
 * （hooks/useTableManager.ts:510/554）⇒ 预置必然丢失。这里在 meta 落列之后按**同一口径**重放
 * （route.query 在 setup 里取，不依赖 inject 时机）。返回是否真的装配了筛选条件（调用方据此决定要不要重取数）。
 */
const replayQueryPreset = (): boolean => {
    if (tableManager.table.acceptQuery === false || !Object.keys(route.query).length || !tableManager.table.column.length) {
        return false
    }
    tableManager.setComSearchData(route.query)
    const groups: WhereGroup[] = []
    const com = tableManager.getComSearchData()
    if (com !== false) {
        groups.push(com)
    }
    const keywords = tableManager.table.filter!.quickSearchKeywords
    if (keywords) {
        const quick = tableManager.getQuickSearchData(keywords)
        if (quick !== false) {
            groups.push(quick)
        }
    }
    if (!groups.length) {
        return false
    }
    tableManager.setFilterWheres(groups)
    return true
}

/** 编辑回填：date/datetime 按选择器的 value-format 归一（element-plus 用 dayjs(值, format) 解析，
 *  ISO 串与 'YYYY-MM-DD HH:mm:ss' 不匹配 ⇒ 选择器会显示空值） */
const normalizeFormDates = (items: AnyObj) => {
    for (const item of formMeta.value) {
        const value = items[item.prop]
        if (value === undefined || value === null || value === '') {
            continue
        }
        if (item.type != 'date' && item.type != 'datetime') {
            continue
        }
        const parsed = dayjs(String(value))
        if (!parsed.isValid()) {
            continue
        }
        items[item.prop] = parsed.format(item.type == 'date' ? 'YYYY-MM-DD' : 'YYYY-MM-DD HH:mm:ss')
    }
}

/** 列表失败分类：404 = 未安装/已停用 ⇒ 空态（list 已关掉请求层的网络提示）；
 *  其余 HTTP 失败由本页提示；HTTP 200 + code≠0 的业务失败（如无权限）由请求层统一提示，这里不动 */
const handleListError = (err: unknown) => {
    const status = (err as { response?: { status?: number } } | undefined)?.response?.status
    if (status === 404) {
        unavailable.value = true
        return
    }
    if (status) {
        ElMessage.error(t('plugin.render.loadFailed'))
    }
}

// list 的响应同时带渲染元数据（「一次请求带元数据 + 数据」）：在这里建列，页面不额外请求
tableManager.opts.after!.getData = ({ res, err }: { res?: ApiResponse; err?: unknown }) => {
    if (err) {
        handleListError(err)
        return
    }
    const payload = res?.data?.data as L2ListData | undefined
    if (payload?.meta) {
        applyMeta(payload.meta)
    }
}

tableManager.opts.after!.getEditData = () => {
    if (tableManager.form.items) {
        normalizeFormDates(tableManager.form.items)
    }
}

// ---------- 取数主流程 ----------

/**
 * 取数：list（首帧同时拿到列/表单元数据）→ drag_sort 实体再按 weigh 序取一次 → 挂
 * Sortable。每次进入都重跑（keepalive=0）；空态的重试按钮走同一入口。
 * 重入保护：重试按钮连点会让 initDragSort 对同一 tbody 挂出多个 Sortable（一次拖动发两次 /sort）。
 *
 * 本函数**只做取数**：initCtx() 留在 onMounted 的同步栈里，不在本函数内调用（理由见 onMounted）。
 */
let loading = false
const load = async () => {
    if (!coords || loading) {
        return
    }
    loading = true
    try {
        if (unavailable.value) {
            unavailable.value = false
            await nextTick() // 表格区域重新挂载后才取得到 Table 组件实例（404 后重试的路径）
        }
        tableManager.table.ref = tableRef.value
        await tableManager.getData()
        if (unavailable.value) {
            return
        }
        // 列就绪后重放 URL query 的预置搜索（首帧的 getData 发生在列装配之前 ⇒ 条件不在那一帧里）
        const presetApplied = replayQueryPreset()
        if (dragSortEnabled.value && tableManager.table.filter!.sort !== 'weigh') {
            // 与宿主 drag_sort 页同口径的展示序（views/admin/auth/rule/index.vue 的 weigh + desc）；
            // 静默重取：首帧必须先把 meta 拿到手，才知道该实体有没有 drag_sort
            tableManager.table.filter!.sort = 'weigh'
            tableManager.table.filter!.order = 'desc'
            await tableManager.getData()
        } else if (presetApplied) {
            // 预置条件变了但排序没变（非 drag_sort 实体）⇒ 也要按预置条件重取一次，
            // 否则界面显示的是无条件首帧、而筛选表单已被预置（两边不一致）
            await tableManager.getData()
        }
        await nextTick()
        if (dragSortEnabled.value) {
            tableManager.initDragSort()
        }
    } finally {
        loading = false
    }
}

onMounted(() => {
    if (!coords) {
        return
    }
    // initCtx 只在这个同步栈里调用：它内部 `const route = useRoute()`（hooks/useTableManager.ts:671）
    // 走的是 inject —— inject 只在 setup / 生命周期钩子的同步栈里有当前实例，放到 await 之后
    // （如空态的重试路径）会取到 undefined 并在 `route.fullPath` 上抛 TypeError（页面卡在空态、
    // 无提示）。取数其余调用都不依赖实例，故 load() 可在事件处理器（重试按钮）里安全调用。
    tableManager.table.ref = tableRef.value
    tableManager.initCtx()
    void load()
})
</script>

<style scoped lang="scss">
.plugin-render {
    .plugin-render-empty {
        padding: 40px 0;
    }

    .plugin-render-hint {
        margin-bottom: 12px;
        color: var(--el-text-color-secondary);
        font-size: 13px;
        line-height: 1.6;
    }
}
</style>
