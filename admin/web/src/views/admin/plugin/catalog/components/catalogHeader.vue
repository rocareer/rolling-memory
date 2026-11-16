<!--
  界面结构与组件用法移植自 BuildAdmin v2.3.9
  https://gitee.com/wonderful-code/buildadmin
  Apache-2.0，Copyright 妙码生花
  已修改：按 ai-go-admin 插件清单的来源/同步/搜索做最小适配；
  删除了上传 ZIP、本地模块开关、发布/积分、会员入口与 BuildAdmin store 依赖。
-->
<template>
    <div>
        <div class="modules-header">
            <div class="table-header-buttons">
                <el-button v-blur :title="t('plugin.source.catalog.toolbar.refresh')" color="#40485b" type="info" @click="emit('refresh')">
                    <Icon name="lucide-refresh-cw" color="#ffffff" size="14" />
                </el-button>
                <el-button-group v-if="canSync" class="ml10">
                    <el-button v-blur :title="t('plugin.source.catalog.toolbar.syncTip')" :loading="syncing" type="primary" @click="emit('sync')">
                        <Icon name="lucide-refresh-cw" color="#ffffff" size="14" />
                        <span class="table-header-operate-text">{{ t('plugin.source.catalog.toolbar.sync') }}</span>
                    </el-button>
                </el-button-group>
            </div>
            <div class="table-search">
                <el-input v-model="keywords" class="xs-hidden" clearable :placeholder="placeholder" @input="onSearchInput" />
            </div>
        </div>
    </div>
</template>

<script setup lang="ts">
import { debounce } from 'lodash-es'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

interface Props {
    syncing?: boolean
    canSync?: boolean
    placeholder?: string
}

withDefaults(defineProps<Props>(), {
    syncing: false,
    canSync: false,
    placeholder: '',
})

const emit = defineEmits<{
    refresh: []
    sync: []
    search: [keywords: string]
}>()

const { t } = useI18n()
const keywords = ref('')

const onSearchInput = debounce(() => {
    emit('search', keywords.value.trim())
}, 500)
</script>

<style scoped lang="scss">
.ml10 {
    margin-left: 10px;
}
.modules-header {
    display: flex;
    align-items: center;
    padding: 10px;
    margin-bottom: 10px;
    background-color: var(--el-fill-color-light);
    border-radius: var(--el-border-radius-base);
    border-top-left-radius: 0;
    border-top-right-radius: 0;
}
.table-header-operate-text {
    padding-left: 6px;
}
.table-search {
    margin-left: auto;
}
</style>
