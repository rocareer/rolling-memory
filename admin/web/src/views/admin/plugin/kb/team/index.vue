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
    </div>
</template>

<script setup lang="ts">
import { TableManagerAPI } from '@/api/table'
import TableHeader from '@/components/table/header/index.vue'
import { getDefaultOptButtons } from '@/components/table/index'
import Table from '@/components/table/index.vue'
import { useTableManager } from '@/hooks/useTableManager'
import { ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import DialogForm from './dialogForm.vue'

defineOptions({
    name: 'plugin/kb/team',
})

const { t } = useI18n()
const optButtons = getDefaultOptButtons(['edit', 'delete'])

// 状态开关行内即点即改，切换前确认后果（禁用团队不参与读视图判定）；取消则值不变、不发请求
// （渲染器 switch.vue 把 customRenderAttr.switch 透传给 el-switch）
const confirmStatusChange = () =>
    ElMessageBox.confirm(t('plugin.kb.team.statusConfirm'), t('common.reminder'), {
        type: 'warning',
        confirmButtonText: t('common.confirm'),
        cancelButtonText: t('common.cancel'),
    })
        .then(() => true)
        .catch(() => false)

const tableManager = useTableManager({
    api: new TableManagerAPI('/admin/plugin/kb/team/'),
    table: {
        column: [
            { type: 'selection', align: 'center', operator: false },
            { label: 'ID', prop: 'id', align: 'center', operator: 'BETWEEN', width: 70 },
            {
                label: t('plugin.kb.team.groupName'),
                prop: 'group_name',
                align: 'center',
                operator: 'ILIKE',
                quickSearch: true,
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.kb.team.displayName'),
                prop: 'display_name',
                align: 'center',
                operator: 'ILIKE',
                quickSearch: true,
                showOverflowTooltip: true,
            },
            {
                label: t('common.status'),
                prop: 'status',
                align: 'center',
                operator: false,
                render: 'switch',
                width: 100,
                sortable: 'custom',
                // 禁用团队不参与读视图判定：切换前确认后果
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
                operator: 'BETWEEN',
                render: 'datetime',
                width: 160,
                sortable: 'custom',
                comSearchRender: 'datetime',
            },
            { label: t('common.operate'), align: 'center', width: 120, render: 'buttons', buttons: optButtons, operator: false },
        ],
        filter: {
            sort: 'id',
            order: 'desc',
        },
        dblClickNotEditColumn: ['status'],
    },
    form: {
        defaultItems: {
            status: 1,
        },
    },
})

tableManager.initCtx()
tableManager.getData()
</script>

<style scoped lang="scss"></style>
