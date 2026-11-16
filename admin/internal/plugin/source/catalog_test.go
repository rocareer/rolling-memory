package source

import (
	"strings"
	"testing"
)

const testSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func validCatalogYAML() string {
	return `schema: ai-go-plugin-catalog/v1
generated_at: '2026-10-23T10:09:38Z'
plugins:
  - uid: demo
    version: 1.0.0
    title_zh: 示例插件
    title_en: Demo Plugin
    desc_zh: 插件包样板。
    desc_en: Sample plugin package.
    kind: mixed
    host_api: '>=1 <2'
    framework_req: '>=0.1 <1'
    license: Apache-2.0
    requires: []
    package:
      url: https://example.com/demo-1.0.0.zip
      size: 14211
      sha256: ` + testSHA + `
      signed: false
`
}

func TestParseCatalogValid(t *testing.T) {
	if _, err := ParseCatalog([]byte("schema: ai-go-plugin-catalog/v1\nplugins: []\n")); err != nil {
		t.Fatalf("空插件清单应合法: %v", err)
	}
	catalog, err := ParseCatalog([]byte(validCatalogYAML()))
	if err != nil {
		t.Fatalf("合法清单被拒: %v", err)
	}
	if len(catalog.Plugins) != 1 || catalog.Plugins[0].UID != "demo" {
		t.Fatalf("解析结果不符: %+v", catalog.Plugins)
	}
	entry, ok := catalog.Entry("demo", "1.0.0")
	if !ok || entry.Package.Size != 14211 {
		t.Fatalf("Entry 查询失败: %+v", entry)
	}
}

func TestParseCatalogRejects(t *testing.T) {
	valid := validCatalogYAML()
	cases := []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"schema missing", strings.Replace(valid, "schema: ai-go-plugin-catalog/v1\n", "", 1)},
		{"schema wrong", strings.Replace(valid, "ai-go-plugin-catalog/v1", "ai-go-plugin-catalog/v2", 1)},
		{"unknown top key", valid + "extra: 1\n"},
		{"unknown entry key", strings.Replace(valid, "    kind: mixed\n", "    kind: mixed\n    unknown_entry: 1\n", 1)},
		{"unknown package key", strings.Replace(valid, "      signed: false\n", "      signed: false\n      force: true\n", 1)},
		{"duplicate key", strings.Replace(valid, "schema: ai-go-plugin-catalog/v1\n", "schema: ai-go-plugin-catalog/v1\nschema: ai-go-plugin-catalog/v1\n", 1)},
		{"anchor", strings.Replace(valid, "generated_at: '2026-10-23T10:09:38Z'", "generated_at: &g '2026-10-23T10:09:38Z'", 1)},
		{"alias", strings.Replace(valid, "generated_at: '2026-10-23T10:09:38Z'", "generated_at: *g", 1)},
		{"merge key", strings.Replace(valid, "schema: ai-go-plugin-catalog/v1\n", "schema: ai-go-plugin-catalog/v1\n<<: {}\n", 1)},
		{"custom tag", strings.Replace(valid, "ai-go-plugin-catalog/v1", "!catalog ai-go-plugin-catalog/v1", 1)},
		{"multi document", valid + "---\nschema: ai-go-plugin-catalog/v1\nplugins: []\n"},
		{"deep nesting", "schema: ai-go-plugin-catalog/v1\nplugins: []\n" + "a: " + strings.Repeat("[", 25) + strings.Repeat("]", 25) + "\n"},
		{"generated_at invalid", strings.Replace(valid, "2026-10-23T10:09:38Z", "yesterday", 1)},
		{"duplicate uid version", duplicateEntry(valid)},
		{"reserved uid", strings.Replace(valid, "uid: demo", "uid: source", 1)},
		{"invalid uid", strings.Replace(valid, "uid: demo", "uid: Demo", 1)},
		{"invalid version", strings.Replace(valid, "version: 1.0.0", "version: 1.0", 1)},
		{"prerelease version", strings.Replace(valid, "version: 1.0.0", "version: 1.0.0-beta", 1)},
		{"invalid kind", strings.Replace(valid, "kind: mixed", "kind: other", 1)},
		{"host_api not string", strings.Replace(valid, "host_api: '>=1 <2'", "host_api: 1", 1)},
		{"host_api invalid domain", strings.Replace(valid, "host_api: '>=1 <2'", "host_api: '>=x'", 1)},
		{"title empty", strings.Replace(valid, "title_zh: 示例插件", "title_zh: ''", 1)},
		{"title too long", strings.Replace(valid, "title_zh: 示例插件", "title_zh: "+strings.Repeat("字", 65), 1)},
		{"desc control char", strings.Replace(valid, "desc_zh: 插件包样板。", `desc_zh: "bad\u0001text"`, 1)},
		{"package both", strings.Replace(valid, "      url: https://example.com/demo-1.0.0.zip\n", "      url: https://example.com/demo-1.0.0.zip\n      path: ./demo.zip\n", 1)},
		{"package neither", strings.Replace(valid, "      url: https://example.com/demo-1.0.0.zip\n", "", 1)},
		{"package url userinfo", strings.Replace(valid, "https://example.com/demo-1.0.0.zip", "https://user:pass@example.com/demo.zip", 1)},
		{"package url query", strings.Replace(valid, "https://example.com/demo-1.0.0.zip", "https://example.com/demo.zip?token=x", 1)},
		{"package url fragment", strings.Replace(valid, "https://example.com/demo-1.0.0.zip", "https://example.com/demo.zip#x", 1)},
		{"package path escape", strings.Replace(valid, "      url: https://example.com/demo-1.0.0.zip\n", "      path: ../demo.zip\n", 1)},
		{"package size zero", strings.Replace(valid, "size: 14211", "size: 0", 1)},
		{"package size too large", strings.Replace(valid, "size: 14211", "size: 20971521", 1)},
		{"sha uppercase", strings.Replace(valid, testSHA, strings.ToUpper(testSHA), 1)},
		{"sha short", strings.Replace(valid, testSHA, "abc", 1)},
		{"signed missing", strings.Replace(valid, "      signed: false\n", "", 1)},
		{"signed not bool", strings.Replace(valid, "signed: false", "signed: no", 1)},
		{"requires self", strings.Replace(valid, "requires: []", "requires:\n      - demo@>=1", 1)},
		{"requires duplicate", strings.Replace(valid, "requires: []", "requires:\n      - dep@>=1\n      - dep@<2", 1)},
		{"requires invalid domain", strings.Replace(valid, "requires: []", "requires:\n      - dep@>=x", 1)},
		{"requires not sequence", strings.Replace(valid, "requires: []", "requires: dep@>=1", 1)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := ParseCatalog([]byte(testCase.data)); err == nil {
				t.Fatalf("应当拒绝但通过了: %s", testCase.name)
			}
		})
	}
}

