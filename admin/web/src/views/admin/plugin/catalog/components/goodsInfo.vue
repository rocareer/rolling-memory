<!--
  界面结构与组件用法移植自 BuildAdmin v2.3.9
  https://gitee.com/wonderful-code/buildadmin
  Apache-2.0，Copyright 妙码生花
  已修改：详情字段换成 catalog 的 uid/version/kind/host_api/package/sha/source/签名；
  轮播图改为图标，Markdown 详情改为纯文本插值；删除了价格、下载量、演示、开发者、
  购买/支付、账号、卸载/升级与状态开关。
-->
<template>
    <el-dialog v-model="visible" class="goods-info-dialog" :title="t('plugin.source.catalog.detail.title')" width="60%">
        <el-scrollbar v-if="entry" :key="`${entry.source_id}-${entry.uid}-${entry.version}`" :height="500">
            <div class="goods-info">
                <div class="goods-images">
                    <div class="goods-cover">
                        <Icon name="lucide-package" size="72" color="var(--el-color-primary)" />
                    </div>
                    <div class="goods-tag">
                        <el-tag size="small" :type="kindType(entry.kind)">{{ kindText(entry.kind) }}</el-tag>
                        <el-tag v-if="entry.installed_version" size="small" type="success">
                            {{ t('plugin.source.catalog.card.installed', { version: entry.installed_version }) }}
                        </el-tag>
                        <el-tag v-if="entry.source_conflict" size="small" type="danger">{{ t('plugin.source.catalog.card.conflict') }}</el-tag>
                        <el-tag v-if="entry.same_content" size="small" type="info">{{ t('plugin.source.catalog.card.sameContent') }}</el-tag>
                        <el-tag v-if="!sourceEnabled" size="small" type="warning">{{ t('plugin.source.catalog.card.sourceDisabled') }}</el-tag>
                        <!-- 环境门禁：生产环境禁止安装（与页面顶部的环境标识同一判定） -->
                        <el-tag v-if="!installAllowed" size="small" type="danger">{{ t('plugin.envGate.forbid') }}</el-tag>
                    </div>
                </div>
                <div class="goods-basic">
                    <h4 class="goods-basic-title">{{ title }}</h4>
                    <div class="basic-item">
                        <div class="basic-item-title">{{ t('plugin.source.catalog.detail.uid') }}</div>
                        <div class="basic-item-content">{{ entry.uid }}</div>
                    </div>
                    <div class="basic-item">
                        <div class="basic-item-title">{{ t('plugin.source.catalog.detail.version') }}</div>
                        <div class="basic-item-content">{{ entry.version }}</div>
                    </div>
                    <div class="basic-item">
                        <div class="basic-item-title">{{ t('plugin.source.catalog.detail.kind') }}</div>
                        <div class="basic-item-content">{{ kindText(entry.kind) }}</div>
                    </div>
                    <div class="basic-item">
                        <div class="basic-item-title">{{ t('plugin.source.catalog.detail.hostApi') }}</div>
                        <div class="basic-item-content">{{ entry.host_api || '-' }}</div>
                    </div>
                    <div class="basic-item">
                        <div class="basic-item-title">{{ t('plugin.source.catalog.detail.frameworkReq') }}</div>
                        <div class="basic-item-content">{{ entry.framework_req || '-' }}</div>
                    </div>
                    <div class="basic-item">
                        <div class="basic-item-title">{{ t('plugin.source.catalog.detail.requires') }}</div>
                        <div class="basic-item-content">{{ entry.requires || '-' }}</div>
                    </div>
                    <div class="basic-item">
                        <div class="basic-item-title">{{ t('plugin.source.catalog.detail.license') }}</div>
                        <div class="basic-item-content">{{ entry.license || '-' }}</div>
                    </div>
                    <div class="basic-item">
                        <div class="basic-item-title">{{ t('plugin.source.catalog.detail.source') }}</div>
                        <div class="basic-item-content">{{ entry.source_name }}</div>
                    </div>
                    <div class="basic-item">
                        <div class="basic-item-title">{{ t('plugin.source.catalog.detail.size') }}</div>
                        <div class="basic-item-content">{{ formatBytes(entry.package_size) }}</div>
                    </div>
                    <div class="basic-item">
                        <div class="basic-item-title">{{ t('plugin.source.catalog.detail.signed') }}</div>
                        <div class="basic-item-content">
                            {{ entry.package_signed ? t('plugin.source.catalog.version.signedYes') : t('plugin.source.catalog.version.signedNo') }}
                        </div>
                    </div>
                    <div class="basic-item">
                        <div class="basic-item-title">{{ t('plugin.source.catalog.detail.syncedAt') }}</div>
                        <div class="basic-item-content">{{ formatTime(entry.synced_at) }}</div>
                    </div>
                    <div class="basic-item">
                        <div class="basic-item-title">{{ t('plugin.source.catalog.detail.sha256') }}</div>
                        <div class="basic-item-content mono">{{ entry.package_sha256 }}</div>
                    </div>
                    <div class="basic-item">
                        <div class="basic-item-title">{{ t('plugin.source.catalog.detail.catalogSha256') }}</div>
                        <div class="basic-item-content mono">{{ entry.catalog_sha256 }}</div>
                    </div>
                    <div class="basic-buttons">
                        <el-button v-if="canPreflight" v-blur type="info" @click="emit('preflight', entry)">{{
                            t('plugin.source.catalog.detail.preflight')
                        }}</el-button>
                        <el-button v-if="entry.installed_version" v-blur disabled class="basic-button-item">
                            {{ t('plugin.source.catalog.card.installed', { version: entry.installed_version }) }}
                        </el-button>
                        <el-button
                            v-else-if="canInstall"
                            v-blur
                            class="basic-button-item"
                            type="success"
                            :disabled="!sourceEnabled || !installAllowed"
                            @click="emit('install', entry)"
                        >
                            {{ t('plugin.source.catalog.installNow') }}
                        </el-button>
                    </div>
                </div>
            </div>
            <div class="goods-detail">
                <p v-if="description">{{ description }}</p>
                <p v-else class="empty-detail">{{ t('plugin.source.catalog.detail.noDescription') }}</p>
            </div>
            <div class="goods-version">
                <h1>{{ t('plugin.source.catalog.detail.versions') }}</h1>
                <CommonSelectVersion
                    :versions="versions"
                    :enabled-source-ids="enabledSourceIds"
                    :can-install="canInstall"
                    :install-allowed="installAllowed"
                    @install="emit('install', $event)"
                />
            </div>
        </el-scrollbar>
        <el-empty v-else :description="t('plugin.source.catalog.detail.empty')" />
    </el-dialog>
