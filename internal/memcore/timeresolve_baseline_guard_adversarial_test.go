// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

// 时间解算基准守卫对抗（防止无基准退化为运行日）：
// 当调用方未提供源轮基准时刻（turnOccurredAt <= 0，如内联子断言/Tool 轮未绑定源轮发生时刻）时，
// 相对时间词（今天/昨天/上周…）**严禁退化为墙钟当日**——那会把「记忆写入日」冒充成「事件发生日」，
// 使 as-of 时间旅行检索在错误的时间轴上命中该断言（实测：基准 2025-04-30 的源轮被写成 tev=2026-11-02 运行日）。
// 正确口径：无基准 ⇒ 解不出 ⇒ 返回 0（宁缺勿造，与全仓「不猜」纪律一致）。

import (
	"testing"
	"time"
)

func TestResolveAssertionTimeNoBaselineMustNotFallBackToWallClock(t *testing.T) {
	wallNow := time.Now().UTC()
	wallDay := time.Date(wallNow.Year(), wallNow.Month(), wallNow.Day(), 0, 0, 0, 0, time.UTC).Unix()

	relativeWords := []string{"今天", "昨天", "今天早上", "上周", "上个月", "刚才"}
	for _, w := range relativeWords {
		got, kind := ResolveAssertionTime("", "", w+"我把事情办完了", 0)
		if got == wallDay {
			t.Errorf("相对词 %q 在无基准时退化为墙钟当日 (%s) ⇒ 会把记忆写入日冒充事件发生日", w, time.Unix(got, 0).UTC().Format("2006-01-02"))
		}
		if got > 0 {
			t.Errorf("相对词 %q 在无基准 (turnOccurredAt=0) 时应返回 0（不猜），实际 got=%d kind=%q", w, got, kind)
		}
	}

	// 对照：提供基准时相对词必须精确解算（不得被本守卫误伤）
	base := time.Date(2025, 4, 30, 10, 0, 0, 0, time.UTC).Unix()
	if got, kind := ResolveAssertionTime("", "", "今天终于把定理搞明白了", base); got != time.Date(2025, 4, 30, 0, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("有基准时「今天」应解为基准当日 2025-04-30，实际 got=%d (%s) kind=%q",
			got, time.Unix(got, 0).UTC().Format("2006-01-02"), kind)
	}
	if got, _ := ResolveAssertionTime("", "", "昨天提交了离职申请", base); got != time.Date(2025, 4, 29, 0, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("有基准时「昨天」应解为 2025-04-29，实际 got=%d (%s)",
			got, time.Unix(got, 0).UTC().Format("2006-01-02"))
	}

	// 显式绝对日期在有/无基准下都必须精确（不受本守卫影响）
	if got, _ := ResolveAssertionTime("", "", "他 2023-08-15 加入了公司", 0); got != time.Date(2023, 8, 15, 0, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("显式绝对日期在无基准下也应精确解出，实际 got=%d", got)
	}

	// 年阶段 DSL (YYYY-late/end) 与裸 4 位量纲数值不劫持相对月份基准（治 CR16 / CR25 1999-12-01）
	jun2026 := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC).Unix()
	if got, kind := ResolveAssertionTime("2026-late", "", "年底就会量产", jun2026); got != time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC).Unix() || kind != "explicit" {
		t.Errorf("2026-late 应解出 2026-12-01/explicit，实际 got=%s kind=%q", time.Unix(got, 0).UTC().Format("2006-01-02"), kind)
	}
	if got, kind := ResolveAssertionTime("this:year-end", "", "年底就会量产", jun2026); got != time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC).Unix() || kind != "relative_resolved" {
		t.Errorf("this:year-end 应解出 2026-12-01/relative_resolved，实际 got=%s kind=%q", time.Unix(got, 0).UTC().Format("2006-01-02"), kind)
	}
	if got, kind := ResolveAssertionTime("2000-01-01 -1m", "", "She raised the gateway rate limit from 2000 to 5000 last month, right?", jun2026); got != time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).Unix() || kind != "relative_resolved" {
		t.Errorf("from 2000 to 5000 last month @ 2026-06-21 不应被量纲 2000 劫持为 1999-12-01，期望 2026-05-01，实际 got=%s kind=%q", time.Unix(got, 0).UTC().Format("2006-01-02"), kind)
	}
}