func TestParseCatalogLimits(t *testing.T) {
	if _, err := ParseCatalog([]byte(strings.Repeat("#", int(MaxCatalogBytes)+1))); err == nil {
		t.Fatal("超过 1 MiB 的清单应被拒绝")
	}

	var builder strings.Builder
	builder.WriteString("schema: ai-go-plugin-catalog/v1\nplugins:\n")
	for i := 0; i < MaxCatalogEntries+1; i++ {
		builder.WriteString("  - uid: p")
		builder.WriteString(strings.Repeat("a", 4))
		builder.WriteString(itoa(i))
		builder.WriteString("\n    version: 1.0.0\n    title_zh: 标题\n    title_en: Title\n    desc_zh: 简介\n    desc_en: Desc\n    kind: code\n    host_api: '>=1 <2'\n    package:\n      url: https://example.com/p.zip\n      size: 1\n      sha256: ")
		builder.WriteString(testSHA)
		builder.WriteString("\n      signed: false\n")
	}
	if _, err := ParseCatalog([]byte(builder.String())); err == nil {
		t.Fatal("超过 1000 条的清单应被拒绝")
	}
}

func TestValidateForOrigin(t *testing.T) {
	localRelative := strings.Replace(validCatalogYAML(), "https://example.com/demo-1.0.0.zip", "./demo-1.0.0.zip", 1)
	catalog, err := ParseCatalog([]byte(localRelative))
	if err != nil {
		t.Fatalf("相对 URL 在解析期应允许: %v", err)
	}
	if err := catalog.ValidateForOrigin(false); err == nil {
		t.Fatal("本地清单的相对 package.url 应被拒绝")
	}

	catalog, err = ParseCatalog([]byte(validCatalogYAML()))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateForOrigin(false); err != nil {
		t.Fatalf("本地清单的绝对 package.url 应允许: %v", err)
	}

	remotePath := strings.Replace(validCatalogYAML(), "      url: https://example.com/demo-1.0.0.zip\n", "      path: ./demo-1.0.0.zip\n", 1)
	catalog, err = ParseCatalog([]byte(remotePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateForOrigin(true); err == nil {
		t.Fatal("远程清单的 package.path 应被拒绝")
	}
	if err := catalog.ValidateForOrigin(false); err != nil {
		t.Fatalf("本地清单的 package.path 应允许: %v", err)
	}
}

func TestNormalizeRequires(t *testing.T) {
	canonical, parsed, err := NormalizeRequires("demo", []string{"depb@<2", "depa@>=1 <2"})
	if err != nil {
		t.Fatalf("合法依赖被拒: %v", err)
	}
	if canonical != "depa@>=1 <2,depb@<2" {
		t.Fatalf("规范化结果不符: %s", canonical)
	}
	if parsed["depa"] != ">=1 <2" || parsed["depb"] != "<2" {
		t.Fatalf("依赖 map 不符: %+v", parsed)
	}
	if _, _, err := NormalizeRequires("demo", []string{"demo@>=1"}); err == nil {
		t.Fatal("自依赖应被拒绝")
	}
	if _, _, err := NormalizeRequires("demo", []string{"depa@>=1", "depa@<2"}); err == nil {
		t.Fatal("重复 uid 应被拒绝")
	}
	if _, _, err := NormalizeRequires("demo", []string{"depa@^1"}); err == nil {
		t.Fatal("不支持的版本域应被拒绝")
	}
}

func duplicateEntry(document string) string {
	parts := strings.SplitN(document, "plugins:\n", 2)
	if len(parts) != 2 {
		return document
	}
	return parts[0] + "plugins:\n" + parts[1] + parts[1]
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
