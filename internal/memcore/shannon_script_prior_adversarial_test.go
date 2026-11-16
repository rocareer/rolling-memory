// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore_test

// 多脚本香农先验行为锁：先验表在「非拉丁非 CJK」脚本上的证据强度
// 明显弱于 Han/Latin/Hangul —— 泰/老/高棉/缅/藏统一取 4.2 bits（按「音节文字 ≈ 假名」
// 类比外推，尚无同等规模语料标定）。本文件把这些先验**固化成可执行断言**：
//   S1 逐族先验值与 8.0 bits 原子概念门的关系（1 Han / 2 Kana / 2 Hangul / 4 Latin 过门；
//      1 音节字符 / 2-3 Latin 不过门）；
//   S2 洪泛坍缩：单字长重复走 Markov 阻尼、周期 2 串联重复走周期坍缩，各族一律压到原子门以下；
//   S3 零宽格式符（Cf）在所有脚本中零贡献且不改变其余语义；
//   S4 全语料无非有限值（NaN/Inf）。
//
// 这不是「证明先验正确」——它是**证据薄弱区的行为锁**：任何调参或改写都必须同步更新本文件，
// 并在 docs/evidence 留新读数；禁止在没有跨语料证据时静默改数。

import (
	"math"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/memcore"
)

func TestShannonScriptPriorEvidenceMatrix(t *testing.T) {
	const tau = memcore.AtomicConceptEntropyBits

	t.Run("S1_per_script_priors_and_tau_alignment", func(t *testing.T) {
		cases := []struct {
			name  string
			r     rune
			prior float64
		}{
			{"Han", '数', 10.0},
			{"Hangul", '공', 7.0},
			{"Hiragana", 'べ', 4.2},
			{"Katakana", 'テ', 4.2},
			{"Thai", 'ก', 4.2},
			{"Lao", 'ກ', 4.2},
			{"Khmer", 'ក', 4.2},
			{"Myanmar", 'က', 4.2},
			{"Tibetan", 'ཀ', 4.2},
			{"LatinLower", 'a', 2.0},
			{"LatinUpper", 'A', 2.0},
			{"Cyrillic", 'П', 2.0},
			{"Arabic", 'م', 2.0},
			{"Digit", '7', 3.32},
			{"Space", ' ', 0.0},
			{"ZeroWidthSpace", '\u200b', 0.0},
			{"CombiningAcute", '\u0301', 0.5},
		}
		for _, c := range cases {
			if got := memcore.RuneShannonBits(c.r); got != c.prior {
				t.Fatalf("%s prior changed: got %v want %v (证据薄弱区调参必须同步更新本测试与 docs/evidence)", c.name, got, c.prior)
			}
		}

		// 跨脚本等熵对齐（这是 8.0 门槛成立的唯一依据）。
		aligned := []struct {
			name string
			s    string
			pass bool
		}{
			{"1 Han", "数", true},
			{"2 Kana", "食べ", true},
			{"1 Kana", "べ", false},
			{"2 Hangul", "공부", true},
			{"1 Hangul", "공", false},
			{"4 Latin", "dance", true},
			{"2 Latin", "in", false},
			{"3 Latin", "art", false},
			{"2 Thai", "กา", true},
			{"1 Thai", "ก", false},
		}
		for _, c := range aligned {
			bits := memcore.StringShannonBits(c.s)
			if (bits >= tau) != c.pass {
				t.Fatalf("%s: bits=%v tau=%v, want pass=%v", c.name, bits, tau, c.pass)
			}
		}
	})

	t.Run("S2_flood_collapse_across_scripts", func(t *testing.T) {
		scripts := []struct {
			name      string
			ch        string
			unit      float64
			tandemAlt string
		}{
			{"Han", "哈", 10.0, "嘿"},
			{"Hangul", "공", 7.0, "부"},
			{"Kana", "べ", 4.2, "る"},
			{"Thai", "ก", 4.2, "า"},
			{"Lao", "ກ", 4.2, "າ"},
			{"Khmer", "ក", 4.2, "ខ"},
			{"Myanmar", "က", 4.2, "ခ"},
			{"Tibetan", "ཀ", 4.2, "ཁ"},
			{"Latin", "a", 2.0, "b"},
		}
		for _, sc := range scripts {
			// 单字长重复：一阶 Markov 阻尼只削掉「重复带来的冗余」，不把信息量归零
			// （200 个同字仍是一个概念的多份拷贝）。不变量 = 远低于线性累加且有限。
			single := strings.Repeat(sc.ch, 200)
			got := memcore.StringShannonBits(single)
			linear := 200 * sc.unit
			if math.IsNaN(got) || math.IsInf(got, 0) {
				t.Fatalf("%s single-rune flood non-finite: %v", sc.name, got)
			}
			// 0 是合法的「完全串联坍缩」结果（rank 版无地板时 200 连写会精确归零）；
			// 不变量是「远低于线性累加」，不是「必须为正」。
			if got < 0 || got >= linear/4 {
				t.Fatalf("%s single-rune flood not collapsed: bits=%v linear=%v", sc.name, got, linear)
			}

			// 周期 2 串联重复（无空白）必须走周期坍缩并落到原子门以下——
			// 这正是「按字符种数判退化」被取代后仍能拦住 "abab..." / "กขกข..." 的机制。
			tandem := strings.Repeat(sc.ch+sc.tandemAlt, 100)
			if tb := memcore.StringShannonBits(tandem); tb >= tau {
				t.Fatalf("%s tandem flood must collapse below atomic gate %v: got %v", sc.name, tau, tb)
			}
		}
	})

	t.Run("S3_zero_width_formatters_are_transparent", func(t *testing.T) {
		pairs := [][2]string{
			{"数据", "数\u200b据"},
			{"食べる", "食\u200dべる"},
			{"공부", "공\u2060부"},
			{"ภาษาไทย", "ภาษา\u200bไทย"},
			{"dance", "dan\u200bce"},
		}
		for _, p := range pairs {
			if a, b := memcore.StringShannonBits(p[0]), memcore.StringShannonBits(p[1]); a != b {
				t.Fatalf("zero-width formatter changed bits for %q: clean=%v injected=%v", p[0], a, b)
			}
		}
	})

	t.Run("S4_no_non_finite_values", func(t *testing.T) {
		corpus := []string{
			"", " ", "\u200b", "a", "数", "공", "ก", "ກ", "ក", "က", "ཀ",
			strings.Repeat("ก", 4096),
			strings.Repeat("ཀ་", 2048),
			strings.Repeat("a", 10000),
			strings.Repeat("数", 10000),
			"😀😀😀",
		}
		for _, s := range corpus {
			v := memcore.StringShannonBits(s)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("non-finite bits on %q (len=%d): %v", s, len(s), v)
			}
			if v < 0 {
				t.Fatalf("negative bits on %q: %v", s, v)
			}
		}
	})
}
