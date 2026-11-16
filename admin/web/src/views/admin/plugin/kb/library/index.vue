<!-- plugin: kb v1.0.0 -->
<template>
    <div class="default-main">
        <TableHeader
            :manager="tableManager"
            v-model:com-search="tableManager.comSearch"
            :buttons="['refresh', 'add', 'edit', 'delete', 'comSearch', 'quickSearch', 'columnDisplay']"
        />
        <Table :manager="tableManager" />

        <DialogForm :manager="tableManager" v-model:form-items="tableManager.form.items!" />

        <!-- 入库对话框（文档导入：文本或文件；节点 plugin/kb/document/import 决定按钮显隐） -->
        <DialogImport v-model="importVisible" :library="importLibrary" @success="tableManager.getData()" />

        <!-- 授权面板（库详情抽屉：库信息 + 授权列表 + 授/撤权，plugin/kb/grant/* 节点显隐） -->
        <GrantPanel v-model="grantVisible" :library="grantLibrary" />
    </div>
</template>

<script setup lang="ts">
import type { KbLibraryRow } from '../api'
import { TableManagerAPI } from '@/api/table'
import TableHeader from '@/components/table/header/index.vue'
import { getDefaultOptButtons } from '@/components/table/index'
import Table from '@/components/table/index.vue'
import { useTableManager } from '@/hooks/useTableManager'
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { auth, getCurrentRoutePath } from '@/utils/common'
import { ElMessageBox } from 'element-plus'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import DialogForm from './dialogForm.vue'
import DialogImport from './dialogImport.vue'
import GrantPanel from './grantPanel.vue'

defineOptions({
    name: 'plugin/kb/library',
})

const { t } = useI18n()

// 页面鉴权：节点叶子名 = 宿主后台前缀 + 节点 name 全名（utils/router.ts 的 assembleAuthNode：
// 叶子条目 = 前缀 + 节点 name 全名，前缀取 adminBaseRoute.path，不硬编码 /admin）。
// 本页的节点（plugin/kb/library/*、plugin/kb/grant/*、plugin/kb/document/* …）都挂在库管理菜单下，
// authNode 的键 = 本页路由 path，故这里统一用「前缀 + plugin/kb/ + 节点路径」取值。
const pagePath = getCurrentRoutePath()
const canKb = (node: string) => auth({ name: pagePath, subNodeName: `${adminBaseRoutePath}/plugin/kb/${node}` })

// 入库对话框（库详情：当前行即目标库，名称 + 文本 + 可选文件）
const importVisible = ref(false)
const importLibrary = ref<KbLibraryRow | null>(null)
const openImport = (row: TableRow) => {
    importLibrary.value = row as unknown as KbLibraryRow
    importVisible.value = true
}

// 授权面板（库详情抽屉）开合与目标库
const grantVisible = ref(false)
const grantLibrary = ref<KbLibraryRow | null>(null)
const openGrant = (row: TableRow) => {
    grantLibrary.value = row as unknown as KbLibraryRow
    grantVisible.value = true
}

// 状态开关行内即点即改，切换前确认后果（禁用库对一切读路径不可见）；取消则值不变、不发请求
// （渲染器 switch.vue 把 customRenderAttr.switch 透传给 el-switch）
const confirmStatusChange = () =>
    ElMessageBox.confirm(t('plugin.kb.library.statusConfirm'), t('common.reminder'), {
        type: 'warning',
        confirmButtonText: t('common.confirm'),
        cancelButtonText: t('common.cancel'),
    })
        .then(() => true)
        .catch(() => false)

const optButtons: OptButton[] = [
    {
        render: 'tip',
        name: 'grant',
        title: t('plugin.kb.library.grantPanel'),
        type: 'info',
        icon: 'lucide-shield-check',
        display: () => canKb('grant/read'),
        click: (row) => openGrant(row),
    },
    {
        render: 'tip',
        name: 'import',
        title: t('plugin.kb.library.importDoc'),
        type: 'primary',
        icon: 'lucide-file-up',
        display: () => canKb('document/import'),
        click: (row) => openImport(row),
    },
    ...getDefaultOptButtons(['edit', 'delete']),
]

const tableManager = useTableManager({
    api: new TableManagerAPI('/admin/plugin/kb/library/'),
    // 表头与表行按钮鉴权：create / update / delete → plugin/kb/library/*
    auth: (node: string) => canKb(`library/${node}`),
    table: {
        column: [
            { type: 'selection', align: 'center', operator: false },
            { label: 'ID', prop: 'id', align: 'center', operator: 'BETWEEN', width: 70 },
            { label: t('plugin.kb.library.name'), prop: 'name', align: 'left', width: 220, operator: 'ILIKE', quickSearch: true },
            {
                label: t('plugin.kb.library.ownerKind'),
                prop: 'owner_kind',
                align: 'center',
                width: 110,
                operator: 'eq',
                render: 'tag',
                custom: { system: 'info', user: 'primary' },
                dict: { system: t('plugin.kb.library.ownerKindSystem'), user: t('plugin.kb.library.ownerKindUser') },
            },
            {
                label: t('plugin.kb.library.ownerId'),
                prop: 'owner_id',
                align: 'center',
                width: 140,
                showOverflowTooltip: true,
                operator: 'eq',
                // 展示属主用户名（列表接口随带 owner_username）；属主主体面不可用/查无 ⇒ 回落裸 id。
                // system 属主 owner_id 恒 0。
                formatter: (row: TableRow) => (row.owner_kind == 'user' ? row.owner_username || `#${row.owner_id}` : '—'),
            },
            {
                label: t('plugin.kb.library.description'),
                prop: 'description',
                align: 'left',
                minWidth: 220,
                showOverflowTooltip: true,
                operator: false,
            },
            {
                label: t('common.status'),
                prop: 'status',
                align: 'center',
                width: 100,
                render: 'switch',
                operator: 'eq',
                // 禁用库对一切读路径不可见：切换前确认后果
                customRenderAttr: {
                    switch: {
                        beforeChange: confirmStatusChange,
                    },
                },
            },
            {
                label: t('common.updatedAt'),
                prop: 'updated_at',
                align: 'center',
                render: 'datetime',
                comSearchRender: 'datetime',
                sortable: 'custom',
                operator: 'BETWEEN',
                width: 160,
            },
            {
                label: t('common.createdAt'),
                prop: 'created_at',
                align: 'center',
                render: 'datetime',
                comSearchRender: 'datetime',
                sortable: 'custom',
                operator: 'BETWEEN',
                width: 160,
            },
            { label: t('common.operate'), align: 'center', width: 220, render: 'buttons', buttons: optButtons, operator: false },
        ],
        filter: {
            order: 'desc',
            sort: 'updated_at',
        },
        dblClickNotEditColumn: ['status'],
    },
    form: {
        defaultItems: {
            owner_kind: 'system',
            owner_id: 0,
            status: 1,
            chunk_config: '',
        },
    },
})

// 提交前：规整属主与切片配置（system 属主恒 owner_id=0；空配置传 null = 未配置）
tableManager.opts.before!.submitForm = ({ items }: { items: AnyObj }) => {
    if (items.owner_kind == 'system') {
        items.owner_id = 0
    }
    const chunkConfig = typeof items.chunk_config == 'string' ? items.chunk_config.trim() : ''
    items.chunk_config = chunkConfig ? chunkConfig : null
}

tableManager.initCtx()
tableManager.getData()
</script>

<style scoped lang="scss"></style>
