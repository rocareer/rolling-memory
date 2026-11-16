import i18n from '@/lang/index'
import { watch } from 'vue'

// 品牌欢迎语覆盖：宿主 dashboard 读取 dashboard.welcomeTip；插件语言包只能新增
// plugin.<名>.* 命名空间，不能覆盖宿主键，所以这里在语言加载完成后 merge 一次。
// 本模块由品牌 overlay 的 main.ts 引入，只在 VITE_BRAND=rolling-brand 时生效。
// 文案与品牌包语言包 plugin.rolling-brand.welcomeTip 同源：优先取语言包，语言包未就绪时回退到同源字面量。
const brandWelcomeTip: Record<string, string> = {
    'zh-cn': 'Rolling · Autonoetic Memory Engine —— 100% 向量无关 × 100% 大模型无关。',
    en: 'Rolling · Autonoetic Memory Engine — 100% Vector-Free × 100% LLM-Free.',
}

const resolveWelcomeTip = (lang: string): string => {
    const key = 'plugin.rolling-brand.welcomeTip'
    if (i18n.global.te(key)) {
        const packed = i18n.global.t(key)
        if (packed && packed !== key) {
            return packed
        }
    }
    return brandWelcomeTip[lang] || brandWelcomeTip['zh-cn']
}

const applyBrandMessages = () => {
    const lang = i18n.global.locale.value as string
    i18n.global.mergeLocaleMessage(lang, { dashboard: { welcomeTip: resolveWelcomeTip(lang) } })
}

// 只在选中本品牌时覆盖；未选品牌或插件未安装时不改宿主语言包。
if (import.meta.env.VITE_BRAND === 'rolling-brand') {
    applyBrandMessages()
    watch(i18n.global.locale, applyBrandMessages)
    if (typeof window !== 'undefined') {
        window.addEventListener('load', applyBrandMessages, { once: true })
    }
}
