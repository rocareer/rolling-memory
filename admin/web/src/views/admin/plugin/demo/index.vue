<!-- plugin: demo v1.0.0 -->
<!--
    示例插件的 L1 自定义页。
    本文件是**源**（plugins/demo/web/views/index.vue）；落位副本 web/src/views/admin/plugin/demo/index.vue
    由安装器复制，两者逐字节一致（同步检查比对 sha256）。
    菜单 component 必须落 /src/views/admin/plugin/**，否则前端按精确路径查表时静默跳过（F7）。

    语言包键前缀 plugin.demo.*（由落位路径 web/src/lang/<语言>/plugin/demo.yaml 生成，F8）。
-->
<template>
    <div class="default-main plugin-demo">
        <el-card shadow="never">
            <template #header>
                <div class="plugin-demo-header">
                    <span>{{ t('plugin.demo.title') }}</span>
                    <el-tag size="small" type="info">{{ t('plugin.demo.tag') }}</el-tag>
                </div>
            </template>

            <el-alert class="plugin-demo-alert" type="info" :closable="false" show-icon :title="t('plugin.demo.intro')" />

            <el-descriptions v-loading="loading" :column="2" border size="small">
                <el-descriptions-item :label="t('plugin.demo.fields.name')">{{ overview.name || '-' }}</el-descriptions-item>
                <el-descriptions-item :label="t('plugin.demo.fields.version')">{{ overview.version || '-' }}</el-descriptions-item>
                <el-descriptions-item :label="t('plugin.demo.fields.kind')">{{ overview.kind || '-' }}</el-descriptions-item>
                <el-descriptions-item :label="t('plugin.demo.fields.hostApi')">{{ overview.host_api_version || '-' }}</el-descriptions-item>
                <el-descriptions-item :label="t('plugin.demo.fields.hostVersion')">{{ overview.host_version || '-' }}</el-descriptions-item>
                <el-descriptions-item :label="t('plugin.demo.fields.entityTable')">{{ overview.entity_table || '-' }}</el-descriptions-item>
                <el-descriptions-item :label="t('plugin.demo.fields.entity')">{{ t('plugin.demo.entityHint') }}</el-descriptions-item>
                <el-descriptions-item :label="t('plugin.demo.fields.note')">{{ overview.note || '-' }}</el-descriptions-item>
                <el-descriptions-item :label="t('plugin.demo.fields.pageHint')" :span="2">{{ overview.page_hint || '-' }}</el-descriptions-item>
            </el-descriptions>

            <div class="plugin-demo-actions">
                <el-button v-blur :loading="loading" type="primary" @click="load">{{ t('plugin.demo.refresh') }}</el-button>
            </div>
        </el-card>
    </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import request from '@/utils/request'

const { t } = useI18n()

/**
 * 插件自身端点的回执（契约见 plugins/demo/plugin.go 的 overview）：
 * POST /admin/plugin/demo/overview —— 相对段 /overview 由插件包的 mount 挂在宿主建好的 plugin/demo 子组上
 * （绝对前缀由宿主提供，插件不写；权限节点 plugin/demo/overview 由 manifest 的 menus 声明）。
 *
 * 为什么直接 import '@/utils/request' 而不建 api/ 文件：插件的落位规则是闭集——web/ 下只允许
 * views/** 与 lang/<语言>.yaml（internal/plugin/cli/plan.go 的 planCopyTarget），插件前端无法往宿主的
 * web/src/api/** 里加文件；请求层的 baseURL、Bearer token、重复请求取消由它照常承担。
 */
interface DemoOverview {
    name: string
    version: string
    kind: string
    host_api_version: string
    host_version: string
    entity: string
    entity_table: string
    note: string
    page_hint: string
}

const loading = ref(false)
const overview = ref<Partial<DemoOverview>>({})

const load = async () => {
    loading.value = true
    try {
        const res = await request<DemoOverview>({ url: '/admin/plugin/demo/overview', method: 'POST', data: {} })
        overview.value = res.data.data ?? {}
    } catch {
        // 请求层已统一提示（网络错误 / code≠0 / 401）；这里只保证不残留半截数据
        overview.value = {}
    } finally {
        loading.value = false
    }
}

onMounted(() => load())
</script>

<style scoped lang="scss">
.plugin-demo {
    .plugin-demo-header {
        display: flex;
        gap: 8px;
        align-items: center;
        justify-content: space-between;
    }

    .plugin-demo-alert {
        margin-bottom: 12px;
    }

    .plugin-demo-actions {
        margin-top: 12px;
    }
}
</style>
