# Third-Party Notices

## AI GO ADMIN

The admin console under `admin/` is derived from AI GO ADMIN (Go + Gin + Vue 3 admin framework).

- Project: AI GO ADMIN (ai-go-admin)
- Source: https://github.com/ai-go-hub/ai-go-admin (mirrors: Gitee / AtomGit)
- License: Apache License 2.0
- Copyright: AI GO ADMIN contributors
- Relationship: derivative work. Rolling-Memory-specific console code lives under `internal/rmconsole`, `web/src/views/admin/plugin/rolling-memory`, `web/src/views/admin/dashboard`, and related routes/APIs.
- Modifications: added/replaced Rolling-Memory pages, APIs, permissions, routes, branding, memory console, ROI monitor, graph, and evaluation lab; upstream demo/payment/market code was removed where it did not apply.

`admin/LICENSE` (Apache-2.0) is retained. See also `admin/NOTICE`.

## BuildAdmin

The plugin catalog UI under `web/src/views/admin/plugin/catalog/` is ported from BuildAdmin.

- Project: BuildAdmin
- Source: https://gitee.com/wonderful-code/buildadmin
- Upstream version: v2.3.9
- Upstream commit: 468652294e6a6a794bf1113f0847b93337d69fb4
- License: Apache License 2.0
- Copyright: 妙码生花

Files modelled on BuildAdmin (structure and component usage retained; adapted to this project's plugin catalog fields, APIs, permissions and Element Plus theme; `commonDone.vue` is a rewrite rather than a port):

- `web/src/views/admin/plugin/catalog/index.vue` (from `web/src/views/backend/module/index.vue`)
- `web/src/views/admin/plugin/catalog/components/tableHeader.vue` (from `components/tableHeader.vue`)
- `web/src/views/admin/plugin/catalog/components/tabs.vue` (from `components/tabs.vue`)
- `web/src/views/admin/plugin/catalog/components/goodsInfo.vue` (from `components/goodsInfo.vue`)
- `web/src/views/admin/plugin/catalog/components/commonDialog.vue` (from `components/commonDialog.vue`)
- `web/src/views/admin/plugin/catalog/components/commonSelectVersion.vue` (from `components/commonSelectVersion.vue`)
- `web/src/views/admin/plugin/catalog/components/installConflict.vue` (from `components/installConflict.vue`)
- `web/src/views/admin/plugin/catalog/components/commonDone.vue` — rewritten for this project, not a port: only the overall result-page structure (summary + hint + close/next buttons) is modelled on `components/commonDone.vue`; its markup (`el-result` / `el-descriptions`), layout and CSS classes are this project's own.

Modifications: fields, request payloads, permission nodes, routing, icons and CSS variables were replaced with this project's equivalents; account, purchase, payment, upload-install, market APIs, BuildAdmin store/types and overwrite-style conflict resolution were removed. Each ported file carries a header stating the origin and the modifications.

Not copied: BuildAdmin trademarks, logos, images, payment icons, account and marketplace APIs.

This project's existing `LICENSE` (Apache-2.0) is retained unchanged.
