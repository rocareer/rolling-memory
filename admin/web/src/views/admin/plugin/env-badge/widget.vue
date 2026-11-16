<!-- plugin: env-badge v1.0.0 -->
<!--
    环境检测浮标（布局级插件组件）。

    源：plugins/env-badge/web/views/widget.vue；落位副本 web/src/views/admin/plugin/env-badge/widget.vue
    由安装器复制，两者逐字节一致（同步检查比对 sha256）。
    宿主后台布局的通用挂载点 web/src/layouts/admin/components/pluginWidgets.vue 只扫描
    /src/views/admin/plugin/*/widget.vue 这一约定路径，因此本文件是浮标唯一的入口。

    可见性 fail-closed：宿主运行时闸在插件停用/未装时返回 404，本组件捕获后不渲染任何东西。
-->
<template>
    <el-tooltip
        v-if="info"
        placement="top"
        effect="dark"
        :show-after="220"
        :enterable="false"
        :hide-after="0"
        popper-class="env-badge-popper"
    >
        <template #content>
            <div class="env-badge-tip">
                <div class="env-badge-tip-title">Backend runtime environment: {{ info.title }}</div>
                <div>Detected: {{ info.basis }}</div>
                <div>
                    Plugin lifecycle:
                    {{ info.lifecycle_allowed ? 'install / uninstall allowed' : 'install / uninstall blocked' }}
                </div>
                <div class="env-badge-tip-meta">ai-go-admin {{ info.host_version }} · plugin API {{ info.host_api_version }}</div>
            </div>
        </template>
        <span class="env-badge" :class="'is-' + info.name" role="status" tabindex="0" :aria-label="ariaLabel">
            <span class="env-badge-dot"></span>
            <span class="env-badge-label">{{ info.label }}</span>
        </span>
    </el-tooltip>
</template>

<script setup lang="ts">
import { useAdminInfo } from '@/stores/adminInfo'
import request from '@/utils/request'
import { computed, ref, watch } from 'vue'

/**
 * 浮标数据契约（后端 plugins/env-badge/plugin.go 的 infoResponse）：
 * POST /admin/plugin/env-badge/info —— 相对段 /info 由插件包 mount 在宿主建好的 plugin/env-badge 子组上，
 * 绝对前缀与运行时闸由宿主提供（插件只挂 AdminAuth：浮标对全体已登录管理员可见）。
 */
interface EnvBadgeInfo {
    name: 'dev' | 'prod'
    label: string
    title: string
    basis: string
    source: 'config' | 'auto'
    configured: string
    lifecycle_allowed: boolean
    host_version: string
    host_api_version: string
}

const adminInfo = useAdminInfo()
const info = ref<EnvBadgeInfo | null>(null)

const ariaLabel = computed(() => (info.value ? `Backend runtime environment: ${info.value.title}` : ''))

const load = async () => {
    try {
        const res = await request<EnvBadgeInfo>(
            { url: '/admin/plugin/env-badge/info', method: 'POST', data: {} },
            // 浮标是静默展示件：插件停用/未装时的 404 与网络失败都不弹全局提示
            { showErrorMessage: false, showNetworkErrorMessage: false, cancelDuplicate: false }
        )
        info.value = res.data.data ?? null
    } catch {
        info.value = null
    }
}

// 登录态变化即重取：登出隐藏，登录后（布局复用的场景）立即补上
watch(
    () => adminInfo.token,
    (token) => {
        if (token) {
            load()
            return
        }
        info.value = null
    },
    { immediate: true }
)
</script>

<style>
/* tooltip 内容被 teleport 到 body，scoped 选择器命中不了 ⇒ 用 popper-class + 全局块 */
.env-badge-popper .env-badge-tip {
    max-width: 320px;
    line-height: 1.5;
}

.env-badge-popper .env-badge-tip-title {
    margin-bottom: 2px;
    font-weight: 600;
}

.env-badge-popper .env-badge-tip-meta {
    margin-top: 4px;
    font-size: 12px;
    opacity: 0.72;
}
</style>

<style scoped>
.env-badge {
    position: fixed;
    right: 16px;
    bottom: 16px;
    z-index: 1500;
    display: inline-flex;
    gap: 6px;
    align-items: center;
    height: 22px;
    padding: 0 9px;
    color: var(--el-color-warning);
    font-size: 11px;
    font-weight: 700;
    line-height: 1;
    letter-spacing: 0.08em;
    cursor: default;
    user-select: none;
    background: var(--el-color-warning-light-9);
    border: 1px solid var(--el-color-warning-light-5);
    border-radius: 999px;
    box-shadow: var(--el-box-shadow-lighter);
}

.env-badge:focus-visible,
.env-badge:hover {
    opacity: 0.88;
    outline: none;
}

.env-badge.is-prod {
    color: var(--el-color-danger);
    background: var(--el-color-danger-light-9);
    border-color: var(--el-color-danger-light-5);
}

.env-badge-dot {
    width: 6px;
    height: 6px;
    background: currentColor;
    border-radius: 50%;
}
</style>
