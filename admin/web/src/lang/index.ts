import { useConfig } from '@/stores/config'
import { getBaseURL } from '@/utils/request'
import { merge, set } from 'lodash-es'
import { type App, watch } from 'vue'
import { createI18n } from 'vue-i18n'
import { parse as parseYaml } from 'yaml'

/**
 * 支持的语言类型
 */
export type LangKey = 'zh-cn' | 'en'

/**
 * 支持的语言列表
 */
export const langs: LangKey[] = ['zh-cn', 'en']

/**
 * 语言显示名称
 */
export const langNames: Record<LangKey, string> = {
    en: 'English',
    'zh-cn': '简体中文',
}

/**
 * i18n 实例
 */
const i18n = createI18n({
    legacy: false,
    locale: 'en',
    fallbackLocale: 'en',
    messages: {},
})

// 开发环境将直接加载全部语言包，以避免 HMR 重新执行模块时 messages 被重置为空
// Vite 的 glob 配置必须是静态值，因此开发环境与生产环境分别声明: 开发全量加载，生产懒加载
const eagerLangGlobs = {
    en: import.meta.glob('./en/**/*.yaml', { query: '?raw', import: 'default', eager: true }),
    'zh-cn': import.meta.glob('./zh-cn/**/*.yaml', { query: '?raw', import: 'default', eager: true }),
}
const lazyLangGlobs = {
    en: import.meta.glob('./en/**/*.yaml', { query: '?raw', import: 'default' }),
    'zh-cn': import.meta.glob('./zh-cn/**/*.yaml', { query: '?raw', import: 'default' }),
}

/**
 * 设置 i18n，并为 vue 安装 i18n 插件
 */
export async function setupI18n(app: App): Promise<void> {
    const config = useConfig()
    i18n.global.fallbackLocale.value = config.lang.fallback

    // 初始化当前语言包
    await setLang(config.lang.active)

    // 监听 config.lang.active 变化（如 siteDataFill 接收到 CLI install --lang 下发的 default_lang）
    watch(
        () => config.lang.active,
        (nextLang) => {
            if (nextLang && i18n.global.locale.value !== nextLang) {
                void setLang(nextLang)
            }
        }
    )

    app.use(i18n)

    // 同步 CLI `rolling-memory install --lang` 持久化的后台默认语言
    syncServerDefaultLang(config)
}

function syncServerDefaultLang(config: ReturnType<typeof useConfig>): void {
    if (typeof window === 'undefined' || typeof fetch === 'undefined') return
    // 必须走 API 基址：dev 环境页面由 Vite 供给，相对路径会打到前端服务而非后台接口而 404
    fetch(`${getBaseURL()}/common/site-config`, { method: 'GET', headers: { Accept: 'application/json' } })
        .then((res) => (res.ok ? res.json() : null))
        .then((body) => {
            const data = body?.data
            if (!data) return
            const prevActive = config.lang.active
            config.siteDataFill(data)
            if (config.lang.active !== prevActive || i18n.global.locale.value !== config.lang.active) {
                void setLang(config.lang.active)
            }
        })
        .catch(() => {})
}

/**
 * 设置语言
 * @param lang 语言标识
 */
export async function setLang(lang: LangKey): Promise<void> {
    await loadMessages(lang)

    const config = useConfig()
    i18n.global.locale.value = lang
    config.setLang(lang)
}

const loadedLocales = new Set<LangKey>()

/**
 * 懒加载语言包
 * @param lang 语言标识
 */
export async function loadMessages(lang: LangKey): Promise<void> {
    // 仅当完整 YAML 语言包已加载过才跳过（避免插件 runtime 预注入单键导致 availableLocales 假阳性短路）
    if (loadedLocales.has(lang)) {
        return
    }

    try {
        // 批量加载 lang 目录下所有 .yaml 文件
        const glob = lazyLangGlobs[lang]
        const promises = Object.entries(glob).map(async ([path, source]) => {
            const raw = await source()
            const data = parseYaml(raw)
            return { path, data }
        })
        const modules = await Promise.all(promises)
        const existing = i18n.global.getLocaleMessage(lang)
        const built = buildMessages(lang, modules)
        i18n.global.setLocaleMessage(lang, merge({}, built, existing))
        loadedLocales.add(lang)
    } catch (error) {
        console.error(`Failed to load lang: ${lang}`, error)
    }
}

