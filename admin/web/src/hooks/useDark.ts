import { useConfig } from '@/stores/config'
import { computed } from 'vue'

/**
 * 更新 html 元素的 dark class 与 color-scheme
 */
export const updateHtmlDarkClass = (val: boolean) => {
    if (typeof document === 'undefined') return
    document.documentElement.classList.toggle('dark', val)
    document.documentElement.style.colorScheme = val ? 'dark' : 'light'
}

/**
 * 暗黑模式相关操作（以 Pinia config.layout.dark 为唯一单一事实源，登录页与后台内外实时同步）
 */
export function useDark() {
    const config = useConfig()

    const isDark = computed<boolean>({
        get() {
            return Boolean(config.layout.dark)
        },
        set(val: boolean) {
            config.setLayoutValue('dark', val)
            updateHtmlDarkClass(val)
        },
    })

    const toggleDark = (val?: boolean) => {
        const next = typeof val === 'boolean' ? val : !config.layout.dark
        config.setLayoutValue('dark', next)
        updateHtmlDarkClass(next)
        return next
    }

    return {
        isDark,
        toggleDark,
    }
}
