// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

// 时间精度契约（架构契约）：t_event / t_valid_from / t_valid_to / t_source / t_record 等
// 全链时间列以 bigint Unix **秒**为语义时基，「至少到秒」是硬下限——凡源文本携带时分秒者，
// 解算结果必须逐秒还原（不得截到日/小时/分钟）；仅当源文本本身只给日期/月份/年份时，
// 才允许落对应粒度的起点（日=当日 00:00:00，月=月首，年=年首），此即「精度=源文本精度」。
// 亚秒（小数秒）当前按秒向下取整（storage = 秒级 bigint），未来高精度扩展见
// 记忆系统时间精度契约（新增 *_ns 列而非改列单位）。

import (
	"testing"
	"time"
)

func TestTimePrecisionContractSecondsMinimum(t *testing.T) {
	ref := time.Date(2025, 4, 30, 10, 15, 30, 0, time.UTC)
	refUnix := ref.Unix()
	utc := func(y int, mo time.Month, d, hh, mm, ss int) int64 {
		return time.Date(y, mo, d, hh, mm, ss, 0, time.UTC).Unix()
	}

	cases := []struct {
		name    string
		expr    string
		text    string
		want    int64
		wantTag string
	}{
		// ① 显式 ISO 日期+时分秒：必须秒级还原
		{"iso-datetime-seconds", "2023-08-15T14:30:25Z", "", utc(2023, time.August, 15, 14, 30, 25), "explicit"},
		{"iso-datetime-no-tz", "2023-08-15 14:30:25", "", utc(2023, time.August, 15, 14, 30, 25), "explicit"},
		// ② 显式日期+中文时分秒（含午后 meridiem）
		{"cn-date-clock", "2023年8月15日 下午3点20分15秒", "2023年8月15日 下午3点20分15秒到了", utc(2023, time.August, 15, 15, 20, 15), "explicit"},
		// ③ 显式日期+英文月名+时分秒（PM）
		{"en-mdy-clock", "August 15, 2023 at 3:20:15 PM", "August 15, 2023 at 3:20:15 PM", utc(2023, time.August, 15, 15, 20, 15), "explicit"},
		// ④ 相对日 + 日内时钟：日锚点保留，时钟逐秒附着
		{"en-relative-day-clock", "", "yesterday at 14:30:25 he called", utc(2025, time.April, 29, 14, 30, 25), "relative_resolved"},
		{"cn-relative-day-clock", "", "昨天下午3点20分15秒他打来电话", utc(2025, time.April, 29, 15, 20, 15), "relative_resolved"},
		// ⑤ 亚日级相对：秒/分钟/小时以完整秒级基准精确偏移
		{"relative-seconds", "", "45 seconds ago the sensor tripped", refUnix - 45, "relative_resolved"},
		{"relative-minutes", "", "30 minutes ago the alarm fired", refUnix - 1800, "relative_resolved"},
		{"relative-hours", "", "2 hours ago the alarm fired", refUnix - 7200, "relative_resolved"},
		// ⑥ 源文本只给日期/月份：允许对应粒度起点（精度=源文本精度）
		{"date-only", "", "he joined on 2023-08-15", utc(2023, time.August, 15, 0, 0, 0), "explicit"},
		{"month-only", "2023-08", "", utc(2023, time.August, 1, 0, 0, 0), "explicit"},
	}

	for _, c := range cases {
		got, kind := ResolveAssertionTime(c.expr, c.text, c.text, refUnix)
		if got != c.want {
			t.Errorf("%s: got=%d (%s) want=%d (%s)", c.name, got,
				time.Unix(maxInt64(got, 0), 0).UTC().Format("2006-01-02 15:04:05"), c.want,
				time.Unix(c.want, 0).UTC().Format("2006-01-02 15:04:05"))
			continue
		}
		if c.wantTag != "" && kind != c.wantTag {
			t.Errorf("%s: kind=%q want=%q", c.name, kind, c.wantTag)
		}
	}
}

// TestTimePrecisionContractSubsecondFloor 亚秒输入按秒向下取整（当前 storage = 秒级 bigint）：
// 该行为是显式契约，未来高精度扩展只允许新增 *_ns 列，不得改变既有秒级列单位。
func TestTimePrecisionContractSubsecondFloor(t *testing.T) {
	got, _ := ResolveAssertionTime("2023-08-15T14:30:25.987654Z", "", "", 0)
	want := time.Date(2023, time.August, 15, 14, 30, 25, 0, time.UTC).Unix()
	if got != want {
		t.Fatalf("小数秒应向下取整到秒：got=%d want=%d", got, want)
	}
}

// TestTimePrecisionContractNeverMinuteGranularity 反向守卫：凡源文本给出秒，结果不得恰为整分（:00 秒）冒充。
func TestTimePrecisionContractNeverMinuteGranularity(t *testing.T) {
	ref := time.Date(2025, 4, 30, 10, 15, 30, 0, time.UTC).Unix()
	for _, src := range []string{
		"yesterday at 14:30:25 he called",
		"45 seconds ago the sensor tripped",
		"2023-08-15T14:30:25Z",
	} {
		got, _ := ResolveAssertionTime("", src, src, ref)
		if got <= 0 {
			t.Fatalf("%q 应解出非零秒级时刻", src)
		}
		if got%60 == 0 && got != time.Date(2023, time.August, 15, 14, 30, 0, 0, time.UTC).Unix() {
			t.Errorf("%q 秒级信息被截断为整分：got=%d", src, got)
		}
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
