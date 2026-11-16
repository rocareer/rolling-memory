import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import i18n from '@/lang'

/**
 * useLoadingProgressText 为任意 v-loading 布尔状态生成平滑单调递增的百分比进度文案：
 * p(t) = min(96, max(8, floor(96 * (1 - exp(-t / tauMs)))))，超过 1s 时附带已用秒数，
 * 让用户在等待表格查询、检索调试、图谱构建或目录扫描时清楚看到实时进度而非干转圈。
 */
export function useLoadingProgressText(loading: Ref<boolean> | (() => boolean), labelGetter?: () => string | undefined, tauMs = 700) {
    const startedAt = ref(0)
    const now = ref(0)
    let timer: ReturnType<typeof setInterval> | null = null

    const stop = () => {
        if (timer !== null) {
            clearInterval(timer)
            timer = null
        }
    }

    const isBusy = computed(() => (typeof loading === 'function' ? loading() : Boolean(loading.value)))

    watch(
        isBusy,
        (busy) => {
            stop()
            if (busy) {
                const t0 = Date.now()
                startedAt.value = t0
                now.value = t0
                timer = setInterval(() => {
                    now.value = Date.now()
                }, 150)
            }
        },
        { immediate: true }
    )

    onScopeDispose(stop)

    const percent = computed(() => {
        if (!isBusy.value || !startedAt.value) return 0
        const elapsed = Math.max(0, now.value - startedAt.value)
        const p = Math.floor(96 * (1 - Math.exp(-elapsed / Math.max(200, tauMs))))
        return Math.min(96, Math.max(8, p))
    })

    const text = computed(() => {
        const base =
            labelGetter?.() ||
            (String(i18n.global.locale.value || '')
                .toLowerCase()
                .startsWith('en')
                ? 'Loading'
                : '正在加载')
        if (!isBusy.value) return `${base}...`
        const elapsedSec = Math.max(0, (now.value - startedAt.value) / 1000)
        if (elapsedSec >= 1.0) {
            return `${base}... ${percent.value}% (${elapsedSec.toFixed(1)}s)`
        }
        return `${base}... ${percent.value}%`
    })

    return { percent, text }
}
