-- ===== 插件管理页菜单行与节点挂靠（web/src/views/admin/plugin/index.vue 的入口行）=====
-- 设计口径：管理面「只对超管开放」的落点——菜单行与节点一律不授予任何角色组。本文件补的正是
-- 000004_plugin.up.sql ⑤ 段预告的那一步——该段只种 3 个节点且 pid=NULL，附注原文：「管理页菜单
-- （web/src/views/admin/plugin/index.vue）随后续版本补建，届时把本 3 个节点的 pid 指向该菜单」。
--
-- 为什么单独一件而不改 000004 的 ⑤ 段：迁移**应用后冻结**（修正走新迁移）；
-- 且已应用过 000004 的库（本地开发库、先落该批变更的下游）不会再跑那一件——本文件对「已应用 000004」与
-- 「全新库」两条路径都生效（全新库：000004 建节点 → 本文件补菜单并挂靠），不改既有迁移的字节。
--
-- 幂等：菜单行 INSERT … WHERE NOT EXISTS（按 name+type 判重）；挂靠走 UPDATE … IS DISTINCT FROM
-- （UPDATE … IS DISTINCT FROM 幂等写法，pid 已就位即不写）。本文件可重复应用，不改既有迁移的字节。
--
-- 菜单字段口径（对齐既有顶层菜单先例，见 cmd/migrate/migrations/000003_seed.up.sql）：
--   type=menu、name 与 path 都取 `plugin`——前端路由名即规则 name，path 是相对后台入口的完整路径；
--     前端 authNode 键 =「后台入口 + path」= /admin/plugin，与页面 getCurrentRoutePath()+can() 的口径
--     逐字对应（节点末段 read/enable/disable ⇒ /admin/plugin/{read,enable,disable}）。
--   component 必须落在 web/src/views/admin/ 下且写精确路径（import.meta.glob 按字符串查表，落别处会静默查不到）；
--   open_type='tab' + keepalive=1（页面 defineOptions name = 菜单 name = plugin，按组件名匹配的 keep-alive 成立）；
--   pid=NULL、status=1；weigh 989 排在既有顶层菜单之后（dashboard 999 / crud 996 等；按 weigh desc 排序）
--     ——不挤占既有排序。
INSERT INTO "__PREFIX__admin_rules" ("pid", "type", "title", "name", "path", "icon", "open_type", "url", "component", "keepalive", "extend", "remark", "weigh", "status", "updated_at", "created_at")
SELECT NULL, 'menu', '插件管理', 'plugin', 'plugin', 'lucide-puzzle', 'tab', '', '/src/views/admin/plugin/index.vue', 1, '', '插件机制台账：安装状态、启停、详情与运行时对照（不授予角色组：默认只有超管可见）', 989, 1, NOW(), NOW()
WHERE NOT EXISTS (SELECT 1 FROM "__PREFIX__admin_rules" WHERE "name" = 'plugin' AND "type" = 'menu');

-- 把 000004 种的 3 个管理面节点挂到上面那条菜单名下（pid 不写死 ID；节点名 = 中间件由 FullPath 推导的
-- checkPath：plugin/list、plugin/get 回落 plugin/read，enable/disable 精确匹配）。
-- 授权关系不动：这 3 个节点本就不在任何角色组的 rules 里，挂靠只改树结构、不改可见性裁决。
UPDATE "__PREFIX__admin_rules" AS n
SET "pid" = m."id",
    "updated_at" = NOW()
FROM "__PREFIX__admin_rules" AS m
WHERE m."name" = 'plugin'
  AND m."type" = 'menu'
  AND n."name" IN ('plugin/read', 'plugin/enable', 'plugin/disable')
  AND n."type" = 'node'
  AND n."pid" IS DISTINCT FROM m."id";