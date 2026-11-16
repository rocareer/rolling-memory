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
import { useI18n } from 'vue-i18n'
import DialogForm from './dialogForm.vue'

defineOptions({
    name: 'plugin/kb/tag_rule',
})

const { t } = useI18n()
const optButtons = getDefaultOptButtons(['edit', 'delete'])

const tableManager = useTableManager({
    api: new TableManagerAPI('/admin/plugin/kb/tag_rule/'),
    table: {
        column: [
            { type: 'selection', align: 'center', operator: false },
            { label: 'ID', prop: 'id', align: 'center', operator: 'BETWEEN', width: 70 },
            {
                label: t('plugin.kb.tag.rule.libraryId'),
                prop: 'library_id',
                align: 'center',
                operator: 'eq',
                width: 100,
                // 表列只存 id；库名 → id 的取值走远程下拉（列表接口 = plugin/kb/library/list）
                comSearchRender: 'remoteSelect',
                comSearchRemote: { remoteURL: '/admin/plugin/kb/library/list', field: 'name', pk: 'id' },
            },
            { label: t('plugin.kb.tag.rule.name'), prop: 'name', align: 'center', operator: 'ILIKE', quickSearch: true, showOverflowTooltip: true },
            {
                label: t('plugin.kb.tag.rule.enabled'),
                prop: 'enabled',
                align: 'center',
                operator: false,
                render: 'switch',
                width: 80,
                sortable: 'custom',
            },
            {
                label: t('plugin.kb.tag.rule.matchType'),
                prop: 'match_type',
                align: 'center',
                operator: 'eq',
                render: 'tag',
                comSearchRender: 'select',
                custom: { path_glob: 'primary', filename_regex: 'info', suffix: 'warning', meta_eq: 'success' },
                dict: {
                    path_glob: t('plugin.kb.tag.rule.matchType path_glob'),
                    filename_regex: t('plugin.kb.tag.rule.matchType filename_regex'),
                    suffix: t('plugin.kb.tag.rule.matchType suffix'),
                    meta_eq: t('plugin.kb.tag.rule.matchType meta_eq'),
                },
            },
            {
                label: t('plugin.kb.tag.rule.pattern'),
                prop: 'pattern',
                align: 'center',
                operator: 'ILIKE',
                quickSearch: true,
                showOverflowTooltip: true,
            },
            {
                label: t('plugin.kb.tag.rule.tagId'),
                prop: 'tag_id',
                align: 'center',
                operator: 'eq',
                width: 100,
                // 表列只存 id；标签名 → id 的取值走远程下拉（列表接口 = plugin/kb/tag/list）
                comSearchRender: 'remoteSelect',
                comSearchRemote: { remoteURL: '/admin/plugin/kb/tag/list', field: 'name', pk: 'id' },
            },
            { label: t('plugin.kb.tag.rule.priority'), prop: 'priority', align: 'center', operator: 'BETWEEN', sortable: 'custom' },
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
            { label: t('common.operate'), align: 'center', width: 100, render: 'buttons', buttons: optButtons, operator: false },
        ],
        filter: {
            sort: 'id',
            order: 'desc',
        },
        dblClickNotEditColumn: ['enabled'],
    },
    form: {
        defaultItems: {
            enabled: 1,
        },
    },
})

tableManager.initCtx()
tableManager.getData()
</script>

<style scoped lang="scss"></style>
