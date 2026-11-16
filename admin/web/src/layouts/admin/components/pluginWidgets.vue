<template>
    <div v-if="adminInfo.token" class="plugin-widgets-dock">
        <component :is="widget" v-for="(widget, path) in widgets" :key="path" />
    </div>
</template>

<script setup lang="ts">
import { useAdminInfo } from '@/stores/adminInfo'
import type { Component } from 'vue'

/**
 * 插件布局级组件挂载点（设计：插件前端贡献点的最小形态）。
 *
 * 约定路径：plugins/<名>/web/views/widget.vue → 落位副本 /src/views/admin/plugin/<名>/widget.vue。
 * 只收插件视图根目录下的 widget.vue（插件普通页面不会被挂到这里），后台所有布局共用本挂载点，
 * 因此浮标/浮层类展示件只写一个文件即可覆盖整个后台。
 *
 * 布局约定：右下角水平紧邻并排（`env-badge` DEV 在左侧，`rolling-memory` RM 心电浮标紧邻在右侧）。
 */
defineOptions({ name: 'PluginWidgets' })

const adminInfo = useAdminInfo()

const modules = import.meta.glob<{ default: Component }>('/src/views/admin/plugin/*/widget.vue', { eager: true })
const orderedEntries = Object.entries(modules).sort(([a], [b]) => {
    // 保证 env-badge (DEV) 恒在左侧第一位，rolling-memory 紧邻在其右侧
    const isEnvA = a.includes('/env-badge/') ? 0 : 1
    const isEnvB = b.includes('/env-badge/') ? 0 : 1
    if (isEnvA !== isEnvB) return isEnvA - isEnvB
    return a.localeCompare(b)
})
const widgets = Object.fromEntries(orderedEntries.map(([path, mod]) => [path, mod.default])) as Record<string, Component>
</script>

<style>
.plugin-widgets-dock {
    position: fixed;
    right: 16px;
    bottom: 14px;
    z-index: 1500;
    display: inline-flex;
    flex-direction: row;
    align-items: center;
    gap: 6px;
}

.plugin-widgets-dock > .env-badge,
.plugin-widgets-dock > .rm-widget-badge {
    position: static !important;
    right: auto !important;
    bottom: auto !important;
    height: 24px !important;
}
</style>
