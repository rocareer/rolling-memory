package rollingbrand

import (
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestPluginLangYAMLParses 守卫品牌包语言包的可解析性：值里出现未加引号的
// ": " 会按映射嵌套解析而整包 fallback 中文，品牌登录页/欢迎语随之回退。
// 语言包属于发布件，必须在单测里按 YAML 解析一次，并核对登录页与欢迎语用到的键。
func TestPluginLangYAMLParses(t *testing.T) {
	for _, name := range []string{"zh-cn.yaml", "en.yaml"} {
		path := filepath.Join("web", "lang", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s is not valid YAML: %v", path, err)
		}
		for _, key := range []string{
			"brandSystem",
			"brandTriad",
			"heroTitle",
			"heroDescription",
			"welcomeTip",
			"presetTip",
			"presetFill",
		} {
			if value, exists := doc[key]; !exists || value == "" {
				t.Fatalf("%s missing non-empty %s", path, key)
			}
		}
	}
}
