package config

import (
	"os"
	"path/filepath"
	"strings"
)

// RollingConfigPath 返回记忆引擎（rolling-memory）的配置文件路径，口径与引擎一致：
// ROLLING_MEMORY_CONFIG → $XDG_CONFIG_HOME/rolling-memory/config.toml（默认 ~/.config/rolling-memory/config.toml）。
func RollingConfigPath() string {
	if p := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_CONFIG")); p != "" {
		return p
	}
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "rolling-memory", "config.toml")
}

// ParseRollingConfig 解析记忆引擎 config.toml 的扁平键值（"段.键" → 去引号后的原值）。
// 引擎侧配置是 TOML 子集（`[section]` + `key = value`），此处按同一形状逐行读取，
// 供控制台与管理面读取引擎运行开关；读取失败返回空映射（调用方各自取缺省）。
func ParseRollingConfig(path string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		out[section+"."+key] = strings.Trim(strings.TrimSpace(parts[1]), `"'`)
	}
	return out
}

// RollingMode 返回记忆引擎运行模式（local | cloud），口径与引擎一致：
// ROLLING_MEMORY_MODE → `[mode] name` → 默认 "local"。
func RollingMode() string {
	if envVal := strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_MODE"))); envVal != "" {
		return envVal
	}
	if val := strings.ToLower(strings.TrimSpace(ParseRollingConfig(RollingConfigPath())["mode.name"])); val != "" {
		return val
	}
	return "local"
}

// IsRollingLocalMode 报告记忆引擎是否运行在本地模式（纯本地金库、零云端请求）。
func IsRollingLocalMode() bool {
	return RollingMode() == "local"
}

// AdminLoginCaptchaDefault 报告登录验证码的默认开关：本地模式默认关闭，云端/公网默认开启。
// 仅作缺省值使用；`captcha.switches.admin_login` 显式配置（含环境变量）优先。
func AdminLoginCaptchaDefault() bool {
	return !IsRollingLocalMode()
}