const buildMessages = (lang: LangKey, modules: { path: string; data: any }[]) => {
    // 按文件路径构建嵌套的 messages 结构
    const mergedMessages: Record<string, any> = {}
    for (const { path, data } of modules) {
        if (typeof data !== 'object' || data === null) {
            continue
        }
        const keys = filePathToKeys(lang, path)
        if (keys.length === 0) {
            // 合并到顶层
            merge(mergedMessages, data)
        } else {
            // 子模块 — 按路径嵌套
            merge(mergedMessages, set({}, keys, data))
        }
    }

    return mergedMessages
}

const filePathToKeys = (lang: LangKey, path: string) => {
    const langPathPrefix = `/${lang}`
    const pathName = path.slice(path.lastIndexOf(langPathPrefix) + (langPathPrefix.length + 1), path.lastIndexOf('.'))
    const keys = pathName.split('/')

    return keys
}

// 开发环境全量加载语言包，避免 HMR 重新执行模块时 messages 被重置为空
if (import.meta.env.DEV) {
    for (const lang of langs) {
        const modules = Object.entries(eagerLangGlobs[lang]).map(([path, raw]) => {
            try {
                return { path, data: parseYaml(raw as string) }
            } catch (e) {
                console.error(`Failed to parse yaml: ${path}`, e)
                return { path, data: {} }
            }
        })
        i18n.global.setLocaleMessage(lang, buildMessages(lang, modules))
    }
}

const menuTitleDict: Record<LangKey, Record<string, string>> = {
    'zh-cn': {
        dashboard: '控制台',
        'dashboard/console': '实时控制台',
        'dashboard/roi': 'ROI 监控屏',
        'dashboard/graph': '记忆图谱',
        'dashboard/artifacts': '记忆控制台',
        auth: '权限管理',
        'auth/group': '角色组管理',
        'auth/admin': '管理员管理',
        'auth/rule': '菜单规则管理',
        'auth/log': '管理员日志管理',
        routine: '常规管理',
        'routine/config': '系统配置',
        'routine/attachment': '附件管理',
        'routine/profile': '个人资料',
        'crud/crud': 'CRUD代码生成',
        'plugin-center': '插件',
        plugin: '插件管理',
        'plugin/source': '仓库来源',
        'plugin/catalog': '插件清单',
        help: '帮助中心',
        'help/docs': '帮助与文档',
        'help/repos': '开源仓库与声明',
        'plugin/rolling-memory': '记忆浏览',
        'plugin/rolling-memory/index': '记忆总览',
        'plugin/rolling-memory/background': 'L1 文档与代码',
        'plugin/rolling-memory/episodic': 'L2 会话记录',
        'plugin/rolling-memory/noetic': 'L3 检索沉淀',
        'plugin/rolling-memory/recall': '检索调试',
        'plugin/rolling-memory/eval-dir': '评测',
        'plugin/rolling-memory/eval': '评测实验室',
        'plugin/rolling-memory/atm-leaderboard': '剑桥 ATM-Bench 榜单',
        'plugin/rolling-memory/leaderboard': 'LoCoMo-Backboard 榜单',
        'plugin/ai': 'AI 路由',
        'plugin/ai/overview': '总览',
        'plugin/ai/index': 'AI 资产体检',
        'plugin/ai/cost': 'AI 成本账单',
        'plugin/ai/channel': '渠道管理',
        'plugin/ai/supplier': '厂商管理',
        'plugin/ai/model': '模型管理',
        'plugin/ai/apikey': '应用接入',
        'plugin/ai/usage': '用量统计',
        'plugin/ai/usage-log': '调用日志',
        'plugin/ai/task': '异步任务',
        'plugin/ai/voice': '语音合成',
    },
    en: {
        dashboard: 'Dashboard',
        'dashboard/console': 'Live Console',
        'dashboard/roi': 'ROI Monitor',
        'dashboard/graph': 'Memory Graph',
        'dashboard/artifacts': 'Memory Console',
        auth: 'Permissions',
        'auth/group': 'Role Groups',
        'auth/admin': 'Admins',
        'auth/rule': 'Menu Rules',
        'auth/log': 'Admin Logs',
        routine: 'Routine',
        'routine/config': 'System Config',
        'routine/attachment': 'Attachments',
        'routine/profile': 'Profile',
        'crud/crud': 'CRUD Generator',
        'plugin-center': 'Plugins',
        plugin: 'Plugin Management',
        'plugin/source': 'Plugin Sources',
        'plugin/catalog': 'Plugin Catalog',
        help: 'Help Center',
        'help/docs': 'Help & Docs',
        'help/repos': 'Open-Source Repos',
        'plugin/rolling-memory': 'Memory Browser',
        'plugin/rolling-memory/index': 'Memory Overview',
        'plugin/rolling-memory/background': 'L1 Docs & Code',
        'plugin/rolling-memory/episodic': 'L2 Sessions',
        'plugin/rolling-memory/noetic': 'L3 Recall Trail',
        'plugin/rolling-memory/recall': 'Recall Debugger',
        'plugin/rolling-memory/eval-dir': 'Evaluation',
        'plugin/rolling-memory/eval': 'Evaluation Lab',
        'plugin/rolling-memory/atm-leaderboard': 'Cambridge ATM-Bench Leaderboard',
        'plugin/rolling-memory/leaderboard': 'LoCoMo-Backboard Leaderboard',
        'plugin/ai': 'AI Router',
        'plugin/ai/overview': 'Overview',
        'plugin/ai/index': 'AI Asset Health',
        'plugin/ai/cost': 'AI Cost Ledger',
        'plugin/ai/channel': 'Channels',
        'plugin/ai/supplier': 'Vendors',
        'plugin/ai/model': 'Models',
        'plugin/ai/apikey': 'App Access',
        'plugin/ai/usage': 'Usage',
        'plugin/ai/usage-log': 'Call Logs',
        'plugin/ai/task': 'Async Tasks',
        'plugin/ai/voice': 'Speech Synthesis',
    },
}

