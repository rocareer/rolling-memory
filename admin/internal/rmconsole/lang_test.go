// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestPluginLangYAMLParses 守卫插件语言包的可解析性：en.yaml 曾因未加引号的
// ": " 值导致整个英文语言包被前端跳过（dev 只跳过本文件，生产会连累整个 en 包），
// 英文界面因此回退中文。语言包属于发布件，必须在单测里按 YAML 解析一次。
func TestPluginLangYAMLParses(t *testing.T) {
	for _, lang := range []string{"zh-cn", "en"} {
		path := filepath.Join("..", "..", "web", "src", "lang", lang, "plugin", "rolling-memory.yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s is not valid YAML: %v", path, err)
		}
		evalSection, ok := doc["eval"].(map[string]any)
		if !ok || len(evalSection) == 0 {
			t.Fatalf("%s missing non-empty eval section", path)
		}
		for _, key := range []string{"tabLedger", "tabRunDetail", "runBatchBtn"} {
			if _, exists := evalSection[key]; !exists {
				t.Fatalf("%s missing eval.%s", path, key)
			}
		}
	}
}
