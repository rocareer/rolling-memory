/**
 * 插件频道环境派生点（管理页 / 清单页 / 来源页共用）。
 *
 * 后端 env 段（/admin/plugin/list、/admin/plugin/catalog/list、/admin/plugin/source/list 的响应）
 * 是唯一权威，本文件只把它翻译成界面要用的三件事实，**不复述判定规则**：
 *   - gated       生产环境 ⇒ 插件频道整频道封闭（门禁弹窗）；
 *   - envNameText 环境名（开发环境 / 生产环境）；
 *   - envHint     结论 + 判定依据（显式配置 plugin.env / 未配置时按源码检出自动判定）。
 *
 * 文案键集中在 lang/<语言>/plugin.yaml 的 plugin.envGate.*（三页共用一份，不再按页复制）；
 * env 未知（首帧、旧后端、列表失败）时 gated=false、文案为空——前端不替后端抢跑判定，
 * 后端门禁（lifecycle_forbidden）才是权威。
 */
import type { PluginEnv } from '@/api/plugin'
import { computed, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'

export function usePluginEnv(env: Ref<PluginEnv | null>) {
    const { t } = useI18n()

    const gated = computed(() => env.value != null && !env.value.lifecycle_allowed)
    const envNameText = computed(() => (env.value ? t(`plugin.envGate.name.${env.value.name}`) : ''))
    const envHint = computed(() => {
        const current = env.value
        if (!current) {
            return ''
        }
        const basis =
            current.source === 'config'
                ? t('plugin.envGate.basisConfig', { value: current.configured || '-' })
                : current.lifecycle_allowed
                  ? t('plugin.envGate.basisAutoRoot')
                  : t('plugin.envGate.basisAutoNoRoot')
        const verdict = current.lifecycle_allowed ? t('plugin.envGate.allow') : t('plugin.envGate.forbid')
        return `${verdict}（${basis}）`
    })

    return { gated, envNameText, envHint }
}
