<!--
  仓库来源配置页（插件清单方案）。
  标准 CRUD 直接复用宿主通用表格：TableHeader + Table + useTableManager + TableManagerAPI。
  新增/编辑共用一个内联 el-dialog（不新建 dialogForm.vue）；每行可测试清单、启停、删除。
  每个来源的编辑弹窗可由 ?source=<name> 直达：同一页面、同一表单，不新增路由或组件。
-->
<template>
    <div class="default-main plugin-source-index">
        <el-alert class="source-alert" type="info" :closable="false" show-icon :title="t('plugin.source.tips.format')" />

        <TableHeader
            :manager="tableManager"
            v-model:com-search="tableManager.comSearch"
            :buttons="['refresh', 'comSearch', 'quickSearch', 'columnDisplay']"
            :quick-search-placeholder="t('plugin.source.searchPlaceholder')"
        >
            <el-button v-if="can('create')" v-blur class="btns-ml-12" type="primary" @click="openCreate">
                <Icon size="16" :stroke-width="3" name="lucide-plus" color="#ffffff" />
                <span class="table-header-operate-text">{{ t('plugin.source.actions.create') }}</span>
            </el-button>
        </TableHeader>

        <Table :manager="tableManager" :empty-text="t('plugin.source.empty')" />

        <!-- 新增 / 编辑：同一个表单、同一组字段；type 只切换提示与安全开关可见性 -->
        <el-dialog
            v-model="dialogVisible"
            :title="editing ? t('plugin.source.dialog.editTitle') : t('plugin.source.dialog.createTitle')"
            width="min(680px, 96vw)"
            :close-on-click-modal="false"
            :destroy-on-close="true"
        >
            <el-form ref="formRef" :model="form" :rules="rules" label-width="160px">
                <el-form-item :label="t('plugin.source.form.name')" prop="name">
                    <el-input v-model="form.name" :disabled="editing" :placeholder="t('plugin.source.form.namePlaceholder')" />
                    <div class="form-tip">{{ t('plugin.source.form.nameTip') }}</div>
                </el-form-item>
                <el-form-item :label="t('plugin.source.form.title')" prop="title">
                    <el-input v-model="form.title" :placeholder="t('plugin.source.form.titlePlaceholder')" />
                </el-form-item>
                <el-form-item :label="t('plugin.source.form.type')" prop="type">
                    <el-select v-model="form.type" class="w100">
                        <el-option :label="t('plugin.source.type.local')" value="local" />
                        <el-option :label="t('plugin.source.type.http')" value="http" />
                        <el-option :label="t('plugin.source.type.git')" value="git" />
                    </el-select>
                </el-form-item>
                <el-form-item :label="t('plugin.source.form.location')" prop="location">
                    <el-input v-model="form.location" :placeholder="locationPlaceholder" />
                    <div class="form-tip">
                        {{ locationTip }}
                    </div>
                </el-form-item>
                <el-form-item :label="t('plugin.source.form.enabled')">
                    <el-switch v-model="form.enabled" :active-value="1" :inactive-value="0" />
                </el-form-item>
                <el-form-item v-if="form.type !== 'local'" :label="t('plugin.source.form.allowInsecureHttp')">
                    <el-switch v-model="form.allow_insecure_http" :active-value="1" :inactive-value="0" />
                    <div class="form-tip">{{ t('plugin.source.form.allowInsecureHttpTip') }}</div>
                </el-form-item>
                <el-form-item v-if="form.type !== 'local'" :label="t('plugin.source.form.allowPrivateHosts')">
                    <el-switch v-model="form.allow_private_hosts" :active-value="1" :inactive-value="0" />
                    <div class="form-tip">{{ t('plugin.source.form.allowPrivateHostsTip') }}</div>
                </el-form-item>
                <el-form-item v-if="form.type === 'local'" :label="t('plugin.source.form.allowAbsolutePaths')">
                    <el-switch v-model="form.allow_absolute_paths" :active-value="1" :inactive-value="0" />
                    <div class="form-tip">{{ t('plugin.source.form.allowAbsolutePathsTip') }}</div>
                </el-form-item>
                <el-form-item :label="t('plugin.source.form.weigh')">
                    <el-input-number v-model="form.weigh" :min="0" :max="999999" controls-position="right" />
                </el-form-item>
                <el-form-item :label="t('plugin.source.form.remark')">
                    <el-input v-model="form.remark" type="textarea" :rows="3" maxlength="255" show-word-limit />
                </el-form-item>
            </el-form>
            <template #footer>
                <div class="dialog-footer">
                    <el-button v-blur :loading="testing" @click="testForm">{{ t('plugin.source.actions.test') }}</el-button>
                    <div>
                        <el-button @click="dialogVisible = false">{{ t('common.cancel') }}</el-button>
                        <el-button type="primary" :loading="saving" @click="save">{{ t('common.save') }}</el-button>
                    </div>
                </div>
            </template>
        </el-dialog>

        <!-- 只读测试结果：成功展示 schema/条目数/摘要；失败按 error_code 区分「来源不可达 / 清单格式非法」 -->
        <el-dialog v-model="testVisible" :title="t('plugin.source.test.title')" width="min(640px, 96vw)">
            <div v-loading="testing" class="test-result">
                <template v-if="testResult">
                    <!-- 能取到 testResult 就一定是成功回执：后端失败恒 code!=0（走 testError 分支），不再按 ok 分叉 -->
                    <el-alert type="success" :closable="false" show-icon :title="t('plugin.source.test.ok', { entries: testResult.entries })" />
                    <el-descriptions :column="1" border size="small" class="test-descriptions">
                        <el-descriptions-item :label="t('plugin.source.test.schema')">{{ testResult.schema || '-' }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.source.test.entries')">{{ testResult.entries }}</el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.source.test.sha256')">
                            <span class="mono">{{ testResult.catalog_sha256 || '-' }}</span>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.source.test.generatedAt')">{{
                            formatTime(testResult.generated_at)
                        }}</el-descriptions-item>
                        <el-descriptions-item v-if="testErrors.length" :label="t('plugin.source.test.errors')">
                            <div v-for="(item, idx) in testErrors" :key="idx" class="test-error-line">{{ item }}</div>
                        </el-descriptions-item>
                    </el-descriptions>
                </template>
                <template v-else-if="testError">
                    <el-alert type="error" :closable="false" show-icon :title="errorText.errorMessage" />
                    <div class="test-hint">{{ errorText.errorHint }}</div>
                    <div v-if="testError.message" class="test-raw">{{ testError.message }}</div>
                    <el-descriptions v-if="testErrors.length" :column="1" border size="small" class="test-descriptions">
                        <el-descriptions-item :label="t('plugin.source.test.errors')">
                            <div v-for="(item, idx) in testErrors" :key="idx" class="test-error-line">{{ item }}</div>
                        </el-descriptions-item>
                    </el-descriptions>
                </template>
            </div>
            <template #footer>
                <el-button @click="testVisible = false">{{ t('common.close') }}</el-button>
                <el-button v-if="testResult?.ok && can('read')" type="primary" @click="goCatalog">{{ t('plugin.source.test.goCatalog') }}</el-button>
            </template>
        </el-dialog>

        <!-- 频道门禁：生产环境整频道封闭（弹窗不可关闭，唯一出口是「返回仪表盘」） -->
        <ChannelGate :env="env" />
    </div>
