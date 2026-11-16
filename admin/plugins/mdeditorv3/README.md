<!-- plugin: mdeditorv3 v1.0.6 -->
# mdeditorv3 — Markdown 编辑器与解析器基础插件（v1.0.6）

`mdeditorv3` 是 `rolling-admin`（`ai-go-admin`）生态的标准基础插件（`kind: code`），为系统注入完整的 **Markdown 语法解析渲染（`MdPreview`）**、**Markdown 富文本双栏实时编辑（`MdEditor`）** 与 **章节目录大纲提取（`MdCatalog`）** 能力。

## 能力清单

1. **表单多编辑器自动挂载（`agInput`）**：
   - 落位视图 `web/src/views/admin/plugin/mdeditorv3/editor.vue` 与 `web/src/components/agInput/components/editor/md-v3.vue` 自动注册到 `agInput` 的 `type="editor"` 组件池；
   - 未显式指定其它编辑器时自动作为系统默认 Markdown 富文本编辑器（替代默认的 `pleaseInstallEditor` 提示）。
2. **双形态复用（编辑模式 + 纯解析预览模式）**：
   - **编辑模式（默认 `previewOnly=false`）**：提供双栏实时预览、代码行号、工具栏排版、全屏编辑（`screenfull`）、代码美化（`prettier`）与图片上传（对接宿主 `/common/upload`）；
   - **纯解析预览模式（`previewOnly=true`）**：提供零边框高保真 Markdown 渲染与 `@catalogChange` 章节目录提取事件，供帮助中心、知识库、公告详情等页面直接调用。
3. **主题与多语言自适应**：
   - 自动跟随宿主 `config.layout.dark` 切换 `dark` / `light` 主题；
   - 自动跟随宿主 `config.lang.active` 切换 `zh-CN` / `en-US` 界面语言。
