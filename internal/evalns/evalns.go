// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package evalns 是「评测命名空间」判定的唯一正本：哪些 scope 属于隔离的基准评测空间，
// 以及这些 scope 的 (suite, 脉冲模式, 世代号) 解析。
//
// 为什么独立成包：同一个判定要被两端共用，且两端都不该为此引入对方的依赖——
//   - `internal/eval`（评测流水线）用它解析 scope 轴；
//   - `internal/api`（产品生命周期）用它把自动 L3 Noetic 结晶挡在评测空间之外。
//
// 命名空间闭集（正本 internal/eval/cloud_engine.go 的 ScopeForSuiteAndMode）：
//   - <user>-mem-eval-<locomo|atmbench>[-pulse-cipher][-<世代号>] => pulse_cipher（脉冲密文）
//   - <user>-mem-eval-<locomo|atmbench>-pulse-plain[-<世代号>]    => pulse_plain （脉冲明文，兼容 -plaintext）
//   - <user>-mem-eval-<locomo|atmbench>-local-plain[-<世代号>]    => local_plain （本地明文，兼容 -local）
//
// 引擎侧落库时 scope 还会带上分身前缀（`ast:default:inc:<user>-mem-eval-...`）。
package evalns

import "strings"

const (
	// Marker 是评测 scope 的命名空间标记（历史固定形态与世代形态共用）。
	Marker = "-mem-eval-"
	// MaxScopeLen 是 scope 的长度上限（与引擎侧校验一致）。
	MaxScopeLen = 191

	// SuiteLoCoMo 是 LoCoMo 套件在 scope 中的线名。
	SuiteLoCoMo = "locomo"
	// SuiteATMWire 是 ATM-Bench 套件在 scope 中的线名（内部套件名是 "atm"）。
	SuiteATMWire = "atmbench"

	// ModePulseCipher 是脉冲密文模式（唯一合法名）。
	ModePulseCipher = "pulse_cipher"
	// ModePulsePlain 是脉冲明文模式（唯一合法名）。
	ModePulsePlain = "pulse_plain"
	// ModeLocalPlain 是本地明文离线模式（唯一合法名）。
	ModeLocalPlain = "local_plain"
)

// IsBenchmarkScope 判定 scope 是否属于隔离的评测命名空间。
//
// 它比 Split 宽松一档，因为用途不同：Split 用于「解析并据此建引擎」（必须严格闭集），
// 本判定用于「评测空间内禁止产品副作用」（宁可多拦，绝不漏拦）。因此接受：
//   - 裸形态：`u10-mem-eval-atmbench-20261107t161739z-187d`
//   - 引擎落库形态（带分身前缀）：`ast:default:inc:u10-mem-eval-atmbench-...`
//
// 判据只认「标记 + 合法套件名」，避免把用户空间（`mem:self` / `ast:default:inc:10` /
// `mem:person:front-1`）或含 "eval" 字样的普通名字（`u10-mem-eval-team`）误判为评测空间。
func IsBenchmarkScope(scope string) bool {
	clean := strings.ToLower(strings.TrimSpace(scope))
	if clean == "" {
		return false
	}
	// 裸形态（缺 <user> 前缀）不是合法 scope，但仍划入评测遗留空间：
	// 遗留调用不得在此结晶，也不得被当成用户空间。
	if strings.HasPrefix(clean, strings.TrimPrefix(Marker, "-")) {
		return true
	}
	index := strings.LastIndex(clean, Marker)
	if index < 0 {
		return false
	}
	rest := clean[index+len(Marker):]
	for _, suite := range []string{SuiteLoCoMo, SuiteATMWire} {
		if rest == suite || strings.HasPrefix(rest, suite+"-") {
			return true
		}
	}
	return false
}

// Split 解析规范评测 scope：返回 (suite, pulseMode, generation, ok)。
// suite 取线名（`locomo` / `atmbench`），pulseMode 取 `pulse_cipher` / `pulse_plain` / `local_plain`。
// 形态闭集：
//   - <prefix>-mem-eval-<locomo|atmbench>[-<世代号>]              => pulse_cipher
//   - <prefix>-mem-eval-<locomo|atmbench>-pulse-plain[-<世代号>]  => pulse_plain
//   - <prefix>-mem-eval-<locomo|atmbench>-local-plain[-<世代号>]  => local_plain
//
// 含空白或 `:` 的 scope（引擎落库形态）一律不解析：它们不是一个可用于建引擎的规范 scope。
func Split(scope string) (string, string, string, bool) {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" || len(scope) > MaxScopeLen || strings.ContainsAny(scope, " \t\r\n:") {
		return "", "", "", false
	}
	index := strings.LastIndex(scope, Marker)
	if index <= 0 {
		return "", "", "", false
	}
	segments := strings.Split(scope[index+len(Marker):], "-")
	if segments[0] == "" {
		return "", "", "", false
	}
	suite := ""
	switch segments[0] {
	case SuiteLoCoMo:
		suite = SuiteLoCoMo
	case SuiteATMWire:
		suite = SuiteATMWire
	default:
		return "", "", "", false
	}
	for _, segment := range segments[1:] {
		if segment == "" {
			return "", "", "", false
		}
	}
	pulseMode := ModePulseCipher
	genSegments := segments[1:]
	if len(genSegments) >= 2 && genSegments[0] == "pulse" && genSegments[1] == "plain" {
		pulseMode = ModePulsePlain
		genSegments = genSegments[2:]
	} else if len(genSegments) >= 2 && genSegments[0] == "pulse" && genSegments[1] == "cipher" {
		pulseMode = ModePulseCipher
		genSegments = genSegments[2:]
	} else if len(genSegments) >= 2 && genSegments[0] == "local" && genSegments[1] == "plain" {
		pulseMode = ModeLocalPlain
		genSegments = genSegments[2:]
	}
	return suite, pulseMode, strings.Join(genSegments, "-"), true
}