</template>

<script setup lang="ts">
import {
    catalogErrorKey,
    errorInfo,
    testSource,
    type PluginSourcePayload,
    type PluginSourceRow,
    type ErrorInfo,
    type SourceTestData,
    type SourceTestPayload,
} from '@/api/plugin/source'
import { TableManagerAPI } from '@/api/table'
import TableHeader from '@/components/table/header/index.vue'
import Table from '@/components/table/index.vue'
import { useTableManager } from '@/hooks/useTableManager'
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { auth, getCurrentRoutePath } from '@/utils/common'
import { ElMessage, dayjs, type FormInstance, type FormItemRule, type TagProps } from 'element-plus'
import { computed, onMounted, reactive, ref, useTemplateRef } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import type { PluginEnv } from '@/api/plugin'
import ChannelGate from '../channelGate.vue'

// keepalive 按组件名匹配；菜单行 name=plugin/source（数据库迁移）
defineOptions({
    name: 'plugin/source',
})

const { t, te } = useI18n()
const route = useRoute()
const router = useRouter()
const formRef = useTemplateRef<FormInstance>('formRef')

// 页面鉴权：节点名 = route.path + 节点末段；与数据库迁移的 5 个节点逐字对应
const pagePath = getCurrentRoutePath()
const can = (node: string) => auth({ name: pagePath, subNodeName: `${pagePath}/${node}` })

