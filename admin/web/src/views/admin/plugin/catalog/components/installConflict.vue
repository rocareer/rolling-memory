<!--
  界面结构与组件用法移植自 BuildAdmin v2.3.9
  https://gitee.com/wonderful-code/buildadmin
  Apache-2.0，Copyright 妙码生花
  已修改：冲突只读阻断；删除了「备份并覆盖 / 丢弃新文件」选项与依赖冲突流程。
  catalog 安装固定 Force:false，不覆盖宿主已有文件。
-->
<template>
    <div class="install-conflict">
        <el-alert type="warning" :closable="false" show-icon :title="t('plugin.source.catalog.conflict.title')" />
        <el-table :data="rows" stripe border class="w100">
            <el-table-column prop="target" :label="t('plugin.source.catalog.conflict.target')" min-width="320" show-overflow-tooltip />
        </el-table>
        <div class="install-hint">{{ t('plugin.source.catalog.conflict.hint') }}</div>
    </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

interface Props {
    targets: string[]
}

const props = withDefaults(defineProps<Props>(), {
    targets: () => [],
})

const { t } = useI18n()
const rows = computed(() => props.targets.map((target) => ({ target })))
</script>

<style scoped lang="scss">
.install-conflict {
    min-height: 120px;
}
.install-hint {
    padding: 12px 0;
    font-size: 12px;
    line-height: 20px;
    color: var(--el-text-color-secondary);
}
</style>
