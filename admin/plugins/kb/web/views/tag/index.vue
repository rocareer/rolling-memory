<!-- plugin: kb v1.0.0 -->
<template>
    <div class="default-main">
        <TableHeader
            :manager="tableManager"
            v-model:com-search="tableManager.comSearch"
            :buttons="['refresh', 'add', 'edit', 'delete', 'comSearch', 'quickSearch', 'columnDisplay']"
        />
        <Table ref="tableRef" :manager="tableManager" />

        <DialogForm :manager="tableManager" v-model:form-items="tableManager.form.items!" />
    </div>
</template>

<script setup lang="ts">
import { TableManagerAPI } from '@/api/table'
import TableHeader from '@/components/table/header/index.vue'
import { getDefaultOptButtons } from '@/components/table/index'
import Table from '@/components/table/index.vue'
import { useTableManager } from '@/hooks/useTableManager'
import { onMounted, useTemplateRef } from 'vue'
import { useI18n } from 'vue-i18n'
import DialogForm from './dialogForm.vue'

defineOptions({
    name: 'plugin/kb/tag',
})

const { t } = useI18n()
const optButtons = getDefaultOptButtons(['sort', 'edit', 'delete'])
const tableRef = useTemplateRef('tableRef')

const tableManager = useTableManager({
    api: new TableManagerAPI('/admin/plugin/kb/tag/'),
    table: {
        column: [
            { type: 'selection', align: 'center', operator: false },
            { label: 'ID', prop: 'id', align: 'center', operator: 'BETWEEN', width: 70 },
            {
                label: t('plugin.kb.tag.groupId'),
                prop: 'group_id',
                align: 'center',
                operator: 'eq',
                width: 100,
                // 表列只存 id；组名 → id 的取值走远程下拉（列表接口 = plugin/kb/tag_group/list）
                comSearchRender: 'remoteSelect',
                comSearchRemote: { remoteURL: '/admin/plugin/kb/tag_group/list', field: 'name', pk: 'id' },
            },
            { label: t('plugin.kb.tag.name'), prop: 'name', align: 'center', operator: 'ILIKE', quickSearch: true, showOverflowTooltip: true },
            { label: t('common.weigh'), prop: 'weigh', align: 'center', width: 80, sortable: 'custom' },
            {
                label: t('common.status'),
                prop: 'status',
                align: 'center',
                operator: false,
                render: 'switch',
                width: 80,
                sortable: 'custom',
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
            { label: t('common.operate'), align: 'center', width: 140, render: 'buttons', buttons: optButtons, operator: false },
        ],
        filter: {
            sort: 'id',
            order: 'desc',
        },
        dblClickNotEditColumn: ['status'],
    },
    form: {
        defaultItems: {
            weigh: 0,
            status: 1,
        },
    },
})

onMounted(() => {
    tableManager.table.ref = tableRef.value
    tableManager.initCtx()
    tableManager.getData()?.then(() => {
        tableManager.initDragSort()
    })
})
</script>

<style scoped lang="scss"></style>