</template>

<script setup lang="ts">
import { entryTitle, formatBytes, type CatalogEntry } from '@/api/plugin/source'
import { dayjs } from 'element-plus'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import CommonSelectVersion from './commonSelectVersion.vue'

interface Props {
    entry: CatalogEntry | null
    versions?: CatalogEntry[]
    enabledSourceIds?: number[]
    canPreflight?: boolean
    canInstall?: boolean
    /** 环境门禁（catalog/list 的 env.lifecycle_allowed）：生产环境置灰安装入口 */
    installAllowed?: boolean
}

const props = withDefaults(defineProps<Props>(), {
    entry: null,
    versions: () => [],
    enabledSourceIds: () => [],
    canPreflight: true,
    canInstall: true,
    installAllowed: true,
})

const emit = defineEmits<{
    preflight: [entry: CatalogEntry]
    install: [entry: CatalogEntry]
}>()

const visible = defineModel<boolean>({ required: true })
const { t, locale } = useI18n()

const title = computed(() => (props.entry ? entryTitle(props.entry, locale.value) : ''))
const description = computed(() =>
    props.entry ? (locale.value === 'en' ? props.entry.desc_en || props.entry.desc_zh : props.entry.desc_zh || props.entry.desc_en) : ''
)
// 后端 catalogRow 不返回 source_enabled；启停以 sources 汇总（enabledSourceIds）为准
const sourceEnabled = computed(() => (props.entry ? props.enabledSourceIds.includes(props.entry.source_id) : true))

type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'
const kindCustom: Record<string, TagType> = { code: 'primary', declarative: 'info', mixed: 'success' }
const kindType = (kind: string): TagType => kindCustom[kind] ?? 'primary'
const kindText = (kind: string) => t(`plugin.source.catalog.kind.${kind === 'declarative' || kind === 'mixed' ? kind : 'code'}`)

const formatTime = (value?: string | null) => (value ? dayjs(value).format('YYYY-MM-DD HH:mm:ss') : '-')
</script>

<style scoped lang="scss">
:deep(.goods-info-dialog) .el-dialog__body {
    padding: 0 20px;
}
.goods-info {
    display: flex;
    position: relative;
    .goods-images {
        max-width: 41%;
        width: 300px;
        .goods-cover {
            display: flex;
            align-items: center;
            justify-content: center;
            height: 300px;
            background-color: var(--el-fill-color-light);
            border-radius: var(--el-border-radius-base);
        }
        .goods-tag {
            display: flex;
            flex-wrap: wrap;
            gap: 4px;
            padding-top: 10px;
        }
    }
    .goods-basic {
        position: relative;
        flex: 1;
        padding: 0 10px;
        .goods-basic-title {
            padding-bottom: 20px;
        }
        .basic-item {
            display: flex;
            align-items: center;
            padding: 4px 0;
            .basic-item-title {
                font-size: var(--el-font-size-base);
                color: var(--el-text-color-secondary);
                width: 110px;
                flex-shrink: 0;
            }
            .basic-item-content {
                font-size: var(--el-font-size-base);
                color: var(--el-text-color-regular);
                word-break: break-all;
            }
        }
        .basic-buttons {
            padding-top: 10px;
        }
    }
}
.basic-button-item {
    --el-loading-spinner-size: 22px;
}
.goods-detail {
    width: 100%;
    padding-top: 10px;
    font-size: var(--el-font-size-base);
    line-height: 22px;
    color: var(--el-text-color-regular);
}
.empty-detail {
    color: var(--el-text-color-secondary);
}
.goods-version {
    width: 100%;
    h1 {
        margin: 1.4em 0 0.8em;
        font-weight: 700;
        font-size: var(--el-font-size-large);
        color: var(--el-color-primary);
    }
}
.mono {
    font-family: var(--el-font-family-mono, monospace);
    word-break: break-all;
}
@media screen and (max-width: 1440px) {
    :deep(.goods-info-dialog) {
        --el-dialog-width: 65% !important;
    }
}
@media screen and (max-width: 1280px) {
    :deep(.goods-info-dialog) {
        --el-dialog-width: 80% !important;
    }
}
@media screen and (max-width: 1024px) {
    :deep(.goods-info-dialog) {
        --el-dialog-width: 92% !important;
    }
}
@media screen and (max-width: 540px) {
    .goods-info {
        flex-wrap: wrap;
        .goods-images {
            max-width: 100%;
            width: 100%;
        }
    }
}
</style>