const legacyFactoryMenuTitles: Record<string, string[]> = {
    'plugin/rolling-memory': ['记忆管理', '记忆浏览', 'Memory Management', 'Memory Browser'],
    'plugin/rolling-memory/episodic': ['L2 情景记忆', 'L2 会话记录', 'L2 Sessions'],
    'plugin/rolling-memory/noetic': ['L3 认知记忆', 'L3 检索沉淀', 'L3 Recall Trail'],
}

function isFactoryMenuTitle(key: string, rawTitle: string): boolean {
    if (!key) return false
    if (menuTitleDict['zh-cn'][key] === rawTitle || menuTitleDict.en[key] === rawTitle) {
        return true
    }
    return Boolean(legacyFactoryMenuTitles[key]?.includes(rawTitle))
}

export function translateMenuTitle(menu: { name?: any; meta?: any }): string {
    const lang = (i18n.global.locale.value || 'zh-cn') as LangKey
    const dict = menuTitleDict[lang] || menuTitleDict['zh-cn']
    const key = String(menu?.name || '')
    const rawTitle = menu?.meta?.rawTitle || menu?.meta?.title
    // 仅将已知出厂标题作为默认值；其他标题保留用户定制（包含语言包 key）。
    if (key && dict[key] && typeof rawTitle === 'string' && rawTitle && !isFactoryMenuTitle(key, rawTitle)) {
        return i18n.global.te(rawTitle) ? i18n.global.t(rawTitle) : rawTitle
    }
    if (key && dict[key]) {
        return dict[key]
    }
    if (typeof rawTitle === 'string' && rawTitle && i18n.global.te(rawTitle)) {
        return i18n.global.t(rawTitle)
    }
    return menu?.meta?.title ? String(menu.meta.title) : i18n.global.t('layouts.untitled')
}

export default i18n