type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'
const typeDict: Record<string, string> = {
    local: t('plugin.source.type.local'),
    http: t('plugin.source.type.http'),
    git: t('plugin.source.type.git'),
}
const typeCustom: Record<string, TagType> = { local: 'info', http: 'primary', git: 'warning' }
const enabledDict: Record<string, string> = {
    1: t('plugin.source.enabled.on'),
    0: t('plugin.source.enabled.off'),
}
const enabledCustom: Record<string, TagType> = { 1: 'success', 0: 'info' }
const syncDict: Record<string, string> = {
    ok: t('plugin.source.syncStatus.ok'),
    error: t('plugin.source.syncStatus.error'),
    never: t('plugin.source.syncStatus.never'),
}
const syncCustom: Record<string, TagType> = { ok: 'success', error: 'danger', never: 'info' }

const formatTime = (value?: string | null) => (value ? dayjs(value).format('YYYY-MM-DD HH:mm:ss') : '-')
const tagTitle = (tip: string): Partial<TagProps> => (tip ? ({ title: tip } as Partial<TagProps>) : {})

// 表格普通列
const tableManager = useTableManager({
    api: new TableManagerAPI('/admin/plugin/source/'),
    table: {
        column: [
            {
                label: t('plugin.source.columns.name'),
                prop: 'name',
                align: 'left',
                width: 160,
                operator: 'ILIKE',
                quickSearch: true,
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.source.columns.title'),
                prop: 'title',
                align: 'left',
                minWidth: 140,
                operator: 'ILIKE',
                quickSearch: true,
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.source.columns.type'),
                prop: 'type',
                align: 'center',
                width: 90,
                render: 'tag',
                custom: typeCustom,
                dict: typeDict,
            },
            {
                label: t('plugin.source.columns.location'),
                prop: 'location',
                align: 'left',
                minWidth: 220,
                operator: 'ILIKE',
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.source.columns.enabled'),
                prop: 'enabled',
                align: 'center',
                width: 90,
                render: 'tag',
                custom: enabledCustom,
                dict: enabledDict,
            },
            {
                label: t('plugin.source.columns.syncStatus'),
                prop: 'last_sync_status',
                align: 'center',
                width: 110,
                operator: false,
                render: 'tag',
                formatter: (row: TableRow) => row.last_sync_status || 'never',
                custom: syncCustom,
                dict: syncDict,
                customRenderAttr: {
                    tag: (ctx: TableContextData) => tagTitle(String(ctx.row?.last_sync_error ?? '')),
                },
            },
            {
                label: t('plugin.source.columns.syncedAt'),
                prop: 'last_synced_at',
                align: 'center',
                width: 160,
                render: 'datetime',
                operator: false,
            },
            { label: t('common.weigh'), prop: 'weigh', align: 'center', width: 90, operator: 'BETWEEN', sortable: 'custom' },
            {
                label: t('common.updatedAt'),
                prop: 'updated_at',
                align: 'center',
                width: 160,
                render: 'datetime',
                operator: false,
                sortable: 'custom',
            },
            {
                label: t('common.operate'),
                align: 'center',
                width: 190,
                render: 'buttons',
                operator: false,
                buttons: [
                    {
                        render: 'tip',
                        name: 'edit',
                        title: t('common.edit'),
                        type: 'primary',
                        icon: 'lucide-pencil',
                        display: () => can('update'),
                        click: (row) => openEdit(row as unknown as PluginSourceRow),
                    },
                    {
                        render: 'tip',
                        name: 'test',
                        title: t('plugin.source.actions.test'),
                        type: 'info',
                        icon: 'lucide-flask-conical',
                        display: () => can('test'),
                        click: (row) => testRow(row as unknown as PluginSourceRow),
                    },
                    {
                        render: 'confirm',
                        name: 'toggle',
                        title: t('plugin.source.actions.toggle'),
                        type: 'warning',
                        icon: 'lucide-power',
                        display: () => can('update'),
                        popconfirm: (ctx) => ({
                            title: ctx.row?.enabled
                                ? t('plugin.source.actions.disableConfirm', { name: ctx.row?.name ?? '' })
                                : t('plugin.source.actions.enableConfirm', { name: ctx.row?.name ?? '' }),
                        }),
                        click: (row) => toggleEnabled(row as unknown as PluginSourceRow),
                    },
                    {
                        render: 'confirm',
                        name: 'delete',
                        title: t('common.delete'),
                        type: 'danger',
                        icon: 'lucide-trash',
                        display: () => can('delete'),
                        popconfirm: (ctx) => ({ title: t('plugin.source.actions.deleteConfirm', { name: ctx.row?.name ?? '' }) }),
                        click: (row) => removeSource(row as unknown as PluginSourceRow),
                    },
                ],
            },
        ],
        filter: {
            sort: 'weigh',
            order: 'desc',
        },
        dblClickNotEditColumn: ['all'],
    },
})

