<template>
    <component :is="config.layout.mode"></component>
    <!-- 初始化完成前不挂载浮标：其 SSE 与 1Hz 轮询会在首屏与 /admin/init 抢占 HTTP/1.1 同源连接槽位 -->
    <PluginWidgets v-if="config.site.initialized" />
</template>

<script setup lang="ts">
import { getInit } from '@/api/admin/index'
import PluginWidgets from '@/layouts/admin/components/pluginWidgets.vue'
import Classic from '@/layouts/admin/container/classic.vue'
import Default from '@/layouts/admin/container/default.vue'
import Double from '@/layouts/admin/container/double.vue'
import LeftSplit from '@/layouts/admin/container/leftSplit.vue'
import Streamline from '@/layouts/admin/container/streamline.vue'
import router from '@/router/index'
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { useAdminInfo } from '@/stores/adminInfo'
import { useConfig } from '@/stores/config'
import { BEFORE_RESIZE_LAYOUT } from '@/stores/constant/cacheKey'
import { useMenu } from '@/stores/menu'
import { setNavTabsWidth } from '@/utils/layout'
import { getFirstMenu, handleAdminRoute } from '@/utils/router'
import { Session } from '@/utils/storage'
import { useEventListener } from '@vueuse/core'
import { isEmpty } from 'lodash-es'
import { onBeforeMount, onMounted, reactive } from 'vue'
import { useRoute } from 'vue-router'

defineOptions({
    components: { Default, Classic, Streamline, Double, LeftSplit },
})

const menu = useMenu()
const route = useRoute()
const config = useConfig()
const adminInfo = useAdminInfo()

const state = reactive({
    autoMenuCollapseLock: false,
})

onMounted(() => {
    if (!adminInfo.token) {
        return router.push({ name: 'admin/login', query: { redirect: route.fullPath } })
    }

    init()
    setNavTabsWidth()
    useEventListener(window, 'resize', setNavTabsWidth)
    useEventListener(window, 'admin:retry-init', () => {
        if (!config.site.initialized && adminInfo.token) {
            init(0)
        }
    })
})
onBeforeMount(() => {
    onAdaptiveLayout()
    useEventListener(window, 'resize', onAdaptiveLayout)
})

/**
 * 后台初始化请求，获取站点配置，动态路由等信息
 * getInit 请求函数已配置响应 key 的命名方式自动转换 snake_case → camelCase
 */
const init = (attempt = 0) => {
    getInit()
        .then((res) => {
            adminInfo.dataFill({ ...res.data.data.admin, super: res.data.data.super }, ['token'])

            config.siteDataFill(res.data.data.siteConfig)
            config.setSiteInitStatus(true)

            if (res.data.data.rules.length) {
                handleAdminRoute(res.data.data.rules)

                // 预跳转到上次路径（仅当该路径在当前已注册路由中存在时才跳，否则走下方 firstRoute）
                if (route.params.to) {
                    const lastRoute = JSON.parse(route.params.to as string)
                    if (lastRoute.path != adminBaseRoutePath) {
                        const matched = router.getRoutes().some((r) => r.path === lastRoute.path)
                        if (matched) {
                            let query = !isEmpty(lastRoute.query) ? lastRoute.query : {}
                            router.push({ path: lastRoute.path, query: query })
                            return
                        }
                    }
                }

                // 跳转到第一个菜单
                let firstRoute = getFirstMenu(menu.rawData)
                if (firstRoute) {
                    router.push(firstRoute.path)
                }
            }
        })
        .catch(() => {
            // 首屏偶发网络拥塞或服务重启会让 /admin/init 超时失败：退避自动重试，
            // 避免停留在 /admin/loading 只能手动强刷新；身份已失效（令牌被清）或已离开 loading 时不再重试
            if (attempt >= 5) return
            if (!adminInfo.token) return
            if (router.currentRoute.value.name !== 'admin/main-loading') return
            window.setTimeout(() => init(attempt + 1), 800 * (attempt + 1))
        })
}

const onAdaptiveLayout = () => {
    let defaultBeforeResizeLayout = {
        menuCollapse: config.layout.menuCollapse,
    }
    let beforeResizeLayout = Session.get(BEFORE_RESIZE_LAYOUT)
    if (!beforeResizeLayout) {
        Session.set(BEFORE_RESIZE_LAYOUT, defaultBeforeResizeLayout)
    }

    const clientWidth = document.body.clientWidth
    if (clientWidth < 1024) {
        /**
         * 锁定窗口改变自动调整 menuCollapse
         * 避免已是小窗且打开了菜单栏时，意外的自动关闭菜单栏
         */
        if (!state.autoMenuCollapseLock) {
            state.autoMenuCollapseLock = true
            config.setLayoutValue('menuCollapse', true)
        }
        config.setLayoutValue('shrink', true)
    } else {
        state.autoMenuCollapseLock = false
        let beforeResizeLayoutTemp = beforeResizeLayout || defaultBeforeResizeLayout

        config.setLayoutValue('menuCollapse', beforeResizeLayoutTemp.menuCollapse)
        config.setLayoutValue('shrink', false)
    }
}
</script>
