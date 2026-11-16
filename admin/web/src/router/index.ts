import i18n from '@/lang/index'
import staticRoutes from '@/router/static'
import { useAdminInfo } from '@/stores/adminInfo'
import { useConfig } from '@/stores/config'
import { loading } from '@/utils/loading'
import { Session } from '@/utils/storage'
import { useTitle } from '@vueuse/core'
import { ElMessage } from 'element-plus'
import NProgress from 'nprogress'
import 'nprogress/nprogress.css'
import { createRouter, createWebHashHistory } from 'vue-router'

const router = createRouter({
    history: createWebHashHistory(),
    routes: staticRoutes,
})

// 路由加载前
router.beforeEach((to) => {
    NProgress.configure({ showSpinner: false })
    NProgress.start()

    // 公开首页不参与后台鉴权；后台路由在挂载布局前鉴权，避免刷新时先渲染后台再跳登录。
    const adminInfo = useAdminInfo()
    if (to.matched.some((record) => record.meta.requiresAuth) && !adminInfo.token) {
        return { name: 'admin/login', query: { redirect: to.fullPath } }
    }

    if (!window.loading) {
        loading.show()
        window.loading = true
    }
})

/**
 * 懒加载页面（动态 import）失败自愈：依赖重优化（dev 下 Vite 返回 504 Outdated Optimize Dep）、
 * 发版后旧 chunk 失效、瞬时网络抖动都会让本次导航直接失败——页面会永久停在 /admin/loading
 * 且没有任何提示，只能手动强刷新。
 */
const CHUNK_LOAD_FAILED = /dynamically imported module|Importing a module script failed|Loading chunk|ChunkLoadError/i
const CHUNK_RETRY_DELAY = 300
const CHUNK_RELOAD_THROTTLE = 10000
const CHUNK_RELOAD_AT_KEY = 'ai-go-chunk-reload-at'

// 导航成功即归还重试机会；失败时先原地重试一次，仍失败则整页重载一次（10 秒节流防刷新循环），再失败才提示手动刷新
let chunkRetryCount = 0

// 路由加载后
router.afterEach((to, _from, failure) => {
    if (window.loading) {
        loading.hide()
        window.loading = false
    }
    NProgress.done()
    if (!failure) {
        chunkRetryCount = 0
    }

    // 设置浏览器标题
    const config = useConfig()
    const titleKey = to?.meta?.title as string | undefined
    const title = titleKey && i18n.global.te(titleKey) ? i18n.global.t(titleKey) : ''
    useTitle().value = title ? `${title}${config.site.name ? ' - ' + config.site.name : ''}` : `${config.site.name}`
})

router.onError((error, to) => {
    const message = error instanceof Error ? error.message : String(error ?? '')
    if (!CHUNK_LOAD_FAILED.test(message)) return

    if (chunkRetryCount === 0) {
        chunkRetryCount++
        window.setTimeout(() => {
            router.replace(to.fullPath).catch(() => {})
        }, CHUNK_RETRY_DELAY)
        return
    }

    const lastReloadAt = Number(Session.get(CHUNK_RELOAD_AT_KEY) ?? 0)
    if (Date.now() - lastReloadAt > CHUNK_RELOAD_THROTTLE) {
        Session.set(CHUNK_RELOAD_AT_KEY, Date.now())
        window.location.reload()
        return
    }

    ElMessage.error(i18n.global.t('common.loadResourceFailed'))
})

export default router
