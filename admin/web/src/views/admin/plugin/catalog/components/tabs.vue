<!--
  界面结构与组件用法移植自 BuildAdmin v2.3.9
  https://gitee.com/wonderful-code/buildadmin
  Apache-2.0，Copyright 妙码生花
  已修改：来源页签 + 清单卡片网格按 ai-go-admin 的 catalog 字段重做；
  删除了价格、下载量、购买、封面图与 BuildAdmin store/types 依赖。
-->
<template>
    <div>
        <el-tabs
            v-model="activeTab"
            v-loading="loading"
            :element-loading-text="t('plugin.source.catalog.loading')"
            type="border-card"
            class="store-tabs"
        >
            <el-tab-pane v-for="tab in tabList" :key="tab.name" :name="tab.name" :label="tab.label" class="store-tab-pane">
                <template v-if="visibleEntries(tab.name).length > 0">
                    <el-row :gutter="15" class="goods">
                        <el-col
                            v-for="item in visibleEntries(tab.name)"
                            :key="`${item.source_id}-${item.uid}-${item.version}`"
                            :xs="12"
                            :sm="8"
                            :md="8"
                            :lg="6"
                            :xl="4"
                            class="goods-col"
                        >
                            <div class="goods-item suspension" @click="emit('open', item)">
                                <div class="goods-cover">
                                    <Icon name="lucide-package" size="46" color="var(--el-color-primary)" />
                                </div>
                                <div class="goods-footer">
                                    <div class="goods-tag">
                                        <el-tag size="small" :type="kindType(item.kind)">{{ kindText(item.kind) }}</el-tag>
                                        <el-tag v-if="item.installed_version" size="small" type="success">
                                            {{ t('plugin.source.catalog.card.installed', { version: item.installed_version }) }}
                                        </el-tag>
                                        <el-tag v-if="item.source_conflict" size="small" type="danger">{{
                                            t('plugin.source.catalog.card.conflict')
                                        }}</el-tag>
                                        <el-tag v-if="item.same_content" size="small" type="info">{{
                                            t('plugin.source.catalog.card.sameContent')
                                        }}</el-tag>
                                        <el-tag v-if="isSourceDisabled(item)" size="small" type="warning">{{
                                            t('plugin.source.catalog.card.sourceDisabled')
                                        }}</el-tag>
                                    </div>
                                    <div class="goods-title" :title="entryTitle(item, locale)">{{ entryTitle(item, locale) }}</div>
                                    <div class="goods-subtitle">{{ item.uid }} · v{{ item.version }}</div>
                                    <div class="goods-sha mono" :title="item.package_sha256">{{ shortSha(item.package_sha256) }}</div>
                                    <div class="goods-data">
                                        <span class="goods-source">{{ item.source_name }}</span>
                                        <span class="goods-size">{{ formatBytes(item.package_size) }}</span>
                                    </div>
                                </div>
                            </div>
                        </el-col>
                    </el-row>
                </template>
                <el-empty v-else class="modules-empty" :description="t('plugin.source.catalog.card.empty')" />
            </el-tab-pane>
        </el-tabs>
    </div>
</template>

<script setup lang="ts">
import { entryTitle, formatBytes, shortSha, type CatalogEntry, type CatalogSourceSummary } from '@/api/plugin/source'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

interface Props {
    sources: CatalogSourceSummary[]
    entries: CatalogEntry[]
    loading?: boolean
}

const props = withDefaults(defineProps<Props>(), {
    loading: false,
})

const emit = defineEmits<{
    open: [entry: CatalogEntry]
}>()

const activeTab = defineModel<string>({ default: 'all' })
const { t, locale } = useI18n()

const tabList = computed(() => [
    { name: 'all', label: t('plugin.source.catalog.tabs.all') },
    ...props.sources.filter((source) => source.enabled === 1).map((source) => ({ name: String(source.id), label: source.title || source.name })),
])

const isSourceDisabled = (entry: CatalogEntry) => props.sources.find((source) => source.id === entry.source_id)?.enabled !== 1

type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'
const kindCustom: Record<string, TagType> = { code: 'primary', declarative: 'info', mixed: 'success' }
const kindType = (kind: string): TagType => kindCustom[kind] ?? 'primary'
const kindText = (kind: string) => t(`plugin.source.catalog.kind.${kind === 'declarative' || kind === 'mixed' ? kind : 'code'}`)

const visibleEntries = (tab: string) => (tab === 'all' ? props.entries : props.entries.filter((item) => String(item.source_id) === tab))
</script>

<style scoped lang="scss">
.suspension:hover {
    z-index: 1;
}
.goods-item {
    display: block;
    margin-bottom: 15px;
    padding-bottom: 40px;
    position: relative;
    border-radius: var(--el-border-radius-base);
    background-color: var(--el-fill-color-extra-light);
    box-shadow: var(--el-box-shadow-light);
    cursor: pointer;
}
.goods-cover {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 120px;
    border-radius: var(--el-border-radius-base);
    border-bottom-left-radius: 0;
    border-bottom-right-radius: 0;
    background-color: var(--el-fill-color-light);
}
.modules-empty {
    width: 100%;
}
.mono {
    font-family: var(--el-font-family-mono, monospace);
}
.goods-footer {
    display: block;
    overflow: hidden;
    padding: 10px 10px 0 10px;
    .goods-tag {
        min-height: 60px;
        display: flex;
        flex-wrap: wrap;
        align-content: flex-start;
        gap: 4px;
    }
    .goods-title {
        display: block;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        padding-top: 6px;
        font-size: 14px;
        line-height: 18px;
    }
    .goods-subtitle {
        padding-top: 2px;
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }
    .goods-sha {
        font-size: 12px;
        color: var(--el-text-color-placeholder);
    }
    .goods-data {
        display: flex;
        width: calc(100% - 20px);
        position: absolute;
        bottom: 0;
        align-items: baseline;
        padding: 10px 0;
        color: var(--el-text-color-secondary);
        font-size: 12px;
        .goods-size {
            margin-left: auto;
        }
    }
}
.el-tabs--border-card {
    border: none;
    box-shadow: var(--el-box-shadow-light);
    border-radius: var(--el-border-radius-base);
}
.el-tabs--border-card :deep(.el-tabs__header) {
    background-color: var(--el-fill-color-light);
    border-bottom: none;
    border-radius: var(--el-border-radius-base);
}
.el-tabs--border-card :deep(.el-tabs__item.is-active) {
    border: 1px solid transparent;
}
.el-tabs--border-card :deep(.el-tabs__nav-wrap) {
    border-radius: var(--el-border-radius-base);
}
:deep(.store-tabs) .el-tabs__content {
    padding: 15px 15px 0 15px;
    min-height: 350px;
}
@media screen and (max-width: 520px) {
    .goods {
        .goods-col {
            max-width: 100%;
            flex-basis: 100%;
        }
    }
}
</style>
