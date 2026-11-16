-- ===== 存量库迁移基线（adopt）增列的回滚：adopted_through =====
-- 语义回退：删列即抹掉「adopt 装入」的状态事实 ⇒ 之后 `uninstall --purge` 不再被默认拒绝（门禁按该列判）。
-- 因此本回滚只用于「adopt 能力整体回退」；单独回退本列前，先确认没有 adopt 装入的插件在库
--（`aigo plugin list` 的「接管」列 / admin_plugins.adopted_through > 0），否则请先按插件链把结构补齐。
-- 幂等：DROP COLUMN IF EXISTS（列不存在 ⇒ 无操作）。
ALTER TABLE "__PREFIX__admin_plugins" DROP COLUMN IF EXISTS "adopted_through";
