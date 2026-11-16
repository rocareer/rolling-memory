package cli

import (
	"path"
	"strings"
)

// 归属标记：每次复制在文件头写一行 `// plugin: <名> v<版本>`（Vue 等写 `<!-- … -->`）
// 作为**审计留痕**。删除侧**不**扫描标记——以目录归属为依据（避免误删手工文件），
// 因此标记缺失不影响清理正确性，只影响留痕。
//
// 标记按文件形态选择注释载体（白名单外一律不写：JSON 之类无注释语法的格式写进去就是破损文件）。
// 写入是**幂等**的：文件头已是标记则原样返回——这保证「源 ↔ 落位副本」的 sha256 在反复安装后仍逐字一致
// （check-plugin-frontend-sync.sh 的比对前提）。
var markerPrefixes = map[string]struct {
	head string // 行首标记
	tail string // 行尾标记（仅块注释形态非空）
}{
	".go": {head: "//"}, ".ts": {head: "//"}, ".tsx": {head: "//"}, ".js": {head: "//"},
	".jsx": {head: "//"}, ".mjs": {head: "//"}, ".cjs": {head: "//"},
	".vue": {head: "<!--", tail: " -->"}, ".html": {head: "<!--", tail: " -->"}, ".md": {head: "<!--", tail: " -->"},
	".yaml": {head: "#"}, ".yml": {head: "#"},
	".sql": {head: "--"},
	".css": {head: "/*", tail: " */"}, ".scss": {head: "/*", tail: " */"}, ".less": {head: "/*", tail: " */"},
}

// OwnershipMarker 归属标记行（含行尾换行；rel 的扩展名不在白名单内 ⇒ 返回空串）
func OwnershipMarker(rel, name, version string) string {
	form, ok := markerPrefixes[strings.ToLower(path.Ext(rel))]
	if !ok {
		return ""
	}
	return form.head + " plugin: " + name + " v" + version + form.tail + "\n"
}

// ApplyOwnershipMarker 在文件头写入归属标记（幂等；白名单外的格式原样返回）
func ApplyOwnershipMarker(rel string, content []byte, name, version string) []byte {
	marker := OwnershipMarker(rel, name, version)
	if marker == "" || strings.HasPrefix(string(content), marker) {
		return content
	}
	out := make([]byte, 0, len(marker)+len(content))
	out = append(out, marker...)
	return append(out, content...)
}
