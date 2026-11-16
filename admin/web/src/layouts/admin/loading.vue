<template>
    <div>
        <div
            v-loading="true"
            element-loading-background="var(--ag-bg-color-overlay)"
            :element-loading-text="loadingText"
            class="default-main ag-flex-center ag-main-loading"
        ></div>
        <div v-if="state.showReload" class="ag-flex-center">
            <el-button v-blur @click="refresh" type="warning">{{ $t('common.reload') }}</el-button>
        </div>
    </div>
</template>

<script setup lang="ts">
import router from '@/router/index'
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { useConfig } from '@/stores/config'
import { useMenu } from '@/stores/menu'
import { useLoadingProgressText } from '@/utils/loadingProgress'
import { getFirstMenu } from '@/utils/router'
import { isEmpty } from 'lodash-es'
import { onUnmounted, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'

let timer: number
const route = useRoute()
const menu = useMenu()
const config = useConfig()
const { t } = useI18n()
const { text: loadingText } = useLoadingProgressText(
    () => true,
    () => t('common.loading').replace(/\.+$/, '')
)
const state = reactive({
    maximumWait: 1000 * 3,
    showReload: false,
})

const refresh = () => {
    if (!config.site.initialized) {
        window.dispatchEvent(new Event('admin:retry-init'))
    }
    router.go(0)
}

/**
 * 离开 loading 页
 *
 * 菜单数据（动态路由）晚于本页挂载就绪时也要自动放行，不能只依赖挂载时的一次性判断：
 * 1. 优先按 :to 精确回跳（与 layouts/admin/index.vue 初始化完成后的跳转口径一致，避免历史栈残留）；
 * 2. 否则落到第一个菜单。
 */
const tryLeaveLoading = () => {
    if (route.name !== 'admin/main-loading') return

    if (route.params.to) {
        try {
            const lastRoute = JSON.parse(route.params.to as string)
            const matched = lastRoute?.path && router.getRoutes().some((r) => r.path === lastRoute.path)
            if (lastRoute.path != adminBaseRoutePath && matched) {
                router.replace({ path: lastRoute.path, query: !isEmpty(lastRoute.query) ? lastRoute.query : {} })
                return
            }
        } catch {
            // :to 非 JSON 包装（如旧的裸路径参数）时直接按首个菜单放行
        }
    }

    const firstRoute = getFirstMenu(menu.rawData)
    if (firstRoute) {
        router.replace(firstRoute.path)
    }
}

watch([() => menu.rawData.length, () => config.site.initialized, () => route.fullPath], tryLeaveLoading, { immediate: true })

timer = window.setTimeout(() => {
    state.showReload = true
}, state.maximumWait)

// 兜底自救：初始化已完成但导航仍未落地（懒加载页面失败、并发导航被取消等）时再放行一次；
// 静态资源加载失败由 router.onError 负责重试，此处只覆盖「停在 loading 页」这一终态
// 监听动态路由加载完成，且目标已挂载
watch([() => menu.rawData.length, () => router.getRoutes()], ([menuLen]) => {
    if (menuLen > 0) {
        tryLeaveLoading()
    }
}, { immediate: true })

// 监听动态路由加载完成，且目标已挂载
watch([() => menu.rawData.length, () => router.getRoutes()], ([menuLen]) => {
    if (menuLen > 0) {
        tryLeaveLoading()
    }
}, { immediate: true })

const watchdogTimer = window.setTimeout(() => {
    if (route.name !== 'admin/main-loading') return
    if (!config.site.initialized) {
        window.dispatchEvent(new Event('admin:retry-init'))
    }
    tryLeaveLoading()
}, state.maximumWait * 2)

onUnmounted(() => {
    clearTimeout(timer)
    clearTimeout(watchdogTimer)
})
</script>

<style scoped lang="scss">
.ag-main-loading {
    height: 300px;
}
</style>
