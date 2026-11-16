<!--
  界面结构与组件用法移植自 BuildAdmin v2.3.9
  https://gitee.com/wonderful-code/buildadmin
  Apache-2.0，Copyright 妙码生花
  已修改：版本表列换成 catalog 的 version/source/size/sha256/signed/安装态；
  删除了系统版本、授权期、续费、订单与 BuildAdmin store/memoize 依赖。
-->
<template>
    <el-table :data="versions" class="w100" stripe>
        <el-table-column property="version" align="center" :label="t('plugin.source.catalog.version.version')" width="110" />
        <el-table-column align="center" :label="t('plugin.source.catalog.version.source')" min-width="130">
            <template #default="scope">
                <span>{{ scope.row.source_name }}</span>
                <el-tag v-if="scope.row.source_conflict" size="small" type="danger" class="ml6">{{
                    t('plugin.source.catalog.card.conflict')
                }}</el-tag>
            </template>
        </el-table-column>
        <el-table-column align="center" :label="t('plugin.source.catalog.version.size')" width="100">
            <template #default="scope">{{ formatBytes(scope.row.package_size) }}</template>
        </el-table-column>
        <el-table-column align="center" :label="t('plugin.source.catalog.version.sha256')" min-width="150">
            <template #default="scope">
                <span class="mono" :title="scope.row.package_sha256">{{ shortSha(scope.row.package_sha256) }}</span>
            </template>
        </el-table-column>
        <el-table-column align="center" :label="t('plugin.source.catalog.version.signed')" width="100">
            <template #default="scope">
                <el-tag size="small" :type="scope.row.package_signed ? 'success' : 'warning'">
                    {{ scope.row.package_signed ? t('plugin.source.catalog.version.signedYes') : t('plugin.source.catalog.version.signedNo') }}
                </el-tag>
            </template>
        </el-table-column>
        <el-table-column align="center" :label="t('plugin.source.catalog.version.action')" min-width="150">
            <template #default="scope">
                <span v-if="scope.row.installed_version" class="installed-text">
                    {{ t('plugin.source.catalog.card.installed', { version: scope.row.installed_version }) }}
                </span>
                <span v-else-if="!enabledSourceIds.includes(scope.row.source_id)" class="disabled-text">
                    {{ t('plugin.source.catalog.card.sourceDisabled') }}
                </span>
                <!-- 环境门禁（生产禁止安装）与来源停用是两回事，分开说：按钮置灰 + 各自的原因文案 -->
                <span v-else-if="!installAllowed" class="disabled-text">{{ t('plugin.envGate.forbid') }}</span>
                <el-button v-else-if="canInstall" type="primary" size="small" @click="onInstall(scope.row)">
                    {{ t('plugin.source.catalog.installNow') }}
                </el-button>
            </template>
        </el-table-column>
    </el-table>
</template>

<script setup lang="ts">
import { formatBytes, shortSha, type CatalogEntry } from '@/api/plugin/source'
import { useI18n } from 'vue-i18n'

interface Props {
    versions: CatalogEntry[]
    enabledSourceIds: number[]
    canInstall?: boolean
    /** 环境门禁（catalog/list 的 env.lifecycle_allowed）：生产环境不允许安装 */
    installAllowed?: boolean
}

withDefaults(defineProps<Props>(), {
    versions: () => [],
    enabledSourceIds: () => [],
    canInstall: true,
    installAllowed: true,
})

const emit = defineEmits<{
    install: [entry: CatalogEntry]
}>()

const onInstall = (row: unknown) => emit('install', row as CatalogEntry)

const { t } = useI18n()
</script>

<style scoped lang="scss">
.ml6 {
    margin-left: 6px;
}
.mono {
    font-family: var(--el-font-family-mono, monospace);
    word-break: break-all;
}
.installed-text {
    color: var(--el-color-success);
    font-size: 12px;
}
.disabled-text {
    color: var(--el-text-color-secondary);
    font-size: 12px;
}
</style>
