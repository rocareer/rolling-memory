<!--
  重写件（不是上游逐字移植）：只沿用 BuildAdmin v2.3.9 结果页的「摘要 + 提示 + 关闭/下一步」结构。
  https://gitee.com/wonderful-code/buildadmin
  Apache-2.0，Copyright 妙码生花
  模板与样式为本项目自写（el-result + el-descriptions 展示 catalog 安装回执 effect/checksum/signed_by）；
  无依赖终端、重新构建、重启 Vite、积分与 BuildAdmin API/store 依赖。
-->
<template>
    <div class="install-done">
        <el-result icon="success" :title="t('plugin.source.catalog.done.title')" :sub-title="t('plugin.source.catalog.done.subtitle')">
            <template #extra>
                <el-descriptions :column="1" border size="small" class="done-descriptions">
                    <el-descriptions-item :label="t('plugin.source.catalog.done.name')">{{ result.name }}</el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.source.catalog.done.version')">{{ result.version }}</el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.source.catalog.done.status')">{{ result.status }}</el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.source.catalog.done.effect')">{{ result.effect }}</el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.source.catalog.done.checksum')">
                        <span class="mono">{{ result.checksum || '-' }}</span>
                    </el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.source.catalog.done.signedBy')">{{ result.signed_by || '-' }}</el-descriptions-item>
                    <el-descriptions-item :label="t('plugin.source.catalog.done.catalogSha256')">
                        <span class="mono">{{ result.catalog_sha256 || '-' }}</span>
                    </el-descriptions-item>
                </el-descriptions>

                <el-alert
                    v-if="result.effect === 'rebuild_required'"
                    class="done-alert"
                    type="warning"
                    :closable="false"
                    show-icon
                    :title="t('plugin.source.catalog.done.rebuildRequired')"
                />
                <div class="done-hint">{{ t('plugin.source.catalog.done.reconcile') }}</div>
                <div class="done-buttons">
                    <el-button @click="emit('close')">{{ t('common.close') }}</el-button>
                    <el-button type="primary" @click="goPlugins">{{ t('plugin.source.catalog.done.goPlugins') }}</el-button>
                </div>
            </template>
        </el-result>
    </div>
</template>

<script setup lang="ts">
import type { CatalogInstallData } from '@/api/plugin/source'
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { ElMessage } from 'element-plus'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

defineProps<{
    result: CatalogInstallData
}>()

const emit = defineEmits<{
    close: []
}>()

const { t } = useI18n()
const router = useRouter()

const goPlugins = () => {
    // 插件管理路由由菜单下发（数据库迁移）；未注册时直接 push 会落到空白区，先提示再中止
    if (!router.hasRoute('plugin')) {
        ElMessage.warning(t('plugin.source.routeMissing'))
        return
    }
    emit('close')
    router.push({ path: `${adminBaseRoutePath}/plugin` })
}
</script>

<style scoped lang="scss">
.done-descriptions {
    margin-bottom: 12px;
    text-align: left;
}
.done-alert {
    margin-bottom: 10px;
    text-align: left;
}
.done-hint {
    font-size: 12px;
    line-height: 20px;
    color: var(--el-text-color-secondary);
    text-align: left;
}
.done-buttons {
    display: flex;
    align-items: center;
    justify-content: center;
    padding-top: 16px;
}
.mono {
    font-family: var(--el-font-family-mono, monospace);
    word-break: break-all;
}
</style>
