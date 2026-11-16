<!--
  插件频道门禁弹窗（宿主所有；插件管理页 / 插件清单页 / 仓库来源页共用）。

  口径（插件频道直接上门禁 · 弹窗）：生产环境（后端 env.lifecycle_allowed=false）**整频道封闭**——
  弹窗不可关闭（无关闭按钮，点遮罩与 Esc 都不关），唯一出口是「返回仪表盘」；频道内容即使已在下方
  （只读请求允许）也被遮罩挡住不可操作。注意层级差别：**后端只禁 install/uninstall**（lifecycle_forbidden），
  整频道封闭是前端的更严口径；启停/来源维护/同步等只读或低危动作在 UI 上同样不可达，兜底走 CLI。

  判定与文案派生单点：usePluginEnv（env 未知时不弹——首帧、旧后端、列表失败都不误报封闭）。
-->
<template>
    <el-dialog
        v-if="gated && !closing"
        class="plugin-channel-gate"
        :model-value="true"
        :title="t('plugin.envGate.title')"
        width="min(600px, 92vw)"
        :show-close="false"
        :close-on-click-modal="false"
        :close-on-press-escape="false"
        :destroy-on-close="false"
        append-to-body
    >
        <el-alert class="plugin-gate-alert" type="error" :closable="false" show-icon :title="t('plugin.envGate.headline')" />
        <div class="plugin-gate-env">{{ envHint }}</div>
        <div class="plugin-gate-body">
            <p>{{ t('plugin.envGate.why') }}</p>
            <p>{{ t('plugin.envGate.howto') }}</p>
        </div>
        <template #footer>
            <!-- 不加 v-blur：本按钮是这张不可关闭模态的唯一出口，失焦即等于键盘用户没有出口 -->
            <el-button type="primary" @click="leave">{{ t('plugin.envGate.leave') }}</el-button>
        </template>
    </el-dialog>
</template>

<script setup lang="ts">
import type { PluginEnv } from '@/api/plugin'
import { adminBaseRoutePath } from '@/router/static/adminBase'
import { computed, onActivated, onUnmounted, ref } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { usePluginEnv } from './usePluginEnv'

const props = defineProps<{ env: PluginEnv | null }>()

const { t } = useI18n()
const router = useRouter()
const { gated, envHint } = usePluginEnv(computed(() => props.env))

// closing：离开前先把弹窗关掉（v-if 转假 ⇒ Teleport 到 body 的弹窗内容随组件卸载）。
// 不先关会出事：三页都带 keep-alive 且弹窗 append-to-body，页面被反激活时 Teleport 出去的内容
// 不随之搬走，遮罩会继续盖在目标页上，唯一出口变成空操作、只能刷新。
//
// 置位必须覆盖**每一条**离开路径，不能只在出口按钮里置：离开频道还可能来自浏览器后退/前进、
// 手改 hash、同布局内的程序化跳转——它们都不经过 leave()。所以用组件内的路由守卫（导航离开前触发，
// 此时组件仍活跃、能可靠卸载弹窗）；三页都在 keep-alive 列表里，再进频道是「激活」而非重新挂载，
// 因此在 onActivated 里复位，否则同一次会话里第二次进频道会没有门禁。
//
// closing 不是单向闩锁，但复位**必须按「本页是否还是当前路由」判**，不能只看导航失败：
//   - 只看 failure 会把无关失败（最常触发的是「点当前页签/当前菜单项」产生的 duplicated，它压根不跑守卫）
//     当成「我们被取消了」，把已离场、已反激活页面的闩锁拉回 false ⇒ 它 Teleport 到 body 的遮罩会重建，
//     盖住用户此刻所在的页面（实测高危回归）；
//   - 只看 afterEach 也漏一类：非导航错误（懒加载 chunk 失败、守卫抛错）根本不进 afterEach ⇒ 闩锁停在
//     true、人还在频道页而弹窗已被卸载（实测中型缺陷），所以还要挂 router.onError。
// 统一判据：任何一次导航落定（失败、错误或成功）后，若**当前路由仍是本页**就复位；已离开则保持关闭，
// 等下次 onActivated（重新进入）复位。
const closing = ref(false)
const route = useRoute()
const myRouteName = (route.name ?? '') as string

const settleClosing = () => {
    if (String(router.currentRoute.value.name ?? '') === myRouteName) {
        closing.value = false
    }
}

onActivated(() => {
    closing.value = false
})
onBeforeRouteLeave(() => {
    closing.value = true
})
const removeAfterEach = router.afterEach(settleClosing)
const removeOnError = router.onError(settleClosing)
onUnmounted(() => {
    removeAfterEach()
    removeOnError()
})

// leave 的唯一出口按钮：仪表盘（菜单已下发才有该路由，未注册时回后台入口 /admin）。
// 不用 router.back()——深链直达频道时「上一页」会把人带出后台。
const leave = () => {
    closing.value = true
    if (router.hasRoute('dashboard')) {
        router.push({ name: 'dashboard' })
        return
    }
    router.push(adminBaseRoutePath)
}
</script>

<style scoped lang="scss">
.plugin-channel-gate {
    .plugin-gate-alert {
        margin-bottom: 12px;
    }

    .plugin-gate-env {
        padding-bottom: 8px;
        font-size: 13px;
        color: var(--el-text-color-secondary);
    }

    .plugin-gate-body {
        p {
            margin: 6px 0;
            font-size: 13px;
            line-height: 22px;
            color: var(--el-text-color-regular);
        }
    }
}
</style>
