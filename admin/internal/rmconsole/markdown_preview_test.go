// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMemoryContentMarkdownPreviewContract 守卫「记忆正文各展示面统一阅读面」契约：
// ① 共享阅读组件以内核 md-editor-v3 的 MdPreview 渲染；正文里的原始 HTML 由 MdPreview 自带的
// XSSPlugin 过滤（非白名单标签以字面文本呈现），代码高亮注册本地 hljs 实例、不请求 CDN；
// ② 自动格式化走 memoryMarkdown 纯函数（格式化测试文件必须同在），不再自维护渲染器、
// 不再提供手动预览/原文切换；
// ③ 三层浏览器 / 检索调试台 / 云端回忆 / ROI 证据抽屉四个正文阅读面必须经该组件展示，
// 且已接入自动格式化的两个弹窗必须传内容线索（:hint=），不得各自复制第二套渲染实现；
// ④ 中英语言包必须同时提供复制原文键与单元正文标题键（1:1 对称，缺一即红）。
func TestMemoryContentMarkdownPreviewContract(t *testing.T) {
	webRoot := filepath.Join("..", "..", "web", "src")
	viewsRoot := filepath.Join(webRoot, "views", "admin", "plugin", "rolling-memory")

	component, err := os.ReadFile(filepath.Join(viewsRoot, "MarkdownPreview.vue"))
	if err != nil {
		t.Fatalf("read MarkdownPreview.vue: %v", err)
	}
	componentSource := string(component)
	for _, required := range []string{
		"from 'md-editor-v3'",
		"MdPreview",
		"XSSPlugin",
		"toMemoryMarkdown",
		"data-testid=\"md-preview-copy\"",
	} {
		if !strings.Contains(componentSource, required) {
			t.Fatalf("MarkdownPreview.vue missing md-editor-v3 reading-surface contract %q", required)
		}
	}
	for _, retired := range []string{"renderMarkdownDocument", "md-preview-mode"} {
		if strings.Contains(componentSource, retired) {
			t.Fatalf("MarkdownPreview.vue must not keep the retired hand-rolled renderer/manual toggle: %q", retired)
		}
	}

	for _, formatter := range []string{"memoryMarkdown.ts", "memoryMarkdown.test.mjs"} {
		if _, err := os.Stat(filepath.Join(viewsRoot, formatter)); err != nil {
			t.Fatalf("memoryMarkdown formatter artifact missing: %v", err)
		}
	}

	for name, path := range map[string]string{
		"layerBrowser":       filepath.Join(viewsRoot, "layerBrowser.vue"),
		"recall":             filepath.Join(viewsRoot, "recall.vue"),
		"eval":               filepath.Join(viewsRoot, "eval.vue"),
		"cloudAccountDialog": filepath.Join(viewsRoot, "cloudAccountDialog.vue"),
		"roi":                filepath.Join(webRoot, "views", "admin", "dashboard", "roi.vue"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s view: %v", name, err)
		}
		if !strings.Contains(string(data), "MarkdownPreview") {
			t.Fatalf("%s view does not render memory content through MarkdownPreview", name)
		}
	}

	// 表格/列表悬停预览必须走懒挂载富格式悬浮卡 MemoryHoverPreview（内部复用 MarkdownPreview），
	// 取代将换行与代码压成单行纯文本的原生 show-overflow-tooltip。
	hoverComponent, err := os.ReadFile(filepath.Join(viewsRoot, "MemoryHoverPreview.vue"))
	if err != nil {
		t.Fatalf("read MemoryHoverPreview.vue: %v", err)
	}
	for _, required := range []string{"MarkdownPreview", "rm-md-hover-popper", "everOpened"} {
		if !strings.Contains(string(hoverComponent), required) {
			t.Fatalf("MemoryHoverPreview.vue missing lazy hover-card contract %q", required)
		}
	}
	for name, path := range map[string]string{
		"layerBrowser": filepath.Join(viewsRoot, "layerBrowser.vue"),
		"recall":       filepath.Join(viewsRoot, "recall.vue"),
		"eval":         filepath.Join(viewsRoot, "eval.vue"),
		"roi":          filepath.Join(webRoot, "views", "admin", "dashboard", "roi.vue"),
		"artifacts":    filepath.Join(webRoot, "views", "admin", "dashboard", "artifacts.vue"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s view: %v", name, err)
		}
		if !strings.Contains(string(data), "MemoryHoverPreview") {
			t.Fatalf("%s view does not render list hover tooltips through MemoryHoverPreview", name)
		}
	}

	// 两个「记忆单元溯源」弹窗必须把层级/来源文件线索交给自动格式化（否则 JSON/源码拿不到围栏）。
	for name, path := range map[string]string{
		"layerBrowser": filepath.Join(viewsRoot, "layerBrowser.vue"),
		"recall":       filepath.Join(viewsRoot, "recall.vue"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s view: %v", name, err)
		}
		if !strings.Contains(string(data), ":hint=") {
			t.Fatalf("%s view must pass content hints (:hint=) into MarkdownPreview", name)
		}
	}

	for _, langFile := range []string{
		filepath.Join(webRoot, "lang", "zh-cn", "plugin", "rolling-memory.yaml"),
		filepath.Join(webRoot, "lang", "en", "plugin", "rolling-memory.yaml"),
	} {
		data, err := os.ReadFile(langFile)
		if err != nil {
			t.Fatalf("read preview labels %s: %v", langFile, err)
		}
		langSource := string(data)
		for _, required := range []string{"copyRaw:", "copied:", "reloadBody:", "contentTitle:"} {
			if !strings.Contains(langSource, required) {
				t.Fatalf("%s missing preview label %q", langFile, required)
			}
		}
		for _, retired := range []string{"modePreview:", "modeRaw:"} {
			if strings.Contains(langSource, retired) {
				t.Fatalf("%s must not keep the retired manual-toggle label %q", langFile, retired)
			}
		}
	}
}
