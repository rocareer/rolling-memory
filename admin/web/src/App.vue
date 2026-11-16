<template>
    <el-config-provider :value-on-clear="() => null" :locale="elLocale">
        <router-view></router-view>
    </el-config-provider>
</template>

<script setup lang="ts">
import { updateHtmlDarkClass } from '@/hooks/useDark'
import { useConfig } from '@/stores/config'
import { init as viteInit } from '@/utils/vite'
import elEn from 'element-plus/es/locale/lang/en'
import elZhCn from 'element-plus/es/locale/lang/zh-cn'
import { computed, onMounted, watch } from 'vue'

const config = useConfig()

// 一次性迁移旧版浏览器缓存：确保默认开启暗黑模式（dark: true）与侧栏左上 Logo（menuTopBarLogo: true）
const THEME_MIGRATION_KEY = 'rm_brand_r07_dark_v1'
if (typeof window !== 'undefined' && !window.localStorage.getItem(THEME_MIGRATION_KEY)) {
    config.setLayoutValue('dark', true)
    config.setLayoutValue('menuTopBarLogo', true)
    config.setLayoutValue('menuTopBarColor', ['#0f1014', '#f5f5f7'])
    window.localStorage.setItem(THEME_MIGRATION_KEY, '1')
}

watch(
    () => config.layout.dark,
    (dark) => {
        updateHtmlDarkClass(Boolean(dark))
    },
    { immediate: true }
)

const elLocales: Record<string, typeof elEn> = {
    en: elEn,
    'zh-cn': elZhCn,
}

const elLocale = computed(() => elLocales[config.lang.active] || elLocales[config.lang.fallback])

onMounted(() => {
    updateHtmlDarkClass(Boolean(config.layout.dark))
    viteInit()
})
</script>
