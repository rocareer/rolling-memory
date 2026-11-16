package host

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 版本域语法（v1 闭集，manifest 的 host_api 与 framework_req 同口径）：
//   - 空格或逗号分隔的比较子序列，全部满足才算命中（与语义）；
//   - 比较子形如 ">=1 <2"、">=1.0.0"、"1.2.3"（裸版本视为 =）；
//   - 段数不足按 0 补齐（">=1" 等价 ">=1.0.0"）；
//   - 不支持 "||"（或语义）、"^" / "~"（插入语义）与预发布标签（"1.0.0-beta"）——一律拒解析（fail-closed）。

// semver 三段版本号（major.minor.patch）
type semver [3]int

// parseSemver 解析 X[.Y[.Z]]：允许前缀 v，段数最多 3，各段为非负整数
func parseSemver(s string) (semver, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "v"))
	if s == "" {
		return semver{}, errors.New("版本号为空")
	}
	segments := strings.Split(s, ".")
	if len(segments) > 3 {
		return semver{}, fmt.Errorf("版本号 %q 段数超过 3", s)
	}
	var version semver
	for i, seg := range segments {
		n, err := strconv.Atoi(seg)
		if err != nil || n < 0 {
			return semver{}, fmt.Errorf("版本号 %q 的第 %d 段 %q 不是非负整数", s, i+1, seg)
		}
		version[i] = n
	}
	return version, nil
}

// compareSemver 比较两个版本号：a<b 返回 -1，相等返回 0，a>b 返回 1
func compareSemver(a, b semver) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// InRange 判断 version 是否落在 domain 描述的版本域内（域语法见文件头注释）
func InRange(domain, version string) (bool, error) {
	constraints, err := parseDomain(domain)
	if err != nil {
		return false, err
	}
	v, err := parseSemver(version)
	if err != nil {
		return false, err
	}
	for _, c := range constraints {
		if !c.match(v) {
			return false, nil
		}
	}
	return true, nil
}

// Compatible 校验 manifest 的 host_api 域是否与本进程的机制接口版本（APIVersion）相容
// （硬校验的是机制接口版本常量）。返回的错误可直接展示给安装者。
func Compatible(hostAPI string) error {
	ok, err := InRange(hostAPI, APIVersion)
	if err != nil {
		return fmt.Errorf("host_api 域 %q 无法解析: %w", hostAPI, err)
	}
	if !ok {
		return fmt.Errorf("host_api 域 %q 与本宿主机制接口版本 %s 不相容", hostAPI, APIVersion)
	}
	return nil
}

// constraint 一个比较子
type constraint struct {
	op      string // >= | > | <= | < | =
	version semver
}

// match 判断版本是否满足本比较子
func (c constraint) match(v semver) bool {
	cmp := compareSemver(v, c.version)
	switch c.op {
	case ">=":
		return cmp >= 0
	case ">":
		return cmp > 0
	case "<=":
		return cmp <= 0
	case "<":
		return cmp < 0
	default: // =
		return cmp == 0
	}
}

// parseDomain 解析版本域为比较子序列（空格/逗号分隔，与语义）
func parseDomain(domain string) ([]constraint, error) {
	if strings.TrimSpace(domain) == "" {
		return nil, errors.New("版本域为空")
	}
	if strings.Contains(domain, "||") {
		return nil, errors.New("不支持 || 语法（v1 只支持与语义的比较子序列）")
	}
	tokens := strings.FieldsFunc(domain, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t' || r == '\n'
	})
	if len(tokens) == 0 {
		return nil, errors.New("版本域为空")
	}
	out := make([]constraint, 0, len(tokens))
	for _, token := range tokens {
		if strings.HasPrefix(token, "^") || strings.HasPrefix(token, "~") {
			return nil, fmt.Errorf("比较子 %q 不支持：v1 只支持 >= > <= < = 与裸版本", token)
		}
		op, rest := splitOperator(token)
		if rest == "" {
			return nil, fmt.Errorf("比较子 %q 缺少版本号", token)
		}
		v, err := parseSemver(rest)
		if err != nil {
			return nil, fmt.Errorf("比较子 %q: %w", token, err)
		}
		out = append(out, constraint{op: op, version: v})
	}
	return out, nil
}

// splitOperator 拆出比较子前缀运算符；裸版本视为 =
func splitOperator(token string) (op, rest string) {
	for _, candidate := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(token, candidate) {
			return candidate, strings.TrimSpace(strings.TrimPrefix(token, candidate))
		}
	}
	return "=", token
}
