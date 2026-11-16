<!--
  记忆图谱视图再导出壳：
  - 页面实现 = `views/admin/dashboard/graph.vue`；
  - 图谱引擎/布局/Worker = `@/components/graph/{engine,layout,layout.worker}.ts`；
  - 保留本文件以兼容菜单/权限节点中的 `plugin/rolling-memory/graph` 视图路径。
-->
<script lang="ts">
import GraphDashboardView from '@/views/admin/dashboard/graph.vue'

export default GraphDashboardView
</script>
