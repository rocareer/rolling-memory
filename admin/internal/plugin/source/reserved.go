package source

import (
	"fmt"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// 本通道（仓库来源 + 统一清单）新增占用的宿主路由段 / 页面目录：
//   - `/admin/plugin/source/...`、`/admin/plugin/catalog/...`（管理面接口）
//   - `web/src/views/admin/plugin/{source,catalog}/`（页面组件目录）
//
// 插件名若命中这两个词，它声明的分组规则名 `plugin/<名>` 与页面目录会与宿主页面互相顶替
// （vue-router 同名路由是「删旧加新」，属静默替换）。因此二者必须进宿主保留字。
//
// 保留字的**唯一正本**是 manifest.ReservedNames（安装期 manifest 校验读它）。本变量只是本通道的
// 登记声明，用途有二：
//
//	① 解析期提前拦截清单条目的 uid（见 CatalogReservedNames / PluginEntry.Validate），
//	   避免先下载包、再发现撞宿主页面；
//	② 漂移哨兵 CheckReservedSync：正本若漏了本通道的词，或静态门禁脚本的 RESERVED_NAMES
//	   与正本不一致，测试期即报红——三处同步（manifest.go / check-plugin-manifest.sh /
//	   plugins/README.md 的说明），缺一即「静态门禁与安装期校验不一致」。
var hostPageReserved = []string{"source", "catalog"}

// HostPageReserved 本通道登记的宿主保留字（只读副本，调用方不得改）。
func HostPageReserved() []string {
	return append([]string{}, hostPageReserved...)
}

// CheckReservedSync 校验保留字三处同步（传空串则跳过该处；三处都传 ⇒ 全量核对）：
//   - scriptText = 校验脚本 的内容（静态门禁那一处）；
//   - readmeText = plugins/README.md 的内容（给人看的说明那一处）。
//
// 判据：
//  1. 正本 manifest.ReservedNames 必须包含本通道登记的每个词（安装期校验口径）；
//  2. 静态门禁脚本 RESERVED_NAMES 的集合必须与正本**完全相等**：多一个词会让门禁误拒合法插件、
//     少一个词会让门禁漏放行。门禁用 grep 逐词匹配，故只比集合、不比顺序；
//  3. README 的保留字条目（`宿主保留字……不得命中` 那一行）列出的词同样必须与正本完全相等——
//     这处是人工维护的说明，最容易在加词时漏改，靠测试钉住，不靠人眼。
func CheckReservedSync(scriptText, readmeText string) error {
	for _, name := range hostPageReserved {
		if !containsString(manifest.ReservedNames, name) {
			return fmt.Errorf("保留字漂移：manifest.ReservedNames 缺少本通道登记的词 %q（正本现为 %s）",
				name, strings.Join(manifest.ReservedNames, "/"))
		}
	}
	if strings.TrimSpace(scriptText) != "" {
		listed, err := scriptReservedNames(scriptText)
		if err != nil {
			return err
		}
		if err := compareReservedSets("check-plugin-manifest.sh 的 RESERVED_NAMES", listed); err != nil {
			return err
		}
	}
	if strings.TrimSpace(readmeText) != "" {
		listed, err := readmeReservedNames(readmeText)
		if err != nil {
			return err
		}
		if err := compareReservedSets("plugins/README.md 的保留字说明", listed); err != nil {
			return err
		}
	}
	return nil
}

// compareReservedSets 与正本逐词比对（多、少都报，附方向以免改错地方）。
func compareReservedSets(where string, listed []string) error {
	for _, name := range manifest.ReservedNames {
		if !containsString(listed, name) {
			return fmt.Errorf("保留字漂移：%s 缺少 %q（该处会与安装期校验不一致）", where, name)
		}
	}
	for _, name := range listed {
		if !containsString(manifest.ReservedNames, name) {
			return fmt.Errorf("保留字漂移：%s 多出 %q（正本没有这个词）", where, name)
		}
	}
	return nil
}

// scriptReservedNames 从门禁脚本正文里取 RESERVED_NAMES="..." 的词表。
// 解析失败（该行被删/改名）即报错——静默跳过等于门禁失效。
func scriptReservedNames(scriptText string) ([]string, error) {
	const key = "RESERVED_NAMES="
	for _, line := range strings.Split(scriptText, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, key) {
			continue
		}
		value := strings.Trim(strings.TrimPrefix(trimmed, key), `"'`)
		fields := strings.Fields(value)
		if len(fields) == 0 {
			return nil, fmt.Errorf("保留字漂移：check-plugin-manifest.sh 的 RESERVED_NAMES 为空")
		}
		return fields, nil
	}
	return nil, fmt.Errorf("保留字漂移：check-plugin-manifest.sh 未找到 %s 行", key)
}

// readmeReservedNames 从 plugins/README.md 的保留字条目里取反引号括起的词表。
// 定位方式 = 同时含「宿主保留字」与「不得命中」的第一行（见 plugins/README.md）；
// 找不到该行、或该行一个反引号词都没有 ⇒ 报错（说明被删/改写，哨兵必须显形而不是静默通过）。
func readmeReservedNames(readmeText string) ([]string, error) {
	const anchor, marker = "宿主保留字", "不得命中"
	for _, line := range strings.Split(readmeText, "\n") {
		if !strings.Contains(line, anchor) || !strings.Contains(line, marker) {
			continue
		}
		words := backtickedWords(line)
		if len(words) == 0 {
			return nil, fmt.Errorf("保留字漂移：plugins/README.md 的保留字条目里没有反引号词表")
		}
		return words, nil
	}
	return nil, fmt.Errorf("保留字漂移：plugins/README.md 未找到含 %q 与 %q 的保留字条目行", anchor, marker)
}

// backtickedWords 取一行里所有 `code` 形式的内容（按出现顺序）。
func backtickedWords(line string) []string {
	out := make([]string, 0, 8)
	for rest := line; ; {
		open := strings.IndexByte(rest, '`')
		if open < 0 {
			return out
		}
		rest = rest[open+1:]
		closeAt := strings.IndexByte(rest, '`')
		if closeAt < 0 {
			return out
		}
		if word := strings.TrimSpace(rest[:closeAt]); word != "" {
			out = append(out, word)
		}
		rest = rest[closeAt+1:]
	}
}