/* ---------------- 新增 / 编辑 ---------------- */
interface SourceForm extends PluginSourcePayload {
    id?: number
    name: string
}

const dialogVisible = ref(false)
const editing = ref(false)
const saving = ref(false)
const editingId = ref<number | null>(null)

const defaultForm = (): SourceForm => ({
    name: '',
    title: '',
    type: 'local',
    location: '',
    enabled: 1,
    allow_insecure_http: 0,
    allow_private_hosts: 0,
    allow_absolute_paths: 0,
    weigh: 0,
    remark: '',
})
const form = reactive<SourceForm>(defaultForm())

const locationPlaceholder = computed(() => {
    if (form.type === 'http') return t('plugin.source.form.locationHttpPlaceholder')
    if (form.type === 'git') return t('plugin.source.form.locationGitPlaceholder')
    return t('plugin.source.form.locationLocalPlaceholder')
})

const locationTip = computed(() => {
    if (form.type === 'http') return t('plugin.source.form.locationHttpTip')
    if (form.type === 'git') return t('plugin.source.form.locationGitTip')
    return t('plugin.source.form.locationLocalTip')
})

// git location 语法：<仓库URL>[#<ref>][!<仓内清单路径>]（与后端 ParseGitLocation 同源约束）
const GIT_LOCATION_PATTERN = /^https?:\/\/[^\s#!]+(#[A-Za-z0-9._\-\/]{1,200})?(![^\s]+)?$/i

const checkLocation = (_rule: unknown, value: string, callback: (error?: Error) => void) => {
    if (!value) {
        callback(new Error(t('plugin.source.form.rules.locationRequired')))
        return
    }
    if (form.type === 'http' && !/^https?:\/\/[^\s]+$/i.test(value)) {
        callback(new Error(t('plugin.source.form.rules.locationUrl')))
        return
    }
    if (form.type === 'git' && !GIT_LOCATION_PATTERN.test(value)) {
        callback(new Error(t('plugin.source.form.rules.locationGit')))
        return
    }
    callback()
}
const rules: Record<string, FormItemRule | FormItemRule[]> = {
    name: [
        { required: true, message: t('plugin.source.form.rules.nameRequired'), trigger: 'blur' },
        { pattern: /^[a-z][a-z0-9-]{1,31}$/, message: t('plugin.source.form.rules.namePattern'), trigger: 'blur' },
    ],
    title: { required: true, message: t('plugin.source.form.rules.titleRequired'), trigger: 'blur' },
    type: { required: true, message: t('plugin.source.form.rules.typeRequired'), trigger: 'change' },
    location: { validator: checkLocation, trigger: 'blur' },
}

const resetForm = (row?: PluginSourceRow) => {
    Object.assign(form, defaultForm())
    editingId.value = null
    if (row) {
        editing.value = true
        editingId.value = row.id
        Object.assign(form, {
            name: row.name,
            title: row.title,
            type: row.type,
            location: row.location,
            enabled: row.enabled,
            allow_insecure_http: row.allow_insecure_http,
            allow_private_hosts: row.allow_private_hosts,
            allow_absolute_paths: row.allow_absolute_paths,
            weigh: row.weigh,
            remark: row.remark,
        })
    } else {
        editing.value = false
    }
}

const openCreate = () => {
    resetForm()
    dialogVisible.value = true
}

const openEdit = (row: PluginSourceRow) => {
    resetForm(row)
    dialogVisible.value = true
}

const save = async () => {
    if (!formRef.value) return
    const valid = await formRef.value.validate().catch(() => false)
    if (!valid) return
    saving.value = true
    try {
        const payload: PluginSourcePayload = {
            title: form.title,
            type: form.type,
            location: form.location,
            enabled: form.enabled,
            allow_insecure_http: form.type === 'local' ? 0 : form.allow_insecure_http,
            allow_private_hosts: form.type === 'local' ? 0 : form.allow_private_hosts,
            allow_absolute_paths: form.type === 'local' ? form.allow_absolute_paths : 0,
            weigh: form.weigh,
            remark: form.remark,
        }
        if (!editingId.value) {
            payload.name = form.name
        }
        const res = editingId.value
            ? await tableManager.api.post('update', String(editingId.value), payload)
            : await tableManager.api.post('create', '', payload)
        // 改了 type/location/安全开关 ⇒ 后端清空该来源快照，必须重新同步才会回到清单页
        if ((res.data.data as { snapshot_cleared?: boolean } | undefined)?.snapshot_cleared) {
            ElMessage.warning(t('plugin.source.dialog.snapshotCleared'))
        }
        dialogVisible.value = false
        tableManager.getData()
    } catch {
        // 失败文案由请求层统一提示（后端 message 原样返回），此处保持对话框打开以便修正
    } finally {
        saving.value = false
    }
}

const toggleEnabled = async (row: PluginSourceRow) => {
    try {
        await tableManager.api.post('update', String(row.id), {
            title: row.title,
            type: row.type,
            location: row.location,
            enabled: row.enabled ? 0 : 1,
            allow_insecure_http: row.type === 'local' ? 0 : row.allow_insecure_http,
            allow_private_hosts: row.type === 'local' ? 0 : row.allow_private_hosts,
            allow_absolute_paths: row.type === 'local' ? row.allow_absolute_paths : 0,
            weigh: row.weigh,
            remark: row.remark,
        })
        tableManager.getData()
    } catch {
        // 同上：请求层已提示
    }
}

// 不用 tableManager.del：该 hook 的 delete 只写 .then，失败会留下未处理 Promise 拒绝
const removeSource = async (row: PluginSourceRow) => {
    try {
        await tableManager.api.delete([String(row.id)])
        tableManager.getData()
    } catch {
        // 请求层已提示；保持列表不变
    }
}

/* ---------------- 只读测试 ---------------- */
const testVisible = ref(false)
const testing = ref(false)
const testResult = ref<SourceTestData | null>(null)
const testError = ref<ErrorInfo | null>(null)

// 稳定错误码 → 本地化「错误 + 下一步」；未知码归一到 unknown，避免页面显示原始 key。
// 只认失败分支：source/test 失败恒走 httpx.Fail（HTTP 200 + code!=0，稳定码在 data.error_code），
// 请求层据此 reject（utils/request.ts:192）⇒ 能拿到 testResult 就一定是 ok=true（后端成功回执唯一分支）。
// 原先「code=0 且 ok=false ⇒ 硬编码 catalog_invalid」不可达，还会把任意未知原因误报成「清单格式非法」，故删除。
const errorCode = computed(() => catalogErrorKey(testError.value?.code, te))
const errorText = computed(() => ({
    errorMessage: t(`plugin.source.errors.${errorCode.value}.message`),
    errorHint: t(`plugin.source.errors.${errorCode.value}.hint`),
}))

const testErrors = computed(() => {
    // code!=0 时 request 直接 reject，明细在 errorInfo.errors（后端 data.errors）
    const errors = testResult.value?.errors ?? testError.value?.errors
    if (!errors) return []
    return Array.isArray(errors) ? errors : Object.values(errors).map(String)
})

const runTest = async (payload: SourceTestPayload) => {
    testResult.value = null
    testError.value = null
    testVisible.value = true
    testing.value = true
    try {
        const res = await testSource(payload)
        testResult.value = res.data.data
    } catch (err) {
        testError.value = errorInfo(err)
    } finally {
        testing.value = false
    }
}

const testForm = () => {
    runTest({
        type: form.type,
        location: form.location,
        allow_insecure_http: form.type === 'http' ? form.allow_insecure_http : 0,
        allow_private_hosts: form.type === 'http' ? form.allow_private_hosts : 0,
        allow_absolute_paths: form.type === 'local' ? form.allow_absolute_paths : 0,
    })
}

const testRow = (row: PluginSourceRow) => {
    runTest({
        type: row.type,
        location: row.location,
        allow_insecure_http: row.allow_insecure_http,
        allow_private_hosts: row.allow_private_hosts,
        allow_absolute_paths: row.allow_absolute_paths,
    })
}

const goCatalog = () => {
    // 清单页路由由菜单下发（数据库迁移）；未注册时直接 push 会落到空白区，先提示再中止
    if (!router.hasRoute('plugin/catalog')) {
        ElMessage.warning(t('plugin.source.routeMissing'))
        return
    }
    testVisible.value = false
    router.push({ path: `${adminBaseRoutePath}/plugin/catalog` })
}

/* ---------------- 初始化：支持 ?source=<name> 直达编辑 ---------------- */
// 环境判定（列表响应随带的 env 段，/admin/plugin/source/list）：生产环境整频道封闭（门禁弹窗）。
// 派生单点 = usePluginEnv；env 未知（旧后端/列表失败）时不判门禁——后端门禁才是权威。
// 钩子的失败分支只带 { err }（useTableManager 的 .catch）：没有 res 时什么都不读，否则钩子自己抛错，
// 会把 getData() 的 promise 变成拒绝、连带把下面 ?source= 深链处理整段跳过（同 render/index.vue 的口径）。
const env = ref<PluginEnv | null>(null)
tableManager.opts.after!.getData = ({ res }: { res?: ApiResponse }) => {
    env.value = (res?.data?.data as { env?: PluginEnv } | undefined)?.env ?? null
}

onMounted(async () => {
    tableManager.initCtx()
    await tableManager.getData()
    const name = typeof route.query.source === 'string' ? route.query.source : ''
    if (!name) return
    try {
        // 来源可能不在当前页；按 name 精确取一次，避免 ?source= 只在第一页生效
        const res = await tableManager.api.list({
            page: 1,
            limit: 50,
            wheres: [{ wheres: [{ field: 'name', value: name, operator: 'eq' }], or: false }],
        })
        const row = (res.data.data.list as PluginSourceRow[]).find((item) => item.name === name)
        if (row) {
            openEdit(row)
        }
    } catch {
        // 请求层已提示；不打开弹窗
    }
})
</script>

<style scoped lang="scss">
.plugin-source-index {
    .source-alert {
        margin-bottom: 10px;
    }

    .form-tip {
        width: 100%;
        margin-top: 2px;
        font-size: 12px;
        line-height: 18px;
        color: var(--el-text-color-secondary);
    }

    .dialog-footer {
        display: flex;
        align-items: center;
        justify-content: space-between;
    }

    .test-result {
        min-height: 120px;
    }

    .test-hint {
        margin: 10px 0;
        font-size: 13px;
        color: var(--el-text-color-regular);
    }

    .test-raw {
        word-break: break-all;
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .test-descriptions {
        margin-top: 12px;
    }

    .test-error-line {
        line-height: 20px;
        word-break: break-all;
    }

    .mono {
        font-family: var(--el-font-family-mono, monospace);
        word-break: break-all;
    }
}
</style>
